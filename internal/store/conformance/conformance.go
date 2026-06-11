// Package conformance provides a driver-agnostic test suite for the store.Store
// interface. Both sqlitestore and pgstore run these tests to guarantee identical
// semantics (D-009, D-021).
//
// Usage:
//
//	func TestMyDriver(t *testing.T) {
//	    conformance.Run(t, func() (store.Store, func()) {
//	        s := openTestStore(t)
//	        return s, func() { s.Close(context.Background()) }
//	    })
//	}
package conformance

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/store/migrations"
	"github.com/oklog/ulid/v2"
)

// Factory returns a ready-to-use store and a cleanup function.
type Factory func() (store.Store, func())

// Run executes the full conformance suite against the provided factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("MigrateIdempotent", func(t *testing.T) { testMigrateIdempotent(t, factory) })
	t.Run("NewMethodGuardBranches", func(t *testing.T) { testNewMethodGuardBranches(t, factory) })
	t.Run("AppliedMigrationsListing", func(t *testing.T) { testAppliedMigrationsListing(t, factory) })
	t.Run("RecordAppendGet", func(t *testing.T) { testRecordAppendGet(t, factory) })
	t.Run("RecordAppendIdempotent", func(t *testing.T) { testRecordAppendIdempotent(t, factory) })
	t.Run("RecordListBySession", func(t *testing.T) { testRecordListBySession(t, factory) })
	t.Run("RecordListBySessionCursor", func(t *testing.T) { testRecordListBySessionCursor(t, factory) })
	t.Run("RecordListUnprocessedMarkProcessed", func(t *testing.T) { testRecordListUnprocessedMarkProcessed(t, factory) })
	t.Run("RecordScopeIsolation", func(t *testing.T) { testRecordScopeIsolation(t, factory) })
	t.Run("RecordGetNotFound", func(t *testing.T) { testRecordGetNotFound(t, factory) })
	t.Run("MemoryInsertGet", func(t *testing.T) { testMemoryInsertGet(t, factory) })
	t.Run("MemoryGetNotFound", func(t *testing.T) { testMemoryGetNotFound(t, factory) })
	t.Run("MemoryUpdate", func(t *testing.T) { testMemoryUpdate(t, factory) })
	t.Run("MemorySetStatus", func(t *testing.T) { testMemorySetStatus(t, factory) })
	t.Run("MemoryListByStatus", func(t *testing.T) { testMemoryListByStatus(t, factory) })
	t.Run("MemoryListByStatusCursor", func(t *testing.T) { testMemoryListByStatusCursor(t, factory) })
	t.Run("MemoryLinks", func(t *testing.T) { testMemoryLinks(t, factory) })
	t.Run("MemoryLinksEmpty", func(t *testing.T) { testMemoryLinksEmpty(t, factory) })
	t.Run("MemoryProvenance", func(t *testing.T) { testMemoryProvenance(t, factory) })
	t.Run("MemoryScopeIsolation", func(t *testing.T) { testMemoryScopeIsolation(t, factory) })
	t.Run("TopicUpsertGetListDelete", func(t *testing.T) { testTopicUpsertGetListDelete(t, factory) })
	t.Run("TopicScopeIsolation", func(t *testing.T) { testTopicScopeIsolation(t, factory) })
	t.Run("BufferAppendListDue", func(t *testing.T) { testBufferAppendListDue(t, factory) })
	t.Run("BufferFlushAtomicity", func(t *testing.T) { testBufferFlushAtomicity(t, factory) })
	t.Run("BufferFlushEmpty", func(t *testing.T) { testBufferFlushEmpty(t, factory) })
	t.Run("BufferScanAged", func(t *testing.T) { testBufferScanAged(t, factory) })
	t.Run("KeyringInsertLookupRevoke", func(t *testing.T) { testKeyringInsertLookupRevoke(t, factory) })
	t.Run("EventEmitList", func(t *testing.T) { testEventEmitList(t, factory) })
	t.Run("EventOrdering", func(t *testing.T) { testEventOrdering(t, factory) })
	t.Run("EventListCursor", func(t *testing.T) { testEventListCursor(t, factory) })
	t.Run("OpsDeadLetters", func(t *testing.T) { testOpsDeadLetters(t, factory) })
	t.Run("OpsDeadLetterAllStages", func(t *testing.T) { testOpsDeadLetterAllStages(t, factory) })
	t.Run("OpsJobMarker", func(t *testing.T) { testOpsJobMarker(t, factory) })
	t.Run("OpsAdvisoryLock", func(t *testing.T) { testOpsAdvisoryLock(t, factory) })
	// BranchStore
	t.Run("BranchCreateGet", func(t *testing.T) { testBranchCreateGet(t, factory) })
	t.Run("BranchSetStatus", func(t *testing.T) { testBranchSetStatus(t, factory) })
	t.Run("BranchListBySession", func(t *testing.T) { testBranchListBySession(t, factory) })
	t.Run("BranchScopeIsolation", func(t *testing.T) { testBranchScopeIsolation(t, factory) })
	t.Run("BranchGetNotFound", func(t *testing.T) { testBranchGetNotFound(t, factory) })
	// Keyring.List
	t.Run("KeyringList", func(t *testing.T) { testKeyringList(t, factory) })
	// S1 — empty-tenant guard (P3: store layer fails closed)
	t.Run("EmptyScopeRejected", func(t *testing.T) { testEmptyScopeRejected(t, factory) })
	// S2 — cross-user / cross-project / cross-session isolation
	t.Run("CrossUserIsolation", func(t *testing.T) { testCrossUserIsolation(t, factory) })
	t.Run("CrossProjectIsolation", func(t *testing.T) { testCrossProjectIsolation(t, factory) })
	t.Run("CrossSessionIsolation", func(t *testing.T) { testCrossSessionIsolation(t, factory) })
	// Q1 — composite cursor handles timestamp ties without dropping rows
	t.Run("CursorTimestampTieRecords", func(t *testing.T) { testCursorTimestampTieRecords(t, factory) })
	t.Run("CursorTimestampTieMemories", func(t *testing.T) { testCursorTimestampTieMemories(t, factory) })
	t.Run("CursorTimestampTieEvents", func(t *testing.T) { testCursorTimestampTieEvents(t, factory) })
	// O1 — concurrent job-marker atomicity
	t.Run("OpsJobMarkerConcurrent", func(t *testing.T) { testOpsJobMarkerConcurrent(t, factory) })
	// Phase 08 — new MemoryStore methods
	t.Run("MemoryGetByContentHash", func(t *testing.T) { testMemoryGetByContentHash(t, factory) })
	t.Run("MemoryGetByContentHashNotFound", func(t *testing.T) { testMemoryGetByContentHashNotFound(t, factory) })
	t.Run("MemoryFindNeighbors", func(t *testing.T) { testMemoryFindNeighbors(t, factory) })
	t.Run("MemoryFindNeighborsEmpty", func(t *testing.T) { testMemoryFindNeighborsEmpty(t, factory) })
	t.Run("MemoryIncrementCounter", func(t *testing.T) { testMemoryIncrementCounter(t, factory) })
	t.Run("MemoryIncrementCounterUnknown", func(t *testing.T) { testMemoryIncrementCounterUnknown(t, factory) })
	t.Run("MemoryCommitAdd", func(t *testing.T) { testMemoryCommitAdd(t, factory) })
	t.Run("MemoryCommitUpdate", func(t *testing.T) { testMemoryCommitUpdate(t, factory) })
	t.Run("MemoryCommitDiscard", func(t *testing.T) { testMemoryCommitDiscard(t, factory) })
	t.Run("MemoryCommitFaultHook", func(t *testing.T) { testMemoryCommitFaultHook(t, factory) })
	t.Run("MemoryCommitFaultHookUpdate", func(t *testing.T) { testMemoryCommitFaultHookUpdate(t, factory) })
	t.Run("MemoryCommitFaultHookSupersede", func(t *testing.T) { testMemoryCommitFaultHookSupersede(t, factory) })
	t.Run("MemoryCommitSupersede", func(t *testing.T) { testMemoryCommitSupersede(t, factory) })
	t.Run("MemoryCommitMerge", func(t *testing.T) { testMemoryCommitMerge(t, factory) })
	t.Run("MemoryCommitScopeRequired", func(t *testing.T) { testMemoryCommitScopeRequired(t, factory) })
	t.Run("MemoryContentHashScopeIsolation", func(t *testing.T) { testMemoryContentHashScopeIsolation(t, factory) })
	t.Run("MemoryFindNeighborsScopeIsolation", func(t *testing.T) { testMemoryFindNeighborsScopeIsolation(t, factory) })
	t.Run("MemoryFindNeighborsByKeyword", func(t *testing.T) { testMemoryFindNeighborsByKeyword(t, factory) })
	t.Run("MemoryCommitAddZeroTimes", func(t *testing.T) { testMemoryCommitAddZeroTimes(t, factory) })
	t.Run("MemoryCommitUpdateZeroTimes", func(t *testing.T) { testMemoryCommitUpdateZeroTimes(t, factory) })
	t.Run("MemoryCommitAddWithProvenance", func(t *testing.T) { testMemoryCommitAddWithProvenance(t, factory) })
	// m7 — TOCTOU: unique content-hash constraint + ErrDuplicateContent
	t.Run("MemoryCommitAddDuplicateContent", func(t *testing.T) { testMemoryCommitAddDuplicateContent(t, factory) })
	t.Run("MemoryCommitAddConcurrentDedup", func(t *testing.T) { testMemoryCommitAddConcurrentDedup(t, factory) })
	// m9 — conformance gaps
	t.Run("MemoryGetJunctions", func(t *testing.T) { testMemoryGetJunctions(t, factory) })
	t.Run("MemoryGetJunctionsEmpty", func(t *testing.T) { testMemoryGetJunctionsEmpty(t, factory) })
	t.Run("MemoryGetByContentHashCrossUser", func(t *testing.T) { testMemoryGetByContentHashCrossUser(t, factory) })
	t.Run("MemoryFindNeighborsCrossUser", func(t *testing.T) { testMemoryFindNeighborsCrossUser(t, factory) })
	// execCommit coverage — merge FaultHook + unknown action guard
	t.Run("MemoryCommitFaultHookMerge", func(t *testing.T) { testMemoryCommitFaultHookMerge(t, factory) })
	t.Run("MemoryCommitUnknownAction", func(t *testing.T) { testMemoryCommitUnknownAction(t, factory) })
	// Phase 09 — VectorStore conformance
	t.Run("VectorUpsertScan", func(t *testing.T) { testVectorUpsertScan(t, factory) })
	t.Run("VectorUpsertReplace", func(t *testing.T) { testVectorUpsertReplace(t, factory) })
	t.Run("VectorDelete", func(t *testing.T) { testVectorDelete(t, factory) })
	t.Run("VectorScopeIsolation", func(t *testing.T) { testVectorScopeIsolation(t, factory) })
	t.Run("VectorCrossUserIsolation", func(t *testing.T) { testVectorCrossUserIsolation(t, factory) })
	t.Run("VectorKindFilter", func(t *testing.T) { testVectorKindFilter(t, factory) })
	t.Run("VectorWindowFilter", func(t *testing.T) { testVectorWindowFilter(t, factory) })
	t.Run("VectorListWithoutVectors", func(t *testing.T) { testVectorListWithoutVectors(t, factory) })
	t.Run("VectorScopeRequired", func(t *testing.T) { testVectorScopeRequired(t, factory) })
	// Phase 09 — MemoryStore lexical + GetMany
	t.Run("LexicalSearch", func(t *testing.T) { testLexicalSearch(t, factory) })
	t.Run("LexicalSearchWindow", func(t *testing.T) { testLexicalSearchWindow(t, factory) })
	t.Run("LexicalSearchScopeIsolation", func(t *testing.T) { testLexicalSearchScopeIsolation(t, factory) })
	t.Run("QuerySearch", func(t *testing.T) { testQuerySearch(t, factory) })
	t.Run("QuerySearchScopeIsolation", func(t *testing.T) { testQuerySearchScopeIsolation(t, factory) })
	t.Run("MemoryGetMany", func(t *testing.T) { testMemoryGetMany(t, factory) })
	t.Run("MemoryGetManyEmpty", func(t *testing.T) { testMemoryGetManyEmpty(t, factory) })
	// Phase 10 — RecordStore.CountRecordsSince
	t.Run("RecordCountRecordsSince", func(t *testing.T) { testRecordCountRecordsSince(t, factory) })
	t.Run("RecordCountRecordsSinceScopeIsolation", func(t *testing.T) { testRecordCountRecordsSinceScopeIsolation(t, factory) })
	// Phase 11 — InjectionStore + MemoryStore.ApplyFeedback + RecordStore.GetMany
	t.Run("InjectionAppendGet", func(t *testing.T) { testInjectionAppendGet(t, factory) })
	t.Run("InjectionAppendIdempotent", func(t *testing.T) { testInjectionAppendIdempotent(t, factory) })
	t.Run("InjectionGetNotFound", func(t *testing.T) { testInjectionGetNotFound(t, factory) })
	t.Run("InjectionListByResponse", func(t *testing.T) { testInjectionListByResponse(t, factory) })
	t.Run("InjectionScopeIsolation", func(t *testing.T) { testInjectionScopeIsolation(t, factory) })
	t.Run("MarkWrongCitation", func(t *testing.T) { testMarkWrongCitation(t, factory) })
	t.Run("MarkWrongCitationNotFound", func(t *testing.T) { testMarkWrongCitationNotFound(t, factory) })
	t.Run("ApplyFeedback", func(t *testing.T) { testApplyFeedback(t, factory) })
	t.Run("ApplyFeedbackNoopMissing", func(t *testing.T) { testApplyFeedbackNoopMissing(t, factory) })
	t.Run("ApplyFeedbackUnknownSignal", func(t *testing.T) { testApplyFeedbackUnknownSignal(t, factory) })
	t.Run("RecordGetMany", func(t *testing.T) { testRecordGetMany(t, factory) })
	t.Run("RecordGetManyMissingOmitted", func(t *testing.T) { testRecordGetManyMissingOmitted(t, factory) })
	t.Run("RecordGetManyScopeIsolation", func(t *testing.T) { testRecordGetManyScopeIsolation(t, factory) })
}

