package openaicompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat" // blank-import driver registration
	"github.com/prometheus/client_golang/prometheus"
)

// TestMain sets the test API-key env var once so parallel tests can use it.
func TestMain(m *testing.M) {
	os.Setenv("STOWAGE_TEST_OPENAICOMPAT_KEY", "test-key") //nolint:errcheck
	os.Exit(m.Run())
}

// newDriver builds an openaicompat Gateway pointed at svr.
// STOWAGE_TEST_OPENAICOMPAT_KEY is set by TestMain.
func newDriver(t *testing.T, svr *httptest.Server, dims int) gateway.Gateway {
	t.Helper()
	cfg := config.GatewayConfig{
		Driver:     "openaicompat",
		BaseURL:    svr.URL,
		APIKey:     "env.STOWAGE_TEST_OPENAICOMPAT_KEY",
		Model:      "gpt-4o",
		EmbedModel: "text-embedding-3-small",
		EmbedDims:  dims,
	}
	gw, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open openaicompat driver: %v", err)
	}
	t.Cleanup(func() { gw.Close(context.Background()) }) //nolint:errcheck
	return gw
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── Golden wire tests (AC-1) ─────────────────────────────────────────────────

// goldenEmbedRequestBody is the exact JSON the driver must send for a single-input
// embed request. Tests assert byte-for-byte match after normalisation.
const goldenEmbedRequestBody = `{"model":"text-embedding-3-small","input":["hello world"]}`

func TestOpenAICompat_GoldenEmbedRequest(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/embeddings" {
			gotBody, _ = io.ReadAll(r.Body)
			resp := `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2,0.3,0.4],"index":0}],"model":"text-embedding-3-small","usage":{"prompt_tokens":2,"total_tokens":2}}`
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(resp)) //nolint:errcheck
		}
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"hello world"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	// Normalise by re-marshalling to canonical JSON.
	var wantObj, gotObj any
	if err := json.Unmarshal([]byte(goldenEmbedRequestBody), &wantObj); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if err := json.Unmarshal(gotBody, &gotObj); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}
	wantNorm, _ := json.Marshal(wantObj)
	gotNorm, _ := json.Marshal(gotObj)
	if string(wantNorm) != string(gotNorm) {
		t.Errorf("embed request body mismatch:\nwant: %s\n got: %s", wantNorm, gotNorm)
	}
}

// goldenCompleteRequestBody is the canonical JSON the driver must send for a
// chat/completions request with json_schema response_format.
const goldenCompleteRequestBody = `{
	"model":"gpt-4o",
	"messages":[{"role":"user","content":"say hello"}],
	"max_tokens":100,
	"response_format":{"type":"json_schema","json_schema":{"name":"response","schema":{"type":"object","properties":{"msg":{"type":"string"}},"required":["msg"]},"strict":true}}
}`

func TestOpenAICompat_GoldenCompleteRequest(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" {
			gotBody, _ = io.ReadAll(r.Body)
			resp := `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"msg\":\"hello\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(resp)) //nolint:errcheck
		}
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	schema := json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}},"required":["msg"]}`)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages:  []gateway.Message{{Role: "user", Content: "say hello"}},
		Schema:    schema,
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var wantObj, gotObj any
	if err := json.Unmarshal([]byte(goldenCompleteRequestBody), &wantObj); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if err := json.Unmarshal(gotBody, &gotObj); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}
	wantNorm, _ := json.Marshal(wantObj)
	gotNorm, _ := json.Marshal(gotObj)
	if string(wantNorm) != string(gotNorm) {
		t.Errorf("complete request body mismatch:\nwant: %s\n got: %s", wantNorm, gotNorm)
	}
}

func TestOpenAICompat_GoldenEmbedResponseDecodes(t *testing.T) {
	t.Parallel()

	// Server returns a single-vector response; we verify structure not specific values.
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"text-embedding-3-small","usage":{"prompt_tokens":2,"total_tokens":2}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 2)
	resp, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"a"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Vectors) != 1 {
		t.Fatalf("want 1 vector, got %d", len(resp.Vectors))
	}
	if len(resp.Vectors[0]) != 2 {
		t.Errorf("want 2 dims, got %d: %v", len(resp.Vectors[0]), resp.Vectors[0])
	}
	if resp.Vectors[0][0] != 0.1 || resp.Vectors[0][1] != 0.2 {
		t.Errorf("unexpected vector: %v", resp.Vectors[0])
	}
}

// ── Schema validation + retry (AC-2) ─────────────────────────────────────────

func TestOpenAICompat_SchemaValidationRetryOnce(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// First call: return invalid JSON (missing required field)
			io.WriteString(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"wrong\":\"field\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`) //nolint:errcheck
		} else {
			// Second call: return valid JSON
			io.WriteString(w, `{"id":"2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"name\":\"Alice\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`) //nolint:errcheck
		}
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	resp, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages:  []gateway.Message{{Role: "user", Content: "give me a name"}},
		Schema:    schema,
		MaxTokens: 50,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(resp.JSON, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", got["name"])
	}
	if callCount.Load() != 2 {
		t.Errorf("expected exactly 2 provider calls (1 retry), got %d", callCount.Load())
	}
}

