package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// insertMemory is a test helper to insert an active memory and return its ID.
func insertMemory(t *testing.T, st store.Store, scope identity.Scope, overrides store.Memory) string {
	t.Helper()
	mem := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     "test content",
		Status:      "active",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		CreatedAt:   time.Now().UnixMilli(),
		UpdatedAt:   time.Now().UnixMilli(),
	}
	if overrides.ID != "" {
		mem.ID = overrides.ID
	}
	if overrides.Kind != "" {
		mem.Kind = overrides.Kind
	}
	if overrides.Content != "" {
		mem.Content = overrides.Content
	}
	if overrides.TrustSource != "" {
		mem.TrustSource = overrides.TrustSource
	}
	if overrides.Stability != 0 {
		mem.Stability = overrides.Stability
	}
	if overrides.LastAccessedAt != 0 {
		mem.LastAccessedAt = overrides.LastAccessedAt
	}
	if overrides.ValidUntil != 0 {
		mem.ValidUntil = overrides.ValidUntil
	}
	if overrides.SessionID != "" {
		mem.SessionID = overrides.SessionID
	}
	if overrides.PrivacyZone != "" {
		mem.PrivacyZone = overrides.PrivacyZone
	}
	if overrides.CreatedAt != 0 {
		mem.CreatedAt = overrides.CreatedAt
	}
	if overrides.UpdatedAt != 0 {
		mem.UpdatedAt = overrides.UpdatedAt
	}
	if err := st.Memories().Insert(context.Background(), scope, mem); err != nil {
		t.Fatalf("insert memory: %v", err)
	}
	return mem.ID
}

// getMemory retrieves a memory and fails the test if not found.
func getMemory(t *testing.T, st store.Store, scope identity.Scope, id string) *store.Memory {
	t.Helper()
	mem, err := st.Memories().Get(context.Background(), scope, id)
	if err != nil {
		t.Fatalf("get memory %q: %v", id, err)
	}
	return mem
}

func TestDecaySweepHealthyMemory(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "t1"}
	// Fresh memory (no elapsed time) → decay factor near 1.0 → healthy.
	id := insertMemory(t, st, scope, store.Memory{
		Stability:      1.0,
		LastAccessedAt: time.Now().UnixMilli(),
	})

	ingest := make(chan pipeline.Item, 8)
	// Use very short intervals so sweep runs quickly.
	profile := lifecycle.Profile{
		DecayInterval:    10 * time.Minute,
		DecayBatchSize:   100,
		DecayGraceSweeps: 2,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.SweepDecayOnce(context.Background())

	mem := getMemory(t, st, scope, id)
	if mem.Status != "active" {
		t.Errorf("expected status=active, got %q", mem.Status)
	}
	if mem.ValidUntil != 0 {
		t.Errorf("expected valid_until=0, got %d", mem.ValidUntil)
	}
}

func TestDecaySweepBelowFloorSetsValidUntil(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "t2"}
	// Memory with LastAccessedAt far in the past → decay below default floor (0.1).
	// With stability=1.0, last_accessed=10+ days ago, decay ~ exp(-0.4*days/1.0)
	// At 10 days: exp(-4.0) ≈ 0.018 < 0.10 → below floor → valid_until set.
	farPast := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		Stability:      1.0,
		LastAccessedAt: farPast,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		DecayInterval:    10 * time.Minute,
		DecayBatchSize:   100,
		DecayGraceSweeps: 2,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.SweepDecayOnce(context.Background())

	mem := getMemory(t, st, scope, id)
	if mem.Status != "active" {
		t.Errorf("expected status=active after first sweep, got %q", mem.Status)
	}
	if mem.ValidUntil == 0 {
		t.Error("expected valid_until set after first below-floor sweep")
	}
	// D-110 regression: grace must be DecayGraceSweeps * DecayInterval in MILLISECONDS
	// (2 * 10min = 20min), not nanoseconds-as-ms (~38 years). The old `!= 0` assertion
	// let a 10^6x inflation through. Bound it tightly: well under an hour from now.
	wantGraceMs := int64(profile.DecayGraceSweeps) * profile.DecayInterval.Milliseconds() // 1_200_000
	nowMs := time.Now().UnixMilli()
	got := mem.ValidUntil - nowMs
	if got < wantGraceMs-60_000 || got > wantGraceMs+60_000 {
		t.Errorf("valid_until grace = %d ms from now; want ≈ %d ms (≈20min). A nanosecond/ms unit bug yields ~1.2e12 ms (~38 years).", got, wantGraceMs)
	}
}

