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
