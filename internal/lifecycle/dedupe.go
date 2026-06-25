package lifecycle

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// dedupeSweepLockKey is the advisory lock key for the dedupe sweep (D-057).
const dedupeSweepLockKey int64 = 0x1402

// nearDupThreshold is the bigram-Jaccard threshold for near-duplicate detection.
// Matches reconcile.nearDupThreshold (D-044) but is local to lifecycle.
const nearDupThreshold = 0.85

// dedupeBatchPageSize is the page size for loading memories during dedupe scan.
const dedupeBatchPageSize = 50

func (m *Manager) runDedupe(ctx context.Context) {
	release, err := m.st.Ops().AdvisoryLock(ctx, dedupeSweepLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/dedupe: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	tenants, err := m.st.Tenants(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/dedupe: list tenants failed", "err", err)
		return
	}

	for _, tenant := range tenants {
		// Run per (tenant,project,user) — NEVER tenant-wide — so FindNeighbors and the merge
		// commit can't compare/merge memories across different users or NULL-scope the survivor
		// (P3 + P1, D-111). Mirrors how the episode/threading sweeps iterate distinct scopes.
		scopes, err := m.st.Memories().DistinctScopes(ctx, identity.Scope{Tenant: tenant})
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/dedupe: distinct scopes failed", "tenant", tenant, "err", err)
			continue
		}
		for _, sc := range scopes {
			m.dedupeScope(ctx, sc)
		}
	}
}

func (m *Manager) dedupeScope(ctx context.Context, scope identity.Scope) {
	comparisons := 0
	cursor := ""
	mergedThisPass := map[string]bool{}
	pageSize := dedupeBatchPageSize
	for comparisons < m.profile.DedupeBatchSize {
		remaining := m.profile.DedupeBatchSize - comparisons
		if remaining < pageSize {
			pageSize = remaining
		}
		// EXACT-leaf scope (D-111 / 29d B1): the candidate batch must be confined to
		// THIS partition — including the NULL-leaf (tenant-/project-level) partition.
		// ListActiveForDecay is tenant-wide and would seed cross-user candidates.
		batch, next, err := m.st.Memories().ListActiveInScope(ctx, scope, pageSize, cursor)
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/dedupe: list failed", "scope", scope.String(), "err", err)
			return
		}
		for _, mem := range batch {
			if comparisons >= m.profile.DedupeBatchSize {
				break
			}
			if mergedThisPass[mem.ID] {
				continue // superseded by an earlier merge in this pass
			}
			// Find structural neighbors — ExactScope so a NULL-leaf partition cannot
			// reach another user's rows (P3 + P1, D-111 / 29d B1).
			jt, _ := m.st.Memories().GetJunctions(ctx, scope, mem.ID)
			neighbors, err := m.st.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{
				Entities:   jt.Entities,
				Keywords:   jt.Keywords,
				Limit:      8,
				ExactScope: true,
			})
			comparisons++
			if err != nil {
				continue
			}

			for _, n := range neighbors {
				if n.ID == mem.ID || mergedThisPass[n.ID] {
					continue // skip self and rows already consumed this pass
				}
				sim := reconcile.BigramJaccard(mem.Content, n.Content)
				if sim < nearDupThreshold {
					continue
				}
				// Near-duplicate found: merge the pair (SelectSurvivor picks the
				// winner inside mergeNearDup, D-111). Mark both consumed: the batch
				// snapshot is stale after a merge, and re-merging this pass's own
				// output cascades (observed: second-generation merges losing provenance).
				m.mergeNearDup(ctx, scope, mem, n, sim)
				mergedThisPass[mem.ID] = true
				mergedThisPass[n.ID] = true
				break // one merge per memory per pass
			}
		}
		if next == "" || len(batch) == 0 {
			break
		}
		cursor = next
	}
}

