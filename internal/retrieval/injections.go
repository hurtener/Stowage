package retrieval

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

const (
	// injectionWriterCap is the channel capacity before drops start.
	// Sized to absorb a reasonable burst without blocking Retrieve.
	injectionWriterCap = 512
)

// injBatch is one enqueue unit: a batch of rows for one Retrieve call.
type injBatch struct {
	scope identity.Scope
	rows  []store.Injection
}

// InjectionWriter is the bounded async writer for injection rows (D-025, D-051).
//
// Enqueue is non-blocking: when the channel is full, the batch is dropped and
// drops counter is incremented. The writer goroutine batches store.Append calls.
// Close drains the channel and waits for the goroutine to exit. Close is
// idempotent — calling it multiple times is safe.
//
// P2: Retrieve never blocks waiting for the injection write to complete.
type InjectionWriter struct {
	inj       store.InjectionStore
	ch        chan injBatch
	done      chan struct{}
	closeOnce sync.Once
	drops     atomic.Int64 // monotonic; exported via Drops()
	log       *slog.Logger

	// FaultHook is TEST-ONLY. If non-nil it is called inside the writer loop
	// before each Append, allowing tests to inject failures or stalls.
	// MUST be nil in all production code paths.
	FaultHook func() error
}

// NewInjectionWriter starts the async writer goroutine and returns the writer.
// Callers must call Close during shutdown to drain pending writes.
func NewInjectionWriter(inj store.InjectionStore, log *slog.Logger) *InjectionWriter {
	w := &InjectionWriter{
		inj:  inj,
		ch:   make(chan injBatch, injectionWriterCap),
		done: make(chan struct{}),
		log:  log.With("subsystem", "injection_writer"),
	}
	go w.loop()
	return w
}

// Enqueue non-blockingly enqueues a batch for the given scope.
// Drops the batch (incrementing Drops) when the channel is full.
func (w *InjectionWriter) Enqueue(scope identity.Scope, rows []store.Injection) {
	if len(rows) == 0 {
		return
	}
	select {
	case w.ch <- injBatch{scope: scope, rows: rows}:
	default:
		w.drops.Add(1)
		w.log.Warn("injection writer: channel full — batch dropped",
			"dropped_rows", len(rows), "total_drops", w.drops.Load())
	}
}

// Drops returns the total number of dropped rows since the writer was created.
func (w *InjectionWriter) Drops() int64 { return w.drops.Load() }

// Close closes the input channel and waits for the writer goroutine to drain.
// All batches queued before Close is called are written before the goroutine exits.
// Calling Close more than once is safe (idempotent).
func (w *InjectionWriter) Close() {
	w.closeOnce.Do(func() {
		close(w.ch)
		<-w.done
	})
}

// loop is the writer goroutine. Reads batches from ch and calls Append.
// Uses context.Background because these writes are fire-and-forget and must
// survive the caller's request context cancellation (P2).
func (w *InjectionWriter) loop() {
	defer close(w.done)
	for b := range w.ch {
		if w.FaultHook != nil {
			if err := w.FaultHook(); err != nil {
				w.log.Warn("injection writer: fault hook triggered", "err", err)
				continue
			}
		}
		if err := w.inj.Append(context.Background(), b.scope, b.rows); err != nil { //nolint:contextcheck
			w.log.Warn("injection writer: Append failed", "err", err)
		}
	}
}
