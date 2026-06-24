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

// insertMemoryWithJunctions inserts an active memory with entities and keywords.
func insertMemoryWithJunctions(
	t *testing.T,
	st store.Store,
	scope identity.Scope,
	content string,
	entities, keywords []string,
) string {
	t.Helper()
	now := time.Now().UnixMilli()
	mem := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     content,
		Status:      "active",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// Back the memory with a real record + provenance so sweep merges run
	// the REAL Commit path (P1: empty-provenance merges fail; an earlier
	// fixture without provenance masked threshold mutations via the sweep's
	// log-and-continue on commit errors).
	rec := store.Record{
		ID: ulid.Make().String(), TenantID: scope.Tenant, Role: "user",
		Content: content, OccurredAt: now, CreatedAt: now,
	}
	if err := st.Records().Append(context.Background(), scope, []store.Record{rec}); err != nil {
		t.Fatalf("append backing record: %v", err)
	}
	cs := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   mem,
		Entities: entities,
		Keywords: keywords,
		Provenance: []store.Provenance{{
			ID:       ulid.Make().String(),
			MemoryID: mem.ID, RecordID: rec.ID, SpanStart: 0,
			SpanEnd: len(content), CreatedAt: now,
		}},
		Events: []store.Event{
			{
				ID:        ulid.Make().String(),
				Type:      "memory.added",
				SubjectID: mem.ID,
				Payload:   "{}",
				CreatedAt: now,
			},
		},
		Scope: scope,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("insert memory with junctions: %v", err)
	}
	return mem.ID
}

func TestDedupeSweepMergesNearDuplicates(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "dedup-t1"}

	// Two nearly identical memories with shared entities.
	content1 := "The user prefers dark mode in all applications for better readability"
	content2 := "The user prefers dark mode in all applications for better readability at night" // sim 0.89 vs content1 (probed) — genuinely above the 0.85 threshold
	shared := []string{"entity-user"}
	kw := []string{"dark-mode", "preference"}

	id1 := insertMemoryWithJunctions(t, st, scope, content1, shared, kw)
	id2 := insertMemoryWithJunctions(t, st, scope, content2, shared, kw)

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		DedupeBatchSize: 100,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// The REAL merge contract: BOTH sources superseded, ONE new active
	// merged memory with summed counters and unioned provenance (the
	// pre-fix assertion accepted "at least one superseded", which passed
	// even when every merge silently failed on the ID collision).
	ctx := context.Background()
	mem1, err1 := st.Memories().Get(ctx, scope, id1)
	mem2, err2 := st.Memories().Get(ctx, scope, id2)
	if err1 != nil || err2 != nil {
		t.Fatalf("get memories: err1=%v, err2=%v", err1, err2)
	}
	if mem1.Status != "superseded" || mem2.Status != "superseded" {
		t.Fatalf("both sources must be superseded, got %q/%q", mem1.Status, mem2.Status)
	}
	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("want exactly 1 merged active memory, got %d", len(active))
	}
	if active[0].ID == id1 || active[0].ID == id2 {
		t.Fatalf("merged memory must have a fresh ID")
	}
	jt, err := st.Memories().GetJunctions(ctx, scope, active[0].ID)
	if err != nil || len(jt.Provenance) < 2 {
		t.Fatalf("merged provenance union missing: %v (n=%d)", err, len(jt.Provenance))
	}
}

func TestDedupeSweepDoesNotMergeDifferentContent(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	scope := identity.Scope{Tenant: "dedup-t2"}

	content1 := "The user loves playing chess and solving puzzles"
	content2 := "The project uses PostgreSQL for its primary database"
	shared := []string{"entity-a"}
	kw := []string{"keyword-a"}

	id1 := insertMemoryWithJunctions(t, st, scope, content1, shared, kw)
	id2 := insertMemoryWithJunctions(t, st, scope, content2, shared, kw)

	ingest := make(chan pipeline.Item, 8)
	mgr := lifecycle.New(st, testLogger(), lifecycle.DefaultProfile(), ingest)
	// Run only dedupe sweep.
	ctx := context.Background()
	// Use RunForce to exercise all sweeps including dedupe.
	mgr.RunForce(ctx)

	// Both memories should remain active — content is different.
	mem1 := getMemory(t, st, scope, id1)
	mem2 := getMemory(t, st, scope, id2)

	if mem1.Status != "active" {
		t.Errorf("mem1 should be active, got %q", mem1.Status)
	}
	if mem2.Status != "active" {
		t.Errorf("mem2 should be active, got %q", mem2.Status)
	}
}

func TestDedupeSweepEmptyStore(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	ingest := make(chan pipeline.Item, 8)
	mgr := lifecycle.New(st, testLogger(), lifecycle.DefaultProfile(), ingest)
	// Should not panic on empty store.
	mgr.RunForce(context.Background())
}

