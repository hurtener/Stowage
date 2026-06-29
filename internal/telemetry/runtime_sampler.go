// Package telemetry — runtime_sampler.go
//
// RuntimeSampler periodically samples Go runtime statistics and emits them as
// a single structured slog line. It is the pull-independent complement to the
// GoCollector Prometheus gauges registered by New: where /metrics requires an
// external scraper, the sampler's log line is always present in the process log
// stream regardless of whether a scraper is attached — making goroutine counts
// and heap pressure visible in log-only deployments and in transient debugging
// sessions where Prometheus is unavailable.
//
// Custom Prometheus gauges are intentionally NOT registered here; the
// GoCollector added by New already exports them via the pull path. This
// sampler's sole responsibility is the push/log signal (D-126).
package telemetry

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"time"
)

// RuntimeSampler samples runtime.NumGoroutine and runtime.MemStats at a fixed
// interval and logs a structured line for each sample. A zero interval disables
// the sampler. Safe for concurrent use after construction; immutable once
// started.
type RuntimeSampler struct {
	log      *slog.Logger
	interval time.Duration

	stop chan struct{} // closed by Close to signal the goroutine
	once sync.Once     // ensures stop is closed at most once
	wg   sync.WaitGroup
}

// NewRuntimeSampler returns a RuntimeSampler that will log at the given
// interval. An interval <= 0 creates a disabled sampler; Start becomes a no-op
// and Close returns nil immediately.
func NewRuntimeSampler(log *slog.Logger, interval time.Duration) *RuntimeSampler {
	return &RuntimeSampler{
		log:      log,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start launches the background sampling goroutine. If interval <= 0 the
// sampler is disabled and Start is a no-op. ctx cancellation terminates the
// goroutine; Close also terminates and joins it. Start must not be called more
// than once on the same RuntimeSampler.
func (s *RuntimeSampler) Start(ctx context.Context) {
	if s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		tick := time.NewTicker(s.interval)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				var m runtime.MemStats
				n := runtime.NumGoroutine()
				runtime.ReadMemStats(&m)
				s.log.LogAttrs(ctx, slog.LevelInfo, "runtime.sample",
					slog.Int("goroutines", n),
					slog.Uint64("heap_alloc_bytes", m.HeapAlloc),
					slog.Uint64("heap_objects", m.HeapObjects),
					slog.Uint64("heap_sys_bytes", m.HeapSys),
					slog.Uint64("num_gc", uint64(m.NumGC)),
					slog.Uint64("gc_pause_total_ns", m.PauseTotalNs),
				)
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			}
		}
	}()
}

// Close signals the sampling goroutine to stop and waits for it to exit before
// returning. Idempotent: safe to call multiple times and safe to call when
// Start was a no-op. The ctx parameter matches the boot.Stack closer
// signature; this implementation does not block on ctx — the goroutine is
// joined unconditionally.
func (s *RuntimeSampler) Close(_ context.Context) error {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
	return nil
}
