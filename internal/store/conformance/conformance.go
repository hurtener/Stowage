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
	t.Run("RecordListByOutcome", func(t *testing.T) { testRecordListByOutcome(t, factory) })
	t.Run("RecordScopeIsolation", func(t *testing.T) { testRecordScopeIsolation(t, factory) })
	t.Run("RecordGetNotFound", func(t *testing.T) { testRecordGetNotFound(t, factory) })
	t.Run("MemoryInsertGet", func(t *testing.T) { testMemoryInsertGet(t, factory) })
	t.Run("MemoryGetNotFound", func(t *testing.T) { testMemoryGetNotFound(t, factory) })
	t.Run("MemoryUpdate", func(t *testing.T) { testMemoryUpdate(t, factory) })
	t.Run("MemorySetStatus", func(t *testing.T) { testMemorySetStatus(t, factory) })
	t.Run("MemoryListByStatus", func(t *testing.T) { testMemoryListByStatus(t, factory) })
	t.Run("MemoryListByKinds", func(t *testing.T) { testMemoryListByKinds(t, factory) })
	t.Run("MemoryListByKindsScopeIsolation", func(t *testing.T) { testMemoryListByKindsScopeIsolation(t, factory) })
	t.Run("MemoryListByStatusCursor", func(t *testing.T) { testMemoryListByStatusCursor(t, factory) })
	t.Run("MemoryLinks", func(t *testing.T) { testMemoryLinks(t, factory) })
	t.Run("MemoryLinksEmpty", func(t *testing.T) { testMemoryLinksEmpty(t, factory) })
	t.Run("MemoryProvenance", func(t *testing.T) { testMemoryProvenance(t, factory) })
	t.Run("MemoryListByRecords", func(t *testing.T) { testMemoryListByRecords(t, factory) })
	t.Run("MemoryScopeIsolation", func(t *testing.T) { testMemoryScopeIsolation(t, factory) })
	t.Run("MemoryDistinctScopes", func(t *testing.T) { testMemoryDistinctScopes(t, factory) })
	t.Run("MemoryListActiveInScope", func(t *testing.T) { testMemoryListActiveInScope(t, factory) })
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
	t.Run("SuggestionLifecycle", func(t *testing.T) { testSuggestionLifecycle(t, factory) })
	t.Run("SuggestionScopeIsolation", func(t *testing.T) { testSuggestionScopeIsolation(t, factory) })
	t.Run("SuggestionScopeUserIsolation", func(t *testing.T) { testSuggestionScopeUserIsolation(t, factory) })
	t.Run("ScopeSettingsKV", func(t *testing.T) { testScopeSettingsKV(t, factory) })
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
	// Episodes (Phase 22, D-079)
	t.Run("EpisodeCRUD", func(t *testing.T) { testEpisodeCRUD(t, factory) })
	t.Run("EpisodeNeedingNarrative", func(t *testing.T) { testEpisodeNeedingNarrative(t, factory) })
	t.Run("EpisodeScopeIsolation", func(t *testing.T) { testEpisodeScopeIsolation(t, factory) })
	t.Run("RecordDistinctSessions", func(t *testing.T) { testRecordDistinctSessions(t, factory) })
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
	t.Run("MemoryFindNeighborsKindFilter", func(t *testing.T) { testMemoryFindNeighborsKindFilter(t, factory) })
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
	t.Run("VectorDistinctModels", func(t *testing.T) { testVectorDistinctModels(t, factory) })
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
	t.Run("RecordCreatedAtsSince", func(t *testing.T) { testRecordCreatedAtsSince(t, factory) })
	// Phase 11 — InjectionStore + MemoryStore.ApplyFeedback + RecordStore.GetMany
	t.Run("InjectionAppendGet", func(t *testing.T) { testInjectionAppendGet(t, factory) })
	t.Run("InjectionAppendIdempotent", func(t *testing.T) { testInjectionAppendIdempotent(t, factory) })
	t.Run("InjectionGetNotFound", func(t *testing.T) { testInjectionGetNotFound(t, factory) })
	t.Run("InjectionListByResponse", func(t *testing.T) { testInjectionListByResponse(t, factory) })
	t.Run("InjectionScopeIsolation", func(t *testing.T) { testInjectionScopeIsolation(t, factory) })
	t.Run("MarkWrongCitation", func(t *testing.T) { testMarkWrongCitation(t, factory) })
	t.Run("MarkWrongCitationNotFound", func(t *testing.T) { testMarkWrongCitationNotFound(t, factory) })
	t.Run("HubSignals", func(t *testing.T) { testHubSignals(t, factory) })
	t.Run("HubSignalsWindowAndEmptySig", func(t *testing.T) { testHubSignalsWindowAndEmptySig(t, factory) })
	t.Run("HubSignalsScopeIsolation", func(t *testing.T) { testHubSignalsScopeIsolation(t, factory) })
	t.Run("ApplyFeedback", func(t *testing.T) { testApplyFeedback(t, factory) })
	t.Run("ApplyFeedbackNoopMissing", func(t *testing.T) { testApplyFeedbackNoopMissing(t, factory) })
	t.Run("ApplyFeedbackUnknownSignal", func(t *testing.T) { testApplyFeedbackUnknownSignal(t, factory) })
	t.Run("RecordGetMany", func(t *testing.T) { testRecordGetMany(t, factory) })
	t.Run("RecordGetManyMissingOmitted", func(t *testing.T) { testRecordGetManyMissingOmitted(t, factory) })
	t.Run("RecordGetManyScopeIsolation", func(t *testing.T) { testRecordGetManyScopeIsolation(t, factory) })
	// Phase 14 — lifecycle sweep store methods
	t.Run("TenantsListing", func(t *testing.T) { testTenantsListing(t, factory) })
	t.Run("MemoryListActiveForDecay", func(t *testing.T) { testMemoryListActiveForDecay(t, factory) })
	t.Run("MemorySetValidUntil", func(t *testing.T) { testMemorySetValidUntil(t, factory) })
	// Phase 15 — grants: groups, membership, grants, EffectiveScopes
	RunGrants(t, factory)
	// Phase 18 — rollback & pending-confirmation resolution
	RunPhase18(t, factory)
	// Phase 21 — DSAR cascading delete (OpsStore.DeleteUserData, D-098)
	t.Run("DSARDeleteUserDataCascade", func(t *testing.T) { testDSARDeleteUserDataCascade(t, factory) })
	t.Run("DSARDeleteUserDataCrossTenantIsolation", func(t *testing.T) { testDSARDeleteUserDataCrossTenantIsolation(t, factory) })
	t.Run("DSARDeleteUserDataScopeRequired", func(t *testing.T) { testDSARDeleteUserDataScopeRequired(t, factory) })
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

