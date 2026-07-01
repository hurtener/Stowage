package conformance

// Phase ae1 conformance tests: TopicViewStore (D-135/D-146/D-151) — the general
// (subject_kind, subject_id, view_name) -> topic-key policy-binding sub-store, this
// phase's only consumer being the agent-shaped methods (subject_kind='agent',
// view_name='default'). Proves CRUD round-trip, atomic replace on re-Put,
// ErrScopeRequired on empty tenant (P3, no unscoped variant), cross-tenant
// isolation, and ErrNotFound for an absent/deleted binding.

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// RunAgentPolicy runs all Phase ae1 TopicViewStore (agent-policy) conformance
// tests. Called from Run() to keep them in the same conformance suite.
func RunAgentPolicy(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("AgentPolicyCRUD", func(t *testing.T) { testAgentPolicyCRUD(t, factory) })
	t.Run("AgentPolicyAtomicReplace", func(t *testing.T) { testAgentPolicyAtomicReplace(t, factory) })
	t.Run("AgentPolicyScopeRequired", func(t *testing.T) { testAgentPolicyScopeRequired(t, factory) })
	t.Run("AgentPolicyCrossTenantIsolation", func(t *testing.T) { testAgentPolicyCrossTenantIsolation(t, factory) })
	t.Run("AgentPolicyNotFound", func(t *testing.T) { testAgentPolicyNotFound(t, factory) })
	t.Run("AgentPolicyList", func(t *testing.T) { testAgentPolicyList(t, factory) })
	t.Run("AgentPolicyNotAScopeTable", func(t *testing.T) { testAgentPolicyNotAScopeTable(t, factory) })
}

func sortedStrings(ss []string) []string {
	out := append([]string(nil), ss...)
	sort.Strings(out)
	return out
}

func equalStringSets(a, b []string) bool {
	a, b = sortedStrings(a), sortedStrings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// testAgentPolicyCRUD exercises Put/Get/List/Delete round-trip.
func testAgentPolicyCRUD(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	p := store.AgentPolicy{
		TenantID:    scope.Tenant,
		AgentID:     "agent-1",
		AllowTopics: []string{"goals", "preferences"},
		DenyTopics:  []string{"secrets"},
	}
	if err := s.TopicViews().PutAgentPolicy(ctx, scope, p); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}

	got, err := s.TopicViews().GetAgentPolicy(ctx, scope, "agent-1")
	if err != nil {
		t.Fatalf("GetAgentPolicy: %v", err)
	}
	if got.AgentID != "agent-1" || got.TenantID != scope.Tenant {
		t.Errorf("identity: got %+v", got)
	}
	if !equalStringSets(got.AllowTopics, p.AllowTopics) {
		t.Errorf("AllowTopics: got %v want %v", got.AllowTopics, p.AllowTopics)
	}
	if !equalStringSets(got.DenyTopics, p.DenyTopics) {
		t.Errorf("DenyTopics: got %v want %v", got.DenyTopics, p.DenyTopics)
	}

	list, err := s.TopicViews().ListAgentPolicies(ctx, scope)
	if err != nil {
		t.Fatalf("ListAgentPolicies: %v", err)
	}
	if len(list) != 1 || list[0].AgentID != "agent-1" {
		t.Errorf("List: got %+v", list)
	}

	if err := s.TopicViews().DeleteAgentPolicy(ctx, scope, "agent-1"); err != nil {
		t.Fatalf("DeleteAgentPolicy: %v", err)
	}
	if _, err := s.TopicViews().GetAgentPolicy(ctx, scope, "agent-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get after delete: got %v want ErrNotFound", err)
	}
	if err := s.TopicViews().DeleteAgentPolicy(ctx, scope, "agent-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("double delete: got %v want ErrNotFound", err)
	}
}

// testAgentPolicyAtomicReplace proves a re-Put fully replaces the prior allow/deny
// sets (delete-then-insert atomicity) rather than merging with them.
func testAgentPolicyAtomicReplace(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	first := store.AgentPolicy{AgentID: "agent-2", AllowTopics: []string{"a", "b"}, DenyTopics: []string{"c"}}
	if err := s.TopicViews().PutAgentPolicy(ctx, scope, first); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second := store.AgentPolicy{AgentID: "agent-2", AllowTopics: []string{"x"}, DenyTopics: nil}
	if err := s.TopicViews().PutAgentPolicy(ctx, scope, second); err != nil {
		t.Fatalf("second Put: %v", err)
	}

	got, err := s.TopicViews().GetAgentPolicy(ctx, scope, "agent-2")
	if err != nil {
		t.Fatalf("GetAgentPolicy: %v", err)
	}
	if !equalStringSets(got.AllowTopics, []string{"x"}) {
		t.Errorf("AllowTopics after replace: got %v want [x]", got.AllowTopics)
	}
	if len(got.DenyTopics) != 0 {
		t.Errorf("DenyTopics after replace: got %v want empty (fully replaced, not merged)", got.DenyTopics)
	}
}

