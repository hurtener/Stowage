package api_test

// store_fault_scenarios_test.go — HTTP-level tests that drive a handler's
// store-error (500) branch via the fault-injecting store.Store decorators in
// store_fault_test.go. Authentication stays healthy (Keys().Lookup always
// delegates to the real keyring); only the one sub-store call under test is
// made to fail.

import (
	"context"
	"fmt"
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
	"github.com/hurtener/stowage/internal/views"
)

// newFaultyTestServer creates a test server backed by a real temp-file
// sqlite store wrapped in a faultyStore, so callers can flip individual
// store faults on before making a request. Returns the server, the test
// HTTP server, and the underlying REAL store (for direct key seeding).
func newFaultyTestServer(t *testing.T, fault *faultyStore) (*api.Server, *httptest.Server) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stowage-fault-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
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

	fault.Store = real

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	reg := prometheus.NewRegistry()
	srv, err := api.New(cfg, fault, log, reg)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc := views.New(fault.TopicViews(), fault.Events(), log)
	srv.SetViewsService(svc)

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx2)
	})
	return srv, ts
}

// TestAdminKeys_ListStoreError covers handleListKeys' store-error (500) branch.
func TestAdminKeys_ListStoreError(t *testing.T) {
	t.Parallel()
	fault := &faultyStore{failKeysList: true}
	_, ts := newFaultyTestServer(t, fault)
	_, adminPT := mustCreateAdminKey(t, fault, "tenant-lke")

	req, _ := http.NewRequest("GET", ts.URL+"/v1/admin/keys", nil)
	req.Header.Set("Authorization", bearerHeader(adminPT))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/admin/keys: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("list keys, store fault: got %d want 500", resp.StatusCode)
	}
}

// TestAdminKeys_RevokeStoreError covers handleRevokeKey's store-error (500)
// branch (a non-ErrKeyNotFound failure from Keys().Revoke).
func TestAdminKeys_RevokeStoreError(t *testing.T) {
	t.Parallel()
	fault := &faultyStore{failKeysRevoke: true}
	_, ts := newFaultyTestServer(t, fault)
	adminKey, adminPT := mustCreateAdminKey(t, fault, "tenant-rke")

	req, _ := http.NewRequest("POST",
		fmt.Sprintf("%s/v1/admin/keys/%s/revoke", ts.URL, adminKey.ID), nil)
	req.Header.Set("Authorization", bearerHeader(adminPT))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("revoke key, store fault: got %d want 500", resp.StatusCode)
	}
}

// TestAdminKeys_RevokeTenantStoreError covers handleRevokeTenantKeys' list
// store-error (500) branch.
func TestAdminKeys_RevokeTenantStoreError(t *testing.T) {
	t.Parallel()
	fault := &faultyStore{failKeysList: true}
	_, ts := newFaultyTestServer(t, fault)
	_, adminPT := mustCreateAdminKey(t, fault, "tenant-rtke")

	resp := doJSON(t, "POST", ts.URL+"/v1/admin/keys/revoke-tenant", adminPT,
		map[string]interface{}{"tenant_id": "tenant-rtke"})
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("revoke-tenant, store fault: got %d want 500", resp.StatusCode)
	}
}

// TestDSAR_StoreError covers handleDSAR's store-error (500) branch.
func TestDSAR_StoreError(t *testing.T) {
	t.Parallel()
	fault := &faultyStore{failDeleteUserData: true}
	_, ts := newFaultyTestServer(t, fault)
	_, adminPT := mustCreateAdminKey(t, fault, "tenant-dsare")

	req, _ := http.NewRequest("DELETE", ts.URL+"/v1/admin/users/some-user", nil)
	req.Header.Set("Authorization", bearerHeader(adminPT))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE dsar: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("dsar, store fault: got %d want 500", resp.StatusCode)
	}
}

// TestViews_ListStoreError covers handleListViews' store-error branch, which
// exercises respondViewError's default (500, unrecognized error) case — no
// legitimate validation/conflict/not-found error reaches it.
func TestViews_ListStoreError(t *testing.T) {
	t.Parallel()
	fault := &faultyStore{failListViews: true}
	_, ts := newFaultyTestServer(t, fault)
	_, pt := mustCreateAgentKey(t, fault, "tenant-vlse")

	resp := doJSON(t, "GET", ts.URL+"/v1/scopes/views", pt, nil)
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("list views, store fault: got %d want 500", resp.StatusCode)
	}
}
