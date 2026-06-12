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
		m.dedupeTenant(ctx, tenant)
	}
}

func (m *Manager) dedupeTenant(ctx context.Context, tenant string) {
	scope := identity.Scope{Tenant: tenant}
	comparisons := 0
	cursor := ""
	mergedThisPass := map[string]bool{}
	pageSize := dedupeBatchPageSize
	for comparisons < m.profile.DedupeBatchSize {
		remaining := m.profile.DedupeBatchSize - comparisons
		if remaining < pageSize {
			pageSize = remaining
		}
		batch, next, err := m.st.Memories().ListActiveForDecay(ctx, scope, pageSize, cursor)
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/dedupe: list failed", "tenant", tenant, "err", err)
			return
		}
		for _, mem := range batch {
			if comparisons >= m.profile.DedupeBatchSize {
				break
			}
			if mergedThisPass[mem.ID] {
				continue // superseded by an earlier merge in this pass
			}
			// Find structural neighbors.
			jt, _ := m.st.Memories().GetJunctions(ctx, scope, mem.ID)
			neighbors, err := m.st.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{
				Entities: jt.Entities,
				Keywords: jt.Keywords,
				Limit:    8,
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
				// Near-duplicate found: merge mem into n (keep the older one).
				// Mark both consumed: the batch snapshot is stale after a
				// merge, and re-merging this pass's own output cascades
				// (observed: second-generation merges losing provenance).
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

	// Union provenance.
	provUnion := append(srcJT.Provenance, tgtJT.Provenance...) //nolint:gocritic

	// Union entities and keywords.
	entitySet := map[string]bool{}
	for _, e := range srcJT.Entities {
		entitySet[e] = true
	}
	for _, e := range tgtJT.Entities {
		entitySet[e] = true
	}
	entities := make([]string, 0, len(entitySet))
	for e := range entitySet {
		entities = append(entities, e)
	}
	kwSet := map[string]bool{}
	for _, k := range srcJT.Keywords {
		kwSet[k] = true
	}
	for _, k := range tgtJT.Keywords {
		kwSet[k] = true
	}
	keywords := make([]string, 0, len(kwSet))
	for k := range kwSet {
		keywords = append(keywords, k)
	}

	// Merged counters (target absorbs source).
	merged := target
	// The merged row is a NEW memory: fresh ID (reusing target.ID collided
	// with the unique PK — every sweep merge silently failed before this),
	// fresh supersedes link; counters/provenance/junctions are unions.
	merged.ID = ulid.Make().String()
	merged.SupersedesID = target.ID
	merged.MatchCount += src.MatchCount
	merged.UseCount += src.UseCount
	merged.SaveCount += src.SaveCount
	merged.FailCount += src.FailCount
	merged.NoiseCount += src.NoiseCount
	merged.InjectCount += src.InjectCount

	now := time.Now().UnixMilli()

	// Build events with prior-state snapshots.
	srcPrior := reconcile.MarshalPriorState(src, srcJT)
	tgtPrior := reconcile.MarshalPriorState(target, tgtJT)

	mergePayload, _ := json.Marshal(map[string]any{
		"similarity": sim,
		"src_id":     src.ID,
		"tgt_id":     target.ID,
	})

	events := []store.Event{
		{
			ID:        ulid.Make().String(),
			Type:      "memory.merged",
			SubjectID: src.ID,
			Reason:    "dedupe: near-duplicate merged into target",
			Payload:   srcPrior,
			CreatedAt: now,
		},
		{
			ID:        ulid.Make().String(),
			Type:      "memory.merged",
			SubjectID: target.ID,
			Reason:    "dedupe: near-duplicate target updated",
			Payload:   tgtPrior,
			CreatedAt: now,
		},
		{
			ID:        ulid.Make().String(),
			Type:      "lifecycle.dedupe",
			SubjectID: target.ID,
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
		Queries:    tgtJT.Queries,
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
	m.log.InfoContext(ctx, "lifecycle/dedupe: near-dup merged",
		"tenant", scope.Tenant, "src", src.ID, "tgt", target.ID, "sim", sim)
}
