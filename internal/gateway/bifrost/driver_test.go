package bifrost_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	bfschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	. "github.com/hurtener/stowage/internal/gateway/bifrost"
)

// ─── fake client ─────────────────────────────────────────────────────────────

// fakeClient implements the bifrostClient interface for unit tests.
type fakeClient struct {
	chatFn   func(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError)
	embedFn  func(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError)
	rerankFn func(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostRerankRequest) (*bfschemas.BifrostRerankResponse, *bfschemas.BifrostError)
	shutdown bool
}

func (f *fakeClient) ChatCompletionRequest(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
	if f.chatFn != nil {
		return f.chatFn(ctx, req)
	}
	return nil, &bfschemas.BifrostError{Error: &bfschemas.ErrorField{Message: "not configured"}}
}

func (f *fakeClient) EmbeddingRequest(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
	if f.embedFn != nil {
		return f.embedFn(ctx, req)
	}
	return nil, &bfschemas.BifrostError{Error: &bfschemas.ErrorField{Message: "not configured"}}
}

func (f *fakeClient) RerankRequest(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostRerankRequest) (*bfschemas.BifrostRerankResponse, *bfschemas.BifrostError) {
	if f.rerankFn != nil {
		return f.rerankFn(ctx, req)
	}
	return nil, &bfschemas.BifrostError{Error: &bfschemas.ErrorField{Message: "not configured"}}
}

func (f *fakeClient) Shutdown() { f.shutdown = true }

// newTestDriver builds a Driver wired to the given fakeClient.
func newTestDriver(t *testing.T, fake *fakeClient, dims int) gateway.Gateway {
	t.Helper()
	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openai",
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:      "gpt-4o",
		EmbedModel: "text-embedding-3-small",
		EmbedDims:  dims,
	}
	prom := prometheus.NewRegistry()
	d := NewDriverWithClient(fake, bfschemas.OpenAI, cfg, discardLog(), prom)
	t.Cleanup(func() { d.Close(context.Background()) }) //nolint:errcheck
	return d
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ─── helper: successful embed response ───────────────────────────────────────

func okEmbedResponse(vecs [][]float64) *bfschemas.BifrostEmbeddingResponse {
	data := make([]bfschemas.EmbeddingData, len(vecs))
	for i, v := range vecs {
		data[i] = bfschemas.EmbeddingData{
			Index:     i,
			Object:    "embedding",
			Embedding: bfschemas.EmbeddingStruct{EmbeddingArray: v},
		}
	}
	usage := &bfschemas.BifrostLLMUsage{PromptTokens: 2}
	return &bfschemas.BifrostEmbeddingResponse{Data: data, Usage: usage}
}

// ─── helper: successful chat response ────────────────────────────────────────

func okChatResponse(content string) *bfschemas.BifrostChatResponse {
	finishReason := "stop"
	return &bfschemas.BifrostChatResponse{
		ID: "test-1",
		Choices: []bfschemas.BifrostResponseChoice{
			{
				FinishReason: &finishReason,
				ChatNonStreamResponseChoice: &bfschemas.ChatNonStreamResponseChoice{
					Message: &bfschemas.ChatMessage{
						Role:    bfschemas.ChatMessageRoleAssistant,
						Content: &bfschemas.ChatMessageContent{ContentStr: &content},
					},
				},
			},
		},
		Usage: &bfschemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 5},
	}
}

// ─── TestMain ────────────────────────────────────────────────────────────────

func TestMain(m *testing.M) {
	os.Setenv("STOWAGE_TEST_BIFROST_SDK_KEY", "test-key") //nolint:errcheck
	os.Exit(m.Run())
}

// ─── Embed: translate both directions ────────────────────────────────────────

