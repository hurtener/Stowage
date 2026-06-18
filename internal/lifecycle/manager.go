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

	"github.com/hurtener/stowage/internal/gateway"
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
	ConfirmInterval   time.Duration // default 10m (Phase 18)

	DecayBatchSize     int // memories per pass; default 200
	DedupeBatchSize    int // comparisons per pass; default 200
	RollupBatchSize    int // sessions per pass; default 50
	ReenqueueBatchSize int // records per pass; default 100
	ConfirmBatchSize   int // parked memories per pass; default 100 (Phase 18)

	// RollupAge is the age threshold for rolling up session working memories.
	// Sessions older than this are eligible for rollup (default 7 days).
	RollupAge time.Duration

	// ReenqueueDeadline is the age after which an unprocessed record is
	// considered stalled and eligible for re-enqueue (default 10m).
	ReenqueueDeadline time.Duration

	// DecayGraceSweeps is the number of consecutive below-floor sweeps before
	// a memory is expired (D-058). Default 2.
	DecayGraceSweeps int

	// ConfirmTTL is the age at which a pending_confirmation memory is
	// automatically promoted to active (Phase 18, D-065). Default 72h.
	// Per D-065: the newer memory wins after the review window lapses.
	ConfirmTTL time.Duration

	// ConfirmRepeats is the match_count threshold at which a parked memory is
	// promoted early via repeated-independent-extraction (Phase 18, D-065).
	// Default 2.
	ConfirmRepeats int

	// Reflection sweep tuning (Phase 19, D-077). The sweep is only registered when
	// SetReflection has wired a gateway + reconcile-input channel (boot does this
	// only for the fleet profile by default — config.ReflectConfigForProfile).
	ReflectInterval   time.Duration // default 30m
	ReflectBatchSize  int           // outcome-tagged records per scope per sweep; default 200
	ReflectEpochEvery int           // every Nth interval re-reflects the trailing window; default 8

	// Episode sweep tuning (Phase 22, D-079). Registered only when SetEpisodes has
	// wired a gateway (boot does this per config.EpisodeConfigForProfile).
	EpisodeDetectInterval  time.Duration // default 15m
	EpisodeNarrateInterval time.Duration // default 15m
	EpisodeIdleWindow      time.Duration // a session with no records for this long is "closed"; default 30m
	EpisodeGapSplit        time.Duration // intra-session gap that splits an episode; default 0 (off, v1)
	EpisodeBatchSize       int           // sessions/episodes per sweep; default 100
}

// DefaultProfile returns the profile with sensible production defaults.
func DefaultProfile() Profile {
	return Profile{
		DecayInterval:     10 * time.Minute,
		DedupeInterval:    30 * time.Minute,
		RollupInterval:    60 * time.Minute,
		ReenqueueInterval: 2 * time.Minute,
		ConfirmInterval:   10 * time.Minute,

		DecayBatchSize:     200,
		DedupeBatchSize:    200,
		RollupBatchSize:    50,
		ReenqueueBatchSize: 100,
		ConfirmBatchSize:   100,

		RollupAge:         7 * 24 * time.Hour,
		ReenqueueDeadline: 10 * time.Minute,
		DecayGraceSweeps:  2,
		ConfirmTTL:        72 * time.Hour, // D-065: 72 h review window
		ConfirmRepeats:    2,              // D-065: 2 independent extractions promote early

		ReflectInterval:   30 * time.Minute,
		ReflectBatchSize:  200,
		ReflectEpochEvery: 8,

		EpisodeDetectInterval:  15 * time.Minute,
		EpisodeNarrateInterval: 15 * time.Minute,
		EpisodeIdleWindow:      30 * time.Minute,
		EpisodeGapSplit:        0,
		EpisodeBatchSize:       100,
	}
}

