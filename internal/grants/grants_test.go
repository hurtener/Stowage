package grants_test

// grants_test.go — unit tests for the grants package (Phase 15).
//
// Coverage targets (AC-8): ≥85 % of internal/grants.
//
// Test categories:
//   - ApplyCeiling (AC-1 defense predicate).
//   - Service.CreateGrant validation (AC-2: cross-tenant, zone_ceiling, access).
//   - Service contribute-mode (AC-4: ErrNotCovered, ErrCrossTenantGrant).
//   - Service group and grant CRUD + event emission.
//   - CheckContributeGrant end-to-end with in-memory store.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// ---- ApplyCeiling tests (AC-1 defense predicate) --------------------------------

func TestApplyCeiling_EmptyCeiling(t *testing.T) {
	mems := []store.Memory{
		{ID: "1", PrivacyZone: "public"},
		{ID: "2", PrivacyZone: "work"},
		{ID: "3", PrivacyZone: "personal"},
		{ID: "4", PrivacyZone: "intimate"},
	}
	got := grants.ApplyCeiling(mems, "")
	if len(got) != 4 {
		t.Errorf("empty ceiling: want 4, got %d", len(got))
	}
}

func TestApplyCeiling_PublicCeiling(t *testing.T) {
	mems := []store.Memory{
		{ID: "1", PrivacyZone: "public"},
		{ID: "2", PrivacyZone: "work"},
		{ID: "3", PrivacyZone: "personal"},
		{ID: "4", PrivacyZone: "intimate"},
	}
	got := grants.ApplyCeiling(mems, "public")
	if len(got) != 1 {
		t.Errorf("public ceiling: want 1, got %d", len(got))
	}
	if got[0].PrivacyZone != "public" {
		t.Errorf("public ceiling: got zone %q, want public", got[0].PrivacyZone)
	}
}

func TestApplyCeiling_WorkCeiling(t *testing.T) {
	mems := []store.Memory{
		{ID: "1", PrivacyZone: "public"},
		{ID: "2", PrivacyZone: "work"},
		{ID: "3", PrivacyZone: "personal"},
		{ID: "4", PrivacyZone: "intimate"},
	}
	got := grants.ApplyCeiling(mems, "work")
	if len(got) != 2 {
		t.Errorf("work ceiling: want 2, got %d", len(got))
	}
	for _, m := range got {
		if m.PrivacyZone == "personal" || m.PrivacyZone == "intimate" {
			t.Errorf("AC-1 violated: %q crossed work ceiling", m.PrivacyZone)
		}
	}
}

func TestApplyCeiling_PersonalCeiling_CapToWork(t *testing.T) {
	// personal ceiling is hard-capped to work: personal and intimate NEVER cross grants.
	mems := []store.Memory{
		{ID: "1", PrivacyZone: "public"},
		{ID: "2", PrivacyZone: "work"},
		{ID: "3", PrivacyZone: "personal"},
		{ID: "4", PrivacyZone: "intimate"},
	}
	got := grants.ApplyCeiling(mems, "personal")
	for _, m := range got {
		if m.PrivacyZone == "personal" || m.PrivacyZone == "intimate" {
			t.Errorf("AC-1 hard-cap violated: %q should not cross from personal ceiling", m.PrivacyZone)
		}
	}
	// Should return at most public+work (2 rows).
	if len(got) > 2 {
		t.Errorf("personal ceiling (capped to work): want ≤2, got %d", len(got))
	}
}

func TestApplyCeiling_UnknownCeiling(t *testing.T) {
	mems := []store.Memory{{ID: "1", PrivacyZone: "public"}}
	got := grants.ApplyCeiling(mems, "bogus")
	if len(got) != 0 {
		t.Errorf("unknown ceiling: want 0, got %d", len(got))
	}
}

