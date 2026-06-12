package lifecycle

// confirmSweep promotes pending_confirmation memories that are eligible.
// This is the fifth lifecycle sweep (Phase 18, D-065), complementing the four
// existing sweeps (decay, dedupe, rollup, re-enqueue). It follows the D-057
// advisory-lock + per-tenant pattern established by the other sweeps.
//
// A parked memory is eligible when EITHER:
//   - age >= ConfirmTTL (default 72 h) — OQ-4 lean-yes: the newer memory wins
//     after the review window lapses; or
//   - match_count >= ConfirmRepeats (default 2) — repeated independent extraction
//     is confirmation.
//
// On promotion the SUPERSEDE path is used (D-065): the target's memory.superseded
// event carries MarshalPriorState so every auto-resolution is itself reversible
// via D-064 rollback. Trust gates are NOT re-applied: TTL/threshold/human action
// IS the gate's resolution.

import (
	"context"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
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
		ttl = 72 * time.Hour
	}
	batchSize := m.profile.ConfirmBatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	repeats := m.profile.ConfirmRepeats
	if repeats <= 0 {
		repeats = 2
	}
	ttlCutoff := time.Now().Add(-ttl).UnixMilli()

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
			// Eligible when TTL elapsed OR repeated-extraction threshold reached (D-065).
			ttlElapsed := mem.CreatedAt <= ttlCutoff
			repeatsReached := mem.MatchCount >= int64(repeats)
			if !ttlElapsed && !repeatsReached {
				continue
			}
			reason := "parked TTL elapsed"
			if repeatsReached && !ttlElapsed {
				reason = "repeated independent extraction"
			}
			m.promoteParked(ctx, scope, mem, reason)
			processed++
		}

		if next == "" || len(batch) == 0 {
			break
		}
		cursor = next
	}
}

// promoteParked promotes a single pending_confirmation memory to active via
// the supersede path (D-065). reason is "parked TTL elapsed" or
// "repeated independent extraction" — it is threaded into event Reason fields.
func (m *Manager) promoteParked(ctx context.Context, scope identity.Scope, mem store.Memory, reason string) {
	now := time.Now().UnixMilli()

	// Promote the parked memory to active.
	promoted := mem
	promoted.Status = "active"
	promoted.UpdatedAt = now

	// Look up the supersede target if any. If the target is gone or not active,
	// promote as a plain activate (no tombstone) — the target was already resolved.
	var targets []store.Memory
	var targetJT store.MemoryJunctions
	if mem.SupersedesID != "" {
		target, fetchErr := m.st.Memories().Get(ctx, scope, mem.SupersedesID)
		if fetchErr == nil && target.Status == "active" {
			targets = append(targets, *target)
			targetJT, _ = m.st.Memories().GetJunctions(ctx, scope, target.ID)
		}
	}

	sweepReason := "confirm sweep: " + reason
	events := []store.Event{
		{
			ID:        ulid.Make().String(),
			Type:      "lifecycle.confirm",
			SubjectID: mem.ID,
			Reason:    sweepReason,
			Payload:   "{}",
			CreatedAt: now,
		},
		{
			ID:        ulid.Make().String(),
			Type:      "memory.confirmed",
			SubjectID: mem.ID,
			Reason:    sweepReason,
			Payload:   "{}",
			CreatedAt: now,
		},
	}
	// D-065: memory.superseded carries MarshalPriorState so the promotion is
	// itself reversible via D-064 rollback.
	for _, t := range targets {
		events = append(events, store.Event{
			ID:        ulid.Make().String(),
			Type:      "memory.superseded",
			SubjectID: t.ID,
			Reason:    sweepReason,
			Payload:   reconcile.MarshalPriorState(t, targetJT),
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
		"supersedes_id", mem.SupersedesID, "reason", reason)
}
