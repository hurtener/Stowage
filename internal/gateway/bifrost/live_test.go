//go:build live

package bifrost_test

// Live test: exercises Complete against OpenRouter's chat/completions endpoint.
// Embeddings are skipped (OpenRouter has no embedding models as of 2026-06-11).
//
// Requires:
//   STOWAGE_TEST_OPENROUTER_KEY   — OpenRouter API key (env.* reference resolved here)
//   STOWAGE_TEST_OPENROUTER_MODEL — model slug, e.g. "openai/gpt-4o-mini"
//
// Run with:
//   STOWAGE_TEST_OPENROUTER_KEY=... STOWAGE_TEST_OPENROUTER_MODEL=... \
//     go test -tags=live -v -run TestLive ./internal/gateway/bifrost/
//
// NOT run in CI or preflight (CLAUDE.md §14).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/bifrost"
	"github.com/prometheus/client_golang/prometheus"
)

const openRouterBase = "https://openrouter.ai/api/v1"

func liveEnv(t *testing.T) (keyEnvVar, model string) {
	t.Helper()
	keyEnvVar = os.Getenv("STOWAGE_TEST_OPENROUTER_KEY")
	model = os.Getenv("STOWAGE_TEST_OPENROUTER_MODEL")
	if keyEnvVar == "" || model == "" {
		t.Skip("STOWAGE_TEST_OPENROUTER_KEY and STOWAGE_TEST_OPENROUTER_MODEL must be set")
	}
	return keyEnvVar, model
}

func TestLiveBifrost_CompleteSchemaConstrained(t *testing.T) {
	apiKey, model := liveEnv(t)

	// Expose the key under a well-known env var so ResolveEnvRef can resolve it.
	const keyEnv = "STOWAGE_LIVE_OPENROUTER_KEY"
	if err := os.Setenv(keyEnv, apiKey); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() { os.Unsetenv(keyEnv) }) //nolint:errcheck

	cfg := config.GatewayConfig{
		Driver:     "bifrost",
		BaseURL:    openRouterBase,
		APIKey:     fmt.Sprintf("env.%s", keyEnv),
		Model:      model,
		EmbedModel: "unused",
		EmbedDims:  1,
	}
	gw, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer gw.Close(context.Background()) //nolint:errcheck

	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"greeting": {"type": "string"},
			"count":    {"type": "integer"}
		},
		"required": ["greeting", "count"],
		"additionalProperties": false
	}`)

	resp, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		System: "You are a helpful assistant. Always respond in valid JSON.",
		Messages: []gateway.Message{
			{Role: "user", Content: "Say hello and give count=1."},
		},
		Schema:    schema,
		MaxTokens: 128,
	})
	if err != nil {
		if errors.Is(err, gateway.ErrSchemaValidation) {
			t.Fatalf("schema validation failed (model did not follow json_schema): %v", err)
		}
		t.Fatalf("Complete: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(resp.JSON, &got); err != nil {
		t.Fatalf("unmarshal response JSON: %v\nbody: %s", err, resp.JSON)
	}
	if _, ok := got["greeting"].(string); !ok {
		t.Errorf("expected string 'greeting' field, got: %v", got)
	}
	if _, ok := got["count"].(float64); !ok {
		t.Errorf("expected numeric 'count' field, got: %v", got)
	}
	t.Logf("live response: %s (in=%d out=%d)", resp.JSON, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}
