package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
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

// TestRollupSweepManyMemories exercises the buildDigestContent > 10 memories
// branch which emits "[+N more]" when a session has more than 10 promotable memories.
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
	var found bool
	for _, m := range active {
		if m.Kind == "narrative" && m.SessionID == "" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected narrative digest for 12-memory session rollup")
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
