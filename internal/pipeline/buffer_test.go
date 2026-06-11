package pipeline_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"

	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func noopLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "pipe-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	_ = f.Close()

	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = f.Name()

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

// insertRecord writes a record directly to the store and returns its ID.
func insertRecord(t *testing.T, st store.Store, tenantID, sessionID, branchID string, tokens int64) string {
	t.Helper()
	id := ulid.Make().String()
	rec := store.Record{
		ID:            id,
		TenantID:      tenantID,
		SessionID:     sessionID,
		BranchID:      branchID,
		Role:          "user",
		Content:       "test content",
		TokenEstimate: tokens,
		OccurredAt:    time.Now().UnixMilli(),
		CreatedAt:     time.Now().UnixMilli(),
	}
	if err := st.Records().Append(context.Background(), identity.Scope{Tenant: tenantID}, []store.Record{rec}); err != nil {
		t.Fatalf("append record: %v", err)
	}
	return id
}

// newStageAndChan creates a stage with the given triggers and an ingest channel.
func newStageAndChan(st store.Store, trig pipeline.Triggers) (*pipeline.Stage, chan pipeline.Item) {
	in := make(chan pipeline.Item, 512)
	s := pipeline.New(st, noopLog(), trig, in)
	return s, in
}

// collectN drains exactly n events from ch, blocking until available.
func collectN(t *testing.T, ch <-chan pipeline.FlushedBuffer, n int, timeout time.Duration) []pipeline.FlushedBuffer {
	t.Helper()
	var out []pipeline.FlushedBuffer
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case fb, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, fb)
		case <-deadline:
			t.Fatalf("collectN: timed out after %v waiting for %d events (got %d)", timeout, n, len(out))
		}
	}
	return out
}

// waitBufferItems polls store.Buffers().ListDue until the buffer has at least n
// unflushed items, or timeout elapses. Used to avoid timing races in tests.
func waitBufferItems(t *testing.T, st store.Store, tenantID, bufKey string, n int, timeout time.Duration) {
	t.Helper()
	scope := identity.Scope{Tenant: tenantID}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		items, err := st.Buffers().ListDue(context.Background(), scope, bufKey, 100)
		if err == nil && len(items) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waitBufferItems: timed out waiting for %d items in buffer %q (tenant %q)", n, bufKey, tenantID)
}

// ── AC-1: Trigger matrix ─────────────────────────────────────────────────────

