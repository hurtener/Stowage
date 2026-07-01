package lifecycle_test

// confirm_test.go — Phase 18 acceptance criteria for the confirm sweep (D-065).
//
// Tests:
//   TestConfirm_TTLPromotion        — memory older than ConfirmTTL is promoted
//   TestConfirm_RepeatsPromotion    — memory with MatchCount >= ConfirmRepeats promoted before TTL
//   TestConfirm_NotYetEligible      — memory below both thresholds is not touched
//   TestConfirm_TargetMissing       — no supersedes_id → plain activate
//   TestConfirm_Idempotent          — running sweep twice leaves golden state
//   TestConfirm_SupersededPayload   — memory.superseded event carries prior-state snapshot
//   TestConfirm_DefaultProfile      — ConfirmTTL=72h, ConfirmRepeats=2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// insertParkedMemory inserts a pending_confirmation memory and returns its ID.
func insertParkedMemory(t *testing.T, st store.Store, scope identity.Scope, overrides store.Memory) string {
	t.Helper()
	mem := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     "parked content",
		Status:      "pending_confirmation",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: ulid.Make().String(),
		CreatedAt:   time.Now().UnixMilli(),
		UpdatedAt:   time.Now().UnixMilli(),
	}
	if overrides.ID != "" {
		mem.ID = overrides.ID
	}
	if overrides.SupersedesID != "" {
		mem.SupersedesID = overrides.SupersedesID
	}
	if overrides.CreatedAt != 0 {
		mem.CreatedAt = overrides.CreatedAt
	}
	if overrides.UpdatedAt != 0 {
		mem.UpdatedAt = overrides.UpdatedAt
	}
	if overrides.MatchCount != 0 {
		mem.MatchCount = overrides.MatchCount
	}
	if overrides.Content != "" {
		mem.Content = overrides.Content
	}
	if err := st.Memories().Insert(context.Background(), scope, mem); err != nil {
		t.Fatalf("insertParkedMemory: %v", err)
	}
	return mem.ID
}

