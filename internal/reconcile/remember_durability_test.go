package reconcile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/pgstore"
	"github.com/oklog/ulid/v2"
)

func TestExplicitReceiptSurvivesStoreRestart(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = filepath.Join(t.TempDir(), "memory.db")
	open := func() store.Store {
		t.Helper()
		st, err := store.Open(ctx, cfg.Store)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := open()
	scope := identity.Scope{Tenant: "restart", User: "u"}
	explicitSource(t, st, scope, "source", "Use Go.", 100)
	req := reconcile.ExplicitRequest{SourceRecordID: "source", Quote: "Use Go.", IdempotencyKey: "persistent"}
	first, err := reconcile.Remember(ctx, st, scope, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	st = open()
	defer func() { _ = st.Close(ctx) }()
	second, err := reconcile.Remember(ctx, st, scope, req)
	if err != nil || !second.Replayed || first.MemoryID != second.MemoryID || first.ReceiptID != second.ReceiptID {
		t.Fatalf("receipt not durable: %+v %v", second, err)
	}
}

func TestCompetingCorrectionsDoNotFork(t *testing.T) {
	st, closeStore := newTestStore(t)
	defer closeStore()
	competingCorrections(t, st)
}

func TestExplicitCommandsPostgres(t *testing.T) {
	dsn := os.Getenv("STOWAGE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("STOWAGE_TEST_PG_DSN not configured")
	}
	ctx := context.Background()
	cfg := config.Defaults()
	cfg.Store.Driver = "postgres"
	cfg.Store.DSN = dsn
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close(ctx) }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	competingCorrections(t, st)
}

func competingCorrections(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	prefix := ulid.Make().String()
	scope := identity.Scope{Tenant: prefix, User: "owner"}
	explicitSource(t, st, scope, prefix+"-old", "Use Python.", 100)
	initial, err := reconcile.Remember(ctx, st, scope, reconcile.ExplicitRequest{SourceRecordID: prefix + "-old", Quote: "Use Python."})
	if err != nil {
		t.Fatal(err)
	}
	explicitSource(t, st, scope, prefix+"-go", "Use Go.", 200)
	explicitSource(t, st, scope, prefix+"-rust", "Use Rust.", 300)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	results := make(chan *reconcile.ExplicitReceipt, 2)
	start := make(chan struct{})
	for _, tc := range []struct{ id, quote string }{{"-go", "Use Go."}, {"-rust", "Use Rust."}} {
		wg.Add(1)
		go func(id, quote string) {
			defer wg.Done()
			<-start
			r, err := reconcile.Correct(ctx, st, scope, reconcile.ExplicitRequest{SourceRecordID: prefix + id, Quote: quote, MemoryID: initial.MemoryID, ExpectedRevision: initial.Revision, IdempotencyKey: id})
			if err != nil {
				errs <- err
				return
			}
			results <- r
		}(tc.id, tc.quote)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(results)
	if len(results) != 1 || len(errs) != 1 {
		t.Fatalf("expected one winner and one conflict; got %d/%d", len(results), len(errs))
	}
	for err := range errs {
		if !errors.Is(err, store.ErrCommandConflict) {
			t.Errorf("wrong conflict: %v", err)
		}
	}
	winner := <-results
	old, err := st.Memories().Get(ctx, scope, initial.MemoryID)
	if err != nil || old.SupersededByID != winner.MemoryID {
		t.Fatalf("forked replacement: %+v %v", old, err)
	}
	quote, id := "Use Go.", "-go"
	if winner.SourceRecordID == prefix+"-rust" {
		quote, id = "Use Rust.", "-rust"
	}
	replayed, err := reconcile.Correct(ctx, st, scope, reconcile.ExplicitRequest{SourceRecordID: prefix + id, Quote: quote, MemoryID: initial.MemoryID, ExpectedRevision: initial.Revision, IdempotencyKey: id})
	if err != nil || !replayed.Replayed || replayed.MemoryID != winner.MemoryID {
		t.Fatalf("winner not replayable: %+v %v", replayed, err)
	}
}
