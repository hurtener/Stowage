package conformance

// Phase ae9 conformance tests: TopicViewStore named-view admin CRUD (D-149/D-151)
// — CreateView/UpdateView/DeleteView/ListViews/GetView over the SAME topic_views
// junction table ae1's agent-shaped methods already exercise (phase-ae1.go). Proves
// create→apply(get)→update→apply(get)→delete→list round-trip, ErrScopeRequired on
// every method (P3, no unscoped variant), UNIQUE conflict on a duplicate natural
// key, and cross-tenant isolation — run against BOTH drivers via conformance.Run.

import (
	"context"
	"errors"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// RunTopicViews runs all Phase ae9 TopicViewStore (named-view admin) conformance
// tests. Called from Run() to keep them in the same conformance suite.
func RunTopicViews(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("TopicViewLifecycle", func(t *testing.T) { testTopicViewLifecycle(t, factory) })
	t.Run("TopicViewCreateConflict", func(t *testing.T) { testTopicViewCreateConflict(t, factory) })
	t.Run("TopicViewUpdateNotFound", func(t *testing.T) { testTopicViewUpdateNotFound(t, factory) })
	t.Run("TopicViewDeleteNotFound", func(t *testing.T) { testTopicViewDeleteNotFound(t, factory) })
	t.Run("TopicViewScopeRequired", func(t *testing.T) { testTopicViewScopeRequired(t, factory) })
	t.Run("TopicViewCrossTenantIsolation", func(t *testing.T) { testTopicViewCrossTenantIsolation(t, factory) })
	t.Run("TopicViewListNarrowedBySubject", func(t *testing.T) { testTopicViewListNarrowedBySubject(t, factory) })
	t.Run("TopicViewValidateRejectsEmpty", func(t *testing.T) { testTopicViewValidateRejectsEmpty(t, factory) })
	t.Run("TopicViewKeySubject", func(t *testing.T) { testTopicViewKeySubject(t, factory) })
	t.Run("TopicViewCoexistsWithAgentPolicy", func(t *testing.T) { testTopicViewCoexistsWithAgentPolicy(t, factory) })
}

// testTopicViewLifecycle exercises the full create -> get -> update -> get ->
// delete -> list round-trip (AC-6).
func testTopicViewLifecycle(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	v := store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-9", ViewName: "work",
		AllowTopics: []string{"goals", "preferences"}, DenyTopics: []string{"secrets"},
	}
	if err := s.TopicViews().CreateView(ctx, scope, v); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	got, err := s.TopicViews().GetView(ctx, scope, "agent", "agent-9", "work")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.SubjectKind != "agent" || got.SubjectID != "agent-9" || got.ViewName != "work" {
		t.Errorf("identity: got %+v", got)
	}
	if !equalStringSets(got.AllowTopics, v.AllowTopics) {
		t.Errorf("AllowTopics: got %v want %v", got.AllowTopics, v.AllowTopics)
	}
	if !equalStringSets(got.DenyTopics, v.DenyTopics) {
		t.Errorf("DenyTopics: got %v want %v", got.DenyTopics, v.DenyTopics)
	}
	if got.CreatedAt == 0 || got.UpdatedAt == 0 {
		t.Errorf("timestamps not set: %+v", got)
	}

	// Update: fully replace allow/deny (delete-then-insert semantics — the
	// resulting row set must match the NEW lists exactly, not a merge).
	upd := store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-9", ViewName: "work",
		AllowTopics: []string{"only-this"},
	}
	if err := s.TopicViews().UpdateView(ctx, scope, upd); err != nil {
		t.Fatalf("UpdateView: %v", err)
	}
	got2, err := s.TopicViews().GetView(ctx, scope, "agent", "agent-9", "work")
	if err != nil {
		t.Fatalf("GetView after update: %v", err)
	}
	if !equalStringSets(got2.AllowTopics, []string{"only-this"}) {
		t.Errorf("AllowTopics after update: got %v want [only-this]", got2.AllowTopics)
	}
	if len(got2.DenyTopics) != 0 {
		t.Errorf("DenyTopics after update: got %v want empty (fully replaced, not merged)", got2.DenyTopics)
	}

	list, err := s.TopicViews().ListViews(ctx, scope, "", "")
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(list) != 1 || list[0].ViewName != "work" {
		t.Errorf("ListViews: got %+v", list)
	}

	if err := s.TopicViews().DeleteView(ctx, scope, "agent", "agent-9", "work"); err != nil {
		t.Fatalf("DeleteView: %v", err)
	}
	if _, err := s.TopicViews().GetView(ctx, scope, "agent", "agent-9", "work"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetView after delete: got %v want ErrNotFound", err)
	}
	listAfter, err := s.TopicViews().ListViews(ctx, scope, "", "")
	if err != nil {
		t.Fatalf("ListViews after delete: %v", err)
	}
	if len(listAfter) != 0 {
		t.Errorf("ListViews after delete: got %+v want empty", listAfter)
	}
}

