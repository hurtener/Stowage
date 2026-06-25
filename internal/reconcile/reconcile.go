package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

const (
	// reconcileWorkers is the number of concurrent reconcile goroutines.
	reconcileWorkers = 4

	// decisionMaxTokens is the model output token budget for the decision call.
	// The decision JSON itself is tiny, but thinking models spend reasoning
	// tokens against this same budget and the gateway fails the call outright
	// at max_tokens (ErrTruncated) — 512 starved gemini-3.5-flash and
	// dead-lettered every reconcile decision (2026-06-12 sanity check, same
	// failure mode as extraction). Generation stops at the closing brace, so
	// the ceiling bounds the worst case, not typical spend.
	decisionMaxTokens = 8192

	// nearDupThreshold is the bigram-Jaccard similarity at or above which a
	// candidate is treated as the same fact as a retrieved neighbor and
	// discarded without an LLM call (D-044). This constant is the profile knob
	// boundary documented here; eval data should inform any adjustment.
	nearDupThreshold = 0.85

	// neighborLimit is the default maximum number of neighbors returned by
	// FindNeighbors for the decision context window.
	neighborLimit = 8

	// augmentCosineFloor is the minimum cosine for a vector neighbor to be added to the
	// LLM reconcile decision context (A4). A floor keeps the prompt focused on genuinely
	// related memories — weakly-related top-k hits in a sparse scope are dropped. The
	// LLM (not a threshold) decides dedup vs supersede, so this is a recall knob, never
	// an auto-discard gate.
	augmentCosineFloor = 0.70

	// vectorNeighborK bounds the semantic neighbor search per candidate (A4).
	vectorNeighborK = 8
)

// ScopeInvalidator invalidates cached retrieval results after a content-changing
// commit. Defined here so the reconcile package does not need to import the
// retrieval package (duck-typed — retrieval.ResultCache satisfies this
// interface automatically, D-053).
type ScopeInvalidator interface {
	InvalidateScope(scope identity.Scope)
}

// ReconcileStage consumes CandidateBatch events from the extract stage and
// reconciles each candidate against the memory store using an 8-step flow.
// It is safe for concurrent use after Start is called.
type ReconcileStage struct {
	mem         store.MemoryStore
	ops         store.OpsStore
	evts        store.EventStore
	gw          gateway.Gateway
	log         *slog.Logger
	in          <-chan pipeline.CandidateBatch
	wg          sync.WaitGroup
	embedder    *Embedder         // optional; nil = no embedding (degraded-embed mode)
	vi          vindex.Index      // optional; nil = structural neighbors only (A4)
	invalidator ScopeInvalidator  // optional; nil = cache invalidation disabled (Phase 12, D-053)
	recs        store.RecordStore // optional; nil = no conversation context in the decision (D-108)
}

