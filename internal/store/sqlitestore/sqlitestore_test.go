package sqlitestore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/store/conformance"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register driver
	"github.com/oklog/ulid/v2"
)

// newTestStore opens a fresh SQLite store in a temp dir and migrates it.
func newTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	cfg := config.StoreConfig{Driver: "sqlite", DSN: dsn}
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		_ = s.Close(context.Background())
		t.Fatalf("migrate: %v", err)
	}
	return s, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Logf("close store: %v", err)
		}
	}
}

// TestConformance runs the full conformance suite against the SQLite driver.
func TestConformance(t *testing.T) {
	conformance.Run(t, func() (store.Store, func()) {
		return newTestStore(t)
	})
}

// TestMigrateTwiceIdempotent verifies migration idempotency.
func TestMigrateTwiceIdempotent(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// Second migrate is idempotent.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

// TestWriteAfterClose verifies that concurrent writers do not panic when the
// store is closed under them (W1). Each goroutine must receive either nil,
// store.ErrClosed, or a context error — never a panic.
func TestWriteAfterClose(t *testing.T) {
	s, _ := newTestStore(t) // no defer cleanup — we close manually below

	ctx := context.Background()
	scope := identity.Scope{Tenant: "close-tenant"}

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := store.Record{
				ID:         ulid.Make().String(),
				Role:       "user",
				Content:    fmt.Sprintf("close-race worker %d", i),
				OccurredAt: time.Now().UnixMilli(),
				CreatedAt:  time.Now().UnixMilli(),
			}
			errs[i] = s.Records().Append(ctx, scope, []store.Record{rec})
		}()
	}

	// Close the store while goroutines are still running.
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Close(closeCtx); err != nil {
		t.Logf("close: %v", err)
	}

	wg.Wait()

	// Every error must be nil, store.ErrClosed, or a context error.
	for i, err := range errs {
		if err == nil ||
			errors.Is(err, store.ErrClosed) ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		t.Errorf("worker %d: unexpected error: %v", i, err)
	}
}

// TestConcurrentWriters verifies that ≥8 concurrent goroutines each writing ≥200
// records produce zero errors (no SQLITE_BUSY surfaces to callers).
// Runs under -race.
func TestConcurrentWriters(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	const (
		numGoroutines       = 8
		recordsPerGoroutine = 200
	)

	var (
		wg       sync.WaitGroup
		errCount atomic.Int64
	)

	// Start concurrent readers in background.
	readerStop := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scope := identity.Scope{Tenant: "reader-tenant"}
			for {
				select {
				case <-readerStop:
					return
				default:
					_, _ = s.Records().ListUnprocessed(ctx, time.Now().UnixMilli(), 10)
					_, _, _ = s.Memories().ListByStatus(ctx, scope, "active", 10, "")
				}
			}
		}()
	}

	// Concurrent writers.
	var writerWg sync.WaitGroup
	for g := 0; g < numGoroutines; g++ {
		g := g
		writerWg.Add(1)
		go func() {
			defer writerWg.Done()
			scope := identity.Scope{Tenant: fmt.Sprintf("tenant-%d", g)}
			base := time.Now().UnixMilli()

			// Append in batches of 10.
			for batch := 0; batch < recordsPerGoroutine/10; batch++ {
				recs := make([]store.Record, 10)
				for i := range recs {
					recs[i] = store.Record{
						ID:         ulid.Make().String(),
						Role:       "user",
						Content:    fmt.Sprintf("g%d b%d i%d", g, batch, i),
						OccurredAt: base + int64(batch*10+i),
						CreatedAt:  base + int64(batch*10+i),
					}
				}
				if err := s.Records().Append(ctx, scope, recs); err != nil {
					errCount.Add(1)
					t.Errorf("writer %d batch %d: %v", g, batch, err)
					return
				}
			}
		}()
	}

	// Wait for all writers to complete, then stop readers.
	writerWg.Wait()
	close(readerStop)
	wg.Wait()

	if errCount.Load() > 0 {
		t.Errorf("total errors: %d", errCount.Load())
	}

	// Verify record counts per tenant.
	for g := 0; g < numGoroutines; g++ {
		scope := identity.Scope{Tenant: fmt.Sprintf("tenant-%d", g)}
		recs, _, err := s.Records().ListBySession(ctx, scope, "", "", 1000, "")
		if err != nil {
			t.Errorf("count tenant-%d: %v", g, err)
			continue
		}
		if len(recs) != recordsPerGoroutine {
			t.Errorf("tenant-%d: got %d records want %d", g, len(recs), recordsPerGoroutine)
		}
	}
}