func TestBifrostSDK_EmbedTranslateRequest(t *testing.T) {
	t.Parallel()

	var gotReq *bfschemas.BifrostEmbeddingRequest
	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			gotReq = req
			return okEmbedResponse([][]float64{{0.1, 0.2, 0.3, 0.4}}), nil
		},
	}

	gw := newTestDriver(t, fake, 4)
	_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"hello"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if gotReq == nil {
		t.Fatal("EmbeddingRequest was not called")
	}
	if gotReq.Provider != bfschemas.OpenAI {
		t.Errorf("want provider=openai, got %q", gotReq.Provider)
	}
	if gotReq.Model != "text-embedding-3-small" {
		t.Errorf("want model=text-embedding-3-small, got %q", gotReq.Model)
	}
	if gotReq.Input == nil || gotReq.Input.Text == nil || *gotReq.Input.Text != "hello" {
		t.Errorf("want single-text input 'hello', got %+v", gotReq.Input)
	}
}

func TestBifrostSDK_EmbedTranslateResponse(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			return okEmbedResponse([][]float64{{0.5, 0.6}}), nil
		},
	}

	gw := newTestDriver(t, fake, 2)
	resp, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Vectors) != 1 {
		t.Fatalf("want 1 vector, got %d", len(resp.Vectors))
	}
	if len(resp.Vectors[0]) != 2 {
		t.Fatalf("want 2 dims, got %d", len(resp.Vectors[0]))
	}
	const eps = 1e-5
	if abs32(resp.Vectors[0][0]-0.5) > eps || abs32(resp.Vectors[0][1]-0.6) > eps {
		t.Errorf("unexpected vector values: %v", resp.Vectors[0])
	}
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// ─── Complete: translate both directions ─────────────────────────────────────

func TestBifrostSDK_CompleteTranslateRequest(t *testing.T) {
	t.Parallel()

	var gotReq *bfschemas.BifrostChatRequest
	fake := &fakeClient{
		chatFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			gotReq = req
			return okChatResponse(`{"name":"Alice"}`), nil
		},
	}

	gw := newTestDriver(t, fake, 4)
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		System:    "you are helpful",
		Messages:  []gateway.Message{{Role: "user", Content: "give a name"}},
		Schema:    schema,
		MaxTokens: 50,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if gotReq == nil {
		t.Fatal("ChatCompletionRequest was not called")
	}
	if gotReq.Provider != bfschemas.OpenAI {
		t.Errorf("want provider=openai, got %q", gotReq.Provider)
	}
	if gotReq.Model != "gpt-4o" {
		t.Errorf("want model=gpt-4o, got %q", gotReq.Model)
	}
	// Expect 2 messages: system + user
	if len(gotReq.Input) != 2 {
		t.Fatalf("want 2 messages (system+user), got %d", len(gotReq.Input))
	}
	if gotReq.Input[0].Role != bfschemas.ChatMessageRoleSystem {
		t.Errorf("want first message role=system, got %q", gotReq.Input[0].Role)
	}
}

