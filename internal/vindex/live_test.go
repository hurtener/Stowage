//go:build live

package vindex_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat"
)

// TestLiveEmbedSemanticOrdering validates the real Embed wire path end-to-end
// (gated; not CI): related texts must rank above unrelated by cosine.
// Run: STOWAGE_TEST_OPENROUTER_KEY=... go test -tags=live -run Live ./internal/vindex/
func TestLiveEmbedSemanticOrdering(t *testing.T) {
	key := os.Getenv("STOWAGE_TEST_OPENROUTER_KEY")
	if key == "" {
		t.Skip("STOWAGE_TEST_OPENROUTER_KEY not set")
	}
	t.Setenv("STOWAGE_LIVE_EMBED_KEY", key)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw, err := gateway.Open(context.Background(), config.GatewayConfig{
		Driver:     "openaicompat",
		BaseURL:    "https://openrouter.ai/api/v1",
		APIKey:     "env.STOWAGE_LIVE_EMBED_KEY",
		Model:      "google/gemini-3.5-flash",
		EmbedModel: "google/gemini-embedding-2",
		EmbedDims:  3072,
	}, log, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer gw.Close(context.Background()) //nolint:errcheck

	// Sequential single-input calls: the upstream behind OpenRouter caps
	// batched embedding requests (observed 200+error-envelope on batch).
	texts := []string{
		"The user prefers concise answers with code examples.",
		"Keep replies short and always include a code snippet.",
		"The quarterly marketing budget for outdoor advertising in Brazil.",
	}
	vectors := make([][]float32, len(texts))
	for i, txt := range texts {
		resp, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{txt}})
		if err != nil {
			t.Fatalf("embed %d: %v", i, err)
		}
		if len(resp.Vectors) != 1 || len(resp.Vectors[0]) != 3072 {
			t.Fatalf("embed %d: unexpected shape %d×%d", i, len(resp.Vectors), len(resp.Vectors[0]))
		}
		vectors[i] = resp.Vectors[0]
	}

	cos := func(a, b []float32) float64 {
		var dot, na, nb float64
		for i := range a {
			dot += float64(a[i]) * float64(b[i])
			na += float64(a[i]) * float64(a[i])
			nb += float64(b[i]) * float64(b[i])
		}
		return dot / (sqrt(na) * sqrt(nb))
	}
	related := cos(vectors[0], vectors[1])
	unrelated := cos(vectors[0], vectors[2])
	t.Logf("related=%.4f unrelated=%.4f", related, unrelated)
	if related <= unrelated {
		t.Fatalf("semantic ordering violated: related %.4f <= unrelated %.4f", related, unrelated)
	}
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 24; i++ {
		z = (z + x/z) / 2
	}
	return z
}