func TestDecaySweepExpiresAfterGrace(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "t3"}
	farPast := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()

	// Memory with valid_until already set in the past (grace elapsed).
	pastValidUntil := time.Now().Add(-1 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		Stability:      1.0,
		LastAccessedAt: farPast,
		ValidUntil:     pastValidUntil,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		DecayInterval:    10 * time.Minute,
		DecayBatchSize:   100,
		DecayGraceSweeps: 2,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.SweepDecayOnce(context.Background())

	mem := getMemory(t, st, scope, id)
	if mem.Status != "expired" {
		t.Errorf("expected status=expired after grace elapsed, got %q", mem.Status)
	}
}

func TestDecaySweepGraceNotElapsed(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "t4"}
	farPast := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()

	// Memory with valid_until in the FUTURE (still within grace period).
	futureValidUntil := time.Now().Add(24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		Stability:      1.0,
		LastAccessedAt: farPast,
		ValidUntil:     futureValidUntil,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		DecayInterval:    10 * time.Minute,
		DecayBatchSize:   100,
		DecayGraceSweeps: 2,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.SweepDecayOnce(context.Background())

	// Should still be active — grace period not elapsed.
	mem := getMemory(t, st, scope, id)
	if mem.Status != "active" {
		t.Errorf("expected status=active (within grace), got %q", mem.Status)
	}
}

func TestDecaySweepClearsValidUntilWhenHealthy(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "t5"}
	// Fresh memory with a stale valid_until set (should be cleared).
	// High stability (e.g. 100.0) + recent access → decay factor near 1.0.
	futureValidUntil := time.Now().Add(24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		Stability:      100.0, // very stable
		LastAccessedAt: time.Now().UnixMilli(),
		ValidUntil:     futureValidUntil,
	})

	ingest := make(chan pipeline.Item, 8)
	mgr := lifecycle.New(st, testLogger(), lifecycle.DefaultProfile(), ingest)
	mgr.SweepDecayOnce(context.Background())

	mem := getMemory(t, st, scope, id)
	// valid_until should be cleared since memory is healthy.
	if mem.ValidUntil != 0 {
		t.Errorf("expected valid_until=0 (cleared), got %d", mem.ValidUntil)
	}
}

func TestDecaySweepUserStatedHighFloor(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "t6"}
	// user_stated memories have floor=0.5. With stability=1.0 and last_accessed
	// 10 days ago, raw decay ≈ 0.018 << 0.5 floor → hits floor.
	farPast := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{
		TrustSource:    "user_stated",
		Stability:      1.0,
		LastAccessedAt: farPast,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		DecayInterval:    10 * time.Minute,
		DecayBatchSize:   100,
		DecayGraceSweeps: 2,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.SweepDecayOnce(context.Background())

	// user_stated memory hits its floor (0.5) → treated the same as any memory
	// at floor: valid_until gets set.
	mem := getMemory(t, st, scope, id)
	// It's at the floor — valid_until should be set.
	if mem.ValidUntil == 0 {
		t.Error("expected valid_until set for user_stated memory at floor")
	}
}

func TestDecaySweepMultipleTenants(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	farPast := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()

	scope1 := identity.Scope{Tenant: "tenant-alpha"}
	scope2 := identity.Scope{Tenant: "tenant-beta"}

	id1 := insertMemory(t, st, scope1, store.Memory{Stability: 1.0, LastAccessedAt: farPast})
	id2 := insertMemory(t, st, scope2, store.Memory{Stability: 1.0, LastAccessedAt: farPast})

	ingest := make(chan pipeline.Item, 8)
	mgr := lifecycle.New(st, testLogger(), lifecycle.DefaultProfile(), ingest)
	mgr.SweepDecayOnce(context.Background())

	mem1 := getMemory(t, st, scope1, id1)
	mem2 := getMemory(t, st, scope2, id2)

	if mem1.ValidUntil == 0 {
		t.Error("tenant-alpha: expected valid_until set")
	}
	if mem2.ValidUntil == 0 {
		t.Error("tenant-beta: expected valid_until set")
	}
}

