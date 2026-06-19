package conformance

// Phase 09 conformance tests: VectorStore (Upsert/Scan/Delete/ListWithoutVectors)
// and MemoryStore extensions (LexicalSearch, QuerySearch, GetMany).
// Proves scope isolation (cross-tenant AND cross-user), upsert-replace,
// window and kind filters, and FTS sync on both drivers.

import (
	"context"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// --- helpers for Phase 09 ---------------------------------------------------

// insertActiveMemory inserts a minimal active memory via Commit and returns its ID.
func insertActiveMemory(t *testing.T, s store.Store, scope identity.Scope, content, kind string, entities, keywords, queries []string) string {
	t.Helper()
	id := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: kind, Content: content, Context: "ctx",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: newID(), // unique hash
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities: entities,
		Keywords: keywords,
		Queries:  queries,
		Events: []store.Event{
			{ID: newID(), Type: "memory.added", SubjectID: id, Payload: `{}`},
		},
	}
	if err := s.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("insertActiveMemory Commit: %v", err)
	}
	return id
}

// insertActiveMemoryAt inserts an active memory with a specific created_at.
func insertActiveMemoryAt(t *testing.T, s store.Store, scope identity.Scope, content, kind string, createdAt int64) string {
	t.Helper()
	id := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: kind, Content: content, Context: "ctx",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: newID(),
			CreatedAt: createdAt, UpdatedAt: createdAt,
		},
		Events: []store.Event{
			{ID: newID(), Type: "memory.added", SubjectID: id, Payload: `{}`},
		},
	}
	if err := s.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("insertActiveMemoryAt Commit: %v", err)
	}
	return id
}

func testVec(dims int, val float32) []float32 {
	v := make([]float32, dims)
	for i := range v {
		v[i] = val
	}
	return v
}

// --- VectorStore conformance ------------------------------------------------

// testVectorDistinctModels verifies the reindex-guard support method: distinct
// embedding model names across all vectors (unscoped).
func testVectorDistinctModels(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	// Empty store ⇒ empty slice.
	got, err := s.Vectors().DistinctModels(ctx)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty store: got %v want []", got)
	}

	scope := tenantScope("t-" + newID())
	m1 := insertActiveMemory(t, s, scope, "v1", "fact", nil, nil, nil)
	m2 := insertActiveMemory(t, s, scope, "v2", "fact", nil, nil, nil)
	if err := s.Vectors().Upsert(ctx, scope, store.StoredVector{MemoryID: m1, Model: "model-a", Dims: 4, Vec: testVec(4, 0.5)}); err != nil {
		t.Fatalf("upsert m1: %v", err)
	}
	if err := s.Vectors().Upsert(ctx, scope, store.StoredVector{MemoryID: m2, Model: "model-b", Dims: 4, Vec: testVec(4, 0.5)}); err != nil {
		t.Fatalf("upsert m2: %v", err)
	}

	got, err = s.Vectors().DistinctModels(ctx)
	if err != nil {
		t.Fatalf("distinct: %v", err)
	}
	if len(got) != 2 || got[0] != "model-a" || got[1] != "model-b" {
		t.Fatalf("expected sorted [model-a, model-b], got %v", got)
	}
}