// TestConfirm_TTLPromotion verifies that a parked memory older than ConfirmTTL
// is promoted to active by the confirm sweep.
func TestConfirm_TTLPromotion(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "tenant-cttl"}
	// Insert a parked memory with a created_at far in the past (> ConfirmTTL).
	pastCreatedAt := time.Now().Add(-5 * time.Minute).UnixMilli()
	parkedID := insertParkedMemory(t, st, scope, store.Memory{
		CreatedAt: pastCreatedAt,
		UpdatedAt: pastCreatedAt,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		ConfirmTTL:       2 * time.Minute, // shorter TTL for test speed
		ConfirmRepeats:   100,             // high so it doesn't trigger
		ConfirmInterval:  24 * time.Hour,
		ConfirmBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	mem := getMemory(t, st, scope, parkedID)
	if mem.Status != "active" {
		t.Errorf("status: got %q want active (TTL promotion)", mem.Status)
	}
}

// TestConfirm_RepeatsPromotion verifies that a parked memory with
// MatchCount >= ConfirmRepeats is promoted before TTL elapses.
func TestConfirm_RepeatsPromotion(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "tenant-crp"}
	// Insert a parked memory that is NOT old enough for TTL but has MatchCount=2.
	futureCreatedAt := time.Now().Add(-1 * time.Minute).UnixMilli() // very recent
	parkedID := insertParkedMemory(t, st, scope, store.Memory{
		CreatedAt:  futureCreatedAt,
		UpdatedAt:  futureCreatedAt,
		MatchCount: 2, // at the ConfirmRepeats threshold
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		ConfirmTTL:       72 * time.Hour, // long TTL so it won't fire on age
		ConfirmRepeats:   2,              // matches MatchCount=2
		ConfirmInterval:  24 * time.Hour,
		ConfirmBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	mem := getMemory(t, st, scope, parkedID)
	if mem.Status != "active" {
		t.Errorf("status: got %q want active (repeats promotion)", mem.Status)
	}
}

// TestConfirm_NotYetEligible verifies that a parked memory below both thresholds
// is left untouched by the sweep.
func TestConfirm_NotYetEligible(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "tenant-cnye"}
	recentCreatedAt := time.Now().Add(-30 * time.Second).UnixMilli()
	parkedID := insertParkedMemory(t, st, scope, store.Memory{
		CreatedAt:  recentCreatedAt,
		UpdatedAt:  recentCreatedAt,
		MatchCount: 1, // below ConfirmRepeats=2
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		ConfirmTTL:       72 * time.Hour, // long TTL
		ConfirmRepeats:   2,
		ConfirmInterval:  24 * time.Hour,
		ConfirmBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	mem := getMemory(t, st, scope, parkedID)
	if mem.Status != "pending_confirmation" {
		t.Errorf("status: got %q want pending_confirmation (not eligible)", mem.Status)
	}
}

// TestConfirm_TargetMissing verifies that a parked memory with no supersedes_id
// (or whose target is gone) is promoted as a plain activate.
func TestConfirm_TargetMissing(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "tenant-ctm"}
	oldCreatedAt := time.Now().Add(-5 * time.Minute).UnixMilli()
	// No SupersedesID — plain parked without a target.
	parkedID := insertParkedMemory(t, st, scope, store.Memory{
		CreatedAt: oldCreatedAt,
		UpdatedAt: oldCreatedAt,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		ConfirmTTL:       2 * time.Minute,
		ConfirmRepeats:   100,
		ConfirmInterval:  24 * time.Hour,
		ConfirmBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	mem := getMemory(t, st, scope, parkedID)
	if mem.Status != "active" {
		t.Errorf("status: got %q want active (plain activate no target)", mem.Status)
	}
}

// TestConfirm_Idempotent verifies that running the sweep twice leaves the same
// golden state (AC-8 idempotency).
func TestConfirm_Idempotent(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "tenant-cid"}
	oldCreatedAt := time.Now().Add(-5 * time.Minute).UnixMilli()
	parkedID := insertParkedMemory(t, st, scope, store.Memory{
		CreatedAt: oldCreatedAt,
		UpdatedAt: oldCreatedAt,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		ConfirmTTL:       2 * time.Minute,
		ConfirmRepeats:   100,
		ConfirmInterval:  24 * time.Hour,
		ConfirmBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)

	// First run — should promote.
	mgr.RunForce(context.Background())
	mem1 := getMemory(t, st, scope, parkedID)
	if mem1.Status != "active" {
		t.Fatalf("after first run: status %q want active", mem1.Status)
	}

	// Second run — no change (memory is now active, not pending_confirmation).
	mgr.RunForce(context.Background())
	mem2 := getMemory(t, st, scope, parkedID)
	if mem2.Status != "active" {
		t.Errorf("after second run: status %q want active (idempotent)", mem2.Status)
	}
	if mem2.UpdatedAt != mem1.UpdatedAt {
		t.Errorf("updated_at changed on second run (not idempotent): %d vs %d",
			mem1.UpdatedAt, mem2.UpdatedAt)
	}
}

// TestConfirm_SupersededPayload verifies that when a parked memory has a
// supersedes_id, the memory.superseded event carries a prior-state snapshot
// (D-065 reversibility requirement).
func TestConfirm_SupersededPayload(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "tenant-csp"}

	// Seed the target (active, will be superseded on promotion).
	targetMem := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     "old fact that will be superseded",
		Status:      "active",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: ulid.Make().String(),
		CreatedAt:   time.Now().UnixMilli(),
		UpdatedAt:   time.Now().UnixMilli(),
	}
	if err := st.Memories().Insert(context.Background(), scope, targetMem); err != nil {
		t.Fatalf("insert target: %v", err)
	}

	// Seed entities+keywords for target so the snapshot is non-trivial.
	commitTarget := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   targetMem,
		Entities: []string{"entity-target"},
		Keywords: []string{"kw-target"},
		Events: []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.added",
			SubjectID: targetMem.ID,
			Payload:   "{}",
			CreatedAt: time.Now().UnixMilli(),
		}},
		Scope: scope,
	}
	// Re-insert with junctions via Commit.
	if err := st.Memories().Commit(context.Background(), scope, commitTarget); err != nil {
		// May fail due to duplicate PK; use Insert path and add junctions separately.
		_ = err
	}

	oldCreatedAt := time.Now().Add(-5 * time.Minute).UnixMilli()
	parkedID := insertParkedMemory(t, st, scope, store.Memory{
		SupersedesID: targetMem.ID,
		CreatedAt:    oldCreatedAt,
		UpdatedAt:    oldCreatedAt,
	})

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		ConfirmTTL:       2 * time.Minute,
		ConfirmRepeats:   100,
		ConfirmInterval:  24 * time.Hour,
		ConfirmBatchSize: 50,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// Verify promotion succeeded.
	promoted := getMemory(t, st, scope, parkedID)
	if promoted.Status != "active" {
		t.Fatalf("parked not promoted: status %q", promoted.Status)
	}

	// Verify target is now superseded.
	target := getMemory(t, st, scope, targetMem.ID)
	if target.Status != "superseded" {
		t.Fatalf("target not superseded: status %q", target.Status)
	}

	// Verify the memory.superseded event carries a prior-state snapshot.
	events, err := st.Events().ListBySubject(context.Background(), scope, targetMem.ID, 20)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	var supersededEvent *store.Event
	for i := range events {
		if events[i].Type == "memory.superseded" {
			supersededEvent = &events[i]
			break
		}
	}
	if supersededEvent == nil {
		t.Fatal("no memory.superseded event found for target")
		return // unreachable; makes non-nil provable to staticcheck (SA5011)
	}
	// The payload must be parseable as a prior-state JSON with an id field.
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(supersededEvent.Payload), &payload); err != nil {
		t.Fatalf("superseded event payload is not valid JSON: %v", err)
	}
	if payload["id"] != targetMem.ID {
		t.Errorf("superseded event payload id: got %v want %q", payload["id"], targetMem.ID)
	}
	if payload["content"] != targetMem.Content {
		t.Errorf("superseded event payload content mismatch")
	}
}

