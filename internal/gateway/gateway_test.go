package gateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// noopLogger returns a logger that discards all output.
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testGatewayCfg(driver string) config.GatewayConfig {
	return config.GatewayConfig{
		Driver:     driver,
		Model:      "test-model",
		EmbedModel: "test-embed",
		EmbedDims:  4,
	}
}

// ── Batcher (AC-3) ────────────────────────────────────────────────────────────

func TestBatcher_CoalescesIntoFewBatches(t *testing.T) {
	t.Parallel()

	var batchCalls atomic.Int64
	dims := 4
	doFn := func(_ context.Context, inputs []string) ([][]float32, gateway.Usage, error) {
		batchCalls.Add(1)
		vecs := make([][]float32, len(inputs))
		for i := range vecs {
			vecs[i] = make([]float32, dims)
		}
		return vecs, gateway.Usage{InputTokens: len(inputs)}, nil
	}

	b := gateway.NewBatcher(doFn, nil, "model")
	defer b.Close()

	const N = 128 // 2 full batches of 64
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := range N {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = b.Embed(context.Background(), fmt.Sprintf("input-%d", idx))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("input %d: unexpected error: %v", i, err)
		}
	}

	// N=128 inputs max batchMaxSize=64 → ≤ ceil(128/64)=2 provider calls.
	// Timing variance may produce more partial batches, so allow up to N calls
	// but assert strictly less than N (coalescing happened).
	got := batchCalls.Load()
	if got >= N {
		t.Errorf("expected coalescing: batch calls should be < %d, got %d", N, got)
	}
	maxExpected := int64((N + gateway.BatchMaxSize - 1) / gateway.BatchMaxSize)
	if got > maxExpected*4 {
		t.Errorf("expected ≤ %d batch calls for %d inputs, got %d", maxExpected*4, N, got)
	}
}

func TestBatcher_CorrectResultsUnderRace(t *testing.T) {
	t.Parallel()

	dims := 3
	doFn := func(_ context.Context, inputs []string) ([][]float32, gateway.Usage, error) {
		vecs := make([][]float32, len(inputs))
		for i, inp := range inputs {
			vecs[i] = []float32{float32(len(inp)), float32(len(inp) + 1), float32(len(inp) + 2)}
		}
		return vecs, gateway.Usage{}, nil
	}

	b := gateway.NewBatcher(doFn, nil, "model")
	defer b.Close()

	const N = 64
	results := make([][]float32, N)
	var wg sync.WaitGroup
	for i := range N {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			input := fmt.Sprintf("in-%d", idx)
			vec, err := b.Embed(context.Background(), input)
			if err != nil {
				t.Errorf("idx %d: %v", idx, err)
				return
			}
			results[idx] = vec
		}(i)
	}
	wg.Wait()

	for i, vec := range results {
		if vec == nil {
			t.Errorf("result %d is nil", i)
			continue
		}
		if len(vec) != dims {
			t.Errorf("result %d: want %d dims, got %d", i, dims, len(vec))
		}
	}
}

func TestBatcher_CancelContextAborts(t *testing.T) {
	t.Parallel()

	blocked := make(chan struct{})
	doFn := func(_ context.Context, _ []string) ([][]float32, gateway.Usage, error) {
		<-blocked
		return nil, gateway.Usage{}, nil
	}

	b := gateway.NewBatcher(doFn, nil, "model")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := b.Embed(ctx, "input")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}

	// Unblock the dispatch goroutine before closing the batcher.
	// (Close waits for in-flight dispatches; the goroutine is blocked on doFn.)
	close(blocked)
	b.Close()
}

// ── EmbedCache (AC-4) ─────────────────────────────────────────────────────────

func TestEmbedCache_HitAvoidProviderTraffic(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	cache := gateway.NewEmbedCache(100)
	model := "m"

	providerCall := func(_ string) []float32 {
		calls.Add(1)
		return []float32{1, 2, 3}
	}

	// First lookup: miss
	if _, ok := cache.Get(model, "hello"); ok {
		t.Fatal("expected cache miss on first call")
	}
	vec := providerCall("hello")
	cache.Put(model, "hello", vec)

	// Second lookup: hit — no provider call
	got, ok := cache.Get(model, "hello")
	if !ok {
		t.Fatal("expected cache hit on second call")
	}
	if len(got) != 3 {
		t.Errorf("unexpected vector length: %d", len(got))
	}
	if calls.Load() != 1 {
		t.Errorf("expected exactly 1 provider call, got %d", calls.Load())
	}
}