func TestApplyCeiling_UnknownZone(t *testing.T) {
	mems := []store.Memory{
		{ID: "1", PrivacyZone: "unknown_zone"},
		{ID: "2", PrivacyZone: "public"},
	}
	got := grants.ApplyCeiling(mems, "work")
	// unknown_zone is skipped (defense-in-depth); public passes.
	if len(got) != 1 || got[0].ID != "2" {
		t.Errorf("unknown zone: want only public row, got %v", got)
	}
}

// ---- Service.CreateGrant validation (AC-2) --------------------------------------

func TestCreateGrant_CrossTenantRejected(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()

	callerScope := identity.Scope{Tenant: "tenant-A"}
	in := grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "tenant-B"}, // different tenant
		GroupID:     "g1",
		Access:      "read",
		ZoneCeiling: "work",
	}
	_, err := svc.CreateGrant(ctx, callerScope, in)
	if !errors.Is(err, grants.ErrCrossTenantGrant) {
		t.Errorf("want ErrCrossTenantGrant, got %v", err)
	}
}

func TestCreateGrant_InvalidZoneCeiling(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()

	callerScope := identity.Scope{Tenant: "t1"}
	for _, ceil := range []string{"personal", "intimate", "", "PUBLIC"} {
		in := grants.CreateGrantInput{
			OwnerScope:  identity.Scope{Tenant: "t1"},
			GroupID:     "g1",
			Access:      "read",
			ZoneCeiling: ceil,
		}
		_, err := svc.CreateGrant(ctx, callerScope, in)
		if !errors.Is(err, grants.ErrInvalidZoneCeiling) {
			t.Errorf("ceiling %q: want ErrInvalidZoneCeiling, got %v", ceil, err)
		}
	}
}

func TestCreateGrant_ValidZoneCeilings(t *testing.T) {
	ctx := context.Background()
	for _, ceil := range []string{"public", "work"} {
		st := newMockGrantStore()
		svc := grants.New(st, nil, noopLog())
		callerScope := identity.Scope{Tenant: "t1"}
		in := grants.CreateGrantInput{
			OwnerScope:  identity.Scope{Tenant: "t1"},
			GroupID:     "g1",
			Access:      "read",
			ZoneCeiling: ceil,
		}
		g, err := svc.CreateGrant(ctx, callerScope, in)
		if err != nil {
			t.Errorf("ceiling %q: unexpected error %v", ceil, err)
		}
		if g == nil || g.ZoneCeiling != ceil {
			t.Errorf("ceiling %q: grant not created correctly", ceil)
		}
	}
}

func TestCreateGrant_InvalidAccess(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()

	callerScope := identity.Scope{Tenant: "t1"}
	in := grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1"},
		GroupID:     "g1",
		Access:      "admin", // invalid
		ZoneCeiling: "work",
	}
	_, err := svc.CreateGrant(ctx, callerScope, in)
	if !errors.Is(err, grants.ErrInvalidAccess) {
		t.Errorf("want ErrInvalidAccess, got %v", err)
	}
}

func TestCreateGrant_ValidAccess(t *testing.T) {
	ctx := context.Background()
	for _, access := range []string{"read", "contribute"} {
		st := newMockGrantStore()
		svc := grants.New(st, nil, noopLog())
		callerScope := identity.Scope{Tenant: "t1"}
		in := grants.CreateGrantInput{
			OwnerScope:  identity.Scope{Tenant: "t1"},
			GroupID:     "g1",
			Access:      access,
			ZoneCeiling: "public",
		}
		g, err := svc.CreateGrant(ctx, callerScope, in)
		if err != nil {
			t.Errorf("access %q: unexpected error %v", access, err)
		}
		if g == nil || g.Access != access {
			t.Errorf("access %q: grant not created correctly", access)
		}
	}
}

// ---- Service group CRUD ---------------------------------------------------------

func TestCreateGroup_ScopeRequired(t *testing.T) {
	svc := grants.New(newMockGrantStore(), nil, noopLog())
	_, err := svc.CreateGroup(context.Background(), identity.Scope{}, "my-group")
	if !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("want ErrScopeRequired, got %v", err)
	}
}