// augmentWithVectorNeighbors merges SEMANTIC neighbors (vector lane, cosine ≥ floor)
// into the structural neighbor set so a same-fact candidate sharing no exact token
// still reaches the LLM reconcile DECISION (A4). It NEVER auto-discards — the LLM
// decides dedup vs supersede. Degraded-safe: nil vindex/gateway or an embed/search
// failure returns the structural set unchanged.
func (r *ReconcileStage) augmentWithVectorNeighbors(ctx context.Context, scope identity.Scope, c pipeline.Candidate, structural []store.Memory) []store.Memory {
	if r.vi == nil || r.gw == nil {
		return structural
	}
	text := buildEnrichedText(store.MemoryForEmbed{
		Content: c.Content, Entities: c.Entities, Keywords: c.Keywords, Queries: c.AnticipatedQueries,
	})
	if text == "" {
		return structural
	}
	// Scope the ctx so the embed call is attributed in the usage-event stream (§10),
	// matching the extract/reflect pattern; the reconcile stage runs on a scope-less
	// background ctx (D-088).
	resp, err := r.gw.Embed(identity.WithScope(ctx, scope), gateway.EmbedRequest{Inputs: []string{text}})
	if err != nil || len(resp.Vectors) == 0 {
		// Gateway down ⇒ structural-only neighbors (D-036 degraded write path).
		return structural
	}
	f := vindex.Filter{}
	if pipeline.IsReflectionKind(c.Kind) {
		f.Kinds = pipeline.ReflectionKindList()
	}
	hits, err := r.vi.Search(ctx, scope, resp.Vectors[0], vectorNeighborK, f)
	if err != nil {
		r.log.WarnContext(ctx, "reconcile: vector neighbor search failed — structural only", "err", err)
		return structural
	}
	have := make(map[string]bool, len(structural))
	for _, n := range structural {
		have[n.ID] = true
	}
	var newIDs []string
	for _, h := range hits {
		if h.Score < augmentCosineFloor || have[h.MemoryID] {
			continue // weakly related, or already a structural neighbor
		}
		have[h.MemoryID] = true
		newIDs = append(newIDs, h.MemoryID)
	}
	if len(newIDs) > 0 {
		extra, gerr := r.mem.GetMany(ctx, scope, newIDs)
		if gerr != nil {
			r.log.WarnContext(ctx, "reconcile: GetMany vector neighbors failed", "err", gerr)
		} else {
			structural = append(structural, extra...)
		}
	}
	return structural
}

// SetVIndex wires the vector index so reconcile augments its structural (entity/keyword)
// neighbor set with SEMANTIC neighbors (A4, brief 02): a candidate that is the same fact
// as an existing memory but shares no exact token is still caught for dedup/supersede.
// Best-effort and degraded-safe: a gateway/search failure falls back to structural-only.
// Call once before Start.
func (r *ReconcileStage) SetVIndex(vi vindex.Index) { r.vi = vi }

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

// SetEmbedder wires an optional Embedder for post-commit vector embedding (D-047).
// Must be called before Start. If not set, vector embedding is skipped (degraded-
// embed mode — retrieval still works lexically).
func (r *ReconcileStage) SetEmbedder(e *Embedder) {
	r.embedder = e
}

// SetRecordStore wires an optional RecordStore so the supersede/merge decision sees the
// candidate's and neighbors' original conversation turns (D-108, Phase 29b). Must be called
// before Start. If not set, the decision runs on memories only (current behaviour).
func (r *ReconcileStage) SetRecordStore(recs store.RecordStore) {
	r.recs = recs
}

// maxContextRecords bounds the conversation turns fed to one reconcile decision (P2/cost).
const maxContextRecords = 12

// buildReconcileContext fetches the raw turns behind the candidate and its neighbors so the
// decision can distinguish a correction from a distinct fact (D-108). Bounded and degrade-safe:
// a fetch error logs and yields an empty/partial context — the decision still runs. Returns the
// zero value when no RecordStore is wired.
func (r *ReconcileStage) buildReconcileContext(ctx context.Context, scope identity.Scope, c pipeline.Candidate, neighbors []store.Memory) ReconcileContext {
	if r.recs == nil {
		return ReconcileContext{}
	}
	seen := map[string]bool{}
	budget := maxContextRecords

	collect := func(ids []string) []store.Record {
		var want []string
		for _, id := range ids {
			if id == "" || seen[id] || budget <= 0 {
				continue
			}
			seen[id] = true
			want = append(want, id)
			budget--
		}
		if len(want) == 0 {
			return nil
		}
		recs, err := r.recs.GetMany(ctx, scope, want)
		if err != nil {
			r.log.WarnContext(ctx, "reconcile: context record fetch failed — proceeding without", "err", err)
			return nil
		}
		return recs
	}

	rc := ReconcileContext{}
	candIDs := make([]string, 0, len(c.Provenance))
	for _, p := range c.Provenance {
		candIDs = append(candIDs, p.RecordID)
	}
	rc.CandidateTurns = collect(candIDs)

	rc.NeighborTurns = map[string][]store.Record{}
	for _, n := range neighbors {
		if budget <= 0 {
			break
		}
		j, err := r.mem.GetJunctions(ctx, scope, n.ID)
		if err != nil {
			continue // best-effort: skip this neighbor's turns
		}
		nIDs := make([]string, 0, len(j.Provenance))
		for _, p := range j.Provenance {
			nIDs = append(nIDs, p.RecordID)
		}
		if turns := collect(nIDs); len(turns) > 0 {
			rc.NeighborTurns[n.ID] = turns
		}
	}
	return rc
}

