package gateway

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Meter records token usage and cost for every provider round-trip (CLAUDE.md §10).
// Phase 05 wires the Meter to the event store; this phase ships PromMeter.
type Meter interface {
	Record(ctx context.Context, op, model string, usage Usage)
	RecordRerank(ctx context.Context, model string, usage RerankUsage)
}

// PromMeter records usage as Prometheus counters and slog debug lines.
// Keys are never logged (CLAUDE.md §7).
type PromMeter struct {
	log          *slog.Logger
	inputTokens  *prometheus.CounterVec
	outputTokens *prometheus.CounterVec
	costUSD      *prometheus.CounterVec
	calls        *prometheus.CounterVec
	searchUnits  *prometheus.CounterVec
	rerankCalls  *prometheus.CounterVec
}

// NewPromMeter returns a Meter backed by a scoped Prometheus registry and slog.
func NewPromMeter(log *slog.Logger, prom *prometheus.Registry) *PromMeter {
	f := promauto.With(prom)
	return &PromMeter{
		log: log,
		inputTokens: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_input_tokens_total",
			Help: "Total input tokens sent to the intelligence provider.",
		}, []string{"op", "model"}),
		outputTokens: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_output_tokens_total",
			Help: "Total output tokens received from the intelligence provider.",
		}, []string{"op", "model"}),
		costUSD: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_cost_usd_total",
			Help: "Estimated cost in USD for gateway provider calls.",
		}, []string{"op", "model"}),
		calls: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_calls_total",
			Help: "Total number of gateway provider round-trips.",
		}, []string{"op", "model"}),
		searchUnits: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_rerank_search_units_total",
			Help: "Total rerank search units consumed (Cohere billing unit).",
		}, []string{"model"}),
		rerankCalls: f.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_rerank_calls_total",
			Help: "Total number of rerank provider round-trips.",
		}, []string{"model"}),
	}
}

// RecordRerank increments rerank Prometheus counters and emits a debug log line.
func (m *PromMeter) RecordRerank(ctx context.Context, model string, usage RerankUsage) {
	m.rerankCalls.WithLabelValues(model).Inc()
	m.searchUnits.WithLabelValues(model).Add(float64(usage.SearchUnits))
	if usage.CostUSD > 0 {
		m.costUSD.WithLabelValues("rerank", model).Add(usage.CostUSD)
	}
	m.log.LogAttrs(ctx, slog.LevelDebug, "gateway.rerank",
		slog.String("model", model),
		slog.Int("search_units", usage.SearchUnits),
		slog.Float64("cost_usd", usage.CostUSD),
	)
}

// Record increments Prometheus counters and emits a debug log line.
func (m *PromMeter) Record(ctx context.Context, op, model string, usage Usage) {
	m.calls.WithLabelValues(op, model).Inc()
	m.inputTokens.WithLabelValues(op, model).Add(float64(usage.InputTokens))
	m.outputTokens.WithLabelValues(op, model).Add(float64(usage.OutputTokens))
	m.costUSD.WithLabelValues(op, model).Add(usage.CostUSD)
	m.log.LogAttrs(ctx, slog.LevelDebug, "gateway.call",
		slog.String("op", op),
		slog.String("model", model),
		slog.Int("input_tokens", usage.InputTokens),
		slog.Int("output_tokens", usage.OutputTokens),
		slog.Float64("cost_usd", usage.CostUSD),
	)
}
