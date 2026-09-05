package reconcile_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

func TestExplicitValidationAndExistingContent(t *testing.T) {
	st, closeStore := newTestStore(t)
	defer closeStore()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "explicit-validation", User: "u"}
	explicitSource(t, st, scope, "source", "Use Go.", 100)
	base := reconcile.ExplicitRequest{SourceRecordID: "source", Quote: "Use Go."}
	for _, change := range []func(*reconcile.ExplicitRequest){
		func(r *reconcile.ExplicitRequest) { r.Kind = "unsupported" },
		func(r *reconcile.ExplicitRequest) { r.Quote = strings.Repeat("x", 8193) },
		func(r *reconcile.ExplicitRequest) { r.Quote = "\xff" },
		func(r *reconcile.ExplicitRequest) { r.IdempotencyKey = strings.Repeat("x", 257) },
		func(r *reconcile.ExplicitRequest) { r.MemoryID = "wrong-operation" },
	} {
		req := base
		change(&req)
		if _, err := reconcile.Remember(ctx, st, scope, req); !errors.Is(err, reconcile.ErrInvalidCommand) {
			t.Errorf("invalid request accepted: %v", err)
		}
	}
	if _, err := reconcile.Remember(ctx, st, identity.Scope{}, base); !errors.Is(err, store.ErrScopeRequired) {
		t.Fatal(err)
	}
	if _, err := reconcile.Correct(ctx, st, scope, base); !errors.Is(err, reconcile.ErrInvalidCommand) {
		t.Fatal(err)
	}
	invalidRevision := base
	invalidRevision.MemoryID = "target"
	invalidRevision.ExpectedRevision = strings.Repeat("z", 64)
	if _, err := reconcile.Correct(ctx, st, scope, invalidRevision); !errors.Is(err, reconcile.ErrInvalidCommand) {
		t.Fatal(err)
	}
	first, err := reconcile.Remember(ctx, st, scope, base)
	if err != nil {
		t.Fatal(err)
	}
	base.IdempotencyKey = "another-command"
	second, err := reconcile.Remember(ctx, st, scope, base)
	if err != nil || second.Outcome != "already_present" || second.MemoryID != first.MemoryID {
		t.Fatalf("exact existing content duplicated: %+v %v", second, err)
	}
	explicitSource(t, st, scope, "reaffirmed", "Use Go.", 200)
	correction := reconcile.ExplicitRequest{SourceRecordID: "reaffirmed", Quote: "Use Go.", MemoryID: first.MemoryID, ExpectedRevision: first.Revision}
	unchanged, err := reconcile.Correct(ctx, st, scope, correction)
	if err != nil || unchanged.Outcome != "already_present" || unchanged.MemoryID != first.MemoryID {
		t.Fatalf("identical correction changed history: %+v %v", unchanged, err)
	}
	explicitSource(t, st, scope, "older", "Use Rust.", 50)
	correction.SourceRecordID = "older"
	correction.Quote = "Use Rust."
	if _, err := reconcile.Correct(ctx, st, scope, correction); !errors.Is(err, reconcile.ErrInvalidEvidence) {
		t.Fatalf("older evidence replaced newer memory: %v", err)
	}
}
