package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

const (
	// reconcileWorkers is the number of concurrent reconcile goroutines.
	reconcileWorkers = 4

	// decisionMaxTokens is the model output token budget for the decision call.
	decisionMaxTokens = 512

	// nearDupThreshold is the bigram-Jaccard similarity at or above which a
	// candidate is treated as the same fact as a retrieved neighbor and
	// discarded without an LLM call (D-044). This constant is the profile knob
	// boundary documented here; eval data should inform any adjustment.
	nearDupThreshold = 0.85

	// neighborLimit is the default maximum number of neighbors returned by
	// FindNeighbors for the decision context window.
	neighborLimit = 8
)

// ReconcileStage consumes CandidateBatch events from the extract stage and
// reconciles each candidate against the memory store using an 8-step flow.
// It is safe for concurrent use after Start is called.
type ReconcileStage struct {
	mem  store.MemoryStore
	ops  store.OpsStore
	evts store.EventStore
	gw   gateway.Gateway
	log  *slog.Logger
	in   <-chan pipeline.CandidateBatch
	wg   sync.WaitGroup
}

// New creates a ReconcileStage wired to the given dependencies.
// in is the read-end of the CandidateBatch channel produced by the extract stage.
func New(
	mem store.MemoryStore,
	ops store.OpsStore,
	evts store.EventStore,
	gw gateway.Gateway,
	log *slog.Logger,
	in <-chan pipeline.CandidateBatch,
) *ReconcileStage {
	return &ReconcileStage{
		mem:  mem,
		ops:  ops,
		evts: evts,
		gw:   gw,
		log:  log.With("stage", "reconcile"),
		in:   in,
	}
}

// Start launches reconcileWorkers goroutines that consume CandidateBatch events.
func (r *ReconcileStage) Start(ctx context.Context) {
	for i := 0; i < reconcileWorkers; i++ {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.worker(ctx)
		}()
	}
}

// Drain blocks until all in-flight work is complete and the upstream channel
// is closed. Call after the extract stage has been drained.
func (r *ReconcileStage) Drain(_ context.Context) {
	r.wg.Wait()
}

// worker processes CandidateBatch events until the input channel is closed.
func (r *ReconcileStage) worker(ctx context.Context) {
	for batch := range r.in {
		for _, c := range batch.Candidates {
			if err := r.processCandidate(ctx, batch.Scope, c); err != nil {
				r.log.WarnContext(ctx, "reconcile: candidate failed",
					"scope", batch.Scope.Tenant,
					"kind", c.Kind,
					"err", err,
				)
				// Dead-letter the failure so operators can investigate.
				_ = r.ops.PutDeadLetter(ctx, store.DeadLetter{
					Stage:    "reconcile",
					ItemID:   batch.BufferKey,
					Error:    err.Error(),
					Attempts: 1,
				})
			}
		}
	}
}