func TestEmbedCache_EvictsLRU(t *testing.T) {
	t.Parallel()

	cache := gateway.NewEmbedCache(2)
	cache.Put("m", "a", []float32{1})
	cache.Put("m", "b", []float32{2})
	// touch "a" → "b" becomes LRU
	cache.Get("m", "a")
	// insert "c" → evicts "b"
	cache.Put("m", "c", []float32{3})

	if _, ok := cache.Get("m", "b"); ok {
		t.Error("expected 'b' to be evicted")
	}
	if _, ok := cache.Get("m", "a"); !ok {
		t.Error("expected 'a' to still be cached")
	}
	if _, ok := cache.Get("m", "c"); !ok {
		t.Error("expected 'c' to be cached")
	}
}

func TestEmbedCache_ModelKeyIsolation(t *testing.T) {
	t.Parallel()

	cache := gateway.NewEmbedCache(10)
	cache.Put("model-a", "same-input", []float32{1})
	if _, ok := cache.Get("model-b", "same-input"); ok {
		t.Error("different model should be a cache miss")
	}
}

// ── CircuitBreaker (AC-5) ─────────────────────────────────────────────────────

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	t.Parallel()

	b := gateway.NewCircuitBreaker()

	for i := range gateway.BreakerThreshold {
		if err := b.Allow(); err != nil {
			t.Fatalf("iteration %d: unexpected error before threshold: %v", i, err)
		}
		b.Failure()
	}

	if err := b.Allow(); !errors.Is(err, gateway.ErrGatewayUnavailable) {
		t.Errorf("expected ErrGatewayUnavailable after threshold, got %v", err)
	}
}

func TestCircuitBreaker_SuccessResetsFailures(t *testing.T) {
	t.Parallel()

	b := gateway.NewCircuitBreaker()

	// N-1 failures (one below threshold)
	for range gateway.BreakerThreshold - 1 {
		b.Allow() //nolint:errcheck
		b.Failure()
	}
	b.Allow()   //nolint:errcheck
	b.Success() // reset counter

	// After success, needs full threshold again to open
	for range gateway.BreakerThreshold - 1 {
		b.Allow() //nolint:errcheck
		b.Failure()
	}
	if err := b.Allow(); err != nil {
		t.Errorf("expected closed circuit after success reset, got %v", err)
	}
}

func TestCircuitBreaker_HalfOpenProbeSuccessCloses(t *testing.T) {
	t.Parallel()

	b := gateway.NewCircuitBreaker()

	// Open the breaker
	for range gateway.BreakerThreshold {
		b.Allow() //nolint:errcheck
		b.Failure()
	}

	// Manually reset via Success to simulate half-open probe recovery
	b.Success()
	if err := b.Allow(); err != nil {
		t.Errorf("expected closed after success from half-open: %v", err)
	}
}

// ── Validate (seam) ───────────────────────────────────────────────────────────

