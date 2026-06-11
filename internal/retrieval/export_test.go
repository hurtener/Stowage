package retrieval

import (
	"log/slog"

	"github.com/hurtener/stowage/internal/store"
)

// ExportRRF exposes the internal rrf function for benchmarking.
func ExportRRF(lanes map[string][]string) []FusedHit {
	return rrf(lanes)
}

// ExportNewHub creates a Hub with the given maxSize for testing.
func ExportNewHub(maxSize int) *Hub { return NewHub(maxSize) }

// ExportQuerySig exposes QuerySig for testing.
func ExportQuerySig(tokens []string) string { return QuerySig(tokens) }

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