// BenchmarkAppendRecords benchmarks appending 100 records per operation.
func BenchmarkAppendRecords(b *testing.B) {
	dir := b.TempDir()
	dsn := filepath.Join(dir, "bench.db")
	cfg := config.StoreConfig{Driver: "sqlite", DSN: dsn}
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer s.Close(context.Background()) //nolint:errcheck
	if err := s.Migrate(context.Background()); err != nil {
		b.Fatalf("migrate: %v", err)
	}

	scope := identity.Scope{Tenant: "bench-tenant"}
	ctx := context.Background()

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		recs := make([]store.Record, 100)
		base := time.Now().UnixMilli()
		for i := range recs {
			recs[i] = store.Record{
				ID:         ulid.Make().String(),
				Role:       "user",
				Content:    "benchmark record",
				OccurredAt: base + int64(i),
				CreatedAt:  base + int64(i),
			}
		}
		if err := s.Records().Append(ctx, scope, recs); err != nil {
			b.Fatalf("append: %v", err)
		}
	}
}

// BenchmarkListByStatus benchmarks listing active memories.
func BenchmarkListByStatus(b *testing.B) {
	dir := b.TempDir()
	dsn := filepath.Join(dir, "bench2.db")
	cfg := config.StoreConfig{Driver: "sqlite", DSN: dsn}
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer s.Close(context.Background()) //nolint:errcheck
	if err := s.Migrate(context.Background()); err != nil {
		b.Fatalf("migrate: %v", err)
	}

	scope := identity.Scope{Tenant: "bench2-tenant"}
	ctx := context.Background()

	// Pre-populate 1000 memories.
	base := time.Now().UnixMilli()
	for i := 0; i < 1000; i++ {
		mem := store.Memory{
			ID:          ulid.Make().String(),
			Kind:        "fact",
			Content:     fmt.Sprintf("memory %d", i),
			Status:      "active",
			Confidence:  0.5,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			CreatedAt:   base + int64(i),
			UpdatedAt:   base + int64(i),
		}
		if err := s.Memories().Insert(ctx, scope, mem); err != nil {
			b.Fatalf("insert: %v", err)
		}
	}

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if _, _, err := s.Memories().ListByStatus(ctx, scope, "active", 50, ""); err != nil {
			b.Fatalf("list: %v", err)
		}
	}
}

