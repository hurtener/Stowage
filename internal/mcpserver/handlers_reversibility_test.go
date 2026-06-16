package mcpserver

// Tests for the D-070 reversibility tools: memory_get, memory_rollback,
// memory_resolve. They exercise the make*Handler factories directly against a
// real sqlite store, mirroring the HTTP/SDK behavior they share via the
// reconcile core.

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

func seedActiveMemory(t *testing.T, st store.Store, scope identity.Scope, content string) store.Memory {
	t.Helper()
	now := time.Now().UnixMilli()
	mem := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     content,
		Status:      "active",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: ulid.Make().String(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	cs := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   mem,
		Entities: []string{"ent"},
		Keywords: []string{"kw"},
		Events: []store.Event{{
			ID: ulid.Make().String(), Type: "memory.added",
			SubjectID: mem.ID, Payload: "{}", CreatedAt: now,
		}},
		Scope: scope,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	got, _ := st.Memories().Get(context.Background(), scope, mem.ID)
	return *got
}

func TestHandlerGet(t *testing.T) {
	svc := newHandlerServices(t)
	scope := testScope()
	mem := seedActiveMemory(t, svc.Store, scope, "mcp get content")

	h := makeGetHandler(svc)
	res, err := h(context.Background(), GetInput{MemoryID: mem.ID})
	if err != nil {
		t.Fatalf("memory_get: %v", err)
	}
	if res.Structured.Memory.ID != mem.ID {
		t.Errorf("id: got %q want %q", res.Structured.Memory.ID, mem.ID)
	}
	if len(res.Structured.Entities) == 0 {
		t.Error("expected entities")
	}

	// Validation + not-found error paths.
	if _, err := h(context.Background(), GetInput{}); err == nil {
		t.Error("empty memory_id: expected error")
	}
	if _, err := h(context.Background(), GetInput{MemoryID: "ghost"}); err == nil {
		t.Error("unknown memory_id: expected error")
	}
}

func TestHandlerRollback(t *testing.T) {
	svc := newHandlerServices(t)
	scope := testScope()
	ctx := context.Background()
	mem := seedActiveMemory(t, svc.Store, scope, "mcp rollback content")

	// Simulate an update: write a prior-state event then mutate.
	jt, _ := svc.Store.Memories().GetJunctions(ctx, scope, mem.ID)
	if err := svc.Store.Events().Emit(ctx, scope, store.Event{
		ID: ulid.Make().String(), Type: "memory.updated", SubjectID: mem.ID,
		Payload: reconcile.MarshalPriorState(mem, jt), CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	upd := mem
	upd.Content = "mutated"
	upd.UpdatedAt = time.Now().UnixMilli()
	if err := svc.Store.Memories().Update(ctx, scope, upd); err != nil {
		t.Fatalf("update: %v", err)
	}

	h := makeRollbackHandler(svc)
	res, err := h(ctx, RollbackInput{MemoryID: mem.ID})
	if err != nil {
		t.Fatalf("memory_rollback: %v", err)
	}
	if res.Structured.Memory.Content != mem.Content {
		t.Errorf("restored content: got %q want %q", res.Structured.Memory.Content, mem.Content)
	}

	// Double rollback → conflict error across the MCP boundary.
	if _, err := h(ctx, RollbackInput{MemoryID: mem.ID}); err == nil {
		t.Error("double rollback: expected conflict error")
	}
	// Validation.
	if _, err := h(ctx, RollbackInput{}); err == nil {
		t.Error("empty memory_id: expected error")
	}
}

func TestHandlerResolve(t *testing.T) {
	svc := newHandlerServices(t)
	scope := testScope()
	ctx := context.Background()

	now := time.Now().UnixMilli()
	parked := store.Memory{
		ID: ulid.Make().String(), TenantID: scope.Tenant, Kind: "fact",
		Content: "mcp parked", Status: "pending_confirmation",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: ulid.Make().String(),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.Store.Memories().Insert(ctx, scope, parked); err != nil {
		t.Fatalf("insert parked: %v", err)
	}

	h := makeResolveHandler(svc)
	res, err := h(ctx, ResolveInput{MemoryID: parked.ID, Action: "confirm"})
	if err != nil {
		t.Fatalf("memory_resolve confirm: %v", err)
	}
	if res.Structured.Status != "active" {
		t.Errorf("status: got %q want active", res.Structured.Status)
	}

	// Validation / error paths.
	if _, err := h(ctx, ResolveInput{MemoryID: parked.ID, Action: "explode"}); err == nil {
		t.Error("bad action: expected error")
	}
	if _, err := h(ctx, ResolveInput{Action: "confirm"}); err == nil {
		t.Error("empty memory_id: expected error")
	}
	if _, err := h(ctx, ResolveInput{MemoryID: "ghost", Action: "reject"}); err == nil {
		t.Error("unknown memory_id: expected error")
	}
}
