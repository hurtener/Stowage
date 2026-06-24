package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// rollupSweepLockKey is the advisory lock key for the rollup sweep (D-057).
const rollupSweepLockKey int64 = 0x1403

// rollupPageSize is the page size used when listing memories for rollup.
const rollupPageSize = 50

func (m *Manager) runRollup(ctx context.Context) {
	release, err := m.st.Ops().AdvisoryLock(ctx, rollupSweepLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/rollup: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	tenants, err := m.st.Tenants(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/rollup: list tenants failed", "err", err)
		return
	}

	for _, tenant := range tenants {
		m.rollupTenant(ctx, tenant)
	}
}

func (m *Manager) rollupTenant(ctx context.Context, tenant string) {
	scope := identity.Scope{Tenant: tenant}
	// Only roll up session memories older than rollupAge.
	cutoff := time.Now().Add(-m.profile.RollupAge).UnixMilli()

	cursor := ""
	processed := 0
	for processed < m.profile.RollupBatchSize {
		remaining := m.profile.RollupBatchSize - processed
		pageSize := rollupPageSize
		if remaining < pageSize {
			pageSize = remaining
		}
		batch, next, err := m.st.Memories().ListActiveForDecay(ctx, scope, pageSize, cursor)
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/rollup: list failed", "tenant", tenant, "err", err)
			return
		}

		// Group session-scoped working memories by session.
		bySession := map[string][]store.Memory{}
		for _, mem := range batch {
			if mem.SessionID == "" || mem.CreatedAt > cutoff {
				continue // not session-scoped or too recent
			}
			bySession[mem.SessionID] = append(bySession[mem.SessionID], mem)
		}

		for sessID, mems := range bySession {
			m.rollupSession(ctx, scope, sessID, mems)
		}
		processed += len(batch)
		if next == "" || len(batch) == 0 {
			break
		}
		cursor = next
	}
}

