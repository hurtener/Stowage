package mock_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/mock"
	"github.com/prometheus/client_golang/prometheus"
)

func newMock(t *testing.T, dims int) gateway.Gateway {
	t.Helper()
	cfg := config.GatewayConfig{
		Driver:     "mock",
		Model:      "test-model",
		EmbedModel: "test-embed",
		EmbedDims:  dims,
	}
	gw, err := gateway.Open(context.Background(), cfg, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open mock: %v", err)
	}
	t.Cleanup(func() { gw.Close(context.Background()) }) //nolint:errcheck
	return gw
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── Embed ─────────────────────────────────────────────────────────────────────

func TestMock_EmbedReturnsDeterministicVectors(t *testing.T) {
	t.Parallel()

	dims := 8
	gw := newMock(t, dims)

	r1, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"hello"}})
	if err != nil {
		t.Fatalf("first embed: %v", err)
	}
	r2, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"hello"}})
	if err != nil {
		t.Fatalf("second embed: %v", err)
	}

	if len(r1.Vectors[0]) != dims {
		t.Errorf("want %d dims, got %d", dims, len(r1.Vectors[0]))
	}
	for i, v := range r1.Vectors[0] {
		if v != r2.Vectors[0][i] {
			t.Errorf("not deterministic at index %d: %v vs %v", i, v, r2.Vectors[0][i])
		}
	}
}

func TestMock_EmbedReturnsUnitVector(t *testing.T) {
	t.Parallel()

	gw := newMock(t, 16)
	resp, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"test input"}})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	vec := resp.Vectors[0]
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	const tolerance = 1e-5
	if math.Abs(norm-1.0) > tolerance {
		t.Errorf("expected unit vector (norm≈1), got norm=%v", norm)
	}
}

func TestMock_DifferentInputsDifferentVectors(t *testing.T) {
	t.Parallel()

	gw := newMock(t, 8)
	r1, _ := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"hello"}})
	r2, _ := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: []string{"world"}})

	same := true
	for i, v := range r1.Vectors[0] {
		if v != r2.Vectors[0][i] {
			same = false
			break
		}
	}
	if same {
		t.Error("expected different inputs to produce different vectors")
	}
}

func TestMock_BatchEmbedMultipleInputs(t *testing.T) {
	t.Parallel()

	const dims = 4
	gw := newMock(t, dims)
	inputs := []string{"a", "b", "c"}
	resp, err := gw.Embed(context.Background(), gateway.EmbedRequest{Inputs: inputs})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(resp.Vectors) != len(inputs) {
		t.Fatalf("want %d vectors, got %d", len(inputs), len(resp.Vectors))
	}
	for i, vec := range resp.Vectors {
		if len(vec) != dims {
			t.Errorf("vector %d: want %d dims, got %d", i, dims, len(vec))
		}
	}
}

// ── Complete ──────────────────────────────────────────────────────────────────

func TestMock_CompleteDefaultsToEmptyObject(t *testing.T) {
	t.Parallel()

	gw := newMock(t, 4)
	schema := json.RawMessage(`{}`)
	resp, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		Schema:   schema,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if string(resp.JSON) != `{}` {
		t.Errorf("want '{}', got %s", resp.JSON)
	}
}

func TestMock_CompleteRequiresSchema(t *testing.T) {
	t.Parallel()

	gw := newMock(t, 4)
	_, err := gw.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "hi"}},
		// Schema intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error when schema is empty")
	}
}

// ── Probe ─────────────────────────────────────────────────────────────────────

func TestMock_ProbeAlwaysSucceeds(t *testing.T) {
	t.Parallel()

	gw := newMock(t, 4)
	if err := gw.Probe(context.Background()); err != nil {
		t.Errorf("probe: %v", err)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestMock_ConcurrentEmbedRaceSafe(t *testing.T) {
	t.Parallel()

	gw := newMock(t, 8)
	done := make(chan error, 20)
	for i := range 20 {
		go func(n int) {
			_, err := gw.Embed(context.Background(), gateway.EmbedRequest{
				Inputs: []string{fmt.Sprintf("concurrent-%d", n)},
			})
			done <- err
		}(i)
	}
	for range 20 {
		if err := <-done; err != nil {
			t.Errorf("concurrent embed: %v", err)
		}
	}
}