// SetScopeInvalidator wires an optional ScopeInvalidator for cache invalidation
// after every content-changing commit (D-053). Must be called before Start.
// If not set, cache invalidation is skipped (safe — cache entries expire via TTL).
func (r *ReconcileStage) SetScopeInvalidator(inv ScopeInvalidator) {
	r.invalidator = inv
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

// candidateAssertionKey returns a candidate's assertion-order key: the LATEST source
// record ID among its provenance. Record IDs are ULIDs — monotonic in ingestion order,
// which IS conversation/turn order — so this orders candidates by when they were said,
// finer than session-granular occurred_at (D-106).
func candidateAssertionKey(c pipeline.Candidate) string {
	key := ""
	for _, p := range c.Provenance {
		if p.RecordID > key {
			key = p.RecordID
		}
	}
	return key
}

// worker processes CandidateBatch events until the input channel is closed.
func (r *ReconcileStage) worker(ctx context.Context) {
	for batch := range r.in {
		// Process a flush's candidates OLDEST-asserted first (by source-record/turn order),
		// so that when two candidates in the same flush state contradictory values for one
		// fact ("6 months" then "9 months"), the older commits first and the newer supersedes
		// it — the current value wins deterministically. Without this, the LLM's arbitrary
		// candidate output order decided the winner, so supersede kept the stale value ~half
		// the time (D-106). Stable sort preserves emission order within the same record.
		sort.SliceStable(batch.Candidates, func(i, j int) bool {
			return candidateAssertionKey(batch.Candidates[i]) < candidateAssertionKey(batch.Candidates[j])
		})
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
		if incErr := r.mem.IncrementCounter(ctx, scope, existing.ID, "match"); incErr != nil {
			r.log.WarnContext(ctx, "reconcile: IncrementCounter failed",
				"id", existing.ID, "err", incErr)
		}
		r.log.DebugContext(ctx, "reconcile: exact dup discarded",
			"tenant", scope.Tenant, "hash", hash, "existing_id", existing.ID)
		return r.commitExactDupDiscard(ctx, scope, existing.ID, "exact duplicate")
	} else if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("reconcile: GetByContentHash: %w", err)
	}

	// Step 2b: Parked-duplicate check (Phase 18, D-064).
	// If the same normalized content is already pending confirmation, bump its
	// match_count and emit memory.reconfirmed — no duplicate park is created.
	if discard, err := r.checkParkedDuplicate(ctx, scope, hash); err != nil {
		return fmt.Errorf("reconcile: parked-dup check: %w", err)
	} else if discard {
		return nil
	}

	// Step 3: Find structural neighbors. Reflection candidates (strategy /
	// failure_mode) restrict the neighbor search to reflection kinds so a strategy
	// can only dedupe/update/supersede another reflection memory — never a fact
	// (D-077 #5). Topic candidates leave Kinds empty (all kinds), as before.
	nq := store.NeighborQuery{
		Entities: c.Entities,
		Keywords: c.Keywords,
		Limit:    neighborLimit,
	}
	if pipeline.IsReflectionKind(c.Kind) {
		nq.Kinds = pipeline.ReflectionKindList()
	}
	neighbors, err := r.mem.FindNeighbors(ctx, scope, nq)
	if err != nil {
		return fmt.Errorf("reconcile: FindNeighbors: %w", err)
	}

	// Step 3b: Augment with SEMANTIC neighbors from the vector lane (A4, brief 02) —
	// catches a same-fact candidate that shares no exact entity/keyword token, so it
	// reaches the LLM reconcile DECISION (dedup OR supersede). Degraded-safe.
	neighbors = r.augmentWithVectorNeighbors(ctx, scope, c, neighbors)

	// Step 4: Near-dup pre-filter (D-044). The fast auto-discard stays LEXICAL only
	// (bigram-Jaccard ≥ nearDupThreshold = near-identical surface form): a polarity flip
	// ("X works" vs "X does not work") embeds at high cosine but is NOT lexically
	// near-identical, so it correctly falls through to the LLM, which detects the
	// contradiction and supersedes (Pearce-Hall, P4). A cosine arm here would silently
	// swallow corrections — semantic similarity drives RECALL (above), never auto-discard.
	for _, n := range neighbors {
		if BigramJaccard(normalized, n.Content) >= nearDupThreshold {
			// D-104 numeric-correction guard: a lexically near-identical candidate that
			// carries a DIFFERENT numeral for the same fact ("...120 stars" vs "...125
			// stars"; "...9 months" vs "...6 months") is a correction, NOT a duplicate.
			// Auto-discarding it would swallow the correction AND bump the stale memory's
			// match_count (raising its rank) — the exact stale-value miss. Fall through to
			// the LLM decision (supersede path) instead.
			if NumeralsDiverge(normalized, n.Content) {
				r.log.DebugContext(ctx, "reconcile: near-dup numeral divergence — routing to LLM (D-104)",
					"tenant", scope.Tenant, "neighbor_id", n.ID)
				continue
			}
			if incErr := r.mem.IncrementCounter(ctx, scope, n.ID, "match"); incErr != nil {
				r.log.WarnContext(ctx, "reconcile: IncrementCounter failed",
					"id", n.ID, "err", incErr)
			}
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

	// Step 6: Build LLM prompt and call gateway. Enrich with the original conversation
	// turns behind the candidate + neighbors so the decision distinguishes a correction
	// from a distinct fact (D-108); degrade-safe (empty context when no RecordStore).
	systemPrompt := BuildSystemPrompt()
	rc := r.buildReconcileContext(ctx, scope, c, neighbors)
	userPrompt := BuildUserPrompt(c, neighbors, rc)

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

// commit applies the trust gate (for supersede/update/merge) and executes the
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
			Topics:   c.Topics,
			Links:    decisionLinksToStore(mem.ID, d.Links),
			Events: []store.Event{
				buildEvent("memory.added", mem.ID, d.Reason, now),
			},
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		if err := r.mem.Commit(ctx, scope, cs); err != nil {
			if errors.Is(err, store.ErrDuplicateContent) {
				return r.handleDuplicateContent(ctx, scope, hash)
			}
			return err
		}
		r.invalidateScope(scope) // new content added — invalidate result cache (D-053)
		r.enqueueEmbed(scope, c, mem.ID, normalized)
		return nil

	case store.ActionPark:
		mem := candidateToMemory(c, normalized, hash, "pending_confirmation")
		cs := store.CommitSet{
			Action:   store.ActionPark,
			Memory:   mem,
			Entities: c.Entities,
			Keywords: c.Keywords,
			Queries:  c.AnticipatedQueries,
			Topics:   c.Topics,
			Events: []store.Event{
				buildEvent("memory.parked", mem.ID, d.Reason, now),
			},
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		if err := r.mem.Commit(ctx, scope, cs); err != nil {
			if errors.Is(err, store.ErrDuplicateContent) {
				return r.handleDuplicateContent(ctx, scope, hash)
			}
			return err
		}
		return nil

	case store.ActionDiscard:
		return r.commitDiscard(ctx, scope, firstTargetID(d.TargetIDs), d.Reason)

	case store.ActionUpdate:
		target := findNeighborByID(neighbors, d.TargetIDs[0])
		jt := r.getJunctions(ctx, scope, target.ID) // M6: fetch junctions for prior-state

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
				Topics:   c.Topics,
				Targets:  []store.Memory{target},
				Events: []store.Event{
					buildEventWithPayload("memory.parked", mem.ID,
						"trust gate: target trust ≥ park threshold; pending human review",
						MarshalPriorState(target, jt), now),
				},
			}
			cs.Provenance = buildProvenance(mem.ID, c.Provenance)
			if err := r.mem.Commit(ctx, scope, cs); err != nil {
				if errors.Is(err, store.ErrDuplicateContent) {
					return r.handleDuplicateContent(ctx, scope, hash)
				}
				return err
			}
			return nil
		}

		// C1/M5: Content comes from the decision; Context comes from the candidate
		// (or the target's if the candidate has none). All other target fields are
		// preserved by the targeted SQL UPDATE in the driver (only content, context,
		// content_hash, updated_at are written).
		normalizedContent := NormalizeContent(d.Content)
		updateHash := ContentHash(normalizedContent)
		updateCtx := c.Context
		if updateCtx == "" {
			updateCtx = target.Context
		}
		mem := store.Memory{
			ID:          target.ID,
			Content:     normalizedContent,
			Context:     updateCtx,
			ContentHash: updateHash,
			// UpdatedAt zero → driver sets to now.
		}
		events := []store.Event{
			buildEventWithPayload("memory.updated", mem.ID, d.Reason, MarshalPriorState(target, jt), now),
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
			Topics:   c.Topics,
			Targets:  []store.Memory{target},
			Links:    decisionLinksToStore(mem.ID, d.Links),
			Events:   events,
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		if err := r.mem.Commit(ctx, scope, cs); err != nil {
			return err
		}
		r.invalidateScope(scope) // content updated — invalidate result cache (D-053)
		r.enqueueEmbed(scope, c, mem.ID, normalizedContent)
		return nil

	case store.ActionSupersede:
		target := findNeighborByID(neighbors, d.TargetIDs[0])
		jt := r.getJunctions(ctx, scope, target.ID) // M6: fetch junctions for prior-state

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
				Topics:   c.Topics,
				Targets:  []store.Memory{target},
				Events: []store.Event{
					buildEventWithPayload("memory.parked", mem.ID,
						"trust gate: target trust ≥ park threshold; pending human review",
						MarshalPriorState(target, jt), now),
				},
			}
			cs.Provenance = buildProvenance(mem.ID, c.Provenance)
			if err := r.mem.Commit(ctx, scope, cs); err != nil {
				if errors.Is(err, store.ErrDuplicateContent) {
					return r.handleDuplicateContent(ctx, scope, hash)
				}
				return err
			}
			return nil
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
			buildEventWithPayload("memory.superseded", target.ID, d.Reason, MarshalPriorState(target, jt), now),
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
			Topics:   c.Topics,
			Targets:  []store.Memory{target},
			Links:    links,
			Events:   events,
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		if err := r.mem.Commit(ctx, scope, cs); err != nil {
			return err
		}
		r.invalidateScope(scope) // new superseding memory added — invalidate result cache (D-053)
		r.enqueueEmbed(scope, c, mem.ID, normalized)
		return nil

	case store.ActionMerge:
		targets := findNeighborsByIDs(neighbors, d.TargetIDs)

		// M3 trust gate: evaluate ALL merge targets.
		// Any High-trust target → park; any Medium (no High) → apply + warn.
		var hasHigh bool
		var hasMedium bool
		var highestTrustID string
		var highestScore float64
		for _, t := range targets {
			s := targetTrustScore(t.UseCount, t.SaveCount, t.TrustSource, t.Importance)
			switch {
			case s >= trustGatePark:
				hasHigh = true
				if s > highestScore {
					highestScore = s
					highestTrustID = t.ID
				}
			case s >= trustGateWarn:
				hasMedium = true
			}
		}

		if hasHigh {
			// Any High-trust target → park candidate; NO target is touched.
			mem := candidateToMemory(c, normalized, hash, "pending_confirmation")
			mem.SupersedesID = highestTrustID
			cs := store.CommitSet{
				Action:   store.ActionPark,
				Memory:   mem,
				Entities: c.Entities,
				Keywords: c.Keywords,
				Queries:  c.AnticipatedQueries,
				Topics:   c.Topics,
				Events: []store.Event{
					buildEvent("memory.parked", mem.ID,
						"trust gate: merge target trust ≥ park threshold; pending human review", now),
				},
			}
			cs.Provenance = buildProvenance(mem.ID, c.Provenance)
			if err := r.mem.Commit(ctx, scope, cs); err != nil {
				if errors.Is(err, store.ErrDuplicateContent) {
					return r.handleDuplicateContent(ctx, scope, hash)
				}
				return err
			}
			return nil
		}

		// M5: use decision.Content as the merged content.
		normalizedMerge := NormalizeContent(d.Content)
		mergeHash := ContentHash(normalizedMerge)

		mem := candidateToMemory(c, normalizedMerge, mergeHash, "active")
		var events []store.Event
		for _, t := range targets {
			jt := r.getJunctions(ctx, scope, t.ID) // M6: fetch junctions per target
			events = append(events, buildEventWithPayload("memory.merged", t.ID, d.Reason, MarshalPriorState(t, jt), now))
		}
		events = append(events, buildEvent("memory.added", mem.ID, "merged memory added", now))
		if hasMedium {
			events = append(events, buildEvent("reconcile.warned", mem.ID,
				"trust gate: merge includes medium-trust targets; review recommended", now))
		}
		cs := store.CommitSet{
			Action:   store.ActionMerge,
			Memory:   mem,
			Entities: c.Entities,
			Keywords: c.Keywords,
			Queries:  c.AnticipatedQueries,
			Topics:   c.Topics,
			Targets:  targets,
			Links:    decisionLinksToStore(mem.ID, d.Links),
			Events:   events,
		}
		cs.Provenance = buildProvenance(mem.ID, c.Provenance)
		if err := r.mem.Commit(ctx, scope, cs); err != nil {
			if errors.Is(err, store.ErrDuplicateContent) {
				return r.handleDuplicateContent(ctx, scope, mergeHash)
			}
			return err
		}
		r.invalidateScope(scope) // merged memory added — invalidate result cache (D-053)
		r.enqueueEmbed(scope, c, mem.ID, normalizedMerge)
		return nil

	default:
		return fmt.Errorf("reconcile: unhandled action %q", action)
	}
}