// Manager runs the lifecycle sweeps.
type Manager struct {
	st      store.Store
	log     *slog.Logger
	profile Profile
	ingest  chan<- pipeline.Item // re-enqueue target

	// Reflection wiring (Phase 19, D-077). Set via SetReflection; when both are
	// non-nil the reflect sweep is registered. nil → reflection off (the default
	// for single-user profiles and every caller that doesn't opt in).
	gw         gateway.Gateway
	reflectOut chan<- pipeline.CandidateBatch

	// Episode wiring (Phase 22, D-079). Set via SetEpisodes; when enabled the
	// detect + narrate sweeps are registered. Shares the gateway handle (gw) with
	// reflection — either setter may supply it.
	episodesEnabled bool

	wg     sync.WaitGroup
	stopCh chan struct{}
}

// SetEpisodes enables the episode detect + narrate sweeps, wiring the gateway the
// narration sweep calls. Must be called before Start. When unset, the episode
// sweeps are not registered (Phase 22, D-079).
func (m *Manager) SetEpisodes(gw gateway.Gateway) {
	m.gw = gw
	m.episodesEnabled = true
}

func (m *Manager) episodesOn() bool {
	return m.episodesEnabled && m.gw != nil && m.profile.EpisodeDetectInterval > 0
}

// SetReflection wires the reflection sweep's dependencies: the gateway it calls
// to distill strategy/failure_mode candidates, and the reconcile-input channel it
// emits CandidateBatches into. Must be called before Start. When unset, the
// reflection sweep is not registered (Phase 19, D-077).
func (m *Manager) SetReflection(gw gateway.Gateway, reflectOut chan<- pipeline.CandidateBatch) {
	m.gw = gw
	m.reflectOut = reflectOut
}

// reflectionEnabled reports whether the reflect sweep should run.
func (m *Manager) reflectionEnabled() bool {
	return m.gw != nil && m.reflectOut != nil && m.profile.ReflectInterval > 0
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
	if p.ConfirmInterval <= 0 {
		p.ConfirmInterval = 10 * time.Minute
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
	if p.ConfirmBatchSize <= 0 {
		p.ConfirmBatchSize = 100
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
	if p.ConfirmTTL <= 0 {
		p.ConfirmTTL = 72 * time.Hour // D-065: 72 h review window
	}
	if p.ConfirmRepeats <= 0 {
		p.ConfirmRepeats = 2 // D-065: 2 independent extractions promote early
	}
	if p.ReflectInterval <= 0 {
		p.ReflectInterval = 30 * time.Minute
	}
	if p.ReflectBatchSize <= 0 {
		p.ReflectBatchSize = 200
	}
	if p.ReflectEpochEvery <= 0 {
		p.ReflectEpochEvery = 8
	}
	if p.EpisodeDetectInterval <= 0 {
		p.EpisodeDetectInterval = 15 * time.Minute
	}
	if p.EpisodeNarrateInterval <= 0 {
		p.EpisodeNarrateInterval = 15 * time.Minute
	}
	if p.EpisodeIdleWindow <= 0 {
		p.EpisodeIdleWindow = 30 * time.Minute
	}
	if p.EpisodeBatchSize <= 0 {
		p.EpisodeBatchSize = 100
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
	m.startSweep(ctx, "confirm", m.profile.ConfirmInterval, m.runConfirm)
	if m.reflectionEnabled() {
		m.startSweep(ctx, "reflect", m.profile.ReflectInterval, m.runReflect)
	}
	if m.episodesOn() {
		m.startSweep(ctx, "episode-detect", m.profile.EpisodeDetectInterval, m.runDetectEpisodes)
		m.startSweep(ctx, "episode-narrate", m.profile.EpisodeNarrateInterval, m.runNarrateEpisodes)
	}
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

// RunForce synchronously executes all five sweeps once (for testing/smoke).
// Activated by STOWAGE_SWEEP_FORCE env var check in main.go.
func (m *Manager) RunForce(ctx context.Context) {
	m.log.InfoContext(ctx, "lifecycle: forced sweep run (all sweeps)")
	m.runDecay(ctx)
	m.runDedupe(ctx)
	m.runRollup(ctx)
	m.runReenqueue(ctx)
	m.runConfirm(ctx)
	if m.reflectionEnabled() {
		m.runReflect(ctx)
	}
	if m.episodesOn() {
		m.runDetectEpisodes(ctx)
		m.runNarrateEpisodes(ctx)
	}
}