// processCandidate executes the 8-step reconciliation flow for one candidate.
//
//  1. Normalize content + compute SHA-256 hash (D-045 content normalization:
//     trim + collapse whitespace; case preserved — case changes meaning).
//  2. Exact-dedup check (GetByContentHash); hit → IncrementCounter(match) +
//     reconcile.dedup_exact event + discard. No LLM call.
//  3. FindNeighbors (structural overlap via entity/keyword junctions).
//  4. Near-dup pre-filter: bigram-Jaccard ≥ nearDupThreshold against any
//     neighbor → IncrementCounter(match) + reconcile.dedup_near + discard (D-044).
//  5. Fast-add path: zero neighbors → commit as active add. No LLM call (D-044).
//  6. Gateway decision call (schema-constrained JSON).
//  7. Validate decision: target_ids ⊆ shown neighbors; non-members degrade to add.
//  8. Trust gate (supersede/update on high-trust TARGET); commit CommitSet.
func (r *ReconcileStage) processCandidate(ctx context.Context, scope identity.Scope, c pipeline.Candidate) error {
	// Step 1: Normalize content and compute hash.
	normalized := NormalizeContent(c.Content)
	if normalized == "" {
		return r.commitDiscard(ctx, scope, "", "empty content after normalization")
	}
	hash := ContentHash(normalized)

	// Step 2: Exact-dedup check.
	existing, err := r.mem.GetByContentHash(ctx, scope, hash)
	if err == nil {
		// Exact duplicate: bump match_count and emit a dedup event.
		_ = r.mem.IncrementCounter(ctx, scope, existing.ID, "match")
		r.log.DebugContext(ctx, "reconcile: exact dup discarded",
			"tenant", scope.Tenant, "hash", hash, "existing_id", existing.ID)
		return r.commitExactDupDiscard(ctx, scope, existing.ID, "exact duplicate")
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("reconcile: GetByContentHash: %w", err)
	}

	// Step 3: Find structural neighbors.
	neighbors, err := r.mem.FindNeighbors(ctx, scope, store.NeighborQuery{
		Entities: c.Entities,
		Keywords: c.Keywords,
		Limit:    neighborLimit,
	})
	if err != nil {
		return fmt.Errorf("reconcile: FindNeighbors: %w", err)
	}

	// Step 4: Near-dup pre-filter (D-044).
	// bigram-Jaccard ≥ nearDupThreshold against any retrieved neighbor treats
	// the candidate as the same fact: bump match_count and discard.
	for _, n := range neighbors {
		if BigramJaccard(normalized, n.Content) >= nearDupThreshold {
			_ = r.mem.IncrementCounter(ctx, scope, n.ID, "match")
			r.log.DebugContext(ctx, "reconcile: near dup discarded",
				"tenant", scope.Tenant, "neighbor_id", n.ID)
			return r.commitNearDupDiscard(ctx, scope, n.ID, "near-duplicate (bigram-Jaccard ≥ threshold)")
		}
	}

	// Step 5: Fast-add path (D-044).
	// Zero neighbors after near-dup filtering means there is nothing to
	// reconcile against. Commit directly as an active add without calling the
	// gateway — the common first-write case for a fresh scope costs nothing.
	if len(neighbors) == 0 {
		return r.commitFastAdd(ctx, scope, c, normalized, hash)
	}

	// Step 6: Build LLM prompt and call gateway.
	systemPrompt := BuildSystemPrompt()
	userPrompt := BuildUserPrompt(c, neighbors)

	resp, err := r.gw.Complete(ctx, gateway.CompleteRequest{
		System:      systemPrompt,
		Messages:    []gateway.Message{{Role: "user", Content: userPrompt}},
		Schema:      DecisionSchema,
		MaxTokens:   decisionMaxTokens,
		Temperature: 0,
	})
	if err != nil {
		return fmt.Errorf("reconcile: gateway.Complete: %w", err)
	}

	// Step 7: Parse and validate decision.
	decision, err := parseDecision(resp.JSON)
	if err != nil {
		// Malformed decision: degrade to add so the candidate is not lost.
		r.log.WarnContext(ctx, "reconcile: invalid decision, degrading to add", "err", err)
		return r.commitFastAdd(ctx, scope, c, normalized, hash)
	}

	// Enforce target_ids ⊆ shown neighbors (D-045 safety net).
	// The model must never touch a memory it was not shown; any out-of-set
	// target degrades the entire decision to add.
	neighborSet := make(map[string]bool, len(neighbors))
	for _, n := range neighbors {
		neighborSet[n.ID] = true
	}
	for _, tid := range decision.TargetIDs {
		if !neighborSet[tid] {
			r.log.WarnContext(ctx, "reconcile: decision targets unseen memory, degrading to add",
				"target_id", tid)
			decision = DecisionOutput{Action: "add", Reason: "target_id not in shown neighbors; degraded to add"}
			break
		}
	}
	// Filter links to only those targeting shown neighbors.
	var validLinks []DecisionLink
	for _, dl := range decision.Links {
		if neighborSet[dl.TargetID] {
			validLinks = append(validLinks, dl)
		}
	}
	decision.Links = validLinks

	// Step 8: Apply trust gate and commit.
	return r.commit(ctx, scope, c, normalized, hash, neighbors, decision)
}