// invalidateScope bumps the scope's result-cache generation counter (D-053).
// No-op when no invalidator is wired.
func (r *ReconcileStage) invalidateScope(scope identity.Scope) {
	if r.invalidator != nil {
		r.invalidator.InvalidateScope(scope)
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
		Topics:   c.Topics,
		Events: []store.Event{
			buildEvent("memory.added", mem.ID, "fast-add: no neighbors found", nowMs()),
		},
	}
	cs.Provenance = buildProvenance(mem.ID, c.Provenance)
	if err := r.mem.Commit(ctx, scope, cs); err != nil {
		// m7: TOCTOU — another goroutine committed the same hash between our
		// exact-dedup check and this commit. Treat as exact-dedup hit.
		if errors.Is(err, store.ErrDuplicateContent) {
			return r.handleDuplicateContent(ctx, scope, hash)
		}
		return err
	}
	r.invalidateScope(scope) // new memory added (fast-add) — invalidate result cache (D-053)
	r.enqueueEmbed(scope, c, mem.ID, normalized)
	return nil
}

// handleDuplicateContent handles a TOCTOU ErrDuplicateContent from Commit (m7).
// It looks up the existing row by hash, bumps its match counter, and emits a
// dedup_exact discard — treating the race as an exact-duplicate hit.
func (r *ReconcileStage) handleDuplicateContent(ctx context.Context, scope identity.Scope, hash string) error {
	if existing, err := r.mem.GetByContentHash(ctx, scope, hash); err == nil {
		if incErr := r.mem.IncrementCounter(ctx, scope, existing.ID, "match"); incErr != nil {
			r.log.WarnContext(ctx, "reconcile: IncrementCounter failed after dedup race",
				"id", existing.ID, "err", incErr)
		}
	}
	return r.commitExactDupDiscard(ctx, scope, "", "exact duplicate (concurrent race)")
}

