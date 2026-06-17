//go:build live

package bifrost_test

// Live tests: exercise the bifrost driver against a real OpenRouter account so a
// single bifrost gateway runs the whole stack (embed + rerank) on OpenRouter
// with one key (D-075). Rerank goes through the AUTO-WIRED Cohere-shape custom
// provider — this is the path the openrouter built-in provider cannot serve.
//
// Requires (env-gated; SKIP without it):
//
//	OPENROUTER_API_KEY        — the OpenRouter API key (read directly via env.* ref)
//	STOWAGE_LIVE_EMBED_MODEL  — optional; default "perplexity/pplx-embed-v1-0.6b"
//	STOWAGE_LIVE_RERANK_MODEL — optional; default "cohere/rerank-4-fast"
//	STOWAGE_LIVE_EMBED_DIMS   — optional; default 1024
//
// Run with (the coordinator supplies the key — never committed):
//
//	OPENROUTER_API_KEY=... go test -tags=live -v -run TestLiveBifrost ./internal/gateway/bifrost/
//
// NOT run in CI or preflight (CLAUDE.md §14).

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
)

const liveOpenRouterBase = "https://openrouter.ai/api/v1"

// liveKeyRef skips the test unless OPENROUTER_API_KEY is set, returning the
// env.* reference the driver resolves (the key VALUE is never inlined).
func liveKeyRef(t *testing.T) string {
	t.Helper()
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set — live bifrost tests are operator-triggered")
	}
	return "env.OPENROUTER_API_KEY"
}

func liveEmbedModel() string {
	if v := os.Getenv("STOWAGE_LIVE_EMBED_MODEL"); v != "" {
		return v
	}
	return "perplexity/pplx-embed-v1-0.6b"
}

func liveRerankModel() string {
	if v := os.Getenv("STOWAGE_LIVE_RERANK_MODEL"); v != "" {
		return v
	}
	return "cohere/rerank-4-fast"
}

func liveEmbedDims() int {
	if v := os.Getenv("STOWAGE_LIVE_EMBED_DIMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1024
}

// TestLiveBifrost_Embed verifies bifrost + OpenRouter embeds via the primary
// provider and returns a non-zero vector.
func TestLiveBifrost_Embed(t *testing.T) {
	keyRef := liveKeyRef(t)

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		Provider:   "openrouter",
		BaseURL:    liveOpenRouterBase,
		APIKey:     keyRef,
		Model:      "inception/mercury-2",
		EmbedModel: liveEmbedModel(),
		EmbedDims:  liveEmbedDims(),
	}
	gw, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer gw.Close(context.Background()) //nolint:errcheck

	resp, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"Go is a statically typed language."}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(resp.Vectors) != 1 || len(resp.Vectors[0]) == 0 {
		t.Fatalf("expected one non-empty vector, got %d vectors", len(resp.Vectors))
	}
	var nonZero bool
	for _, v := range resp.Vectors[0] {
		if v != 0 {
			nonZero = true
			break
		}
	}
	if !nonZero {
		t.Fatal("embed vector is all zeros")
	}
	t.Logf("live embed: model=%s dims=%d", cfg.EmbedModel, len(resp.Vectors[0]))
}

// TestLiveBifrost_Rerank verifies the AUTO-WIRED Cohere-shape custom provider
// reranks over OpenRouter through the Stowage bifrost driver (D-075/AC-1): real,
// sorted relevance scores. This is the path that PROVES bifrost can now run the
// full OpenRouter stack — it previously failed ("rerank not supported").
func TestLiveBifrost_Rerank(t *testing.T) {
	keyRef := liveKeyRef(t)

	cfg := config.GatewayConfig{
		Driver:      "bifrost",
		Provider:    "openrouter",
		BaseURL:     liveOpenRouterBase,
		APIKey:      keyRef,
		Model:       "inception/mercury-2",
		EmbedModel:  liveEmbedModel(),
		EmbedDims:   liveEmbedDims(),
		RerankModel: liveRerankModel(),
	}
	gw, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer gw.Close(context.Background()) //nolint:errcheck

	resp, err := gw.Rerank(context.Background(), gateway.RerankRequest{
		Query: "what is the Go programming language",
		Documents: []string{
			"Go is an open source programming language that makes it easy to build simple, reliable, and efficient software.",
			"Python is a high-level, general-purpose programming language.",
			"The Go gopher is the mascot of the language.",
		},
		TopN: 3,
	})
	if err != nil {
		t.Fatalf("Rerank (auto-wired custom provider): %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected at least one rerank result")
	}
	// Results must be sorted by descending relevance.
	for i := 1; i < len(resp.Results); i++ {
		if resp.Results[i-1].Score < resp.Results[i].Score {
			t.Errorf("results not sorted by score: [%d]=%v < [%d]=%v",
				i-1, resp.Results[i-1].Score, i, resp.Results[i].Score)
		}
	}
	t.Logf("live rerank: model=%s results=%+v search_units=%d", cfg.RerankModel, resp.Results, resp.Usage.SearchUnits)
}
