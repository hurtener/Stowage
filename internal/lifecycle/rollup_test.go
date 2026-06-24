package lifecycle_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

func TestRollupSweepOldSessionMemory(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "rollup-t1"}
	sessID := "sess-001"

	// Memory older than rollupAge (7d) with a session ID.
	old := time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		SessionID: sessID,
		CreatedAt: old,
		UpdatedAt: old,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		RollupAge:       7 * 24 * time.Hour,
		RollupBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// Source memory should be superseded or expired by rollup.
	mem := getMemory(t, st, scope, id)
	if mem.Status == "active" {
		t.Errorf("expected source memory not active after rollup, got %q", mem.Status)
	}

	// A narrative digest should exist.
	ctx := context.Background()
	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 20, "")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	var foundDigest bool
	for _, m := range active {
		if m.Kind == "narrative" && m.SessionID == "" {
			foundDigest = true
			break
		}
	}
	if !foundDigest {
		t.Error("expected narrative digest to be created by rollup")
	}
}

func TestRollupSweepRecentSessionMemoryNotRolledUp(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "rollup-t2"}
	sessID := "sess-002"

	// Recent memory (< 7d) with a session ID — should NOT be rolled up.
	recent := time.Now().Add(-2 * 24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		SessionID: sessID,
		CreatedAt: recent,
		UpdatedAt: recent,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		RollupAge:       7 * 24 * time.Hour,
		RollupBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// Memory should still be active.
	mem := getMemory(t, st, scope, id)
	if mem.Status != "active" {
		t.Errorf("expected status=active for recent memory, got %q", mem.Status)
	}
}

func TestRollupSweepNoSessionID(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "rollup-t3"}

	// Old memory with NO session ID → should NOT be rolled up.
	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		CreatedAt: old,
		UpdatedAt: old,
		// SessionID is empty
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		RollupAge:       7 * 24 * time.Hour,
		RollupBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	mem := getMemory(t, st, scope, id)
	if mem.Status != "active" {
		t.Errorf("expected status=active for non-session memory, got %q", mem.Status)
	}
}

func TestRollupSweepPersonalZoneExpiredUnpromoted(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "rollup-t4"}
	sessID := "sess-personal"

	// Old session memory in personal zone → should be expired without promotion.
	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		SessionID:   sessID,
		PrivacyZone: "personal",
		CreatedAt:   old,
		UpdatedAt:   old,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		RollupAge:       7 * 24 * time.Hour,
		RollupBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// Should be expired (not promoted).
	mem := getMemory(t, st, scope, id)
	if mem.Status != "expired" {
		t.Errorf("expected personal zone memory to be expired, got %q", mem.Status)
	}

	// No narrative digest should exist for this session.
	ctx := context.Background()
	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 20, "")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	for _, m := range active {
		if m.Kind == "narrative" {
			t.Errorf("unexpected narrative digest created for personal zone session")
		}
	}
}

func TestRollupSweepPersonalPlusZone(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "rollup-t5"}
	sessID := "sess-personal-plus"

	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		SessionID:   sessID,
		PrivacyZone: "personal+",
		CreatedAt:   old,
		UpdatedAt:   old,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		RollupAge:       7 * 24 * time.Hour,
		RollupBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	mem := getMemory(t, st, scope, id)
	if mem.Status != "expired" {
		t.Errorf("expected personal+ zone memory to be expired, got %q", mem.Status)
	}
}

// TestRollupSweepManyMemories asserts that when a session has >10 promotable memories, the
// digest includes EVERY one's content (D-116) — rollup supersedes them all, so none may be
// silently dropped from the digest as the old 10-item cap did.
// TestRollupSweepIsReversible is the regression guard for 29d S1 (P4 / D-070): rollup is a
// many-to-one merge, so it must emit memory.merged (not memory.superseded). Rolling back ANY
// one source then restores ALL siblings via ListSupersededBy and removes the shared digest.
// Pre-fix (memory.superseded) restored only the one subject and stranded the N-1 siblings.
func TestRollupSweepIsReversible(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "rollup-rb"}
	sessID := "sess-rb"

	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, insertMemory(t, st, scope, store.Memory{
			SessionID: sessID,
			Content:   "rollup source " + string(rune('A'+i)),
			CreatedAt: old + int64(i),
			UpdatedAt: old + int64(i),
		}))
	}

	profile := lifecycle.Profile{RollupAge: 7 * 24 * time.Hour, RollupBatchSize: 100}
	mgr := lifecycle.New(st, testLogger(), profile, make(chan pipeline.Item, 8))
	mgr.RunForce(ctx)

	// Find the digest and confirm all sources superseded.
	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 20, "")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	var digestID string
	for i := range active {
		if active[i].Kind == "narrative" {
			digestID = active[i].ID
		}
	}
	if digestID == "" {
		t.Fatal("no digest produced")
	}
	for _, id := range ids {
		if m := getMemory(t, st, scope, id); m.Status == "active" {
			t.Fatalf("source %q still active before rollback", id)
		}
	}

	// Roll back ONE source — rollbackMerged must restore ALL of them.
	if _, err := reconcile.Rollback(ctx, st, scope, ids[0]); err != nil {
		t.Fatalf("Rollback rollup source: %v", err)
	}
	for _, id := range ids {
		if m := getMemory(t, st, scope, id); m.Status != "active" {
			t.Errorf("source %q status = %q after rollback, want active (all siblings restored)", id, m.Status)
		}
	}
	// The shared digest is retired (no longer active).
	if d := getMemory(t, st, scope, digestID); d.Status == "active" {
		t.Errorf("digest still active after rollback, want retired")
	}
}

