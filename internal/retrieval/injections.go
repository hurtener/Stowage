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

// injBatch is one enqueue unit: a batch of rows for one Retrieve call, plus an
// optional response-keyed query event (Phase 26 trace capture, D-086) written in the
// same async pass so retrieve latency is unchanged.
type injBatch struct {
	scope identity.Scope
	rows  []store.Injection
	query *store.Event
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
	events    store.EventStore // optional (Phase 26 trace capture); nil ⇒ no query events
	counter   memoryCounter    // optional; nil ⇒ inject_count is not incremented
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

// SetEventStore enables Phase-26 trace capture: when set, an enqueued query event is
// written (async) alongside the injection rows. Call once at construction, before any
// Enqueue — not safe to call concurrently with Enqueue.
func (w *InjectionWriter) SetEventStore(es store.EventStore) { w.events = es }

// memoryCounter is the minimal slice of MemoryStore the writer needs to record that a
// memory was injected (the inject utility counter, D-008).
type memoryCounter interface {
	IncrementCounter(ctx context.Context, scope identity.Scope, id, counter string) error
}

// SetMemoryCounter enables inject_count tracking: when set, each distinct memory in an
// appended injection batch has its `inject` counter incremented (the precision /
// zombie-memory-killer signal — a memory injected but never used loses score, D-008).
// Call once at construction, before any Enqueue — not safe to call concurrently.
func (w *InjectionWriter) SetMemoryCounter(c memoryCounter) { w.counter = c }

// Enqueue non-blockingly enqueues a batch for the given scope, with an optional
// response-keyed query event (nil to skip). Drops the batch (incrementing Drops) when
// the channel is full.
func (w *InjectionWriter) Enqueue(scope identity.Scope, rows []store.Injection, queryEvent *store.Event) {
	if len(rows) == 0 && queryEvent == nil {
		return
	}
	select {
	case w.ch <- injBatch{scope: scope, rows: rows, query: queryEvent}:
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
		if len(b.rows) > 0 {
			if err := w.inj.Append(context.Background(), b.scope, b.rows); err != nil { //nolint:contextcheck
				w.log.Warn("injection writer: Append failed", "err", err)
			} else if w.counter != nil {
				// The injection rows are durable, so the inject happened: bump inject_count
				// once per DISTINCT memory in the batch (one response = one injection event
				// per memory). This is the precision-factor / zombie-memory-killer signal:
				// a memory injected-but-never-used decays in score (D-008, brief 02).
				// Each retrieve call mints fresh injection IDs, so a redelivered identical
				// batch (idempotent Append no-op) re-incrementing is not reachable in
				// practice; the counter is a soft signal where a rare over-count is benign.
				seen := make(map[string]struct{}, len(b.rows))
				for _, row := range b.rows {
					if row.MemoryID == "" {
						continue
					}
					if _, dup := seen[row.MemoryID]; dup {
						continue
					}
					seen[row.MemoryID] = struct{}{}
					if err := w.counter.IncrementCounter(context.Background(), b.scope, row.MemoryID, "inject"); err != nil { //nolint:contextcheck
						w.log.Warn("injection writer: inject_count increment failed", "memory_id", row.MemoryID, "err", err)
					}
				}
			}
		}
		// Phase-26 trace capture: persist the response-keyed query event (best-effort,
		// off the retrieve request path). Only when an event store is wired.
		if b.query != nil && w.events != nil {
			if err := w.events.Emit(context.Background(), b.scope, *b.query); err != nil { //nolint:contextcheck
				w.log.Warn("injection writer: query event emit failed", "err", err)
			}
		}
	}
}