// --- helpers ----------------------------------------------------------------

func newID() string { return ulid.Make().String() }

func nowMs() int64 { return time.Now().UnixMilli() }

func mustScope(tenant, project, user, session string) identity.Scope {
	return identity.Scope{
		Tenant:  tenant,
		Project: project,
		User:    user,
		Session: session,
	}
}

func tenantScope(tenant string) identity.Scope {
	return identity.Scope{Tenant: tenant}
}

// --- MigrateIdempotent ------------------------------------------------------

// testAppliedMigrationsListing asserts AppliedMigrations matches the embedded
// migration set after Migrate (drives `stowage migrate --status`). Migration
// names are identical across drivers by construction, so the sqlite list is
// the canonical expectation.
func testAppliedMigrationsListing(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	applied, err := s.AppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	known := migrations.Known("sqlite")
	if len(applied) != len(known) {
		t.Fatalf("applied %v != known %v", applied, known)
	}
	for i := range known {
		if applied[i] != known[i] {
			t.Errorf("migration %d: applied %q != known %q", i, applied[i], known[i])
		}
	}
}

// testNewMethodGuardBranches sweeps the guard/edge branches of the Phase 09
// methods on every driver: empty-scope rejection, empty/zero inputs, filters.
func testNewMethodGuardBranches(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("guard-branches-" + ulid.Make().String())
	zero := identity.Scope{}

	// Empty-scope rejection on every new method.
	if err := s.Vectors().Upsert(ctx, zero, store.StoredVector{MemoryID: "m1", Vec: []float32{1}}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("Vectors.Upsert zero scope: got %v", err)
	}
	if err := s.Vectors().Delete(ctx, zero, "m1"); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("Vectors.Delete zero scope: got %v", err)
	}
	if _, err := s.Vectors().Scan(ctx, zero, nil, store.Window{}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("Vectors.Scan zero scope: got %v", err)
	}
	if _, err := s.Memories().LexicalSearch(ctx, zero, "x", 5, store.Window{}, nil); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("LexicalSearch zero scope: got %v", err)
	}
	if _, err := s.Memories().QuerySearch(ctx, zero, "x", 5, store.Window{}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("QuerySearch zero scope: got %v", err)
	}
	if _, err := s.Memories().GetMany(ctx, zero, []string{"a"}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("GetMany zero scope: got %v", err)
	}

	// Empty/zero inputs return empty, not errors.
	if hits, err := s.Memories().LexicalSearch(ctx, scope, "", 5, store.Window{}, nil); err != nil || len(hits) != 0 {
		t.Errorf("LexicalSearch empty query: %v %v", hits, err)
	}
	if hits, err := s.Memories().LexicalSearch(ctx, scope, "x", 0, store.Window{}, nil); err != nil || len(hits) != 0 {
		t.Errorf("LexicalSearch k=0: %v %v", hits, err)
	}
	if hits, err := s.Memories().QuerySearch(ctx, scope, "", 5, store.Window{}); err != nil || len(hits) != 0 {
		t.Errorf("QuerySearch empty query: %v %v", hits, err)
	}
	if got, err := s.Memories().GetMany(ctx, scope, nil); err != nil || len(got) != 0 {
		t.Errorf("GetMany empty ids: %v %v", got, err)
	}

	// Filtered variants exercise the kinds/window branches.
	if _, err := s.Memories().LexicalSearch(ctx, scope, "anything", 5, store.Window{From: 1, Until: 2}, []string{"fact"}); err != nil {
		t.Errorf("LexicalSearch with filters: %v", err)
	}
	if _, err := s.Vectors().Scan(ctx, scope, []string{"fact"}, store.Window{From: 1, Until: 2}); err != nil {
		t.Errorf("Vectors.Scan with filters: %v", err)
	}

	// Delete of a nonexistent vector is a no-op, not an error.
	if err := s.Vectors().Delete(ctx, scope, "never-existed"); err != nil {
		t.Errorf("Vectors.Delete missing: %v", err)
	}

	// Fully sub-scoped round-trip exercises every scope-column branch.
	full := identity.Scope{Tenant: scope.Tenant, Project: "p1", User: "u1", Session: "s1"}
	backing := store.Memory{
		ID: newID(), Kind: "fact", Content: "guard-branch backing memory",
		Status: "active", Importance: 3, Confidence: 0.9,
		TrustSource: "llm_extracted", Stability: 1.0,
	}
	if err := s.Memories().Insert(ctx, full, backing); err != nil {
		t.Fatalf("insert backing memory: %v", err)
	}
	sv := store.StoredVector{
		MemoryID: backing.ID, TenantID: full.Tenant,
		ProjectID: full.Project, UserID: full.User, SessionID: full.Session,
		Vec: []float32{0.6, 0.8},
	}
	if err := s.Vectors().Upsert(ctx, full, sv); err != nil {
		t.Fatalf("Upsert full scope: %v", err)
	}
	// Upsert-replace branch.
	sv.Vec = []float32{1, 0}
	if err := s.Vectors().Upsert(ctx, full, sv); err != nil {
		t.Fatalf("Upsert replace: %v", err)
	}
	got, err := s.Vectors().Scan(ctx, full, nil, store.Window{})
	if err != nil || len(got) != 1 || got[0].Vec[0] != 1 {
		t.Fatalf("Scan full scope after replace: %v %v", got, err)
	}
	// Sibling session sees nothing (session branch of the WHERE).
	sib := identity.Scope{Tenant: full.Tenant, Project: "p1", User: "u1", Session: "s2"}
	if got, err := s.Vectors().Scan(ctx, sib, nil, store.Window{}); err != nil || len(got) != 0 {
		t.Errorf("sibling session scan: %v %v", got, err)
	}
	if err := s.Vectors().Delete(ctx, full, sv.MemoryID); err != nil {
		t.Errorf("Delete full scope: %v", err)
	}
	// GetMany with a mix of present and missing ids (uses an existing memory).
	if got, err := s.Memories().GetMany(ctx, full, []string{"missing-1", "missing-2"}); err != nil || len(got) != 0 {
		t.Errorf("GetMany all-missing: %v %v", got, err)
	}
	if hits, err := s.Memories().QuerySearch(ctx, scope, "x", 0, store.Window{}); err != nil || len(hits) != 0 {
		t.Errorf("QuerySearch k=0: %v %v", hits, err)
	}
}

func testMigrateIdempotent(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate (idempotent): %v", err)
	}
}

// --- RecordStore ------------------------------------------------------------

func testRecordAppendGet(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	rec := store.Record{
		ID:            newID(),
		Role:          "user",
		Content:       "hello world",
		OccurredAt:    nowMs(),
		CreatedAt:     nowMs(),
		TokenEstimate: 5,
	}
	if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Records().Get(ctx, scope, rec.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != rec.Content {
		t.Errorf("content: got %q want %q", got.Content, rec.Content)
	}
}

func testRecordAppendIdempotent(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	rec := store.Record{
		ID: newID(), Role: "user", Content: "original",
		OccurredAt: nowMs(), CreatedAt: nowMs(),
	}
	if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	// Second append with same ID must be silently ignored.
	rec2 := rec
	rec2.Content = "modified"
	if err := s.Records().Append(ctx, scope, []store.Record{rec2}); err != nil {
		t.Fatalf("second Append: %v", err)
	}
	got, _ := s.Records().Get(ctx, scope, rec.ID)
	if got.Content != "original" {
		t.Errorf("idempotency broken: got %q want %q", got.Content, "original")
	}
}

func testRecordListBySession(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := mustScope("t-"+newID(), "proj", "user", "sess")

	var recs []store.Record
	base := nowMs()
	for i := 0; i < 5; i++ {
		recs = append(recs, store.Record{
			ID: newID(), Role: "user", Content: fmt.Sprintf("msg%d", i),
			OccurredAt: base + int64(i), CreatedAt: nowMs(),
		})
	}
	if err := s.Records().Append(ctx, scope, recs); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _, err := s.Records().ListBySession(ctx, scope, "sess", "", 10, "")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("got %d records want 5", len(got))
	}
}

func testRecordListUnprocessedMarkProcessed(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	past := time.Now().Add(-time.Hour).UnixMilli()
	rec := store.Record{
		ID: newID(), Role: "user", Content: "unprocessed",
		OccurredAt: past, CreatedAt: past,
	}
	if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	unproc, err := s.Records().ListUnprocessed(ctx, nowMs(), 10)
	if err != nil {
		t.Fatalf("ListUnprocessed: %v", err)
	}
	found := false
	for _, r := range unproc {
		if r.ID == rec.ID {
			found = true
		}
	}
	if !found {
		t.Error("expected unprocessed record not found")
	}
	if err := s.Records().MarkProcessed(ctx, []string{rec.ID}); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	unproc2, _ := s.Records().ListUnprocessed(ctx, nowMs(), 10)
	for _, r := range unproc2 {
		if r.ID == rec.ID {
			t.Error("record still unprocessed after MarkProcessed")
		}
	}
}

func testRecordScopeIsolation(t *testing.T, factory Factory) {
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
	// Tenant B must not see tenant A's record.
	if _, err := s.Records().Get(ctx, scopeB, rec.ID); err == nil {
		t.Error("cross-tenant isolation violated: Get returned no error")
	}
	got, _, _ := s.Records().ListBySession(ctx, scopeB, "", "", 10, "")
	for _, r := range got {
		if r.ID == rec.ID {
			t.Error("cross-tenant isolation violated: record visible in wrong tenant")
		}
	}
}

