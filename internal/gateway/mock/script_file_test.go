package mock_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/mock"
)

// TestLazyScriptFile proves the STOWAGE_MOCK_SCRIPT lazy-file mode: entries
// are re-read per call (post-boot writes visible) and consumed FIFO by a
// persistent offset; exhaustion falls back to {}.
func TestLazyScriptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.json")
	t.Setenv("STOWAGE_MOCK_SCRIPT", path)

	gw, err := gateway.Open(context.Background(),
		config.GatewayConfig{Driver: "mock", EmbedDims: 4}, discardLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer gw.Close(context.Background()) //nolint:errcheck

	schema := json.RawMessage(`{"type":"object"}`)
	req := gateway.CompleteRequest{
		Messages: []gateway.Message{{Role: "user", Content: "x"}},
		Schema:   schema,
	}

	// File absent at first call → {} fallback.
	r1, err := gw.Complete(context.Background(), req)
	if err != nil || string(r1.JSON) != "{}" {
		t.Fatalf("absent file: got %s err %v, want {}", r1.JSON, err)
	}

	// Write two entries AFTER boot; consumed FIFO across re-reads.
	if err := os.WriteFile(path, []byte(`[{"a":1},{"b":2}]`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r2, _ := gw.Complete(context.Background(), req)
	if string(r2.JSON) != `{"a":1}` {
		t.Fatalf("entry0: got %s", r2.JSON)
	}
	r3, _ := gw.Complete(context.Background(), req)
	if string(r3.JSON) != `{"b":2}` {
		t.Fatalf("entry1 after re-read: got %s", r3.JSON)
	}

	// Exhausted → {} again; malformed file also degrades to {}.
	r4, _ := gw.Complete(context.Background(), req)
	if string(r4.JSON) != "{}" {
		t.Fatalf("exhausted: got %s", r4.JSON)
	}
	if err := os.WriteFile(path, []byte(`not json`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r5, _ := gw.Complete(context.Background(), req)
	if string(r5.JSON) != "{}" {
		t.Fatalf("malformed: got %s", r5.JSON)
	}
}