func TestValidateJSON_ValidInstance(t *testing.T) {
	t.Parallel()

	rawSchema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	sch, err := gateway.CompileSchema(rawSchema)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	valid := json.RawMessage(`{"name":"Alice"}`)
	if err := gateway.ValidateJSON(sch, valid); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateJSON_InvalidInstance(t *testing.T) {
	t.Parallel()

	rawSchema := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`)
	sch, err := gateway.CompileSchema(rawSchema)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	invalid := json.RawMessage(`{"count":"not-an-int"}`)
	err = gateway.ValidateJSON(sch, invalid)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if !errors.Is(err, gateway.ErrSchemaValidation) {
		t.Errorf("expected ErrSchemaValidation, got %v", err)
	}
}

func TestValidateJSON_MalformedJSON(t *testing.T) {
	t.Parallel()

	sch, _ := gateway.CompileSchema(json.RawMessage(`{}`))
	err := gateway.ValidateJSON(sch, json.RawMessage(`not json`))
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
	if !errors.Is(err, gateway.ErrSchemaValidation) {
		t.Errorf("want ErrSchemaValidation, got %v", err)
	}
}

// ── Meter (AC-8) ──────────────────────────────────────────────────────────────

func TestPromMeter_RecordsCounters(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	m := gateway.NewPromMeter(noopLogger(), reg)

	ctx := context.Background()
	m.Record(ctx, "embed", "voyage-3-lite", gateway.Usage{InputTokens: 10, CostUSD: 0.001})
	m.Record(ctx, "complete", "gpt-4o", gateway.Usage{InputTokens: 50, OutputTokens: 20, CostUSD: 0.005})

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	counters := map[string]float64{}
	for _, mf := range mfs {
		for _, metric := range mf.GetMetric() {
			if mf.GetType() != dto.MetricType_COUNTER {
				continue
			}
			key := mf.GetName()
			for _, lp := range metric.GetLabel() {
				key += "," + lp.GetName() + "=" + lp.GetValue()
			}
			counters[key] += metric.GetCounter().GetValue()
		}
	}

	checkCounter := func(name string, want float64) {
		t.Helper()
		if got := counters[name]; got != want {
			t.Errorf("counter %q: want %v, got %v", name, want, got)
		}
	}

	// Prometheus sorts labels alphabetically in gathered output: model < op.
	checkCounter("gateway_calls_total,model=voyage-3-lite,op=embed", 1)
	checkCounter("gateway_calls_total,model=gpt-4o,op=complete", 1)
	checkCounter("gateway_input_tokens_total,model=voyage-3-lite,op=embed", 10)
	checkCounter("gateway_input_tokens_total,model=gpt-4o,op=complete", 50)
	checkCounter("gateway_output_tokens_total,model=gpt-4o,op=complete", 20)
}

// ── Registry ──────────────────────────────────────────────────────────────────

func TestOpen_UnknownDriver(t *testing.T) {
	t.Parallel()

	cfg := testGatewayCfg("no-such-driver")
	_, err := gateway.Open(context.Background(), cfg, noopLogger(), prometheus.NewRegistry())
	if !errors.Is(err, gateway.ErrDriverNotRegistered) {
		t.Errorf("expected ErrDriverNotRegistered, got %v", err)
	}
}

// ── Benchmark ────────────────────────────────────────────────────────────────

func BenchmarkEmbedCacheHit(b *testing.B) {
	cache := gateway.NewEmbedCache(defaultCacheSize)
	vec := make([]float32, 512)
	for i := range vec {
		vec[i] = float32(i) / 512
	}
	cache.Put("model", "benchmark-input", vec)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, ok := cache.Get("model", "benchmark-input")
			if !ok {
				b.Fatal("cache miss in benchmark")
			}
		}
	})
}

const defaultCacheSize = 50_000

// ── Fuzz ─────────────────────────────────────────────────────────────────────

func FuzzSchemaValidation(f *testing.F) {
	// Seed corpus: valid and invalid JSON instances.
	f.Add(`{}`)
	f.Add(`{"name":"Alice"}`)
	f.Add(`{"count":42}`)
	f.Add(`null`)
	f.Add(`"string"`)
	f.Add(`123`)
	f.Add(`[1,2,3]`)
	f.Add(`not-json`)

	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name":  {"type": "string"},
			"count": {"type": "integer"}
		},
		"required": ["name"]
	}`)
	sch, err := gateway.CompileSchema(schema)
	if err != nil {
		f.Fatalf("compile schema: %v", err)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Must not panic regardless of input.
		_ = gateway.ValidateJSON(sch, json.RawMessage(input))
	})
}

// fakeUsageEmitter captures emitted usage for the meter wiring test.
type fakeUsageEmitter struct {
	usage  int
	rerank int
}

func (f *fakeUsageEmitter) EmitUsage(_ context.Context, _, _ string, _, _ int, _ float64) { f.usage++ }
func (f *fakeUsageEmitter) EmitRerankUsage(_ context.Context, _ string, _ int, _ float64) { f.rerank++ }

func TestPromMeter_EmitsUsageEvents(t *testing.T) {
	m := gateway.NewPromMeter(slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
	em := &fakeUsageEmitter{}
	m.SetEmitter(em)

	m.Record(context.Background(), "embed", "m-1", gateway.Usage{InputTokens: 10, OutputTokens: 0, CostUSD: 0.001})
	m.Record(context.Background(), "complete", "m-2", gateway.Usage{InputTokens: 100, OutputTokens: 50, CostUSD: 0.02})
	m.RecordRerank(context.Background(), "rr", gateway.RerankUsage{SearchUnits: 3, CostUSD: 0.005})

	if em.usage != 2 {
		t.Errorf("EmitUsage called %d times, want 2", em.usage)
	}
	if em.rerank != 1 {
		t.Errorf("EmitRerankUsage called %d times, want 1", em.rerank)
	}

	// No emitter ⇒ no panic, Prom-only.
	m2 := gateway.NewPromMeter(slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
	m2.Record(context.Background(), "embed", "m", gateway.Usage{InputTokens: 1})
}