// TestBifrostSDK_CompleteModelOverrideAndReasoning covers D-100: a per-request
// Model overrides the configured model, and ReasoningEffort sets params.Reasoning.
// The default path (empty fields) must leave both untouched.
func TestBifrostSDK_CompleteModelOverrideAndReasoning(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)

	t.Run("override+reasoning", func(t *testing.T) {
		t.Parallel()
		var got *bfschemas.BifrostChatRequest
		fake := &fakeClient{chatFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			got = req
			return okChatResponse(`{"name":"Alice"}`), nil
		}}
		gw := newTestDriver(t, fake, 4)
		if _, err := gw.Complete(context.Background(), gateway.CompleteRequest{
			Messages:        []gateway.Message{{Role: "user", Content: "x"}},
			Schema:          schema,
			MaxTokens:       50,
			Model:           "anthropic/claude-sonnet-4.6",
			ReasoningEffort: "medium",
		}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if got.Model != "anthropic/claude-sonnet-4.6" {
			t.Errorf("per-request model override not applied: got %q", got.Model)
		}
		if got.Params == nil || got.Params.Reasoning == nil || got.Params.Reasoning.Effort == nil || *got.Params.Reasoning.Effort != "medium" {
			t.Errorf("reasoning effort not set to medium: %+v", got.Params)
		}
	})

	t.Run("default-no-reasoning", func(t *testing.T) {
		t.Parallel()
		var got *bfschemas.BifrostChatRequest
		fake := &fakeClient{chatFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			got = req
			return okChatResponse(`{"name":"Alice"}`), nil
		}}
		gw := newTestDriver(t, fake, 4)
		if _, err := gw.Complete(context.Background(), gateway.CompleteRequest{
			Messages:  []gateway.Message{{Role: "user", Content: "x"}},
			Schema:    schema,
			MaxTokens: 50,
		}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if got.Model != "gpt-4o" {
			t.Errorf("default model should be the configured one, got %q", got.Model)
		}
		if got.Params != nil && got.Params.Reasoning != nil {
			t.Errorf("no reasoning param should be set on the default path, got %+v", got.Params.Reasoning)
		}
	})
}

func TestBifrostSDK_CompleteTranslateResponse(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{
		chatFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			return okChatResponse(`{"greeting":"hello","count":1}`), nil
		},
	}

	gw := newTestDriver(t, fake, 4)
	schema := json.RawMessage(`{"type":"object","properties":{"greeting":{"type":"string"},"count":{"type":"integer"}},"required":["greeting","count"]}`)
	resp, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages:  []gateway.Message{{Role: "user", Content: "hi"}},
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
	if got["greeting"] != "hello" {
		t.Errorf("want greeting=hello, got %v", got["greeting"])
	}
}

// ─── Error classification ─────────────────────────────────────────────────────

func TestBifrostSDK_EmbedBifrostErrorPropagated(t *testing.T) {
	t.Parallel()

	statusCode := 429
	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			return nil, &bfschemas.BifrostError{
				StatusCode: &statusCode,
				Error:      &bfschemas.ErrorField{Message: "rate limited"},
			}
		},
	}

	gw := newTestDriver(t, fake, 4)
	_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBifrostSDK_CompleteBifrostErrorPropagated(t *testing.T) {
	t.Parallel()

	statusCode := 401
	fake := &fakeClient{
		chatFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			return nil, &bfschemas.BifrostError{
				StatusCode: &statusCode,
				Error:      &bfschemas.ErrorField{Message: "unauthorized"},
			}
		},
	}

	gw := newTestDriver(t, fake, 4)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		Schema:   json.RawMessage(`{"type":"object"}`),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ─── Fail-closed: missing API key ────────────────────────────────────────────

func TestBifrostSDK_FailsClosedOnMissingAPIKey(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openai",
		APIKey:     "env.STOWAGE_TEST_BIFROST_MISSING_KEY_9999",
		Model:      "m",
		EmbedModel: "e",
		EmbedDims:  4,
	}
	_, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err == nil {
		t.Fatal("expected error when API key env var is unset")
	}
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Errorf("want ErrMissingAPIKey, got: %v", err)
	}
}

// ─── Fail-closed: invalid provider ───────────────────────────────────────────

func TestBifrostSDK_FailsClosedOnInvalidProvider(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "not-a-real-provider",
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:      "m",
		EmbedModel: "e",
		EmbedDims:  4,
	}
	_, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err == nil {
		t.Fatal("expected error for invalid provider")
	}
	if !errors.Is(err, ErrInvalidProvider) {
		t.Errorf("want ErrInvalidProvider, got: %v", err)
	}
}

// ─── Fail-closed: empty provider ─────────────────────────────────────────────

func TestBifrostSDK_FailsClosedOnEmptyProvider(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "", // not set
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:      "m",
		EmbedModel: "e",
		EmbedDims:  4,
	}
	_, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err == nil {
		t.Fatal("expected error for empty provider")
	}
	if !errors.Is(err, ErrInvalidProvider) {
		t.Errorf("want ErrInvalidProvider, got: %v", err)
	}
}