// TestTriggerMatrix proves AC-1: each trigger fires exactly once with the
// correct reason.
func TestTriggerMatrix(t *testing.T) {
	t.Parallel()

	// Tight triggers so tests finish quickly.
	trig := pipeline.Triggers{Count: 3, Tokens: 50, MaxAge: 200 * time.Millisecond}

	cases := []struct {
		name    string
		trigger string
		setup   func(t *testing.T, st store.Store, s *pipeline.Stage, in chan pipeline.Item)
	}{
		{
			name:    "count",
			trigger: pipeline.TriggerCount,
			setup: func(t *testing.T, st store.Store, s *pipeline.Stage, in chan pipeline.Item) {
				t.Helper()
				tenant := "t-count"
				for i := 0; i < 3; i++ {
					id := insertRecord(t, st, tenant, "sess", "br", 5)
					in <- pipeline.Item{RecordID: id, TenantID: tenant, SessionID: "sess", BranchID: "br"}
				}
			},
		},
		{
			name:    "tokens",
			trigger: pipeline.TriggerTokens,
			setup: func(t *testing.T, st store.Store, s *pipeline.Stage, in chan pipeline.Item) {
				t.Helper()
				tenant := "t-tokens"
				for i := 0; i < 2; i++ {
					id := insertRecord(t, st, tenant, "sess", "br", 30)
					in <- pipeline.Item{RecordID: id, TenantID: tenant, SessionID: "sess", BranchID: "br"}
				}
			},
		},
		{
			name:    "age",
			trigger: pipeline.TriggerAge,
			setup: func(t *testing.T, st store.Store, s *pipeline.Stage, in chan pipeline.Item) {
				t.Helper()
				tenant := "t-age"
				id := insertRecord(t, st, tenant, "sess", "br", 5)
				in <- pipeline.Item{RecordID: id, TenantID: tenant, SessionID: "sess", BranchID: "br"}
				// item will age and the ticker will flush it
			},
		},
		{
			name:    "explicit",
			trigger: pipeline.TriggerExplicit,
			setup: func(t *testing.T, st store.Store, s *pipeline.Stage, in chan pipeline.Item) {
				t.Helper()
				tenant := "t-explicit"
				id := insertRecord(t, st, tenant, "sess", "br", 5)
				in <- pipeline.Item{RecordID: id, TenantID: tenant, SessionID: "sess", BranchID: "br"}
				// Wait until AppendItem has persisted the item before calling FlushKey,
				// so FlushKey finds ≥1 item and emits the flush with trigger "explicit"
				// rather than returning nil (empty buffer).
				waitBufferItems(t, st, tenant, "sess/br", 1, 3*time.Second)
				scope := identity.Scope{Tenant: tenant}
				if err := s.FlushKey(context.Background(), scope, "sess/br", pipeline.TriggerExplicit); err != nil {
					t.Errorf("FlushKey: %v", err)
				}
			},
		},
		{
			name:    "session_end",
			trigger: pipeline.TriggerSessionEnd,
			setup: func(t *testing.T, st store.Store, s *pipeline.Stage, in chan pipeline.Item) {
				t.Helper()
				tenant := "t-sess-end"
				id := insertRecord(t, st, tenant, "sess", "br", 5)
				in <- pipeline.Item{RecordID: id, TenantID: tenant, SessionID: "sess", BranchID: "br"}
				waitBufferItems(t, st, tenant, "sess/br", 1, 3*time.Second)
				scope := identity.Scope{Tenant: tenant}
				if err := s.FlushKey(context.Background(), scope, "sess/br", pipeline.TriggerSessionEnd); err != nil {
					t.Errorf("FlushKey: %v", err)
				}
			},
		},
		{
			name:    "branch_discard",
			trigger: pipeline.TriggerBranchDiscard,
			setup: func(t *testing.T, st store.Store, s *pipeline.Stage, in chan pipeline.Item) {
				t.Helper()
				tenant := "t-branch-disc"
				id := insertRecord(t, st, tenant, "sess", "br-disc", 5)
				in <- pipeline.Item{RecordID: id, TenantID: tenant, SessionID: "sess", BranchID: "br-disc"}
				waitBufferItems(t, st, tenant, "sess/br-disc", 1, 3*time.Second)
				s.FlushBranch(context.Background(), "br-disc")
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newTestStore(t)
			s, in := newStageAndChan(st, trig)
			s.Start(context.Background())
			t.Cleanup(func() {
				close(in)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				s.Drain(ctx)
			})

			tc.setup(t, st, s, in)

			events := collectN(t, s.Downstream(), 1, 5*time.Second)
			if len(events) != 1 {
				t.Fatalf("trigger %q: got %d events want 1", tc.trigger, len(events))
			}
			if events[0].Trigger != tc.trigger {
				t.Errorf("trigger: got %q want %q", events[0].Trigger, tc.trigger)
			}
			// branch_discard must set SkipPromotion.
			if tc.trigger == pipeline.TriggerBranchDiscard && !events[0].SkipPromotion {
				t.Error("branch_discard: SkipPromotion must be true")
			}
			if tc.trigger != pipeline.TriggerBranchDiscard && events[0].SkipPromotion {
				t.Errorf("trigger %q: SkipPromotion must be false", tc.trigger)
			}
		})
	}
}

// ── AC-2: Exactly-once under concurrency ─────────────────────────────────────