// getJunctions fetches junction rows for a memory for prior-state snapshots (M6).
// Errors are logged at Warn and an empty MemoryJunctions is returned so the
// snapshot is still emitted (minus junctions) rather than dropping the event.
func (r *ReconcileStage) getJunctions(ctx context.Context, scope identity.Scope, id string) store.MemoryJunctions {
	j, err := r.mem.GetJunctions(ctx, scope, id)
	if err != nil {
		r.log.WarnContext(ctx, "reconcile: GetJunctions failed; prior-state junctions omitted",
			"id", id, "err", err)
	}
	return j
}

// enqueueEmbed non-blockingly enqueues an embed job for a newly committed memory.
// If no embedder is wired, or if the memory status is not "active", this is a
// no-op. Enriched text = content + entities + keywords + anticipated queries (D-047).
func (r *ReconcileStage) enqueueEmbed(scope identity.Scope, c pipeline.Candidate, memID, content string) {
	if r.embedder == nil {
		return
	}
	m := store.MemoryForEmbed{
		MemoryID: memID,
		TenantID: scope.Tenant,
		Content:  content,
		Entities: c.Entities,
		Keywords: c.Keywords,
		Queries:  c.AnticipatedQueries,
	}
	r.embedder.Enqueue(EmbedJob{
		Scope:        scope,
		MemoryID:     memID,
		EnrichedText: buildEnrichedText(m),
	})
}