// ─── Probe: dims mismatch ─────────────────────────────────────────────────────

func TestBifrostSDK_ProbeDimsMismatch(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			// Return 2-dim vector, driver configured for 4
			return okEmbedResponse([][]float64{{0.1, 0.2}}), nil
		},
	}

	gw := newTestDriver(t, fake, 4) // configured for 4 dims
	err := gw.Probe(context.Background())
	if err == nil {
		t.Fatal("expected probe to fail on dim mismatch")
	}
	if !errors.Is(err, gateway.ErrProbeFailed) {
		t.Errorf("want ErrProbeFailed, got %v", err)
	}
}

func TestBifrostSDK_ProbeSuccess(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			return okEmbedResponse([][]float64{{0.1, 0.2, 0.3, 0.4}}), nil
		},
	}

	gw := newTestDriver(t, fake, 4)
	if err := gw.Probe(context.Background()); err != nil {
		t.Errorf("expected probe success, got: %v", err)
	}
}

// ─── Schema validation + retry (seam policy) ─────────────────────────────────

func TestBifrostSDK_SchemaValidationRetryOnce(t *testing.T) {
	t.Parallel()

	callCount := 0
	fake := &fakeClient{
		chatFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			callCount++
			if callCount == 1 {
				return okChatResponse(`{"wrong":"field"}`), nil
			}
			return okChatResponse(`{"name":"Alice"}`), nil
		},
	}

	gw := newTestDriver(t, fake, 4)
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
		t.Fatalf("unmarshal: %v", err)
	}
	if got["name"] != "Alice" {
		t.Errorf("want name=Alice, got %v", got["name"])
	}
	if callCount != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", callCount)
	}
}

func TestBifrostSDK_SchemaValidationFailsTwice(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{
		chatFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			return okChatResponse(`{"wrong":"field"}`), nil
		},
	}

	gw := newTestDriver(t, fake, 4)
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages:  []gateway.Message{{Role: "user", Content: "give me a name"}},
		Schema:    schema,
		MaxTokens: 50,
	})
	if err == nil {
		t.Fatal("expected schema validation error")
	}
	if !errors.Is(err, gateway.ErrSchemaValidation) {
		t.Errorf("want ErrSchemaValidation, got %v", err)
	}
}

// ─── ErrTruncated ─────────────────────────────────────────────────────────────

func TestBifrostSDK_TruncatedResponse(t *testing.T) {
	t.Parallel()

	lengthReason := "length"
	fake := &fakeClient{
		chatFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			content := "partial"
			return &bfschemas.BifrostChatResponse{
				Choices: []bfschemas.BifrostResponseChoice{
					{
						FinishReason: &lengthReason,
						ChatNonStreamResponseChoice: &bfschemas.ChatNonStreamResponseChoice{
							Message: &bfschemas.ChatMessage{
								Role:    bfschemas.ChatMessageRoleAssistant,
								Content: &bfschemas.ChatMessageContent{ContentStr: &content},
							},
						},
					},
				},
			}, nil
		},
	}

	gw := newTestDriver(t, fake, 4)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		Schema:   json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, gateway.ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}

// ─── Account interface methods ────────────────────────────────────────────────

func TestAccount_GetConfiguredProviders(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openai",
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:      "m",
		EmbedModel: "e",
		EmbedDims:  4,
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	providers, err := account.GetConfiguredProviders()
	if err != nil {
		t.Fatalf("GetConfiguredProviders: %v", err)
	}
	if len(providers) != 1 || providers[0] != bfschemas.OpenAI {
		t.Errorf("want [openai], got %v", providers)
	}
}

