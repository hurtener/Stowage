package trust

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
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

// fakeVerifyGateway returns a fixed entailed verdict.
type fakeVerifyGateway struct{}

func (fakeVerifyGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (fakeVerifyGateway) Complete(context.Context, gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	return gateway.CompleteResponse{JSON: []byte(`{"verdict":"entailed","confidence":0.88,"explanation":"x"}`)}, nil
}
func (fakeVerifyGateway) Probe(context.Context) error { return nil }
func (fakeVerifyGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}
func (fakeVerifyGateway) Close(context.Context) error { return nil }

// TestVerifyClaim_CapturesVerdict proves VerifyClaim emits a verify.verdict event keyed
// by the response_id the citations belong to (Phase 26 trace capture, D-086).
func TestVerifyClaim_CapturesVerdict(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "vc-t"}
	ctx := context.Background()
	// A memory + an injection (citation) for response "resp-9".
	if err := st.Memories().Insert(ctx, scope, store.Memory{ID: "m1", Kind: "fact", Content: "Paris is the capital.", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.Injections().Append(ctx, scope, []store.Injection{{ID: "cit-1", ResponseID: "resp-9", MemoryID: "m1", CreatedAt: 1}}); err != nil {
		t.Fatalf("inj: %v", err)
	}

	v, err := VerifyClaim(ctx, st, fakeVerifyGateway{}, scope, "Paris is the capital", []string{"cit-1"})
	if err != nil {
		t.Fatalf("VerifyClaim: %v", err)
	}
	if v.Verdict != "entailed" || v.Confidence != 0.88 {
		t.Fatalf("verdict wrong: %+v", v)
	}
	// The verify.verdict event is captured keyed by response_id.
	evs, _ := st.Events().ListBySubject(ctx, scope, "resp-9", 10)
	if !hasEvent(evs, "verify.verdict") {
		t.Errorf("expected a verify.verdict event keyed by response_id, got %+v", evs)
	}
}

// TestVerifyClaim_CapturesVerdictPerResponse proves that when a verify call mixes
// citations from MULTIPLE responses, EACH distinct response's trace records the verdict
// (A8, D-094) — not just the first (the prior simplification).
func TestVerifyClaim_CapturesVerdictPerResponse(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "vc-multi"}
	ctx := context.Background()
	// Two memories, each cited in a DIFFERENT response.
	if err := st.Memories().Insert(ctx, scope, store.Memory{ID: "m1", Kind: "fact", Content: "Paris is the capital.", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("insert m1: %v", err)
	}
	if err := st.Memories().Insert(ctx, scope, store.Memory{ID: "m2", Kind: "fact", Content: "France is in Europe.", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("insert m2: %v", err)
	}
	if err := st.Injections().Append(ctx, scope, []store.Injection{
		{ID: "cit-1", ResponseID: "resp-A", MemoryID: "m1", CreatedAt: 1},
		{ID: "cit-2", ResponseID: "resp-B", MemoryID: "m2", CreatedAt: 1},
	}); err != nil {
		t.Fatalf("inj: %v", err)
	}

	// A second citation for resp-A (same response) — must NOT double-count it.
	if err := st.Injections().Append(ctx, scope, []store.Injection{
		{ID: "cit-3", ResponseID: "resp-A", MemoryID: "m1", CreatedAt: 1},
	}); err != nil {
		t.Fatalf("inj cit-3: %v", err)
	}

	if _, err := VerifyClaim(ctx, st, fakeVerifyGateway{}, scope, "Paris is the capital of France in Europe", []string{"cit-1", "cit-2", "cit-3"}); err != nil {
		t.Fatalf("VerifyClaim: %v", err)
	}

	// EACH distinct response's trace must carry EXACTLY ONE verify.verdict event — the
	// seenResp dedup must not double-emit for resp-A despite its two citations (A8/D-094).
	for _, resp := range []string{"resp-A", "resp-B"} {
		evs, _ := st.Events().ListBySubject(ctx, scope, resp, 10)
		n := 0
		for _, e := range evs {
			if e.Type == "verify.verdict" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("response %s: expected exactly 1 verify.verdict event, got %d (%+v)", resp, n, evs)
		}
	}
}

// TestReview_RejectRollback proves D-117/audit #10: a review rejection (→ quarantined) is
// reversible — Rollback restores the memory to pending_review (the un-quarantine path that was
// silently dead because the event types weren't in isRestorable).
func TestReview_RejectRollback(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "rv-rb"}
	ctx := context.Background()
	id := assertReview(t, st, scope, "a claim to reject then undo")

	if _, err := Resolve(ctx, st, scope, id, ReviewReject); err != nil {
		t.Fatalf("Resolve reject: %v", err)
	}
	if mem, _ := st.Memories().Get(ctx, scope, id); mem.Status != "quarantined" {
		t.Fatalf("pre-rollback status = %q, want quarantined", mem.Status)
	}
	if _, err := reconcile.Rollback(ctx, st, scope, id); err != nil {
		t.Fatalf("Rollback review_rejected: %v", err)
	}
	mem, err := st.Memories().Get(ctx, scope, id)
	if err != nil {
		t.Fatalf("get after rollback: %v", err)
	}
	if mem.Status != "pending_review" {
		t.Errorf("after rollback status = %q, want pending_review (un-quarantined)", mem.Status)
	}
}

// TestReview_ApproveRollback is the regression guard for 29d N4: the approve→active
// inversion must round-trip too. Dropping memory.review_approved from isRestorable (or the
// rollback switch) leaves only the reject path tested; this pins the approve direction.
func TestReview_ApproveRollback(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "rv-rb-approve"}
	ctx := context.Background()
	id := assertReview(t, st, scope, "a claim to approve then undo")

	if _, err := Resolve(ctx, st, scope, id, ReviewApprove); err != nil {
		t.Fatalf("Resolve approve: %v", err)
	}
	if mem, _ := st.Memories().Get(ctx, scope, id); mem.Status != "active" {
		t.Fatalf("pre-rollback status = %q, want active", mem.Status)
	}
	if _, err := reconcile.Rollback(ctx, st, scope, id); err != nil {
		t.Fatalf("Rollback review_approved: %v", err)
	}
	mem, err := st.Memories().Get(ctx, scope, id)
	if err != nil {
		t.Fatalf("get after rollback: %v", err)
	}
	if mem.Status != "pending_review" {
		t.Errorf("after rollback status = %q, want pending_review (undo-approval)", mem.Status)
	}
}
