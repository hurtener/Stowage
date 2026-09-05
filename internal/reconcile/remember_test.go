package reconcile_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

func explicitSource(t *testing.T, st store.Store, scope identity.Scope, id, text string, at int64) {
	t.Helper()
	if err := st.Records().Append(context.Background(), scope, []store.Record{{ID: id, Role: "user", Content: text, OccurredAt: at, CreatedAt: at}}); err != nil { t.Fatal(err) }
}

func TestRememberEvidenceAndReplay(t *testing.T) {
	st, closeStore := newTestStore(t); defer closeStore()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t", Project: "p", User: "u", Session: "s"}
	explicitSource(t, st, scope, "src", "Prefiero café. Keep authentication in Pengui.", 100)
	req := reconcile.ExplicitRequest{SourceRecordID: "src", Quote: "Keep authentication in Pengui.", Kind: "decision", IdempotencyKey: "save-1"}
	r, err := reconcile.Remember(ctx, st, scope, req)
	if err != nil { t.Fatal(err) }
	if r.Outcome != "saved" || !r.RetrievalEligible || r.Replayed { t.Fatalf("bad receipt: %+v", r) }
	j, err := st.Memories().GetJunctions(ctx, scope, r.MemoryID)
	if err != nil || len(j.Provenance) != 1 || j.Provenance[0].RecordID != "src" || j.Provenance[0].SpanStart != len("Prefiero café. ") { t.Fatalf("bad evidence: %+v %v", j, err) }
	again, err := reconcile.Remember(ctx, st, scope, req)
	if err != nil || !again.Replayed || again.MemoryID != r.MemoryID || again.ReceiptID != r.ReceiptID { t.Fatalf("bad replay: %+v %v", again, err) }
	req.Quote = "Prefiero café."
	if _, err := reconcile.Remember(ctx, st, scope, req); !errors.Is(err, reconcile.ErrIdempotencyConflict) { t.Fatalf("different body accepted: %v", err) }
	req.Quote = "Keep authentication in Pengui."
	if err := st.Memories().SetStatus(ctx, scope, r.MemoryID, "deleted", 200); err != nil { t.Fatal(err) }
	again, err = reconcile.Remember(ctx, st, scope, req)
	if err != nil || again.RetrievalEligible || again.CurrentStatus != "deleted" { t.Fatalf("replay revived deleted memory: %+v %v", again, err) }
}

func TestRememberRejectsFabricatedAndForeignEvidence(t *testing.T) {
	st, closeStore := newTestStore(t); defer closeStore()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t", User: "u"}
	explicitSource(t, st, scope, "user", "actual user text", 100)
	foreign := identity.Scope{Tenant: "t", User: "other"}
	explicitSource(t, st, foreign, "foreign", "actual user text", 100)
	if err := st.Records().Append(ctx, scope, []store.Record{{ID: "assistant", Role: "assistant", Content: "actual user text", OccurredAt: 100, CreatedAt: 100}}); err != nil { t.Fatal(err) }
	for _, tc := range []struct{id, quote string}{{"user", "invented"}, {"foreign", "actual user text"}, {"assistant", "actual user text"}, {"missing", "text"}} {
		if _, err := reconcile.Remember(ctx, st, scope, reconcile.ExplicitRequest{SourceRecordID: tc.id, Quote: tc.quote}); !errors.Is(err, reconcile.ErrInvalidEvidence) { t.Errorf("%s accepted or wrong error: %v", tc.id, err) }
	}
}

func TestRememberConcurrentSameKey(t *testing.T) {
	st, closeStore := newTestStore(t); defer closeStore()
	ctx := context.Background(); scope := identity.Scope{Tenant: "t", User: "u"}
	explicitSource(t, st, scope, "src", "Use Go.", 100)
	req := reconcile.ExplicitRequest{SourceRecordID: "src", Quote: "Use Go.", IdempotencyKey: "one"}
	var wg sync.WaitGroup
	ids := make(chan string, 16); errs := make(chan error, 16)
	for range 16 { wg.Add(1); go func() { defer wg.Done(); r, err := reconcile.Remember(ctx, st, scope, req); if err != nil { errs <- err; return }; ids <- r.MemoryID }() }
	wg.Wait(); close(ids); close(errs)
	for err := range errs { t.Error(err) }
	first := ""; for id := range ids { if first == "" { first = id }; if id != first { t.Error("duplicate memory") } }
	if first == "" { t.Fatal("no committed receipt") }
	j, err := st.Memories().GetJunctions(ctx, scope, first)
	if err != nil || len(j.Provenance) != 1 { t.Fatalf("duplicate provenance: %+v %v", j, err) }
}

func TestCorrectPreservesHistoryAndRejectsStaleRevision(t *testing.T) {
	st, closeStore := newTestStore(t); defer closeStore()
	ctx := context.Background(); scope := identity.Scope{Tenant: "t", User: "u"}
	explicitSource(t, st, scope, "old", "Use Python.", 100)
	a, err := reconcile.Remember(ctx, st, scope, reconcile.ExplicitRequest{SourceRecordID: "old", Quote: "Use Python."}); if err != nil { t.Fatal(err) }
	explicitSource(t, st, scope, "new", "Use Go instead.", 200)
	req := reconcile.ExplicitRequest{SourceRecordID: "new", Quote: "Use Go instead.", MemoryID: a.MemoryID, ExpectedRevision: a.Revision, IdempotencyKey: "correct-1"}
	b, err := reconcile.Correct(ctx, st, scope, req); if err != nil { t.Fatal(err) }
	old, err := st.Memories().Get(ctx, scope, a.MemoryID)
	if err != nil || old.Status != "superseded" || old.SupersededByID != b.MemoryID { t.Fatalf("history lost: %+v %v", old, err) }
	if _, err := reconcile.Correct(ctx, st, scope, req); err != nil { t.Fatalf("correction replay failed: %v", err) }
	req.IdempotencyKey = "different"
	if _, err := reconcile.Correct(ctx, st, scope, req); !errors.Is(err, store.ErrCommandConflict) { t.Fatalf("stale correction accepted: %v", err) }
	if _, err := reconcile.Rollback(ctx, st, scope, a.MemoryID); err != nil { t.Fatalf("not reversible: %v", err) }
	old, err = st.Memories().Get(ctx, scope, a.MemoryID)
	if err != nil || old.Status != "active" || old.Content != "Use Python." { t.Fatalf("wrong rollback: %+v %v", old, err) }
}