func (m *Manager) mergeNearDup(ctx context.Context, scope identity.Scope, src, target store.Memory, sim float64) {
	srcJT, _ := m.st.Memories().GetJunctions(ctx, scope, src.ID)
	tgtJT, _ := m.st.Memories().GetJunctions(ctx, scope, target.ID)

	// Pick the SURVIVOR deterministically (D-111) — later ValidFrom → trust → importance →
	// CreatedAt → ULID — instead of arbitrarily keeping `target`. correction=true when the two
	// carry divergent numerals (a value CORRECTION, not a pure duplicate): then we keep ONLY
	// the survivor's surface (entities/keywords/queries) so the stale value's wording does not
	// pollute the survivor and resurface it; for a true duplicate we union the surface.
	survivor, loser := reconcile.SelectSurvivor(src, target)
	survivorJT, loserJT := srcJT, tgtJT
	if survivor.ID == target.ID {
		survivorJT, loserJT = tgtJT, srcJT
	}
	correction := reconcile.NumeralsDiverge(src.Content, target.Content)

	// Provenance is always unioned (full history, reversible).
	provUnion := append(append([]store.Provenance{}, survivorJT.Provenance...), loserJT.Provenance...)

	entities := survivorJT.Entities
	keywords := survivorJT.Keywords
	queries := survivorJT.Queries
	if !correction {
		entities = unionStrings(survivorJT.Entities, loserJT.Entities)
		keywords = unionStrings(survivorJT.Keywords, loserJT.Keywords)
		queries = unionStrings(survivorJT.Queries, loserJT.Queries)
	}

	// The merged row is a NEW memory carrying the SURVIVOR's content/date, a fresh ID (reusing
	// an existing PK collided — every sweep merge silently failed before this), superseding the
	// loser; counters are unioned so usage stats are preserved.
	merged := survivor
	merged.ID = ulid.Make().String()
	merged.SupersedesID = loser.ID
	merged.MatchCount += loser.MatchCount
	merged.UseCount += loser.UseCount
	merged.SaveCount += loser.SaveCount
	merged.FailCount += loser.FailCount
	merged.NoiseCount += loser.NoiseCount
	merged.InjectCount += loser.InjectCount

	now := time.Now().UnixMilli()

	// Build events with prior-state snapshots.
	srcPrior := reconcile.MarshalPriorState(src, srcJT)
	tgtPrior := reconcile.MarshalPriorState(target, tgtJT)

	// Audit text names the ACTUAL winner/loser (D-111 made the survivor dynamic;
	// the event log is the source of truth, §8). Prior-state payloads stay keyed to
	// each memory's own ID so rollback round-trips regardless of which won.
	mergePayload, _ := json.Marshal(map[string]any{
		"similarity":  sim,
		"src_id":      src.ID,
		"tgt_id":      target.ID,
		"survivor_id": survivor.ID,
		"loser_id":    loser.ID,
		"merged_id":   merged.ID,
	})

	srcReason := "dedupe: near-duplicate, survivor"
	if src.ID == loser.ID {
		srcReason = "dedupe: near-duplicate, superseded by survivor"
	}
	tgtReason := "dedupe: near-duplicate, survivor"
	if target.ID == loser.ID {
		tgtReason = "dedupe: near-duplicate, superseded by survivor"
	}

	events := []store.Event{
		{
			ID:        ulid.Make().String(),
			Type:      "memory.merged",
			SubjectID: src.ID,
			Reason:    srcReason,
			Payload:   srcPrior,
			CreatedAt: now,
		},
		{
			ID:        ulid.Make().String(),
			Type:      "memory.merged",
			SubjectID: target.ID,
			Reason:    tgtReason,
			Payload:   tgtPrior,
			CreatedAt: now,
		},
		{
			ID:        ulid.Make().String(),
			Type:      "lifecycle.dedupe",
			SubjectID: merged.ID,
			Reason:    "near-dup merge",
			Payload:   string(mergePayload),
			CreatedAt: now,
		},
	}

	// Build provenance rows for the commit.
	storeProvenance := make([]store.Provenance, len(provUnion))
	copy(storeProvenance, provUnion)
	for i := range storeProvenance {
		// Fresh row IDs: these are COPIES of existing provenance rows; the
		// originals stay attached to the superseded sources (D-017 history),
		// and reusing their PKs makes the insert silently no-op.
		storeProvenance[i].ID = ulid.Make().String()
		storeProvenance[i].MemoryID = merged.ID
	}

	cs := store.CommitSet{
		Action:     store.ActionMerge,
		Memory:     merged,
		Entities:   entities,
		Keywords:   keywords,
		Queries:    queries,
		Provenance: storeProvenance,
		Targets:    []store.Memory{src, target},
		Events:     events,
		Scope:      scope,
	}
	if err := m.st.Memories().Commit(ctx, scope, cs); err != nil {
		m.log.WarnContext(ctx, "lifecycle/dedupe: merge commit failed",
			"src", src.ID, "tgt", target.ID, "err", err)
		return
	}
	// D-118 / 29d S5: the result cache is keyed by the REQUEST scope, which every
	// retrieve surface builds tenant-only — so invalidate at TENANT granularity. A
	// full-scope invalidate bumps gens["t/p/u"], which the tenant-keyed Get never checks.
	m.invalidateScope(identity.Scope{Tenant: scope.Tenant})
	m.log.InfoContext(ctx, "lifecycle/dedupe: near-dup merged",
		"tenant", scope.Tenant, "src", src.ID, "tgt", target.ID, "sim", sim)
}

// unionStrings returns the de-duplicated union of two string slices, preserving a's order
// then appending new elements from b (used to union junction surfaces for true duplicates).
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
