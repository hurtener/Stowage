// Package lifecycle implements the Stowage maintenance sweeps (Phase 14).
// Four sweeps run on jittered tickers: decay, dedupe, rollup, re-enqueue.
// Each sweep is idempotent, singleflight (pg advisory lock / sqlite no-op),
// and fully evented (prior-state snapshots per D-017).
package lifecycle

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// Profile holds per-sweep interval configuration (D-034 knob guardrail).
// All intervals are base values; each sweep adds up to 50% jitter.
type Profile struct {
	DecayInterval     time.Duration // default 10m
	DedupeInterval    time.Duration // default 30m
	RollupInterval    time.Duration // default 60m
	ReenqueueInterval time.Duration // default 2m

	DecayBatchSize     int // memories per pass; default 200
	DedupeBatchSize    int // comparisons per pass; default 200
	RollupBatchSize    int // sessions per pass; default 50
	ReenqueueBatchSize int // records per pass; default 100

	// RollupAge is the age threshold for rolling up session working memories.
	// Sessions older than this are eligible for rollup (default 7 days).
	RollupAge time.Duration

	// ReenqueueDeadline is the age after which an unprocessed record is
	// considered stalled and eligible for re-enqueue (default 10m).
	ReenqueueDeadline time.Duration

	// DecayGraceSweeps is the number of consecutive below-floor sweeps before
	// a memory is expired (D-058). Default 2.
	DecayGraceSweeps int
}

// DefaultProfile returns the profile with sensible production defaults.
func DefaultProfile() Profile {
	return Profile{
		DecayInterval:     10 * time.Minute,
		DedupeInterval:    30 * time.Minute,
		RollupInterval:    60 * time.Minute,
		ReenqueueInterval: 2 * time.Minute,

		DecayBatchSize:     200,
		DedupeBatchSize:    200,
		RollupBatchSize:    50,
		ReenqueueBatchSize: 100,

		RollupAge:         7 * 24 * time.Hour,
		ReenqueueDeadline: 10 * time.Minute,
		DecayGraceSweeps:  2,
	}
}

// Manager runs the four lifecycle sweeps.
type Manager struct {
	st      store.Store
	log     *slog.Logger
	profile Profile
	ingest  chan<- pipeline.Item // re-enqueue target

	wg     sync.WaitGroup
	stopCh chan struct{}
}

// New creates a Manager. Call Start to begin sweeps.
// ingest is the write-end of the pipeline ingest channel (for re-enqueue).
func New(st store.Store, log *slog.Logger, profile Profile, ingest chan<- pipeline.Item) *Manager {
	p := profile
	// Apply defaults for zero values.
	if p.DecayInterval <= 0 {
		p.DecayInterval = 10 * time.Minute
	}
	if p.DedupeInterval <= 0 {
		p.DedupeInterval = 30 * time.Minute
	}
	if p.RollupInterval <= 0 {
		p.RollupInterval = 60 * time.Minute
	}
	if p.ReenqueueInterval <= 0 {
		p.ReenqueueInterval = 2 * time.Minute
	}
	if p.DecayBatchSize <= 0 {
		p.DecayBatchSize = 200
	}
	if p.DedupeBatchSize <= 0 {
		p.DedupeBatchSize = 200
	}
	if p.RollupBatchSize <= 0 {
		p.RollupBatchSize = 50
	}
	if p.ReenqueueBatchSize <= 0 {
		p.ReenqueueBatchSize = 100
	}
	if p.RollupAge <= 0 {
		p.RollupAge = 7 * 24 * time.Hour
	}
	if p.ReenqueueDeadline <= 0 {
		p.ReenqueueDeadline = 10 * time.Minute
	}
	if p.DecayGraceSweeps <= 0 {
		p.DecayGraceSweeps = 2
	}
	return &Manager{
		st:      st,
		log:     log.With("subsystem", "lifecycle"),
		profile: p,
		ingest:  ingest,
		stopCh:  make(chan struct{}),
	}
}

// Start launches one goroutine per sweep.
func (m *Manager) Start(ctx context.Context) {
	m.startSweep(ctx, "decay", m.profile.DecayInterval, m.runDecay)
	m.startSweep(ctx, "dedupe", m.profile.DedupeInterval, m.runDedupe)
	m.startSweep(ctx, "rollup", m.profile.RollupInterval, m.runRollup)
	m.startSweep(ctx, "reenqueue", m.profile.ReenqueueInterval, m.runReenqueue)
}

// Stop signals all sweeps to stop and waits for them to finish.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

func (m *Manager) startSweep(ctx context.Context, name string, base time.Duration, fn func(context.Context)) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			// Jitter: up to 50% of base (non-crypto; D-034 knob guardrail).
			baseMs := int64(base / time.Millisecond)
			jitterMs := int64(1)
			if half := baseMs / 2; half > 1 {
				jitterMs = rand.Int64N(half) //nolint:gosec
			}
			delay := base + time.Duration(jitterMs)*time.Millisecond
			select {
			case <-m.stopCh:
				return
			case <-time.After(delay):
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						m.log.ErrorContext(ctx, "lifecycle: sweep panic", "sweep", name, "panic", r)
					}
				}()
				fn(ctx)
			}()
		}
	}()
}

// RunForce synchronously executes all four sweeps once (for testing/smoke).
// Activated by STOWAGE_SWEEP_FORCE env var check in main.go.
func (m *Manager) RunForce(ctx context.Context) {
	m.log.InfoContext(ctx, "lifecycle: forced sweep run (all sweeps)")
	m.runDecay(ctx)
	m.runDedupe(ctx)
	m.runRollup(ctx)
	m.runReenqueue(ctx)
}
