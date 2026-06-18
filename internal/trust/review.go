package trust

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// review.go is the gateway-free review-queue core (RFC §6c, D-084): list a scope's
// pending_review memories and approve (→active) or reject (→quarantined) them,
// reversibly via prior-state events (D-017). It must NOT import internal/gateway.

// ReviewAction is the resolution applied to a pending_review memory.
type ReviewAction string

const (
	// ReviewApprove promotes the parked memory to active (it becomes retrievable).
	ReviewApprove ReviewAction = "approve"
	// ReviewReject moves the parked memory to quarantined (held, reversible — not deleted).
	ReviewReject ReviewAction = "reject"
)

// ErrNotPending is returned by Resolve when the memory is not pending_review.
var ErrNotPending = errors.New("trust: memory is not pending_review")

// reviewMaxLimit caps a review-queue page.
const reviewMaxLimit = 100

// ReviewResult is the outcome of a resolve.
type ReviewResult struct {
	ID     string
	Status string // "active" (approve) | "quarantined" (reject)
}

// ListPending returns the scope's pending_review memories oldest-first (FIFO — the
// store ListByStatus order), scope-enforced (P3). limit ≤ 0 defaults to 20, capped at 100.
func ListPending(ctx context.Context, st store.Store, scope identity.Scope, limit int, cursor string) ([]store.Memory, string, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > reviewMaxLimit {
		limit = reviewMaxLimit
	}
	mems, next, err := st.Memories().ListByStatus(ctx, scope, "pending_review", limit, cursor)
	if err != nil {
		return nil, "", fmt.Errorf("trust: list pending_review: %w", err)
	}
	return mems, next, nil
}

// Resolve approves or rejects a pending_review memory, atomically + reversibly (the
// event carries the prior state; mirrors reconcile.Resolve). Approve ⇒ active +
// retrieval-cache invalidation; reject ⇒ quarantined. ErrNotPending when not parked.
func Resolve(ctx context.Context, st store.Store, scope identity.Scope, id string, action ReviewAction, inv ...reconcile.ScopeInvalidator) (*ReviewResult, error) {
	mem, err := st.Memories().Get(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	if mem.Status != "pending_review" {
		return nil, ErrNotPending
	}
	jt, _ := st.Memories().GetJunctions(ctx, scope, id)
	now := time.Now().UnixMilli()

	updated := *mem
	updated.UpdatedAt = now
	var evType, reason string
	switch action {
	case ReviewApprove:
		updated.Status = "active"
		evType, reason = "memory.review_approved", "trust: review approved → active"
	case ReviewReject:
		updated.Status = "quarantined"
		evType, reason = "memory.review_rejected", "trust: review rejected → quarantined"
	default:
		return nil, fmt.Errorf("trust: resolve: invalid action %q", action)
	}

	cs := store.CommitSet{
		Action: store.ActionConfirm, // full-row status update + events in one tx
		Memory: updated,
		Events: []store.Event{{
			ID: ulid.Make().String(), TenantID: scope.Tenant, Type: evType, SubjectID: id,
			Reason: reason, Payload: reconcile.MarshalPriorState(*mem, jt), CreatedAt: now,
		}},
		Scope: scope,
	}
	if err := st.Memories().Commit(ctx, scope, cs); err != nil {
		return nil, fmt.Errorf("trust: resolve commit: %w", err)
	}
	if action == ReviewApprove { // content becomes retrievable — bust stale results
		for _, in := range inv {
			if in != nil {
				in.InvalidateScope(scope)
			}
		}
	}
	return &ReviewResult{ID: id, Status: updated.Status}, nil
}
