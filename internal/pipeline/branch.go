package pipeline

// branch.go — the shared branch fork/merge/discard core (D-029, D-071).
//
// Branch control verbs are single-user (Tier A): reachable on {SDK, MCP, HTTP}.
// The HTTP handler (internal/api/branches_handler.go), the MCP memory_branch
// tool, and the embedded SDK all call these ONE set of functions so the surfaces
// cannot drift. Discard sets SkipPromotion via the branch-discard flush trigger
// (D-029) — a discarded branch's buffered turns are never promoted to memories.

import (
	"context"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// ForkBranch creates a new open branch for a session and returns its ID.
// sessionID is required.
func ForkBranch(ctx context.Context, st store.Store, scope identity.Scope, sessionID, parentBranchID string) (string, error) {
	if sessionID == "" {
		return "", fmt.Errorf("branch: session_id is required for fork")
	}
	now := time.Now().UnixMilli()
	br := store.Branch{
		ID:             ulid.Make().String(),
		SessionID:      sessionID,
		ParentBranchID: parentBranchID,
		Status:         "open",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := st.Branches().Create(ctx, scope, br); err != nil {
		return "", fmt.Errorf("branch: fork: %w", err)
	}
	return br.ID, nil
}

// MergeBranch transitions a branch to "merged". branchID is required.
// Returns store.ErrNotFound (wrapped) when the branch is absent.
func MergeBranch(ctx context.Context, st store.Store, scope identity.Scope, branchID string) error {
	if branchID == "" {
		return fmt.Errorf("branch: branch_id is required for merge")
	}
	if err := st.Branches().SetStatus(ctx, scope, branchID, "merged", time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("branch: merge: %w", err)
	}
	return nil
}

// DiscardBranch transitions a branch to "discarded" and flushes any buffers
// associated with it using the branch-discard trigger (SkipPromotion=true, D-029).
// Records remain readable (P1 fidelity). branchID is required. stage may be nil
// (no buffers to flush). The flush is synchronous here so embedded/SDK callers
// observe the SkipPromotion effect deterministically; the HTTP handler wraps it
// in a goroutine for fire-and-forget semantics.
func DiscardBranch(ctx context.Context, st store.Store, stage *Stage, scope identity.Scope, branchID string) error {
	if branchID == "" {
		return fmt.Errorf("branch: branch_id is required for discard")
	}
	if err := st.Branches().SetStatus(ctx, scope, branchID, "discarded", time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("branch: discard: %w", err)
	}
	if stage != nil {
		stage.FlushBranch(ctx, branchID)
	}
	return nil
}