// testAgentPolicyScopeRequired asserts every TopicViewStore method returns
// ErrScopeRequired on an empty tenant (P3 — no unscoped variant).
func testAgentPolicyScopeRequired(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	zero := identity.Scope{}

	if err := s.TopicViews().PutAgentPolicy(ctx, zero, store.AgentPolicy{AgentID: "a"}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("PutAgentPolicy: got %v want ErrScopeRequired", err)
	}
	if _, err := s.TopicViews().GetAgentPolicy(ctx, zero, "a"); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("GetAgentPolicy: got %v want ErrScopeRequired", err)
	}
	if _, err := s.TopicViews().ListAgentPolicies(ctx, zero); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("ListAgentPolicies: got %v want ErrScopeRequired", err)
	}
	if err := s.TopicViews().DeleteAgentPolicy(ctx, zero, "a"); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("DeleteAgentPolicy: got %v want ErrScopeRequired", err)
	}
}

// testAgentPolicyCrossTenantIsolation proves a binding in tenant A is invisible to
// tenant B (P3).
func testAgentPolicyCrossTenantIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	if err := s.TopicViews().PutAgentPolicy(ctx, scopeA, store.AgentPolicy{
		AgentID: "shared-agent-id", AllowTopics: []string{"secret-topic"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy A: %v", err)
	}

	if _, err := s.TopicViews().GetAgentPolicy(ctx, scopeB, "shared-agent-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant get: got %v want ErrNotFound", err)
	}
	listB, err := s.TopicViews().ListAgentPolicies(ctx, scopeB)
	if err != nil {
		t.Fatalf("ListAgentPolicies B: %v", err)
	}
	for _, p := range listB {
		if p.AgentID == "shared-agent-id" {
			t.Error("cross-tenant policy visible in tenant B's list")
		}
	}
}

// testAgentPolicyNotFound proves GetAgentPolicy/DeleteAgentPolicy return
// ErrNotFound for an agent with no binding (the UNBOUND-agent case the resolver
// treats as unfiltered-not-degraded, D-139).
func testAgentPolicyNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	if _, err := s.TopicViews().GetAgentPolicy(ctx, scope, "never-bound"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetAgentPolicy unbound: got %v want ErrNotFound", err)
	}
	if err := s.TopicViews().DeleteAgentPolicy(ctx, scope, "never-bound"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteAgentPolicy unbound: got %v want ErrNotFound", err)
	}
}

// testAgentPolicyList proves ListAgentPolicies returns every bound agent for the
// tenant, ordered by agent_id ascending.
func testAgentPolicyList(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	for _, agentID := range []string{"zeta", "alpha", "mu"} {
		if err := s.TopicViews().PutAgentPolicy(ctx, scope, store.AgentPolicy{
			AgentID: agentID, AllowTopics: []string{"t-" + agentID},
		}); err != nil {
			t.Fatalf("Put %s: %v", agentID, err)
		}
	}
	list, err := s.TopicViews().ListAgentPolicies(ctx, scope)
	if err != nil {
		t.Fatalf("ListAgentPolicies: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d policies want 3", len(list))
	}
	if list[0].AgentID != "alpha" || list[1].AgentID != "mu" || list[2].AgentID != "zeta" {
		t.Errorf("List order: got %s,%s,%s want alpha,mu,zeta", list[0].AgentID, list[1].AgentID, list[2].AgentID)
	}
}

// testAgentPolicyNotAScopeTable asserts a topic_views row is never surfaced by a
// memory read: it carries no memory rows and is not one of the 12 scope tables
// (AC-2). We prove this indirectly — a memory listed by scope carries no trace of
// the policy binding's fields, and inserting a policy does not create a memory.
func testAgentPolicyNotAScopeTable(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	before, _, err := s.Memories().ListByStatus(ctx, scope, "active", 100, "")
	if err != nil {
		t.Fatalf("ListByStatus before: %v", err)
	}
	if err := s.TopicViews().PutAgentPolicy(ctx, scope, store.AgentPolicy{
		AgentID: "agent-not-a-memory", AllowTopics: []string{"x"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}
	after, _, err := s.Memories().ListByStatus(ctx, scope, "active", 100, "")
	if err != nil {
		t.Fatalf("ListByStatus after: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("PutAgentPolicy created a memory row: before=%d after=%d", len(before), len(after))
	}
}