// --- MemoryStore ------------------------------------------------------------

func testMemoryInsertGet(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "sky is blue",
		Status: "active", Importance: 4, Confidence: 0.9,
		TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Memories().Get(ctx, scope, mem.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != mem.Content {
		t.Errorf("content: got %q want %q", got.Content, mem.Content)
	}
	if got.Confidence != mem.Confidence {
		t.Errorf("confidence: got %v want %v", got.Confidence, mem.Confidence)
	}
}

func testMemoryUpdate(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "original",
		Status: "active", Importance: 3, Confidence: 0.5,
		TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	mem.Content = "updated"
	mem.Confidence = 0.8
	mem.UpdatedAt = nowMs()
	if err := s.Memories().Update(ctx, scope, mem); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := s.Memories().Get(ctx, scope, mem.ID)
	if got.Content != "updated" {
		t.Errorf("content after update: got %q want %q", got.Content, "updated")
	}
}

func testMemorySetStatus(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "test",
		Status: "active", Confidence: 0.5,
		TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Memories().SetStatus(ctx, scope, mem.ID, "superseded", nowMs()); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, _ := s.Memories().Get(ctx, scope, mem.ID)
	if got.Status != "superseded" {
		t.Errorf("status: got %q want superseded", got.Status)
	}
}

func testMemoryListByStatus(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	for i := 0; i < 3; i++ {
		mem := store.Memory{
			ID: newID(), Kind: "fact", Content: fmt.Sprintf("fact%d", i),
			Status: "active", Confidence: 0.5,
			TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: nowMs() + int64(i), UpdatedAt: nowMs(),
		}
		if err := s.Memories().Insert(ctx, scope, mem); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	got, _, err := s.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d want 3", len(got))
	}
}

func testMemoryLinks(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	m1 := store.Memory{
		ID: newID(), Kind: "fact", Content: "m1",
		Status: "active", Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	m2 := store.Memory{
		ID: newID(), Kind: "fact", Content: "m2",
		Status: "active", Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, m1); err != nil {
		t.Fatal(err)
	}
	if err := s.Memories().Insert(ctx, scope, m2); err != nil {
		t.Fatal(err)
	}

	link := store.Link{
		ID: newID(), TenantID: scope.Tenant,
		FromMemory: m1.ID, ToMemory: m2.ID,
		Type: "supports", Source: "explicit", Confidence: 1.0,
		CreatedAt: nowMs(),
	}
	if err := s.Memories().InsertLinks(ctx, scope, []store.Link{link}); err != nil {
		t.Fatalf("InsertLinks: %v", err)
	}
	links, err := s.Memories().ListLinks(ctx, scope, m1.ID, "")
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("got %d links want 1", len(links))
	}
}

func testMemoryProvenance(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	rec := store.Record{
		ID: newID(), Role: "user", Content: "test",
		OccurredAt: nowMs(), CreatedAt: nowMs(),
	}
	if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatal(err)
	}
	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "derived",
		Status: "active", Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatal(err)
	}
	prov := store.Provenance{
		ID: newID(), MemoryID: mem.ID, RecordID: rec.ID,
		SpanStart: 0, SpanEnd: 11, TenantID: scope.Tenant,
		CreatedAt: nowMs(),
	}
	if err := s.Memories().AddProvenance(ctx, scope, []store.Provenance{prov}); err != nil {
		t.Fatalf("AddProvenance: %v", err)
	}
}

func testMemoryScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "private",
		Status: "active", Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scopeA, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := s.Memories().Get(ctx, scopeB, mem.ID); err == nil {
		t.Error("cross-tenant isolation violated: Get returned no error")
	}
	got, _, _ := s.Memories().ListByStatus(ctx, scopeB, "active", 10, "")
	for _, m := range got {
		if m.ID == mem.ID {
			t.Error("cross-tenant isolation violated: memory visible in wrong tenant")
		}
	}
}

// --- TopicStore -------------------------------------------------------------

func testTopicUpsertGetListDelete(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	topic := store.Topic{
		ID: newID(), Key: "goals", Description: "user goals",
		Status: "active", Pack: "", CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Topics().Upsert(ctx, scope, topic); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := s.Topics().Get(ctx, scope, "goals")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Description != "user goals" {
		t.Errorf("description: got %q want %q", got.Description, "user goals")
	}

	// Upsert update.
	topic.Description = "updated goals"
	topic.UpdatedAt = nowMs()
	if err := s.Topics().Upsert(ctx, scope, topic); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}
	got2, _ := s.Topics().Get(ctx, scope, "goals")
	if got2.Description != "updated goals" {
		t.Errorf("upsert update: got %q want updated goals", got2.Description)
	}

	list, err := s.Topics().List(ctx, scope)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("list len: got %d want 1", len(list))
	}

	if err := s.Topics().Delete(ctx, scope, "goals"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Topics().Get(ctx, scope, "goals"); err == nil {
		t.Error("expected ErrNotFound after delete")
	}

	// Driver parity: deleting a missing topic is ErrNotFound on every driver.
	if err := s.Topics().Delete(ctx, scope, "never-existed"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete missing topic: got %v want ErrNotFound", err)
	}
	// Deleting an already-deleted topic is also ErrNotFound (soft-delete is terminal).
	if err := s.Topics().Delete(ctx, scope, "goals"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete deleted topic: got %v want ErrNotFound", err)
	}
}

func testTopicScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	topic := store.Topic{
		ID: newID(), Key: "private-topic", Description: "secret",
		Status: "active", CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Topics().Upsert(ctx, scopeA, topic); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := s.Topics().Get(ctx, scopeB, "private-topic"); err == nil {
		t.Error("cross-tenant isolation violated")
	}
}

// --- BufferStore ------------------------------------------------------------

func testBufferAppendListDue(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	bufKey := "test-buffer"
	for i := 0; i < 3; i++ {
		item := store.BufferItem{
			ID: newID(), BufferKey: bufKey, TokenEstimate: 10,
			CreatedAt: nowMs() + int64(i),
		}
		if err := s.Buffers().AppendItem(ctx, scope, item); err != nil {
			t.Fatalf("AppendItem %d: %v", i, err)
		}
	}
	due, err := s.Buffers().ListDue(ctx, scope, bufKey, 10)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	if len(due) != 3 {
		t.Errorf("got %d due items want 3", len(due))
	}
}

func testBufferFlushAtomicity(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	bufKey := "flush-test-" + newID()
	const numItems = 20
	for i := 0; i < numItems; i++ {
		item := store.BufferItem{
			ID: newID(), BufferKey: bufKey, TokenEstimate: 1,
			CreatedAt: nowMs() + int64(i),
		}
		if err := s.Buffers().AppendItem(ctx, scope, item); err != nil {
			t.Fatalf("AppendItem %d: %v", i, err)
		}
	}

	// Concurrent flushers — only one should win items.
	const numFlushers = 4
	results := make([][]store.BufferItem, numFlushers)
	var wg sync.WaitGroup
	for i := 0; i < numFlushers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := s.Buffers().Flush(ctx, scope, bufKey)
			if err == nil {
				results[i] = items
			}
		}()
	}
	wg.Wait()

	// Total items flushed across all goroutines must equal numItems (exactly once).
	total := 0
	for _, r := range results {
		total += len(r)
	}
	if total != numItems {
		t.Errorf("flush atomicity: total flushed %d want %d", total, numItems)
	}

	// A second flush should return nothing.
	again, _ := s.Buffers().Flush(ctx, scope, bufKey)
	if len(again) != 0 {
		t.Errorf("second flush returned %d items want 0", len(again))
	}
}

// --- Keyring ----------------------------------------------------------------

