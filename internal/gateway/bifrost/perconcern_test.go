package bifrost_test

import (
	"context"
	"testing"

	bfschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/gateway/bifrost"
)

// keyVal resolves the single key value an Account returns for a provider (a1b).
func keyVal(t *testing.T, a *bifrost.Account, p bfschemas.ModelProvider) string {
	t.Helper()
	keys, err := a.GetKeysForProvider(context.Background(), p)
	if err != nil {
		t.Fatalf("GetKeysForProvider(%s): %v", p, err)
	}
	if len(keys) != 1 {
		t.Fatalf("want 1 key for %s, got %d", p, len(keys))
	}
	return keys[0].Value.Val
}

func hasProvider(provs []bfschemas.ModelProvider, p bfschemas.ModelProvider) bool {
	for _, q := range provs {
		if q == p {
			return true
		}
	}
	return false
}

// orDefaultCfg is the a1 default-shaped gateway config (OpenRouter, full stack)
// used as the base for per-concern tests. TestMain sets STOWAGE_TEST_BIFROST_SDK_KEY.
func orDefaultCfg() config.GatewayConfig {
	return config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "openrouter",
		APIKey:      "env.STOWAGE_TEST_BIFROST_SDK_KEY",
		Model:       "openai/gpt-5.4-nano",
		EmbedModel:  "perplexity/pplx-embed-v1-0.6b",
		EmbedDims:   1024,
		RerankModel: "cohere/rerank-4-fast",
	}
}

// TestAccount_PerConcernEmpty_Fallback verifies that with no per-concern overrides
// the wiring is identical to a1: embed uses the primary provider+key, the rerank
// provider inherits the primary key (a1b AC-3).
func TestAccount_PerConcernEmpty_Fallback(t *testing.T) {
	a, err := bifrost.NewAccount(orDefaultCfg())
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if a.DistinctEmbed() {
		t.Error("expected no distinct embed provider when embed_provider empty")
	}
	if a.EmbedProviderName() != bfschemas.OpenRouter {
		t.Errorf("embed provider = %q, want openrouter (inherit)", a.EmbedProviderName())
	}
	if got := keyVal(t, a, bfschemas.OpenRouter); got != "test-key" {
		t.Errorf("primary key = %q, want test-key", got)
	}
	// OpenRouter is non-native rerank + rerank_model set → custom rerank wired,
	// inheriting the primary key.
	if got := keyVal(t, a, bifrost.CustomRerankProviderName()); got != "test-key" {
		t.Errorf("rerank key = %q, want inherit primary (test-key)", got)
	}
	provs, _ := a.GetConfiguredProviders()
	if len(provs) != 2 || !hasProvider(provs, bfschemas.OpenRouter) || !hasProvider(provs, bifrost.CustomRerankProviderName()) {
		t.Errorf("providers = %v, want [openrouter stowage-rerank]", provs)
	}
}

// TestAccount_DistinctEmbedProvider verifies embed_provider routes embedding to a
// distinct provider with its own key + native (empty) base, primary unaffected (AC-4).
func TestAccount_DistinctEmbedProvider(t *testing.T) {
	t.Setenv("STOWAGE_TEST_EMBED_KEY", "embed-key")
	cfg := orDefaultCfg()
	cfg.EmbedProvider = "openai"
	cfg.EmbedAPIKey = "env.STOWAGE_TEST_EMBED_KEY"
	cfg.EmbedModel = "text-embedding-3-small"
	cfg.EmbedDims = 1536

	a, err := bifrost.NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if !a.DistinctEmbed() || a.EmbedProviderName() != bfschemas.OpenAI {
		t.Fatalf("embed provider = %q distinct=%v, want openai/true", a.EmbedProviderName(), a.DistinctEmbed())
	}
	if got := keyVal(t, a, bfschemas.OpenAI); got != "embed-key" {
		t.Errorf("embed key = %q, want embed-key (distinct)", got)
	}
	if got := keyVal(t, a, bfschemas.OpenRouter); got != "test-key" {
		t.Errorf("primary key = %q, want test-key (unaffected)", got)
	}
	provs, _ := a.GetConfiguredProviders()
	if !hasProvider(provs, bfschemas.OpenAI) {
		t.Errorf("openai not in configured providers %v", provs)
	}
	pc, err := a.GetConfigForProvider(bfschemas.OpenAI)
	if err != nil {
		t.Fatalf("GetConfigForProvider(openai): %v", err)
	}
	if pc.NetworkConfig.BaseURL != "" {
		t.Errorf("embed base = %q, want empty (OpenAI native endpoint)", pc.NetworkConfig.BaseURL)
	}
}

// TestAccount_DistinctEmbedInheritsPrimaryKey verifies an empty embed_api_key falls
// back to the primary key even when embed routes to a distinct provider.
func TestAccount_DistinctEmbedInheritsPrimaryKey(t *testing.T) {
	cfg := orDefaultCfg()
	cfg.EmbedProvider = "openai" // no embed_api_key → inherit primary
	cfg.EmbedModel = "text-embedding-3-small"
	cfg.EmbedDims = 1536

	a, err := bifrost.NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if got := keyVal(t, a, bfschemas.OpenAI); got != "test-key" {
		t.Errorf("embed key = %q, want test-key (inherit primary)", got)
	}
}