// TestDecaySweepActivityAxisDrivesDecay proves A3: the decay sweep now decays on the
// ACTIVITY axis (records since last_accessed), not only wall-clock. Two memories with
// negligible time-decay differ only in how many records were created after they were
// last accessed: the one with many intervening records falls below floor; the one
// accessed after those records stays healthy. (Before the fix, activityTurns was
// hardcoded 0 and BOTH stayed healthy.)
func TestDecaySweepActivityAxisDrivesDecay(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "t-activity"}
	now := time.Now().UnixMilli()
	hour := int64(time.Hour / time.Millisecond)

	// 5 records created ~1h ago.
	recTime := now - hour
	recs := make([]store.Record, 0, 5)
	for i := 0; i < 5; i++ {
		recs = append(recs, store.Record{ID: ulid.Make().String(), Role: "user", Content: "r", CreatedAt: recTime, OccurredAt: recTime})
	}
	if err := st.Records().Append(context.Background(), scope, recs); err != nil {
		t.Fatalf("append records: %v", err)
	}

	// Stale: last accessed BEFORE the records (~2h ago) ⇒ 5 activity turns ⇒ decays.
	stale := insertMemory(t, st, scope, store.Memory{Stability: 1.0, LastAccessedAt: now - 2*hour})
	// Fresh: last accessed AFTER the records (~30m ago) ⇒ 0 activity turns ⇒ healthy.
	fresh := insertMemory(t, st, scope, store.Memory{Stability: 1.0, LastAccessedAt: now - hour/2})

	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{DecayInterval: 10 * time.Minute, DecayBatchSize: 100, DecayGraceSweeps: 2}, make(chan pipeline.Item, 8))
	mgr.SweepDecayOnce(context.Background())

	if m := getMemory(t, st, scope, stale); m.ValidUntil == 0 {
		t.Errorf("stale memory (5 activity turns) should be below floor → valid_until set; got 0")
	}
	if m := getMemory(t, st, scope, fresh); m.ValidUntil != 0 {
		t.Errorf("fresh memory (0 activity turns) should stay healthy; got valid_until=%d", m.ValidUntil)
	}
}

// countingInvalidator records InvalidateScope calls (D-118 test double).
type countingInvalidator struct{ scopes []identity.Scope }

func (c *countingInvalidator) InvalidateScope(s identity.Scope) { c.scopes = append(c.scopes, s) }

// TestDecayExpireInvalidatesCache proves D-118/audit #15: a status-mutating sweep (expire)
// invalidates the retrieval cache, so it can't serve the expired memory for the TTL.
func TestDecayExpireInvalidatesCache(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "decay-inv"}
	past := time.Now().Add(-1 * time.Hour).UnixMilli()
	id := insertMemory(t, st, scope, store.Memory{Stability: 1.0,
		LastAccessedAt: time.Now().Add(-30 * 24 * time.Hour).UnixMilli(), ValidUntil: past})

	inv := &countingInvalidator{}
	profile := lifecycle.Profile{DecayInterval: 10 * time.Minute, DecayBatchSize: 100, DecayGraceSweeps: 2}
	mgr := lifecycle.New(st, testLogger(), profile, make(chan pipeline.Item, 8))
	mgr.SetScopeInvalidator(inv)
	mgr.SweepDecayOnce(context.Background())

	if mem := getMemory(t, st, scope, id); mem.Status != "expired" {
		t.Fatalf("memory status = %q, want expired", mem.Status)
	}
	if len(inv.scopes) == 0 {
		t.Error("expire did not invalidate the retrieval cache (D-118)")
	}
}
