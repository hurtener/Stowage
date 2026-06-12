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
	"os"
	"sort"
	"strings"
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
	cfg        config.GatewayConfig
	log        *slog.Logger
	meter      gateway.Meter
	mu         sync.Mutex
	scriptPath string // lazy script file (STOWAGE_MOCK_SCRIPT); see Complete
	fileOffset int    // entries of scriptPath already consumed
	script     []Script
}

func open(
	_ context.Context,
	cfg config.GatewayConfig,
	log *slog.Logger,
	prom *prometheus.Registry,
) (gateway.Gateway, error) {
	drv := &Driver{
		cfg:   cfg,
		log:   log,
		meter: gateway.NewPromMeter(log, prom),
	}
	if path := os.Getenv("STOWAGE_MOCK_SCRIPT"); path != "" {
		// Lazy mode: the file is (re)read at each Complete call so smoke
		// tests can write entries after boot (e.g. with runtime record IDs).
		drv.scriptPath = path
		log.Info("mock gateway: lazy script file enabled", "path", path)
	}
	return drv, nil
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

// LoadScriptFile loads scripted Complete responses from a JSON file: an array
// of raw JSON values consumed FIFO. Used by smoke tests to drive live servers
// end-to-end (the env var STOWAGE_MOCK_SCRIPT is read at Open). Dev/test
// affordance only — not a config key (knob guardrail).
func (d *Driver) LoadScriptFile(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // dev/test affordance, operator-supplied path
	if err != nil {
		return fmt.Errorf("mock: read script file: %w", err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("mock: parse script file: %w", err)
	}
	d.mu.Lock()
	for _, it := range items {
		d.script = append(d.script, Script{JSON: it})
	}
	d.mu.Unlock()
	return nil
}

// nextFromFileLocked reads the lazy script file and returns the next
// unconsumed entry (FIFO by persistent offset), or `{}` when exhausted or
// unreadable. Caller must hold d.mu.
func (d *Driver) nextFromFileLocked() Script {
	data, err := os.ReadFile(d.scriptPath) //nolint:gosec // dev/test affordance
	if err != nil {
		return Script{JSON: json.RawMessage(`{}`)}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil || d.fileOffset >= len(items) {
		return Script{JSON: json.RawMessage(`{}`)}
	}
	s := Script{JSON: items[d.fileOffset]}
	d.fileOffset++
	return s
}

// Complete returns the next scripted response, or `{}` if none is queued.
func (d *Driver) Complete(ctx context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	if len(req.Schema) == 0 {
		return gateway.CompleteResponse{}, fmt.Errorf("mock: Complete: Schema is required")
	}

	d.mu.Lock()
	var s Script
	switch {
	case len(d.script) > 0:
		s, d.script = d.script[0], d.script[1:]
	case d.scriptPath != "":
		s = d.nextFromFileLocked()
	default:
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

// Rerank returns deterministic relevance scores based on token overlap with the
// query (Jaccard similarity). Results are sorted by score descending; ties are
// broken by original document index.
func (d *Driver) Rerank(ctx context.Context, req gateway.RerankRequest) (gateway.RerankResponse, error) {
	queryTokens := tokenSet(req.Query)
	results := make([]gateway.RerankResult, len(req.Documents))
	for i, doc := range req.Documents {
		docTokens := tokenSet(doc)
		results[i] = gateway.RerankResult{Index: i, Score: jaccardOverlap(queryTokens, docTokens)}
	}
	// Sort by score descending; stable by original index on tie.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Index < results[j].Index
	})
	if req.TopN > 0 && len(results) > req.TopN {
		results = results[:req.TopN]
	}
	usage := gateway.RerankUsage{SearchUnits: len(req.Documents)}
	d.meter.RecordRerank(ctx, "mock-rerank", usage)
	return gateway.RerankResponse{Results: results, Usage: usage}, nil
}

// tokenSet splits s into lowercase word tokens and returns them as a set.
func tokenSet(s string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, f := range strings.Fields(strings.ToLower(s)) {
		tokens[f] = struct{}{}
	}
	return tokens
}

// jaccardOverlap computes the Jaccard similarity coefficient for two token sets.
// Returns 1.0 when both sets are empty.
func jaccardOverlap(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	var intersection, union int
	for k := range a {
		union++
		if _, ok := b[k]; ok {
			intersection++
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
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
