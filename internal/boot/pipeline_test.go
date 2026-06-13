package boot_test

import (
	"context"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
)

// TestStartPipeline_NilStack verifies the nil-Stack guard returns an error
// rather than panicking.
func TestStartPipeline_NilStack(t *testing.T) {
	t.Parallel()
	if _, err := boot.StartPipeline(context.Background(), nil, config.Config{}); err == nil {
		t.Fatal("StartPipeline(nil stack): expected error, got nil")
	}
}

// startedStack opens a Stack from a valid sqlite+mock config; the caller drains
// the returned pipeline and closes the stack.
func startedStack(t *testing.T) (*boot.Stack, func()) {
	t.Helper()
	cfg := validCfg(t)
	stk, err := boot.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("boot.Open: %v", err)
	}
	return stk, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = stk.Close(ctx)
	}
}

// TestStartPipeline_StartAndDrain exercises the full StartPipeline wiring and a
// clean Drain, then proves Drain is idempotent (second call is a no-op, no panic
// on the already-closed channel).
func TestStartPipeline_StartAndDrain(t *testing.T) {
	stk, closeStack := startedStack(t)
	defer closeStack()

	p, err := boot.StartPipeline(context.Background(), stk, *validCfg(t))
	if err != nil {
		t.Fatalf("StartPipeline: %v", err)
	}
	if p.In == nil {
		t.Fatal("StartPipeline: In channel is nil")
	}
	if p.Stage == nil {
		t.Fatal("StartPipeline: Stage is nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// Idempotent: a second Drain must not panic (channel already closed) or error.
	if err := p.Drain(ctx); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
}

// TestStartPipeline_SweepForce covers the STOWAGE_SWEEP_FORCE branch, which runs
// every lifecycle sweep once synchronously before returning (now uniform across
// all live paths, D-068).
func TestStartPipeline_SweepForce(t *testing.T) {
	t.Setenv("STOWAGE_SWEEP_FORCE", "1")
	stk, closeStack := startedStack(t)
	defer closeStack()

	p, err := boot.StartPipeline(context.Background(), stk, *validCfg(t))
	if err != nil {
		t.Fatalf("StartPipeline (sweep-force): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := p.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}
