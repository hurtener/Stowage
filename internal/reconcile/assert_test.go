package reconcile_test

// assert_test.go — D-071: the direct memory-assert core (reconcile.Assert) shared
// by the MCP memory_assert tool and the embedded SDK Assert method.

import (
	"context"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
)

func TestAssert_AddUpdateDelete(t *testing.T) {
	st, done := newTestStore(t)
	defer done()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "assert-tenant"}

	// add
	add, err := reconcile.Assert(ctx, st, scope, reconcile.AssertParams{
		Action: "add", Content: "the sky is blue", Kind: "fact", Context: "weather",
	})
	if err != nil {
		t.Fatalf("Assert add: %v", err)
	}
	if add.MemoryID == "" || add.Status != "active" {
		t.Fatalf("Assert add unexpected: %+v", add)
	}
	got, err := st.Memories().Get(ctx, scope, add.MemoryID)
	if err != nil {
		t.Fatalf("Get after add: %v", err)
	}
	if got.Content != "the sky is blue" || got.Kind != "fact" {
		t.Errorf("added memory wrong: %+v", got)
	}

	// update
	upd, err := reconcile.Assert(ctx, st, scope, reconcile.AssertParams{
		Action: "update", MemoryID: add.MemoryID, Content: "the sky is grey",
	})
	if err != nil {
		t.Fatalf("Assert update: %v", err)
	}
	if upd.Status != "active" {
		t.Errorf("update status: %q", upd.Status)
	}
	got, _ = st.Memories().Get(ctx, scope, add.MemoryID)
	if got.Content != "the sky is grey" {
		t.Errorf("update did not apply: %q", got.Content)
	}

	// delete
	del, err := reconcile.Assert(ctx, st, scope, reconcile.AssertParams{
		Action: "delete", MemoryID: add.MemoryID,
	})
	if err != nil {
		t.Fatalf("Assert delete: %v", err)
	}
	if del.Status != "deleted" {
		t.Errorf("delete status: %q", del.Status)
	}
	got, _ = st.Memories().Get(ctx, scope, add.MemoryID)
	if got.Status != "deleted" {
		t.Errorf("delete did not apply: %q", got.Status)
	}
}

func TestAssert_DefaultsAndValidation(t *testing.T) {
	st, done := newTestStore(t)
	defer done()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "assert-val"}

	// add with no kind → defaults to "fact".
	add, err := reconcile.Assert(ctx, st, scope, reconcile.AssertParams{Action: "add", Content: "x"})
	if err != nil {
		t.Fatalf("Assert add default kind: %v", err)
	}
	got, _ := st.Memories().Get(ctx, scope, add.MemoryID)
	if got.Kind != "fact" {
		t.Errorf("default kind: want fact got %q", got.Kind)
	}

	cases := []reconcile.AssertParams{
		{Action: ""},                  // missing action
		{Action: "add"},               // add without content
		{Action: "update"},            // update without memory_id
		{Action: "delete"},            // delete without memory_id
		{Action: "bogus", Content: "y"}, // unknown action
	}
	for i, p := range cases {
		if _, err := reconcile.Assert(ctx, st, scope, p); err == nil {
			t.Errorf("case %d (%+v): expected error, got nil", i, p)
		}
	}

	// update of a missing memory errors.
	if _, err := reconcile.Assert(ctx, st, scope, reconcile.AssertParams{
		Action: "update", MemoryID: "01JXXXXXXXXXXXXXXXXXXXXXXX", Content: "z",
	}); err == nil {
		t.Error("update missing memory: expected error, got nil")
	}
}
