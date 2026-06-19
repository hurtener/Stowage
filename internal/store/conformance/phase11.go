package conformance

// Phase 11 conformance tests: InjectionStore (Append/Get/ListByResponse/MarkWrongCitation)
// and MemoryStore.ApplyFeedback and RecordStore.GetMany.
// Proves scope isolation (cross-tenant), empty-scope rejection, and atomic counter semantics.

import (
	"context"
	"testing"

	"github.com/hurtener/stowage/internal/store"
)

// --- InjectionStore conformance ---------------------------------------------

func testInjectionAppendGet(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertActiveMemory(t, s, scope, "injection test memory", "fact", nil, nil, nil)
	inj := store.Injection{
		ID:         newID(),
		ResponseID: newID(),
		MemoryID:   memID,
		Rank:       1,
		Score:      0.9,
		Lane:       "lexical",
		CreatedAt:  nowMs(),
	}
	if err := s.Injections().Append(ctx, scope, []store.Injection{inj}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Injections().Get(ctx, scope, inj.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MemoryID != inj.MemoryID {
		t.Errorf("memory_id: got %q want %q", got.MemoryID, inj.MemoryID)
	}
	if got.Score != inj.Score {
		t.Errorf("score: got %v want %v", got.Score, inj.Score)
	}
	if got.Lane != inj.Lane {
		t.Errorf("lane: got %q want %q", got.Lane, inj.Lane)
	}
}

func testInjectionAppendIdempotent(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertActiveMemory(t, s, scope, "idempotent test memory", "fact", nil, nil, nil)
	inj := store.Injection{
		ID:         newID(),
		ResponseID: newID(),
		MemoryID:   memID,
		Rank:       1,
		Score:      0.9,
		Lane:       "lexical",
		CreatedAt:  nowMs(),
	}
	if err := s.Injections().Append(ctx, scope, []store.Injection{inj}); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	// Duplicate ID must be silently ignored.
	inj2 := inj
	inj2.Score = 0.1
	if err := s.Injections().Append(ctx, scope, []store.Injection{inj2}); err != nil {
		t.Fatalf("second Append: %v", err)
	}
	got, _ := s.Injections().Get(ctx, scope, inj.ID)
	if got.Score != 0.9 {
		t.Errorf("idempotency broken: score got %v want 0.9", got.Score)
	}
}

func testInjectionGetNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	if _, err := s.Injections().Get(ctx, scope, "no-such-id"); err == nil {
		t.Error("expected ErrNotFound for missing injection")
	}
}

func testInjectionListByResponse(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	responseID := newID()
	// Insert 3 injections for the same response, each with a distinct memory.
	for i := 0; i < 3; i++ {
		memID := insertActiveMemory(t, s, scope, "list-response memory", "fact", nil, nil, nil)
		inj := store.Injection{
			ID:         newID(),
			ResponseID: responseID,
			MemoryID:   memID,
			Rank:       i + 1,
			Score:      float64(3-i) * 0.3,
			Lane:       "lexical",
			CreatedAt:  nowMs(),
		}
		if err := s.Injections().Append(ctx, scope, []store.Injection{inj}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	// Insert one more injection for a different response (must NOT appear).
	otherMemID := insertActiveMemory(t, s, scope, "other-response memory", "fact", nil, nil, nil)
	otherInj := store.Injection{
		ID:         newID(),
		ResponseID: newID(),
		MemoryID:   otherMemID,
		Rank:       1,
		Score:      0.5,
		Lane:       "vector",
		CreatedAt:  nowMs(),
	}
	if err := s.Injections().Append(ctx, scope, []store.Injection{otherInj}); err != nil {
		t.Fatalf("Append other: %v", err)
	}

	rows, err := s.Injections().ListByResponse(ctx, scope, responseID)
	if err != nil {
		t.Fatalf("ListByResponse: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("got %d rows want 3", len(rows))
	}
	// Verify ordered by rank ascending.
	for i := 1; i < len(rows); i++ {
		if rows[i].Rank < rows[i-1].Rank {
			t.Errorf("rows not ordered by rank: rows[%d].Rank=%d < rows[%d].Rank=%d",
				i, rows[i].Rank, i-1, rows[i-1].Rank)
		}
	}
}

func testInjectionScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	memID := insertActiveMemory(t, s, scopeA, "scope isolation memory", "fact", nil, nil, nil)
	inj := store.Injection{
		ID:         newID(),
		ResponseID: newID(),
		MemoryID:   memID,
		Rank:       1,
		Score:      0.8,
		Lane:       "lexical",
		CreatedAt:  nowMs(),
	}
	if err := s.Injections().Append(ctx, scopeA, []store.Injection{inj}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Tenant B must not see tenant A's injection.
	if _, err := s.Injections().Get(ctx, scopeB, inj.ID); err == nil {
		t.Error("cross-tenant isolation violated: Get returned no error for another tenant's injection")
	}
	rows, err := s.Injections().ListByResponse(ctx, scopeB, inj.ResponseID)
	if err != nil {
		t.Fatalf("ListByResponse: %v", err)
	}
	for _, r := range rows {
		if r.ID == inj.ID {
			t.Error("cross-tenant isolation violated: injection visible in wrong tenant")
		}
	}
}

func testMarkWrongCitation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert the memory the injection points to.
	memID := insertActiveMemory(t, s, scope, "test memory for wrong citation", "fact", nil, nil, nil)
	mem, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get memory: %v", err)
	}
	beforeNoise := mem.NoiseCount
	beforeFail := mem.FailCount

	inj := store.Injection{
		ID:         newID(),
		ResponseID: newID(),
		MemoryID:   memID,
		Rank:       1,
		Score:      0.8,
		Lane:       "lexical",
		CreatedAt:  nowMs(),
	}
	if err := s.Injections().Append(ctx, scope, []store.Injection{inj}); err != nil {
		t.Fatalf("Append injection: %v", err)
	}

	if err := s.Injections().MarkWrongCitation(ctx, scope, inj.ID); err != nil {
		t.Fatalf("MarkWrongCitation: %v", err)
	}

	// Injection.Feedback must be "wrong_citation".
	gotInj, err := s.Injections().Get(ctx, scope, inj.ID)
	if err != nil {
		t.Fatalf("Get injection after MarkWrongCitation: %v", err)
	}
	if gotInj.Feedback != "wrong_citation" {
		t.Errorf("injection.feedback: got %q want wrong_citation", gotInj.Feedback)
	}

	// Memory noise_count and fail_count must each have incremented by 1.
	gotMem, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get memory after MarkWrongCitation: %v", err)
	}
	if gotMem.NoiseCount != beforeNoise+1 {
		t.Errorf("noise_count: got %d want %d", gotMem.NoiseCount, beforeNoise+1)
	}
	if gotMem.FailCount != beforeFail+1 {
		t.Errorf("fail_count: got %d want %d", gotMem.FailCount, beforeFail+1)
	}
}

func testMarkWrongCitationNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	if err := s.Injections().MarkWrongCitation(ctx, scope, "no-such-id"); err == nil {
		t.Error("expected ErrNotFound for missing citation")
	}
}

