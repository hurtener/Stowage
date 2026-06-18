package mcpserver_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
)

// nullStore implements a no-op store that satisfies the Services.Store interface.
// We can't use a real store in unit tests without a DB, so the server just
// registers tools; handler tests use the store in integration tests.
// For tool-registration and scope tests we only need a valid Services struct
// — the store is never called.

func newTestServices(t *testing.T) *mcpserver.Services {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return &mcpserver.Services{
		Store:      nil, // nil store: tools register fine; handlers fail at call-time
		Retriever:  nil,
		TopicSvc:   nil,
		PipelineIn: nil,
		Log:        log,
		ScopeFn:    mcpserver.StdioScopeFn("test-tenant"),
	}
}

func TestNew_SevenToolsRegistered(t *testing.T) {
	svc := newTestServices(t)
	srv, err := mcpserver.New(server.Info{
		Name:    "stowage-mcp-test",
		Version: "0.0.1",
	}, svc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	tools := srv.Tools()
	if len(tools) != 15 {
		t.Fatalf("expected 15 tools registered, got %d: %v", len(tools), tools)
	}

	want := map[string]bool{
		"memory_ingest":    true,
		"memory_retrieve":  true,
		"memory_playbook":  true,
		"memory_episodes":  true,
		"memory_causal":    true,
		"memory_drilldown": true,
		"memory_feedback":  true,
		"memory_assert":    true,
		"memory_topics":    true,
		"memory_get":       true,
		"memory_rollback":  true,
		"memory_resolve":   true,
		"memory_flush":     true,
		"memory_branch":    true,
		"memory_grants":    true,
	}
	for _, name := range tools {
		if !want[name] {
			t.Errorf("unexpected tool registered: %q", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("tool not registered: %q", name)
	}
}

func TestNew_ConcurrentCreation(t *testing.T) {
	// AC-7: concurrent server creation must be race-free.
	const goroutines = 3
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	srvs := make([]*server.Server, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			svc := newTestServices(t)
			srv, err := mcpserver.New(server.Info{
				Name:    "stowage-mcp-test",
				Version: "0.0.1",
			}, svc)
			errs[idx] = err
			srvs[idx] = srv
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: New failed: %v", i, err)
		}
	}
	for i, srv := range srvs {
		if srv == nil {
			t.Errorf("goroutine %d: nil server", i)
			continue
		}
		if n := len(srv.Tools()); n != 15 {
			t.Errorf("goroutine %d: expected 15 tools, got %d", i, n)
		}
	}
}

func TestStdioScopeFn(t *testing.T) {
	fn := mcpserver.StdioScopeFn("acme")
	scope, err := fn(context.Background())
	if err != nil {
		t.Fatalf("StdioScopeFn: %v", err)
	}
	if scope.Tenant != "acme" {
		t.Errorf("expected tenant=acme, got %q", scope.Tenant)
	}
	if scope.Project != "" || scope.User != "" || scope.Session != "" {
		t.Errorf("expected tenant-only scope, got %+v", scope)
	}
}

func TestStdioScopeFn_DefaultTenant(t *testing.T) {
	fn := mcpserver.StdioScopeFn("default")
	scope, err := fn(context.Background())
	if err != nil {
		t.Fatalf("StdioScopeFn default: %v", err)
	}
	if scope.Tenant != "default" {
		t.Errorf("expected tenant=default, got %q", scope.Tenant)
	}
}

func TestKeyringMiddleware_ValidKey(t *testing.T) {
	kr := auth.NewMemKeyring()
	key, plaintext, err := auth.Generate("tenant-mcp", auth.RoleAgent)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := kr.Insert(key); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var gotTenant string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, _ := identity.FromContext(r.Context())
		gotTenant = sc.Tenant
		w.WriteHeader(200)
	})
	handler := mcpserver.KeyringMiddleware(kr, inner)
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || gotTenant != "tenant-mcp" {
		t.Fatalf("want 200 + key tenant, got %d %q", rec.Code, gotTenant)
	}
	// Revoked key → 403 (runtime rotation, D-030).
	if err := kr.Revoke(key.ID, time.Now()); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	if rec2.Code != 403 {
		t.Fatalf("revoked key: want 403, got %d", rec2.Code)
	}
}

// Ensure identity package is reachable from this test package.
var _ = identity.Scope{}
