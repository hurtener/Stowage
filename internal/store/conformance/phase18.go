package conformance

// Phase 18 conformance tests: ListBySubject, GetByContentHashStatus,
// ActionRollback, ActionConfirm, and cross-scope rollback unconstructibility.
// All tests run on both SQLite and Postgres drivers (D-064, D-065).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/oklog/ulid/v2"
)

// RunPhase18 registers all Phase 18 conformance tests.
// Called by conformance.Run via the shared test runner.
func RunPhase18(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("EventListBySubject", func(t *testing.T) { testEventListBySubject(t, factory) })
	t.Run("EventListBySubjectEmpty", func(t *testing.T) { testEventListBySubjectEmpty(t, factory) })
	t.Run("EventListBySubjectNewestFirst", func(t *testing.T) { testEventListBySubjectNewestFirst(t, factory) })
	t.Run("EventListBySubjectScopeRequired", func(t *testing.T) { testEventListBySubjectScopeRequired(t, factory) })
	t.Run("EventListBySubjectScopeIsolation", func(t *testing.T) { testEventListBySubjectScopeIsolation(t, factory) })
	t.Run("GetByContentHashStatus", func(t *testing.T) { testGetByContentHashStatus(t, factory) })
	t.Run("GetByContentHashStatusNotFound", func(t *testing.T) { testGetByContentHashStatusNotFound(t, factory) })
	t.Run("GetByContentHashStatusScopeIsolation", func(t *testing.T) { testGetByContentHashStatusScopeIsolation(t, factory) })
	t.Run("ListSupersededBy", func(t *testing.T) { testListSupersededBy(t, factory) })
	t.Run("ListSupersededByEmpty", func(t *testing.T) { testListSupersededByEmpty(t, factory) })
	t.Run("ListSupersededByScopeIsolation", func(t *testing.T) { testListSupersededByScopeIsolation(t, factory) })
	t.Run("CommitRollbackUpdate", func(t *testing.T) { testCommitRollbackUpdate(t, factory) })
	t.Run("CommitRollbackSupersede", func(t *testing.T) { testCommitRollbackSupersede(t, factory) })
	t.Run("CommitRollbackTombstone", func(t *testing.T) { testCommitRollbackTombstone(t, factory) })
	t.Run("CommitRollbackJunctionsReplaced", func(t *testing.T) { testCommitRollbackJunctionsReplaced(t, factory) })
	t.Run("CommitRollbackExtraMemory", func(t *testing.T) { testCommitRollbackExtraMemory(t, factory) })
	t.Run("CommitRollbackFaultHook", func(t *testing.T) { testCommitRollbackFaultHook(t, factory) })
	t.Run("CommitConfirmPromote", func(t *testing.T) { testCommitConfirmPromote(t, factory) })
	t.Run("CommitConfirmActivateNoTarget", func(t *testing.T) { testCommitConfirmActivateNoTarget(t, factory) })
	t.Run("CommitConfirmFaultHook", func(t *testing.T) { testCommitConfirmFaultHook(t, factory) })
	t.Run("CrossScopeRollbackUnconstructible", func(t *testing.T) { testCrossScopeRollbackUnconstructible(t, factory) })
	t.Run("RollbackBumpsGeneration", func(t *testing.T) { testRollbackBumpsGeneration(t, factory) })
	t.Run("AppliedMigrationsPhase18", func(t *testing.T) { testAppliedMigrationsPhase18(t, factory) })
}

// --- EventStore.ListBySubject ------------------------------------------------

func testEventListBySubject(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())
	subj := newID()

	// Emit two events for the subject and one for a different subject.
	for i, typ := range []string{"memory.added", "memory.updated"} {
		ev := store.Event{
			ID:        newID(),
			Type:      typ,
			SubjectID: subj,
			Payload:   "{}",
			CreatedAt: nowMs() + int64(i),
		}
		if err := s.Events().Emit(ctx, scope, ev); err != nil {
			t.Fatalf("Emit %d: %v", i, err)
		}
	}
	// Different subject — must not appear.
	other := store.Event{ID: newID(), Type: "memory.added", SubjectID: newID(), Payload: "{}", CreatedAt: nowMs()}
	if err := s.Events().Emit(ctx, scope, other); err != nil {
		t.Fatalf("Emit other: %v", err)
	}

	got, err := s.Events().ListBySubject(ctx, scope, subj, 10)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events want 2", len(got))
	}
	for _, ev := range got {
		if ev.SubjectID != subj {
			t.Errorf("wrong subject_id %q", ev.SubjectID)
		}
	}
}