func TestOpenAICompat_SchemaValidationFailsTwice(t *testing.T) {
	t.Parallel()

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Always return invalid JSON
		io.WriteString(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"wrong\":\"field\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages:  []gateway.Message{{Role: "user", Content: "give me a name"}},
		Schema:    schema,
		MaxTokens: 50,
	})
	if err == nil {
		t.Fatal("expected schema validation error, got nil")
	}
	if !errors.Is(err, gateway.ErrSchemaValidation) {
		t.Errorf("expected ErrSchemaValidation, got %v", err)
	}
}

// ── Retry policy (AC-6) ───────────────────────────────────────────────────────

func TestOpenAICompat_Retries429(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2,0.3,0.4],"index":0}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 calls (2×429 + 1 success), got %d", callCount.Load())
	}
}

func TestOpenAICompat_Retries503(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2,0.3,0.4],"index":0}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if err != nil {
		t.Fatalf("expected success after retry: %v", err)
	}
}

func TestOpenAICompat_DoesNotRetry400(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if callCount.Load() != 1 {
		t.Errorf("expected exactly 1 call for 400, got %d", callCount.Load())
	}
}

// ── Circuit breaker (AC-5) ────────────────────────────────────────────────────

func TestOpenAICompat_BreakerOpensAfter5Failures(t *testing.T) {
	t.Parallel()

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)

	var openErr error
	// Each Embed goes through the retry loop (maxRetries=3), recording 3 failures.
	// After 5 consecutive failures the breaker opens. With maxRetries=3 per call,
	// we need 2 calls to accumulate ≥5 failures (3+3=6 ≥ 5).
	for i := range 3 {
		_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
		if errors.Is(err, gateway.ErrGatewayUnavailable) {
			openErr = err
			t.Logf("breaker opened on call %d", i+1)
			break
		}
	}

	// The breaker must have opened by now.
	if !errors.Is(openErr, gateway.ErrGatewayUnavailable) {
		// If not yet open, the next call should be fast-failed.
		_, openErr = gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
		if !errors.Is(openErr, gateway.ErrGatewayUnavailable) {
			t.Errorf("expected ErrGatewayUnavailable after 5+ failures, got %v", openErr)
		}
	}
}

// ── Probe (AC-7) ─────────────────────────────────────────────────────────────

func TestOpenAICompat_ProbeFailsOnWrongDims(t *testing.T) {
	t.Parallel()

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a 2-dim vector, but the driver is configured for 4 dims.
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4) // configured for 4 dims
	err := gw.Probe(context.Background())
	if err == nil {
		t.Fatal("expected probe to fail on dim mismatch")
	}
	if !errors.Is(err, gateway.ErrProbeFailed) {
		t.Errorf("expected ErrProbeFailed, got %v", err)
	}
}

func TestOpenAICompat_ProbeSucceedsOnCorrectDims(t *testing.T) {
	t.Parallel()

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2,0.3,0.4],"index":0}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	if err := gw.Probe(context.Background()); err != nil {
		t.Errorf("expected probe success, got: %v", err)
	}
}

// ── Authorization header ──────────────────────────────────────────────────────

func TestOpenAICompat_SendsAuthorizationHeader(t *testing.T) {
	t.Parallel()

	var gotAuth string
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[1,2,3,4],"index":0}],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}}) //nolint:errcheck

	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Errorf("expected Bearer auth, got %q", gotAuth)
	}
}

// ── fail-closed: missing API key ─────────────────────────────────────────────

func TestOpenAICompat_FailsClosedOnMissingAPIKey(t *testing.T) {
	t.Parallel()

	// Deliberately do NOT set STOWAGE_TEST_OPENAICOMPAT_KEY2
	cfg := config.GatewayConfig{
		Driver:     "openaicompat",
		BaseURL:    "http://localhost:9",
		APIKey:     "env.STOWAGE_TEST_OPENAICOMPAT_KEY2",
		Model:      "m",
		EmbedModel: "e",
		EmbedDims:  4,
	}
	_, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err == nil {
		t.Fatal("expected error when API key env var is unset")
	}
}

// ── Truncation (finish_reason "length") ──────────────────────────────────────

// TestOpenAICompat_TruncatedResponse asserts that a provider stop on the token
// limit surfaces as gateway.ErrTruncated, not as a schema-validation failure
// (found by live validation against a thinking model with a small budget).
func TestOpenAICompat_TruncatedResponse(t *testing.T) {
	t.Parallel()

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"Here is"},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":128,"total_tokens":133}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		Schema:   json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, gateway.ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
	if errors.Is(err, gateway.ErrSchemaValidation) {
		t.Fatalf("truncation must not be reported as schema validation: %v", err)
	}
}

