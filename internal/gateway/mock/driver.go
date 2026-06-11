// Package mock provides a deterministic, hermetic gateway driver for tests.
//
// Embeddings are unit vectors seeded from sha256(input) at the configured dims.
// Completions return scripted responses registered via PushScript; if no script
// is queued the driver returns an empty-object JSON (`{}`).
//
// Register with a blank import:
//
//	import _ "github.com/hurtener/stowage/internal/gateway/mock"
package mock

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sync"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/prometheus/client_golang/prometheus"
)

func init() {
	gateway.Register("mock", open)
}

// Script is a scripted response for one Complete call.
type Script struct {
	JSON json.RawMessage
	Err  error
}

// Driver is the mock gateway driver. It is safe for concurrent use.
type Driver struct {
	cfg    config.GatewayConfig
	log    *slog.Logger
	meter  gateway.Meter
	mu     sync.Mutex
	script []Script
}

func open(
	_ context.Context,
	cfg config.GatewayConfig,
	log *slog.Logger,
	prom *prometheus.Registry,
) (gateway.Gateway, error) {
	return &Driver{
		cfg:   cfg,
		log:   log,
		meter: gateway.NewPromMeter(log, prom),
	}, nil
}

// PushScript queues a scripted response for the next Complete call.
// If multiple scripts are pushed, they are consumed in FIFO order.
func (d *Driver) PushScript(s Script) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script = append(d.script, s)
}

// Embed returns deterministic unit vectors seeded from sha256(input).
func (d *Driver) Embed(ctx context.Context, req gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	vecs := make([][]float32, len(req.Inputs))
	for i, input := range req.Inputs {
		vecs[i] = deterministicUnit(input, d.cfg.EmbedDims)
	}
	usage := gateway.Usage{InputTokens: len(req.Inputs)}
	d.meter.Record(ctx, "embed", d.cfg.EmbedModel, usage)
	return gateway.EmbedResponse{Vectors: vecs, Usage: usage}, nil
}

// Complete returns the next scripted response, or `{}` if none is queued.
func (d *Driver) Complete(ctx context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	if len(req.Schema) == 0 {
		return gateway.CompleteResponse{}, fmt.Errorf("mock: Complete: Schema is required")
	}

	d.mu.Lock()
	var s Script
	if len(d.script) > 0 {
		s, d.script = d.script[0], d.script[1:]
	} else {
		s = Script{JSON: json.RawMessage(`{}`)}
	}
	d.mu.Unlock()

	if s.Err != nil {
		return gateway.CompleteResponse{}, s.Err
	}

	usage := gateway.Usage{InputTokens: len(req.System) + len(req.Messages), OutputTokens: len(s.JSON)}
	d.meter.Record(ctx, "complete", d.cfg.Model, usage)
	return gateway.CompleteResponse{JSON: s.JSON, Usage: usage}, nil
}

// Probe always succeeds for the mock driver.
func (d *Driver) Probe(_ context.Context) error { return nil }

// Close is a no-op for the mock driver.
func (d *Driver) Close(_ context.Context) error { return nil }

// deterministicUnit returns a unit-length float32 vector of length dims seeded
// from sha256(input). If dims == 0, returns nil.
func deterministicUnit(input string, dims int) []float32 {
	if dims <= 0 {
		return nil
	}
	h := sha256.Sum256([]byte(input))
	vec := make([]float32, dims)
	for i := range vec {
		b := h[i%len(h)]
		vec[i] = float32(b)/127.5 - 1.0 // range [-1, 1]
	}
	// Normalise to unit length.
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	norm := float32(math.Sqrt(sum))
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}