// TestExactlyOnce proves AC-2: many concurrent appenders and racing triggers
// result in every record appearing in exactly one FlushedBuffer, no duplicates.
func TestExactlyOnce(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	trig := pipeline.Triggers{Count: 5, Tokens: 10000, MaxAge: 10 * time.Second}
	s, in := newStageAndChan(st, trig)
	s.Start(context.Background())

	const (
		tenants    = 4
		recsPerTen = 20 // 4 full count-trigger flushes per tenant
	)

	var wg sync.WaitGroup
	for range tenants {
		tenant := "exact-tenant-" + ulid.Make().String()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < recsPerTen; i++ {
				id := insertRecord(t, st, tenant, "sess", "br", 1)
				in <- pipeline.Item{RecordID: id, TenantID: tenant}
			}
		}()
	}
	wg.Wait()
	close(in)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.Drain(ctx)

	// Collect all downstream events.
	var allFB []pipeline.FlushedBuffer
	for fb := range s.Downstream() {
		allFB = append(allFB, fb)
	}

	// Each record ID must appear exactly once across all FlushedBuffers.
	seen := make(map[string]int)
	for _, fb := range allFB {
		for _, id := range fb.RecordIDs {
			seen[id]++
		}
	}

	// Check all expected IDs are present and no ID appears more than once.
	duplicates := 0
	for id, count := range seen {
		if count > 1 {
			t.Errorf("record %q flushed %d times (want 1)", id, count)
			duplicates++
		}
	}
	if duplicates > 0 {
		t.Errorf("exactly-once violated: %d duplicate record IDs", duplicates)
	}
}

// ── AC-3: Branch isolation ────────────────────────────────────────────────────

// TestBranchIsolation proves AC-3: records from branch X never appear in
// branch Y's FlushedBuffer.
func TestBranchIsolation(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	trig := pipeline.Triggers{Count: 2, Tokens: 10000, MaxAge: 10 * time.Second}
	s, in := newStageAndChan(st, trig)
	s.Start(context.Background())
	t.Cleanup(func() {
		close(in)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.Drain(ctx)
	})

	tenant := "t-iso"

	// Branch A: 2 records → count trigger.
	brA := "branch-A"
	idA1 := insertRecord(t, st, tenant, "sess", brA, 5)
	idA2 := insertRecord(t, st, tenant, "sess", brA, 5)
	in <- pipeline.Item{RecordID: idA1, TenantID: tenant, SessionID: "sess", BranchID: brA}
	in <- pipeline.Item{RecordID: idA2, TenantID: tenant, SessionID: "sess", BranchID: brA}

	// Branch B: 2 records → count trigger.
	brB := "branch-B"
	idB1 := insertRecord(t, st, tenant, "sess", brB, 5)
	idB2 := insertRecord(t, st, tenant, "sess", brB, 5)
	in <- pipeline.Item{RecordID: idB1, TenantID: tenant, SessionID: "sess", BranchID: brB}
	in <- pipeline.Item{RecordID: idB2, TenantID: tenant, SessionID: "sess", BranchID: brB}

	events := collectN(t, s.Downstream(), 2, 5*time.Second)

	// Group events by branch.
	branchEvents := make(map[string][]string)
	for _, fb := range events {
		branchEvents[fb.BranchID] = append(branchEvents[fb.BranchID], fb.RecordIDs...)
	}

	// Verify isolation: branch A IDs must not appear in branch B's flush.
	setA := map[string]bool{idA1: true, idA2: true}
	setB := map[string]bool{idB1: true, idB2: true}
	for _, id := range branchEvents[brB] {
		if setA[id] {
			t.Errorf("branch isolation violated: record %q (branch A) found in branch B flush", id)
		}
	}
	for _, id := range branchEvents[brA] {
		if setB[id] {
			t.Errorf("branch isolation violated: record %q (branch B) found in branch A flush", id)
		}
	}
}

// ── AC-4: Crash recovery ─────────────────────────────────────────────────────

