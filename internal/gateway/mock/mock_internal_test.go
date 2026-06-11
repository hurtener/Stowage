package mock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/prometheus/client_golang/prometheus"
)

func newDriver(t *testing.T, dims int) *Driver {
	t.Helper()
	cfg := config.GatewayConfig{
		Driver:     "mock",
		Model:      "m",
		EmbedModel: "e",
		EmbedDims:  dims,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Driver{cfg: cfg, log: log, meter: gateway.NewPromMeter(log, prometheus.NewRegistry())}
	t.Cleanup(func() { d.Close(context.Background()) }) //nolint:errcheck
	return d
}

func TestPushScript_ConsumesInOrder(t *testing.T) {
	t.Parallel()

	d := newDriver(t, 4)
	d.PushScript(Script{JSON: json.RawMessage(`{"n":1}`)})
	d.PushScript(Script{JSON: json.RawMessage(`{"n":2}`)})

	schema := json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`)
	r1, err := d.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "q"}},
		Schema:   schema,
	})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r2, err := d.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "q"}},
		Schema:   schema,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if string(r1.JSON) != `{"n":1}` {
		t.Errorf("first script: want {\"n\":1}, got %s", r1.JSON)
	}
	if string(r2.JSON) != `{"n":2}` {
		t.Errorf("second script: want {\"n\":2}, got %s", r2.JSON)
	}
}

func TestPushScript_ErrorPropagates(t *testing.T) {
	t.Parallel()

	d := newDriver(t, 4)
	sentinel := errors.New("scripted error")
	d.PushScript(Script{Err: sentinel})

	schema := json.RawMessage(`{}`)
	_, err := d.Complete(context.Background(), gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "q"}},
		Schema:   schema,
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected scripted error, got %v", err)
	}
}

func TestPushScript_FallsBackToDefaultAfterScriptExhausted(t *testing.T) {
	t.Parallel()

	d := newDriver(t, 4)
	d.PushScript(Script{JSON: json.RawMessage(`{"scripted":true}`)})

	schema := json.RawMessage(`{}`)
	req := gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "q"}},
		Schema:   schema,
	}

	r1, err := d.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if string(r1.JSON) != `{"scripted":true}` {
		t.Errorf("first: want scripted response, got %s", r1.JSON)
	}

	r2, err := d.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if string(r2.JSON) != `{}` {
		t.Errorf("second: want '{}' default, got %s", r2.JSON)
	}
}