func testEventListBySubjectEmpty(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	got, err := s.Events().ListBySubject(ctx, scope, "no-such-subject", 10)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

func testEventListBySubjectNewestFirst(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())
	subj := newID()
	base := nowMs()

	for i := 0; i < 5; i++ {
		ev := store.Event{
			ID:        newID(),
			Type:      "memory.added",
			SubjectID: subj,
			Payload:   "{}",
			CreatedAt: base + int64(i),
		}
		if err := s.Events().Emit(ctx, scope, ev); err != nil {
			t.Fatalf("Emit %d: %v", i, err)
		}
	}

	got, err := s.Events().ListBySubject(ctx, scope, subj, 10)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d want 5", len(got))
	}
	// Newest first: each event's created_at should be >= the next.
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt > got[i-1].CreatedAt {
			t.Errorf("not newest-first at index %d: %d > %d", i, got[i].CreatedAt, got[i-1].CreatedAt)
		}
	}
}

func testEventListBySubjectScopeRequired(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	_, err := s.Events().ListBySubject(ctx, identity.Scope{}, "x", 10)
	if !errors.Is(err, store.ErrScopeRequired) {
		t.Errorf("expected ErrScopeRequired, got %v", err)
	}
}

func testEventListBySubjectScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("A-" + newID())
	scopeB := tenantScope("B-" + newID())
	subj := newID()

	ev := store.Event{ID: newID(), Type: "memory.added", SubjectID: subj, Payload: "{}", CreatedAt: nowMs()}
	if err := s.Events().Emit(ctx, scopeA, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got, err := s.Events().ListBySubject(ctx, scopeB, subj, 10)
	if err != nil {
		t.Fatalf("ListBySubject scopeB: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cross-tenant isolation violated: got %d events", len(got))
	}
}

// --- MemoryStore.GetByContentHashStatus -------------------------------------

func testGetByContentHashStatus(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	hash := ulid.Make().String() // deterministic unique hash
	mem := store.Memory{
		ID:          newID(),
		Kind:        "fact",
		Content:     "parked memory",
		Status:      "pending_confirmation",
		Importance:  3,
		Confidence:  0.7,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: hash,
		CreatedAt:   nowMs(),
		UpdatedAt:   nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Memories().GetByContentHashStatus(ctx, scope, hash, "pending_confirmation")
	if err != nil {
		t.Fatalf("GetByContentHashStatus: %v", err)
	}
	if got.ID != mem.ID {
		t.Errorf("ID: got %q want %q", got.ID, mem.ID)
	}

	// Active query on the same hash must not find the parked row.
	_, err = s.Memories().GetByContentHash(ctx, scope, hash)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetByContentHash should return ErrNotFound for pending row, got %v", err)
	}
}

func testGetByContentHashStatusNotFound(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	_, err := s.Memories().GetByContentHashStatus(ctx, scope, "no-such-hash", "pending_confirmation")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func testGetByContentHashStatusScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("A-" + newID())
	scopeB := tenantScope("B-" + newID())
	hash := ulid.Make().String()

	mem := store.Memory{
		ID:          newID(),
		Kind:        "fact",
		Content:     "secret",
		Status:      "pending_confirmation",
		Importance:  3,
		Confidence:  0.7,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: hash,
		CreatedAt:   nowMs(),
		UpdatedAt:   nowMs(),
	}
	if err := s.Memories().Insert(ctx, scopeA, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	_, err := s.Memories().GetByContentHashStatus(ctx, scopeB, hash, "pending_confirmation")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant isolation violated: got %v", err)
	}
}

// --- ActionRollback conformance ---------------------------------------------