// --- InjectionStore.HubSignals conformance (D-092) --------------------------

// testHubSignals proves the core durable hub-signal semantics: HubSignals counts
// DISTINCT query_sig values per memory, deduping repeats of the same signature.
func testHubSignals(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memHub := insertActiveMemory(t, s, scope, "generic hub memory", "fact", nil, nil, nil)
	memNarrow := insertActiveMemory(t, s, scope, "specific narrow memory", "fact", nil, nil, nil)

	// memHub returned by 3 distinct query clusters; one cluster repeats (must dedup → 3).
	// memNarrow returned by 1 cluster.
	rows := []store.Injection{
		{ID: newID(), ResponseID: newID(), MemoryID: memHub, QuerySig: "sig-a", CreatedAt: nowMs()},
		{ID: newID(), ResponseID: newID(), MemoryID: memHub, QuerySig: "sig-b", CreatedAt: nowMs()},
		{ID: newID(), ResponseID: newID(), MemoryID: memHub, QuerySig: "sig-c", CreatedAt: nowMs()},
		{ID: newID(), ResponseID: newID(), MemoryID: memHub, QuerySig: "sig-a", CreatedAt: nowMs()}, // repeat
		{ID: newID(), ResponseID: newID(), MemoryID: memNarrow, QuerySig: "sig-a", CreatedAt: nowMs()},
	}
	if err := s.Injections().Append(ctx, scope, rows); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Injections().HubSignals(ctx, scope, []string{memHub, memNarrow}, 0)
	if err != nil {
		t.Fatalf("HubSignals: %v", err)
	}
	if got[memHub] != 3 {
		t.Errorf("memHub distinct clusters: got %d want 3 (dedup of sig-a)", got[memHub])
	}
	if got[memNarrow] != 1 {
		t.Errorf("memNarrow distinct clusters: got %d want 1", got[memNarrow])
	}

	// Empty memoryIDs → empty map, no query.
	empty, err := s.Injections().HubSignals(ctx, scope, nil, 0)
	if err != nil {
		t.Fatalf("HubSignals(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("HubSignals(nil): got %d entries want 0", len(empty))
	}
}

// testHubSignalsWindowAndEmptySig proves the recency window excludes old injections
// and that empty query_sig rows (pre-migration / non-retrieve) never count.
func testHubSignalsWindowAndEmptySig(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	mem := insertActiveMemory(t, s, scope, "windowed memory", "fact", nil, nil, nil)
	now := nowMs()
	old := now - 1_000_000 // well before the cutoff we pass below

	rows := []store.Injection{
		{ID: newID(), ResponseID: newID(), MemoryID: mem, QuerySig: "recent-1", CreatedAt: now},
		{ID: newID(), ResponseID: newID(), MemoryID: mem, QuerySig: "recent-2", CreatedAt: now},
		{ID: newID(), ResponseID: newID(), MemoryID: mem, QuerySig: "stale", CreatedAt: old}, // outside window
		{ID: newID(), ResponseID: newID(), MemoryID: mem, QuerySig: "", CreatedAt: now},      // empty sig — never counts
	}
	if err := s.Injections().Append(ctx, scope, rows); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Injections().HubSignals(ctx, scope, []string{mem}, now-1000)
	if err != nil {
		t.Fatalf("HubSignals: %v", err)
	}
	if got[mem] != 2 {
		t.Errorf("windowed distinct clusters: got %d want 2 (stale + empty-sig excluded)", got[mem])
	}
}

// testHubSignalsScopeIsolation proves tenant B's injections never leak into tenant
// A's hub signal (P3).
func testHubSignalsScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	memA := insertActiveMemory(t, s, scopeA, "tenant A memory", "fact", nil, nil, nil)
	if err := s.Injections().Append(ctx, scopeA, []store.Injection{
		{ID: newID(), ResponseID: newID(), MemoryID: memA, QuerySig: "sig-a", CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("Append A: %v", err)
	}

	// Tenant B asking about memA's ID must see zero signals (the ID is not theirs).
	got, err := s.Injections().HubSignals(ctx, scopeB, []string{memA}, 0)
	if err != nil {
		t.Fatalf("HubSignals B: %v", err)
	}
	if got[memA] != 0 {
		t.Errorf("cross-tenant isolation violated: tenant B saw %d signals for tenant A's memory", got[memA])
	}
}

// --- MemoryStore.ApplyFeedback conformance ----------------------------------

func testApplyFeedback(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertActiveMemory(t, s, scope, "feedback test memory", "fact", nil, nil, nil)
	mem, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	tests := []struct {
		signal string
		getter func(*store.Memory) int64
	}{
		{"use", func(m *store.Memory) int64 { return m.UseCount }},
		{"save", func(m *store.Memory) int64 { return m.SaveCount }},
		{"fail", func(m *store.Memory) int64 { return m.FailCount }},
		{"noise", func(m *store.Memory) int64 { return m.NoiseCount }},
	}

	_ = mem // initial counts are all 0
	for _, tc := range tests {
		before, err := s.Memories().Get(ctx, scope, memID)
		if err != nil {
			t.Fatalf("Get before %s: %v", tc.signal, err)
		}
		if err := s.Memories().ApplyFeedback(ctx, scope, memID, tc.signal); err != nil {
			t.Fatalf("ApplyFeedback %s: %v", tc.signal, err)
		}
		after, err := s.Memories().Get(ctx, scope, memID)
		if err != nil {
			t.Fatalf("Get after %s: %v", tc.signal, err)
		}
		if tc.getter(after) != tc.getter(before)+1 {
			t.Errorf("ApplyFeedback %s: counter did not increment (before=%d after=%d)",
				tc.signal, tc.getter(before), tc.getter(after))
		}
		if after.LastAccessedAt == 0 {
			t.Errorf("ApplyFeedback %s: last_accessed_at not touched", tc.signal)
		}
	}
}

func testApplyFeedbackNoopMissing(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// ApplyFeedback on a missing memory is a no-op (not ErrNotFound).
	if err := s.Memories().ApplyFeedback(ctx, scope, "no-such-id", "use"); err != nil {
		t.Errorf("ApplyFeedback missing memory: got %v want nil", err)
	}
}

func testApplyFeedbackUnknownSignal(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := insertActiveMemory(t, s, scope, "signal test", "fact", nil, nil, nil)
	if err := s.Memories().ApplyFeedback(ctx, scope, memID, "bogus-signal"); err == nil {
		t.Error("expected error for unknown signal")
	}
}

// --- RecordStore.GetMany conformance ----------------------------------------

func testRecordGetMany(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	var ids []string
	base := nowMs()
	for i := 0; i < 3; i++ {
		rec := store.Record{
			ID:         newID(),
			Role:       "user",
			Content:    "content",
			OccurredAt: base + int64(i),
			CreatedAt:  nowMs(),
		}
		ids = append(ids, rec.ID)
		if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// GetMany for all three IDs; order matches input order.
	got, err := s.Records().GetMany(ctx, scope, ids)
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d records want 3", len(got))
	}
	for i, rec := range got {
		if rec.ID != ids[i] {
			t.Errorf("record[%d].ID: got %q want %q", i, rec.ID, ids[i])
		}
	}
}

func testRecordGetManyMissingOmitted(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	rec := store.Record{
		ID: newID(), Role: "user", Content: "x",
		OccurredAt: nowMs(), CreatedAt: nowMs(),
	}
	if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Mix of present and missing IDs; missing are silently omitted.
	got, err := s.Records().GetMany(ctx, scope, []string{rec.ID, "missing-1", "missing-2"})
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d records want 1 (missing silently omitted)", len(got))
	}
}

func testRecordGetManyScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	rec := store.Record{
		ID: newID(), Role: "user", Content: "secret",
		OccurredAt: nowMs(), CreatedAt: nowMs(),
	}
	if err := s.Records().Append(ctx, scopeA, []store.Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Tenant B must not see tenant A's record via GetMany.
	got, err := s.Records().GetMany(ctx, scopeB, []string{rec.ID})
	if err != nil {
		t.Fatalf("GetMany: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cross-tenant isolation violated: GetMany returned %d records want 0", len(got))
	}
}
