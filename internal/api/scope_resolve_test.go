package api_test

// scope_resolve_test.go — HTTP-level coverage for the ae8 resolveScope /
// respondScopeError seam (internal/api/auth.go) that IS reachable over a
// real request: strict read posture (retrieval.read_posture=strict, D-137
// knob 2) refusing an identity-less read with 403 (ErrIdentityRequired)
// before any store call. See auth_test.go (internal package) for the
// ModeJWT-only sentinels (ErrTenantMismatch/ErrUserConflict) that cannot be
// driven through this package's ModeKeyring HTTP harness.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/store"
)

// newStrictPostureTestServer creates a test server with
// retrieval.read_posture=strict (ae8, D-137/D-148), otherwise identical to
// newTestServer.
func newStrictPostureTestServer(t *testing.T) (*api.Server, *httptest.Server, store.Store) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stowage-strict-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	_ = f.Close()

	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = f.Name()
	cfg.Retrieval.ReadPosture = "strict"

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	reg := prometheus.NewRegistry()

	srv, err := api.New(cfg, st, log, reg)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx2)
	})
	return srv, ts, st
}

// TestScope_StrictPostureNoIdentity proves that, in strict read posture, a
// GET /v1/memories call with no user_id/project_id args and a bare keyring
// key (no verified user) is refused with 403 (ErrIdentityRequired) before
// any store call — the respondScopeError "identity required" branch, driven
// through a real request.
func TestScope_StrictPostureNoIdentity(t *testing.T) {
	t.Parallel()
	_, ts, st := newStrictPostureTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-strict-noid")

	req, _ := http.NewRequest("GET", ts.URL+"/v1/memories", nil)
	req.Header.Set("Authorization", bearerHeader(pt))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/memories: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("strict posture, no identity: got %d want 403 (ErrIdentityRequired)", resp.StatusCode)
	}
}

// TestScope_StrictPostureWithUser proves the same strict-posture server
// still serves normally once a user_id is supplied (the refusal is
// identity-presence-gated, not a blanket strict-mode failure).
func TestScope_StrictPostureWithUser(t *testing.T) {
	t.Parallel()
	_, ts, st := newStrictPostureTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-strict-withid")

	req, _ := http.NewRequest("GET", ts.URL+"/v1/memories?user_id=u1", nil)
	req.Header.Set("Authorization", bearerHeader(pt))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/memories?user_id=u1: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("strict posture, with user_id: got %d want 200", resp.StatusCode)
	}
}