func TestAccount_GetKeysForProvider(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openai",
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:      "m",
		EmbedModel: "e",
		EmbedDims:  4,
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}

	keys, err := account.GetKeysForProvider(context.Background(), bfschemas.OpenAI)
	if err != nil {
		t.Fatalf("GetKeysForProvider: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(keys))
	}
	if keys[0].ID != "stowage-default" {
		t.Errorf("want ID=stowage-default, got %q", keys[0].ID)
	}
}

func TestAccount_GetKeysForProvider_WrongProvider(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openai",
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:      "m",
		EmbedModel: "e",
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	_, err = account.GetKeysForProvider(context.Background(), bfschemas.Anthropic)
	if err == nil {
		t.Fatal("expected error for wrong provider")
	}
}

func TestAccount_GetConfigForProvider(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openai",
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		BaseURL:    "https://custom.endpoint",
		Model:      "m",
		EmbedModel: "e",
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}

	provCfg, err := account.GetConfigForProvider(bfschemas.OpenAI)
	if err != nil {
		t.Fatalf("GetConfigForProvider: %v", err)
	}
	if provCfg.NetworkConfig.BaseURL != "https://custom.endpoint" {
		t.Errorf("want BaseURL=https://custom.endpoint, got %q", provCfg.NetworkConfig.BaseURL)
	}
}

func TestAccount_GetConfigForProvider_WrongProvider(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openai",
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:      "m",
		EmbedModel: "e",
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	_, err = account.GetConfigForProvider(bfschemas.Anthropic)
	if err == nil {
		t.Fatal("expected error for wrong provider")
	}
}

// ─── Close: both shutdown paths ───────────────────────────────────────────────

func TestBifrostSDK_CloseTwiceIsIdempotent(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{}
	gw := newTestDriver(t, fake, 4)
	if err := gw.Close(context.Background()); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := gw.Close(context.Background()); err != nil {
		t.Errorf("second Close (idempotent): %v", err)
	}
}

func TestBifrostSDK_EmbedAfterClosedReturnsUnavailable(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{}
	gw := newTestDriver(t, fake, 4)
	_ = gw.Close(context.Background())
	_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if !errors.Is(err, gateway.ErrGatewayUnavailable) {
		t.Errorf("want ErrGatewayUnavailable after close, got %v", err)
	}
}

func TestBifrostSDK_CompleteAfterClosedReturnsUnavailable(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{}
	gw := newTestDriver(t, fake, 4)
	_ = gw.Close(context.Background())
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		Schema:   json.RawMessage(`{"type":"object"}`),
	})
	if !errors.Is(err, gateway.ErrGatewayUnavailable) {
		t.Errorf("want ErrGatewayUnavailable after close, got %v", err)
	}
}

func TestBifrostSDK_ProbeAfterClosedFails(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{}
	gw := newTestDriver(t, fake, 4)
	_ = gw.Close(context.Background())
	err := gw.Probe(context.Background())
	if !errors.Is(err, gateway.ErrProbeFailed) {
		t.Errorf("want ErrProbeFailed after close, got %v", err)
	}
}

// ─── Probe: error cases ───────────────────────────────────────────────────────

func TestBifrostSDK_ProbeEmbedFails(t *testing.T) {
	t.Parallel()

	statusCode := 500
	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			return nil, &bfschemas.BifrostError{
				StatusCode: &statusCode,
				Error:      &bfschemas.ErrorField{Message: "probe embed failed"},
			}
		},
	}

	gw := newTestDriver(t, fake, 4)
	err := gw.Probe(context.Background())
	if !errors.Is(err, gateway.ErrProbeFailed) {
		t.Errorf("want ErrProbeFailed, got %v", err)
	}
}

// ─── translateBifrostError: all branches ─────────────────────────────────────