func testKeyringInsertLookupRevoke(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()

	key, _, err := auth.Generate("tenant-kr-"+newID(), auth.RoleAgent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	kr := s.Keys()
	if err := kr.Insert(key); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := kr.Lookup(key.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.TenantID != key.TenantID {
		t.Errorf("TenantID: got %q want %q", got.TenantID, key.TenantID)
	}

	// ErrKeyNotFound for unknown ID.
	if _, err := kr.Lookup("no-such-key"); err == nil {
		t.Error("expected ErrKeyNotFound")
	}

	// Revoke.
	if err := kr.Revoke(key.ID, time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got2, _ := kr.Lookup(key.ID)
	if got2.RevokedAt == nil {
		t.Error("RevokedAt should be set after revoke")
	}
}

// --- EventStore -------------------------------------------------------------

func testEventEmitList(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	ev := store.Event{
		ID: newID(), Type: "memory.created", SubjectID: newID(),
		Payload: `{"kind":"fact"}`, CreatedAt: nowMs(),
	}
	if err := s.Events().Emit(ctx, scope, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	events, _, err := s.Events().List(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("got %d events want 1", len(events))
	}
	if events[0].Type != "memory.created" {
		t.Errorf("type: got %q want memory.created", events[0].Type)
	}
}

func testEventOrdering(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	base := nowMs()
	for i := 0; i < 5; i++ {
		ev := store.Event{
			ID: newID(), Type: fmt.Sprintf("event.%d", i),
			CreatedAt: base + int64(i),
		}
		if err := s.Events().Emit(ctx, scope, ev); err != nil {
			t.Fatalf("Emit %d: %v", i, err)
		}
	}
	events, _, err := s.Events().List(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].CreatedAt < events[i-1].CreatedAt {
			t.Errorf("events not ordered: events[%d].CreatedAt=%d < events[%d].CreatedAt=%d",
				i, events[i].CreatedAt, i-1, events[i-1].CreatedAt)
		}
	}
}

// --- OpsStore ---------------------------------------------------------------

func testOpsDeadLetters(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	dl := store.DeadLetter{
		ID: newID(), Stage: "extract", ItemID: newID(),
		Error: "timeout", Attempts: 1, CreatedAt: nowMs(),
	}
	if err := s.Ops().PutDeadLetter(ctx, dl); err != nil {
		t.Fatalf("PutDeadLetter: %v", err)
	}
	letters, err := s.Ops().ListDeadLetters(ctx, "extract", 10)
	if err != nil {
		t.Fatalf("ListDeadLetters: %v", err)
	}
	if len(letters) != 1 {
		t.Errorf("got %d dead letters want 1", len(letters))
	}
	if err := s.Ops().ResolveDeadLetter(ctx, dl.ID, nowMs()); err != nil {
		t.Fatalf("ResolveDeadLetter: %v", err)
	}
	letters2, _ := s.Ops().ListDeadLetters(ctx, "extract", 10)
	if len(letters2) != 0 {
		t.Errorf("got %d dead letters after resolve want 0", len(letters2))
	}
}

func testOpsJobMarker(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	job, marker := "sweep", "2026-06-11"
	set1, err := s.Ops().CheckAndSetJobMarker(ctx, job, marker, nowMs())
	if err != nil {
		t.Fatalf("CheckAndSetJobMarker: %v", err)
	}
	if !set1 {
		t.Error("first call should return true (newly set)")
	}
	set2, err := s.Ops().CheckAndSetJobMarker(ctx, job, marker, nowMs())
	if err != nil {
		t.Fatalf("CheckAndSetJobMarker second: %v", err)
	}
	if set2 {
		t.Error("second call should return false (already set)")
	}
}

func testOpsAdvisoryLock(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	release, err := s.Ops().AdvisoryLock(ctx, 12345)
	if err != nil {
		t.Fatalf("AdvisoryLock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// --- Additional coverage tests ----------------------------------------------

func testRecordListBySessionCursor(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := mustScope("t-"+newID(), "proj", "user", "sess2")

	base := nowMs()
	var recs []store.Record
	for i := 0; i < 6; i++ {
		recs = append(recs, store.Record{
			ID: newID(), Role: "user", Content: fmt.Sprintf("msg%d", i),
			OccurredAt: base + int64(i), CreatedAt: nowMs(),
		})
	}
	if err := s.Records().Append(ctx, scope, recs); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// First page: limit 3.
	page1, cursor, err := s.Records().ListBySession(ctx, scope, "sess2", "", 3, "")
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 3 {
		t.Errorf("page1 len: got %d want 3", len(page1))
	}
	if cursor == "" {
		t.Error("expected non-empty cursor after first page")
	}

	// Q1: cursor encodes last item of page1 (recs[2]); page2 filter is
	// (occurred_at, id) > cursor → returns recs[3], recs[4], recs[5] = 3 items.
	page2, _, err := s.Records().ListBySession(ctx, scope, "sess2", "", 3, cursor)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 3 {
		t.Errorf("page2 len: got %d want 3 (cursor is last of page1, no rows dropped)", len(page2))
	}
}

func testRecordGetNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	if _, err := s.Records().Get(ctx, scope, "no-such-id"); err == nil {
		t.Error("expected ErrNotFound")
	}
}

func testMemoryGetNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	if _, err := s.Memories().Get(ctx, scope, "no-such-id"); err == nil {
		t.Error("expected ErrNotFound")
	}
}

func testMemoryListByStatusCursor(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	base := nowMs()
	for i := 0; i < 6; i++ {
		mem := store.Memory{
			ID: newID(), Kind: "fact", Content: fmt.Sprintf("fact%d", i),
			Status: "active", Confidence: 0.5,
			TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: base + int64(i), UpdatedAt: base + int64(i),
		}
		if err := s.Memories().Insert(ctx, scope, mem); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	page1, cursor, err := s.Memories().ListByStatus(ctx, scope, "active", 3, "")
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 3 {
		t.Errorf("page1 len: got %d want 3", len(page1))
	}
	if cursor == "" {
		t.Error("expected cursor after first page")
	}

	// Q1: cursor is last item of page1 (mem[2]); page2 filter gives mem[3..5] = 3 items.
	page2, _, err := s.Memories().ListByStatus(ctx, scope, "active", 3, cursor)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 3 {
		t.Errorf("page2 len: got %d want 3 (cursor is last of page1, no rows dropped)", len(page2))
	}
}

func testMemoryLinksEmpty(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// ListLinks on empty store should return empty, not error.
	links, err := s.Memories().ListLinks(ctx, scope, "", "")
	if err != nil {
		t.Fatalf("ListLinks empty: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("expected 0 links, got %d", len(links))
	}
}

func testBufferFlushEmpty(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Flush on an empty buffer should return empty, not error.
	items, err := s.Buffers().Flush(ctx, scope, "empty-buffer")
	if err != nil {
		t.Fatalf("Flush empty: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func testBufferScanAged(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert items with varying timestamps.
	past := nowMs() - 10_000   // 10 s ago — aged
	recent := nowMs() + 60_000 // 60 s in the future — not aged

	for i := 0; i < 3; i++ {
		item := store.BufferItem{
			ID:            newID(),
			BufferKey:     "aged-buf",
			TokenEstimate: 5,
			CreatedAt:     past + int64(i),
		}
		if err := s.Buffers().AppendItem(ctx, scope, item); err != nil {
			t.Fatalf("AppendItem aged %d: %v", i, err)
		}
	}
	// One recent item that should NOT appear in ScanAged.
	recentItem := store.BufferItem{
		ID:            newID(),
		BufferKey:     "aged-buf",
		TokenEstimate: 5,
		CreatedAt:     recent,
	}
	if err := s.Buffers().AppendItem(ctx, scope, recentItem); err != nil {
		t.Fatalf("AppendItem recent: %v", err)
	}

	// ScanAged with threshold = now should return only the 3 aged items.
	aged, err := s.Buffers().ScanAged(ctx, nowMs(), 100)
	if err != nil {
		t.Fatalf("ScanAged: %v", err)
	}
	count := 0
	for _, it := range aged {
		if it.BufferKey == "aged-buf" && it.TenantID == scope.Tenant {
			count++
		}
	}
	if count != 3 {
		t.Errorf("ScanAged: got %d aged items want 3 (recent item must be excluded)", count)
	}

	// After flushing, ScanAged must not return flushed items.
	if _, err := s.Buffers().Flush(ctx, scope, "aged-buf"); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	afterFlush, err := s.Buffers().ScanAged(ctx, nowMs(), 100)
	if err != nil {
		t.Fatalf("ScanAged after flush: %v", err)
	}
	for _, it := range afterFlush {
		if it.TenantID == scope.Tenant && it.FlushedAt != 0 {
			t.Errorf("ScanAged after flush: got flushed item %q", it.ID)
		}
	}
}

func testEventListCursor(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	base := nowMs()
	for i := 0; i < 6; i++ {
		ev := store.Event{
			ID: newID(), Type: fmt.Sprintf("ev.%d", i),
			CreatedAt: base + int64(i),
		}
		if err := s.Events().Emit(ctx, scope, ev); err != nil {
			t.Fatalf("Emit %d: %v", i, err)
		}
	}

	page1, cursor, err := s.Events().List(ctx, scope, 3, "")
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 3 {
		t.Errorf("page1 len: got %d want 3", len(page1))
	}
	if cursor == "" {
		t.Error("expected cursor after first page")
	}

	// Q1: cursor is last item of page1 (ev[2]); page2 filter gives ev[3..5] = 3 items.
	page2, _, err := s.Events().List(ctx, scope, 3, cursor)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 3 {
		t.Errorf("page2 len: got %d want 3 (cursor is last of page1, no rows dropped)", len(page2))
	}
}

func testOpsDeadLetterAllStages(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	// Add dead letters for two stages.
	stages := []string{"extract", "reconcile"}
	for _, stage := range stages {
		dl := store.DeadLetter{
			ID: newID(), Stage: stage, ItemID: newID(),
			Error: "test error", Attempts: 1, CreatedAt: nowMs(),
		}
		if err := s.Ops().PutDeadLetter(ctx, dl); err != nil {
			t.Fatalf("PutDeadLetter(%s): %v", stage, err)
		}
	}

	// List all (empty stage string).
	all, err := s.Ops().ListDeadLetters(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListDeadLetters all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("got %d dead letters want 2", len(all))
	}
}

// =============================================================================
// S1 — empty-tenant guard (store layer fails closed, P3)
// =============================================================================

// testEmptyScopeRejected asserts that every scoped read AND write method
// returns store.ErrScopeRequired when called with an empty Tenant.
func testEmptyScopeRejected(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	zero := identity.Scope{} // empty tenant

	assertScopeRequired := func(t *testing.T, label string, err error) {
		t.Helper()
		if !errors.Is(err, store.ErrScopeRequired) {
			t.Errorf("%s: got %v want store.ErrScopeRequired", label, err)
		}
	}

	// RecordStore
	assertScopeRequired(t, "Records.Append",
		s.Records().Append(ctx, zero, []store.Record{{ID: newID(), Role: "user", Content: "x", OccurredAt: nowMs(), CreatedAt: nowMs()}}))
	_, err := s.Records().Get(ctx, zero, "any")
	assertScopeRequired(t, "Records.Get", err)
	_, _, err = s.Records().ListBySession(ctx, zero, "", "", 1, "")
	assertScopeRequired(t, "Records.ListBySession", err)

	// MemoryStore
	assertScopeRequired(t, "Memories.Insert",
		s.Memories().Insert(ctx, zero, store.Memory{ID: newID(), Kind: "fact", Content: "x", Status: "active", TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs()}))
	_, err = s.Memories().Get(ctx, zero, "any")
	assertScopeRequired(t, "Memories.Get", err)
	_, _, err = s.Memories().ListByStatus(ctx, zero, "active", 1, "")
	assertScopeRequired(t, "Memories.ListByStatus", err)
	assertScopeRequired(t, "Memories.InsertLinks",
		s.Memories().InsertLinks(ctx, zero, []store.Link{{ID: newID(), FromMemory: "x", ToMemory: "y", Type: "supports", Source: "explicit", Confidence: 1.0, CreatedAt: nowMs()}}))
	_, err = s.Memories().ListLinks(ctx, zero, "", "")
	assertScopeRequired(t, "Memories.ListLinks", err)
	assertScopeRequired(t, "Memories.AddProvenance",
		s.Memories().AddProvenance(ctx, zero, []store.Provenance{{ID: newID(), MemoryID: "m", RecordID: "r", CreatedAt: nowMs()}}))

	// Phase 08 MemoryStore methods (m9: add to empty-scope sweep).
	_, err = s.Memories().GetByContentHash(ctx, zero, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	assertScopeRequired(t, "Memories.GetByContentHash", err)
	_, err = s.Memories().FindNeighbors(ctx, zero, store.NeighborQuery{Entities: []string{"e"}})
	assertScopeRequired(t, "Memories.FindNeighbors", err)
	assertScopeRequired(t, "Memories.IncrementCounter",
		s.Memories().IncrementCounter(ctx, zero, "any-id", "match"))
	_, err = s.Memories().GetJunctions(ctx, zero, "any-id")
	assertScopeRequired(t, "Memories.GetJunctions", err)

	// TopicStore
	assertScopeRequired(t, "Topics.Upsert",
		s.Topics().Upsert(ctx, zero, store.Topic{ID: newID(), Key: "k", Status: "active", CreatedAt: nowMs(), UpdatedAt: nowMs()}))
	_, err = s.Topics().Get(ctx, zero, "k")
	assertScopeRequired(t, "Topics.Get", err)
	_, err = s.Topics().List(ctx, zero)
	assertScopeRequired(t, "Topics.List", err)
	assertScopeRequired(t, "Topics.Delete", s.Topics().Delete(ctx, zero, "k"))

	// BufferStore
	assertScopeRequired(t, "Buffers.AppendItem",
		s.Buffers().AppendItem(ctx, zero, store.BufferItem{ID: newID(), BufferKey: "b", CreatedAt: nowMs()}))
	_, err = s.Buffers().ListDue(ctx, zero, "b", 1)
	assertScopeRequired(t, "Buffers.ListDue", err)
	_, err = s.Buffers().Flush(ctx, zero, "b")
	assertScopeRequired(t, "Buffers.Flush", err)

	// EventStore
	assertScopeRequired(t, "Events.Emit",
		s.Events().Emit(ctx, zero, store.Event{ID: newID(), Type: "t", CreatedAt: nowMs()}))
	_, _, err = s.Events().List(ctx, zero, 1, "")
	assertScopeRequired(t, "Events.List", err)

	// BranchStore
	assertScopeRequired(t, "Branches.Create",
		s.Branches().Create(ctx, zero, store.Branch{ID: newID(), SessionID: "s", Status: "open", CreatedAt: nowMs(), UpdatedAt: nowMs()}))
	_, err = s.Branches().Get(ctx, zero, "any")
	assertScopeRequired(t, "Branches.Get", err)
	assertScopeRequired(t, "Branches.SetStatus",
		s.Branches().SetStatus(ctx, zero, "any", "merged", nowMs()))
	_, err = s.Branches().ListBySession(ctx, zero, "s")
	assertScopeRequired(t, "Branches.ListBySession", err)

	// Phase 11 — InjectionStore empty-scope rejection
	assertScopeRequired(t, "Injections.Append",
		s.Injections().Append(ctx, zero, []store.Injection{{ID: newID(), ResponseID: newID(), MemoryID: newID(), CreatedAt: nowMs()}}))
	_, err = s.Injections().Get(ctx, zero, "any")
	assertScopeRequired(t, "Injections.Get", err)
	_, err = s.Injections().ListByResponse(ctx, zero, "any")
	assertScopeRequired(t, "Injections.ListByResponse", err)
	assertScopeRequired(t, "Injections.MarkWrongCitation",
		s.Injections().MarkWrongCitation(ctx, zero, "any"))

	// Phase 11 — MemoryStore.ApplyFeedback + Records.GetMany empty-scope rejection
	assertScopeRequired(t, "Memories.ApplyFeedback",
		s.Memories().ApplyFeedback(ctx, zero, "any", "use"))
	_, err = s.Records().GetMany(ctx, zero, []string{"a"})
	assertScopeRequired(t, "Records.GetMany", err)
}

// =============================================================================
// S2 — cross-user / cross-project / cross-session isolation (same tenant)
// =============================================================================

// testCrossUserIsolation verifies that narrowing the scope to a specific user
// hides data belonging to a different user in the same tenant.
func testCrossUserIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "t-" + newID()
	scopeA := identity.Scope{Tenant: tenant, User: "user-A"}
	scopeB := identity.Scope{Tenant: tenant, User: "user-B"}

	base := nowMs()

	// Insert a record for user A.
	rec := store.Record{ID: newID(), Role: "user", Content: "user-A secret",
		OccurredAt: base, CreatedAt: base}
	if err := s.Records().Append(ctx, scopeA, []store.Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// User B must not see user A's record.
	if _, err := s.Records().Get(ctx, scopeB, rec.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Records.Get cross-user: got err=%v want ErrNotFound", err)
	}
	list, _, _ := s.Records().ListBySession(ctx, scopeB, "", "", 10, "")
	for _, r := range list {
		if r.ID == rec.ID {
			t.Error("Records.ListBySession cross-user: saw user-A record in user-B list")
		}
	}

	// Insert a memory for user A.
	mem := store.Memory{ID: newID(), Kind: "fact", Content: "user-A memory",
		Status: "active", TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: base, UpdatedAt: base}
	if err := s.Memories().Insert(ctx, scopeA, mem); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}
	if _, err := s.Memories().Get(ctx, scopeB, mem.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Memories.Get cross-user: got err=%v want ErrNotFound", err)
	}
	mems, _, _ := s.Memories().ListByStatus(ctx, scopeB, "active", 10, "")
	for _, m := range mems {
		if m.ID == mem.ID {
			t.Error("Memories.ListByStatus cross-user: saw user-A memory in user-B list")
		}
	}

	// Insert a topic for user A.
	topic := store.Topic{ID: newID(), Key: "ua-topic", Description: "secret",
		Status: "active", CreatedAt: base, UpdatedAt: base}
	if err := s.Topics().Upsert(ctx, scopeA, topic); err != nil {
		t.Fatalf("Upsert topic: %v", err)
	}
	if _, err := s.Topics().Get(ctx, scopeB, "ua-topic"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Topics.Get cross-user: got err=%v want ErrNotFound", err)
	}
	topics, _ := s.Topics().List(ctx, scopeB)
	for _, tp := range topics {
		if tp.ID == topic.ID {
			t.Error("Topics.List cross-user: saw user-A topic in user-B list")
		}
	}

	// Insert a buffer item for user A.
	bItem := store.BufferItem{ID: newID(), BufferKey: "ua-buf", TokenEstimate: 1, CreatedAt: base}
	if err := s.Buffers().AppendItem(ctx, scopeA, bItem); err != nil {
		t.Fatalf("AppendItem: %v", err)
	}
	due, _ := s.Buffers().ListDue(ctx, scopeB, "ua-buf", 10)
	for _, it := range due {
		if it.ID == bItem.ID {
			t.Error("Buffers.ListDue cross-user: saw user-A item in user-B list")
		}
	}

	// Links are tenant-scoped only (no user/project/session columns in the links
	// table). User B in the same tenant CAN see links inserted by user A — this
	// is intentional; see the ListLinks doc comment.
}

// testCrossProjectIsolation verifies that a narrower project scope hides data
// from a different project in the same tenant.
func testCrossProjectIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "t-" + newID()
	scopeA := identity.Scope{Tenant: tenant, Project: "proj-A"}
	scopeB := identity.Scope{Tenant: tenant, Project: "proj-B"}

	base := nowMs()

	rec := store.Record{ID: newID(), Role: "user", Content: "proj-A secret",
		OccurredAt: base, CreatedAt: base}
	if err := s.Records().Append(ctx, scopeA, []store.Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := s.Records().Get(ctx, scopeB, rec.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Records.Get cross-project: got err=%v want ErrNotFound", err)
	}
	list, _, _ := s.Records().ListBySession(ctx, scopeB, "", "", 10, "")
	for _, r := range list {
		if r.ID == rec.ID {
			t.Error("Records.ListBySession cross-project: saw proj-A record in proj-B list")
		}
	}

	mem := store.Memory{ID: newID(), Kind: "fact", Content: "proj-A memory",
		Status: "active", TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: base, UpdatedAt: base}
	if err := s.Memories().Insert(ctx, scopeA, mem); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}
	if _, err := s.Memories().Get(ctx, scopeB, mem.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Memories.Get cross-project: got err=%v want ErrNotFound", err)
	}

	topic := store.Topic{ID: newID(), Key: "pa-topic", Description: "secret",
		Status: "active", CreatedAt: base, UpdatedAt: base}
	if err := s.Topics().Upsert(ctx, scopeA, topic); err != nil {
		t.Fatalf("Upsert topic: %v", err)
	}
	if _, err := s.Topics().Get(ctx, scopeB, "pa-topic"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Topics.Get cross-project: got err=%v want ErrNotFound", err)
	}
}

// testCrossSessionIsolation verifies that a session-scoped ListBySession hides
// records from a different session in the same tenant.
func testCrossSessionIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "t-" + newID()
	scopeA := identity.Scope{Tenant: tenant, Session: "sess-A"}
	scopeB := identity.Scope{Tenant: tenant, Session: "sess-B"}

	base := nowMs()
	rec := store.Record{ID: newID(), Role: "user", Content: "sess-A secret",
		OccurredAt: base, CreatedAt: base}
	if err := s.Records().Append(ctx, scopeA, []store.Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// scopeB filters by session_id = sess-B, so sess-A records must not appear.
	list, _, _ := s.Records().ListBySession(ctx, scopeB, "sess-B", "", 10, "")
	for _, r := range list {
		if r.ID == rec.ID {
			t.Error("Records.ListBySession cross-session: saw sess-A record in sess-B list")
		}
	}
}

// =============================================================================
// Q1 — composite cursor: no rows lost or duplicated under timestamp ties
// =============================================================================

// testCursorTimestampTieRecords inserts ≥5 records sharing one occurred_at,
// paginates with a page size that straddles the tie, and asserts every row is
// returned exactly once.
func testCursorTimestampTieRecords(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	const n = 6
	const limit = 3
	ts := nowMs()

	var recs []store.Record
	for i := 0; i < n; i++ {
		recs = append(recs, store.Record{
			ID: newID(), Role: "user", Content: fmt.Sprintf("tie%d", i),
			OccurredAt: ts, // all share the same timestamp
			CreatedAt:  nowMs(),
		})
	}
	if err := s.Records().Append(ctx, scope, recs); err != nil {
		t.Fatalf("Append: %v", err)
	}

	seen := make(map[string]int)
	cursor := ""
	for {
		page, next, err := s.Records().ListBySession(ctx, scope, "", "", limit, cursor)
		if err != nil {
			t.Fatalf("ListBySession: %v", err)
		}
		for _, r := range page {
			seen[r.ID]++
		}
		cursor = next
		if cursor == "" {
			break
		}
	}
	if len(seen) != n {
		t.Errorf("tie cursor (records): got %d distinct rows want %d", len(seen), n)
	}
	for id, cnt := range seen {
		if cnt != 1 {
			t.Errorf("tie cursor (records): row %q seen %d times want 1", id, cnt)
		}
	}
}

// testCursorTimestampTieMemories is the same tie test for memories.ListByStatus.
func testCursorTimestampTieMemories(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	const n = 6
	const limit = 3
	ts := nowMs()

	for i := 0; i < n; i++ {
		mem := store.Memory{
			ID: newID(), Kind: "fact", Content: fmt.Sprintf("tie%d", i),
			Status: "active", TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: ts, // all share the same timestamp
			UpdatedAt: ts,
		}
		if err := s.Memories().Insert(ctx, scope, mem); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	seen := make(map[string]int)
	cursor := ""
	for {
		page, next, err := s.Memories().ListByStatus(ctx, scope, "active", limit, cursor)
		if err != nil {
			t.Fatalf("ListByStatus: %v", err)
		}
		for _, m := range page {
			seen[m.ID]++
		}
		cursor = next
		if cursor == "" {
			break
		}
	}
	if len(seen) != n {
		t.Errorf("tie cursor (memories): got %d distinct rows want %d", len(seen), n)
	}
	for id, cnt := range seen {
		if cnt != 1 {
			t.Errorf("tie cursor (memories): row %q seen %d times want 1", id, cnt)
		}
	}
}

// testCursorTimestampTieEvents is the same tie test for events.List.
func testCursorTimestampTieEvents(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	const n = 6
	const limit = 3
	ts := nowMs()

	for i := 0; i < n; i++ {
		ev := store.Event{
			ID:        newID(),
			Type:      fmt.Sprintf("tie.%d", i),
			CreatedAt: ts, // all share the same timestamp
		}
		if err := s.Events().Emit(ctx, scope, ev); err != nil {
			t.Fatalf("Emit %d: %v", i, err)
		}
	}

	seen := make(map[string]int)
	cursor := ""
	for {
		page, next, err := s.Events().List(ctx, scope, limit, cursor)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, ev := range page {
			seen[ev.ID]++
		}
		cursor = next
		if cursor == "" {
			break
		}
	}
	if len(seen) != n {
		t.Errorf("tie cursor (events): got %d distinct rows want %d", len(seen), n)
	}
	for id, cnt := range seen {
		if cnt != 1 {
			t.Errorf("tie cursor (events): row %q seen %d times want 1", id, cnt)
		}
	}
}

// =============================================================================
// O1 — concurrent job-marker atomicity
// =============================================================================

// testOpsJobMarkerConcurrent launches N goroutines calling CheckAndSetJobMarker
// for the same (job, marker) concurrently. Exactly one must receive true.
// On SQLite the single-writer goroutine serializes writes; on PostgreSQL the
// INSERT ... ON CONFLICT DO NOTHING is inherently atomic.
func testOpsJobMarkerConcurrent(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	const N = 8
	job, marker := "concurrent-sweep", newID()

	var wg sync.WaitGroup
	var winners atomic.Int64
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			set, err := s.Ops().CheckAndSetJobMarker(ctx, job, marker, nowMs())
			if err != nil {
				t.Errorf("CheckAndSetJobMarker: %v", err)
				return
			}
			if set {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()

	if winners.Load() != 1 {
		t.Errorf("concurrent job marker: %d goroutines won, want exactly 1", winners.Load())
	}
}

// =============================================================================
// BranchStore — lifecycle (RFC §5.5, D-029)
// =============================================================================

func testBranchCreateGet(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := mustScope("t-"+newID(), "proj", "user", "sess")

	br := store.Branch{
		ID:             newID(),
		SessionID:      "sess",
		ParentBranchID: "",
		Status:         "open",
		CreatedAt:      nowMs(),
		UpdatedAt:      nowMs(),
	}
	if err := s.Branches().Create(ctx, scope, br); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Branches().Get(ctx, scope, br.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "open" {
		t.Errorf("status: got %q want open", got.Status)
	}
	if got.TenantID != scope.Tenant {
		t.Errorf("tenantID: got %q want %q", got.TenantID, scope.Tenant)
	}
}

func testBranchSetStatus(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	br := store.Branch{
		ID:        newID(),
		SessionID: "sess",
		Status:    "open",
		CreatedAt: nowMs(),
		UpdatedAt: nowMs(),
	}
	if err := s.Branches().Create(ctx, scope, br); err != nil {
		t.Fatalf("Create: %v", err)
	}
	now := nowMs()
	if err := s.Branches().SetStatus(ctx, scope, br.ID, "merged", now); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, err := s.Branches().Get(ctx, scope, br.ID)
	if err != nil {
		t.Fatalf("Get after SetStatus: %v", err)
	}
	if got.Status != "merged" {
		t.Errorf("status after SetStatus: got %q want merged", got.Status)
	}

	// SetStatus on non-existent branch → ErrNotFound.
	if err := s.Branches().SetStatus(ctx, scope, "no-such-id", "discarded", now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetStatus missing: got %v want ErrNotFound", err)
	}
}

func testBranchListBySession(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	sessA := "sess-A-" + newID()
	sessB := "sess-B-" + newID()

	for i := 0; i < 3; i++ {
		br := store.Branch{
			ID:        newID(),
			SessionID: sessA,
			Status:    "open",
			CreatedAt: nowMs() + int64(i),
			UpdatedAt: nowMs(),
		}
		if err := s.Branches().Create(ctx, scope, br); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	// One branch for a different session — must not appear in sessA list.
	brB := store.Branch{
		ID:        newID(),
		SessionID: sessB,
		Status:    "open",
		CreatedAt: nowMs(),
		UpdatedAt: nowMs(),
	}
	if err := s.Branches().Create(ctx, scope, brB); err != nil {
		t.Fatalf("Create sessB: %v", err)
	}

	got, err := s.Branches().ListBySession(ctx, scope, sessA)
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("got %d branches want 3", len(got))
	}
	for _, b := range got {
		if b.SessionID != sessA {
			t.Errorf("unexpected session %q in result", b.SessionID)
		}
	}
}

func testBranchScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	br := store.Branch{
		ID:        newID(),
		SessionID: "sess",
		Status:    "open",
		CreatedAt: nowMs(),
		UpdatedAt: nowMs(),
	}
	if err := s.Branches().Create(ctx, scopeA, br); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Tenant B must not see tenant A's branch.
	if _, err := s.Branches().Get(ctx, scopeB, br.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant Get: got %v want ErrNotFound", err)
	}
	list, _ := s.Branches().ListBySession(ctx, scopeB, "sess")
	for _, b := range list {
		if b.ID == br.ID {
			t.Error("cross-tenant isolation violated: branch visible in wrong tenant")
		}
	}
}

func testBranchGetNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	if _, err := s.Branches().Get(ctx, scope, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get missing: got %v want ErrNotFound", err)
	}
}

// =============================================================================
// Keyring.List
// =============================================================================

func testKeyringList(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()

	tenantA := "tenant-list-A-" + newID()
	tenantB := "tenant-list-B-" + newID()

	keyA, _, err := auth.Generate(tenantA, auth.RoleAgent)
	if err != nil {
		t.Fatalf("Generate A: %v", err)
	}
	keyB, _, err := auth.Generate(tenantB, auth.RoleAdmin)
	if err != nil {
		t.Fatalf("Generate B: %v", err)
	}
	kr := s.Keys()
	if err := kr.Insert(keyA); err != nil {
		t.Fatalf("Insert A: %v", err)
	}
	if err := kr.Insert(keyB); err != nil {
		t.Fatalf("Insert B: %v", err)
	}

	// List all: should include both.
	all, err := kr.List("")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	found := 0
	for _, k := range all {
		if k.ID == keyA.ID || k.ID == keyB.ID {
			found++
		}
	}
	if found != 2 {
		t.Errorf("List all: found %d of 2 expected keys", found)
	}

	// List for tenantA only: should include keyA, not keyB.
	listA, err := kr.List(tenantA)
	if err != nil {
		t.Fatalf("List tenantA: %v", err)
	}
	for _, k := range listA {
		if k.ID == keyB.ID {
			t.Error("List tenantA returned keyB")
		}
	}
	foundA := false
	for _, k := range listA {
		if k.ID == keyA.ID {
			foundA = true
		}
	}
	if !foundA {
		t.Error("List tenantA did not return keyA")
	}
}

// =============================================================================
// Phase 08 — GetByContentHash, FindNeighbors, IncrementCounter, Commit
// =============================================================================

func testMemoryGetByContentHash(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 64 hex chars
	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "some content",
		Status: "active", Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
		ContentHash: hash,
		CreatedAt:   nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Memories().GetByContentHash(ctx, scope, hash)
	if err != nil {
		t.Fatalf("GetByContentHash: %v", err)
	}
	if got.ID != mem.ID {
		t.Errorf("ID: got %q want %q", got.ID, mem.ID)
	}
	if got.ContentHash != hash {
		t.Errorf("ContentHash: got %q want %q", got.ContentHash, hash)
	}
}

func testMemoryGetByContentHashNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	_, err := s.Memories().GetByContentHash(ctx, scope, "0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func testMemoryFindNeighbors(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert a memory via Commit so junction rows are written.
	memID := newID()
	mem := store.Memory{
		ID: memID, Kind: "fact", Content: "Go is fast",
		Status: "active", Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	cs := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   mem,
		Entities: []string{"Go", "speed"},
		Keywords: []string{"programming", "performance"},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// FindNeighbors by entity overlap.
	neighbors, err := s.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{
		Entities: []string{"Go"},
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("FindNeighbors: %v", err)
	}
	if len(neighbors) != 1 {
		t.Errorf("got %d neighbors want 1", len(neighbors))
	}
	if len(neighbors) > 0 && neighbors[0].ID != memID {
		t.Errorf("neighbor ID: got %q want %q", neighbors[0].ID, memID)
	}
}

func testMemoryFindNeighborsEmpty(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Empty query → no results, no error.
	neighbors, err := s.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{})
	if err != nil {
		t.Fatalf("FindNeighbors empty: %v", err)
	}
	if len(neighbors) != 0 {
		t.Errorf("expected 0 neighbors, got %d", len(neighbors))
	}
}

func testMemoryIncrementCounter(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "counter test",
		Status: "active", Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	for _, counter := range []string{"match", "inject", "use", "save", "fail", "noise"} {
		if err := s.Memories().IncrementCounter(ctx, scope, mem.ID, counter); err != nil {
			t.Errorf("IncrementCounter(%q): %v", counter, err)
		}
	}

	got, err := s.Memories().Get(ctx, scope, mem.ID)
	if err != nil {
		t.Fatalf("Get after increment: %v", err)
	}
	if got.MatchCount != 1 {
		t.Errorf("MatchCount: got %d want 1", got.MatchCount)
	}
	if got.InjectCount != 1 {
		t.Errorf("InjectCount: got %d want 1", got.InjectCount)
	}
	if got.UseCount != 1 {
		t.Errorf("UseCount: got %d want 1", got.UseCount)
	}
	if got.SaveCount != 1 {
		t.Errorf("SaveCount: got %d want 1", got.SaveCount)
	}
	if got.FailCount != 1 {
		t.Errorf("FailCount: got %d want 1", got.FailCount)
	}
	if got.NoiseCount != 1 {
		t.Errorf("NoiseCount: got %d want 1", got.NoiseCount)
	}
}

func testMemoryIncrementCounterUnknown(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	err := s.Memories().IncrementCounter(ctx, scope, "any-id", "invalid_counter")
	if err == nil {
		t.Error("expected error for unknown counter, got nil")
	}
}

func testMemoryCommitAdd(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := newID()
	evID := newID()
	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "committed fact",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: hash,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities: []string{"entity-A"},
		Keywords: []string{"keyword-B"},
		Queries:  []string{"anticipated query for fact"},
		Events: []store.Event{
			// CreatedAt: 0 exercises the "set to now" branch in insertEventSQLite.
			{ID: evID, Type: "memory.committed", SubjectID: memID, Payload: `{"action":"add"}`},
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Memory must be readable.
	got, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get after commit: %v", err)
	}
	if got.Content != "committed fact" {
		t.Errorf("Content: got %q want committed fact", got.Content)
	}
	if got.ContentHash != hash {
		t.Errorf("ContentHash: got %q want %q", got.ContentHash, hash)
	}

	// GetByContentHash must find it.
	byHash, err := s.Memories().GetByContentHash(ctx, scope, hash)
	if err != nil {
		t.Fatalf("GetByContentHash: %v", err)
	}
	if byHash.ID != memID {
		t.Errorf("GetByContentHash ID: got %q want %q", byHash.ID, memID)
	}

	// Event must be present.
	evs, _, err := s.Events().List(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("Events.List: %v", err)
	}
	found := false
	for _, ev := range evs {
		if ev.ID == evID {
			found = true
		}
	}
	if !found {
		t.Error("committed event not found in events table")
	}
}

func testMemoryCommitUpdate(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// First add a memory with junctions.
	memID := newID()
	cs1 := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "original",
			Status: "active", Confidence: 0.7, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities: []string{"original-entity"},
	}
	if err := s.Memories().Commit(ctx, scope, cs1); err != nil {
		t.Fatalf("first Commit: %v", err)
	}

	// Now update it with new content and new junctions.
	newHash := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	cs2 := store.CommitSet{
		Action: store.ActionUpdate,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "updated content",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: newHash,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities: []string{"new-entity"},
		Keywords: []string{"new-keyword"},
	}
	if err := s.Memories().Commit(ctx, scope, cs2); err != nil {
		t.Fatalf("update Commit: %v", err)
	}

	got, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "updated content" {
		t.Errorf("Content: got %q want updated content", got.Content)
	}
	if got.ContentHash != newHash {
		t.Errorf("ContentHash: got %q want %q", got.ContentHash, newHash)
	}

	// FindNeighbors by new entity must find the updated memory.
	neighbors, err := s.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{Entities: []string{"new-entity"}})
	if err != nil {
		t.Fatalf("FindNeighbors: %v", err)
	}
	if len(neighbors) != 1 {
		t.Errorf("got %d neighbors want 1", len(neighbors))
	}

	// Old entity must no longer match.
	oldNeighbors, err := s.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{Entities: []string{"original-entity"}})
	if err != nil {
		t.Fatalf("FindNeighbors old: %v", err)
	}
	if len(oldNeighbors) != 0 {
		t.Errorf("old entity still matches: got %d want 0", len(oldNeighbors))
	}
}

func testMemoryCommitDiscard(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	evID := newID()
	cs := store.CommitSet{
		Action: store.ActionDiscard,
		// Memory is zero-value for discard.
		Events: []store.Event{
			{ID: evID, Type: "memory.discarded", SubjectID: "some-id", Payload: `{"reason":"exact_dup"}`, CreatedAt: nowMs()},
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit discard: %v", err)
	}

	// Event must be present.
	evs, _, err := s.Events().List(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("Events.List: %v", err)
	}
	found := false
	for _, ev := range evs {
		if ev.ID == evID {
			found = true
		}
	}
	if !found {
		t.Error("discard event not found in events table")
	}
}

func testMemoryCommitFaultHook(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := newID()
	evID := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "fault hook test",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities: []string{"entity-fault"},
		Events: []store.Event{
			{ID: evID, Type: "memory.added", SubjectID: memID, CreatedAt: nowMs()},
		},
		FaultHook: func() error { return errors.New("injected fault") },
	}

	err := s.Memories().Commit(ctx, scope, cs)
	if err == nil {
		t.Fatal("expected error from FaultHook, got nil")
	}

	// Memory must NOT exist (transaction rolled back).
	if _, getErr := s.Memories().Get(ctx, scope, memID); !errors.Is(getErr, store.ErrNotFound) {
		t.Errorf("memory should not exist after fault; Get returned: %v", getErr)
	}

	// Event must NOT exist.
	evs, _, _ := s.Events().List(ctx, scope, 10, "")
	for _, ev := range evs {
		if ev.ID == evID {
			t.Error("event should not exist after fault (partial write)")
		}
	}
}

// testMemoryCommitFaultHookUpdate tests that a FaultHook on ActionUpdate rolls
// back the entire transaction, leaving no partial rows.
func testMemoryCommitFaultHookUpdate(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert original memory.
	memID := newID()
	orig := store.Memory{
		ID: memID, Kind: "fact", Content: "original",
		Status: "active", Confidence: 0.7, TrustSource: "llm_extracted",
		Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, orig); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Attempt an update with a FaultHook that fails.
	cs := store.CommitSet{
		Action: store.ActionUpdate,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "should-not-persist",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities:  []string{"updated-entity"},
		FaultHook: func() error { return errors.New("injected update fault") },
	}
	if err := s.Memories().Commit(ctx, scope, cs); err == nil {
		t.Fatal("expected error from FaultHook on update, got nil")
	}

	// Memory content must remain original (transaction rolled back).
	got, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "original" {
		t.Errorf("update fault: content = %q, want original (rolled back)", got.Content)
	}
}

// testMemoryCommitFaultHookSupersede tests that a FaultHook on ActionSupersede
// rolls back the transaction, leaving the target memory in its original state.
func testMemoryCommitFaultHookSupersede(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert the target memory.
	oldID := newID()
	oldMem := store.Memory{
		ID: oldID, Kind: "fact", Content: "old fact",
		Status: "active", Confidence: 0.5, TrustSource: "llm_extracted",
		Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, oldMem); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	newMemID := newID()
	cs := store.CommitSet{
		Action: store.ActionSupersede,
		Memory: store.Memory{
			ID: newMemID, Kind: "fact", Content: "new fact (should not persist)",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, SupersedesID: oldID,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Targets:   []store.Memory{oldMem},
		FaultHook: func() error { return errors.New("injected supersede fault") },
	}
	if err := s.Memories().Commit(ctx, scope, cs); err == nil {
		t.Fatal("expected error from FaultHook on supersede, got nil")
	}

	// Target must still be active (transaction rolled back).
	got, err := s.Memories().Get(ctx, scope, oldID)
	if err != nil {
		t.Fatalf("Get target: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("supersede fault: target status = %q, want active (rolled back)", got.Status)
	}

	// New memory must not exist.
	if _, err := s.Memories().Get(ctx, scope, newMemID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("supersede fault: new memory should not exist; got %v", err)
	}
}

func testMemoryCommitSupersede(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert the memory to be superseded.
	oldID := newID()
	oldMem := store.Memory{
		ID: oldID, Kind: "fact", Content: "old fact",
		Status: "active", Confidence: 0.5, TrustSource: "llm_extracted",
		Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, oldMem); err != nil {
		t.Fatalf("Insert old: %v", err)
	}

	newID2 := newID()
	cs := store.CommitSet{
		Action: store.ActionSupersede,
		Memory: store.Memory{
			ID: newID2, Kind: "fact", Content: "corrected fact",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, SupersedesID: oldID,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Targets: []store.Memory{oldMem},
		// Contradicts link from new memory to superseded target (both FKs satisfied).
		Links: []store.Link{
			{ID: newID(), FromMemory: newID2, ToMemory: oldID, Type: "contradicts", Source: "reconciler", Confidence: 1.0},
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit supersede: %v", err)
	}

	// Old memory must be superseded.
	gotOld, err := s.Memories().Get(ctx, scope, oldID)
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if gotOld.Status != "superseded" {
		t.Errorf("old memory status: got %q want superseded", gotOld.Status)
	}
	if gotOld.SupersededByID != newID2 {
		t.Errorf("old memory SupersededByID: got %q want %q", gotOld.SupersededByID, newID2)
	}

	// New memory must exist.
	gotNew, err := s.Memories().Get(ctx, scope, newID2)
	if err != nil {
		t.Fatalf("Get new: %v", err)
	}
	if gotNew.Content != "corrected fact" {
		t.Errorf("new memory content: got %q want corrected fact", gotNew.Content)
	}
}

func testMemoryCommitMerge(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert two source memories.
	srcA := store.Memory{
		ID: newID(), Kind: "fact", Content: "source A",
		Status: "active", Confidence: 0.6, TrustSource: "llm_extracted",
		Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	srcB := store.Memory{
		ID: newID(), Kind: "fact", Content: "source B",
		Status: "active", Confidence: 0.6, TrustSource: "llm_extracted",
		Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, srcA); err != nil {
		t.Fatalf("Insert srcA: %v", err)
	}
	if err := s.Memories().Insert(ctx, scope, srcB); err != nil {
		t.Fatalf("Insert srcB: %v", err)
	}

	mergedID := newID()
	cs := store.CommitSet{
		Action: store.ActionMerge,
		Memory: store.Memory{
			ID: mergedID, Kind: "fact", Content: "merged A and B",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Targets: []store.Memory{srcA, srcB},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit merge: %v", err)
	}

	// Both source memories must be superseded.
	for _, src := range []store.Memory{srcA, srcB} {
		got, err := s.Memories().Get(ctx, scope, src.ID)
		if err != nil {
			t.Fatalf("Get source %q: %v", src.ID, err)
		}
		if got.Status != "superseded" {
			t.Errorf("source %q status: got %q want superseded", src.ID, got.Status)
		}
		if got.SupersededByID != mergedID {
			t.Errorf("source %q SupersededByID: got %q want %q", src.ID, got.SupersededByID, mergedID)
		}
	}

	// Merged memory must exist.
	gotMerged, err := s.Memories().Get(ctx, scope, mergedID)
	if err != nil {
		t.Fatalf("Get merged: %v", err)
	}
	if gotMerged.Content != "merged A and B" {
		t.Errorf("merged content: got %q", gotMerged.Content)
	}
}

func testMemoryCommitScopeRequired(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	zero := identity.Scope{}

	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{ID: newID(), Kind: "fact", Content: "x", Status: "active", TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs()},
	}
	err := s.Memories().Commit(ctx, zero, cs)
	if !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("Commit with empty scope: got %v want ErrScopeRequired", err)
	}
}

func testMemoryContentHashScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	hash := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	memA := store.Memory{
		ID: newID(), Kind: "fact", Content: "tenant A content",
		Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: hash,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scopeA, memA); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Tenant B must not see tenant A's hash.
	_, err := s.Memories().GetByContentHash(ctx, scopeB, hash)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant GetByContentHash: got %v want ErrNotFound", err)
	}
}

func testMemoryFindNeighborsScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("tenant-A-" + newID())
	scopeB := tenantScope("tenant-B-" + newID())

	// Commit a memory with entities in tenant A.
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: newID(), Kind: "fact", Content: "tenant A memory",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities: []string{"shared-entity"},
	}
	if err := s.Memories().Commit(ctx, scopeA, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Tenant B finds no neighbors for the same entity.
	neighbors, err := s.Memories().FindNeighbors(ctx, scopeB, store.NeighborQuery{
		Entities: []string{"shared-entity"},
	})
	if err != nil {
		t.Fatalf("FindNeighbors: %v", err)
	}
	if len(neighbors) != 0 {
		t.Errorf("cross-tenant FindNeighbors: got %d results want 0", len(neighbors))
	}
}

// testMemoryFindNeighborsByKeyword verifies that FindNeighbors works when
// the query uses Keywords (not just Entities). Covers the keyword CTE branch.
func testMemoryFindNeighborsByKeyword(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "Go uses goroutines",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Keywords: []string{"goroutine", "concurrency"},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Search by keyword only — exercises the keyword CTE path in FindNeighbors.
	neighbors, err := s.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{
		Keywords: []string{"goroutine"},
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("FindNeighbors by keyword: %v", err)
	}
	if len(neighbors) != 1 {
		t.Errorf("got %d neighbors want 1", len(neighbors))
	}
	if len(neighbors) > 0 && neighbors[0].ID != memID {
		t.Errorf("neighbor ID: got %q want %q", neighbors[0].ID, memID)
	}
}

// testMemoryCommitAddZeroTimes verifies that Commit with zero-valued CreatedAt
// and UpdatedAt on the Memory, and an Event with empty Payload, are all
// defaulted to server-side values. Covers the zero-timestamp branches in
// insertMemorySQLite and insertEventSQLite.
func testMemoryCommitAddZeroTimes(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := newID()
	evID := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "zero-time fact",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0,
			// CreatedAt and UpdatedAt deliberately zero — defaults applied server-side.
		},
		Events: []store.Event{
			// Payload deliberately empty — insertEventSQLite must set it to "{}".
			{ID: evID, Type: "memory.added", SubjectID: memID},
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit with zero timestamps: %v", err)
	}

	got, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get after zero-time commit: %v", err)
	}
	if got.CreatedAt == 0 {
		t.Error("CreatedAt should have been defaulted to non-zero")
	}
	if got.UpdatedAt == 0 {
		t.Error("UpdatedAt should have been defaulted to non-zero")
	}
}

// testMemoryCommitUpdateZeroTimes verifies that an ActionUpdate CommitSet with
// a zero-valued UpdatedAt on the Memory is defaulted server-side. Covers the
// zero-timestamp branch in updateMemorySQLite.
func testMemoryCommitUpdateZeroTimes(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert original memory.
	memID := newID()
	cs1 := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "original",
			Status: "active", Confidence: 0.7, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs1); err != nil {
		t.Fatalf("first Commit: %v", err)
	}

	// Update with zero UpdatedAt — should default server-side.
	cs2 := store.CommitSet{
		Action: store.ActionUpdate,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "updated",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0,
			// UpdatedAt deliberately zero.
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs2); err != nil {
		t.Fatalf("Commit update with zero UpdatedAt: %v", err)
	}

	got, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if got.Content != "updated" {
		t.Errorf("Content: got %q want updated", got.Content)
	}
	if got.UpdatedAt == 0 {
		t.Error("UpdatedAt should have been defaulted to non-zero")
	}
}

// testMemoryCommitAddWithProvenance verifies that a CommitSet with Provenance
// rows (including a zero CreatedAt) writes provenance correctly. Covers the
// insertProvenanceSQLite loop body.
func testMemoryCommitAddWithProvenance(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Create a record so the FK on provenance.record_id is satisfied.
	recID := newID()
	rec := store.Record{
		ID:         recID,
		Role:       "user",
		Content:    "source record for provenance",
		OccurredAt: nowMs(),
		CreatedAt:  nowMs(),
	}
	if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatalf("Append record: %v", err)
	}

	memID := newID()
	provID := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "provenance fact",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Provenance: []store.Provenance{
			{
				ID:       provID,
				MemoryID: memID,
				RecordID: recID,
				// CreatedAt deliberately zero — defaulted server-side.
			},
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit with provenance: %v", err)
	}

	// Memory must be readable.
	got, err := s.Memories().Get(ctx, scope, memID)
	if err != nil {
		t.Fatalf("Get after commit with provenance: %v", err)
	}
	if got.Content != "provenance fact" {
		t.Errorf("Content: got %q want provenance fact", got.Content)
	}
}

// testMemoryCommitAddDuplicateContent verifies that committing two memories with
// the same scope+content_hash returns ErrDuplicateContent on the second commit
// (m7: TOCTOU unique index guard).
func testMemoryCommitAddDuplicateContent(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	hash := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	cs1 := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: newID(), Kind: "fact", Content: "first copy",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: hash,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs1); err != nil {
		t.Fatalf("first Commit: %v", err)
	}

	cs2 := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: newID(), Kind: "fact", Content: "second copy same hash",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: hash,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
	}
	err := s.Memories().Commit(ctx, scope, cs2)
	if !errors.Is(err, store.ErrDuplicateContent) {
		t.Errorf("second Commit with same hash: got %v want ErrDuplicateContent", err)
	}
}

