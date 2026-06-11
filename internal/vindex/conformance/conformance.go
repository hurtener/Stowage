// Package conformance provides a driver-agnostic test suite for vindex.Index.
// Run against each driver (sqlitebrute via sqlite store, pgbrute via pg store)
// to guarantee identical semantics.
//
// Usage:
//
//	func TestMyIndex(t *testing.T) {
//	    conformance.Run(t, func() (vindex.Index, store.Store, func()) {
//	        st := openTestStore(t)
//	        vi := vindex.New(st.Vectors(), 4, "test-model")
//	        return vi, st, func() { st.Close(context.Background()) }
//	    })
//	}
package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// Factory returns a ready-to-use Index, the underlying Store, and a cleanup
// function. The Store is needed to insert memories (via Commit) before testing.
type Factory func() (vindex.Index, store.Store, func())

// Run executes the full vindex conformance suite.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("UpsertSearch", func(t *testing.T) { testUpsertSearch(t, factory) })
	t.Run("UpsertReplace", func(t *testing.T) { testUpsertReplace(t, factory) })
	t.Run("Delete", func(t *testing.T) { testDelete(t, factory) })
	t.Run("ScopeIsolation", func(t *testing.T) { testScopeIsolation(t, factory) })
	t.Run("CrossUserIsolation", func(t *testing.T) { testCrossUserIsolation(t, factory) })
	t.Run("DimsMismatch", func(t *testing.T) { testDimsMismatch(t, factory) })
	t.Run("KindFilter", func(t *testing.T) { testKindFilter(t, factory) })
	t.Run("WindowFilter", func(t *testing.T) { testWindowFilter(t, factory) })
	t.Run("DegradedNilVec", func(t *testing.T) { testDegradedNilVec(t, factory) })
}

// --- helpers ----------------------------------------------------------------

func newID() string { return ulid.Make().String() }

func nowMs() int64 { return time.Now().UnixMilli() }

func tenantScope(tenant string) identity.Scope {
	return identity.Scope{Tenant: tenant}
}

func userScope(tenant, user string) identity.Scope {
	return identity.Scope{Tenant: tenant, User: user}
}

// insertMemory commits a minimal active memory and returns its ID.
func insertMemory(t *testing.T, s store.Store, scope identity.Scope, content, kind string, createdAt int64) string {
	t.Helper()
	id := newID()
	ts := createdAt
	if ts == 0 {
		ts = nowMs()
	}
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: kind, Content: content, Context: "ctx",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: newID(),
			CreatedAt: ts, UpdatedAt: ts,
		},
		Events: []store.Event{
			{ID: newID(), Type: "memory.added", SubjectID: id, Payload: `{}`},
		},
	}
	if err := s.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("insertMemory Commit: %v", err)
	}
	return id
}

// unitVec returns a unit-length vector of given dims with all components equal.
func unitVec(dims int) []float32 {
	v := make([]float32, dims)
	val := float32(1.0 / float64(dims))
	for i := range v {
		v[i] = val
	}
	return v
}

func altVec(dims int) []float32 {
	v := make([]float32, dims)
	for i := range v {
		if i%2 == 0 {
			v[i] = 1.0
		} else {
			v[i] = -1.0
		}
	}
	return v
}

// --- tests ------------------------------------------------------------------

func testUpsertSearch(t *testing.T, factory Factory) {
	t.Helper()
	vi, s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertMemory(t, s, scope, "test memory", "fact", 0)
	vec := unitVec(4)

	if err := vi.Upsert(ctx, scope, memID, vec); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	hits, err := vi.Search(ctx, scope, vec, 10, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search: got %d hits, want 1", len(hits))
	}
	if hits[0].MemoryID != memID {
		t.Errorf("Search hit: got %q want %q", hits[0].MemoryID, memID)
	}
	if hits[0].Score < 0.99 {
		t.Errorf("Search: score of identical vector should be ~1.0, got %v", hits[0].Score)
	}
}

func testUpsertReplace(t *testing.T, factory Factory) {
	t.Helper()
	vi, s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertMemory(t, s, scope, "replace test", "fact", 0)
	first := unitVec(4)
	second := altVec(4)

	if err := vi.Upsert(ctx, scope, memID, first); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := vi.Upsert(ctx, scope, memID, second); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	// Search with the second vec — should get high similarity.
	hits, err := vi.Search(ctx, scope, second, 10, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("Search after replace: got %d hits, want 1", len(hits))
	}
	if hits[0].Score < 0.99 {
		t.Errorf("replace did not take effect: score=%v", hits[0].Score)
	}
}