func TestCreateGroup_OK(t *testing.T) {
	svc := grants.New(newMockGrantStore(), nil, noopLog())
	g, err := svc.CreateGroup(context.Background(), identity.Scope{Tenant: "t1"}, "eng")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if g.Name != "eng" || g.TenantID != "t1" || g.ID == "" {
		t.Errorf("unexpected group: %+v", g)
	}
}

func TestListGroups_ReturnsOwnTenantOnly(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()

	_, err := svc.CreateGroup(ctx, identity.Scope{Tenant: "A"}, "grpA")
	if err != nil {
		t.Fatalf("CreateGroup A: %v", err)
	}
	_, err = svc.CreateGroup(ctx, identity.Scope{Tenant: "B"}, "grpB")
	if err != nil {
		t.Fatalf("CreateGroup B: %v", err)
	}

	grps, err := svc.ListGroups(ctx, identity.Scope{Tenant: "A"})
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	for _, g := range grps {
		if g.TenantID != "A" {
			t.Errorf("cross-tenant group %q visible", g.TenantID)
		}
	}
}

// ---- Service grant CRUD (RevokeGrant, ListGrants, GetGrant) ---------------------

func TestRevokeGrant_OK(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()

	callerScope := identity.Scope{Tenant: "t1"}
	g, err := svc.CreateGrant(ctx, callerScope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1"},
		GroupID:     "g1",
		Access:      "read",
		ZoneCeiling: "work",
	})
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	if err := svc.RevokeGrant(ctx, callerScope, g.ID); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}

	// Second revoke should return ErrNotFound.
	if err := svc.RevokeGrant(ctx, callerScope, g.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("double revoke: want ErrNotFound, got %v", err)
	}
}

func TestListGrants_ReturnsAll(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	for i := 0; i < 3; i++ {
		_, err := svc.CreateGrant(ctx, scope, grants.CreateGrantInput{
			OwnerScope:  scope,
			GroupID:     "g1",
			Access:      "read",
			ZoneCeiling: "public",
		})
		if err != nil {
			t.Fatalf("CreateGrant %d: %v", i, err)
		}
	}

	list, err := svc.ListGrants(ctx, scope)
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("want 3 grants, got %d", len(list))
	}
}

// ---- CheckContributeGrant (AC-4) -----------------------------------------------

func TestCheckContributeGrant_CrossTenant(t *testing.T) {
	svc := grants.New(newMockGrantStore(), nil, noopLog())
	err := svc.CheckContributeGrant(
		context.Background(),
		identity.Scope{Tenant: "A"},
		identity.Scope{Tenant: "B"},
		"user1",
	)
	if !errors.Is(err, grants.ErrCrossTenantGrant) {
		t.Errorf("want ErrCrossTenantGrant, got %v", err)
	}
}

func TestCheckContributeGrant_EmptyCallerUser(t *testing.T) {
	svc := grants.New(newMockGrantStore(), nil, noopLog())
	err := svc.CheckContributeGrant(
		context.Background(),
		identity.Scope{Tenant: "A"},
		identity.Scope{Tenant: "A", User: "target"},
		"", // no caller user → cannot be in any group
	)
	if !errors.Is(err, grants.ErrNotCovered) {
		t.Errorf("want ErrNotCovered, got %v", err)
	}
}

func TestCheckContributeGrant_Covered(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	// Create group, add contributor.
	g, err := svc.CreateGroup(ctx, scope, "writers")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	_, err = svc.AddMember(ctx, scope, g.ID, "alice")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	// Create a contribute grant for target scope.
	_, err = svc.CreateGrant(ctx, scope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1", User: "bob"},
		GroupID:     g.ID,
		Access:      "contribute",
		ZoneCeiling: "work",
	})
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	// Alice (member of writers group) should be covered.
	err = svc.CheckContributeGrant(
		ctx,
		scope,
		identity.Scope{Tenant: "t1", User: "bob"},
		"alice",
	)
	if err != nil {
		t.Errorf("CheckContributeGrant covered: want nil, got %v", err)
	}
}

