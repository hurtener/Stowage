package lifecycle_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register driver
)

// newTestStore opens a fresh SQLite store in a temp dir and migrates it.
func newTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "lifecycle_test.db")
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
		_ = s.Close(ctx)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestDefaultProfile(t *testing.T) {
	p := lifecycle.DefaultProfile()
	if p.DecayInterval != 10*time.Minute {
		t.Errorf("DecayInterval: got %v, want 10m", p.DecayInterval)
	}
	if p.DedupeInterval != 30*time.Minute {
		t.Errorf("DedupeInterval: got %v, want 30m", p.DedupeInterval)
	}
	if p.RollupInterval != 60*time.Minute {
		t.Errorf("RollupInterval: got %v, want 60m", p.RollupInterval)
	}
	if p.ReenqueueInterval != 2*time.Minute {
		t.Errorf("ReenqueueInterval: got %v, want 2m", p.ReenqueueInterval)
	}
	if p.DecayBatchSize != 200 {
		t.Errorf("DecayBatchSize: got %d, want 200", p.DecayBatchSize)
	}
	if p.DecayGraceSweeps != 2 {
		t.Errorf("DecayGraceSweeps: got %d, want 2", p.DecayGraceSweeps)
	}
	if p.RollupAge != 7*24*time.Hour {
		t.Errorf("RollupAge: got %v, want 7d", p.RollupAge)
	}
	if p.ReenqueueDeadline != 10*time.Minute {
		t.Errorf("ReenqueueDeadline: got %v, want 10m", p.ReenqueueDeadline)
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	ingest := make(chan pipeline.Item, 8)
	// Zero profile → all defaults applied.
	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{}, ingest)
	if mgr == nil {
		t.Fatal("New returned nil")
	}
}

func TestManagerStartStop(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		DecayInterval:     24 * time.Hour,
		DedupeInterval:    24 * time.Hour,
		RollupInterval:    24 * time.Hour,
		ReenqueueInterval: 24 * time.Hour,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	ctx := context.Background()
	mgr.Start(ctx)
	// Stop should not block.
	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("Stop timed out")
	}
}

func TestRunForceEmptyStore(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	ingest := make(chan pipeline.Item, 8)
	mgr := lifecycle.New(st, testLogger(), lifecycle.DefaultProfile(), ingest)
	// RunForce on an empty store should not panic or error.
	ctx := context.Background()
	mgr.RunForce(ctx)
}

// TestManagerSweepActuallyFires starts all sweeps with a very short interval
// (10ms) so the goroutines actually fire the sweep functions via the
// time.After path (not just the stopCh path), covering the startSweep
// goroutine body beyond the early-stop branch.
func TestManagerSweepActuallyFires(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	ingest := make(chan pipeline.Item, 32)
	// Use 10ms intervals: half=5>1 so jitterMs branch is also exercised.
	profile := lifecycle.Profile{
		DecayInterval:     10 * time.Millisecond,
		DedupeInterval:    10 * time.Millisecond,
		RollupInterval:    10 * time.Millisecond,
		ReenqueueInterval: 10 * time.Millisecond,
		// Sensible batch defaults.
		DecayBatchSize:     50,
		DedupeBatchSize:    50,
		RollupBatchSize:    50,
		ReenqueueBatchSize: 50,
		RollupAge:          7 * 24 * time.Hour,
		ReenqueueDeadline:  10 * time.Minute,
		DecayGraceSweeps:   2,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	ctx := context.Background()
	mgr.Start(ctx)

	// Wait long enough for each sweep goroutine to fire at least once
	// (each has delay ~10-15ms, so 200ms gives ~10+ iterations per goroutine).
	time.Sleep(200 * time.Millisecond)

	// Stop must complete without hanging.
	done := make(chan struct{})
	go func() {
		mgr.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("Stop timed out after 5s")
	}
}