// testCommitRollbackUpdate round-trips an update rollback: seed → update → rollback → golden.
func testCommitRollbackUpdate(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Seed an active memory.
	seed := store.Memory{
		ID:          newID(),
		Kind:        "fact",
		Content:     "original content",
		Status:      "active",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: ulid.Make().String(),
		CreatedAt:   nowMs(),
		UpdatedAt:   nowMs(),
	}
	cs := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   seed,
		Entities: []string{"entity-A"},
		Keywords: []string{"kw-A"},
		Queries:  []string{"q-A"},
		Events:   []store.Event{{ID: newID(), Type: "memory.added", SubjectID: seed.ID, Payload: "{}", CreatedAt: nowMs()}},
		Scope:    scope,
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	// Simulate an update (change content).
	updated := seed
	updated.Content = "updated content"
	updated.ContentHash = ulid.Make().String()
	updated.UpdatedAt = nowMs() + 1
	ucSet := store.CommitSet{
		Action:   store.ActionUpdate,
		Memory:   updated,
		Entities: []string{"entity-B"},
		Keywords: []string{"kw-B"},
		Queries:  []string{"q-B"},
		Events:   []store.Event{{ID: newID(), Type: "memory.updated", SubjectID: updated.ID, Payload: `{"content":"original content"}`, CreatedAt: nowMs() + 1}},
		Scope:    scope,
	}
	if err := s.Memories().Commit(ctx, scope, ucSet); err != nil {
		t.Fatalf("update commit: %v", err)
	}

	// Rollback to seed state.
	rbSet := store.CommitSet{
		Action:   store.ActionRollback,
		Memory:   seed,
		Entities: []string{"entity-A"},
		Keywords: []string{"kw-A"},
		Queries:  []string{"q-A"},
		Events:   []store.Event{{ID: newID(), Type: "memory.rolled_back", SubjectID: seed.ID, Payload: "{}", CreatedAt: nowMs() + 2}},
		Scope:    scope,
	}
	if err := s.Memories().Commit(ctx, scope, rbSet); err != nil {
		t.Fatalf("rollback commit: %v", err)
	}

	// Verify restored state matches seed.
	got, err := s.Memories().Get(ctx, scope, seed.ID)
	if err != nil {
		t.Fatalf("Get after rollback: %v", err)
	}
	if got.Content != seed.Content {
		t.Errorf("content: got %q want %q", got.Content, seed.Content)
	}
	if got.ContentHash != seed.ContentHash {
		t.Errorf("content_hash: got %q want %q", got.ContentHash, seed.ContentHash)
	}
	if got.Status != "active" {
		t.Errorf("status: got %q want active", got.Status)
	}

	// Junctions should be restored to entity-A / kw-A / q-A.
	jt, err := s.Memories().GetJunctions(ctx, scope, seed.ID)
	if err != nil {
		t.Fatalf("GetJunctions: %v", err)
	}
	if len(jt.Entities) != 1 || jt.Entities[0] != "entity-A" {
		t.Errorf("entities after rollback: %v", jt.Entities)
	}
	if len(jt.Keywords) != 1 || jt.Keywords[0] != "kw-A" {
		t.Errorf("keywords after rollback: %v", jt.Keywords)
	}
}

// testCommitRollbackSupersede tests rollback of a supersede op.
func testCommitRollbackSupersede(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Seed the target.
	target := store.Memory{
		ID:          newID(),
		Kind:        "fact",
		Content:     "old fact",
		Status:      "active",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: ulid.Make().String(),
		CreatedAt:   nowMs(),
		UpdatedAt:   nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, target); err != nil {
		t.Fatalf("Insert target: %v", err)
	}

	// Supersede: new memory, old target becomes superseded.
	newMem := store.Memory{
		ID:           newID(),
		Kind:         "fact",
		Content:      "new fact",
		Status:       "active",
		SupersedesID: target.ID,
		Importance:   4,
		Confidence:   0.9,
		TrustSource:  "llm_extracted",
		Stability:    1.0,
		ContentHash:  ulid.Make().String(),
		CreatedAt:    nowMs() + 1,
		UpdatedAt:    nowMs() + 1,
	}
	supersedeSet := store.CommitSet{
		Action:  store.ActionSupersede,
		Memory:  newMem,
		Targets: []store.Memory{target},
		Events: []store.Event{
			{ID: newID(), Type: "memory.superseded", SubjectID: target.ID, Payload: `{"id":"` + target.ID + `"}`, CreatedAt: nowMs() + 1},
			{ID: newID(), Type: "memory.added", SubjectID: newMem.ID, Payload: "{}", CreatedAt: nowMs() + 1},
		},
		Scope: scope,
	}
	if err := s.Memories().Commit(ctx, scope, supersedeSet); err != nil {
		t.Fatalf("supersede commit: %v", err)
	}

	// Verify target is superseded.
	gotTarget, _ := s.Memories().Get(ctx, scope, target.ID)
	if gotTarget.Status != "superseded" {
		t.Fatalf("target should be superseded, got %q", gotTarget.Status)
	}
	if gotTarget.SupersededByID != newMem.ID {
		t.Fatalf("superseded_by_id: got %q want %q", gotTarget.SupersededByID, newMem.ID)
	}

	// Rollback: restore target + tombstone newMem.
	restoredTarget := target // cleared superseded_by_id
	restoredTarget.SupersededByID = ""
	rbSet := store.CommitSet{
		Action:  store.ActionRollback,
		Memory:  restoredTarget,
		Targets: []store.Memory{newMem},
		Events:  []store.Event{{ID: newID(), Type: "memory.rolled_back", SubjectID: target.ID, Payload: "{}", CreatedAt: nowMs() + 2}},
		Scope:   scope,
	}
	if err := s.Memories().Commit(ctx, scope, rbSet); err != nil {
		t.Fatalf("rollback commit: %v", err)
	}

	// Verify target is restored.
	gotRestored, err := s.Memories().Get(ctx, scope, target.ID)
	if err != nil {
		t.Fatalf("Get restored: %v", err)
	}
	if gotRestored.Status != "active" {
		t.Errorf("status: got %q want active", gotRestored.Status)
	}
	if gotRestored.SupersededByID != "" {
		t.Errorf("superseded_by_id should be empty, got %q", gotRestored.SupersededByID)
	}
}