// commit applies the trust gate (for supersede/update) and executes the
// CommitSet for the reconciliation decision.
func (r *ReconcileStage) commit(
	ctx context.Context,
	scope identity.Scope,
	c pipeline.Candidate,
	normalized, hash string,
	neighbors []store.Memory,
	d DecisionOutput,
) error {
	action := store.ReconcileAction(d.Action)
	now := nowMs()

	switch action {
	case store.ActionAdd:
		mem := candidateToMemory(c, normalized, hash, "active")
		cs := store.CommitSet{
			Action:   store.ActionAdd,
			Memory:   mem,
			Entities: c.Entities,
			Keywords: c.Keywords,
			Queries:  c.AnticipatedQueries,
			Links:    decisionLinksToStore(mem.ID, d.Links),
			Events: []store.Event{
				buildEvent("memory.added", mem.ID, d.Reason, now),
			},
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		return r.mem.Commit(ctx, scope, cs)

	case store.ActionPark:
		mem := candidateToMemory(c, normalized, hash, "pending_confirmation")
		cs := store.CommitSet{
			Action:   store.ActionPark,
			Memory:   mem,
			Entities: c.Entities,
			Keywords: c.Keywords,
			Queries:  c.AnticipatedQueries,
			Events: []store.Event{
				buildEvent("memory.parked", mem.ID, d.Reason, now),
			},
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		return r.mem.Commit(ctx, scope, cs)

	case store.ActionDiscard:
		return r.commitDiscard(ctx, scope, firstTargetID(d.TargetIDs), d.Reason)

	case store.ActionUpdate:
		target := findNeighborByID(neighbors, d.TargetIDs[0])

		// Trust gate: high-trust targets cannot be silently updated (D-044).
		level := targetTrustLevel(target)
		if level == TrustLevelHigh {
			// Park the incoming memory as pending_confirmation; target stays active.
			mem := candidateToMemory(c, normalized, hash, "pending_confirmation")
			mem.SupersedesID = target.ID
			cs := store.CommitSet{
				Action:   store.ActionPark,
				Memory:   mem,
				Entities: c.Entities,
				Keywords: c.Keywords,
				Queries:  c.AnticipatedQueries,
				Targets:  []store.Memory{target},
				Events: []store.Event{
					buildEventWithPayload("memory.parked", mem.ID,
						"trust gate: target trust ≥ park threshold; pending human review",
						MarshalPriorState(target), now),
				},
			}
			cs.Provenance = buildProvenance(mem.ID, c.Provenance)
			return r.mem.Commit(ctx, scope, cs)
		}

		mem := candidateToMemory(c, normalized, hash, "active")
		mem.ID = target.ID // reuse existing memory ID
		events := []store.Event{
			buildEventWithPayload("memory.updated", mem.ID, d.Reason, MarshalPriorState(target), now),
		}
		if level == TrustLevelMedium {
			events = append(events, buildEvent("reconcile.warned", mem.ID,
				"trust gate: medium-trust target updated; review recommended", now))
		}
		cs := store.CommitSet{
			Action:   store.ActionUpdate,
			Memory:   mem,
			Entities: c.Entities,
			Keywords: c.Keywords,
			Queries:  c.AnticipatedQueries,
			Targets:  []store.Memory{target},
			Links:    decisionLinksToStore(mem.ID, d.Links),
			Events:   events,
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		return r.mem.Commit(ctx, scope, cs)

	case store.ActionSupersede:
		target := findNeighborByID(neighbors, d.TargetIDs[0])

		// Trust gate: high-trust targets cannot be silently superseded (D-044).
		level := targetTrustLevel(target)
		if level == TrustLevelHigh {
			// Park the incoming memory as pending_confirmation; target stays active.
			mem := candidateToMemory(c, normalized, hash, "pending_confirmation")
			mem.SupersedesID = target.ID
			cs := store.CommitSet{
				Action:   store.ActionPark,
				Memory:   mem,
				Entities: c.Entities,
				Keywords: c.Keywords,
				Queries:  c.AnticipatedQueries,
				Targets:  []store.Memory{target},
				Events: []store.Event{
					buildEventWithPayload("memory.parked", mem.ID,
						"trust gate: target trust ≥ park threshold; pending human review",
						MarshalPriorState(target), now),
				},
			}
			cs.Provenance = buildProvenance(mem.ID, c.Provenance)
			return r.mem.Commit(ctx, scope, cs)
		}

		// Apply contradiction boost: corrections outrank what they correct (D-044).
		// importance = max(candidate.Importance, contradictionBoostImportanceFloor = 4)
		// stability += contradictionBoostStabilityDelta (~45 days normalised)
		mem := candidateToMemory(c, normalized, hash, "active")
		mem.SupersedesID = target.ID
		applyContradictionBoost(&mem, c.Importance)

		// Automatic contradicts link plus any explicit decision links.
		links := []store.Link{
			{
				ID:         ulid.Make().String(),
				FromMemory: mem.ID,
				ToMemory:   target.ID,
				Type:       "contradicts",
				Source:     "reconciler",
				Confidence: 1.0,
			},
		}
		links = append(links, decisionLinksToStore(mem.ID, d.Links)...)

		events := []store.Event{
			buildEventWithPayload("memory.superseded", target.ID, d.Reason, MarshalPriorState(target), now),
			buildEvent("memory.added", mem.ID, "superseding memory added", now),
		}
		if level == TrustLevelMedium {
			events = append(events, buildEvent("reconcile.warned", target.ID,
				"trust gate: medium-trust target superseded; review recommended", now))
		}

		cs := store.CommitSet{
			Action:   store.ActionSupersede,
			Memory:   mem,
			Entities: c.Entities,
			Keywords: c.Keywords,
			Queries:  c.AnticipatedQueries,
			Targets:  []store.Memory{target},
			Links:    links,
			Events:   events,
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		return r.mem.Commit(ctx, scope, cs)

	case store.ActionMerge:
		targets := findNeighborsByIDs(neighbors, d.TargetIDs)
		mem := candidateToMemory(c, normalized, hash, "active")
		var events []store.Event
		for _, t := range targets {
			events = append(events, buildEventWithPayload("memory.merged", t.ID, d.Reason, MarshalPriorState(t), now))
		}
		events = append(events, buildEvent("memory.added", mem.ID, "merged memory added", now))
		cs := store.CommitSet{
			Action:   store.ActionMerge,
			Memory:   mem,
			Entities: c.Entities,
			Keywords: c.Keywords,
			Queries:  c.AnticipatedQueries,
			Targets:  targets,
			Links:    decisionLinksToStore(mem.ID, d.Links),
			Events:   events,
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		return r.mem.Commit(ctx, scope, cs)

	default:
		return fmt.Errorf("reconcile: unhandled action %q", action)
	}
}

// commitDiscard commits an ActionDiscard with a single event.
func (r *ReconcileStage) commitDiscard(ctx context.Context, scope identity.Scope, subjectID, reason string) error {
	cs := store.CommitSet{
		Action: store.ActionDiscard,
		Events: []store.Event{
			buildEvent("memory.discarded", subjectID, reason, nowMs()),
		},
	}
	return r.mem.Commit(ctx, scope, cs)
}

// commitExactDupDiscard commits an ActionDiscard for exact-hash duplicates,
// emitting a reconcile.dedup_exact event (D-044).
func (r *ReconcileStage) commitExactDupDiscard(ctx context.Context, scope identity.Scope, subjectID, reason string) error {
	cs := store.CommitSet{
		Action: store.ActionDiscard,
		Events: []store.Event{
			buildEvent("reconcile.dedup_exact", subjectID, reason, nowMs()),
		},
	}
	return r.mem.Commit(ctx, scope, cs)
}

// commitNearDupDiscard commits an ActionDiscard for near-duplicate candidates
// (bigram-Jaccard ≥ nearDupThreshold), emitting a reconcile.dedup_near event (D-044).
func (r *ReconcileStage) commitNearDupDiscard(ctx context.Context, scope identity.Scope, subjectID, reason string) error {
	cs := store.CommitSet{
		Action: store.ActionDiscard,
		Events: []store.Event{
			buildEvent("reconcile.dedup_near", subjectID, reason, nowMs()),
		},
	}
	return r.mem.Commit(ctx, scope, cs)
}

// commitFastAdd commits a new memory as active without a gateway call.
// Used for the fast-add path (zero neighbors) and as a safe fallback when
// the decision cannot be applied (D-044).
func (r *ReconcileStage) commitFastAdd(ctx context.Context, scope identity.Scope, c pipeline.Candidate, normalized, hash string) error {
	mem := candidateToMemory(c, normalized, hash, "active")
	cs := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   mem,
		Entities: c.Entities,
		Keywords: c.Keywords,
		Queries:  c.AnticipatedQueries,
		Events: []store.Event{
			buildEvent("memory.added", mem.ID, "fast-add: no neighbors found", nowMs()),
		},
	}
	cs.Provenance = buildProvenance(mem.ID, c.Provenance)
	return r.mem.Commit(ctx, scope, cs)
}

// --- helpers ----------------------------------------------------------------

func nowMs() int64 {
	return time.Now().UnixMilli()
}

func candidateToMemory(c pipeline.Candidate, normalized, hash, status string) store.Memory {
	return store.Memory{
		ID:          ulid.Make().String(),
		Kind:        c.Kind,
		Content:     normalized,
		Status:      status,
		Importance:  c.Importance,
		Confidence:  c.Confidence,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: hash,
	}
}

func buildProvenance(memID string, spans []pipeline.ProvSpan) []store.Provenance {
	out := make([]store.Provenance, len(spans))
	for i, sp := range spans {
		out[i] = store.Provenance{
			ID:        ulid.Make().String(),
			MemoryID:  memID,
			RecordID:  sp.RecordID,
			SpanStart: sp.SpanStart,
			SpanEnd:   sp.SpanEnd,
		}
	}
	return out
}

func buildEvent(typ, subjectID, reason string, tsMs int64) store.Event {
	return store.Event{
		ID:        ulid.Make().String(),
		Type:      typ,
		SubjectID: subjectID,
		Reason:    reason,
		Payload:   "{}",
		CreatedAt: tsMs,
	}
}

func buildEventWithPayload(typ, subjectID, reason, payload string, tsMs int64) store.Event {
	if payload == "" {
		payload = "{}"
	}
	return store.Event{
		ID:        ulid.Make().String(),
		Type:      typ,
		SubjectID: subjectID,
		Reason:    reason,
		Payload:   payload,
		CreatedAt: tsMs,
	}
}

// decisionLinksToStore converts decision-output links to store.Link rows.
// source is always "reconciler"; from is the newly committed memory.
func decisionLinksToStore(fromMemID string, dls []DecisionLink) []store.Link {
	if len(dls) == 0 {
		return nil
	}
	out := make([]store.Link, len(dls))
	for i, dl := range dls {
		out[i] = store.Link{
			ID:         ulid.Make().String(),
			FromMemory: fromMemID,
			ToMemory:   dl.TargetID,
			Type:       dl.Type,
			Source:     "reconciler",
			Confidence: 1.0,
		}
	}
	return out
}

func findNeighborByID(neighbors []store.Memory, id string) store.Memory {
	for _, n := range neighbors {
		if n.ID == id {
			return n
		}
	}
	// Fallback: return a stub with just the ID if the neighbor wasn't found.
	return store.Memory{ID: id}
}

func findNeighborsByIDs(neighbors []store.Memory, ids []string) []store.Memory {
	out := make([]store.Memory, 0, len(ids))
	for _, id := range ids {
		out = append(out, findNeighborByID(neighbors, id))
	}
	return out
}

func firstTargetID(ids []string) string {
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}
