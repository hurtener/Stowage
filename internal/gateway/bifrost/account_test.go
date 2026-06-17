package bifrost_test

import (
	"context"
	"testing"

	bfschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	. "github.com/hurtener/stowage/internal/gateway/bifrost"
)

// ─── Native-rerank gate ────────────────────────────────────────────────────────

func TestIsNativeRerankProvider(t *testing.T) {
	t.Parallel()
	native := []bfschemas.ModelProvider{bfschemas.Cohere, bfschemas.VLLM, bfschemas.Bedrock, bfschemas.Vertex}
	for _, p := range native {
		if !IsNativeRerankProvider(p) {
			t.Errorf("%q should be native-rerank", p)
		}
	}
	nonNative := []bfschemas.ModelProvider{bfschemas.OpenRouter, bfschemas.OpenAI, bfschemas.Anthropic, bfschemas.Gemini}
	for _, p := range nonNative {
		if IsNativeRerankProvider(p) {
			t.Errorf("%q should NOT be native-rerank", p)
		}
	}
}

// ─── Account auto-wiring (D-075) ────────────────────────────────────────────────

// TestAccount_AutoWiresCustomRerank_NonNativePlusModel asserts the synthetic
// Cohere-shape rerank provider is wired when the primary is non-native and a
// rerank model is configured (the OpenRouter full-stack case).
func TestAccount_AutoWiresCustomRerank_NonNativePlusModel(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "openrouter",
		APIKey:      "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		BaseURL:     "https://openrouter.ai/api/v1",
		Model:       "inception/mercury-2",
		EmbedModel:  "perplexity/pplx-embed-v1-0.6b",
		EmbedDims:   1024,
		RerankModel: "cohere/rerank-4-fast",
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}

	if !account.CustomRerank() {
		t.Fatal("expected customRerank=true for non-native primary + rerank model")
	}
	if account.RerankProviderName() != CustomRerankProviderName() {
		t.Errorf("rerank provider = %q, want %q", account.RerankProviderName(), CustomRerankProviderName())
	}

	// GetConfiguredProviders → [primary, stowage-rerank].
	provs, err := account.GetConfiguredProviders()
	if err != nil {
		t.Fatalf("GetConfiguredProviders: %v", err)
	}
	if len(provs) != 2 || provs[0] != bfschemas.OpenRouter || provs[1] != CustomRerankProviderName() {
		t.Errorf("providers = %v, want [openrouter %s]", provs, CustomRerankProviderName())
	}

	// GetConfigForProvider(stowage-rerank) → Cohere base, /rerank path, base URL.
	rc, err := account.GetConfigForProvider(CustomRerankProviderName())
	if err != nil {
		t.Fatalf("GetConfigForProvider(rerank): %v", err)
	}
	if rc.NetworkConfig.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("rerank base URL = %q, want OpenRouter base", rc.NetworkConfig.BaseURL)
	}
	if rc.CustomProviderConfig == nil {
		t.Fatal("rerank config has no CustomProviderConfig")
	}
	if rc.CustomProviderConfig.BaseProviderType != bfschemas.Cohere {
		t.Errorf("base provider type = %q, want cohere", rc.CustomProviderConfig.BaseProviderType)
	}
	if rc.CustomProviderConfig.AllowedRequests == nil || !rc.CustomProviderConfig.AllowedRequests.Rerank {
		t.Error("rerank not in AllowedRequests")
	}
	if got := rc.CustomProviderConfig.RequestPathOverrides[bfschemas.RerankRequest]; got != "/rerank" {
		t.Errorf("rerank path override = %q, want /rerank", got)
	}

	// GetKeysForProvider(stowage-rerank) → same key, wildcard Models.
	keys, err := account.GetKeysForProvider(context.Background(), CustomRerankProviderName())
	if err != nil {
		t.Fatalf("GetKeysForProvider(rerank): %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(keys))
	}
	if !keys[0].Models.IsUnrestricted() {
		t.Errorf("rerank key Models = %v, want wildcard {\"*\"}", keys[0].Models)
	}
}

// TestAccount_RerankBaseURLOverride asserts rerank_base_url overrides base_url
// for the auto-wired rerank provider only (embed/complete keep base_url).
func TestAccount_RerankBaseURLOverride(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:        "bifrost",
		Provider:      "openrouter",
		APIKey:        "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		BaseURL:       "https://openrouter.ai/api/v1",
		Model:         "m",
		EmbedModel:    "e",
		RerankModel:   "cohere/rerank-4-fast",
		RerankBaseURL: "https://rerank.example.com/v1",
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if account.RerankBaseURL() != "https://rerank.example.com/v1" {
		t.Errorf("rerank base URL = %q, want override", account.RerankBaseURL())
	}
	// Primary config still uses base_url.
	pc, err := account.GetConfigForProvider(bfschemas.OpenRouter)
	if err != nil {
		t.Fatalf("GetConfigForProvider(primary): %v", err)
	}
	if pc.NetworkConfig.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("primary base URL = %q, want base_url unchanged", pc.NetworkConfig.BaseURL)
	}
	rc, err := account.GetConfigForProvider(CustomRerankProviderName())
	if err != nil {
		t.Fatalf("GetConfigForProvider(rerank): %v", err)
	}
	if rc.NetworkConfig.BaseURL != "https://rerank.example.com/v1" {
		t.Errorf("rerank base URL = %q, want override", rc.NetworkConfig.BaseURL)
	}
}