// checkParkedDuplicate returns true when the same content hash is already in
// pending_confirmation state. It bumps the existing memory's match_count,
// emits a memory.reconfirmed event, and returns true so the caller discards
// the incoming candidate without creating a second parked row (Phase 18, D-064).
func (r *ReconcileStage) checkParkedDuplicate(ctx context.Context, scope identity.Scope, hash string) (bool, error) {
	existing, err := r.mem.GetByContentHashStatus(ctx, scope, hash, "pending_confirmation")
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Hit: bump match counter.
	if incErr := r.mem.IncrementCounter(ctx, scope, existing.ID, "match"); incErr != nil {
		r.log.WarnContext(ctx, "reconcile: IncrementCounter failed on parked dup",
			"id", existing.ID, "err", incErr)
	}
	// Emit memory.reconfirmed event atomically via ActionDiscard commit.
	cs := store.CommitSet{
		Action: store.ActionDiscard,
		Events: []store.Event{
			buildEvent("memory.reconfirmed", existing.ID,
				"parked duplicate: content already pending confirmation", nowMs()),
		},
	}
	if commitErr := r.mem.Commit(ctx, scope, cs); commitErr != nil {
		r.log.WarnContext(ctx, "reconcile: memory.reconfirmed event commit failed", "err", commitErr)
	}
	r.log.DebugContext(ctx, "reconcile: parked dup reconfirmed",
		"tenant", scope.Tenant, "hash", hash, "existing_id", existing.ID)
	return true, nil
}

// --- helpers ----------------------------------------------------------------

func nowMs() int64 {
	return time.Now().UnixMilli()
}

func candidateToMemory(c pipeline.Candidate, normalized, hash, status string) store.Memory {
	// Server-set provenance/seed (D-077 #4): reflection candidates carry
	// "llm_reflected" + a seed stability; topic candidates leave them zero and
	// inherit the defaults below.
	trust := c.TrustSource
	if trust == "" {
		trust = "llm_extracted"
	}
	stability := c.Stability
	if stability == 0 {
		stability = 1.0
	}
	return store.Memory{
		ID:          ulid.Make().String(),
		Kind:        c.Kind,
		Content:     normalized,
		Context:     c.Context, // M4: preserve candidate context
		Status:      status,
		Importance:  c.Importance,
		Confidence:  c.Confidence,
		TrustSource: trust,
		Stability:   stability,
		ContentHash: hash,
		ValidFrom:   c.OccurredAt, // assertion (conversation) date — surfaced as "when" at retrieval (D-109)
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
