package mcpserver

// metaintake_handlers_test.go — Phase ae2 (D-137/D-138) handler-level golden/
// regression coverage (AC-1..AC-5): additivity (a no-_meta call is
// byte-identical to today, for a read handler AND a mutate handler), the
// _meta-wins precedence, and the tenant-mismatch reject with a redacted
// reason. Uses server.WithRequestMeta(ctx, m) to inject _meta the way a
// dockyard host would.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// TestHandlerRetrieve_MetaIntake_Additivity (AC-5): a memory_retrieve call that
// injects no _meta resolves the exact same user-scoped result as an arg-only
// call did before ae2 — proving mi's zero value + metaElseArg("", arg) == arg
// leaves the resolved scope untouched.
func TestHandlerRetrieve_MetaIntake_Additivity(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)
	ctx := context.Background()

	userScope := identity.Scope{Tenant: testScope().Tenant, User: "user-a"}
	seedRetrievableMemory(t, svc.Store, userScope, "01MIRETADDAAAAAAAAAAAAAAA", "additivity fixture qzrx", "")

	// No _meta, user_id arg narrows exactly as it did pre-ae2.
	res, err := h(ctx, RetrieveInput{Query: "qzrx", UserID: "user-a", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(res.Structured.Items) != 1 {
		t.Fatalf("expected the user-a memory via the arg alone, got %d items", len(res.Structured.Items))
	}

	// A mismatched arg still fails to see it (scope isolation unaffected by ae2).
	res2, err := h(ctx, RetrieveInput{Query: "qzrx", UserID: "user-b", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve (other user): %v", err)
	}
	if len(res2.Structured.Items) != 0 {
		t.Fatalf("expected zero items for a non-matching user_id arg, got %d", len(res2.Structured.Items))
	}
}

// TestHandlerRetrieve_MetaIntake_Narrows (AC-1): _meta.user alone (no user_id
// arg) narrows a memory_retrieve read to that user's own scope.
func TestHandlerRetrieve_MetaIntake_Narrows(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)

	u1Scope := identity.Scope{Tenant: testScope().Tenant, User: "u1"}
	u2Scope := identity.Scope{Tenant: testScope().Tenant, User: "u2"}
	seedRetrievableMemory(t, svc.Store, u1Scope, "01MINARROWU1AAAAAAAAAAAAA", "narrow fixture qwbn", "")
	seedRetrievableMemory(t, svc.Store, u2Scope, "01MINARROWU2AAAAAAAAAAAAA", "narrow fixture qwbn", "")

	metaCtx := server.WithRequestMeta(context.Background(), map[string]any{"user": "u1"})
	res, err := h(metaCtx, RetrieveInput{Query: "qwbn", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve with _meta.user: %v", err)
	}
	if len(res.Structured.Items) != 1 || res.Structured.Items[0].ID != "01MINARROWU1AAAAAAAAAAAAA" {
		t.Fatalf("expected only u1's memory via _meta, got %+v", res.Structured.Items)
	}
}

// TestHandlerRetrieve_MetaIntake_Precedence (AC-2): when both user_id arg and
// _meta.user are present, the effective user is the _meta value.
func TestHandlerRetrieve_MetaIntake_Precedence(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)

	u1Scope := identity.Scope{Tenant: testScope().Tenant, User: "u1"}
	u2Scope := identity.Scope{Tenant: testScope().Tenant, User: "u2"}
	seedRetrievableMemory(t, svc.Store, u1Scope, "01MIPRECU1AAAAAAAAAAAAAAA", "precedence fixture qylm", "")
	seedRetrievableMemory(t, svc.Store, u2Scope, "01MIPRECU2AAAAAAAAAAAAAAA", "precedence fixture qylm", "")

	// arg says u2, _meta says u1 -> _meta wins.
	metaCtx := server.WithRequestMeta(context.Background(), map[string]any{"user": "u1"})
	res, err := h(metaCtx, RetrieveInput{Query: "qylm", UserID: "u2", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(res.Structured.Items) != 1 || res.Structured.Items[0].ID != "01MIPRECU1AAAAAAAAAAAAAAA" {
		t.Fatalf("expected _meta.user to win over the user_id arg, got %+v", res.Structured.Items)
	}
}

// TestHandlerRetrieve_MetaIntake_TenantMismatchRejects (AC-3): a _meta.tenant
// that disagrees with the credential tenant rejects with a redacted
// identity.ErrTenantMismatch — no tenant value in the surfaced error.
func TestHandlerRetrieve_MetaIntake_TenantMismatchRejects(t *testing.T) {
	svc := newFullServices(t) // ScopeFn pins "test-tenant"
	h := makeRetrieveHandler(svc)

	metaCtx := server.WithRequestMeta(context.Background(), map[string]any{"tenant": "attacker-tenant"})
	_, err := h(metaCtx, RetrieveInput{Query: "anything", Limit: 10})
	if err == nil {
		t.Fatal("expected a tenant-mismatch rejection")
	}
	if !errors.Is(err, identity.ErrTenantMismatch) {
		t.Errorf("error %v does not wrap identity.ErrTenantMismatch", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "attacker-tenant") || strings.Contains(msg, "test-tenant") {
		t.Errorf("surfaced error %q leaks a tenant value", msg)
	}
}

// TestHandlerRollback_MetaIntake_Additivity (AC-5, mutate): a memory_rollback
// call that injects no _meta resolves the same user-scoped target as an
// arg-only call did before ae2. Distinguishes scope-match from scope-mismatch
// via the underlying error shape (store.ErrNotFound on a scope miss vs
// reconcile.ErrNoPriorState once the record is found but has no event to
// invert — the record was seeded directly, without a restorable event).
func TestHandlerRollback_MetaIntake_Additivity(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeRollbackHandler(svc)
	ctx := context.Background()

	userScope := identity.Scope{Tenant: testScope().Tenant, User: "user-a"}
	if err := svc.Store.Memories().Insert(ctx, userScope, store.Memory{
		ID: "01MIROLLBACKAAAAAAAAAAAAA", Kind: "fact", Content: "rollback fixture", Status: "active",
		Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	// Matching arg, no _meta: scope resolves to user-a exactly as pre-ae2 ->
	// the record IS found (fails later on ErrNoPriorState, not ErrNotFound).
	_, err := h(ctx, RollbackInput{MemoryID: "01MIROLLBACKAAAAAAAAAAAAA", UserID: "user-a"})
	if err == nil {
		t.Fatal("expected an error (no restorable event) — the record must still be FOUND")
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Fatalf("scope resolution regressed: matching user_id arg should still find the record, got %v", err)
	}
	if !errors.Is(err, reconcile.ErrNoPriorState) {
		t.Errorf("expected ErrNoPriorState (record found, no restorable event), got %v", err)
	}

	// Non-matching arg, no _meta: scope isolation still holds (ErrNotFound).
	_, err = h(ctx, RollbackInput{MemoryID: "01MIROLLBACKAAAAAAAAAAAAA", UserID: "user-b"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound for a non-matching user_id arg, got %v", err)
	}
}

// TestHandlerRollback_MetaIntake_Precedence (AC-2, mutate): _meta.user
// overrides a non-matching user_id arg for memory_rollback's scope build too.
func TestHandlerRollback_MetaIntake_Precedence(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeRollbackHandler(svc)
	ctx := context.Background()

	userScope := identity.Scope{Tenant: testScope().Tenant, User: "user-a"}
	if err := svc.Store.Memories().Insert(ctx, userScope, store.Memory{
		ID: "01MIROLLPRECAAAAAAAAAAAAA", Kind: "fact", Content: "rollback precedence fixture", Status: "active",
		Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	metaCtx := server.WithRequestMeta(ctx, map[string]any{"user": "user-a"})
	_, err := h(metaCtx, RollbackInput{MemoryID: "01MIROLLPRECAAAAAAAAAAAAA", UserID: "user-b"})
	if errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected _meta.user to win over the mismatched user_id arg, got %v", err)
	}
	if !errors.Is(err, reconcile.ErrNoPriorState) {
		t.Errorf("expected ErrNoPriorState (record found via _meta.user), got %v", err)
	}
}

// TestHandlerRollback_MetaIntake_TenantMismatchRejects (AC-3, mutate): the
// D-138 guard runs on write/mutate handlers too.
func TestHandlerRollback_MetaIntake_TenantMismatchRejects(t *testing.T) {
	svc := newHandlerServices(t) // ScopeFn pins "test-tenant"
	h := makeRollbackHandler(svc)

	metaCtx := server.WithRequestMeta(context.Background(), map[string]any{"tenant": "attacker-tenant"})
	_, err := h(metaCtx, RollbackInput{MemoryID: "does-not-matter"})
	if !errors.Is(err, identity.ErrTenantMismatch) {
		t.Errorf("error %v does not wrap identity.ErrTenantMismatch", err)
	}
}