// insertDedupeMemoryVF is insertMemoryWithJunctions with an explicit ValidFrom and
// TrustSource so SelectSurvivor's primary rule (later ValidFrom) is deterministic.
func insertDedupeMemoryVF(t *testing.T, st store.Store, scope identity.Scope, content string, validFrom int64, entities, keywords []string) string {
	t.Helper()
	now := time.Now().UnixMilli()
	mem := store.Memory{
		ID: ulid.Make().String(), TenantID: scope.Tenant, Kind: "fact", Content: content,
		Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ValidFrom: validFrom, CreatedAt: now, UpdatedAt: now,
	}
	rec := store.Record{ID: ulid.Make().String(), TenantID: scope.Tenant, Role: "user",
		Content: content, OccurredAt: now, CreatedAt: now}
	if err := st.Records().Append(context.Background(), scope, []store.Record{rec}); err != nil {
		t.Fatalf("append backing record: %v", err)
	}
	cs := store.CommitSet{
		Action: store.ActionAdd, Memory: mem, Entities: entities, Keywords: keywords,
		Provenance: []store.Provenance{{ID: ulid.Make().String(), MemoryID: mem.ID, RecordID: rec.ID,
			SpanStart: 0, SpanEnd: len(content), CreatedAt: now}},
		Events: []store.Event{{ID: ulid.Make().String(), Type: "memory.added", SubjectID: mem.ID, Payload: "{}", CreatedAt: now}},
		Scope:  scope,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("insert memory VF: %v", err)
	}
	return mem.ID
}

func activeCount(t *testing.T, st store.Store, scope identity.Scope) int {
	t.Helper()
	act, _, err := st.Memories().ListByStatus(context.Background(), scope, "active", 100, "")
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	return len(act)
}

// TestDedupeSweepNeverMergesAcrossUsers is the regression guard for 29d B1: two users
// under one tenant with near-identical canonical facts must NOT be merged, while
// near-dups WITHIN a user still merge. The pre-fix tenant-wide candidate seed merged
// across users (P3 + P1); this test fails against that code and passes with the
// exact-leaf ListActiveInScope/FindNeighbors path.
func TestDedupeSweepNeverMergesAcrossUsers(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	tenant := "dedup-xuser"
	alice := identity.Scope{Tenant: tenant, User: "alice"}
	bob := identity.Scope{Tenant: tenant, User: "bob"}

	content1 := "The user prefers dark mode in all applications for better readability"
	content2 := "The user prefers dark mode in all applications for better readability at night"
	shared := []string{"entity-user"}
	kw := []string{"dark-mode", "preference"}

	// alice owns a near-dup PAIR (must merge); bob owns one near-identical to alice's
	// (must NOT merge across the user boundary).
	insertMemoryWithJunctions(t, st, alice, content1, shared, kw)
	insertMemoryWithJunctions(t, st, alice, content2, shared, kw)
	bID := insertMemoryWithJunctions(t, st, bob, content1, shared, kw)

	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{DedupeBatchSize: 100}, make(chan pipeline.Item, 8))
	mgr.RunForce(context.Background())

	// alice's pair collapsed to exactly one merged active memory.
	if n := activeCount(t, st, alice); n != 1 {
		t.Errorf("alice active count = %d, want 1 (the near-dup pair must merge within the user)", n)
	}
	// bob is untouched — no cross-user merge.
	if m := getMemory(t, st, bob, bID); m.Status != "active" {
		t.Errorf("bob memory status = %q, want active (cross-user merge is a P3+P1 violation)", m.Status)
	}
	if n := activeCount(t, st, bob); n != 1 {
		t.Errorf("bob active count = %d, want 1", n)
	}
}

// TestDedupeSweepKeepsSurvivorContent is the regression guard for 29d S4: the merged row
// must carry the SURVIVOR's content (later ValidFrom), not an arbitrary one. Reverting
// `merged := survivor` to `merged := target` (or flipping SelectSurvivor) fails this.
func TestDedupeSweepKeepsSurvivorContent(t *testing.T) {
	older := "You need 120 stars for gold level"
	newer := "You need 125 stars for gold level"
	shared := []string{"entity-gold"}

	run := func(t *testing.T, survivorIsNewer bool) string {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := identity.Scope{Tenant: "dedup-surv", User: "u1"}
		t0 := time.Now().Add(-time.Hour).UnixMilli()
		t1 := time.Now().UnixMilli()
		if survivorIsNewer {
			insertDedupeMemoryVF(t, st, scope, older, t0, shared, []string{"k-old"})
			insertDedupeMemoryVF(t, st, scope, newer, t1, shared, []string{"k-new"})
		} else {
			insertDedupeMemoryVF(t, st, scope, newer, t0, shared, []string{"k-old"})
			insertDedupeMemoryVF(t, st, scope, older, t1, shared, []string{"k-new"})
		}
		mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{DedupeBatchSize: 100}, make(chan pipeline.Item, 8))
		mgr.RunForce(context.Background())
		act, _, err := st.Memories().ListByStatus(context.Background(), scope, "active", 10, "")
		if err != nil {
			t.Fatalf("list active: %v", err)
		}
		if len(act) != 1 {
			t.Fatalf("want 1 merged active, got %d", len(act))
		}
		return act[0].Content
	}

	if got := run(t, true); got != newer {
		t.Errorf("survivor content = %q, want the later-ValidFrom value %q", got, newer)
	}
	// Swap which assertion is newer: the survivor follows ValidFrom, not insertion order.
	if got := run(t, false); got != older {
		t.Errorf("swapped survivor content = %q, want %q", got, older)
	}
}

