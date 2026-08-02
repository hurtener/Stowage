package reconcile_test

// assert_test.go — D-071: the direct memory-assert core (reconcile.Assert) shared
// by the MCP memory_assert tool and the embedded SDK Assert method.

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

type recordingInvalidator struct {
	scopes []identity.Scope
}

func (r *recordingInvalidator) InvalidateScope(scope identity.Scope) {
	r.scopes = append(r.scopes, scope)
}

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
		{Action: ""},                    // missing action
		{Action: "add"},                 // add without content
		{Action: "update"},              // update without memory_id
		{Action: "delete"},              // delete without memory_id
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

func TestAssert_InvalidatesEveryObservableCacheScope(t *testing.T) {
	st, done := newTestStore(t)
	defer done()

	ctx := context.Background()
	writeScope := identity.Scope{
		Tenant: "assert-cache-tenant", Project: "project-a", User: "user-a", Session: "session-a",
	}
	readScopes := []identity.Scope{
		{Tenant: writeScope.Tenant},
		{Tenant: writeScope.Tenant, Project: writeScope.Project},
		{Tenant: writeScope.Tenant, User: writeScope.User},
		{Tenant: writeScope.Tenant, Project: writeScope.Project, User: writeScope.User},
		{Tenant: writeScope.Tenant, User: writeScope.User, Session: writeScope.Session},
		writeScope,
		{Tenant: writeScope.Tenant, Project: writeScope.Project, User: writeScope.User, Agent: "agent-a"},
		{Tenant: writeScope.Tenant, Project: writeScope.Project, User: writeScope.User, Session: writeScope.Session, Agent: "agent-a"},
	}
	otherTenant := identity.Scope{Tenant: "assert-cache-other", Agent: "agent-a"}
	cache := retrieval.NewResultCache(64)
	inv := &recordingInvalidator{}
	items := []retrieval.MemoryItem{{Memory: store.Memory{ID: "cached-memory"}}}

	prime := func(t *testing.T) {
		t.Helper()
		for _, scope := range readScopes {
			cache.Put(scope, "sig", "balanced", "session-a", 0, 0, nil, false, 5, items, retrieval.Support{})
		}
		cache.Put(otherTenant, "sig", "balanced", "session-a", 0, 0, nil, false, 5, items, retrieval.Support{})
	}
	assertBusted := func(t *testing.T) {
		t.Helper()
		for _, scope := range readScopes {
			if _, _, ok := cache.Get(scope, "sig", "balanced", "session-a", 0, 0, nil, false, 5); ok {
				t.Errorf("cache remained visible for scope %+v", scope)
			}
		}
		if _, _, ok := cache.Get(otherTenant, "sig", "balanced", "session-a", 0, 0, nil, false, 5); !ok {
			t.Error("tenant-wide assert invalidation crossed the tenant boundary")
		}
	}

	prime(t)
	add, err := reconcile.Assert(ctx, st, writeScope, reconcile.AssertParams{
		Action: "add", Content: "assert cache invalidation probe",
	}, cache, inv)
	if err != nil {
		t.Fatalf("Assert add: %v", err)
	}
	assertBusted(t)

	prime(t)
	if _, err := reconcile.Assert(ctx, st, writeScope, reconcile.AssertParams{
		Action: "update", MemoryID: add.MemoryID, Content: "updated cache invalidation probe",
	}, cache, inv); err != nil {
		t.Fatalf("Assert update: %v", err)
	}
	assertBusted(t)

	prime(t)
	if _, err := reconcile.Assert(ctx, st, writeScope, reconcile.AssertParams{
		Action: "delete", MemoryID: add.MemoryID,
	}, cache, inv); err != nil {
		t.Fatalf("Assert delete: %v", err)
	}
	assertBusted(t)

	want := identity.Scope{Tenant: writeScope.Tenant}
	if len(inv.scopes) != 3 {
		t.Fatalf("invalidations=%d want one per successful add/update/delete", len(inv.scopes))
	}
	for i, got := range inv.scopes {
		if got != want {
			t.Errorf("invalidation[%d]=%+v want tenant-only %+v", i, got, want)
		}
	}

	before := slices.Clone(inv.scopes)
	if _, err := reconcile.Assert(ctx, st, writeScope, reconcile.AssertParams{
		Action: "bogus", MemoryID: add.MemoryID,
	}, cache, inv); err == nil {
		t.Fatal("invalid assert action unexpectedly succeeded")
	}
	if !slices.Equal(before, inv.scopes) {
		t.Fatal("failed assert invalidated cache before a successful mutation")
	}
}

func TestAssert_DeleteImmediatelyMissesSameSessionAgentCache(t *testing.T) {
	st, done := newTestStore(t)
	defer done()

	ctx := context.Background()
	writeScope := identity.Scope{
		Tenant: "assert-delete-tenant", Project: "project-a", User: "user-a", Session: "session-a",
	}
	readScope := writeScope
	readScope.Agent = "agent-a"

	gw := &stubGateway{}
	ret := retrieval.New(
		st.Memories(), st.Records(), vindex.New(st.Vectors(), 4, "assert-cache-test"), gw,
		slog.New(slog.DiscardHandler),
	)
	defer ret.Close()

	const query = "same session deletion cache probe"
	add, err := reconcile.Assert(ctx, st, writeScope, reconcile.AssertParams{
		Action: "add", Content: query,
	}, ret.Cache())
	if err != nil {
		t.Fatalf("Assert add: %v", err)
	}
	req := retrieval.Request{Query: query, Limit: 5, SessionID: writeScope.Session}

	first, err := ret.Retrieve(ctx, readScope, req)
	if err != nil {
		t.Fatalf("prime retrieve: %v", err)
	}
	if first.CacheHit || len(first.Items) != 1 || first.Items[0].Memory.ID != add.MemoryID {
		t.Fatalf("prime retrieve=%+v want one uncached asserted memory", first)
	}
	second, err := ret.Retrieve(ctx, readScope, req)
	if err != nil {
		t.Fatalf("cached retrieve: %v", err)
	}
	if !second.CacheHit {
		t.Fatal("identical same-session agent read did not prime the cache")
	}

	if _, err := reconcile.Assert(ctx, st, writeScope, reconcile.AssertParams{
		Action: "delete", MemoryID: add.MemoryID,
	}, ret.Cache()); err != nil {
		t.Fatalf("Assert delete: %v", err)
	}
	after, err := ret.Retrieve(ctx, readScope, req)
	if err != nil {
		t.Fatalf("post-delete retrieve: %v", err)
	}
	if after.CacheHit {
		t.Fatal("identical same-session post-delete read hit the stale cache")
	}
	if len(after.Items) != 0 {
		t.Fatalf("identical same-session post-delete read returned %d items, want zero", len(after.Items))
	}
}
