package reconcile

// assert.go — the direct memory-assert core (D-071).
//
// memory_assert is a single-user, pipeline-bypassing escape hatch that adds,
// updates, or deletes a memory directly in the store. It is reachable on the
// MCP tool surface and the embedded SDK (Tier A, {SDK, MCP}); it is deliberately
// absent from the HTTP surface, which keeps writes routed through the ingest
// pipeline (D-071). Both the MCP handler and the SDK call this ONE core so the
// two surfaces cannot drift (the parity discipline of D-067/D-070).

import (
	"context"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// AssertParams carries the inputs for a direct memory assert.
type AssertParams struct {
	Action   string // "add" | "update" | "delete"
	MemoryID string // required for update/delete
	Content  string // required for add
	Kind     string // optional; defaults to "fact" on add
	Context  string // optional
	// Review (add only) parks the asserted memory as pending_review instead of active
	// — the uncited-claim safeguard (RFC §6c, Phase 25/D-084). A pending_review memory
	// is inert (not retrievable) until approved via the review queue.
	Review bool
}

// AssertResult is the outcome of an Assert.
type AssertResult struct {
	MemoryID string
	Action   string
	Status   string
}

// Assert applies a direct add/update/delete to a memory within scope, bypassing
// the ingestion pipeline. It is the shared core for the MCP memory_assert tool
// and the embedded SDK Assert method (D-071).
//
// Every assert action changes the active memory set, so on success the optional
// ScopeInvalidator(s) are invalidated in the core (D-053; D-070 Wave-B
// checkpoint) — surfaces pass their retrieval cache (or nothing) and none
// invalidates separately.
func Assert(ctx context.Context, st store.Store, scope identity.Scope, p AssertParams, inv ...ScopeInvalidator) (*AssertResult, error) {
	if p.Action == "" {
		return nil, fmt.Errorf("assert: action must be set (add|update|delete)")
	}

	now := time.Now().UnixMilli()
	var memoryID, status string

	switch p.Action {
	case "add":
		if p.Content == "" {
			return nil, fmt.Errorf("assert: content required for action=add")
		}
		kind := p.Kind
		if kind == "" {
			kind = "fact"
		}
		memoryID = ulid.Make().String()
		status = "active"
		if p.Review {
			status = "pending_review" // uncited-claim safeguard (§6c, D-084)
		}
		m := store.Memory{
			ID:        memoryID,
			TenantID:  scope.Tenant,
			Kind:      kind,
			Content:   p.Content,
			Context:   p.Context,
			Status:    status,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := st.Memories().Insert(ctx, scope, m); err != nil {
			return nil, fmt.Errorf("assert: insert: %w", err)
		}
		if p.Review {
			// Emit the pending_review audit event (best-effort; the insert is durable).
			_ = st.Events().Emit(ctx, scope, store.Event{
				ID: ulid.Make().String(), TenantID: scope.Tenant, Type: "memory.pending_review",
				SubjectID: memoryID, Reason: "assert: parked for review (uncited)", Payload: "{}", CreatedAt: now,
			})
		}

	case "update":
		if p.MemoryID == "" {
			return nil, fmt.Errorf("assert: memory_id required for action=update")
		}
		memoryID = p.MemoryID
		existing, err := st.Memories().Get(ctx, scope, memoryID)
		if err != nil {
			return nil, fmt.Errorf("assert: get memory: %w", err)
		}
		if p.Content != "" {
			existing.Content = p.Content
		}
		if p.Context != "" {
			existing.Context = p.Context
		}
		if p.Kind != "" {
			existing.Kind = p.Kind
		}
		existing.UpdatedAt = now
		if err := st.Memories().Update(ctx, scope, *existing); err != nil {
			return nil, fmt.Errorf("assert: update: %w", err)
		}
		status = existing.Status

	case "delete":
		if p.MemoryID == "" {
			return nil, fmt.Errorf("assert: memory_id required for action=delete")
		}
		memoryID = p.MemoryID
		if err := st.Memories().SetStatus(ctx, scope, memoryID, "deleted", now); err != nil {
			return nil, fmt.Errorf("assert: set status: %w", err)
		}
		status = "deleted"

	default:
		return nil, fmt.Errorf("assert: unknown action %q (want add|update|delete)", p.Action)
	}

	invalidateScopes(scope, inv)
	return &AssertResult{MemoryID: memoryID, Action: p.Action, Status: status}, nil
}
