package trust

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register sqlite driver
)

func openStore(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, config.StoreConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	return st
}

// assertReview parks a memory as pending_review via the shared assert core.
func assertReview(t *testing.T, st store.Store, scope identity.Scope, content string) string {
	t.Helper()
	res, err := reconcile.Assert(context.Background(), st, scope, reconcile.AssertParams{
		Action: "add", Content: content, Kind: "fact", Review: true,
	})
	if err != nil {
		t.Fatalf("assert review: %v", err)
	}
	if res.Status != "pending_review" {
		t.Fatalf("assert review should park pending_review, got %q", res.Status)
	}
	return res.MemoryID
}

func TestReview_ListAndApprove(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "rv-t"}
	ctx := context.Background()

	id := assertReview(t, st, scope, "an uncited agent claim")

	// Listed.
	items, _, err := ListPending(ctx, st, scope, 10, "")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("expected 1 pending item %s, got %+v", id, items)
	}

	// Approve → active.
	res, err := Resolve(ctx, st, scope, id, ReviewApprove)
	if err != nil {
		t.Fatalf("Resolve approve: %v", err)
	}
	if res.Status != "active" {
		t.Errorf("approve ⇒ active, got %q", res.Status)
	}
	mem, _ := st.Memories().Get(ctx, scope, id)
	if mem.Status != "active" {
		t.Errorf("memory should be active after approve, got %q", mem.Status)
	}
	// No longer pending.
	if items, _, _ := ListPending(ctx, st, scope, 10, ""); len(items) != 0 {
		t.Errorf("queue should be empty after approve, got %d", len(items))
	}
	// Audit event emitted.
	evs, _ := st.Events().ListBySubject(ctx, scope, id, 10)
	if !hasEvent(evs, "memory.review_approved") {
		t.Error("expected memory.review_approved event")
	}
}

func TestReview_Reject(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "rv-r"}
	ctx := context.Background()
	id := assertReview(t, st, scope, "a rejected claim")

	res, err := Resolve(ctx, st, scope, id, ReviewReject)
	if err != nil {
		t.Fatalf("Resolve reject: %v", err)
	}
	if res.Status != "quarantined" {
		t.Errorf("reject ⇒ quarantined, got %q", res.Status)
	}
	mem, _ := st.Memories().Get(ctx, scope, id)
	if mem.Status != "quarantined" {
		t.Errorf("memory should be quarantined after reject, got %q", mem.Status)
	}
	evs, _ := st.Events().ListBySubject(ctx, scope, id, 10)
	if !hasEvent(evs, "memory.review_rejected") {
		t.Error("expected memory.review_rejected event")
	}
}

func TestReview_ResolveNotPending(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "rv-np"}
	ctx := context.Background()
	// An active (not pending_review) memory.
	res, _ := reconcile.Assert(ctx, st, scope, reconcile.AssertParams{Action: "add", Content: "active mem", Kind: "fact"})
	if _, err := Resolve(ctx, st, scope, res.MemoryID, ReviewApprove); !errors.Is(err, ErrNotPending) {
		t.Errorf("resolving a non-pending memory should return ErrNotPending, got %v", err)
	}
}

func TestReview_ScopeIsolation(t *testing.T) {
	st := openStore(t)
	a := identity.Scope{Tenant: "rv-a"}
	b := identity.Scope{Tenant: "rv-b"}
	ctx := context.Background()
	id := assertReview(t, st, a, "tenant a's pending claim")

	// Tenant b sees nothing and cannot resolve a's memory.
	if items, _, _ := ListPending(ctx, st, b, 10, ""); len(items) != 0 {
		t.Errorf("cross-tenant queue leak: %d", len(items))
	}
	if _, err := Resolve(ctx, st, b, id, ReviewApprove); err == nil {
		t.Error("cross-tenant resolve should fail")
	}
}

func TestResolveCited(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "rc-t"}
	ctx := context.Background()
	// Seed a memory + an injection (citation handle) pointing at it.
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: "mem-1", Kind: "fact", Content: "Paris is the capital of France.", Status: "active",
		Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.Injections().Append(ctx, scope, []store.Injection{{
		ID: "cit-1", ResponseID: "resp-1", MemoryID: "mem-1", Rank: 0, Score: 0.9, CreatedAt: 1,
	}}); err != nil {
		t.Fatalf("append injection: %v", err)
	}

	cited, err := ResolveCited(ctx, st, scope, []string{"cit-1", "unknown-handle"})
	if err != nil {
		t.Fatalf("ResolveCited: %v", err)
	}
	if len(cited) != 1 || cited[0].ID != "mem-1" || cited[0].Content == "" {
		t.Fatalf("expected 1 resolved memory, got %+v", cited)
	}
	// Cross-tenant: another tenant resolves nothing.
	if c, _ := ResolveCited(ctx, st, identity.Scope{Tenant: "other"}, []string{"cit-1"}); len(c) != 0 {
		t.Errorf("cross-tenant citation leak: %+v", c)
	}
}

func hasEvent(evs []store.Event, typ string) bool {
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}
