package lifecycle

// confirmSweep promotes pending_confirmation memories whose TTL has elapsed.
// This is the fifth lifecycle sweep (Phase 18, D-065), complementing the four
// existing sweeps (decay, dedupe, rollup, re-enqueue). It follows the D-057
// advisory-lock + per-tenant pattern established by the other sweeps.
//
// A parked memory is eligible when:
//   - status = 'pending_confirmation'
//   - now - created_at >= ConfirmTTL (default 10 minutes)
//
// On promotion:
//   1. ActionConfirm CommitSet: memory.status → 'active'
//   2. If memory.supersedes_id is set, the target is superseded with
//      superseded_by_id = memory.ID (standard confirm path from D-065).
//   3. A memory.superseded event is emitted for each superseded target;
//      a lifecycle.confirm event is emitted for the promoted memory.

import (
	"context"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// confirmSweepLockKey is the advisory lock key for the confirm sweep (D-057).
const confirmSweepLockKey int64 = 0x1405

// confirmBatchPageSize is the page size for loading parked memories per pass.
const confirmBatchPageSize = 50

func (m *Manager) runConfirm(ctx context.Context) {
	release, err := m.st.Ops().AdvisoryLock(ctx, confirmSweepLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/confirm: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	tenants, err := m.st.Tenants(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/confirm: list tenants failed", "err", err)
		return
	}

	for _, tenant := range tenants {
		m.confirmTenant(ctx, tenant)
	}
}

func (m *Manager) confirmTenant(ctx context.Context, tenant string) {
	scope := identity.Scope{Tenant: tenant}
	ttl := m.profile.ConfirmTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	batchSize := m.profile.ConfirmBatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	repeats := m.profile.ConfirmRepeats
	if repeats <= 0 {
		repeats = 3
	}
	cutoff := time.Now().Add(-ttl).UnixMilli()

	processed := 0
	cursor := ""
	pageSize := confirmBatchPageSize

	for processed < batchSize {
		remaining := batchSize - processed
		if remaining < pageSize {
			pageSize = remaining
		}
		// ListByStatus fetches pending_confirmation memories ordered by created_at ASC.
		batch, next, err := m.st.Memories().ListByStatus(ctx, scope, "pending_confirmation", pageSize, cursor)
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/confirm: list failed",
				"tenant", tenant, "err", err)
			return
		}

		for _, mem := range batch {
			if processed >= batchSize {
				break
			}
			// Only promote memories whose TTL has elapsed.
			if mem.CreatedAt > cutoff {
				continue
			}
			m.promoteParked(ctx, scope, mem, repeats)
			processed++
		}

		if next == "" || len(batch) == 0 {
			break
		}
		cursor = next
	}
}

func (m *Manager) promoteParked(ctx context.Context, scope identity.Scope, mem store.Memory, _ int) {
	now := time.Now().UnixMilli()

	// Promote the parked memory to active.
	promoted := mem
	promoted.Status = "active"
	promoted.UpdatedAt = now

	// Look up the supersede target if any.
	var targets []store.Memory
	if mem.SupersedesID != "" {
		target, err := m.st.Memories().Get(ctx, scope, mem.SupersedesID)
		if err == nil && target.Status == "active" {
			targets = append(targets, *target)
		}
	}

	events := []store.Event{
		{
			ID:        ulid.Make().String(),
			Type:      "lifecycle.confirm",
			SubjectID: mem.ID,
			Reason:    "confirm sweep: parked TTL elapsed",
			Payload:   "{}",
			CreatedAt: now,
		},
		{
			ID:        ulid.Make().String(),
			Type:      "memory.confirmed",
			SubjectID: mem.ID,
			Reason:    "confirm sweep: parked TTL elapsed",
			Payload:   "{}",
			CreatedAt: now,
		},
	}
	for _, t := range targets {
		events = append(events, store.Event{
			ID:        ulid.Make().String(),
			Type:      "memory.superseded",
			SubjectID: t.ID,
			Reason:    "confirm sweep: superseded on parked promotion",
			Payload:   "{}",
			CreatedAt: now,
		})
	}

	cs := store.CommitSet{
		Action:  store.ActionConfirm,
		Memory:  promoted,
		Targets: targets,
		Events:  events,
		Scope:   scope,
	}
	if err := m.st.Memories().Commit(ctx, scope, cs); err != nil {
		m.log.WarnContext(ctx, "lifecycle/confirm: commit failed",
			"tenant", scope.Tenant, "memory_id", mem.ID, "err", err)
		return
	}
	m.log.InfoContext(ctx, "lifecycle/confirm: promoted parked memory",
		"tenant", scope.Tenant, "memory_id", mem.ID,
		"supersedes_id", mem.SupersedesID)
}