func (m *Manager) rollupSession(ctx context.Context, scope identity.Scope, sessID string, mems []store.Memory) {
	if len(mems) == 0 {
		return
	}

	now := time.Now().UnixMilli()

	// Separate personal-zone memories (cannot be promoted) from promotable ones.
	var promotable []store.Memory
	var personal []store.Memory
	for _, mem := range mems {
		if mem.PrivacyZone == "personal" || mem.PrivacyZone == "personal+" {
			personal = append(personal, mem)
		} else {
			promotable = append(promotable, mem)
		}
	}

	// Expire personal zone memories without promotion.
	for _, mem := range personal {
		jt, _ := m.st.Memories().GetJunctions(ctx, scope, mem.ID)
		priorJSON := reconcile.MarshalPriorState(mem, jt)
		cs := store.CommitSet{
			Action: store.ActionDiscard,
			Events: []store.Event{
				{
					ID:        ulid.Make().String(),
					Type:      "memory.expired",
					SubjectID: mem.ID,
					Reason:    "rollup: personal zone session memory expired unpromoted",
					Payload:   priorJSON,
					CreatedAt: now,
				},
			},
			Scope: scope,
		}
		if err := m.st.Memories().Commit(ctx, scope, cs); err != nil {
			m.log.WarnContext(ctx, "lifecycle/rollup: expire personal failed", "id", mem.ID, "err", err)
			continue
		}
		_ = m.st.Memories().SetStatus(ctx, scope, mem.ID, "expired", now)
	}

	if len(promotable) == 0 {
		// Personal-zone-only session: rows were expired above but the sole
		// invalidateScope below is unreachable — drop cached results here so an
		// expired memory isn't served for the 60s TTL (D-118 / 29d S2). scope is
		// already tenant-only, which matches the tenant-keyed result cache.
		if len(personal) > 0 {
			m.invalidateScope(scope)
		}
		return
	}

	// Build narrative digest from ALL promotable memories.
	digestContent := buildDigestContent(sessID, promotable)
	digestHash := reconcile.ContentHash(reconcile.NormalizeContent(digestContent))

	// Union provenance, entities, keywords and aggregate counters.
	entitySet := map[string]bool{}
	kwSet := map[string]bool{}
	var allProv []store.Provenance
	maxImportance := 0
	var totalUse, totalSave, totalFail, totalNoise, totalInject int64

	for _, mem := range promotable {
		jt, _ := m.st.Memories().GetJunctions(ctx, scope, mem.ID)
		for _, e := range jt.Entities {
			entitySet[e] = true
		}
		for _, k := range jt.Keywords {
			kwSet[k] = true
		}
		allProv = append(allProv, jt.Provenance...)
		if mem.Importance > maxImportance {
			maxImportance = mem.Importance
		}
		totalUse += mem.UseCount
		totalSave += mem.SaveCount
		totalFail += mem.FailCount
		totalNoise += mem.NoiseCount
		totalInject += mem.InjectCount
	}

	entities := make([]string, 0, len(entitySet))
	for e := range entitySet {
		entities = append(entities, e)
	}
	keywords := make([]string, 0, len(kwSet))
	for k := range kwSet {
		keywords = append(keywords, k)
	}

	// Promoted digest goes to parent scope (SessionID intentionally empty).
	digest := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "narrative",
		Content:     digestContent,
		Status:      "active",
		Importance:  maxImportance,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   2.0, // slightly more stable than default
		ContentHash: digestHash,
		CreatedAt:   now,
		UpdatedAt:   now,
		UseCount:    totalUse,
		SaveCount:   totalSave,
		FailCount:   totalFail,
		NoiseCount:  totalNoise,
		InjectCount: totalInject,
		// SessionID intentionally empty — promoted to parent scope
	}

	var events []store.Event
	for _, mem := range promotable {
		jt, _ := m.st.Memories().GetJunctions(ctx, scope, mem.ID)
		priorJSON := reconcile.MarshalPriorState(mem, jt)
		// memory.merged (NOT memory.superseded): this is a many-to-one merge into one
		// digest. memory.merged routes to rollbackMerged, which restores ALL siblings via
		// ListSupersededBy(digest); memory.superseded would restore only the one subject and
		// strand the N-1 siblings on a tombstoned digest (P4 / D-070, 29d S1).
		events = append(events, store.Event{
			ID:        ulid.Make().String(),
			Type:      "memory.merged",
			SubjectID: mem.ID,
			Reason:    "rollup: session working memory rolled into digest",
			Payload:   priorJSON,
			CreatedAt: now,
		})
	}

	rollupPayload, _ := json.Marshal(map[string]any{
		"session_id":   sessID,
		"source_count": len(promotable),
		"digest_id":    digest.ID,
	})
	events = append(events, store.Event{
		ID:        ulid.Make().String(),
		Type:      "lifecycle.rollup",
		SubjectID: digest.ID,
		Reason:    fmt.Sprintf("session %s rolled up (%d memories)", sessID, len(promotable)),
		Payload:   string(rollupPayload),
		CreatedAt: now,
	})
	events = append(events, store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.added",
		SubjectID: digest.ID,
		Reason:    "rollup: session digest promoted",
		Payload:   "{}",
		CreatedAt: now,
	})

	// Build provenance rows for the digest (re-stamp IDs for uniqueness).
	digestProv := make([]store.Provenance, len(allProv))
	for i, p := range allProv {
		digestProv[i] = store.Provenance{
			ID:        ulid.Make().String(),
			MemoryID:  digest.ID,
			RecordID:  p.RecordID,
			SpanStart: p.SpanStart,
			SpanEnd:   p.SpanEnd,
			TenantID:  scope.Tenant,
			CreatedAt: now,
		}
	}

	cs := store.CommitSet{
		Action:     store.ActionMerge,
		Memory:     digest,
		Entities:   entities,
		Keywords:   keywords,
		Provenance: digestProv,
		Targets:    promotable,
		Events:     events,
		Scope:      scope,
	}
	if err := m.st.Memories().Commit(ctx, scope, cs); err != nil {
		m.log.WarnContext(ctx, "lifecycle/rollup: commit failed",
			"session", sessID, "err", err)
		return
	}
	m.invalidateScope(scope) // D-118: sources superseded + digest added — drop cached results
	m.log.InfoContext(ctx, "lifecycle/rollup: session rolled up",
		"tenant", scope.Tenant, "session", sessID,
		"sources", len(promotable), "digest", digest.ID)
}

// buildDigestContent builds the narrative digest content string from ALL the session's
// promotable memories. It must include every memory whose content is being superseded by this
// digest — the previous 10-item cap silently dropped the content of memories 11+ even though
// they were all retired via Targets (D-116, audit #6). A session digest is bounded in practice
// (rollup only fires on idle, aged sessions of working memory).
func buildDigestContent(sessID string, mems []store.Memory) string {
	s := fmt.Sprintf("Session digest [%s]:", sessID)
	for _, mem := range mems {
		s += fmt.Sprintf(" %s.", mem.Content)
	}
	return s
}
