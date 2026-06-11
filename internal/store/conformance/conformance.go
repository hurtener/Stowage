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
	"github.com/oklog/ulid/v2"
)

// Factory returns a ready-to-use store and a cleanup function.
type Factory func() (store.Store, func())

// Run executes the full conformance suite against the provided factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("MigrateIdempotent", func(t *testing.T) { testMigrateIdempotent(t, factory) })
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