// TestRollupSweepInvalidatesCacheAtTenant is the regression guard for 29d N3: a rollup
// invalidates the retrieval cache at tenant granularity.
func TestRollupSweepInvalidatesCacheAtTenant(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "rollup-inv"}
	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	for i := 0; i < 2; i++ {
		insertMemory(t, st, scope, store.Memory{
			SessionID: "sess-inv", Content: "roll " + string(rune('A'+i)),
			CreatedAt: old + int64(i), UpdatedAt: old + int64(i),
		})
	}
	inv := &countingInvalidator{}
	profile := lifecycle.Profile{RollupAge: 7 * 24 * time.Hour, RollupBatchSize: 100}
	mgr := lifecycle.New(st, testLogger(), profile, make(chan pipeline.Item, 8))
	mgr.SetScopeInvalidator(inv)
	mgr.RunForce(context.Background())

	if len(inv.scopes) == 0 {
		t.Fatal("rollup did not invalidate the retrieval cache (D-118)")
	}
	for _, s := range inv.scopes {
		if s.User != "" || s.Project != "" {
			t.Errorf("invalidation scope = %s, want tenant-only (tenant-keyed cache)", s.String())
		}
	}
}

func TestRollupSweepManyMemories(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "rollup-t6"}
	sessID := "sess-many"

	// Insert 12 promotable session memories older than rollupAge.
	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	var ids []string
	for i := 0; i < 12; i++ {
		id := insertMemory(t, st, scope, store.Memory{
			SessionID: sessID,
			Content:   "memory content " + string(rune('A'+i)),
			CreatedAt: old + int64(i),
			UpdatedAt: old + int64(i),
		})
		ids = append(ids, id)
	}

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		RollupAge:       7 * 24 * time.Hour,
		RollupBatchSize: 100,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// All source memories should be superseded.
	for _, id := range ids {
		mem := getMemory(t, st, scope, id)
		if mem.Status == "active" {
			t.Errorf("source memory %q still active after rollup", id)
		}
	}

	// A narrative digest should exist with SessionID empty (promoted to parent scope).
	ctx := context.Background()
	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 20, "")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	var digest *store.Memory
	for i := range active {
		if active[i].Kind == "narrative" && active[i].SessionID == "" {
			digest = &active[i]
			break
		}
	}
	if digest == nil {
		t.Fatal("expected narrative digest for 12-memory session rollup")
	}
	// D-116 regression: ALL 12 superseded memories' content must be in the digest — the old
	// 10-item cap silently dropped #11/#12 ("memory content K"/"memory content L") while still
	// retiring them. No "[+N more]" elision.
	for i := 0; i < 12; i++ {
		want := "memory content " + string(rune('A'+i))
		if !strings.Contains(digest.Content, want) {
			t.Errorf("digest missing superseded content %q (silent loss): %q", want, digest.Content)
		}
	}
	if strings.Contains(digest.Content, "more]") {
		t.Errorf("digest elided content with [+N more]: %q", digest.Content)
	}
}

// TestRollupSweepIdempotent runs the rollup sweep twice and asserts the same
// DB state (idempotency — AC-1).
func TestRollupSweepIdempotent(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "rollup-t7"}
	sessID := "sess-idem"

	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	insertMemory(t, st, scope, store.Memory{
		SessionID: sessID,
		Content:   "idempotent memory",
		CreatedAt: old,
		UpdatedAt: old,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		RollupAge:       7 * 24 * time.Hour,
		RollupBatchSize: 100,
	}
	ctx := context.Background()
	mgr := lifecycle.New(st, testLogger(), profile, ingest)

	// First pass.
	mgr.RunForce(ctx)

	// Count active memories after first pass.
	active1, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 20, "")

	// Second pass.
	mgr.RunForce(ctx)

	// Count after second pass — should be identical.
	active2, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 20, "")

	if len(active1) != len(active2) {
		t.Errorf("rollup not idempotent: after 1st pass %d active, after 2nd pass %d active",
			len(active1), len(active2))
	}
}