func TestCheckContributeGrant_NotMember(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	g, err := svc.CreateGroup(ctx, scope, "writers")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	// alice is in the group, charlie is not.
	_, err = svc.AddMember(ctx, scope, g.ID, "alice")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	_, err = svc.CreateGrant(ctx, scope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1", User: "bob"},
		GroupID:     g.ID,
		Access:      "contribute",
		ZoneCeiling: "public",
	})
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}

	err = svc.CheckContributeGrant(
		ctx,
		scope,
		identity.Scope{Tenant: "t1", User: "bob"},
		"charlie", // not a member
	)
	if !errors.Is(err, grants.ErrNotCovered) {
		t.Errorf("want ErrNotCovered for non-member, got %v", err)
	}
}

func TestCheckContributeGrant_ReadGrantDoesNotCover(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	g, _ := svc.CreateGroup(ctx, scope, "readers")
	_, _ = svc.AddMember(ctx, scope, g.ID, "alice")
	_, _ = svc.CreateGrant(ctx, scope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1", User: "bob"},
		GroupID:     g.ID,
		Access:      "read", // read only — does not cover contribute
		ZoneCeiling: "work",
	})

	err := svc.CheckContributeGrant(
		ctx,
		scope,
		identity.Scope{Tenant: "t1", User: "bob"},
		"alice",
	)
	if !errors.Is(err, grants.ErrNotCovered) {
		t.Errorf("read grant should not cover contribute, got %v", err)
	}
}

func TestCheckContributeGrant_RevokedGrantNotCovered(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	g, _ := svc.CreateGroup(ctx, scope, "writers")
	_, _ = svc.AddMember(ctx, scope, g.ID, "alice")
	gr, _ := svc.CreateGrant(ctx, scope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1", User: "bob"},
		GroupID:     g.ID,
		Access:      "contribute",
		ZoneCeiling: "public",
	})

	// Revoke the grant.
	if err := svc.RevokeGrant(ctx, scope, gr.ID); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}

	err := svc.CheckContributeGrant(
		ctx,
		scope,
		identity.Scope{Tenant: "t1", User: "bob"},
		"alice",
	)
	if !errors.Is(err, grants.ErrNotCovered) {
		t.Errorf("revoked grant should not cover, got %v", err)
	}
}

// ---- Event emission (smoke: no panic on nil EventStore) --------------------------

func TestService_NilEventStore_NoPanic(t *testing.T) {
	// nil EventStore is explicitly supported (emitEvent is a no-op).
	svc := grants.New(newMockGrantStore(), nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	if _, err := svc.CreateGroup(ctx, scope, "g"); err != nil {
		t.Fatalf("CreateGroup with nil events: %v", err)
	}
}

// ---- EffectiveScopes delegation --------------------------------------------------

func TestEffectiveScopes_DelegatestoStore(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()

	callerScope := identity.Scope{Tenant: "t1", User: "u1"}
	scopes, err := svc.EffectiveScopes(ctx, callerScope)
	if err != nil {
		t.Fatalf("EffectiveScopes: %v", err)
	}
	if len(scopes) < 1 {
		t.Error("EffectiveScopes: want ≥1 scope (own), got 0")
	}
}

// ---- AddMember / RemoveMember ----------------------------------------------------

func TestAddRemoveMember(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	g, err := svc.CreateGroup(ctx, scope, "team")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	m, err := svc.AddMember(ctx, scope, g.ID, "alice")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if m.UserID != "alice" {
		t.Errorf("member UserID: got %q want alice", m.UserID)
	}

	if err := svc.RemoveMember(ctx, scope, g.ID, "alice"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}

	// Removing again: ErrNotFound.
	if err := svc.RemoveMember(ctx, scope, g.ID, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("double remove: want ErrNotFound, got %v", err)
	}
}

// ---- helpers -----------------------------------------------------------------

func noopLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// mockGrantStore is a minimal in-memory implementation of store.GrantStore.
// Suitable for unit testing the grants.Service without a real database.
type mockGrantStore struct {
	mu      sync.Mutex
	groups  map[string]store.Group
	members []store.GroupMember
	grants  map[string]store.Grant
}

func newMockGrantStore() *mockGrantStore {
	return &mockGrantStore{
		groups: make(map[string]store.Group),
		grants: make(map[string]store.Grant),
	}
}

func (m *mockGrantStore) checkScope(scope identity.Scope) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return nil
}