// testTopicViewCreateConflict proves a second CreateView for the SAME natural
// key is rejected with ErrConflict — including when the second call's topic-key
// set does not overlap the first's (so the per-key UNIQUE index alone would not
// have caught it; the exists pre-check must).
func testTopicViewCreateConflict(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	first := store.TopicView{SubjectKind: "agent", SubjectID: "a1", ViewName: "v1", AllowTopics: []string{"x"}}
	if err := s.TopicViews().CreateView(ctx, scope, first); err != nil {
		t.Fatalf("first CreateView: %v", err)
	}
	// A disjoint topic-key set for the SAME natural key must still conflict.
	second := store.TopicView{SubjectKind: "agent", SubjectID: "a1", ViewName: "v1", AllowTopics: []string{"y"}}
	if err := s.TopicViews().CreateView(ctx, scope, second); !errors.Is(err, store.ErrConflict) {
		t.Errorf("second CreateView (disjoint keys, same natural key): got %v want ErrConflict", err)
	}
	// The first view's rows must be untouched.
	got, err := s.TopicViews().GetView(ctx, scope, "agent", "a1", "v1")
	if err != nil {
		t.Fatalf("GetView after rejected conflict: %v", err)
	}
	if !equalStringSets(got.AllowTopics, []string{"x"}) {
		t.Errorf("view mutated by a rejected conflicting CreateView: got %v want [x]", got.AllowTopics)
	}
}

// testTopicViewUpdateNotFound proves UpdateView on an absent natural key returns
// ErrNotFound (it does not silently create the view).
func testTopicViewUpdateNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	v := store.TopicView{SubjectKind: "key", SubjectID: "sk_absent", ViewName: "default", AllowTopics: []string{"x"}}
	if err := s.TopicViews().UpdateView(ctx, scope, v); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateView on absent view: got %v want ErrNotFound", err)
	}
	if _, err := s.TopicViews().GetView(ctx, scope, "key", "sk_absent", "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a failed UpdateView must not create the view: GetView got %v want ErrNotFound", err)
	}
}

// testTopicViewDeleteNotFound proves DeleteView on an absent/already-deleted
// natural key returns ErrNotFound.
func testTopicViewDeleteNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	if err := s.TopicViews().DeleteView(ctx, scope, "agent", "never-created", "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteView on absent view: got %v want ErrNotFound", err)
	}
}

// testTopicViewScopeRequired asserts every ae9 TopicViewStore method returns
// ErrScopeRequired on an empty tenant (P3 — no unscoped variant).
func testTopicViewScopeRequired(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	zero := identity.Scope{}
	v := store.TopicView{SubjectKind: "agent", SubjectID: "a", ViewName: "default", AllowTopics: []string{"x"}}

	if err := s.TopicViews().CreateView(ctx, zero, v); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("CreateView: got %v want ErrScopeRequired", err)
	}
	if err := s.TopicViews().UpdateView(ctx, zero, v); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("UpdateView: got %v want ErrScopeRequired", err)
	}
	if err := s.TopicViews().DeleteView(ctx, zero, "agent", "a", "default"); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("DeleteView: got %v want ErrScopeRequired", err)
	}
	if _, err := s.TopicViews().ListViews(ctx, zero, "", ""); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("ListViews: got %v want ErrScopeRequired", err)
	}
	if _, err := s.TopicViews().GetView(ctx, zero, "agent", "a", "default"); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("GetView: got %v want ErrScopeRequired", err)
	}
}