func testDelete(t *testing.T, factory Factory) {
	t.Helper()
	vi, s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertMemory(t, s, scope, "delete test", "fact", 0)
	if err := vi.Upsert(ctx, scope, memID, unitVec(4)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := vi.Delete(ctx, scope, memID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	hits, err := vi.Search(ctx, scope, unitVec(4), 10, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("expected 0 hits after Delete, got %d", len(hits))
	}

	// Delete absent — must not error.
	if err := vi.Delete(ctx, scope, "nonexistent"); err != nil {
		t.Errorf("Delete absent: unexpected error: %v", err)
	}
}

func testScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	vi, s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	memA := insertMemory(t, s, scopeA, "tenant A memory", "fact", 0)
	memB := insertMemory(t, s, scopeB, "tenant B memory", "fact", 0)

	if err := vi.Upsert(ctx, scopeA, memA, unitVec(4)); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}
	if err := vi.Upsert(ctx, scopeB, memB, unitVec(4)); err != nil {
		t.Fatalf("Upsert B: %v", err)
	}

	hitsA, err := vi.Search(ctx, scopeA, unitVec(4), 10, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search A: %v", err)
	}
	if len(hitsA) != 1 || hitsA[0].MemoryID != memA {
		t.Errorf("scope isolation breach: A search got %v", hitsA)
	}

	hitsB, err := vi.Search(ctx, scopeB, unitVec(4), 10, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search B: %v", err)
	}
	if len(hitsB) != 1 || hitsB[0].MemoryID != memB {
		t.Errorf("scope isolation breach: B search got %v", hitsB)
	}
}

func testCrossUserIsolation(t *testing.T, factory Factory) {
	t.Helper()
	vi, s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "tenant-" + newID()
	scopeU1 := userScope(tenant, "user-1")
	scopeU2 := userScope(tenant, "user-2")

	memU1 := insertMemory(t, s, scopeU1, "user 1 memory", "fact", 0)
	memU2 := insertMemory(t, s, scopeU2, "user 2 memory", "fact", 0)

	if err := vi.Upsert(ctx, scopeU1, memU1, unitVec(4)); err != nil {
		t.Fatalf("Upsert U1: %v", err)
	}
	if err := vi.Upsert(ctx, scopeU2, memU2, unitVec(4)); err != nil {
		t.Fatalf("Upsert U2: %v", err)
	}

	hitsU1, err := vi.Search(ctx, scopeU1, unitVec(4), 10, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search U1: %v", err)
	}
	if len(hitsU1) != 1 || hitsU1[0].MemoryID != memU1 {
		t.Errorf("cross-user isolation breach: U1 search got %v", hitsU1)
	}
}

func testDimsMismatch(t *testing.T, factory Factory) {
	t.Helper()
	vi, s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertMemory(t, s, scope, "dims test", "fact", 0)

	// Upsert with wrong dims (4 expected, sending 8).
	wrongDims := make([]float32, 8)
	err := vi.Upsert(ctx, scope, memID, wrongDims)
	if err == nil {
		t.Fatal("expected ErrDimsMismatch, got nil")
	}
	var mismatch vindex.ErrDimsMismatch
	if err.Error() == "" {
		t.Errorf("expected non-empty error message")
	}
	_ = mismatch
}

func testKindFilter(t *testing.T, factory Factory) {
	t.Helper()
	vi, s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	factID := insertMemory(t, s, scope, "a fact", "fact", 0)
	prefID := insertMemory(t, s, scope, "a preference", "preference", 0)

	for _, id := range []string{factID, prefID} {
		if err := vi.Upsert(ctx, scope, id, unitVec(4)); err != nil {
			t.Fatalf("Upsert %v: %v", id, err)
		}
	}

	hits, err := vi.Search(ctx, scope, unitVec(4), 10, vindex.Filter{Kinds: []string{"fact"}})
	if err != nil {
		t.Fatalf("Search with kind filter: %v", err)
	}
	if len(hits) != 1 || hits[0].MemoryID != factID {
		t.Errorf("kind filter: got %v, want only factID", hits)
	}
}

func testWindowFilter(t *testing.T, factory Factory) {
	t.Helper()
	vi, s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	now := nowMs()
	oldID := insertMemory(t, s, scope, "old memory", "fact", now-10000)
	newID_ := insertMemory(t, s, scope, "new memory", "fact", now)

	for _, id := range []string{oldID, newID_} {
		if err := vi.Upsert(ctx, scope, id, unitVec(4)); err != nil {
			t.Fatalf("Upsert %v: %v", id, err)
		}
	}

	hits, err := vi.Search(ctx, scope, unitVec(4), 10, vindex.Filter{
		Window: store.Window{From: now - 5000},
	})
	if err != nil {
		t.Fatalf("Search with window: %v", err)
	}
	if len(hits) != 1 || hits[0].MemoryID != newID_ {
		t.Errorf("window filter: got %v, want only newID", hits)
	}
}

func testDegradedNilVec(t *testing.T, factory Factory) {
	t.Helper()
	vi, _, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// nil vec → returns nil,nil (degraded mode signal)
	hits, err := vi.Search(ctx, scope, nil, 10, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search(nil vec): expected nil error, got %v", err)
	}
	if hits != nil {
		t.Errorf("Search(nil vec): expected nil hits, got %v", hits)
	}
}
