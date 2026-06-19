package retrieval

import (
	"log/slog"
	"time"

	"github.com/hurtener/stowage/internal/store"
)

// ExportRRF exposes the internal rrf function for benchmarking.
func ExportRRF(lanes map[string][]string) []FusedHit {
	return rrf(lanes)
}

// ExportQuerySig exposes QuerySig for testing.
func ExportQuerySig(tokens []string) string { return QuerySig(tokens) }

// ExportHubWindowMs exposes the durable hub recency window for testing (D-092).
func ExportHubWindowMs() int64 { return hubWindowMs }

// InjWr exposes the InjectionWriter for fault-hook testing (TEST-ONLY).
// Returns nil when injections are not wired.
func (r *Retriever) InjWr() *InjectionWriter { return r.injWr }

// NewInjectionWriterForTest creates an InjectionWriter with a custom channel
// capacity. TEST-ONLY — production code always uses the standard cap (512).
func NewInjectionWriterForTest(inj store.InjectionStore, log *slog.Logger, cap int) *InjectionWriter {
	w := &InjectionWriter{
		inj:  inj,
		ch:   make(chan injBatch, cap),
		done: make(chan struct{}),
		log:  log.With("subsystem", "injection_writer_test"),
	}
	go w.loop()
	return w
}

// ExportNewResultCache creates a ResultCache for testing.
func ExportNewResultCache(cap int) *ResultCache { return NewResultCache(cap) }

// ExportNewHotSet creates a HotSet for testing.
func ExportNewHotSet(cap int) *HotSet { return NewHotSet(cap) }

// SetTestNow sets a custom clock on the ResultCache for TTL testing.
func (c *ResultCache) SetTestNow(fn func() time.Time) { c.now = fn }

// CacheOf returns the Retriever's ResultCache.
func (r *Retriever) CacheOf() *ResultCache { return r.cache }

// HotSetOf returns the Retriever's HotSet.
func (r *Retriever) HotSetOf() *HotSet { return r.hotSet }