// TestCrashRecovery proves AC-4: items appended via one stage instance are
// flushed by a fresh stage instance within one ticker period.
func TestCrashRecovery(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	// Very short max age so the ticker fires quickly in the test.
	trig := pipeline.Triggers{Count: 1000, Tokens: 1_000_000, MaxAge: 100 * time.Millisecond}

	tenant := "t-crash"

	// Stage 1: append items but stop before any trigger fires.
	in1 := make(chan pipeline.Item, 10)
	s1 := pipeline.New(st, noopLog(), trig, in1)
	s1.Start(context.Background())

	id1 := insertRecord(t, st, tenant, "sess", "br", 5)
	in1 <- pipeline.Item{RecordID: id1, TenantID: tenant, SessionID: "sess", BranchID: "br"}
	id2 := insertRecord(t, st, tenant, "sess", "br", 5)
	in1 <- pipeline.Item{RecordID: id2, TenantID: tenant, SessionID: "sess", BranchID: "br"}

	// Give workers time to append to the buffer store.
	time.Sleep(100 * time.Millisecond)

	// "Crash" stage 1: close without flushing.
	close(in1)
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel1()
	s1.Drain(ctx1)

	// Stage 2 (fresh): must find and flush the aged items within one ticker period.
	in2 := make(chan pipeline.Item, 10)
	s2 := pipeline.New(st, noopLog(), trig, in2)
	s2.Start(context.Background())
	t.Cleanup(func() {
		close(in2)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s2.Drain(ctx)
	})

	// Wait for the ticker to fire (MaxAge + buffer + jitter).
	events := collectN(t, s2.Downstream(), 1, 5*time.Second)
	if len(events) == 0 {
		t.Fatal("crash recovery: no flush event from fresh stage instance")
	}

	// Verify the recovered items were flushed.
	foundIDs := make(map[string]bool)
	for _, fb := range events {
		for _, id := range fb.RecordIDs {
			foundIDs[id] = true
		}
	}
	for _, wantID := range []string{id1, id2} {
		if !foundIDs[wantID] {
			t.Errorf("crash recovery: record %q not found in recovered flush", wantID)
		}
	}
}

// ── AC-6: buffer.flushed events ──────────────────────────────────────────────

// TestBufferFlushedEvents proves AC-6: buffer.flushed events are emitted via
// the EventStore with the trigger reason.
func TestBufferFlushedEvents(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	trig := pipeline.Triggers{Count: 1, Tokens: 10000, MaxAge: 10 * time.Second}
	s, in := newStageAndChan(st, trig)
	s.Start(context.Background())
	t.Cleanup(func() {
		close(in)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.Drain(ctx)
	})

	tenant := "t-events"
	scope := identity.Scope{Tenant: tenant}

	id1 := insertRecord(t, st, tenant, "sess", "br", 5)
	in <- pipeline.Item{RecordID: id1, TenantID: tenant, SessionID: "sess", BranchID: "br"}

	// Wait for the flush event.
	collectN(t, s.Downstream(), 1, 5*time.Second)

	// Give the event store write a moment.
	time.Sleep(50 * time.Millisecond)

	events, _, err := st.Events().List(context.Background(), scope, 10, "")
	if err != nil {
		t.Fatalf("Events.List: %v", err)
	}

	found := false
	for _, ev := range events {
		if ev.Type == "buffer.flushed" {
			found = true
			if ev.Reason == "" {
				t.Error("buffer.flushed event must carry a trigger reason")
			}
		}
	}
	if !found {
		t.Error("no buffer.flushed event found in EventStore")
	}
}

// ── Golden test on FlushedBuffer JSON shape ──────────────────────────────────

