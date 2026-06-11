package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
)

// extractWorkers is the number of concurrent extract goroutines per stage.
// Not a top-level config knob (D-034 guardrail).
const extractWorkers = 4

// extractDownstreamCap is the CandidateBatch channel buffer size.
const extractDownstreamCap = 256

// extractMaxTokens is the model output token budget for the extract call.
const extractMaxTokens = 4096

// ExtractStage consumes FlushedBuffer events from the buffer stage and
// produces CandidateBatch events on its downstream channel.
//
// Phase 07 7-step flow per flush:
//  1. SkipPromotion check → emit extraction.skipped; return.
//  2. Hydrate records via RecordStore.Get.
//  3. Get active topics; if empty → emit extraction.skipped; return.
//  4. Build prompt (system template + topics + transcript, token-clamped).
//  5. gateway.Complete with CandidateSchema.
//  6. Per-candidate server-side validation (kind/range/provenance/span clamp).
//  7. Emit CandidateBatch on downstream + extraction.completed event.
//     Terminal gateway failure → dead-letter + extraction.failed event.
//
// Concurrency: extractWorkers goroutines share no mutable state; race-safe.
type ExtractStage struct {
	st      store.Store
	gw      gateway.Gateway
	svc     *topics.Service
	log     *slog.Logger
	profile string

	in  <-chan FlushedBuffer
	out chan CandidateBatch

	wg sync.WaitGroup
}

// NewExtractStage constructs an ExtractStage. Call Start to begin consuming.
// profile is used for default-pack selection and transcript token budgeting.
func NewExtractStage(
	st store.Store,
	gw gateway.Gateway,
	svc *topics.Service,
	log *slog.Logger,
	profile string,
	in <-chan FlushedBuffer,
) *ExtractStage {
	return &ExtractStage{
		st:      st,
		gw:      gw,
		svc:     svc,
		log:     log,
		profile: profile,
		in:      in,
		out:     make(chan CandidateBatch, extractDownstreamCap),
	}
}

// Downstream returns the read-end of the CandidateBatch channel.
// Phase 08 replaces the no-op consumer with reconciliation dispatch.
func (e *ExtractStage) Downstream() <-chan CandidateBatch { return e.out }

// Start launches extractWorkers goroutines. ctx is used only for logging.
func (e *ExtractStage) Start(ctx context.Context) {
	for i := 0; i < extractWorkers; i++ {
		e.wg.Add(1)
		go e.runWorker(ctx)
	}
}

// Drain waits for all workers to finish and then closes the downstream channel.
// Call after the buffer stage Downstream() channel (our in) has been closed.
func (e *ExtractStage) Drain(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		e.log.WarnContext(ctx, "extract: drain timed out")
	}
	close(e.out)
}

// ----------------------------------------------------------------------------
// worker
// ----------------------------------------------------------------------------

func (e *ExtractStage) runWorker(ctx context.Context) {
	defer e.wg.Done()
	for fb := range e.in {
		e.processFlush(ctx, fb)
	}
}

