// Package boot_test exercises the boot.Open assembly sequence.
// Tests cover every major branch: nil config, bad drivers, degraded probe,
// happy-path with all-non-nil fields, and Close idempotency.
package boot_test

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"

	// Register drivers needed by the happy-path and partial-open tests.
	_ "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
	_ "github.com/hurtener/stowage/internal/vindex/hnsw"
)

// probeFailGateway is a gateway.Gateway whose Probe always returns an error,
// used to exercise the degraded-mode boot path (D-036).
type probeFailGateway struct{}

func (g *probeFailGateway) Embed(_ context.Context, _ gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (g *probeFailGateway) Complete(_ context.Context, _ gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	return gateway.CompleteResponse{}, nil
}
func (g *probeFailGateway) Probe(_ context.Context) error {
	return errors.New("probe: deliberate test failure")
}
func (g *probeFailGateway) Rerank(_ context.Context, _ gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}
func (g *probeFailGateway) Close(_ context.Context) error { return nil }

func init() {
	gateway.Register("probe-fail-test",
		func(_ context.Context, _ config.GatewayConfig, _ *slog.Logger, _ *prometheus.Registry) (gateway.Gateway, error) {
			return &probeFailGateway{}, nil
		})
}

// validCfg returns a minimal valid *config.Config backed by a temp SQLite DB.
func validCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = filepath.Join(t.TempDir(), "boot_test.db")
	cfg.Gateway.Driver = "mock"
	cfg.VIndex.Driver = "hnsw"
	return cfg
}

// TestBoot_Open_NilConfig verifies that Open returns an error for a nil config.
func TestBoot_Open_NilConfig(t *testing.T) {
	t.Parallel()
	_, err := boot.Open(context.Background(), nil)
	if err == nil {
		t.Fatal("Open(nil): expected error, got nil")
	}
}

// TestBoot_Open_BadTelemetry verifies that Open returns an error when the
// telemetry log level is invalid (covers boot: telemetry error path).
func TestBoot_Open_BadTelemetry(t *testing.T) {
	t.Parallel()
	cfg := validCfg(t)
	cfg.Telemetry.LogLevel = "not-a-valid-level"
	_, err := boot.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("Open with invalid telemetry: expected error, got nil")
	}
}

// TestBoot_Open_BadStoreDriver verifies that Open returns an error when the
// store driver is not registered (covers boot: store error path).
func TestBoot_Open_BadStoreDriver(t *testing.T) {
	t.Parallel()
	cfg := validCfg(t)
	cfg.Store.Driver = "no-such-store-driver"
	_, err := boot.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("Open with unknown store driver: expected error, got nil")
	}
}

// TestBoot_Open_BadGatewayDriver verifies that Open returns an error when the
// gateway driver is not registered. At this point the store is open and in the
// closers slice, so the partial-open cleanup path (s.close) is exercised.
func TestBoot_Open_BadGatewayDriver(t *testing.T) {
	t.Parallel()
	cfg := validCfg(t)
	cfg.Gateway.Driver = "no-such-gateway-driver"
	_, err := boot.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("Open with unknown gateway driver: expected error, got nil")
	}
}

// TestBoot_Open_BadVIndexDriver verifies that Open returns an error when the
// vindex driver is not registered. At this point both the store and gateway are
// open, so the partial-open cleanup path exercises two closers.
func TestBoot_Open_BadVIndexDriver(t *testing.T) {
	t.Parallel()
	cfg := validCfg(t)
	cfg.VIndex.Driver = "no-such-vindex-driver"
	_, err := boot.Open(context.Background(), cfg)
	if err == nil {
		t.Fatal("Open with unknown vindex driver: expected error, got nil")
	}
}

// TestBoot_Open_GatewayProbeDegraded verifies that a Probe failure does NOT
// cause Open to return an error — the stack boots in degraded mode (D-036).
func TestBoot_Open_GatewayProbeDegraded(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := validCfg(t)
	cfg.Gateway.Driver = "probe-fail-test"

	stk, err := boot.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open with probe-fail gateway: expected success (degraded mode), got: %v", err)
	}
	if stk == nil {
		t.Fatal("Open returned nil stack in degraded mode")
	}

	shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := stk.Close(shutCtx); err != nil {
		t.Errorf("Close after degraded boot: %v", err)
	}
}

// TestBoot_Open_Success verifies that a successful Open produces a Stack where
// every field is non-nil and Close releases all resources cleanly.
func TestBoot_Open_Success(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stk, err := boot.Open(ctx, validCfg(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Every public field must be non-nil after a successful open.
	if stk.Log == nil {
		t.Error("Stack.Log is nil")
	}
	if stk.Metrics == nil {
		t.Error("Stack.Metrics is nil")
	}
	if stk.Store == nil {
		t.Error("Stack.Store is nil")
	}
	if stk.Gateway == nil {
		t.Error("Stack.Gateway is nil")
	}
	if stk.VIndex == nil {
		t.Error("Stack.VIndex is nil")
	}
	if stk.Embedder == nil {
		t.Error("Stack.Embedder is nil")
	}
	if stk.Retriever == nil {
		t.Error("Stack.Retriever is nil")
	}
	if stk.TopicSvc == nil {
		t.Error("Stack.TopicSvc is nil")
	}
	if stk.GrantsSvc == nil {
		t.Error("Stack.GrantsSvc is nil")
	}

	shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := stk.Close(shutCtx); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestBoot_Close_Idempotent verifies that calling Close twice does not panic
// or return an error on the second call (closers slice is cleared after first).
func TestBoot_Close_Idempotent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stk, err := boot.Open(ctx, validCfg(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	bg := context.Background()
	if err := stk.Close(bg); err != nil {
		t.Errorf("first Close: %v", err)
	}
	// Second close: closers slice is nil, loop body never runs — must be safe.
	if err := stk.Close(bg); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestBoot_Open_TraceSigner verifies the Phase-26 trace-signing-key resolution: a
// valid env-ref'd ed25519 seed yields a non-nil TraceSigner; a malformed seed fails
// boot loud (D-086).
func TestBoot_Open_TraceSigner(t *testing.T) {
	// Valid 32-byte seed (base64) → signer wired.
	seed := make([]byte, 32)
	seed[0] = 7
	t.Setenv("STOWAGE_TEST_TRACE_SEED", base64.StdEncoding.EncodeToString(seed))
	cfg := validCfg(t)
	cfg.Trace.SigningKey = "env.STOWAGE_TEST_TRACE_SEED"
	stk, err := boot.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open with valid signing key: %v", err)
	}
	defer func() { _ = stk.Close(context.Background()) }()
	if stk.TraceSigner == nil {
		t.Error("expected a non-nil TraceSigner when trace.signing_key is set")
	}

	// Malformed seed → boot fails loud.
	t.Setenv("STOWAGE_TEST_BAD_SEED", "not-base64!!")
	bad := validCfg(t)
	bad.Trace.SigningKey = "env.STOWAGE_TEST_BAD_SEED"
	if _, err := boot.Open(context.Background(), bad); err == nil {
		t.Error("expected boot to fail on a malformed trace signing key")
	}
}