// testTopicViewCrossTenantIsolation proves a view in tenant A is invisible
// (Get/List) to tenant B, and that tenant B may create a view under the SAME
// natural key without conflict (P3, disjoint namespaces per tenant).
func testTopicViewCrossTenantIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	v := store.TopicView{SubjectKind: "agent", SubjectID: "shared-agent", ViewName: "default", AllowTopics: []string{"secret-topic"}}
	if err := s.TopicViews().CreateView(ctx, scopeA, v); err != nil {
		t.Fatalf("CreateView A: %v", err)
	}

	if _, err := s.TopicViews().GetView(ctx, scopeB, "agent", "shared-agent", "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant GetView: got %v want ErrNotFound", err)
	}
	listB, err := s.TopicViews().ListViews(ctx, scopeB, "", "")
	if err != nil {
		t.Fatalf("ListViews B: %v", err)
	}
	for _, got := range listB {
		if got.SubjectID == "shared-agent" {
			t.Error("cross-tenant view visible in tenant B's list")
		}
	}
	// Same natural key, different tenant — must NOT conflict (disjoint namespaces).
	if err := s.TopicViews().CreateView(ctx, scopeB, v); err != nil {
		t.Errorf("CreateView B (same natural key as A, different tenant): got %v want nil", err)
	}
}

// testTopicViewListNarrowedBySubject proves ListViews narrows to one subject
// when both subjectKind/subjectID are supplied, and returns every tenant view
// (ordered by created_at ASC) when they are omitted.
func testTopicViewListNarrowedBySubject(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	views := []store.TopicView{
		{SubjectKind: "agent", SubjectID: "a1", ViewName: "v1", AllowTopics: []string{"x"}},
		{SubjectKind: "agent", SubjectID: "a1", ViewName: "v2", AllowTopics: []string{"y"}},
		{SubjectKind: "key", SubjectID: "sk_1", ViewName: "default", AllowTopics: []string{"z"}},
	}
	for _, v := range views {
		if err := s.TopicViews().CreateView(ctx, scope, v); err != nil {
			t.Fatalf("CreateView %+v: %v", v, err)
		}
	}

	narrowed, err := s.TopicViews().ListViews(ctx, scope, "agent", "a1")
	if err != nil {
		t.Fatalf("ListViews narrowed: %v", err)
	}
	if len(narrowed) != 2 {
		t.Fatalf("narrowed: got %d views want 2", len(narrowed))
	}
	for _, v := range narrowed {
		if v.SubjectKind != "agent" || v.SubjectID != "a1" {
			t.Errorf("narrowed list leaked a different subject: %+v", v)
		}
	}

	all, err := s.TopicViews().ListViews(ctx, scope, "", "")
	if err != nil {
		t.Fatalf("ListViews all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all: got %d views want 3", len(all))
	}
	// created_at ASC: the first-created view (v1) must lead.
	if all[0].ViewName != "v1" || all[0].SubjectID != "a1" {
		t.Errorf("ListViews order: got first=%+v want v1/a1", all[0])
	}
}