// TestAccount_NativePrimaryNoCustomRerank asserts a native-rerank primary (e.g.
// cohere) does NOT get a custom provider — rerank routes to the primary (AC-2).
func TestAccount_NativePrimaryNoCustomRerank(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "cohere",
		APIKey:      "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:       "command-r",
		EmbedModel:  "embed-v4",
		RerankModel: "rerank-4-fast",
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if account.CustomRerank() {
		t.Error("native primary (cohere) must NOT auto-wire a custom rerank provider")
	}
	if account.RerankProviderName() != bfschemas.Cohere {
		t.Errorf("rerank provider = %q, want cohere (primary)", account.RerankProviderName())
	}
	provs, err := account.GetConfiguredProviders()
	if err != nil {
		t.Fatalf("GetConfiguredProviders: %v", err)
	}
	if len(provs) != 1 || provs[0] != bfschemas.Cohere {
		t.Errorf("providers = %v, want [cohere] only", provs)
	}
}

// TestAccount_NoRerankModelNoCustomProvider asserts a non-native primary with NO
// rerank model configured does not auto-wire the custom provider.
func TestAccount_NoRerankModelNoCustomProvider(t *testing.T) {
	t.Parallel()

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openrouter",
		APIKey:     "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:      "m",
		EmbedModel: "e",
		// RerankModel intentionally empty.
	}
	account, err := NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if account.CustomRerank() {
		t.Error("no rerank model → no custom rerank provider")
	}
	provs, _ := account.GetConfiguredProviders()
	if len(provs) != 1 {
		t.Errorf("providers = %v, want single primary", provs)
	}
}

// ─── Driver routing (D-075) ─────────────────────────────────────────────────────

// TestDriver_RerankRoutesToCustomProvider asserts the Driver sends Rerank to the
// auto-wired stowage-rerank provider for a non-native primary + rerank model,
// while embed routes to the primary (AC-1/AC-4 routing, via the fake client).
func TestDriver_RerankRoutesToCustomProvider(t *testing.T) {
	t.Parallel()

	var rerankProvider, embedProvider bfschemas.ModelProvider
	fake := &fakeClient{
		rerankFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostRerankRequest) (*bfschemas.BifrostRerankResponse, *bfschemas.BifrostError) {
			rerankProvider = req.Provider
			return &bfschemas.BifrostRerankResponse{
				Results: []bfschemas.RerankResult{{Index: 0, RelevanceScore: 0.71}},
				Usage:   &bfschemas.BifrostLLMUsage{PromptTokens: 1},
			}, nil
		},
		embedFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			embedProvider = req.Provider
			return okEmbedResponse([][]float64{{0.1, 0.2, 0.3, 0.4}}), nil
		},
	}

	cfg := config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "openrouter",
		APIKey:      "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		BaseURL:     "https://openrouter.ai/api/v1",
		Model:       "inception/mercury-2",
		EmbedModel:  "perplexity/pplx-embed-v1-0.6b",
		EmbedDims:   4,
		RerankModel: "cohere/rerank-4-fast",
	}
	d := NewDriverWithClient(fake, bfschemas.OpenRouter, cfg, discardLog(), prometheus.NewRegistry())
	t.Cleanup(func() { d.Close(context.Background()) }) //nolint:errcheck

	if d.RerankProviderName() != CustomRerankProviderName() {
		t.Errorf("driver rerank provider = %q, want %q", d.RerankProviderName(), CustomRerankProviderName())
	}

	if _, err := d.Rerank(context.Background(), gateway.RerankRequest{
		Query:     "golang concurrency",
		Documents: []string{"goroutines", "the GIL"},
	}); err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if rerankProvider != CustomRerankProviderName() {
		t.Errorf("rerank routed to %q, want %q", rerankProvider, CustomRerankProviderName())
	}

	if _, err := d.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"x"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if embedProvider != bfschemas.OpenRouter {
		t.Errorf("embed routed to %q, want primary openrouter", embedProvider)
	}
}

// TestDriver_RerankRoutesToPrimaryWhenNative asserts a native-rerank primary
// routes Rerank straight to the primary provider (no custom indirection).
func TestDriver_RerankRoutesToPrimaryWhenNative(t *testing.T) {
	t.Parallel()

	var rerankProvider bfschemas.ModelProvider
	fake := &fakeClient{
		rerankFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostRerankRequest) (*bfschemas.BifrostRerankResponse, *bfschemas.BifrostError) {
			rerankProvider = req.Provider
			return &bfschemas.BifrostRerankResponse{
				Results: []bfschemas.RerankResult{{Index: 0, RelevanceScore: 0.5}},
				Usage:   &bfschemas.BifrostLLMUsage{PromptTokens: 1},
			}, nil
		},
	}
	cfg := config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "cohere",
		APIKey:      "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:       "command-r",
		EmbedModel:  "embed-v4",
		EmbedDims:   4,
		RerankModel: "rerank-4-fast",
	}
	d := NewDriverWithClient(fake, bfschemas.Cohere, cfg, discardLog(), prometheus.NewRegistry())
	t.Cleanup(func() { d.Close(context.Background()) }) //nolint:errcheck

	if d.RerankProviderName() != bfschemas.Cohere {
		t.Errorf("driver rerank provider = %q, want cohere", d.RerankProviderName())
	}
	if _, err := d.Rerank(context.Background(), gateway.RerankRequest{
		Query:     "q",
		Documents: []string{"d"},
	}); err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if rerankProvider != bfschemas.Cohere {
		t.Errorf("rerank routed to %q, want primary cohere", rerankProvider)
	}
}