func (m *mockGrantStore) CreateGroup(ctx context.Context, scope identity.Scope, g store.Group) error {
	if err := m.checkScope(scope); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g.TenantID = scope.Tenant
	m.groups[g.ID] = g
	return nil
}

func (m *mockGrantStore) GetGroup(ctx context.Context, scope identity.Scope, id string) (*store.Group, error) {
	if err := m.checkScope(scope); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.groups[id]
	if !ok || g.TenantID != scope.Tenant {
		return nil, store.ErrNotFound
	}
	return &g, nil
}

func (m *mockGrantStore) ListGroups(ctx context.Context, scope identity.Scope) ([]store.Group, error) {
	if err := m.checkScope(scope); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Group
	for _, g := range m.groups {
		if g.TenantID == scope.Tenant {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *mockGrantStore) DeleteGroup(ctx context.Context, scope identity.Scope, id string) error {
	if err := m.checkScope(scope); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.groups[id]
	if !ok || g.TenantID != scope.Tenant {
		return store.ErrNotFound
	}
	delete(m.groups, id)
	// Remove members.
	var newMembers []store.GroupMember
	for _, mm := range m.members {
		if mm.GroupID != id {
			newMembers = append(newMembers, mm)
		}
	}
	m.members = newMembers
	return nil
}

func (m *mockGrantStore) AddMember(ctx context.Context, scope identity.Scope, mm store.GroupMember) error {
	if err := m.checkScope(scope); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mm.TenantID = scope.Tenant
	// Idempotent: if (group_id, user_id, tenant_id) already exists, skip.
	for _, existing := range m.members {
		if existing.GroupID == mm.GroupID && existing.UserID == mm.UserID && existing.TenantID == mm.TenantID {
			return nil
		}
	}
	m.members = append(m.members, mm)
	return nil
}

func (m *mockGrantStore) RemoveMember(ctx context.Context, scope identity.Scope, groupID, userID string) error {
	if err := m.checkScope(scope); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, mm := range m.members {
		if mm.GroupID == groupID && mm.UserID == userID && mm.TenantID == scope.Tenant {
			m.members = append(m.members[:i], m.members[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *mockGrantStore) ListMembers(ctx context.Context, scope identity.Scope, groupID string) ([]store.GroupMember, error) {
	if err := m.checkScope(scope); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.GroupMember
	for _, mm := range m.members {
		if mm.GroupID == groupID && mm.TenantID == scope.Tenant {
			out = append(out, mm)
		}
	}
	return out, nil
}

func (m *mockGrantStore) CreateGrant(ctx context.Context, scope identity.Scope, g store.Grant) error {
	if err := m.checkScope(scope); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g.TenantID = scope.Tenant
	m.grants[g.ID] = g
	return nil
}

func (m *mockGrantStore) GetGrant(ctx context.Context, scope identity.Scope, id string) (*store.Grant, error) {
	if err := m.checkScope(scope); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok || g.TenantID != scope.Tenant {
		return nil, store.ErrNotFound
	}
	return &g, nil
}

func (m *mockGrantStore) ListGrants(ctx context.Context, scope identity.Scope) ([]store.Grant, error) {
	if err := m.checkScope(scope); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.Grant
	for _, g := range m.grants {
		if g.TenantID == scope.Tenant {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *mockGrantStore) RevokeGrant(ctx context.Context, scope identity.Scope, id string, revokedAt int64) error {
	if err := m.checkScope(scope); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok || g.TenantID != scope.Tenant || g.RevokedAt != 0 {
		return store.ErrNotFound
	}
	g.RevokedAt = revokedAt
	m.grants[id] = g
	return nil
}

func (m *mockGrantStore) EffectiveScopes(ctx context.Context, callerScope identity.Scope) ([]store.ScopedQuery, error) {
	if callerScope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// First: own scope (always included, no ceiling).
	result := []store.ScopedQuery{{Scope: callerScope, ZoneCeiling: ""}}

	if callerScope.User == "" {
		return result, nil
	}

	// Build set of group IDs the caller belongs to.
	memberGroups := make(map[string]bool)
	for _, mm := range m.members {
		if mm.TenantID == callerScope.Tenant && mm.UserID == callerScope.User {
			memberGroups[mm.GroupID] = true
		}
	}
	if len(memberGroups) == 0 {
		return result, nil
	}

	// Find active grants matching caller's group membership.
	seen := make(map[string]bool) // dedup by (tenant,project,user,session,ceiling)
	for _, g := range m.grants {
		if g.TenantID != callerScope.Tenant {
			continue
		}
		if g.RevokedAt != 0 {
			continue
		}
		if !memberGroups[g.GroupID] {
			continue
		}
		if g.ZoneCeiling != "public" && g.ZoneCeiling != "work" {
			continue
		}
		key := g.TenantID + "|" + g.ProjectID + "|" + g.UserID + "|" + g.SessionID + "|" + g.ZoneCeiling
		if seen[key] {
			continue
		}
		seen[key] = true
		sq := store.ScopedQuery{
			Scope: identity.Scope{
				Tenant:  g.TenantID,
				Project: g.ProjectID,
				User:    g.UserID,
				Session: g.SessionID,
			},
			ZoneCeiling: g.ZoneCeiling,
		}
		// Never return own scope with a ceiling.
		if sq.Scope == callerScope {
			continue
		}
		result = append(result, sq)
	}

	return result, nil
}

// ---- grantCoversScope branches (called via CheckContributeGrant) ----------------

func TestCheckContributeGrant_ScopeFilters(t *testing.T) {
	// The contribute grant specifies specific ProjectID/UserID/SessionID.
	// If the target scope doesn't match, the grant does not cover it.
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	g, _ := svc.CreateGroup(ctx, scope, "team")
	_, _ = svc.AddMember(ctx, scope, g.ID, "alice")

	// Grant specifically for user "bob" project "proj-X" session "sess-Y".
	_, _ = svc.CreateGrant(ctx, scope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1", Project: "proj-X", User: "bob", Session: "sess-Y"},
		GroupID:     g.ID,
		Access:      "contribute",
		ZoneCeiling: "work",
	})

	// Target matches: should be covered.
	if err := svc.CheckContributeGrant(ctx, scope,
		identity.Scope{Tenant: "t1", Project: "proj-X", User: "bob", Session: "sess-Y"}, "alice"); err != nil {
		t.Errorf("exact match: want nil, got %v", err)
	}

	// ProjectID mismatch: not covered.
	err := svc.CheckContributeGrant(ctx, scope,
		identity.Scope{Tenant: "t1", Project: "proj-WRONG", User: "bob", Session: "sess-Y"}, "alice")
	if !errors.Is(err, grants.ErrNotCovered) {
		t.Errorf("project mismatch: want ErrNotCovered, got %v", err)
	}

	// UserID mismatch: not covered.
	err = svc.CheckContributeGrant(ctx, scope,
		identity.Scope{Tenant: "t1", Project: "proj-X", User: "wrong-user", Session: "sess-Y"}, "alice")
	if !errors.Is(err, grants.ErrNotCovered) {
		t.Errorf("user mismatch: want ErrNotCovered, got %v", err)
	}

	// SessionID mismatch: not covered.
	err = svc.CheckContributeGrant(ctx, scope,
		identity.Scope{Tenant: "t1", Project: "proj-X", User: "bob", Session: "wrong-sess"}, "alice")
	if !errors.Is(err, grants.ErrNotCovered) {
		t.Errorf("session mismatch: want ErrNotCovered, got %v", err)
	}
}

// ---- Event emission with real EventStore ----------------------------------------

func TestService_EventEmission(t *testing.T) {
	ev := &mockEventStore{}
	svc := grants.New(newMockGrantStore(), ev, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	// Create group → group.created event.
	g, err := svc.CreateGroup(ctx, scope, "eng")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if !ev.hasType("group.created") {
		t.Error("expected group.created event")
	}

	// Add member → group.member.added event.
	_, err = svc.AddMember(ctx, scope, g.ID, "u1")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if !ev.hasType("group.member.added") {
		t.Error("expected group.member.added event")
	}

	// Create grant → grant.created event.
	gr, err := svc.CreateGrant(ctx, scope, grants.CreateGrantInput{
		OwnerScope:  identity.Scope{Tenant: "t1"},
		GroupID:     g.ID,
		Access:      "read",
		ZoneCeiling: "public",
	})
	if err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if !ev.hasType("grant.created") {
		t.Error("expected grant.created event")
	}

	// Revoke grant → grant.revoked event.
	if err := svc.RevokeGrant(ctx, scope, gr.ID); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}
	if !ev.hasType("grant.revoked") {
		t.Error("expected grant.revoked event")
	}

	// Remove member → group.member.removed event.
	if err := svc.RemoveMember(ctx, scope, g.ID, "u1"); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if !ev.hasType("group.member.removed") {
		t.Error("expected group.member.removed event")
	}

	// Delete group → group.deleted event.
	if err := svc.DeleteGroup(ctx, scope, g.ID); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if !ev.hasType("group.deleted") {
		t.Error("expected group.deleted event")
	}
}

// ---- GetGrant / GetGroup --------------------------------------------------------

func TestGetGrant_NotFound(t *testing.T) {
	svc := grants.New(newMockGrantStore(), nil, noopLog())
	_, err := svc.GetGrant(context.Background(), identity.Scope{Tenant: "t1"}, "no-such")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestGetGroup_NotFound(t *testing.T) {
	svc := grants.New(newMockGrantStore(), nil, noopLog())
	_, err := svc.GetGroup(context.Background(), identity.Scope{Tenant: "t1"}, "no-such")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestListMembers_OK(t *testing.T) {
	st := newMockGrantStore()
	svc := grants.New(st, nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	g, _ := svc.CreateGroup(ctx, scope, "team")
	_, _ = svc.AddMember(ctx, scope, g.ID, "u1")
	_, _ = svc.AddMember(ctx, scope, g.ID, "u2")

	members, err := svc.ListMembers(ctx, scope, g.ID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if len(members) != 2 {
		t.Errorf("want 2 members, got %d", len(members))
	}
}

// mockEventStore is a minimal in-memory EventStore for testing.
type mockEventStore struct {
	mu     sync.Mutex
	events []store.Event
}

func (e *mockEventStore) Emit(_ context.Context, _ identity.Scope, ev store.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

func (e *mockEventStore) List(_ context.Context, _ identity.Scope, limit int, _ string) ([]store.Event, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.events, "", nil
}

func (e *mockEventStore) hasType(typ string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if ev.Type == typ {
			return true
		}
	}
	return false
}

// nowMs returns the current time in Unix milliseconds.
func nowMs() int64 {
	return time.Now().UnixMilli()
}