func TestBifrostSDK_ErrorClassification(t *testing.T) {
	t.Parallel()

	// 403 (auth error branch)
	statusCode403 := 403
	fake403 := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			return nil, &bfschemas.BifrostError{
				StatusCode: &statusCode403,
				Error:      &bfschemas.ErrorField{Message: "forbidden"},
			}
		},
	}
	gw403 := newTestDriver(t, fake403, 4)
	_, err403 := gw403.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if err403 == nil {
		t.Error("403: expected error")
	}

	// no status code
	fakeNoCode := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			return nil, &bfschemas.BifrostError{
				StatusCode: nil,
				Error:      &bfschemas.ErrorField{Message: "no code"},
			}
		},
	}
	gwNoCode := newTestDriver(t, fakeNoCode, 4)
	_, errNoCode := gwNoCode.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
	if errNoCode == nil {
		t.Error("no-code: expected error")
	}
}

// ─── translateEmbedRequest: multiple texts ───────────────────────────────────

func TestBifrostSDK_EmbedMultipleTexts(t *testing.T) {
	t.Parallel()

	var gotReq *bfschemas.BifrostEmbeddingRequest
	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			gotReq = req
			return okEmbedResponse([][]float64{{0.1, 0.2}, {0.3, 0.4}}), nil
		},
	}

	gw := newTestDriver(t, fake, 2)
	resp, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Vectors) != 2 {
		t.Errorf("want 2 vectors, got %d", len(resp.Vectors))
	}
	// Verify multi-text path: Texts slice should be populated, not Text
	if gotReq != nil && gotReq.Input != nil {
		if gotReq.Input.Texts == nil {
			t.Error("want Texts slice for multi-text, got nil")
		}
	}
}

// ─── translateChatResponse: edge cases ───────────────────────────────────────

func TestBifrostSDK_CompleteNoSchema_Fails(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{}
	gw := newTestDriver(t, fake, 4)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		// No Schema — must fail
	})
	if err == nil {
		t.Fatal("expected error for missing schema")
	}
}

// ─── extractUsage: non-nil cost ───────────────────────────────────────────────

func TestBifrostSDK_ExtractUsageWithCost(t *testing.T) {
	t.Parallel()

	costVal := 0.005
	fake := &fakeClient{
		chatFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError) {
			content := `{"name":"Bob"}`
			finishReason := "stop"
			return &bfschemas.BifrostChatResponse{
				Choices: []bfschemas.BifrostResponseChoice{
					{
						FinishReason: &finishReason,
						ChatNonStreamResponseChoice: &bfschemas.ChatNonStreamResponseChoice{
							Message: &bfschemas.ChatMessage{
								Role:    bfschemas.ChatMessageRoleAssistant,
								Content: &bfschemas.ChatMessageContent{ContentStr: &content},
							},
						},
					},
				},
				Usage: &bfschemas.BifrostLLMUsage{
					PromptTokens:     10,
					CompletionTokens: 5,
					Cost:             &bfschemas.BifrostCost{TotalCost: costVal},
				},
			}, nil
		},
	}

	gw := newTestDriver(t, fake, 4)
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages:  []gateway.Message{{Role: "user", Content: "hi"}},
		Schema:    schema,
		MaxTokens: 50,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

// ─── Rerank ───────────────────────────────────────────────────────────────────

