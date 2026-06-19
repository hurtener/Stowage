package lifecycle

// expire.go — the proactive-suggestion expiry sweep (Phase 27, D-087). It GCs
// PENDING suggestion offers older than SuggestTTL that the agent never resolved.
// Stale offers must not linger: the proactive engine dedupes against the session's
// any-status suggestion history, so an un-GC'd pending offer would permanently
// suppress re-offering that memory. Expiring it (status → 'expired', NOT counted
// as accept or dismiss) frees the memory to be offered again later while leaving
// the feedback tallies untouched.
//
// Gateway-free, advisory-locked, per-tenant, idempotent — the D-057 sweep pattern.

import (
	"context"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// suggestExpireLockKey is the advisory lock key for the expiry sweep (D-057).
const suggestExpireLockKey int64 = 0x140A

func (m *Manager) runExpireSuggestions(ctx context.Context) {
	release, err := m.st.Ops().AdvisoryLock(ctx, suggestExpireLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/suggest-expire: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	tenants, err := m.st.Tenants(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/suggest-expire: list tenants failed", "err", err)
		return
	}
	for _, tenant := range tenants {
		m.expireSuggestionsTenant(ctx, tenant)
	}
}

func (m *Manager) expireSuggestionsTenant(ctx context.Context, tenant string) {
	scope := identity.Scope{Tenant: tenant}
	ttl := m.profile.SuggestTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	batchSize := m.profile.SuggestExpireBatch
	if batchSize <= 0 {
		batchSize = 200
	}
	before := time.Now().Add(-ttl).UnixMilli()

	// One bounded page per sweep (D-057 pattern): if more than batchSize offers are
	// stale, the next sweep (15m later) drains the rest. No inner pagination loop.
	stale, err := m.st.Suggestions().ListPendingBefore(ctx, scope, before, batchSize)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/suggest-expire: list failed", "tenant", tenant, "err", err)
		return
	}
	if len(stale) == 0 {
		return
	}
	ids := make([]string, len(stale))
	for i, s := range stale {
		ids[i] = s.ID
	}
	now := time.Now().UnixMilli()
	if err := m.st.Suggestions().ExpirePending(ctx, scope, ids, now); err != nil {
		m.log.WarnContext(ctx, "lifecycle/suggest-expire: expire failed", "tenant", tenant, "err", err)
		return
	}
	// Audit trail (§8): one suggestion.expired event per GC'd offer.
	for _, s := range stale {
		_ = m.st.Events().Emit(ctx, scope, store.Event{
			ID: ulid.Make().String(), SessionID: s.SessionID,
			Type: "suggestion.expired", SubjectID: s.ID,
			Reason: "proactive offer expired (unresolved past TTL)", Payload: "{}", CreatedAt: now,
		})
	}
	m.log.InfoContext(ctx, "lifecycle/suggest-expire: expired stale offers", "tenant", tenant, "count", len(ids))
}