func testRecordListByOutcome(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())
	base := time.Now().Add(-time.Hour).UnixMilli()

	// success + failure tagged, plus an untagged record and an old success.
	succ := store.Record{ID: newID(), SessionID: "s1", BranchID: "main", Role: "tool", Content: "did X", Outcome: "success", OccurredAt: base + 100, CreatedAt: base + 100}
	fail := store.Record{ID: newID(), SessionID: "s1", BranchID: "main", Role: "tool", Content: "Y broke", Outcome: "failure", OccurredAt: base + 200, CreatedAt: base + 200}
	plain := store.Record{ID: newID(), SessionID: "s1", BranchID: "main", Role: "user", Content: "hi", Outcome: "", OccurredAt: base + 50, CreatedAt: base + 50}
	old := store.Record{ID: newID(), SessionID: "s0", BranchID: "main", Role: "tool", Content: "old win", Outcome: "success", OccurredAt: base - 10_000, CreatedAt: base - 10_000}
	if err := s.Records().Append(ctx, scope, []store.Record{succ, fail, plain, old}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// since just before succ's timestamp: returns succ+fail (not plain/untagged, not old).
	got, err := s.Records().ListByOutcome(ctx, scope, []string{"success", "failure"}, base+60, 100)
	if err != nil {
		t.Fatalf("ListByOutcome: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 tagged records since cutoff, got %d", len(got))
	}
	// Ordered by (session, branch, occurred_at): succ (100) before fail (200).
	if got[0].ID != succ.ID || got[1].ID != fail.ID {
		t.Errorf("ordering wrong: got %s,%s want %s,%s", got[0].ID, got[1].ID, succ.ID, fail.ID)
	}
	for _, r := range got {
		if r.Outcome == "" {
			t.Error("untagged record leaked into ListByOutcome")
		}
	}

	// Filter to failures only.
	fails, err := s.Records().ListByOutcome(ctx, scope, []string{"failure"}, base+60, 100)
	if err != nil {
		t.Fatalf("ListByOutcome(failure): %v", err)
	}
	if len(fails) != 1 || fails[0].ID != fail.ID {
		t.Errorf("expected only the failure record, got %d", len(fails))
	}

	// Empty outcomes → no rows.
	none, err := s.Records().ListByOutcome(ctx, scope, nil, 0, 100)
	if err != nil {
		t.Fatalf("ListByOutcome(nil): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("empty outcomes should return no rows, got %d", len(none))
	}

	// Scope isolation: a different tenant sees nothing.
	other := tenantScope("t-" + newID())
	iso, err := s.Records().ListByOutcome(ctx, other, []string{"success", "failure"}, 0, 100)
	if err != nil {
		t.Fatalf("ListByOutcome(other): %v", err)
	}
	if len(iso) != 0 {
		t.Errorf("cross-scope leak: other tenant saw %d records", len(iso))
	}

	// Missing scope fails closed (P3).
	if _, err := s.Records().ListByOutcome(ctx, identity.Scope{}, []string{"success"}, 0, 100); err == nil {
		t.Error("expected error on empty scope (P3 fail-closed)")
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

// testMemoryListByKinds proves the D-072 playbook view: active-only + kind
// filter, with non-matching kinds and non-active statuses excluded.
func testMemoryListByKinds(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	insert := func(kind, status string) string {
		id := newID()
		mem := store.Memory{
			ID: id, Kind: kind, Content: kind + "-" + status,
			Status: status, Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		}
		if err := s.Memories().Insert(ctx, scope, mem); err != nil {
			t.Fatalf("Insert %s/%s: %v", kind, status, err)
		}
		return id
	}

	strategyID := insert("strategy", "active")
	failureID := insert("failure_mode", "active")
	insert("fact", "active")         // wrong kind — excluded
	insert("strategy", "superseded") // right kind, wrong status — excluded
	insert("gotcha", "active")       // a kind we don't request below — excluded

	got, err := s.Memories().ListByKinds(ctx, scope, []string{"strategy", "failure_mode"})
	if err != nil {
		t.Fatalf("ListByKinds: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, m := range got {
		gotIDs[m.ID] = true
		if m.Status != "active" {
			t.Errorf("ListByKinds returned non-active memory %s (status=%s)", m.ID, m.Status)
		}
		if m.Kind != "strategy" && m.Kind != "failure_mode" {
			t.Errorf("ListByKinds returned out-of-filter kind %q", m.Kind)
		}
	}
	if !gotIDs[strategyID] || !gotIDs[failureID] {
		t.Errorf("ListByKinds missing expected active rows: got %v", gotIDs)
	}
	if len(got) != 2 {
		t.Errorf("ListByKinds: got %d rows, want 2", len(got))
	}

	// Empty kinds → empty, non-error.
	empty, err := s.Memories().ListByKinds(ctx, scope, nil)
	if err != nil {
		t.Fatalf("ListByKinds empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListByKinds(nil): got %d rows, want 0", len(empty))
	}
}

// testMemoryListByKindsScopeIsolation proves cross-scope rows are invisible (P3).
func testMemoryListByKindsScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "t-" + newID()
	scopeA := mustScope(tenant, "p", "userA", "")
	scopeB := mustScope(tenant, "p", "userB", "")

	memA := store.Memory{
		ID: newID(), Kind: "strategy", Content: "A-strategy", Status: "active",
		Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scopeA, memA); err != nil {
		t.Fatalf("Insert A: %v", err)
	}

	got, err := s.Memories().ListByKinds(ctx, scopeB, []string{"strategy"})
	if err != nil {
		t.Fatalf("ListByKinds B: %v", err)
	}
	for _, m := range got {
		if m.ID == memA.ID {
			t.Fatalf("scope isolation breach: user B saw user A's memory %s", memA.ID)
		}
	}

	// Empty-scope guard: tenant required (P3 fails closed).
	if _, err := s.Memories().ListByKinds(ctx, identity.Scope{}, []string{"strategy"}); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("ListByKinds empty scope: got %v want ErrScopeRequired", err)
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

func testMemoryListByRecords(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Two records; r1 backs a decision + a fact, r2 backs a preference, r3 backs nothing.
	r1, r2, r3 := newID(), newID(), newID()
	recs := []store.Record{
		{ID: r1, Role: "user", Content: "a", OccurredAt: nowMs(), CreatedAt: nowMs()},
		{ID: r2, Role: "user", Content: "b", OccurredAt: nowMs(), CreatedAt: nowMs()},
		{ID: r3, Role: "user", Content: "c", OccurredAt: nowMs(), CreatedAt: nowMs()},
	}
	if err := s.Records().Append(ctx, scope, recs); err != nil {
		t.Fatalf("append records: %v", err)
	}
	mkMem := func(kind, status string) string {
		id := newID()
		if err := s.Memories().Insert(ctx, scope, store.Memory{
			ID: id, Kind: kind, Content: kind + " mem", Status: status,
			Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: nowMs(), UpdatedAt: nowMs(),
		}); err != nil {
			t.Fatalf("insert %s: %v", kind, err)
		}
		return id
	}
	decID := mkMem("decision", "active")
	factID := mkMem("fact", "active")
	prefID := mkMem("preference", "active")
	supID := mkMem("decision", "superseded") // non-active must be excluded

	addProv := func(memID, recID string) {
		if err := s.Memories().AddProvenance(ctx, scope, []store.Provenance{{
			ID: newID(), MemoryID: memID, RecordID: recID, TenantID: scope.Tenant, CreatedAt: nowMs(),
		}}); err != nil {
			t.Fatalf("addprov: %v", err)
		}
	}
	addProv(decID, r1)
	addProv(factID, r1)
	addProv(prefID, r2)
	addProv(supID, r1) // superseded decision also points at r1

	// kind-filtered to decision: only decID (factID excluded by kind; supID by status).
	got, err := s.Memories().ListMemoriesByRecords(ctx, scope, []string{r1}, []string{"decision"})
	if err != nil {
		t.Fatalf("ListMemoriesByRecords: %v", err)
	}
	if len(got) != 1 || got[0].ID != decID {
		t.Fatalf("kind-filtered: want [%s], got %+v", decID, got)
	}

	// DISTINCT: decID has provenance to BOTH r1 and r2 → must appear exactly once.
	addProv(decID, r2)
	dd, err := s.Memories().ListMemoriesByRecords(ctx, scope, []string{r1, r2}, []string{"decision"})
	if err != nil {
		t.Fatalf("ListMemoriesByRecords(distinct): %v", err)
	}
	if len(dd) != 1 || dd[0].ID != decID {
		t.Fatalf("DISTINCT: a memory provenance-linked to 2 records must return once, got %+v", dd)
	}

	// no kind filter, records r1+r2: decID, factID, prefID (supID excluded by status).
	got2, err := s.Memories().ListMemoriesByRecords(ctx, scope, []string{r1, r2}, nil)
	if err != nil {
		t.Fatalf("ListMemoriesByRecords(all): %v", err)
	}
	ids := map[string]bool{}
	for _, m := range got2 {
		ids[m.ID] = true
	}
	if len(got2) != 3 || !ids[decID] || !ids[factID] || !ids[prefID] || ids[supID] {
		t.Fatalf("all-kinds: want {dec,fact,pref}, got %+v", got2)
	}

	// record with no provenance ⇒ empty; empty recordIDs ⇒ empty.
	if g, _ := s.Memories().ListMemoriesByRecords(ctx, scope, []string{r3}, nil); len(g) != 0 {
		t.Errorf("r3 should yield none, got %d", len(g))
	}
	if g, _ := s.Memories().ListMemoriesByRecords(ctx, scope, nil, nil); len(g) != 0 {
		t.Errorf("empty recordIDs should yield none, got %d", len(g))
	}

	// scope isolation: another tenant sees nothing for r1.
	other := tenantScope("t-other-" + newID())
	if g, _ := s.Memories().ListMemoriesByRecords(ctx, other, []string{r1}, nil); len(g) != 0 {
		t.Errorf("cross-tenant leak: %d", len(g))
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
	_, err = s.Memories().MemoriesTopics(ctx, zero, []string{"any-id"})
	assertScopeRequired(t, "Memories.MemoriesTopics", err)

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
	_, err = s.Injections().HubSignals(ctx, zero, []string{"any"}, 0)
	assertScopeRequired(t, "Injections.HubSignals", err)

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

// testMemoryFindNeighborsKindFilter asserts NeighborQuery.Kinds restricts the
// result to the named kinds — the store-layer enforcement the reflection
// write-side relies on so a strategy cannot supersede a fact (D-077 #5).
func testMemoryFindNeighborsKindFilter(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// A fact and a strategy that share an entity ("API-X").
	factID, stratID := newID(), newID()
	commit := func(id, kind string) {
		if err := s.Memories().Commit(ctx, scope, store.CommitSet{
			Action: store.ActionAdd,
			Memory: store.Memory{
				ID: id, Kind: kind, Content: kind + " about API-X",
				Status: "active", Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
				CreatedAt: nowMs(), UpdatedAt: nowMs(),
			},
			Entities: []string{"API-X"},
			Keywords: []string{"api"},
		}); err != nil {
			t.Fatalf("Commit %s: %v", kind, err)
		}
	}
	commit(factID, "fact")
	commit(stratID, "strategy")

	// No kind filter → both returned.
	all, err := s.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{Entities: []string{"API-X"}, Limit: 10})
	if err != nil {
		t.Fatalf("FindNeighbors (all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered: got %d neighbors, want 2", len(all))
	}

	// Kind filter to strategy/failure_mode → only the strategy.
	filtered, err := s.Memories().FindNeighbors(ctx, scope, store.NeighborQuery{
		Entities: []string{"API-X"}, Limit: 10, Kinds: []string{"strategy", "failure_mode"},
	})
	if err != nil {
		t.Fatalf("FindNeighbors (kind-filtered): %v", err)
	}
	if len(filtered) != 1 {
		t.Fatalf("kind-filtered: got %d neighbors, want 1", len(filtered))
	}
	if filtered[0].ID != stratID {
		t.Errorf("kind-filtered neighbor: got %q, want the strategy %q (a fact leaked through)", filtered[0].ID, stratID)
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
		Topics:   []string{"topic-auth", "topic-deploy"},
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
	if len(j.Topics) != 2 || j.Topics[0] != "topic-auth" || j.Topics[1] != "topic-deploy" {
		t.Errorf("Topics: got %v want [topic-auth topic-deploy]", j.Topics)
	}
	if len(j.Provenance) != 1 || j.Provenance[0].RecordID != recID {
		t.Errorf("Provenance: got %v want 1 row with RecordID=%q", j.Provenance, recID)
	}

	// MemoriesTopics batch reader (D-089): returns the memory's topics keyed by id.
	tm, err := s.Memories().MemoriesTopics(ctx, scope, []string{memID, "nonexistent"})
	if err != nil {
		t.Fatalf("MemoriesTopics: %v", err)
	}
	if got := tm[memID]; len(got) != 2 || got[0] != "topic-auth" {
		t.Errorf("MemoriesTopics[%s] = %v, want [topic-auth topic-deploy]", memID, got)
	}
	if _, ok := tm["nonexistent"]; ok {
		t.Errorf("MemoriesTopics returned an entry for a memory with no topics")
	}
	// Empty ids ⇒ empty map (no query).
	if em, err := s.Memories().MemoriesTopics(ctx, scope, nil); err != nil || len(em) != 0 {
		t.Errorf("MemoriesTopics(nil) = %v, %v; want empty", em, err)
	}
	// Cross-tenant isolation (P3): another tenant cannot read this memory's topics.
	other := tenantScope("t-other-" + newID())
	if om, err := s.Memories().MemoriesTopics(ctx, other, []string{memID}); err != nil || len(om) != 0 {
		t.Errorf("cross-tenant MemoriesTopics leak: %v, %v", om, err)
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

// testRecordCreatedAtsSince verifies the per-item ActivityTurns timestamp fetch
// returns the scope's record created_ats strictly newer than sinceMs, ASC, capped.
func testRecordCreatedAtsSince(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("createdats-" + ulid.Make().String())

	// Empty → empty slice.
	got, err := s.Records().RecordCreatedAtsSince(ctx, scope, 0, 100)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty store: got %d want 0", len(got))
	}

	base := int64(2_000_000_000_000)
	for i, ts := range []int64{base + 2000, base, base + 1000} { // inserted out of order
		rec := store.Record{ID: newID(), Role: "user", Content: fmt.Sprintf("m%d", i), CreatedAt: ts, OccurredAt: ts}
		if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
			t.Fatalf("Append[%d]: %v", i, err)
		}
	}

	// Since base-1 → all three, ASC.
	got, err = s.Records().RecordCreatedAtsSince(ctx, scope, base-1, 100)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(got) != 3 || got[0] != base || got[1] != base+1000 || got[2] != base+2000 {
		t.Fatalf("expected sorted [base, base+1000, base+2000], got %v", got)
	}

	// Strictly-after semantics: since base → 2.
	got, _ = s.Records().RecordCreatedAtsSince(ctx, scope, base, 100)
	if len(got) != 2 || got[0] != base+1000 {
		t.Fatalf("since base: got %v", got)
	}

	// Cap is honored.
	got, _ = s.Records().RecordCreatedAtsSince(ctx, scope, base-1, 2)
	if len(got) != 2 {
		t.Fatalf("cap=2: got %d", len(got))
	}

	// Scope isolation: a different scope sees none.
	other := tenantScope("createdats-other-" + ulid.Make().String())
	if g, _ := s.Records().RecordCreatedAtsSince(ctx, other, 0, 100); len(g) != 0 {
		t.Errorf("cross-scope leak: %d", len(g))
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

// --- Phase 14: lifecycle sweep store methods ---------------------------------

// testTenantsListing asserts Store.Tenants returns distinct tenant IDs (D-057).
func testTenantsListing(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	// Empty store → no tenants.
	tenants, err := s.Tenants(ctx)
	if err != nil {
		t.Fatalf("Tenants (empty): %v", err)
	}
	if len(tenants) != 0 {
		t.Errorf("expected 0 tenants, got %d: %v", len(tenants), tenants)
	}

	// Insert memories for two tenants (same tenant_id → should deduplicate).
	for _, tid := range []string{"tenant-a", "tenant-b", "tenant-a"} {
		scope := tenantScope(tid)
		mem := store.Memory{
			ID:          newID(),
			TenantID:    tid,
			Kind:        "fact",
			Content:     "conformance test",
			Status:      "active",
			Importance:  1,
			Confidence:  0.5,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			CreatedAt:   nowMs(),
			UpdatedAt:   nowMs(),
		}
		if err := s.Memories().Insert(ctx, scope, mem); err != nil {
			t.Fatalf("insert memory for tenant %q: %v", tid, err)
		}
	}

	tenants, err = s.Tenants(ctx)
	if err != nil {
		t.Fatalf("Tenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Errorf("expected 2 distinct tenants, got %d: %v", len(tenants), tenants)
	}
	tenantSet := map[string]bool{}
	for _, tid := range tenants {
		tenantSet[tid] = true
	}
	for _, expected := range []string{"tenant-a", "tenant-b"} {
		if !tenantSet[expected] {
			t.Errorf("expected tenant %q in result", expected)
		}
	}

	// A tenant with only RECORDS (no memories) must also appear — the reflection
	// sweep operates on outcome-tagged records before any memory exists (D-077).
	recScope := tenantScope("tenant-records-only")
	if err := s.Records().Append(ctx, recScope, []store.Record{
		{ID: newID(), Role: "tool", Content: "did a thing", Outcome: "success", OccurredAt: nowMs(), CreatedAt: nowMs()},
	}); err != nil {
		t.Fatalf("append record-only tenant: %v", err)
	}
	tenants, err = s.Tenants(ctx)
	if err != nil {
		t.Fatalf("Tenants (with records): %v", err)
	}
	rset := map[string]bool{}
	for _, tid := range tenants {
		rset[tid] = true
	}
	if !rset["tenant-records-only"] {
		t.Errorf("record-only tenant missing from Tenants(): %v", tenants)
	}
}

// testMemoryListActiveForDecay asserts ListActiveForDecay returns only active
// memories, paginates correctly, and is tenant-isolated.
func testMemoryListActiveForDecay(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	scope := tenantScope("decay-scope-a")
	other := tenantScope("decay-scope-b")

	insertActive := func(tid string, sc identity.Scope) string {
		now := nowMs()
		mem := store.Memory{
			ID:          newID(),
			TenantID:    tid,
			Kind:        "fact",
			Content:     "active memory " + newID(),
			Status:      "active",
			Importance:  1,
			Confidence:  0.5,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.Memories().Insert(ctx, sc, mem); err != nil {
			t.Fatalf("insert active memory: %v", err)
		}
		return mem.ID
	}

	// Insert 3 active memories in scope, 1 expired, 1 in other scope.
	id1 := insertActive(scope.Tenant, scope)
	id2 := insertActive(scope.Tenant, scope)
	id3 := insertActive(scope.Tenant, scope)
	expiredID := insertActive(scope.Tenant, scope)
	insertActive(other.Tenant, other)

	// Set one to expired.
	if err := s.Memories().SetStatus(ctx, scope, expiredID, "expired", nowMs()); err != nil {
		t.Fatalf("SetStatus expired: %v", err)
	}

	// List active for decay — should return exactly 3.
	mems, nextCursor, err := s.Memories().ListActiveForDecay(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("ListActiveForDecay: %v", err)
	}
	if len(mems) != 3 {
		t.Errorf("expected 3 active memories, got %d", len(mems))
	}
	if nextCursor != "" {
		t.Errorf("expected empty cursor, got %q", nextCursor)
	}
	ids := map[string]bool{}
	for _, m := range mems {
		ids[m.ID] = true
		if m.Status != "active" {
			t.Errorf("expected status=active, got %q for %q", m.Status, m.ID)
		}
	}
	for _, id := range []string{id1, id2, id3} {
		if !ids[id] {
			t.Errorf("expected ID %q in results", id)
		}
	}
	if ids[expiredID] {
		t.Error("expired memory should not appear in ListActiveForDecay")
	}

	// Pagination: limit=2, then cursor.
	page1, cursor1, err := s.Memories().ListActiveForDecay(ctx, scope, 2, "")
	if err != nil {
		t.Fatalf("ListActiveForDecay page1: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("expected 2 on page1, got %d", len(page1))
	}
	if cursor1 == "" {
		t.Error("expected non-empty cursor after page1")
	}

	page2, cursor2, err := s.Memories().ListActiveForDecay(ctx, scope, 2, cursor1)
	if err != nil {
		t.Fatalf("ListActiveForDecay page2: %v", err)
	}
	if len(page2) != 1 {
		t.Errorf("expected 1 on page2, got %d", len(page2))
	}
	if cursor2 != "" {
		t.Errorf("expected empty cursor after last page, got %q", cursor2)
	}

	// Scope isolation — empty-tenant rejected.
	_, _, err = s.Memories().ListActiveForDecay(ctx, identity.Scope{}, 10, "")
	if err == nil {
		t.Error("expected error for empty tenant scope")
	}
}

// testMemorySetValidUntil asserts SetValidUntil sets and clears valid_until.
func testMemorySetValidUntil(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	scope := tenantScope("valid-until-scope")
	now := nowMs()
	mem := store.Memory{
		ID:          newID(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     "setvaliduntil test",
		Status:      "active",
		Importance:  1,
		Confidence:  0.5,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Set valid_until to future timestamp.
	future := now + 3_600_000 // +1 hour
	if err := s.Memories().SetValidUntil(ctx, scope, mem.ID, future); err != nil {
		t.Fatalf("SetValidUntil: %v", err)
	}
	got, err := s.Memories().Get(ctx, scope, mem.ID)
	if err != nil {
		t.Fatalf("Get after SetValidUntil: %v", err)
	}
	if got.ValidUntil != future {
		t.Errorf("ValidUntil: got %d, want %d", got.ValidUntil, future)
	}

	// Clear valid_until with 0.
	if err := s.Memories().SetValidUntil(ctx, scope, mem.ID, 0); err != nil {
		t.Fatalf("SetValidUntil(0): %v", err)
	}
	got, err = s.Memories().Get(ctx, scope, mem.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if got.ValidUntil != 0 {
		t.Errorf("expected ValidUntil=0 after clear, got %d", got.ValidUntil)
	}

	// Empty tenant rejected.
	err = s.Memories().SetValidUntil(ctx, identity.Scope{}, mem.ID, future)
	if err == nil {
		t.Error("expected error for empty tenant scope")
	}
}

// --- Phase 22: episodes + distinct sessions -----------------------------------

func testEpisodeCRUD(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	ep := store.Episode{
		ID: newID(), SessionID: "sess-1", Title: "draft", Status: "closed",
		StartedAt: 1000, EndedAt: 2000, Outcome: "success",
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Episodes().CreateEpisode(ctx, scope, ep); err != nil {
		t.Fatalf("CreateEpisode: %v", err)
	}
	got, err := s.Episodes().GetEpisode(ctx, scope, ep.ID)
	if err != nil {
		t.Fatalf("GetEpisode: %v", err)
	}
	if got.SessionID != "sess-1" || got.Status != "closed" || got.Outcome != "success" {
		t.Errorf("episode round-trip wrong: %+v", got)
	}
	bySess, err := s.Episodes().GetEpisodeBySession(ctx, scope, "sess-1")
	if err != nil || bySess.ID != ep.ID {
		t.Errorf("GetEpisodeBySession: %v / %+v", err, bySess)
	}
	if _, err := s.Episodes().GetEpisodeBySession(ctx, scope, "no-such"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetEpisodeBySession(absent) = %v, want ErrNotFound", err)
	}
	// Attach a narrative.
	if err := s.Episodes().SetEpisodeNarrative(ctx, scope, ep.ID, "mem-narr-1", "March deploy", nowMs()); err != nil {
		t.Fatalf("SetEpisodeNarrative: %v", err)
	}
	got2, _ := s.Episodes().GetEpisode(ctx, scope, ep.ID)
	if got2.NarrativeMemoryID != "mem-narr-1" || got2.Title != "March deploy" {
		t.Errorf("narrative not attached: %+v", got2)
	}
	// List.
	eps, _, err := s.Episodes().ListEpisodes(ctx, scope, 10, "")
	if err != nil || len(eps) != 1 {
		t.Errorf("ListEpisodes: %v / %d", err, len(eps))
	}
	if _, err := s.Episodes().GetEpisode(ctx, scope, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetEpisode(missing) = %v, want ErrNotFound", err)
	}
}

func testEpisodeNeedingNarrative(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	narrated := store.Episode{ID: newID(), SessionID: "s-a", Status: "closed", StartedAt: 100, NarrativeMemoryID: "m-1", CreatedAt: nowMs(), UpdatedAt: nowMs()}
	pending := store.Episode{ID: newID(), SessionID: "s-b", Status: "closed", StartedAt: 200, CreatedAt: nowMs(), UpdatedAt: nowMs()}
	if err := s.Episodes().CreateEpisode(ctx, scope, narrated); err != nil {
		t.Fatalf("create narrated: %v", err)
	}
	if err := s.Episodes().CreateEpisode(ctx, scope, pending); err != nil {
		t.Fatalf("create pending: %v", err)
	}
	need, err := s.Episodes().ListEpisodesNeedingNarrative(ctx, 10)
	if err != nil {
		t.Fatalf("ListEpisodesNeedingNarrative: %v", err)
	}
	found := false
	for _, e := range need {
		if e.ID == narrated.ID {
			t.Error("narrated episode leaked into needing-narrative list")
		}
		if e.ID == pending.ID {
			found = true
		}
	}
	if !found {
		t.Error("pending episode missing from needing-narrative list")
	}
}

func testEpisodeScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	a := tenantScope("t-" + newID())
	b := tenantScope("t-" + newID())
	ep := store.Episode{ID: newID(), SessionID: "s1", Status: "closed", StartedAt: 1, CreatedAt: nowMs(), UpdatedAt: nowMs()}
	if err := s.Episodes().CreateEpisode(ctx, a, ep); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.Episodes().GetEpisode(ctx, b, ep.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-scope GetEpisode = %v, want ErrNotFound", err)
	}
	eps, _, _ := s.Episodes().ListEpisodes(ctx, b, 10, "")
	if len(eps) != 0 {
		t.Errorf("cross-scope ListEpisodes returned %d", len(eps))
	}
	if err := s.Episodes().CreateEpisode(ctx, identity.Scope{}, ep); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("CreateEpisode(empty scope) = %v, want ErrScopeRequired", err)
	}
}

func testRecordDistinctSessions(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Two session-scoped record sets + one sessionless record.
	s1 := identity.Scope{Tenant: scope.Tenant, Project: "p", User: "u", Session: "s1"}
	s2 := identity.Scope{Tenant: scope.Tenant, Project: "p", User: "u", Session: "s2"}
	mk := func(id string, occ int64) store.Record {
		return store.Record{ID: id, BranchID: "main", Role: "user", Content: "x", OccurredAt: occ, CreatedAt: occ}
	}
	if err := s.Records().Append(ctx, s1, []store.Record{mk(newID(), 100), mk(newID(), 200)}); err != nil {
		t.Fatalf("append s1: %v", err)
	}
	if err := s.Records().Append(ctx, s2, []store.Record{mk(newID(), 5000)}); err != nil {
		t.Fatalf("append s2: %v", err)
	}
	if err := s.Records().Append(ctx, scope, []store.Record{mk(newID(), 300)}); err != nil { // sessionless
		t.Fatalf("append sessionless: %v", err)
	}

	// idleBefore = 1000 → only s1 (last=200) is "closed"; s2 (last=5000) and the
	// sessionless record are excluded.
	got, err := s.Records().DistinctSessions(ctx, scope, 1000, 10)
	if err != nil {
		t.Fatalf("DistinctSessions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 closed session, got %d: %+v", len(got), got)
	}
	if got[0].SessionID != "s1" || got[0].RecordCount != 2 || got[0].FirstOccurred != 100 || got[0].LastOccurred != 200 {
		t.Errorf("session info wrong: %+v", got[0])
	}
	// Project/User are carried so episodes are created at the full scope (P3).
	if got[0].ProjectID != "p" || got[0].UserID != "u" {
		t.Errorf("session info missing project/user: %+v", got[0])
	}

	// A wider idle window includes both real sessions (never the sessionless one).
	got2, _ := s.Records().DistinctSessions(ctx, scope, 10000, 10)
	if len(got2) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(got2))
	}

	// Scope isolation + fail-closed.
	other := tenantScope("t-" + newID())
	iso, _ := s.Records().DistinctSessions(ctx, other, 10000, 10)
	if len(iso) != 0 {
		t.Errorf("cross-scope DistinctSessions returned %d", len(iso))
	}
	if _, err := s.Records().DistinctSessions(ctx, identity.Scope{}, 10000, 10); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("DistinctSessions(empty) = %v, want ErrScopeRequired", err)
	}
}

// --- SuggestionStore + ScopeSettingsStore (Phase 27, D-087) -----------------

func testSuggestionLifecycle(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())
	sess := "sess-" + newID()

	// Empty create is a no-op, not an error.
	if err := s.Suggestions().Create(ctx, scope, nil); err != nil {
		t.Fatalf("empty create: %v", err)
	}

	id := newID()
	if err := s.Suggestions().Create(ctx, scope, []store.Suggestion{{
		ID: id, SessionID: sess, TriggerKind: "recent_episode", MemoryID: "m1", EpisodeID: "e1",
		Status: "pending", CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// ListBySession with status="" (any) returns the pending row too.
	if any, err := s.Suggestions().ListBySession(ctx, scope, sess, "", 10); err != nil || len(any) != 1 {
		t.Fatalf("list any-status: %v / %d", err, len(any))
	}
	// Idempotent create (duplicate id ignored).
	if err := s.Suggestions().Create(ctx, scope, []store.Suggestion{{ID: id, SessionID: sess, TriggerKind: "recent_episode", Status: "pending", CreatedAt: nowMs(), UpdatedAt: nowMs()}}); err != nil {
		t.Fatalf("create dup: %v", err)
	}

	// ListBySession (pending).
	got, err := s.Suggestions().ListBySession(ctx, scope, sess, "pending", 10)
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatalf("list pending: %v / %+v", err, got)
	}

	// Resolve accept (CAS pending→accepted, counter).
	res, err := s.Suggestions().Resolve(ctx, scope, id, "accept", nowMs())
	if err != nil || res.Status != "accepted" || res.AcceptCount != 1 {
		t.Fatalf("accept: %v / %+v", err, res)
	}
	// Double-resolve ⇒ ErrNotPending (no double count).
	if _, err := s.Suggestions().Resolve(ctx, scope, id, "dismiss", nowMs()); !errors.Is(err, store.ErrNotPending) {
		t.Errorf("double-resolve should be ErrNotPending, got %v", err)
	}
	// CountByTrigger reflects the accept (all-time, since=0).
	acc, dis, err := s.Suggestions().CountByTrigger(ctx, scope, "recent_episode", 0)
	if err != nil || acc != 1 || dis != 0 {
		t.Errorf("count: %v acc=%d dis=%d", err, acc, dis)
	}
	// Windowed CountByTrigger: a `since` past the resolve's updated_at excludes it.
	accW, disW, _ := s.Suggestions().CountByTrigger(ctx, scope, "recent_episode", nowMs()+1_000_000)
	if accW != 0 || disW != 0 {
		t.Errorf("windowed count should exclude old feedback, got acc=%d dis=%d", accW, disW)
	}

	// Expiry: a second pending suggestion expired by the sweep path; ExpirePending
	// returns the ids it actually transitioned.
	id2 := newID()
	_ = s.Suggestions().Create(ctx, scope, []store.Suggestion{{ID: id2, SessionID: sess, TriggerKind: "expiring", MemoryID: "m2", Status: "pending", CreatedAt: 100, UpdatedAt: 100}})
	pend, _ := s.Suggestions().ListPendingBefore(ctx, scope, 200, 10)
	ids := make([]string, 0, len(pend))
	for _, p := range pend {
		ids = append(ids, p.ID)
	}
	expired, eerr := s.Suggestions().ExpirePending(ctx, scope, ids, nowMs())
	if eerr != nil {
		t.Fatalf("expire: %v", eerr)
	}
	if len(expired) != 1 || expired[0] != id2 {
		t.Errorf("ExpirePending should report exactly the transitioned id, got %v", expired)
	}
	g2, _ := s.Suggestions().Get(ctx, scope, id2)
	if g2.Status != "expired" {
		t.Errorf("suggestion should be expired, got %q", g2.Status)
	}
	// Re-expiring an already-expired id transitions nothing.
	reexp, _ := s.Suggestions().ExpirePending(ctx, scope, []string{id2}, nowMs())
	if len(reexp) != 0 {
		t.Errorf("re-expire should report nothing, got %v", reexp)
	}
	// Get missing ⇒ ErrNotFound.
	if _, err := s.Suggestions().Get(ctx, scope, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("get missing = %v, want ErrNotFound", err)
	}
}

// testSuggestionScopeUserIsolation proves Get/Resolve enforce FULL scope (P3): a
// caller scoped to a different user within the same tenant cannot read or resolve
// another user's offer.
func testSuggestionScopeUserIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "tu-" + newID()
	alice := identity.Scope{Tenant: tenant, User: "alice"}
	bob := identity.Scope{Tenant: tenant, User: "bob"}

	id := newID()
	if err := s.Suggestions().Create(ctx, alice, []store.Suggestion{{ID: id, SessionID: "s", TriggerKind: "expiring", MemoryID: "m1", Status: "pending", CreatedAt: nowMs(), UpdatedAt: nowMs()}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Bob cannot Get alice's offer.
	if _, err := s.Suggestions().Get(ctx, bob, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-user Get should be ErrNotFound, got %v", err)
	}
	// Bob cannot Resolve alice's offer (ErrNotPending — the CAS matches no row in bob's scope).
	if _, err := s.Suggestions().Resolve(ctx, bob, id, "accept", nowMs()); !errors.Is(err, store.ErrNotPending) {
		t.Errorf("cross-user Resolve should be ErrNotPending, got %v", err)
	}
	// Alice still can (full scope matches).
	if _, err := s.Suggestions().Resolve(ctx, alice, id, "accept", nowMs()); err != nil {
		t.Errorf("same-user Resolve should succeed, got %v", err)
	}
}

func testSuggestionScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	a := tenantScope("ta-" + newID())
	b := tenantScope("tb-" + newID())
	id := newID()
	_ = s.Suggestions().Create(ctx, a, []store.Suggestion{{ID: id, SessionID: "s", TriggerKind: "recent_episode", Status: "pending", CreatedAt: nowMs(), UpdatedAt: nowMs()}})
	// Tenant b sees nothing and cannot resolve a's suggestion.
	if got, _ := s.Suggestions().ListBySession(ctx, b, "s", "", 10); len(got) != 0 {
		t.Errorf("cross-tenant suggestion leak: %d", len(got))
	}
	if _, err := s.Suggestions().Resolve(ctx, b, id, "accept", nowMs()); err == nil {
		t.Error("cross-tenant resolve should fail")
	}
}

func testScopeSettingsKV(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "tk-" + newID(), User: "u1"}

	// Absent ⇒ found=false.
	if _, found, err := s.ScopeSettings().Get(ctx, scope, "proactive"); err != nil || found {
		t.Fatalf("absent get: found=%v err=%v", found, err)
	}
	// Set then Get.
	if err := s.ScopeSettings().Set(ctx, scope, "proactive", `{"enabled":true}`, nowMs()); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, found, err := s.ScopeSettings().Get(ctx, scope, "proactive")
	if err != nil || !found || v != `{"enabled":true}` {
		t.Fatalf("get: %q found=%v err=%v", v, found, err)
	}
	// Upsert (same scope+key) replaces, does not duplicate.
	_ = s.ScopeSettings().Set(ctx, scope, "proactive", `{"enabled":false}`, nowMs()+1)
	v2, _, _ := s.ScopeSettings().Get(ctx, scope, "proactive")
	if v2 != `{"enabled":false}` {
		t.Errorf("upsert should replace, got %q", v2)
	}
	if m, _ := s.ScopeSettings().List(ctx, scope); len(m) != 1 {
		t.Errorf("list should have exactly 1 key, got %d", len(m))
	}
	// A DIFFERENT scope (tenant-only) is a distinct row.
	tenantOnly := identity.Scope{Tenant: scope.Tenant}
	_ = s.ScopeSettings().Set(ctx, tenantOnly, "proactive", `{"enabled":true}`, nowMs())
	tv, _, _ := s.ScopeSettings().Get(ctx, tenantOnly, "proactive")
	if tv != `{"enabled":true}` {
		t.Errorf("tenant-scope row should be independent of user-scope, got %q", tv)
	}
	// Delete.
	if err := s.ScopeSettings().Delete(ctx, scope, "proactive"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, _ := s.ScopeSettings().Get(ctx, scope, "proactive"); found {
		t.Error("deleted key should be absent")
	}
	// Cross-tenant isolation.
	other := identity.Scope{Tenant: "tk-other-" + newID()}
	if _, found, _ := s.ScopeSettings().Get(ctx, other, "proactive"); found {
		t.Error("cross-tenant settings leak")
	}

	// A scope without a tenant is rejected on every entry point (P3 — no unscoped
	// query). Get/Set/List/Delete all fail closed.
	noTenant := identity.Scope{User: "u"}
	if _, _, err := s.ScopeSettings().Get(ctx, noTenant, "proactive"); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("Get no-tenant = %v, want ErrScopeRequired", err)
	}
	if err := s.ScopeSettings().Set(ctx, noTenant, "proactive", "{}", nowMs()); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("Set no-tenant = %v, want ErrScopeRequired", err)
	}
	if _, err := s.ScopeSettings().List(ctx, noTenant); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("List no-tenant = %v, want ErrScopeRequired", err)
	}
	if err := s.ScopeSettings().Delete(ctx, noTenant, "proactive"); !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("Delete no-tenant = %v, want ErrScopeRequired", err)
	}
}

// testMemoryDistinctScopes verifies DistinctScopes returns each (project,user) with active
// memories under a tenant, never crossing users, and is itself scope-isolated (D-111).
func testMemoryDistinctScopes(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "tenant-ds-" + newID()
	alice := identity.Scope{Tenant: tenant, User: "alice"}
	bob := identity.Scope{Tenant: tenant, User: "bob"}
	mk := func(sc identity.Scope, content string) {
		m := store.Memory{ID: newID(), Kind: "fact", Content: content, Status: "active",
			Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs()}
		if err := s.Memories().Insert(ctx, sc, m); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	mk(alice, "alice fact 1")
	mk(alice, "alice fact 2")
	mk(bob, "bob fact")

	scopes, err := s.Memories().DistinctScopes(ctx, tenantScope(tenant))
	if err != nil {
		t.Fatalf("DistinctScopes: %v", err)
	}
	users := map[string]bool{}
	for _, sc := range scopes {
		if sc.Tenant != tenant {
			t.Errorf("scope tenant = %q, want %q", sc.Tenant, tenant)
		}
		users[sc.User] = true
	}
	if len(scopes) != 2 || !users["alice"] || !users["bob"] {
		t.Errorf("DistinctScopes = %+v, want exactly alice + bob", scopes)
	}
	// Scope isolation: querying alice's scope returns only alice.
	aScopes, err := s.Memories().DistinctScopes(ctx, alice)
	if err != nil {
		t.Fatalf("DistinctScopes(alice): %v", err)
	}
	for _, sc := range aScopes {
		if sc.User != "alice" {
			t.Errorf("alice-scoped DistinctScopes leaked user %q", sc.User)
		}
	}
}

// testMemoryListActiveInScope pins the EXACT-leaf semantics the dedupe sweep relies on
// (D-111 / 29d B1): an empty project/user leaf matches IS NULL, never wildcards across
// sub-scopes. Without this, a per-user pass would seed candidates from every user and a
// merge could cross users (P3 + P1). Also asserts the DistinctScopes → ListActiveInScope
// round-trip: a returned bucket fed back returns ONLY that bucket's rows.
func testMemoryListActiveInScope(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	tenant := "tenant-lais-" + newID()
	alice := identity.Scope{Tenant: tenant, User: "alice"}
	bob := identity.Scope{Tenant: tenant, User: "bob"}
	tenantLevel := identity.Scope{Tenant: tenant}                 // project NULL, user NULL
	projectLevel := identity.Scope{Tenant: tenant, Project: "px"} // user NULL

	mk := func(sc identity.Scope, content string) {
		m := store.Memory{ID: newID(), Kind: "fact", Content: content, Status: "active",
			Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: nowMs(), UpdatedAt: nowMs()}
		if err := s.Memories().Insert(ctx, sc, m); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	mk(alice, "alice fact")
	mk(bob, "bob fact")
	mk(tenantLevel, "tenant fact")
	mk(projectLevel, "project fact")

	contentsIn := func(sc identity.Scope) map[string]bool {
		got, _, err := s.Memories().ListActiveInScope(ctx, sc, 100, "")
		if err != nil {
			t.Fatalf("ListActiveInScope(%s): %v", sc.String(), err)
		}
		out := map[string]bool{}
		for _, m := range got {
			out[m.Content] = true
		}
		return out
	}

	// Exact-leaf isolation: each partition sees ONLY its own row.
	if c := contentsIn(alice); !c["alice fact"] || c["bob fact"] || c["tenant fact"] || c["project fact"] || len(c) != 1 {
		t.Errorf("alice partition = %v, want only {alice fact}", c)
	}
	// Tenant-only scope = the NULL-leaf partition: only the tenant-level row, NOT alice/bob/project.
	if c := contentsIn(tenantLevel); !c["tenant fact"] || c["alice fact"] || c["bob fact"] || c["project fact"] || len(c) != 1 {
		t.Errorf("tenant-level (NULL-leaf) partition = %v, want only {tenant fact}", c)
	}
	// Project-level scope (user NULL): only the project-level row, NOT alice/bob.
	if c := contentsIn(projectLevel); !c["project fact"] || c["alice fact"] || c["bob fact"] || len(c) != 1 {
		t.Errorf("project-level partition = %v, want only {project fact}", c)
	}

	// DistinctScopes → ListActiveInScope round-trip: every returned bucket is exact.
	scopes, err := s.Memories().DistinctScopes(ctx, tenantScope(tenant))
	if err != nil {
		t.Fatalf("DistinctScopes: %v", err)
	}
	total := 0
	for _, sc := range scopes {
		got, _, err := s.Memories().ListActiveInScope(ctx, sc, 100, "")
		if err != nil {
			t.Fatalf("ListActiveInScope(%s): %v", sc.String(), err)
		}
		if len(got) == 0 {
			t.Errorf("bucket %s returned no rows via ListActiveInScope", sc.String())
		}
		for _, m := range got {
			if m.UserID != sc.User || m.ProjectID != sc.Project {
				t.Errorf("bucket %s leaked a row scoped (project=%q,user=%q)", sc.String(), m.ProjectID, m.UserID)
			}
		}
		total += len(got)
	}
	if total != 4 {
		t.Errorf("round-trip covered %d rows, want all 4", total)
	}
}