// testCommitRollbackTombstone ensures Targets are set to 'deleted' on rollback.
func testCommitRollbackTombstone(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Seed a memory to be tombstoned.
	resultMem := store.Memory{
		ID: newID(), Kind: "fact", Content: "result", Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, resultMem); err != nil {
		t.Fatalf("Insert result: %v", err)
	}

	// Seed the primary memory.
	primary := store.Memory{
		ID: newID(), Kind: "fact", Content: "primary", Status: "superseded",
		SupersededByID: resultMem.ID,
		Importance:     3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, primary); err != nil {
		t.Fatalf("Insert primary: %v", err)
	}

	// Rollback: restore primary (active, superseded_by_id cleared), tombstone result.
	restored := primary
	restored.Status = "active"
	restored.SupersededByID = ""
	rbSet := store.CommitSet{
		Action:  store.ActionRollback,
		Memory:  restored,
		Targets: []store.Memory{resultMem},
		Events:  []store.Event{{ID: newID(), Type: "memory.rolled_back", SubjectID: primary.ID, Payload: "{}", CreatedAt: nowMs() + 1}},
		Scope:   scope,
	}
	if err := s.Memories().Commit(ctx, scope, rbSet); err != nil {
		t.Fatalf("rollback commit: %v", err)
	}

	gotResult, err := s.Memories().Get(ctx, scope, resultMem.ID)
	if err != nil {
		t.Fatalf("Get result after rollback: %v", err)
	}
	if gotResult.Status != "deleted" {
		t.Errorf("result status: got %q want deleted", gotResult.Status)
	}
}

// testCommitRollbackJunctionsReplaced verifies junction replacement semantics.
func testCommitRollbackJunctionsReplaced(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Seed with entity-A.
	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "original", Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		ContentHash: ulid.Make().String(), CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	cs := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   mem,
		Entities: []string{"entity-A"},
		Keywords: []string{"kw-A"},
		Events:   []store.Event{{ID: newID(), Type: "memory.added", SubjectID: mem.ID, Payload: "{}", CreatedAt: nowMs()}},
		Scope:    scope,
	}
	if err := s.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Update to entity-B.
	updMem := mem
	updMem.Content = "updated"
	updMem.ContentHash = ulid.Make().String()
	ucSet := store.CommitSet{
		Action:   store.ActionUpdate,
		Memory:   updMem,
		Entities: []string{"entity-B"},
		Keywords: []string{"kw-B"},
		Events:   []store.Event{{ID: newID(), Type: "memory.updated", SubjectID: mem.ID, Payload: "{}", CreatedAt: nowMs() + 1}},
		Scope:    scope,
	}
	if err := s.Memories().Commit(ctx, scope, ucSet); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Rollback to entity-A.
	rbSet := store.CommitSet{
		Action:   store.ActionRollback,
		Memory:   mem,
		Entities: []string{"entity-A"},
		Keywords: []string{"kw-A"},
		Events:   []store.Event{{ID: newID(), Type: "memory.rolled_back", SubjectID: mem.ID, Payload: "{}", CreatedAt: nowMs() + 2}},
		Scope:    scope,
	}
	if err := s.Memories().Commit(ctx, scope, rbSet); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	jt, err := s.Memories().GetJunctions(ctx, scope, mem.ID)
	if err != nil {
		t.Fatalf("GetJunctions: %v", err)
	}
	if len(jt.Entities) != 1 || jt.Entities[0] != "entity-A" {
		t.Errorf("entities not replaced: %v", jt.Entities)
	}
	if len(jt.Keywords) != 1 || jt.Keywords[0] != "kw-A" {
		t.Errorf("keywords not replaced: %v", jt.Keywords)
	}
}