// TestConfirm_DefaultProfile verifies the production defaults are 72h and 2.
func TestConfirm_DefaultProfile(t *testing.T) {
	p := lifecycle.DefaultProfile()
	if p.ConfirmTTL != 72*time.Hour {
		t.Errorf("ConfirmTTL: got %v want 72h (D-065)", p.ConfirmTTL)
	}
	if p.ConfirmRepeats != 2 {
		t.Errorf("ConfirmRepeats: got %d want 2 (D-065)", p.ConfirmRepeats)
	}
}

// TestConfirm_PaginationCursor verifies that when there are more eligible
// memories than one page (confirmBatchPageSize=50), the sweep uses the cursor
// to fetch additional pages and stops at ConfirmBatchSize.
// Covers: cursor = next (pagination), and the processed >= batchSize early break.
func TestConfirm_PaginationCursor(t *testing.T) {
	t.Parallel()
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "tenant-cpag"}
	oldCreatedAt := time.Now().Add(-5 * time.Minute).UnixMilli()

	// Insert 55 eligible pending_confirmation memories.
	const total = 55
	for i := 0; i < total; i++ {
		insertParkedMemory(t, st, scope, store.Memory{
			CreatedAt: oldCreatedAt,
			UpdatedAt: oldCreatedAt,
		})
	}

	ingest := make(chan pipeline.Item, 8)
	// ConfirmBatchSize=51 causes the sweep to:
	//   1. fetch first page of 50 (cursor = next), promote all 50
	//   2. fetch second page, promote 1 more, then hit processed >= batchSize break
	profile := lifecycle.Profile{
		ConfirmTTL:       2 * time.Minute,
		ConfirmRepeats:   100,
		ConfirmInterval:  24 * time.Hour,
		ConfirmBatchSize: 51,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// Exactly 51 should be promoted; 4 should remain pending.
	activeCount := 0
	pendingCount := 0
	batch, _, err := st.Memories().ListByStatus(context.Background(), scope, "active", 100, "")
	if err == nil {
		activeCount = len(batch)
	}
	pendingBatch, _, err := st.Memories().ListByStatus(context.Background(), scope, "pending_confirmation", 100, "")
	if err == nil {
		pendingCount = len(pendingBatch)
	}
	if activeCount != 51 {
		t.Errorf("active after pagination sweep: got %d want 51", activeCount)
	}
	if pendingCount != total-51 {
		t.Errorf("pending after pagination sweep: got %d want %d", pendingCount, total-51)
	}
}