// processFlush runs the 7-step extraction flow for one FlushedBuffer.
func (e *ExtractStage) processFlush(ctx context.Context, fb FlushedBuffer) {
	// Step 1: SkipPromotion — branch-discard flushes are not extracted.
	if fb.SkipPromotion {
		e.emitEvent(ctx, fb.Scope, "extraction.skipped", fb.Key, "skip_promotion",
			map[string]interface{}{"reason": "skip_promotion", "buffer_key": fb.Key})
		return
	}

	// Step 2: Hydrate records.
	records, err := e.hydrateRecords(ctx, fb)
	if err != nil {
		e.log.WarnContext(ctx, "extract: hydrate records failed; dead-lettering",
			"buffer_key", fb.Key, "err", err)
		e.deadLetter(ctx, fb, fmt.Sprintf("hydrate records: %v", err))
		return
	}

	// Step 3: Get active topics; short-circuit on empty set (AC-2).
	activeTopics, err := e.svc.ActiveTopics(ctx, fb.Scope)
	if err != nil {
		e.log.WarnContext(ctx, "extract: get topics failed; dead-lettering",
			"buffer_key", fb.Key, "err", err)
		e.deadLetter(ctx, fb, fmt.Sprintf("get topics: %v", err))
		return
	}
	if len(activeTopics) == 0 {
		e.emitEvent(ctx, fb.Scope, "extraction.skipped", fb.Key, "no_topics",
			map[string]interface{}{"reason": "no_topics", "buffer_key": fb.Key})
		return
	}

	// Step 4: Build prompt.
	topicLines := topicViewsToLines(activeTopics)
	budget := tokenBudgetForProfile(e.profile)
	prompt := BuildPrompt(topicLines, records, budget)

	// Step 5: gateway.Complete.
	req := gateway.CompleteRequest{
		System: prompt.SystemPrompt,
		Messages: []gateway.Message{
			{Role: "user", Content: prompt.UserContent},
		},
		Schema:      CandidateSchema,
		MaxTokens:   extractMaxTokens,
		Temperature: 0.0,
	}
	resp, err := e.gw.Complete(ctx, req)
	if err != nil {
		reason := "gateway_failure"
		if strings.Contains(err.Error(), "unavailable") {
			reason = "gateway_unavailable"
		}
		e.log.WarnContext(ctx, "extract: gateway failure; dead-lettering",
			"buffer_key", fb.Key, "reason", reason, "err", err)
		e.deadLetter(ctx, fb, fmt.Sprintf("gateway: %v", err))
		e.emitEvent(ctx, fb.Scope, "extraction.failed", fb.Key, reason,
			map[string]interface{}{
				"reason":     reason,
				"buffer_key": fb.Key,
				"error":      err.Error(),
			})
		return
	}

	// Step 6: Parse + per-candidate validation.
	var list CandidateList
	if err := json.Unmarshal(resp.JSON, &list); err != nil {
		e.log.WarnContext(ctx, "extract: unmarshal candidates failed; dead-lettering",
			"buffer_key", fb.Key, "err", err)
		e.deadLetter(ctx, fb, fmt.Sprintf("unmarshal: %v", err))
		return
	}

	recordSet := make(map[string]bool, len(fb.RecordIDs))
	for _, id := range fb.RecordIDs {
		recordSet[id] = true
	}
	recordContents := make(map[string]string, len(records))
	for _, rec := range records {
		recordContents[rec.ID] = rec.Content
	}

	validated, dropped := ValidateCandidates(list.Candidates, recordSet, recordContents)

	// P3: stamp scope + branch from the flush onto the batch (not per-candidate).
	batch := CandidateBatch{
		Scope:      fb.Scope,
		BufferKey:  fb.Key,
		BranchID:   fb.BranchID,
		Candidates: validated,
	}

	// Step 7a: Emit CandidateBatch on downstream (non-blocking; drop if full).
	select {
	case e.out <- batch:
	default:
		e.log.WarnContext(ctx, "extract: downstream full; dropping batch",
			"buffer_key", fb.Key)
	}

	// Step 7b: Emit extraction.completed event with counts (AC-8).
	truncatedFlag := 0
	if prompt.Truncated {
		truncatedFlag = 1
	}
	e.emitEvent(ctx, fb.Scope, "extraction.completed", fb.Key, "",
		map[string]interface{}{
			"produced":   len(validated),
			"dropped":    dropped,
			"truncated":  truncatedFlag,
			"buffer_key": fb.Key,
		})
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// hydrateRecords fetches the full store.Record for each ID in the flush.
func (e *ExtractStage) hydrateRecords(ctx context.Context, fb FlushedBuffer) ([]store.Record, error) {
	records := make([]store.Record, 0, len(fb.RecordIDs))
	for _, id := range fb.RecordIDs {
		rec, err := e.st.Records().Get(ctx, fb.Scope, id)
		if err != nil {
			return nil, fmt.Errorf("get record %q: %w", id, err)
		}
		records = append(records, *rec)
	}
	return records, nil
}

// deadLetter writes a dead-letter record for the given flush descriptor.
// The flush descriptor JSON is the canonical payload (per spec).
func (e *ExtractStage) deadLetter(ctx context.Context, fb FlushedBuffer, errMsg string) {
	dl := store.DeadLetter{
		ID:        ulid.Make().String(),
		Stage:     "extract",
		ItemID:    fb.Key,
		Error:     errMsg,
		Attempts:  1,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := e.st.Ops().PutDeadLetter(ctx, dl); err != nil {
		e.log.WarnContext(ctx, "extract: put dead letter failed",
			"buffer_key", fb.Key, "err", err)
	}
}

// emitEvent writes an audit event to the EventStore.
func (e *ExtractStage) emitEvent(
	ctx context.Context,
	scope identity.Scope,
	evType, subjectID, reason string,
	payload map[string]interface{},
) {
	p, _ := json.Marshal(payload)
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      evType,
		SubjectID: subjectID,
		Reason:    reason,
		Payload:   string(p),
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := e.st.Events().Emit(ctx, scope, ev); err != nil {
		e.log.WarnContext(ctx, "extract: emit event failed", "type", evType, "err", err)
	}
}

// topicViewsToLines converts TopicViews to "key: description" strings.
func topicViewsToLines(views []topics.TopicView) []string {
	lines := make([]string, len(views))
	for i, v := range views {
		lines[i] = v.Key + ": " + v.Description
	}
	return lines
}

// ----------------------------------------------------------------------------
// candidate validation (exported for fuzz target)
// ----------------------------------------------------------------------------

// ValidateCandidates runs server-side per-candidate validation against the
// flush's record set, clamps provenance spans, and returns (valid, droppedCount).
//
// Validation rules (per spec):
//   - Empty content → drop.
//   - Unknown kind → drop.
//   - Importance not in [1,5] → drop.
//   - Confidence not in [0,1] → drop.
//   - No provenance entries → drop.
//   - Any provenance record_id not in recordSet → drop entire candidate.
//   - Spans are clamped to [0, len(content)] (not a drop reason).
func ValidateCandidates(
	candidates []Candidate,
	recordSet map[string]bool,
	recordContents map[string]string,
) ([]Candidate, int) {
	valid := make([]Candidate, 0, len(candidates))
	dropped := 0
	for _, c := range candidates {
		if !isValidCandidate(c, recordSet) {
			dropped++
			continue
		}
		// Clamp provenance spans to record content length.
		clampedProv := make([]ProvSpan, len(c.Provenance))
		for i, p := range c.Provenance {
			maxLen := len(recordContents[p.RecordID])
			start := clampInt(p.SpanStart, 0, maxLen)
			end := clampInt(p.SpanEnd, 0, maxLen)
			if start > end {
				end = start
			}
			clampedProv[i] = ProvSpan{
				RecordID:  p.RecordID,
				SpanStart: start,
				SpanEnd:   end,
			}
		}
		c.Provenance = clampedProv
		valid = append(valid, c)
	}
	return valid, dropped
}

// isValidCandidate returns true if the candidate passes all server-side checks.
func isValidCandidate(c Candidate, recordSet map[string]bool) bool {
	if strings.TrimSpace(c.Content) == "" {
		return false
	}
	if !ValidKinds[c.Kind] {
		return false
	}
	if c.Importance < 1 || c.Importance > 5 {
		return false
	}
	if c.Confidence < 0 || c.Confidence > 1 {
		return false
	}
	if len(c.Provenance) == 0 {
		return false
	}
	for _, p := range c.Provenance {
		if !recordSet[p.RecordID] {
			return false
		}
	}
	return true
}

// clampInt returns v clamped to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
