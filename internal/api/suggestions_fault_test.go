package api_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// faultScopeSettings makes Get fail, exercising the governance-resolution 500
// branch in the suggestions + proactive-config handlers.
type faultScopeSettings struct{ store.ScopeSettingsStore }

func (faultScopeSettings) Get(context.Context, identity.Scope, string) (string, bool, error) {
	return "", false, errors.New("injected scope-settings failure")
}

// faultStore embeds a real store but swaps in a failing ScopeSettings sub-store.
type faultStore struct{ store.Store }

func (f faultStore) ScopeSettings() store.ScopeSettingsStore {
	return faultScopeSettings{f.Store.ScopeSettings()}
}

// newFaultServer builds an API server whose ScopeSettings().Get always errors.
func newFaultServer(t *testing.T) (*httptest.Server, store.Store) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stowage-fault-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	_ = f.Close()
	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = f.Name()
	ctx := context.Background()
	real, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := real.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = real.Close(context.Background()) })

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv, err := api.New(cfg, faultStore{real}, log, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, real
}

func TestSuggestions_GovernanceResolveFails(t *testing.T) {
	t.Parallel()
	ts, st := newFaultServer(t)
	tenant := "tenant-fault"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	r, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/suggestions?session_id=s1", nil, agentKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("governance resolve failure want 500, got %d", r.StatusCode)
	}
}

func TestProactiveConfig_GetResolveFails(t *testing.T) {
	t.Parallel()
	ts, st := newFaultServer(t)
	tenant := "tenant-fault-gov"
	_, adminKey := mustCreateAdminKey(t, st, tenant)

	r, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/admin/proactive", nil, adminKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("governance resolve failure want 500, got %d", r.StatusCode)
	}
}