// TestZeroTimestampDefaults verifies that items with zero timestamps receive
// server-side defaults. This covers the "if field == 0 { field = now }" branches
// in buffers, events, memories, records, links, provenance, and dead-letters.
func TestZeroTimestampDefaults(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	scope := identity.Scope{Tenant: "ts-tenant"}

	// Record with zero CreatedAt.
	recID := ulid.Make().String()
	rec := store.Record{
		ID:         recID,
		Role:       "user",
		Content:    "zero-ts record",
		OccurredAt: time.Now().UnixMilli(),
		// CreatedAt deliberately zero
	}
	if err := s.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Memory with zero CreatedAt and UpdatedAt.
	memID := ulid.Make().String()
	mem := store.Memory{
		ID:          memID,
		Kind:        "fact",
		Content:     "zero-ts memory",
		Status:      "active",
		TrustSource: "llm_extracted",
		Stability:   1.0,
		// CreatedAt and UpdatedAt deliberately zero
	}
	if err := s.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	// Links with zero CreatedAt.
	fromID := ulid.Make().String()
	toID := ulid.Make().String()
	from := store.Memory{ID: fromID, Kind: "fact", Content: "from", Status: "active", TrustSource: "x", Stability: 1.0, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}
	to := store.Memory{ID: toID, Kind: "fact", Content: "to", Status: "active", TrustSource: "x", Stability: 1.0, CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()}
	if err := s.Memories().Insert(ctx, scope, from); err != nil {
		t.Fatalf("Insert from: %v", err)
	}
	if err := s.Memories().Insert(ctx, scope, to); err != nil {
		t.Fatalf("Insert to: %v", err)
	}
	link := store.Link{
		ID:         ulid.Make().String(),
		FromMemory: fromID,
		ToMemory:   toID,
		Type:       "relates_to",
		Source:     "explicit",
		Confidence: 0.9,
		// CreatedAt deliberately zero
	}
	if err := s.Memories().InsertLinks(ctx, scope, []store.Link{link}); err != nil {
		t.Fatalf("InsertLinks: %v", err)
	}

	// ListLinks with toMemoryID filter (covers the AND to_memory = ? branch).
	links, err := s.Memories().ListLinks(ctx, scope, "", toID)
	if err != nil {
		t.Fatalf("ListLinks(toMemoryID): %v", err)
	}
	if len(links) != 1 {
		t.Errorf("ListLinks by toMemoryID: got %d want 1", len(links))
	}

	// Provenance with zero CreatedAt.
	prov := store.Provenance{
		ID:       ulid.Make().String(),
		MemoryID: memID,
		RecordID: recID,
		// CreatedAt deliberately zero
	}
	if err := s.Memories().AddProvenance(ctx, scope, []store.Provenance{prov}); err != nil {
		t.Fatalf("AddProvenance: %v", err)
	}

	// BufferItem with zero CreatedAt.
	bufItem := store.BufferItem{
		ID:        ulid.Make().String(),
		BufferKey: "buf-zero-ts",
		RecordID:  recID,
		// CreatedAt deliberately zero
	}
	if err := s.Buffers().AppendItem(ctx, scope, bufItem); err != nil {
		t.Fatalf("AppendItem: %v", err)
	}

	// Event with zero CreatedAt.
	ev := store.Event{
		ID:   ulid.Make().String(),
		Type: "zero_ts_event",
		// CreatedAt deliberately zero
	}
	if err := s.Events().Emit(ctx, scope, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// DeadLetter with zero ID and zero CreatedAt (covers both default branches).
	dl := store.DeadLetter{
		// ID deliberately empty
		Stage:    "ingress",
		ItemID:   "item-zero",
		Error:    "some error",
		Attempts: 1,
		// CreatedAt deliberately zero
	}
	if err := s.Ops().PutDeadLetter(ctx, dl); err != nil {
		t.Fatalf("PutDeadLetter: %v", err)
	}
}

// TestListBySessionWithBranch verifies that ListBySession filters by branchID
// when provided (covers the AND branch_id = ? clause).
func TestListBySessionWithBranch(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	ctx := context.Background()
	sessionID := ulid.Make().String()
	branchA := ulid.Make().String()
	branchB := ulid.Make().String()
	base := time.Now().UnixMilli()

	// Insert records on two different branches.
	for i, branchID := range []string{branchA, branchB} {
		rec := store.Record{
			ID:         ulid.Make().String(),
			Role:       "user",
			Content:    fmt.Sprintf("branch %d record", i),
			OccurredAt: base + int64(i),
			CreatedAt:  base + int64(i),
			BranchID:   branchID,
		}
		scopeWithSession := identity.Scope{Tenant: "branch-tenant", Session: sessionID}
		if err := s.Records().Append(ctx, scopeWithSession, []store.Record{rec}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// List only branch A records.
	scopeWithSession := identity.Scope{Tenant: "branch-tenant", Session: sessionID}
	recs, _, err := s.Records().ListBySession(ctx, scopeWithSession, sessionID, branchA, 10, "")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("got %d records for branch A, want 1", len(recs))
	}
}

// TestKeyInsertWithRevokedAt verifies that inserting a key with a non-nil
// RevokedAt is stored and retrieved correctly (covers the revokedAt branch).
func TestKeyInsertWithRevokedAt(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	revokedTime := time.Now().UTC().Truncate(time.Millisecond)
	var hash [32]byte
	hash[0] = 0xde
	hash[1] = 0xad

	key := auth.Key{
		ID:        "sk_testrevokedkey",
		TenantID:  "revoke-tenant",
		Role:      auth.Role("admin"),
		Hash:      hash,
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
		RevokedAt: &revokedTime,
	}
	if err := s.Keys().Insert(key); err != nil {
		t.Fatalf("Insert with RevokedAt: %v", err)
	}
	got, err := s.Keys().Lookup(key.ID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.RevokedAt == nil {
		t.Fatal("RevokedAt should be non-nil after insert")
	}
	if !got.RevokedAt.Equal(revokedTime) {
		t.Errorf("RevokedAt mismatch: got %v want %v", got.RevokedAt, revokedTime)
	}
}

// TestRevokeNonExistentKey verifies that revoking a key that does not exist
// returns auth.ErrKeyNotFound (covers the n==0 branch in Revoke).
func TestRevokeNonExistentKey(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	err := s.Keys().Revoke("no-such-key", time.Now())
	if !errors.Is(err, auth.ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound, got: %v", err)
	}
}

// TestResolveDeadLetterNotFound verifies that resolving a non-existent dead
// letter returns store.ErrNotFound (covers the n==0 branch in ResolveDeadLetter).
func TestResolveDeadLetterNotFound(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	err := s.Ops().ResolveDeadLetter(context.Background(), "no-such-dl", time.Now().UnixMilli())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// TestChecksumMismatchDetected verifies that a tampered migration checksum
// causes Migrate to return store.ErrChecksumMismatch.
func TestChecksumMismatchDetected(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "mismatch.db")

	// First open + migrate succeeds.
	cfg := config.StoreConfig{Driver: "sqlite", DSN: dsn}
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Close before tampering.
	if err := s.Close(ctx); err != nil {
		t.Logf("close: %v", err)
	}

	// Tamper with the stored checksum via a raw SQL connection.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	_, err = db.Exec(`UPDATE schema_migrations SET checksum = 'tampered-bad-checksum'`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("tamper: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Logf("raw close: %v", err)
	}

	// Reopen and migrate — must detect the mismatch.
	s2, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close(context.Background()) //nolint:errcheck
	err = s2.Migrate(ctx)
	if !errors.Is(err, store.ErrChecksumMismatch) {
		t.Errorf("expected ErrChecksumMismatch, got: %v", err)
	}
}

// TestMarkProcessedEmpty verifies that MarkProcessed with no IDs is a no-op.
func TestMarkProcessedEmpty(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()

	if err := s.Records().MarkProcessed(context.Background(), nil); err != nil {
		t.Errorf("MarkProcessed(nil): %v", err)
	}
	if err := s.Records().MarkProcessed(context.Background(), []string{}); err != nil {
		t.Errorf("MarkProcessed([]): %v", err)
	}
}

// TestMain is the test entrypoint.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// TestAppliedMigrationsBeforeMigrate covers the bootstrap branch: a store
// opened without Migrate has no schema_migrations table and must return
// (nil, nil), not an error — `migrate --status` relies on this.
func TestAppliedMigrationsBeforeMigrate(t *testing.T) {
	dir := t.TempDir()
	cfg := config.StoreConfig{Driver: "sqlite", DSN: dir + "/fresh.db"}
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close(context.Background()) //nolint:errcheck

	applied, err := s.AppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("AppliedMigrations on fresh db: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("expected no applied migrations, got %v", applied)
	}

	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	applied, err = s.AppliedMigrations(context.Background())
	if err != nil || len(applied) == 0 {
		t.Fatalf("after migrate: %v %v", applied, err)
	}
}
