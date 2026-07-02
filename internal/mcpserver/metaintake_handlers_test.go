package mcpserver

// metaintake_handlers_test.go — Phase ae2 (D-137/D-138) handler-level golden/
// regression coverage (AC-1..AC-5): the _meta-only narrowing, and the
// tenant-mismatch reject with a redacted reason. Uses
// server.WithRequestMeta(ctx, m) to inject _meta the way a dockyard host
// would.
//
// ae2b (D-140/M1) retired the RetrieveInput.UserID/RollbackInput.UserID args
// these tests originally exercised alongside _meta to prove the ae2
// arg-vs-_meta precedence rule. With the arg gone there is nothing left to
// take precedence over, so the former "additivity"/"precedence" scenarios
// below are rewritten as pure _meta-only narrowing/isolation checks — the
// _meta seam itself (readMetaIdentity/resolveScope) is otherwise unchanged by
// ae2b and still deserves this handler-level coverage.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// TestHandlerRetrieve_MetaOnly_Isolates (post-ae2b): a memory_retrieve call
// narrows via _meta.user alone (the arg no longer exists) and a mismatched
// _meta.user still fails to see another user's memory (scope isolation).
func TestHandlerRetrieve_MetaOnly_Isolates(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)

	userScope := identity.Scope{Tenant: testScope().Tenant, User: "user-a"}
	seedRetrievableMemory(t, svc.Store, userScope, "01MIRETADDAAAAAAAAAAAAAAA", "additivity fixture qzrx", "")

	// _meta.user=user-a narrows to the seeded memory.
	metaA := server.WithRequestMeta(context.Background(), map[string]any{"user": "user-a"})
	res, err := h(metaA, RetrieveInput{Query: "qzrx", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(res.Structured.Items) != 1 {
		t.Fatalf("expected the user-a memory via _meta.user, got %d items", len(res.Structured.Items))
	}

	// _meta.user=user-b still fails to see it (scope isolation).
	metaB := server.WithRequestMeta(context.Background(), map[string]any{"user": "user-b"})
	res2, err := h(metaB, RetrieveInput{Query: "qzrx", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve (other user): %v", err)
	}
	if len(res2.Structured.Items) != 0 {
		t.Fatalf("expected zero items for a non-matching _meta.user, got %d", len(res2.Structured.Items))
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

// TestHandlerRetrieve_MetaProject_Narrows (ae2b, M1, AC-4): _meta.project
// alone — the ONLY remaining MCP channel for project narrowing now that the
// project_id arg is retired — narrows a memory_retrieve read to that
// project's own scope, proving _meta.project reaches
// identity.IdentitySources.MetaProject end to end through resolveScope.
func TestHandlerRetrieve_MetaProject_Narrows(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)

	p1Scope := identity.Scope{Tenant: testScope().Tenant, Project: "p1"}
	p2Scope := identity.Scope{Tenant: testScope().Tenant, Project: "p2"}
	seedRetrievableMemory(t, svc.Store, p1Scope, "01MIPROJP1AAAAAAAAAAAAAAA", "project fixture qzpj", "")
	seedRetrievableMemory(t, svc.Store, p2Scope, "01MIPROJP2AAAAAAAAAAAAAAA", "project fixture qzpj", "")

	metaCtx := server.WithRequestMeta(context.Background(), map[string]any{"project": "p1"})
	res, err := h(metaCtx, RetrieveInput{Query: "qzpj", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve with _meta.project: %v", err)
	}
	if len(res.Structured.Items) != 1 || res.Structured.Items[0].ID != "01MIPROJP1AAAAAAAAAAAAAAA" {
		t.Fatalf("expected only p1's memory via _meta.project, got %+v", res.Structured.Items)
	}
}

// TestHandlerRetrieve_LegacyUserIDArgIsSilentlyDropped proves the ae2b Design
// "honest residual note": encoding/json silently discards an unknown field on
// Unmarshal, so a hypothetical legacy caller that still sends a raw
// {"user_id": "u2", ...} body gets NO error — the field is dropped on the wire
// before it ever reaches the handler, and only _meta governs the resolved
// scope. This replaces the pre-ae2b arg-vs-_meta precedence test, which
// exercised a field (RetrieveInput.UserID) that no longer exists.
func TestHandlerRetrieve_LegacyUserIDArgIsSilentlyDropped(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)

	u1Scope := identity.Scope{Tenant: testScope().Tenant, User: "u1"}
	u2Scope := identity.Scope{Tenant: testScope().Tenant, User: "u2"}
	seedRetrievableMemory(t, svc.Store, u1Scope, "01MIPRECU1AAAAAAAAAAAAAAA", "precedence fixture qylm", "")
	seedRetrievableMemory(t, svc.Store, u2Scope, "01MIPRECU2AAAAAAAAAAAAAAA", "precedence fixture qylm", "")

	// A raw wire body still carrying "user_id":"u2" (a hypothetical legacy
	// caller) decodes cleanly — the unknown field is silently dropped.
	var in RetrieveInput
	raw := []byte(`{"query":"qylm","limit":10,"user_id":"u2"}`)
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("unmarshal legacy body: %v", err)
	}

	// _meta.user=u1 is the only channel left; the dropped user_id has no effect.
	metaCtx := server.WithRequestMeta(context.Background(), map[string]any{"user": "u1"})
	res, err := h(metaCtx, in)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(res.Structured.Items) != 1 || res.Structured.Items[0].ID != "01MIPRECU1AAAAAAAAAAAAAAA" {
		t.Fatalf("expected _meta.user=u1 to govern (legacy user_id silently dropped), got %+v", res.Structured.Items)
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

// TestHandlerRollback_MetaOnly_Isolates (mutate): a memory_rollback call
// narrows via _meta.user alone (the arg no longer exists). Distinguishes
// scope-match from scope-mismatch via the underlying error shape
// (store.ErrNotFound on a scope miss vs reconcile.ErrNoPriorState once the
// record is found but has no event to invert — the record was seeded
// directly, without a restorable event).
func TestHandlerRollback_MetaOnly_Isolates(t *testing.T) {
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

	// _meta.user=user-a resolves to user-a's scope -> the record IS found
	// (fails later on ErrNoPriorState, not ErrNotFound).
	metaA := server.WithRequestMeta(ctx, map[string]any{"user": "user-a"})
	_, err := h(metaA, RollbackInput{MemoryID: "01MIROLLBACKAAAAAAAAAAAAA"})
	if err == nil {
		t.Fatal("expected an error (no restorable event) — the record must still be FOUND")
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Fatalf("scope resolution regressed: matching _meta.user should still find the record, got %v", err)
	}
	if !errors.Is(err, reconcile.ErrNoPriorState) {
		t.Errorf("expected ErrNoPriorState (record found, no restorable event), got %v", err)
	}

	// _meta.user=user-b: scope isolation still holds (ErrNotFound).
	metaB := server.WithRequestMeta(ctx, map[string]any{"user": "user-b"})
	_, err = h(metaB, RollbackInput{MemoryID: "01MIROLLBACKAAAAAAAAAAAAA"})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound for a non-matching _meta.user, got %v", err)
	}
}

// TestHandlerRollback_LegacyUserIDArgIsSilentlyDropped (mutate) mirrors
// TestHandlerRetrieve_LegacyUserIDArgIsSilentlyDropped for the mutate path: a
// raw legacy {"user_id": "user-b", ...} body decodes with the field silently
// dropped (encoding/json), so _meta.user alone governs the resolved scope.
func TestHandlerRollback_LegacyUserIDArgIsSilentlyDropped(t *testing.T) {
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

	var in RollbackInput
	raw := []byte(`{"memory_id":"01MIROLLPRECAAAAAAAAAAAAA","user_id":"user-b"}`)
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("unmarshal legacy body: %v", err)
	}

	metaCtx := server.WithRequestMeta(ctx, map[string]any{"user": "user-a"})
	_, err := h(metaCtx, in)
	if errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected _meta.user=user-a to govern (legacy user_id silently dropped), got %v", err)
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