// TestDedupeSweepNumeralCorrectionDropsLoserSurface is the regression guard for 29d S3/S8:
// when the two near-dups carry divergent numerals (a value CORRECTION), the merged surface
// keeps ONLY the survivor's junctions so the stale value's tokens cannot resurface it (P1).
// A non-numeral near-dup instead UNIONS the loser's surface.
func TestDedupeSweepNumeralCorrectionDropsLoserSurface(t *testing.T) {
	shared := []string{"entity-gold"}

	t.Run("correction_drops_loser_surface", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := identity.Scope{Tenant: "dedup-corr", User: "u1"}
		t0 := time.Now().Add(-time.Hour).UnixMilli()
		t1 := time.Now().UnixMilli()
		insertDedupeMemoryVF(t, st, scope, "You need 120 stars for gold level", t0, shared, []string{"stars-120"})
		insertDedupeMemoryVF(t, st, scope, "You need 125 stars for gold level", t1, shared, []string{"stars-125"})

		mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{DedupeBatchSize: 100}, make(chan pipeline.Item, 8))
		mgr.RunForce(context.Background())

		act, _, _ := st.Memories().ListByStatus(context.Background(), scope, "active", 10, "")
		if len(act) != 1 {
			t.Fatalf("want 1 merged active, got %d", len(act))
		}
		if act[0].Content != "You need 125 stars for gold level" {
			t.Errorf("merged content = %q, want the corrected (survivor) value", act[0].Content)
		}
		jt, _ := st.Memories().GetJunctions(context.Background(), scope, act[0].ID)
		for _, k := range jt.Keywords {
			if k == "stars-120" {
				t.Errorf("numeral correction must DROP the loser-only token, but stars-120 survives in %v", jt.Keywords)
			}
		}
	})

	t.Run("true_duplicate_unions_surface", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := identity.Scope{Tenant: "dedup-union", User: "u1"}
		t0 := time.Now().Add(-time.Hour).UnixMilli()
		t1 := time.Now().UnixMilli()
		c1 := "The user prefers dark mode in all applications for better readability"
		c2 := "The user prefers dark mode in all applications for better readability at night"
		insertDedupeMemoryVF(t, st, scope, c1, t0, shared, []string{"k-loser"})
		insertDedupeMemoryVF(t, st, scope, c2, t1, shared, []string{"k-surv"})

		mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{DedupeBatchSize: 100}, make(chan pipeline.Item, 8))
		mgr.RunForce(context.Background())

		act, _, _ := st.Memories().ListByStatus(context.Background(), scope, "active", 10, "")
		if len(act) != 1 {
			t.Fatalf("want 1 merged active, got %d", len(act))
		}
		jt, _ := st.Memories().GetJunctions(context.Background(), scope, act[0].ID)
		var hasLoser bool
		for _, k := range jt.Keywords {
			if k == "k-loser" {
				hasLoser = true
			}
		}
		if !hasLoser {
			t.Errorf("a non-numeral true duplicate must UNION the loser's surface, but k-loser is absent from %v", jt.Keywords)
		}
	})
}

// TestDedupeSweepInvalidatesCacheAtTenant is the regression guard for 29d N3/S5: a merge
// invalidates the retrieval cache, and at TENANT granularity (the cache is tenant-keyed).
func TestDedupeSweepInvalidatesCacheAtTenant(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "dedup-inv", User: "u1"}
	c1 := "The user prefers dark mode in all applications for better readability"
	c2 := "The user prefers dark mode in all applications for better readability at night"
	shared := []string{"entity-user"}
	kw := []string{"dark-mode"}
	insertMemoryWithJunctions(t, st, scope, c1, shared, kw)
	insertMemoryWithJunctions(t, st, scope, c2, shared, kw)

	inv := &countingInvalidator{}
	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{DedupeBatchSize: 100}, make(chan pipeline.Item, 8))
	mgr.SetScopeInvalidator(inv)
	mgr.RunForce(context.Background())

	if len(inv.scopes) == 0 {
		t.Fatal("dedupe merge did not invalidate the retrieval cache (D-118)")
	}
	for _, s := range inv.scopes {
		if s.User != "" || s.Project != "" {
			t.Errorf("invalidation scope = %s, want tenant-only (the cache is tenant-keyed, 29d S5)", s.String())
		}
	}
}