func testVectorUpsertScan(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertActiveMemory(t, s, scope, "vector test memory", "fact", nil, nil, nil)

	vec := testVec(4, 0.5)
	sv := store.StoredVector{
		MemoryID: memID, Model: "test-model", Dims: 4, Vec: vec,
	}
	if err := s.Vectors().Upsert(ctx, scope, sv); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := s.Vectors().Scan(ctx, scope, nil, store.Window{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Scan: got %d results, want 1", len(results))
	}
	if results[0].MemoryID != memID {
		t.Errorf("Scan MemoryID: got %q want %q", results[0].MemoryID, memID)
	}
	if len(results[0].Vec) != 4 {
		t.Errorf("Scan Vec dims: got %d want 4", len(results[0].Vec))
	}
}

func testVectorUpsertReplace(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertActiveMemory(t, s, scope, "replace test", "fact", nil, nil, nil)

	// First upsert.
	first := testVec(4, 0.1)
	if err := s.Vectors().Upsert(ctx, scope, store.StoredVector{
		MemoryID: memID, Model: "model-v1", Dims: 4, Vec: first,
	}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	// Second upsert — should replace.
	second := testVec(4, 0.9)
	if err := s.Vectors().Upsert(ctx, scope, store.StoredVector{
		MemoryID: memID, Model: "model-v2", Dims: 4, Vec: second,
	}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	results, err := s.Vectors().Scan(ctx, scope, nil, store.Window{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Scan after replace: got %d, want 1", len(results))
	}
	// The model and vector should reflect the second upsert.
	if results[0].Model != "model-v2" {
		t.Errorf("Model after replace: got %q want model-v2", results[0].Model)
	}
	if results[0].Vec[0] < 0.8 {
		t.Errorf("Vec not replaced: vec[0] = %v, want ~0.9", results[0].Vec[0])
	}
}

func testVectorDelete(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertActiveMemory(t, s, scope, "delete test", "fact", nil, nil, nil)

	if err := s.Vectors().Upsert(ctx, scope, store.StoredVector{
		MemoryID: memID, Model: "m", Dims: 4, Vec: testVec(4, 0.5),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.Vectors().Delete(ctx, scope, memID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	results, err := s.Vectors().Scan(ctx, scope, nil, store.Window{})
	if err != nil {
		t.Fatalf("Scan after delete: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results after Delete, got %d", len(results))
	}

	// Delete of absent ID is a no-op (must not error).
	if err := s.Vectors().Delete(ctx, scope, "nonexistent"); err != nil {
		t.Errorf("Delete absent: unexpected error: %v", err)
	}
}

func testVectorScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	memA := insertActiveMemory(t, s, scopeA, "tenant A memory", "fact", nil, nil, nil)
	memB := insertActiveMemory(t, s, scopeB, "tenant B memory", "fact", nil, nil, nil)

	if err := s.Vectors().Upsert(ctx, scopeA, store.StoredVector{
		MemoryID: memA, Model: "m", Dims: 4, Vec: testVec(4, 0.1),
	}); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}
	if err := s.Vectors().Upsert(ctx, scopeB, store.StoredVector{
		MemoryID: memB, Model: "m", Dims: 4, Vec: testVec(4, 0.9),
	}); err != nil {
		t.Fatalf("Upsert B: %v", err)
	}

	// Tenant A scan should only see its own vector.
	resA, err := s.Vectors().Scan(ctx, scopeA, nil, store.Window{})
	if err != nil {
		t.Fatalf("Scan A: %v", err)
	}
	if len(resA) != 1 || resA[0].MemoryID != memA {
		t.Errorf("Scan A: got %v, want only memA", resA)
	}

	// Tenant B scan should only see its own vector.
	resB, err := s.Vectors().Scan(ctx, scopeB, nil, store.Window{})
	if err != nil {
		t.Fatalf("Scan B: %v", err)
	}
	if len(resB) != 1 || resB[0].MemoryID != memB {
		t.Errorf("Scan B: got %v, want only memB", resB)
	}
}

func testVectorCrossUserIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "tenant-" + newID()
	scopeU1 := mustScope(tenant, "", "user-1", "")
	scopeU2 := mustScope(tenant, "", "user-2", "")

	memU1 := insertActiveMemory(t, s, scopeU1, "user 1 memory", "fact", nil, nil, nil)
	memU2 := insertActiveMemory(t, s, scopeU2, "user 2 memory", "fact", nil, nil, nil)

	if err := s.Vectors().Upsert(ctx, scopeU1, store.StoredVector{
		MemoryID: memU1, Model: "m", Dims: 4, Vec: testVec(4, 0.1),
	}); err != nil {
		t.Fatalf("Upsert U1: %v", err)
	}
	if err := s.Vectors().Upsert(ctx, scopeU2, store.StoredVector{
		MemoryID: memU2, Model: "m", Dims: 4, Vec: testVec(4, 0.9),
	}); err != nil {
		t.Fatalf("Upsert U2: %v", err)
	}

	resU1, err := s.Vectors().Scan(ctx, scopeU1, nil, store.Window{})
	if err != nil {
		t.Fatalf("Scan U1: %v", err)
	}
	if len(resU1) != 1 || resU1[0].MemoryID != memU1 {
		t.Errorf("cross-user isolation breach: U1 scan got %v", resU1)
	}
}

func testVectorKindFilter(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	factID := insertActiveMemory(t, s, scope, "a fact memory", "fact", nil, nil, nil)
	prefID := insertActiveMemory(t, s, scope, "a preference memory", "preference", nil, nil, nil)

	for _, pair := range []struct{ id, model string }{
		{factID, "m"}, {prefID, "m"},
	} {
		if err := s.Vectors().Upsert(ctx, scope, store.StoredVector{
			MemoryID: pair.id, Model: pair.model, Dims: 4, Vec: testVec(4, 0.5),
		}); err != nil {
			t.Fatalf("Upsert %v: %v", pair.id, err)
		}
	}

	// Filter to kind=fact only.
	res, err := s.Vectors().Scan(ctx, scope, []string{"fact"}, store.Window{})
	if err != nil {
		t.Fatalf("Scan with kind filter: %v", err)
	}
	if len(res) != 1 || res[0].MemoryID != factID {
		t.Errorf("kind filter: got %v, want only factID", res)
	}
}

func testVectorWindowFilter(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	now := time.Now().UnixMilli()
	oldID := insertActiveMemoryAt(t, s, scope, "old memory", "fact", now-10000)
	newID_ := insertActiveMemoryAt(t, s, scope, "new memory", "fact", now)

	for _, id := range []string{oldID, newID_} {
		if err := s.Vectors().Upsert(ctx, scope, store.StoredVector{
			MemoryID: id, Model: "m", Dims: 4, Vec: testVec(4, 0.5),
		}); err != nil {
			t.Fatalf("Upsert %v: %v", id, err)
		}
	}

	// Window that excludes the old memory.
	res, err := s.Vectors().Scan(ctx, scope, nil, store.Window{From: now - 5000})
	if err != nil {
		t.Fatalf("Scan with window: %v", err)
	}
	if len(res) != 1 || res[0].MemoryID != newID_ {
		t.Errorf("window filter: got %v, want only newID", res)
	}
}

func testVectorListWithoutVectors(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert two memories — one with a vector, one without.
	withVec := insertActiveMemory(t, s, scope, "has vector", "fact", []string{"ent-A"}, []string{"kw-B"}, nil)
	noVec := insertActiveMemory(t, s, scope, "no vector", "fact", nil, nil, nil)

	// Give withVec a vector.
	if err := s.Vectors().Upsert(ctx, scope, store.StoredVector{
		MemoryID: withVec, Model: "m", Dims: 4, Vec: testVec(4, 0.5),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	missing, err := s.Vectors().ListWithoutVectors(ctx, 10)
	if err != nil {
		t.Fatalf("ListWithoutVectors: %v", err)
	}

	found := false
	for _, m := range missing {
		if m.MemoryID == noVec {
			found = true
		}
		if m.MemoryID == withVec {
			t.Errorf("ListWithoutVectors returned memID that has a vector: %v", withVec)
		}
	}
	if !found {
		t.Errorf("ListWithoutVectors did not return memory without vector: %v", noVec)
	}
}

func testVectorScopeRequired(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	empty := identity.Scope{} // empty tenant

	if err := s.Vectors().Upsert(ctx, empty, store.StoredVector{MemoryID: "x", Dims: 4, Vec: testVec(4, 0.5)}); err == nil {
		t.Error("Upsert with empty scope: expected ErrScopeRequired, got nil")
	}
	if _, err := s.Vectors().Scan(ctx, empty, nil, store.Window{}); err == nil {
		t.Error("Scan with empty scope: expected ErrScopeRequired, got nil")
	}
	if err := s.Vectors().Delete(ctx, empty, "x"); err == nil {
		t.Error("Delete with empty scope: expected ErrScopeRequired, got nil")
	}
}

// --- MemoryStore lexical + GetMany conformance ------------------------------

func testLexicalSearch(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert a memory with a distinctive term.
	memID := insertActiveMemory(t, s, scope, "PostgreSQL ACID compliance ensures durability", "fact", nil, nil, nil)
	// Insert a memory with different content.
	_ = insertActiveMemory(t, s, scope, "Go uses goroutines for concurrency", "fact", nil, nil, nil)

	// Search for "PostgreSQL".
	hits, err := s.Memories().LexicalSearch(ctx, scope, "PostgreSQL", 10, store.Window{}, nil)
	if err != nil {
		t.Fatalf("LexicalSearch: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("LexicalSearch: expected at least 1 hit, got 0")
	}
	if hits[0].MemoryID != memID {
		t.Errorf("LexicalSearch top result: got %q want %q", hits[0].MemoryID, memID)
	}
	if hits[0].Rank <= 0 {
		t.Errorf("LexicalSearch rank should be > 0, got %v", hits[0].Rank)
	}
}

func testLexicalSearchWindow(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	now := time.Now().UnixMilli()
	oldID := insertActiveMemoryAt(t, s, scope, "PostgreSQL old memory durability", "fact", now-10000)
	newID_ := insertActiveMemoryAt(t, s, scope, "PostgreSQL new memory durability", "fact", now)

	// Window excludes old memory.
	hits, err := s.Memories().LexicalSearch(ctx, scope, "PostgreSQL durability", 10, store.Window{From: now - 5000}, nil)
	if err != nil {
		t.Fatalf("LexicalSearch with window: %v", err)
	}
	for _, h := range hits {
		if h.MemoryID == oldID {
			t.Errorf("window filter failed: old memory appeared in results")
		}
	}
	found := false
	for _, h := range hits {
		if h.MemoryID == newID_ {
			found = true
		}
	}
	if !found {
		t.Errorf("window filter: new memory not found in results")
	}
}

func testLexicalSearchScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	_ = insertActiveMemory(t, s, scopeA, "unique lexical term xenolith alpha", "fact", nil, nil, nil)
	_ = insertActiveMemory(t, s, scopeB, "unique lexical term xenolith beta", "fact", nil, nil, nil)

	// Tenant A search must not see tenant B's memory.
	hits, err := s.Memories().LexicalSearch(ctx, scopeA, "xenolith", 10, store.Window{}, nil)
	if err != nil {
		t.Fatalf("LexicalSearch scope A: %v", err)
	}
	for _, h := range hits {
		m, err := s.Memories().Get(ctx, scopeA, h.MemoryID)
		if err != nil {
			t.Fatalf("Get %v in scope A: %v", h.MemoryID, err)
		}
		if m.TenantID != scopeA.Tenant {
			t.Errorf("cross-tenant leak: got memory from tenant %q in scope A search", m.TenantID)
		}
	}
}

func testQuerySearch(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertActiveMemory(t, s, scope, "cache implementation details", "fact",
		nil, nil, []string{"how does the cache work", "cache invalidation strategy"})
	// Unrelated memory.
	_ = insertActiveMemory(t, s, scope, "unrelated content", "fact", nil, nil, []string{"something else"})

	hits, err := s.Memories().QuerySearch(ctx, scope, "cache work", 10, store.Window{})
	if err != nil {
		t.Fatalf("QuerySearch: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("QuerySearch: expected at least 1 hit")
	}
	if hits[0].MemoryID != memID {
		t.Errorf("QuerySearch top result: got %q want %q", hits[0].MemoryID, memID)
	}
}

func testQuerySearchScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	_ = insertActiveMemory(t, s, scopeA, "scope A content", "fact", nil, nil, []string{"unique xenolith query"})
	_ = insertActiveMemory(t, s, scopeB, "scope B content", "fact", nil, nil, []string{"unique xenolith query"})

	hits, err := s.Memories().QuerySearch(ctx, scopeA, "xenolith", 10, store.Window{})
	if err != nil {
		t.Fatalf("QuerySearch scope A: %v", err)
	}
	for _, h := range hits {
		m, err := s.Memories().Get(ctx, scopeA, h.MemoryID)
		if err != nil {
			t.Fatalf("Get in scope A: %v", err)
		}
		if m.TenantID != scopeA.Tenant {
			t.Errorf("cross-tenant leak in QuerySearch")
		}
	}
}

func testMemoryGetMany(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	id1 := insertActiveMemory(t, s, scope, "memory one", "fact", nil, nil, nil)
	id2 := insertActiveMemory(t, s, scope, "memory two", "preference", nil, nil, nil)
	id3 := insertActiveMemory(t, s, scope, "memory three", "decision", nil, nil, nil)

	mems, err := s.Memories().GetMany(ctx, scope, []string{id1, id3, "nonexistent"})
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if len(mems) != 2 {
		t.Fatalf("GetMany: got %d, want 2 (id1+id3)", len(mems))
	}
	// id2 must not appear.
	for _, m := range mems {
		if m.ID == id2 {
			t.Errorf("GetMany returned id2 which was not requested")
		}
	}
	// Both id1 and id3 must appear.
	ids := map[string]bool{id1: false, id3: false}
	for _, m := range mems {
		ids[m.ID] = true
	}
	for id, found := range ids {
		if !found {
			t.Errorf("GetMany missing expected id: %v", id)
		}
	}
}

func testMemoryGetManyEmpty(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	mems, err := s.Memories().GetMany(ctx, scope, nil)
	if err != nil {
		t.Fatalf("GetMany(nil): %v", err)
	}
	if len(mems) != 0 {
		t.Errorf("GetMany(nil): got %d, want 0", len(mems))
	}
}