// TestAccount_RerankAPIKeyOverride verifies rerank_api_key gives the rerank provider
// its own credential, leaving the primary key untouched (AC-5).
func TestAccount_RerankAPIKeyOverride(t *testing.T) {
	t.Setenv("STOWAGE_TEST_RERANK_KEY", "rerank-key")
	cfg := orDefaultCfg()
	cfg.RerankAPIKey = "env.STOWAGE_TEST_RERANK_KEY"

	a, err := bifrost.NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if got := keyVal(t, a, bifrost.CustomRerankProviderName()); got != "rerank-key" {
		t.Errorf("rerank key = %q, want rerank-key (override)", got)
	}
	if got := keyVal(t, a, bfschemas.OpenRouter); got != "test-key" {
		t.Errorf("primary key = %q, want test-key (unaffected)", got)
	}
}

// TestAccount_DistinctNativeRerankProvider verifies rerank_provider naming a native
// rerank provider routes rerank there natively (no custom Cohere-shape) with its key.
func TestAccount_DistinctNativeRerankProvider(t *testing.T) {
	t.Setenv("STOWAGE_TEST_RERANK_KEY", "rerank-key")
	cfg := orDefaultCfg()
	cfg.RerankProvider = "cohere" // native rerank provider
	cfg.RerankAPIKey = "env.STOWAGE_TEST_RERANK_KEY"

	a, err := bifrost.NewAccount(cfg)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if a.CustomRerank() {
		t.Error("a native rerank provider must NOT auto-wire the Cohere-shape custom provider")
	}
	if a.RerankProviderName() != bfschemas.Cohere {
		t.Errorf("rerank provider = %q, want cohere", a.RerankProviderName())
	}
	if got := keyVal(t, a, bfschemas.Cohere); got != "rerank-key" {
		t.Errorf("rerank key = %q, want rerank-key", got)
	}
	provs, _ := a.GetConfiguredProviders()
	if !hasProvider(provs, bfschemas.Cohere) {
		t.Errorf("cohere not in configured providers %v", provs)
	}
	// Blocker-1 guard: the distinct cohere rerank provider must NOT inherit the
	// primary's OpenRouter base URL; empty rerank_base_url → cohere's native endpoint.
	pc, err := a.GetConfigForProvider(bfschemas.Cohere)
	if err != nil {
		t.Fatalf("GetConfigForProvider(cohere): %v", err)
	}
	if pc.NetworkConfig.BaseURL != "" {
		t.Errorf("cohere rerank base = %q, want empty (native, not the primary's OpenRouter host)", pc.NetworkConfig.BaseURL)
	}
}

// TestAccount_UnknownRerankProviderFailsLoud verifies a typo'd rerank_provider fails
// at construction rather than silently routing to the custom Cohere-shape (review #2).
func TestAccount_UnknownRerankProviderFailsLoud(t *testing.T) {
	cfg := orDefaultCfg()
	cfg.RerankProvider = "cohaire" // typo of cohere
	if _, err := bifrost.NewAccount(cfg); err == nil {
		t.Fatal("NewAccount with unknown rerank_provider = nil error, want fail-loud")
	}
}

// TestAccount_EmbedRerankSameProviderConflict verifies that naming the same provider
// for embed and rerank with DIFFERENT keys fails loud (review #3), while matching
// key+base is allowed (collapses to one entry).
func TestAccount_EmbedRerankSameProviderConflict(t *testing.T) {
	t.Setenv("STOWAGE_TEST_EMBED_KEY", "embed-key")
	t.Setenv("STOWAGE_TEST_RERANK_KEY", "rerank-key")
	base := orDefaultCfg()
	base.EmbedProvider = "cohere"
	base.RerankProvider = "cohere"

	// Different keys → fail loud.
	diff := base
	diff.EmbedAPIKey = "env.STOWAGE_TEST_EMBED_KEY"
	diff.RerankAPIKey = "env.STOWAGE_TEST_RERANK_KEY"
	if _, err := bifrost.NewAccount(diff); err == nil {
		t.Error("embed_provider==rerank_provider with different keys = nil error, want fail-loud")
	}

	// Same key (both inherit primary) → allowed.
	same := base
	if _, err := bifrost.NewAccount(same); err != nil {
		t.Errorf("embed_provider==rerank_provider with matching (inherited) key: %v", err)
	}
}

// TestDriver_EmbedRoutesToEmbedProvider verifies the Driver sends the embed request
// to embed_provider, not the primary completion provider (a1b AC-4, driver seam).
func TestDriver_EmbedRoutesToEmbedProvider(t *testing.T) {
	cfg := orDefaultCfg()
	cfg.EmbedProvider = "openai"
	cfg.EmbedModel = "text-embedding-3-small"
	cfg.EmbedDims = 4

	var gotProvider bfschemas.ModelProvider
	fake := &fakeClient{
		embedFn: func(_ *bfschemas.BifrostContext, req *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError) {
			gotProvider = req.Provider
			return okEmbedResponse([][]float64{{0.1, 0.2, 0.3, 0.4}}), nil
		},
	}
	d := bifrost.NewDriverWithClient(fake, bfschemas.OpenRouter, cfg, discardLog(), prometheus.NewRegistry())
	if d.EmbedProviderName() != bfschemas.OpenAI {
		t.Fatalf("driver embed provider = %q, want openai", d.EmbedProviderName())
	}
	if _, err := d.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"hello"}}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotProvider != bfschemas.OpenAI {
		t.Errorf("embed routed to %q, want openai (embed_provider)", gotProvider)
	}
}