// testCommitRollbackExtraMemory tests merge-rollback with ExtraMemories (sibling restore).
func testCommitRollbackExtraMemory(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Insert two sibling memories that were merged.
	sibA := store.Memory{
		ID: newID(), Kind: "fact", Content: "sib-A", Status: "superseded",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	sibB := store.Memory{
		ID: newID(), Kind: "fact", Content: "sib-B", Status: "superseded",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, sibA); err != nil {
		t.Fatalf("Insert sibA: %v", err)
	}
	if err := s.Memories().Insert(ctx, scope, sibB); err != nil {
		t.Fatalf("Insert sibB: %v", err)
	}

	// Rollback sibA (primary) + sibB (extra).
	restoredA := sibA
	restoredA.Status = "active"
	restoredA.SupersededByID = ""
	restoredB := sibB
	restoredB.Status = "active"
	restoredB.SupersededByID = ""

	rbSet := store.CommitSet{
		Action: store.ActionRollback,
		Memory: restoredA,
		ExtraMemories: []store.RollbackMemory{
			{Memory: restoredB, Entities: []string{"sib-b-entity"}},
		},
		Events: []store.Event{
			{ID: newID(), Type: "memory.rolled_back", SubjectID: sibA.ID, Payload: "{}", CreatedAt: nowMs() + 1},
			{ID: newID(), Type: "memory.rolled_back", SubjectID: sibB.ID, Payload: "{}", CreatedAt: nowMs() + 1},
		},
		Scope: scope,
	}
	if err := s.Memories().Commit(ctx, scope, rbSet); err != nil {
		t.Fatalf("rollback with extra: %v", err)
	}

	// Both siblings should be active.
	gotA, err := s.Memories().Get(ctx, scope, sibA.ID)
	if err != nil {
		t.Fatalf("Get sibA: %v", err)
	}
	if gotA.Status != "active" {
		t.Errorf("sibA status: got %q want active", gotA.Status)
	}
	gotB, err := s.Memories().Get(ctx, scope, sibB.ID)
	if err != nil {
		t.Fatalf("Get sibB: %v", err)
	}
	if gotB.Status != "active" {
		t.Errorf("sibB status: got %q want active", gotB.Status)
	}
}

// testCommitRollbackFaultHook verifies no partial writes on mid-rollback fault.
func testCommitRollbackFaultHook(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "original", Status: "superseded",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	restored := mem
	restored.Status = "active"
	rbSet := store.CommitSet{
		Action: store.ActionRollback,
		Memory: restored,
		Events: []store.Event{{ID: newID(), Type: "memory.rolled_back", SubjectID: mem.ID, Payload: "{}", CreatedAt: nowMs() + 1}},
		Scope:  scope,
		FaultHook: func() error {
			return errors.New("fault")
		},
	}
	if err := s.Memories().Commit(ctx, scope, rbSet); err == nil {
		t.Fatal("expected error from FaultHook")
	}

	// Memory should still be in superseded state (rollback rolled back).
	got, err := s.Memories().Get(ctx, scope, mem.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "superseded" {
		t.Errorf("status after fault: got %q want superseded (rollback should have rolled back)", got.Status)
	}
}

// --- ActionConfirm conformance ----------------------------------------------

func testCommitConfirmPromote(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Seed target and parked memory.
	target := store.Memory{
		ID: newID(), Kind: "fact", Content: "old", Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, target); err != nil {
		t.Fatalf("Insert target: %v", err)
	}

	parked := store.Memory{
		ID:           newID(),
		Kind:         "fact",
		Content:      "new",
		Status:       "pending_confirmation",
		SupersedesID: target.ID,
		Importance:   4,
		Confidence:   0.9,
		TrustSource:  "llm_extracted",
		Stability:    1.0,
		ContentHash:  ulid.Make().String(),
		CreatedAt:    nowMs() + 1,
		UpdatedAt:    nowMs() + 1,
	}
	if err := s.Memories().Insert(ctx, scope, parked); err != nil {
		t.Fatalf("Insert parked: %v", err)
	}

	// Confirm: parked → active; target → superseded.
	promoted := parked
	promoted.Status = "active"
	confirmSet := store.CommitSet{
		Action:  store.ActionConfirm,
		Memory:  promoted,
		Targets: []store.Memory{target},
		Events: []store.Event{
			{ID: newID(), Type: "memory.superseded", SubjectID: target.ID, Payload: "{}", CreatedAt: nowMs() + 2},
			{ID: newID(), Type: "memory.confirmed", SubjectID: parked.ID, Payload: "{}", CreatedAt: nowMs() + 2},
		},
		Scope: scope,
	}
	if err := s.Memories().Commit(ctx, scope, confirmSet); err != nil {
		t.Fatalf("confirm commit: %v", err)
	}

	// Verify parked is now active.
	gotParked, err := s.Memories().Get(ctx, scope, parked.ID)
	if err != nil {
		t.Fatalf("Get parked: %v", err)
	}
	if gotParked.Status != "active" {
		t.Errorf("parked status: got %q want active", gotParked.Status)
	}

	// Verify target is superseded with superseded_by_id = parked.ID.
	gotTarget, err := s.Memories().Get(ctx, scope, target.ID)
	if err != nil {
		t.Fatalf("Get target: %v", err)
	}
	if gotTarget.Status != "superseded" {
		t.Errorf("target status: got %q want superseded", gotTarget.Status)
	}
	if gotTarget.SupersededByID != parked.ID {
		t.Errorf("superseded_by_id: got %q want %q", gotTarget.SupersededByID, parked.ID)
	}
}

func testCommitConfirmActivateNoTarget(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Parked memory with no supersedes_id (or target gone).
	parked := store.Memory{
		ID: newID(), Kind: "fact", Content: "plain parked", Status: "pending_confirmation",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		ContentHash: ulid.Make().String(), CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, parked); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Confirm with no Targets — plain activate.
	promoted := parked
	promoted.Status = "active"
	confirmSet := store.CommitSet{
		Action:  store.ActionConfirm,
		Memory:  promoted,
		Targets: nil,
		Events:  []store.Event{{ID: newID(), Type: "memory.confirmed", SubjectID: parked.ID, Payload: "{}", CreatedAt: nowMs() + 1}},
		Scope:   scope,
	}
	if err := s.Memories().Commit(ctx, scope, confirmSet); err != nil {
		t.Fatalf("confirm commit: %v", err)
	}

	got, err := s.Memories().Get(ctx, scope, parked.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("status: got %q want active", got.Status)
	}
}

func testCommitConfirmFaultHook(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	parked := store.Memory{
		ID: newID(), Kind: "fact", Content: "test", Status: "pending_confirmation",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, parked); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	promoted := parked
	promoted.Status = "active"
	confirmSet := store.CommitSet{
		Action:  store.ActionConfirm,
		Memory:  promoted,
		Targets: nil,
		Events:  []store.Event{{ID: newID(), Type: "memory.confirmed", SubjectID: parked.ID, Payload: "{}", CreatedAt: nowMs() + 1}},
		Scope:   scope,
		FaultHook: func() error {
			return errors.New("fault")
		},
	}
	if err := s.Memories().Commit(ctx, scope, confirmSet); err == nil {
		t.Fatal("expected error from FaultHook")
	}

	got, err := s.Memories().Get(ctx, scope, parked.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "pending_confirmation" {
		t.Errorf("status after fault: got %q want pending_confirmation", got.Status)
	}
}

// --- P3: cross-scope rollback unconstructible --------------------------------

// testCrossScopeRollbackUnconstructible asserts that a caller in scope A
// cannot roll back scope B's memory. The test simulates this by attempting to
// read scope B's memory with scope A's credentials (via Get) — the store layer
// must return ErrNotFound, making the cross-scope rollback unconstructible.
func testCrossScopeRollbackUnconstructible(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("A-" + newID())
	scopeB := tenantScope("B-" + newID())

	// Seed a memory in scope B.
	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "scope-B secret", Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scopeB, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// A caller in scope A cannot see scope B's memory.
	_, err := s.Memories().Get(ctx, scopeA, mem.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-scope Get should return ErrNotFound (P3), got %v", err)
	}

	// Events in scope B are invisible from scope A.
	evts, err := s.Events().ListBySubject(ctx, scopeA, mem.ID, 10)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	if len(evts) != 0 {
		t.Errorf("cross-scope events should be empty (P3), got %d", len(evts))
	}
}

// testRollbackBumpsGeneration is a marker test: it verifies that ActionRollback
// commits round-trip correctly (the generation-bump assertion is done at the
// api handler layer with the live cache wired). Here we just ensure the commit
// succeeds and a memory.rolled_back event is written in the same tx.
func testRollbackBumpsGeneration(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	mem := store.Memory{
		ID: newID(), Kind: "fact", Content: "original", Status: "superseded",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	restored := mem
	restored.Status = "active"
	restored.SupersededByID = ""
	evID := newID()
	rbSet := store.CommitSet{
		Action: store.ActionRollback,
		Memory: restored,
		Events: []store.Event{{ID: evID, Type: "memory.rolled_back", SubjectID: mem.ID, Payload: "{}", CreatedAt: nowMs() + 1}},
		Scope:  scope,
	}
	if err := s.Memories().Commit(ctx, scope, rbSet); err != nil {
		t.Fatalf("rollback commit: %v", err)
	}

	// Verify the event was written in the same tx.
	evts, err := s.Events().ListBySubject(ctx, scope, mem.ID, 10)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	found := false
	for _, ev := range evts {
		if ev.ID == evID && ev.Type == "memory.rolled_back" {
			found = true
		}
	}
	if !found {
		t.Errorf("memory.rolled_back event not found for memory %q; events: %v", mem.ID, evts)
	}
}

// testAppliedMigrationsPhase18 verifies migration 0006 is applied.
func testAppliedMigrationsPhase18(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()

	applied, err := s.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	found := false
	for _, v := range applied {
		if v == "0006_events_subject_index" {
			found = true
		}
	}
	if !found {
		t.Errorf("migration 0006_events_subject_index not applied; applied: %v", applied)
	}
}

// --- MemoryStore.ListSupersededBy -------------------------------------------

func testListSupersededBy(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	// Seed digest (the superseder).
	digest := store.Memory{
		ID: newID(), Kind: "fact", Content: "merged digest", Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, digest); err != nil {
		t.Fatalf("Insert digest: %v", err)
	}

	// Seed two siblings pointing to the digest.
	sibA := store.Memory{
		ID: newID(), Kind: "fact", Content: "sib-A", Status: "superseded",
		SupersededByID: digest.ID,
		Importance:     3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	sibB := store.Memory{
		ID: newID(), Kind: "fact", Content: "sib-B", Status: "superseded",
		SupersededByID: digest.ID,
		Importance:     3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scope, sibA); err != nil {
		t.Fatalf("Insert sibA: %v", err)
	}
	if err := s.Memories().Insert(ctx, scope, sibB); err != nil {
		t.Fatalf("Insert sibB: %v", err)
	}

	got, err := s.Memories().ListSupersededBy(ctx, scope, digest.ID)
	if err != nil {
		t.Fatalf("ListSupersededBy: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d memories want 2", len(got))
	}
	for _, m := range got {
		if m.SupersededByID != digest.ID {
			t.Errorf("unexpected superseded_by_id %q", m.SupersededByID)
		}
	}
}

func testListSupersededByEmpty(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-" + newID())

	got, err := s.Memories().ListSupersededBy(ctx, scope, "no-such-id")
	if err != nil {
		t.Fatalf("ListSupersededBy: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

func testListSupersededByScopeIsolation(t *testing.T, factory Factory) {
	t.Helper()
	s, cleanup := factory()
	defer cleanup()
	ctx := context.Background()
	scopeA := tenantScope("A-" + newID())
	scopeB := tenantScope("B-" + newID())

	digestID := newID()
	sib := store.Memory{
		ID: newID(), Kind: "fact", Content: "sib", Status: "superseded",
		SupersededByID: digestID,
		Importance:     3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: nowMs(), UpdatedAt: nowMs(),
	}
	if err := s.Memories().Insert(ctx, scopeA, sib); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Memories().ListSupersededBy(ctx, scopeB, digestID)
	if err != nil {
		t.Fatalf("ListSupersededBy scopeB: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("cross-scope isolation violated: got %d", len(got))
	}
}

// suppress unused warning for time import used in nowMs.
var _ = time.Now

// suppress unused warning for ulid.
var _ = ulid.Make
