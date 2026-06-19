package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// gatewayUsageEventCap bounds the async usage-event buffer before drops start.
const gatewayUsageEventCap = 512

type scopedEvent struct {
	scope identity.Scope
	ev    store.Event
}

// gatewayUsageEmitter implements gateway.UsageEventEmitter by writing a
// `gateway.call` / `gateway.rerank` event to the store event stream (§8/§10 — every
// gateway call is metered AND emitted, so cost governance and the audit trail see
// provider usage). It is ASYNC and non-blocking (§8: emit paths never block the
// caller): EmitUsage enqueues onto a bounded channel and a drain goroutine performs
// the durable write, so a gateway call on the SLO-bound read path is never delayed by
// an events INSERT. A full buffer drops (best-effort, like the injection writer,
// D-025). Events are attributed to the scope carried in ctx; a scope-less background
// call (the events table requires a tenant) is skipped — see D-088 for why the batched
// embed path is Prom-metered only.
type gatewayUsageEmitter struct {
	events    store.EventStore
	log       *slog.Logger
	ch        chan scopedEvent
	done      chan struct{}
	closeOnce sync.Once
	drops     atomic.Int64
}

func newGatewayUsageEmitter(events store.EventStore, log *slog.Logger) *gatewayUsageEmitter {
	g := &gatewayUsageEmitter{
		events: events, log: log,
		ch:   make(chan scopedEvent, gatewayUsageEventCap),
		done: make(chan struct{}),
	}
	go g.loop()
	return g
}

func (g *gatewayUsageEmitter) loop() {
	defer close(g.done)
	for se := range g.ch {
		// Background ctx: the write is fire-and-forget and must survive the caller's
		// request cancellation (P2/§8).
		if err := g.events.Emit(context.Background(), se.scope, se.ev); err != nil { //nolint:contextcheck
			g.log.Warn("gateway usage event emit failed", "type", se.ev.Type, "err", err)
		}
	}
}

// enqueue non-blockingly buffers a scoped event; drops (incrementing drops) when full.
func (g *gatewayUsageEmitter) enqueue(ctx context.Context, op string, ev store.Event) {
	scope, err := identity.FromContext(ctx)
	if err != nil || scope.Tenant == "" {
		// Scope-less (background/batched) gateway call — no tenant to attribute. The
		// Prom counters still record it; the event stream is tenant-scoped by design.
		g.log.LogAttrs(ctx, slog.LevelDebug, "gateway usage event skipped (no scope in ctx)",
			slog.String("op", op))
		return
	}
	select {
	case g.ch <- scopedEvent{scope: scope, ev: ev}:
	default:
		g.drops.Add(1)
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
	g.enqueue(ctx, op, store.Event{
		ID: ulid.Make().String(), Type: "gateway.call",
		Reason: "gateway call metered", Payload: string(payload),
	})
}

func (g *gatewayUsageEmitter) EmitRerankUsage(ctx context.Context, model string, searchUnits int, costUSD float64) {
	payload, _ := json.Marshal(struct {
		Op          string  `json:"op"`
		Model       string  `json:"model"`
		SearchUnits int     `json:"search_units"`
		CostUSD     float64 `json:"cost_usd"`
	}{"rerank", model, searchUnits, costUSD})
	g.enqueue(ctx, "rerank", store.Event{
		ID: ulid.Make().String(), Type: "gateway.rerank",
		Reason: "gateway call metered", Payload: string(payload),
	})
}

// Close drains buffered events and stops the goroutine. Idempotent.
func (g *gatewayUsageEmitter) Close(context.Context) error {
	g.closeOnce.Do(func() {
		close(g.ch)
		<-g.done
	})
	return nil
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

// wireGatewayUsageEvents wires the async usage-event emitter onto the gateway's meter
// when the driver supports it (type assertion — keeps the optional capability off the
// Gateway interface so test fakes need not implement it). Returns the emitter so boot
// can register its Close in the shutdown chain (nil when the driver has no meter hook).
func wireGatewayUsageEvents(gw gateway.Gateway, events store.EventStore, log *slog.Logger) *gatewayUsageEmitter {
	s, ok := gw.(interface {
		SetUsageEmitter(gateway.UsageEventEmitter)
	})
	if !ok {
		return nil
	}
	em := newGatewayUsageEmitter(events, log)
	s.SetUsageEmitter(em)
	return em
}