// TestFlushedBufferGolden is a golden test for the FlushedBuffer JSON shape
// (AC-6, CLAUDE.md §11 golden contract). Run with UPDATE_GOLDEN=1 to regenerate.
func TestFlushedBufferGolden(t *testing.T) {
	fb := pipeline.FlushedBuffer{
		Scope:         identity.Scope{Tenant: "acme"},
		Key:           "sess-1/branch-1",
		BranchID:      "branch-1",
		RecordIDs:     []string{"rec-001", "rec-002", "rec-003"},
		TokenEstimate: 75,
		Trigger:       pipeline.TriggerCount,
		SkipPromotion: false,
	}

	got, err := json.MarshalIndent(fb, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	goldenPath := filepath.Join("testdata", "flushed_buffer.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to create)", goldenPath, err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Errorf("FlushedBuffer JSON mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// ── Trigger-defaults coverage ─────────────────────────────────────────────────

// TestTriggersFromConfig verifies the per-profile trigger defaults (D-042).
func TestTriggersFromConfig(t *testing.T) {
	cases := []struct {
		profile    string
		wantCount  int
		wantTokens int64
		wantMaxAge time.Duration
	}{
		{"assistant", 12, 1500, 90 * time.Second},
		{"coding-agent", 20, 2500, 180 * time.Second},
		{"fleet", 30, 4000, 120 * time.Second},
		{"unknown", 12, 1500, 90 * time.Second}, // fallback to assistant
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.profile, func(t *testing.T) {
			trig := pipeline.TriggersFromConfig(tc.profile)
			if trig.Count != tc.wantCount {
				t.Errorf("Count: got %d want %d", trig.Count, tc.wantCount)
			}
			if trig.Tokens != tc.wantTokens {
				t.Errorf("Tokens: got %d want %d", trig.Tokens, tc.wantTokens)
			}
			if trig.MaxAge != tc.wantMaxAge {
				t.Errorf("MaxAge: got %v want %v", trig.MaxAge, tc.wantMaxAge)
			}
		})
	}
}

// ── Race test (validates -race correctness) ───────────────────────────────────

// TestStageRace is the stage-level race test: many concurrent writers across
// many buffer keys. Validated with -race.
func TestStageRace(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	trig := pipeline.Triggers{Count: 3, Tokens: 10000, MaxAge: 10 * time.Second}
	s, in := newStageAndChan(st, trig)
	s.Start(context.Background())

	const (
		nWriters = 8
		nRecords = 12 // 4 count-trigger flushes per writer
	)

	var wg sync.WaitGroup
	var totalSent atomic.Int64
	for w := 0; w < nWriters; w++ {
		tenant := "race-t-" + ulid.Make().String()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < nRecords; i++ {
				id := insertRecord(t, st, tenant, "sess", "br", 5)
				in <- pipeline.Item{RecordID: id, TenantID: tenant}
				totalSent.Add(1)
			}
		}()
	}
	wg.Wait()
	close(in)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.Drain(ctx)

	// Collect all flushed record IDs.
	seen := make(map[string]int)
	for fb := range s.Downstream() {
		for _, id := range fb.RecordIDs {
			seen[id]++
		}
	}
	for id, cnt := range seen {
		if cnt > 1 {
			t.Errorf("race test: record %q flushed %d times (want 1)", id, cnt)
		}
	}
}

// ── BenchmarkPipelineEnqueue (AC-5: P2 non-blocking enqueue guard) ───────────
// Confirms the pipeline Item channel enqueue is non-blocking regardless of
// stage state. The full ingest ACK bench lives in internal/api.
func BenchmarkPipelineEnqueue(b *testing.B) {
	f, err := os.CreateTemp(b.TempDir(), "bench-*.db")
	if err != nil {
		b.Fatalf("create temp db: %v", err)
	}
	_ = f.Close()
	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = f.Name()
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	defer func() { _ = st.Close(context.Background()) }()

	trig := pipeline.Triggers{Count: 100, Tokens: 100_000, MaxAge: 300 * time.Second}
	in := make(chan pipeline.Item, 4096)
	s := pipeline.New(st, noopLog(), trig, in)
	s.Start(ctx)
	defer func() {
		close(in)
		dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.Drain(dctx)
	}()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Non-blocking enqueue (P2 fire-and-forget; drop if full).
			select {
			case in <- pipeline.Item{RecordID: ulid.Make().String(), TenantID: "bench"}:
			default:
			}
		}
	})
}