func TestBifrost_RerankClientFake(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{
		rerankFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostRerankRequest) (*bfschemas.BifrostRerankResponse, *bfschemas.BifrostError) {
			// Echo back relevance scores: doc 0 = high, doc 1 = low.
			return &bfschemas.BifrostRerankResponse{
				Results: []bfschemas.RerankResult{
					{Index: 0, RelevanceScore: 0.9},
					{Index: 1, RelevanceScore: 0.2},
				},
				Usage: &bfschemas.BifrostLLMUsage{PromptTokens: 2},
			}, nil
		},
	}

	cfg := config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "openai",
		APIKey:      "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:       "gpt-4o",
		EmbedModel:  "text-embedding-3-small",
		EmbedDims:   4,
		RerankModel: "cohere/rerank-4-fast",
	}
	d := NewDriverWithClient(fake, bfschemas.OpenAI, cfg, discardLog(), prometheus.NewRegistry())
	t.Cleanup(func() { d.Close(context.Background()) }) //nolint:errcheck

	resp, err := d.Rerank(context.Background(), gateway.RerankRequest{
		Query:     "golang concurrency",
		Documents: []string{"goroutines are great", "python has the GIL"},
	})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Score != 0.9 {
		t.Errorf("want first result score=0.9, got %v", resp.Results[0].Score)
	}
	if resp.Results[0].Index != 0 {
		t.Errorf("want first result index=0, got %d", resp.Results[0].Index)
	}
	// Usage: search_units = PromptTokens from bifrost response.
	if resp.Usage.SearchUnits != 2 {
		t.Errorf("want search_units=2, got %d", resp.Usage.SearchUnits)
	}
}

func TestBifrost_RerankAfterClosedReturnsUnavailable(t *testing.T) {
	t.Parallel()

	fake := &fakeClient{}
	cfg := config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "openai",
		APIKey:      "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:       "m",
		EmbedModel:  "e",
		EmbedDims:   4,
		RerankModel: "rerank-model",
	}
	d := NewDriverWithClient(fake, bfschemas.OpenAI, cfg, discardLog(), prometheus.NewRegistry())
	_ = d.Close(context.Background())

	_, err := d.Rerank(context.Background(), gateway.RerankRequest{
		Query:     "q",
		Documents: []string{"d"},
	})
	if !errors.Is(err, gateway.ErrGatewayUnavailable) {
		t.Errorf("want ErrGatewayUnavailable after close, got %v", err)
	}
}

func TestBifrost_RerankBifrostError(t *testing.T) {
	t.Parallel()

	statusCode := 500
	fake := &fakeClient{
		rerankFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostRerankRequest) (*bfschemas.BifrostRerankResponse, *bfschemas.BifrostError) {
			return nil, &bfschemas.BifrostError{
				StatusCode: &statusCode,
				Error:      &bfschemas.ErrorField{Message: "internal error"},
			}
		},
	}
	cfg := config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "openai",
		APIKey:      "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:       "m",
		EmbedModel:  "e",
		EmbedDims:   4,
		RerankModel: "rerank-model",
	}
	d := NewDriverWithClient(fake, bfschemas.OpenAI, cfg, discardLog(), prometheus.NewRegistry())
	t.Cleanup(func() { d.Close(context.Background()) }) //nolint:errcheck

	_, err := d.Rerank(context.Background(), gateway.RerankRequest{
		Query:     "q",
		Documents: []string{"d"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ─── Seam wiring: breaker integrates with SDK driver ─────────────────────────

func TestBifrostSDK_BreakerOpensAfterFailures(t *testing.T) {
	t.Parallel()

	statusCode := 500
	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, _ *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			return nil, &bfschemas.BifrostError{
				StatusCode: &statusCode,
				Error:      &bfschemas.ErrorField{Message: "internal error"},
			}
		},
	}

	gw := newTestDriver(t, fake, 4)

	// Drive enough failures to open the breaker (threshold: 5 consecutive
	// failures in the gateway.CircuitBreaker). Each Embed call to the batcher
	// goes through embedBatch which records breaker failures.
	var openErr error
	for i := range 10 {
		_, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
		if errors.Is(err, gateway.ErrGatewayUnavailable) {
			openErr = err
			t.Logf("breaker opened on call %d", i+1)
			break
		}
	}
	if !errors.Is(openErr, gateway.ErrGatewayUnavailable) {
		// One more call after the loop should get fast-failed.
		_, openErr = gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}})
		if !errors.Is(openErr, gateway.ErrGatewayUnavailable) {
			t.Errorf("expected ErrGatewayUnavailable after repeated failures, got: %v", openErr)
		}
	}
}