// testMemoryCommitAddConcurrentDedup verifies the TOCTOU guard under concurrent
// load: N goroutines race to commit a memory with the same scope+content_hash;
// exactly one must succeed and the rest must get ErrDuplicateContent.
// After all goroutines finish exactly one row with that hash must exist.
func testMemoryCommitAddConcurrentDedup(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	const n = 8
	hash := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	var (
		wg         sync.WaitGroup
		successCnt int64
		dedupCnt   int64
		otherErrs  int64
	)
	start := make(chan struct{})
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			cs := store.CommitSet{
				Action: store.ActionAdd,
				Memory: store.Memory{
					ID: newID(), Kind: "fact", Content: "concurrent copy",
					Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
					Stability: 1.0, ContentHash: hash,
					CreatedAt: nowMs(), UpdatedAt: nowMs(),
				},
			}
			err := s.Memories().Commit(ctx, scope, cs)
			switch {
			case err == nil:
				atomic.AddInt64(&successCnt, 1)
			case errors.Is(err, store.ErrDuplicateContent):
				atomic.AddInt64(&dedupCnt, 1)
			default:
				atomic.AddInt64(&otherErrs, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if successCnt != 1 {
		t.Errorf("concurrent dedup: %d goroutines succeeded, want exactly 1", successCnt)
	}
	if dedupCnt != n-1 {
		t.Errorf("concurrent dedup: %d ErrDuplicateContent, want %d", dedupCnt, n-1)
	}
	if otherErrs != 0 {
		t.Errorf("concurrent dedup: %d unexpected errors", otherErrs)
	}

	// Exactly one row with that hash must exist.
	if _, err := s.Memories().GetByContentHash(ctx, scope, hash); err != nil {
		t.Errorf("GetByContentHash after concurrent dedup: %v", err)
	}
}

// testMemoryGetJunctions verifies that GetJunctions returns the entities,
// keywords, queries, and provenance rows written by Commit.
func testMemoryGetJunctions(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert a record to satisfy the provenance FK.
	recID := newID()
	rec := store.Record{
		ID:         recID,
		Role:       "user",
		Content:    "source record for junctions",
		OccurredAt: nowMs(),
		CreatedAt:  nowMs(),
	}
	if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatalf("Append record: %v", err)
	}

	memID := newID()
	provID := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "junction fact",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities: []string{"entity-X", "entity-Y"},
		Keywords: []string{"kw-alpha"},
		Queries:  []string{"what is entity-X?"},
		Provenance: []store.Provenance{
			{ID: provID, MemoryID: memID, RecordID: recID},
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	j, err := s.Memories().GetJunctions(ctx, scope, memID)
	if err != nil {
		t.Fatalf("GetJunctions: %v", err)
	}

	if len(j.Entities) != 2 {
		t.Errorf("Entities: got %d want 2", len(j.Entities))
	}
	if len(j.Keywords) != 1 || j.Keywords[0] != "kw-alpha" {
		t.Errorf("Keywords: got %v want [kw-alpha]", j.Keywords)
	}
	if len(j.Queries) != 1 || j.Queries[0] != "what is entity-X?" {
		t.Errorf("Queries: got %v want [what is entity-X?]", j.Queries)
	}
	if len(j.Provenance) != 1 || j.Provenance[0].RecordID != recID {
		t.Errorf("Provenance: got %v want 1 row with RecordID=%q", j.Provenance, recID)
	}
}

// testMemoryGetJunctionsEmpty verifies that GetJunctions returns empty (not
// ErrNotFound) when the memory has no junction rows.
func testMemoryGetJunctionsEmpty(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := newID()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "no junctions",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		// No Entities, Keywords, Queries, or Provenance.
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	j, err := s.Memories().GetJunctions(ctx, scope, memID)
	if err != nil {
		t.Fatalf("GetJunctions: %v", err)
	}
	if len(j.Entities) != 0 {
		t.Errorf("Entities: got %d want 0", len(j.Entities))
	}
	if len(j.Keywords) != 0 {
		t.Errorf("Keywords: got %d want 0", len(j.Keywords))
	}
	if len(j.Queries) != 0 {
		t.Errorf("Queries: got %d want 0", len(j.Queries))
	}
	if len(j.Provenance) != 0 {
		t.Errorf("Provenance: got %d want 0", len(j.Provenance))
	}
}

// testMemoryGetByContentHashCrossUser verifies that GetByContentHash is isolated
// per user: user A cannot see user B's memory even within the same tenant.
func testMemoryGetByContentHashCrossUser(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "tenant-xu-" + newID()
	scopeA := mustScope(tenant, "", "user-A", "")
	scopeB := mustScope(tenant, "", "user-B", "")

	hash := "1111111111111111111111111111111111111111111111111111111111111111"
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: newID(), Kind: "fact", Content: "user A content",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: hash,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
	}
	if err := s.Memories().Commit(ctx, scopeA, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// User A finds their memory.
	if _, err := s.Memories().GetByContentHash(ctx, scopeA, hash); err != nil {
		t.Errorf("user A GetByContentHash: %v", err)
	}

	// User B must not find user A's memory.
	_, err := s.Memories().GetByContentHash(ctx, scopeB, hash)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-user GetByContentHash: got %v want ErrNotFound", err)
	}
}

// testMemoryFindNeighborsCrossUser verifies that FindNeighbors is isolated per
// user: user A's entities do not surface in user B's neighbor query within the
// same tenant.
func testMemoryFindNeighborsCrossUser(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "tenant-nc-" + newID()
	scopeA := mustScope(tenant, "", "user-A", "")
	scopeB := mustScope(tenant, "", "user-B", "")

	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: newID(), Kind: "fact", Content: "user A entity memory",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Entities: []string{"cross-user-entity"},
	}
	if err := s.Memories().Commit(ctx, scopeA, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// User A finds their memory.
	neighborsA, err := s.Memories().FindNeighbors(ctx, scopeA, store.NeighborQuery{
		Entities: []string{"cross-user-entity"},
	})
	if err != nil {
		t.Fatalf("user A FindNeighbors: %v", err)
	}
	if len(neighborsA) != 1 {
		t.Errorf("user A FindNeighbors: got %d results want 1", len(neighborsA))
	}

	// User B must see no results.
	neighborsB, err := s.Memories().FindNeighbors(ctx, scopeB, store.NeighborQuery{
		Entities: []string{"cross-user-entity"},
	})
	if err != nil {
		t.Fatalf("user B FindNeighbors: %v", err)
	}
	if len(neighborsB) != 0 {
		t.Errorf("cross-user FindNeighbors: got %d results want 0", len(neighborsB))
	}
}

// testMemoryCommitFaultHookMerge tests that a FaultHook on ActionMerge rolls
// back the transaction, leaving source memories unchanged.
func testMemoryCommitFaultHookMerge(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert two memories to be merged.
	srcA := store.Memory{
		ID: newID(), Kind: "fact", Content: "merge src A",
		Status: "active", Confidence: 0.6, TrustSource: "llm_extracted",
		Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	srcB := store.Memory{
		ID: newID(), Kind: "fact", Content: "merge src B",
		Status: "active", Confidence: 0.6, TrustSource: "llm_extracted",
		Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, srcA); err != nil {
		t.Fatalf("Insert srcA: %v", err)
	}
	if err := s.Memories().Insert(ctx, scope, srcB); err != nil {
		t.Fatalf("Insert srcB: %v", err)
	}

	mergedID := newID()
	cs := store.CommitSet{
		Action: store.ActionMerge,
		Memory: store.Memory{
			ID: mergedID, Kind: "fact", Content: "merged (should not persist)",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
		Targets:   []store.Memory{srcA, srcB},
		FaultHook: func() error { return errors.New("injected merge fault") },
	}
	if err := s.Memories().Commit(ctx, scope, cs); err == nil {
		t.Fatal("expected error from FaultHook on merge, got nil")
	}

	// Source memories must still be active (transaction rolled back).
	for _, src := range []store.Memory{srcA, srcB} {
		got, err := s.Memories().Get(ctx, scope, src.ID)
		if err != nil {
			t.Fatalf("Get source %q after merge fault: %v", src.ID, err)
		}
		if got.Status != "active" {
			t.Errorf("merge fault: source %q status = %q, want active (rolled back)", src.ID, got.Status)
		}
	}

	// Merged memory must not exist.
	if _, err := s.Memories().Get(ctx, scope, mergedID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("merge fault: merged memory should not exist; got %v", err)
	}
}

// testMemoryCommitUnknownAction verifies that Commit returns an error for an
// unrecognised action, leaving no memory row behind.
func testMemoryCommitUnknownAction(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	memID := newID()
	cs := store.CommitSet{
		Action: store.ReconcileAction("bogus_action"),
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "should not persist",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs(),
		},
	}
	if err := s.Memories().Commit(ctx, scope, cs); err == nil {
		t.Fatal("expected error for unknown action, got nil")
	}

	// No row must exist.
	if _, err := s.Memories().Get(ctx, scope, memID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown action: memory should not be persisted; got %v", err)
	}
}

// --- Phase 10: RecordStore.CountRecordsSince ---------------------------------

// testRecordCountRecordsSince verifies that CountRecordsSince returns the
// correct count of records created after sinceMs.
func testRecordCountRecordsSince(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("countsince-" + ulid.Make().String())

	// Nothing yet → 0.
	n, err := s.Records().CountRecordsSince(ctx, scope, 0)
	if err != nil {
		t.Fatalf("CountRecordsSince empty: %v", err)
	}
	if n != 0 {
		t.Errorf("empty store: got %d want 0", n)
	}

	// Insert three records at different timestamps.
	base := int64(1_000_000_000_000)
	for i, ts := range []int64{base, base + 1000, base + 2000} {
		rec := store.Record{
			ID: newID(), Role: "user", Content: fmt.Sprintf("msg%d", i),
			CreatedAt: ts, OccurredAt: ts,
		}
		if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	// Count since base-1 (before all records) → 3.
	n, err = s.Records().CountRecordsSince(ctx, scope, base-1)
	if err != nil {
		t.Fatalf("CountRecordsSince all: %v", err)
	}
	if n != 3 {
		t.Errorf("all: got %d want 3", n)
	}

	// Count since base (strictly after base) → 2.
	n, err = s.Records().CountRecordsSince(ctx, scope, base)
	if err != nil {
		t.Fatalf("CountRecordsSince since-base: %v", err)
	}
	if n != 2 {
		t.Errorf("since base: got %d want 2", n)
	}

	// Count since base+2000 (after all) → 0.
	n, err = s.Records().CountRecordsSince(ctx, scope, base+2000)
	if err != nil {
		t.Fatalf("CountRecordsSince after-all: %v", err)
	}
	if n != 0 {
		t.Errorf("after-all: got %d want 0", n)
	}
}

// testRecordCountRecordsSinceScopeIsolation verifies that CountRecordsSince
// only counts records for the given scope (P3 enforcement).
func testRecordCountRecordsSinceScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	scopeA := tenantScope("countsince-isol-a-" + ulid.Make().String())
	scopeB := tenantScope("countsince-isol-b-" + ulid.Make().String())

	base := int64(2_000_000_000_000)
	// Insert 2 records in scopeA and 1 in scopeB.
	for i := 0; i < 2; i++ {
		rec := store.Record{ID: newID(), Role: "user", Content: "a", CreatedAt: base + int64(i)*1000, OccurredAt: base}
		if err := s.Records().Append(ctx, scopeA, []store.Record{rec}); err != nil {
			t.Fatalf("Append scopeA[%d]: %v", i, err)
		}
	}
	recB := store.Record{ID: newID(), Role: "user", Content: "b", CreatedAt: base, OccurredAt: base}
	if err := s.Records().Append(ctx, scopeB, []store.Record{recB}); err != nil {
		t.Fatalf("Append scopeB: %v", err)
	}

	nA, err := s.Records().CountRecordsSince(ctx, scopeA, base-1)
	if err != nil {
		t.Fatalf("CountRecordsSince scopeA: %v", err)
	}
	if nA != 2 {
		t.Errorf("scopeA: got %d want 2", nA)
	}

	nB, err := s.Records().CountRecordsSince(ctx, scopeB, base-1)
	if err != nil {
		t.Fatalf("CountRecordsSince scopeB: %v", err)
	}
	if nB != 1 {
		t.Errorf("scopeB: got %d want 1", nB)
	}
}
