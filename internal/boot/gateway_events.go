package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// gatewayUsageEmitter implements gateway.UsageEventEmitter by writing a
// `gateway.call` / `gateway.rerank` event to the store event stream (§8/§10 —
// every gateway call is metered AND emitted, so cost governance and the audit
// trail see provider usage). It attributes the event to the scope carried in ctx;
// a call made on a scope-less background ctx (the events table requires a tenant)
// is skipped with a debug log rather than mis-attributed.
type gatewayUsageEmitter struct {
	events store.EventStore
	log    *slog.Logger
}

func (g *gatewayUsageEmitter) emit(ctx context.Context, typ, op, model, payload string) {
	scope, err := identity.FromContext(ctx)
	if err != nil || scope.Tenant == "" {
		// Scope-less (background) gateway call — no tenant to attribute. The Prom
		// counters still record it; the event stream is tenant-scoped by design.
		g.log.LogAttrs(ctx, slog.LevelDebug, "gateway usage event skipped (no scope in ctx)",
			slog.String("op", op), slog.String("model", model))
		return
	}
	if err := g.events.Emit(ctx, scope, store.Event{
		ID: ulid.Make().String(), Type: typ, SubjectID: op,
		Reason: "gateway call metered", Payload: payload, CreatedAt: 0,
	}); err != nil {
		g.log.WarnContext(ctx, "gateway usage event emit failed", "op", op, "err", err)
	}
}

func (g *gatewayUsageEmitter) EmitUsage(ctx context.Context, op, model string, inputTokens, outputTokens int, costUSD float64) {
	payload, _ := json.Marshal(struct {
		Op           string  `json:"op"`
		Model        string  `json:"model"`
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		CostUSD      float64 `json:"cost_usd"`
	}{op, model, inputTokens, outputTokens, costUSD})
	g.emit(ctx, "gateway.call", op, model, string(payload))
}

func (g *gatewayUsageEmitter) EmitRerankUsage(ctx context.Context, model string, searchUnits int, costUSD float64) {
	payload, _ := json.Marshal(struct {
		Op          string  `json:"op"`
		Model       string  `json:"model"`
		SearchUnits int     `json:"search_units"`
		CostUSD     float64 `json:"cost_usd"`
	}{"rerank", model, searchUnits, costUSD})
	g.emit(ctx, "gateway.rerank", "rerank", model, string(payload))
}

// checkEmbedModel returns an error if any persisted embedding model differs from the
// configured one — a model change requires an explicit reindex, never a silent mix of
// incompatible embeddings (§10 gateway-seam rule).
func checkEmbedModel(persisted []string, configured string) error {
	for _, m := range persisted {
		if m != configured {
			return fmt.Errorf("embedding model mismatch: vectors persisted with %q but config.gateway.embed_model=%q — a model change requires an explicit reindex (re-embed); refusing to serve mixed embeddings (§10)", m, configured)
		}
	}
	return nil
}

// wireGatewayUsageEvents wires the usage-event emitter onto the gateway's meter when
// the driver supports it (type assertion — keeps the optional capability off the
// Gateway interface so test fakes need not implement it).
func wireGatewayUsageEvents(gw gateway.Gateway, events store.EventStore, log *slog.Logger) {
	if s, ok := gw.(interface {
		SetUsageEmitter(gateway.UsageEventEmitter)
	}); ok {
		s.SetUsageEmitter(&gatewayUsageEmitter{events: events, log: log})
	}
}
