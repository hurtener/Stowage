package conformance

// Phase 21 conformance tests: OpsStore.DeleteUserData — the DSAR cascading delete
// (RFC §13, D-098). Proves the cascade is COMPLETE (every user_id-bearing table and
// every child of the user's memories is erased, incl. cross-user rows referencing a
// purged memory so the FK-restricted parent deletes never fail), ISOLATED (another
// user in the same tenant and the same user id in another tenant are untouched), the
// verbatim records are deleted (the P1 exception), a tenant-scoped `user.purged` audit
// event survives the events purge, and the empty-scope guard fails closed (P3).

import (
	"context"
	"errors"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// testDSARDeleteUserDataCascade populates a rich dataset for one user (plus a second
// user whose data — including rows that reference the first user's memories — must be
// handled correctly), purges the first user, and asserts completeness + isolation.
func testDSARDeleteUserDataCascade(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	tenant := "t-" + newID()
	alice := identity.Scope{Tenant: tenant, User: "alice"}
	bob := identity.Scope{Tenant: tenant, User: "bob"}

	// --- alice's verbatim records ---
	rec1, rec2 := newID(), newID()
	if err := s.Records().Append(ctx, alice, []store.Record{
		{ID: rec1, Role: "user", Content: "alice says hello", OccurredAt: nowMs(), CreatedAt: nowMs()},
		{ID: rec2, Role: "assistant", Content: "noted", OccurredAt: nowMs(), CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("append alice records: %v", err)
	}

	// --- alice's m1 with the full junction set + provenance into rec1 ---
	m1 := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: m1, Kind: "fact", Content: "alice likes go", Context: "ctx",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: newID(), CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities:   []string{"go"},
		Keywords:   []string{"language"},
		Queries:    []string{"what does alice like?"},
		Topics:     []string{"preferences"},
		Provenance: []store.Provenance{{ID: newID(), MemoryID: m1, RecordID: rec1, TenantID: tenant, CreatedAt: nowMs()}},
		Events:     []store.Event{{ID: newID(), Type: "memory.added", SubjectID: m1, Payload: `{}`}},
	}
	if err := s.Memories().Commit(ctx, alice, cs); err != nil {
		t.Fatalf("commit alice m1: %v", err)
	}
	m2 := insertActiveMemory(t, s, alice, "alice uses sqlite", "fact", nil, nil, nil)

	// alice's vector on m1
	if err := s.Vectors().Upsert(ctx, alice, store.StoredVector{
		MemoryID: m1, Vec: []float32{0.1, 0.2, 0.3, 0.4}, Dims: 4, Model: "test", Kind: "fact", CreatedAt: nowMs(),
	}); err != nil {
		t.Fatalf("upsert alice vector: %v", err)
	}
	// alice's intra-user link m1 -> m2
	aliceLink := newID()
	if err := s.Memories().InsertLinks(ctx, alice, []store.Link{
		{ID: aliceLink, FromMemory: m1, ToMemory: m2, Type: "relates_to", Source: "explicit", Confidence: 1.0, CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("insert alice link: %v", err)
	}
	// alice's own injection citing m1
	aliceInj := newID()
	if err := s.Injections().Append(ctx, alice, []store.Injection{
		{ID: aliceInj, ResponseID: newID(), MemoryID: m1, Rank: 1, Score: 0.9, Lane: "vector", CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("append alice injection: %v", err)
	}
	// alice's other user_id-scoped rows
	if err := s.Branches().Create(ctx, alice, store.Branch{ID: newID(), SessionID: "s1", Status: "open", CreatedAt: nowMs(), UpdatedAt: nowMs()}); err != nil {
		t.Fatalf("create alice branch: %v", err)
	}
	if err := s.Episodes().CreateEpisode(ctx, alice, store.Episode{ID: newID(), SessionID: "s1", Status: "closed", StartedAt: nowMs(), CreatedAt: nowMs(), UpdatedAt: nowMs()}); err != nil {
		t.Fatalf("create alice episode: %v", err)
	}
	if err := s.Suggestions().Create(ctx, alice, []store.Suggestion{
		{ID: newID(), TriggerKind: "recent_episode", MemoryID: m1, Status: "pending", CreatedAt: nowMs(), UpdatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("create alice suggestion: %v", err)
	}
	if err := s.ScopeSettings().Set(ctx, alice, "proactive.enabled", "true", nowMs()); err != nil {
		t.Fatalf("set alice scope setting: %v", err)
	}
	if err := s.Buffers().AppendItem(ctx, alice, store.BufferItem{ID: newID(), BufferKey: "bk", RecordID: rec1, CreatedAt: nowMs()}); err != nil {
		t.Fatalf("append alice buffer item: %v", err)
	}
	// alice's extraction-magnet topic (the `topics` table — user_id-scoped, distinct
	// from the memory_topics junction above).
	aliceTopicKey := "alice-magnet"
	if err := s.Topics().Upsert(ctx, alice, store.Topic{ID: newID(), Key: aliceTopicKey, Description: "alice's topic", Status: "active", CreatedAt: nowMs(), UpdatedAt: nowMs()}); err != nil {
		t.Fatalf("upsert alice topic: %v", err)
	}
	// alice in a group with a grant she owns
	grp := newID()
	if err := s.Grants().CreateGroup(ctx, alice, store.Group{ID: grp, Name: "team", CreatedAt: nowMs()}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := s.Grants().AddMember(ctx, alice, store.GroupMember{ID: newID(), GroupID: grp, UserID: "alice", CreatedAt: nowMs()}); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if err := s.Grants().CreateGrant(ctx, alice, store.Grant{ID: newID(), UserID: "alice", GroupID: grp, Access: "read", ZoneCeiling: "work", CreatedAt: nowMs(), UpdatedAt: nowMs()}); err != nil {
		t.Fatalf("create grant: %v", err)
	}

	// --- bob: his own memory + rows that REFERENCE alice's memory (cross-user) ---
	mB := insertActiveMemory(t, s, bob, "bob fact", "fact", nil, nil, nil)
	mB2 := insertActiveMemory(t, s, bob, "bob fact 2", "fact", nil, nil, nil)
	// bob link that touches alice's m1 (must be deleted on purge)
	bobLinkToAlice := newID()
	if err := s.Memories().InsertLinks(ctx, bob, []store.Link{
		{ID: bobLinkToAlice, FromMemory: mB, ToMemory: m1, Type: "relates_to", Source: "inferred", Confidence: 0.5, CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("insert bob->alice link: %v", err)
	}
	// bob's own link mB -> mB2 (must SURVIVE)
	bobOwnLink := newID()
	if err := s.Memories().InsertLinks(ctx, bob, []store.Link{
		{ID: bobOwnLink, FromMemory: mB, ToMemory: mB2, Type: "relates_to", Source: "inferred", Confidence: 0.5, CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("insert bob own link: %v", err)
	}
	// bob's injection citing alice's m1 (cross-user; must be deleted on purge)
	bobInjOnAlice := newID()
	if err := s.Injections().Append(ctx, bob, []store.Injection{
		{ID: bobInjOnAlice, ResponseID: newID(), MemoryID: m1, Rank: 1, Score: 0.4, Lane: "vector", CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("append bob->alice injection: %v", err)
	}
	// bob's own injection citing his own memory (must SURVIVE)
	bobInjOwn := newID()
	if err := s.Injections().Append(ctx, bob, []store.Injection{
		{ID: bobInjOwn, ResponseID: newID(), MemoryID: mB, Rank: 1, Score: 0.7, Lane: "vector", CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("append bob own injection: %v", err)
	}

	// --- purge alice ---
	c, err := s.Ops().DeleteUserData(ctx, alice)
	if err != nil {
		t.Fatalf("DeleteUserData(alice): %v", err)
	}

	// --- counts: every table alice populated reports a positive count ---
	checks := []struct {
		name string
		got  int64
		min  int64
	}{
		{"records", c.Records, 2},
		{"memories", c.Memories, 2},
		{"provenance", c.Provenance, 1},
		{"entities", c.Entities, 1},
		{"keywords", c.Keywords, 1},
		{"queries", c.Queries, 1},
		{"memory_topics", c.MemoryTopics, 1}, // the m1 junction row
		{"topics", c.Topics, 1},              // the magnet topic
		{"vectors", c.Vectors, 1},
		{"links", c.Links, 2},           // alice m1->m2 + bob mB->m1
		{"injections", c.Injections, 2}, // alice's own + bob's cross-user on m1
		{"suggestions", c.Suggestions, 1},
		{"buffer_items", c.BufferItems, 1},
		{"scope_settings", c.ScopeSettings, 1},
		{"group_members", c.GroupMembers, 1},
		{"grants", c.Grants, 1},
		{"branches", c.Branches, 1},
		{"episodes", c.Episodes, 1},
		{"events", c.Events, 2}, // 2x memory.added for alice
	}
	for _, ck := range checks {
		if ck.got < ck.min {
			t.Errorf("count %s: got %d want >= %d", ck.name, ck.got, ck.min)
		}
	}

	// --- alice's data is gone ---
	if _, err := s.Memories().Get(ctx, alice, m1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alice m1 still present: err=%v", err)
	}
	if _, err := s.Memories().Get(ctx, alice, m2); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alice m2 still present: err=%v", err)
	}
	if _, err := s.Records().Get(ctx, alice, rec1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alice rec1 still present: err=%v", err)
	}
	if _, err := s.Injections().Get(ctx, alice, aliceInj); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alice injection still present: err=%v", err)
	}
	if _, err := s.Topics().Get(ctx, alice, aliceTopicKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alice magnet topic still present: err=%v", err)
	}

	// --- cross-user rows referencing alice's memory are gone ---
	if _, err := s.Injections().Get(ctx, bob, bobInjOnAlice); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("bob's injection on alice's memory survived: err=%v", err)
	}
	bobLinks, err := s.Memories().ListLinks(ctx, bob, mB, "")
	if err != nil {
		t.Fatalf("list bob links: %v", err)
	}
	for _, l := range bobLinks {
		if l.ID == bobLinkToAlice {
			t.Errorf("bob's link to alice's memory survived")
		}
	}

	// --- bob's own data SURVIVES ---
	if _, err := s.Memories().Get(ctx, bob, mB); err != nil {
		t.Errorf("bob's own memory was wrongly deleted: %v", err)
	}
	if _, err := s.Injections().Get(ctx, bob, bobInjOwn); err != nil {
		t.Errorf("bob's own injection was wrongly deleted: %v", err)
	}
	foundOwnLink := false
	for _, l := range bobLinks {
		if l.ID == bobOwnLink {
			foundOwnLink = true
		}
	}
	if !foundOwnLink {
		t.Errorf("bob's own link (mB->mB2) was wrongly deleted")
	}

	// --- the user.purged audit event survives at tenant scope ---
	tScope := identity.Scope{Tenant: tenant}
	evs, err := s.Events().ListBySubject(ctx, tScope, "alice", 10)
	if err != nil {
		t.Fatalf("list events by subject: %v", err)
	}
	foundPurge := false
	for _, e := range evs {
		if e.Type == "user.purged" {
			foundPurge = true
		}
	}
	if !foundPurge {
		t.Errorf("user.purged audit event not found for alice")
	}
}

// testDSARDeleteUserDataCrossTenantIsolation proves a purge of (t1, alice) leaves an
// identically-named user in t2 entirely intact (P3 cross-tenant isolation).
func testDSARDeleteUserDataCrossTenantIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	t1 := identity.Scope{Tenant: "t1-" + newID(), User: "alice"}
	t2 := identity.Scope{Tenant: "t2-" + newID(), User: "alice"}

	rec1 := newID()
	if err := s.Records().Append(ctx, t1, []store.Record{{ID: rec1, Role: "user", Content: "t1 alice", OccurredAt: nowMs(), CreatedAt: nowMs()}}); err != nil {
		t.Fatalf("append t1 record: %v", err)
	}
	m1 := insertActiveMemory(t, s, t1, "t1 alice memory", "fact", nil, nil, nil)

	rec2 := newID()
	if err := s.Records().Append(ctx, t2, []store.Record{{ID: rec2, Role: "user", Content: "t2 alice", OccurredAt: nowMs(), CreatedAt: nowMs()}}); err != nil {
		t.Fatalf("append t2 record: %v", err)
	}
	m2 := insertActiveMemory(t, s, t2, "t2 alice memory", "fact", nil, nil, nil)

	if _, err := s.Ops().DeleteUserData(ctx, t1); err != nil {
		t.Fatalf("DeleteUserData(t1 alice): %v", err)
	}

	// t1 gone
	if _, err := s.Memories().Get(ctx, t1, m1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("t1 memory survived: err=%v", err)
	}
	if _, err := s.Records().Get(ctx, t1, rec1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("t1 record survived: err=%v", err)
	}
	// t2 intact (same user id, different tenant)
	if _, err := s.Memories().Get(ctx, t2, m2); err != nil {
		t.Errorf("t2 alice memory wrongly deleted: %v", err)
	}
	if _, err := s.Records().Get(ctx, t2, rec2); err != nil {
		t.Errorf("t2 alice record wrongly deleted: %v", err)
	}
}

// testDSARDeleteUserDataScopeRequired proves the cascade fails closed without both a
// tenant and a user (P3 — no tenant-wide or unscoped purge).
func testDSARDeleteUserDataScopeRequired(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	if _, err := s.Ops().DeleteUserData(ctx, identity.Scope{}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("empty scope: got %v want ErrScopeRequired", err)
	}
	if _, err := s.Ops().DeleteUserData(ctx, identity.Scope{User: "alice"}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("no tenant: got %v want ErrScopeRequired", err)
	}
	if _, err := s.Ops().DeleteUserData(ctx, identity.Scope{Tenant: "t1"}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("no user: got %v want ErrScopeRequired", err)
	}
}
