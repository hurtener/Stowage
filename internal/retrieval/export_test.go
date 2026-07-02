package retrieval

import (
	"context"
	"log/slog"
	"time"

	"github.com/hurtener/stowage/internal/identity"
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

// ExportFilterByTopicOwnScope exposes filterByTopicOwnScope (ae6, D-139/D-144) for
// testing the own-scope fail-OPEN topic filter directly.
func (r *Retriever) ExportFilterByTopicOwnScope(
	ctx context.Context, scope identity.Scope, ids []string, include, exclude []string,
) ([]string, bool) {
	return r.filterByTopicOwnScope(ctx, scope, ids, include, exclude)
}

// ExportFilterByTopic exposes grants' filterByTopic (fail-CLOSED) so tests can
// directly contrast its error semantics against ExportFilterByTopicOwnScope's
// fail-OPEN semantics on the SAME injected error (D-139).
func (r *Retriever) ExportFilterByTopic(
	ctx context.Context, scope identity.Scope, mems []store.Memory, topicKey string,
) []store.Memory {
	return r.filterByTopic(ctx, scope, mems, topicKey)
}

// ExportTopicFilterScoringK exposes the resolved topicFilterScoringK (falling back to
// defaultTopicFilterScoringK when unset) for testing the D-144 widening knob.
func (r *Retriever) ExportTopicFilterScoringK() int {
	if r.topicFilterScoringK <= 0 {
		return defaultTopicFilterScoringK
	}
	return r.topicFilterScoringK
}

// ExportResolveAgentTopics exposes resolveAgentTopics (ae1, D-135/D-139/D-146) for
// testing the fail-open agent->topic resolver directly.
func (r *Retriever) ExportResolveAgentTopics(
	ctx context.Context, scope identity.Scope,
) (allow, deny []string, active, degraded bool) {
	return r.resolveAgentTopics(ctx, scope)
}

// ExportResolveAndApplyView exposes resolveAndApplyView (ae9, D-149/D-151) for
// testing the fail-open named-view apply path directly.
func (r *Retriever) ExportResolveAndApplyView(
	ctx context.Context, scope identity.Scope, req Request, ids []string,
) (kept []string, degraded bool) {
	return r.resolveAndApplyView(ctx, scope, req, ids)
}

// ExportHasViewApply exposes hasViewApply (ae9, D-149) for testing the
// result-cache bypass predicate directly.
func (r *Retriever) ExportHasViewApply(scope identity.Scope, req Request) bool {
	return r.hasViewApply(scope, req)
}