// testTopicViewValidateRejectsEmpty proves CreateView/UpdateView reject a view
// with neither AllowTopics nor DenyTopics (ErrEmptyPolicy — the junction table
// has no way to represent an empty view; see (TopicView).Validate).
func testTopicViewValidateRejectsEmpty(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	empty := store.TopicView{SubjectKind: "agent", SubjectID: "a-empty", ViewName: "default"}
	if err := s.TopicViews().CreateView(ctx, scope, empty); !errors.Is(err, store.ErrEmptyPolicy) {
		t.Errorf("CreateView empty: got %v want ErrEmptyPolicy", err)
	}
	// Seed a real view, then prove an empty UpdateView is rejected too (no
	// silent wipe).
	seed := store.TopicView{SubjectKind: "agent", SubjectID: "a-empty2", ViewName: "default", AllowTopics: []string{"keep"}}
	if err := s.TopicViews().CreateView(ctx, scope, seed); err != nil {
		t.Fatalf("seed CreateView: %v", err)
	}
	if err := s.TopicViews().UpdateView(ctx, scope, store.TopicView{SubjectKind: "agent", SubjectID: "a-empty2", ViewName: "default"}); !errors.Is(err, store.ErrEmptyPolicy) {
		t.Errorf("UpdateView empty: got %v want ErrEmptyPolicy", err)
	}
	got, err := s.TopicViews().GetView(ctx, scope, "agent", "a-empty2", "default")
	if err != nil {
		t.Fatalf("GetView after rejected empty update: %v", err)
	}
	if !equalStringSets(got.AllowTopics, []string{"keep"}) {
		t.Errorf("view wiped by a rejected empty UpdateView: got %v want [keep]", got.AllowTopics)
	}
}

// testTopicViewKeySubject proves the "key" subject_kind round-trips exactly
// like "agent" (ae9's key-id-fallback subject, AC-3).
func testTopicViewKeySubject(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	v := store.TopicView{SubjectKind: "key", SubjectID: "sk_abc123", ViewName: "default", AllowTopics: []string{"k1"}}
	if err := s.TopicViews().CreateView(ctx, scope, v); err != nil {
		t.Fatalf("CreateView key subject: %v", err)
	}
	got, err := s.TopicViews().GetView(ctx, scope, "key", "sk_abc123", "default")
	if err != nil {
		t.Fatalf("GetView key subject: %v", err)
	}
	if got.SubjectKind != "key" || got.SubjectID != "sk_abc123" {
		t.Errorf("key subject identity: got %+v", got)
	}
}

// testTopicViewCoexistsWithAgentPolicy proves ae1's agent-shaped methods and
// ae9's named-view methods operate on the SAME underlying rows without
// interfering — ae1's PutAgentPolicy writes exactly the (agent, "default")
// view, which ae9's GetView/ListViews must see, and vice versa (D-151: one
// table, one seam).
func testTopicViewCoexistsWithAgentPolicy(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// ae1's PutAgentPolicy writes (subject_kind="agent", view_name="default").
	if err := s.TopicViews().PutAgentPolicy(ctx, scope, store.AgentPolicy{
		AgentID: "agent-coexist", AllowTopics: []string{"via-ae1"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}
	// ae9's GetView must see it as the ("agent", "default") view.
	viaAe9, err := s.TopicViews().GetView(ctx, scope, "agent", "agent-coexist", "default")
	if err != nil {
		t.Fatalf("GetView (ae1's row via ae9): %v", err)
	}
	if !equalStringSets(viaAe9.AllowTopics, []string{"via-ae1"}) {
		t.Errorf("ae9 GetView of ae1's row: got %v want [via-ae1]", viaAe9.AllowTopics)
	}

	// ae9's CreateView writes a NAMED (non-default) view for the same agent —
	// ae1's GetAgentPolicy (which only ever reads view_name="default") must be
	// unaffected by it.
	if err := s.TopicViews().CreateView(ctx, scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-coexist", ViewName: "work", AllowTopics: []string{"via-ae9"},
	}); err != nil {
		t.Fatalf("CreateView named view: %v", err)
	}
	viaAe1, err := s.TopicViews().GetAgentPolicy(ctx, scope, "agent-coexist")
	if err != nil {
		t.Fatalf("GetAgentPolicy after ae9 named view: %v", err)
	}
	if !equalStringSets(viaAe1.AllowTopics, []string{"via-ae1"}) {
		t.Errorf("ae1's default binding was disturbed by ae9's named view: got %v want [via-ae1]", viaAe1.AllowTopics)
	}
}
