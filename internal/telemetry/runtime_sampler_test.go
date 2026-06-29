package telemetry_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/telemetry"
)

// String returns a snapshot of the buffer contents, safe for concurrent use.
// Defined here to extend the syncBuffer declared in telemetry_test.go without
// modifying that file.
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRuntimeSamplerEmits verifies that a sampler with a positive interval logs
// at least one "runtime.sample" line with a goroutines attribute after one tick.
func TestRuntimeSamplerEmits(t *testing.T) {
	var buf syncBuffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(base)

	sampler := telemetry.NewRuntimeSampler(logger, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sampler.Start(ctx)

	// Wait long enough for at least one tick (20 ms) with comfortable headroom.
	time.Sleep(120 * time.Millisecond)

	if err := sampler.Close(context.Background()); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "runtime.sample") {
		t.Errorf("expected at least one runtime.sample log line; got:\n%s", out)
	}
	if !strings.Contains(out, "goroutines=") {
		t.Errorf("expected goroutines attribute in log output; got:\n%s", out)
	}
}

// TestRuntimeSamplerDisabled verifies that interval <= 0 is a no-op: no goroutine
// is launched, no log lines are emitted, and Close still returns nil.
func TestRuntimeSamplerDisabled(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(base)

	sampler := telemetry.NewRuntimeSampler(logger, 0)
	ctx := context.Background()
	sampler.Start(ctx)

	// Give any inadvertently-launched goroutine a chance to fire.
	time.Sleep(30 * time.Millisecond)

	if err := sampler.Close(context.Background()); err != nil {
		t.Fatalf("Close() on disabled sampler error: %v", err)
	}

	if buf.Len() > 0 {
		t.Errorf("disabled sampler should not log anything; got:\n%s", buf.String())
	}
}

// TestRuntimeSamplerCloseIdempotent verifies that calling Close twice does not
// panic and that the goroutine has stopped (no further log lines after Close).
func TestRuntimeSamplerCloseIdempotent(t *testing.T) {
	var buf syncBuffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(base)

	sampler := telemetry.NewRuntimeSampler(logger, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sampler.Start(ctx)

	// Let at least one tick fire.
	time.Sleep(60 * time.Millisecond)

	// First Close — goroutine must exit.
	if err := sampler.Close(context.Background()); err != nil {
		t.Fatalf("first Close() error: %v", err)
	}

	// Snapshot log output immediately after Close.
	snapshot := buf.String()

	// Second Close — must not panic.
	if err := sampler.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error: %v", err)
	}

	// Wait a full extra tick interval and verify no new lines were appended.
	time.Sleep(60 * time.Millisecond)
	after := buf.String()
	if after != snapshot {
		t.Errorf("goroutine continued logging after Close:\nbefore:\n%s\nafter:\n%s", snapshot, after)
	}
}