// ── Provider error envelope (HTTP 200 + {"error":...}) ───────────────────────

// Some OpenAI-compatible providers (OpenRouter) wrap upstream failures in an
// HTTP 200 body. Found by live validation: a capped upstream returned
// 200+{"error":{"code":429,...}} and the driver reported "0 vectors".
func TestOpenAICompat_EmbedErrorEnvelope(t *testing.T) {
	t.Parallel()
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"error":{"code":429,"message":"spending cap exceeded"}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if err == nil || !strings.Contains(err.Error(), "error envelope") || !strings.Contains(err.Error(), "spending cap") {
		t.Fatalf("want envelope error with provider message, got %v", err)
	}
}

func TestOpenAICompat_CompleteErrorEnvelope(t *testing.T) {
	t.Parallel()
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"error":{"code":"rate_limited","message":"upstream unavailable"}}`) //nolint:errcheck
	}))
	defer svr.Close()

	gw := newDriver(t, svr, 4)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		Schema:   json.RawMessage(`{"type":"object"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "error envelope") {
		t.Fatalf("want envelope error, got %v", err)
	}
}

// ── Rerank ────────────────────────────────────────────────────────────────────

// goldenRerankRequestBody is the Cohere-shape wire body the driver must send.
const goldenRerankRequestBody = `{"model":"cohere/rerank-4-fast","query":"what is Go","documents":["Go is fast","Python is slow"],"top_n":2}`

func TestOpenAICompat_GoldenRerankRequest(t *testing.T) {
	t.Parallel()

	var gotBody []byte
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rerank" {
			gotBody, _ = io.ReadAll(r.Body)
			resp := `{"results":[{"index":0,"relevance_score":0.9},{"index":1,"relevance_score":0.3}],"usage":{"search_units":1}}`
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(resp)) //nolint:errcheck
		}
	}))
	defer svr.Close()

	cfg := config.GatewayConfig{
		Driver:      "openaicompat",
		BaseURL:     svr.URL,
		APIKey:      "env.STOWAGE_TEST_OPENAICOMPAT_KEY",
		Model:       "gpt-4o",
		EmbedModel:  "text-embedding-3-small",
		EmbedDims:   4,
		RerankModel: "cohere/rerank-4-fast",
	}
	gw, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { gw.Close(context.Background()) }) //nolint:errcheck

	resp, err := gw.Rerank(context.Background(), gateway.RerankRequest{
		Query:     "what is Go",
		Documents: []string{"Go is fast", "Python is slow"},
		TopN:      2,
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(resp.Results))
	}

	// Verify wire body matches golden shape.
	var wantObj, gotObj any
	if err := json.Unmarshal([]byte(goldenRerankRequestBody), &wantObj); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if err := json.Unmarshal(gotBody, &gotObj); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}
	wantNorm, _ := json.Marshal(wantObj)
	gotNorm, _ := json.Marshal(gotObj)
	if string(wantNorm) != string(gotNorm) {
		t.Errorf("rerank request body mismatch:\nwant: %s\n got: %s", wantNorm, gotNorm)
	}
}

func TestOpenAICompat_RerankUsageMetered(t *testing.T) {
	t.Parallel()

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[{"index":0,"relevance_score":0.8}],"usage":{"search_units":3}}`) //nolint:errcheck
	}))
	defer svr.Close()

	cfg := config.GatewayConfig{
		Driver:      "openaicompat",
		BaseURL:     svr.URL,
		APIKey:      "env.STOWAGE_TEST_OPENAICOMPAT_KEY",
		Model:       "gpt-4o",
		EmbedModel:  "e",
		EmbedDims:   4,
		RerankModel: "cohere/rerank-4-fast",
	}
	gw, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { gw.Close(context.Background()) }) //nolint:errcheck

	resp, err := gw.Rerank(context.Background(), gateway.RerankRequest{
		Query:     "hello",
		Documents: []string{"world"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if resp.Usage.SearchUnits != 3 {
		t.Errorf("want search_units=3, got %d", resp.Usage.SearchUnits)
	}
}

func TestOpenAICompat_RerankErrorEnvelope(t *testing.T) {
	t.Parallel()

	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"error":{"code":429,"message":"rate limit"}}`) //nolint:errcheck
	}))
	defer svr.Close()

	cfg := config.GatewayConfig{
		Driver:      "openaicompat",
		BaseURL:     svr.URL,
		APIKey:      "env.STOWAGE_TEST_OPENAICOMPAT_KEY",
		Model:       "m",
		EmbedModel:  "e",
		EmbedDims:   4,
		RerankModel: "rerank-model",
	}
	gw, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { gw.Close(context.Background()) }) //nolint:errcheck

	_, err = gw.Rerank(context.Background(), gateway.RerankRequest{
		Query:     "q",
		Documents: []string{"d1"},
	})
	if err == nil || !strings.Contains(err.Error(), "error envelope") {
		t.Fatalf("want rerank envelope error, got %v", err)
	}
}
