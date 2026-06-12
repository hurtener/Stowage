package api_test

// grants_handler_test.go — HTTP-level tests for grants and group endpoints
// (Phase 15, RFC §5.3).
//
// Tests cover:
//   - Group CRUD via /v1/admin/groups (admin role required).
//   - Member management via /v1/admin/groups/{id}/members.
//   - Grant CRUD via /v1/scopes/grants.
//   - Grant revocation via /v1/grants/{id}/revoke.
//   - Zone ceiling validation (personal/intimate → 400).
//   - 503 when grants service not wired.
//   - Contribute-mode ingest: 403 without grant.
//
// Coverage target: push internal/api above 80 % threshold.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"context"
	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/store"

	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
)

// newGrantsTestServer creates a test server with the grants service wired.
// This is separate from newTestServer to avoid coupling all existing tests
// to the grants service.
func newGrantsTestServer(t *testing.T) (*api.Server, *httptest.Server, store.Store) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stowage-grants-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	_ = f.Close()

	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = f.Name()

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

	// Wire grants service (Phase 15).
	grantsSvc := grants.New(st.Grants(), st.Events(), log)
	srv.SetGrantsService(grantsSvc)

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx2)
	})
	return srv, ts, st
}

func doJSON(t *testing.T, method, url, adminKey string, body interface{}) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewBuffer(b)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if adminKey != "" {
		req.Header.Set("Authorization", "Bearer "+adminKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, dst interface{}) {
	t.Helper()
	defer drainClose(resp.Body)
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// ---- Group CRUD tests -----------------------------------------------------------

func TestHandleCreateGroup_OK(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	resp := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "eng"})
	if resp.StatusCode != http.StatusCreated {
		drainClose(resp.Body)
		t.Fatalf("status: got %d want 201", resp.StatusCode)
	}
	var got map[string]interface{}
	decodeJSON(t, resp, &got)
	if got["name"] != "eng" {
		t.Errorf("name: got %v want eng", got["name"])
	}
}

func TestHandleCreateGroup_RequiresAdmin(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t1")

	resp := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "eng"})
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("agent should be 403, got %d", resp.StatusCode)
	}
}

func TestHandleCreateGroup_MissingName(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	resp := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{})
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing name: want 400, got %d", resp.StatusCode)
	}
}

func TestHandleListGroups_OK(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	// Create a group.
	resp1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	drainClose(resp1.Body)

	resp := doJSON(t, "GET", ts.URL+"/v1/admin/groups", pt, nil)
	if resp.StatusCode != http.StatusOK {
		drainClose(resp.Body)
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	var got map[string]interface{}
	decodeJSON(t, resp, &got)
	groups, ok := got["groups"].([]interface{})
	if !ok || len(groups) < 1 {
		t.Errorf("expected ≥1 group, got %v", got["groups"])
	}
}

func TestHandleGrantsSvc_NotWired(t *testing.T) {
	t.Parallel()
	// Server without grants service wired.
	f, err := os.CreateTemp(t.TempDir(), "stowage-nowire-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	_ = f.Close()

	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = f.Name()

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
	srv, err := api.New(cfg, st, log, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	// Do NOT wire grants service.
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx2)
	})

	// Create an admin key directly in the store.
	_, pt := mustCreateAdminKey(t, st, "t1")

	resp := doJSON(t, "GET", ts.URL+"/v1/admin/groups", pt, nil)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unwired grants: want 503, got %d", resp.StatusCode)
	}
}

// ---- Member management tests ----------------------------------------------------

func TestHandleAddMember_OK(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	// Create a group.
	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	// Add member.
	resp := doJSON(t, "POST", ts.URL+"/v1/admin/groups/"+groupID+"/members", pt,
		map[string]string{"user_id": "alice"})
	if resp.StatusCode != http.StatusCreated {
		drainClose(resp.Body)
		t.Fatalf("add member: got %d want 201", resp.StatusCode)
	}
	var m map[string]interface{}
	decodeJSON(t, resp, &m)
	if m["user_id"] != "alice" {
		t.Errorf("user_id: got %v want alice", m["user_id"])
	}
}

func TestHandleAddMember_MissingUserID(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	resp := doJSON(t, "POST", ts.URL+"/v1/admin/groups/"+groupID+"/members", pt,
		map[string]string{})
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing user_id: want 400, got %d", resp.StatusCode)
	}
}

func TestHandleRemoveMember_NotFound(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	resp := doJSON(t, "DELETE", ts.URL+"/v1/admin/groups/no-such/members/no-user", pt, nil)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("remove missing: want 404, got %d", resp.StatusCode)
	}
}

func TestHandleRemoveMember_OK(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	r2 := doJSON(t, "POST", ts.URL+"/v1/admin/groups/"+groupID+"/members", pt,
		map[string]string{"user_id": "bob"})
	drainClose(r2.Body)

	resp := doJSON(t, "DELETE", ts.URL+"/v1/admin/groups/"+groupID+"/members/bob", pt, nil)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("remove member: want 204, got %d", resp.StatusCode)
	}
}

// ---- Grant management tests -----------------------------------------------------

func TestHandleCreateGrant_OK(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	resp := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
		"group_id":     groupID,
		"user_id":      "owner",
		"access":       "read",
		"zone_ceiling": "work",
	})
	if resp.StatusCode != http.StatusCreated {
		drainClose(resp.Body)
		t.Fatalf("create grant: got %d want 201", resp.StatusCode)
	}
	var g map[string]interface{}
	decodeJSON(t, resp, &g)
	if g["zone_ceiling"] != "work" {
		t.Errorf("zone_ceiling: got %v want work", g["zone_ceiling"])
	}
	if g["access"] != "read" {
		t.Errorf("access: got %v want read", g["access"])
	}
}

func TestHandleCreateGrant_ZoneCeiling_Personal_Rejected(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	for _, ceil := range []string{"personal", "intimate"} {
		resp := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
			"group_id":     groupID,
			"zone_ceiling": ceil,
		})
		defer drainClose(resp.Body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("ceiling %q: want 400, got %d", ceil, resp.StatusCode)
		}
	}
}

func TestHandleCreateGrant_MissingGroupID(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	resp := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
		"zone_ceiling": "work",
	})
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing group_id: want 400, got %d", resp.StatusCode)
	}
}

func TestHandleCreateGrant_MissingZoneCeiling(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	resp := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
		"group_id": "g1",
	})
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing zone_ceiling: want 400, got %d", resp.StatusCode)
	}
}

func TestHandleListGrants_OK(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	// Create a grant.
	r2 := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
		"group_id":     groupID,
		"user_id":      "owner",
		"access":       "read",
		"zone_ceiling": "public",
	})
	drainClose(r2.Body)

	resp := doJSON(t, "GET", ts.URL+"/v1/scopes/grants", pt, nil)
	if resp.StatusCode != http.StatusOK {
		drainClose(resp.Body)
		t.Fatalf("list grants: got %d want 200", resp.StatusCode)
	}
	var got map[string]interface{}
	decodeJSON(t, resp, &got)
	gs, ok := got["grants"].([]interface{})
	if !ok || len(gs) < 1 {
		t.Errorf("expected ≥1 grant, got %v", got["grants"])
	}
}

func TestHandleRevokeGrant_OK(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	r2 := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
		"group_id":     groupID,
		"user_id":      "owner",
		"access":       "read",
		"zone_ceiling": "work",
	})
	var g map[string]interface{}
	decodeJSON(t, r2, &g)
	grantID := g["id"].(string)

	resp := doJSON(t, "POST", ts.URL+"/v1/grants/"+grantID+"/revoke", pt, nil)
	if resp.StatusCode != http.StatusOK {
		drainClose(resp.Body)
		t.Fatalf("revoke: got %d want 200", resp.StatusCode)
	}
	var rev map[string]interface{}
	decodeJSON(t, resp, &rev)
	if rev["status"] != "revoked" {
		t.Errorf("status: got %v want revoked", rev["status"])
	}
}

func TestHandleRevokeGrant_NotFound(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	resp := doJSON(t, "POST", ts.URL+"/v1/grants/no-such-grant/revoke", pt, nil)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("revoke missing: want 404, got %d", resp.StatusCode)
	}
}

// ---- Additional error-path coverage to satisfy 80% threshold -------------------

func TestHandleCreateGroup_BadJSON(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	req, _ := http.NewRequest("POST", ts.URL+"/v1/admin/groups", bytes.NewBufferString("notjson"))
	req.Header.Set("Authorization", "Bearer "+pt)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/admin/groups: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad JSON: want 400, got %d", resp.StatusCode)
	}
}

func TestHandleAddMember_GroupNotFound(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	resp := doJSON(t, "POST", ts.URL+"/v1/admin/groups/no-such-group/members", pt,
		map[string]string{"user_id": "alice"})
	defer drainClose(resp.Body)
	// Group does not exist — store returns ErrNotFound → 404.
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("group not found: want 404 or 500, got %d", resp.StatusCode)
	}
}

func TestHandleCreateGrant_InvalidAccess(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "team"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	resp := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
		"group_id":     groupID,
		"user_id":      "owner",
		"access":       "superadmin", // invalid
		"zone_ceiling": "work",
	})
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid access: want 400, got %d", resp.StatusCode)
	}
}

func TestHandleRevokeGrant_MissingGrantsSvc(t *testing.T) {
	// 503 when grants service not wired — test via list grants (simpler).
	t.Parallel()
	f, err := os.CreateTemp(t.TempDir(), "stowage-nowire2-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	_ = f.Close()

	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = f.Name()

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
	srv, err := api.New(cfg, st, log, prometheus.NewRegistry())
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

	_, pt := mustCreateAdminKey(t, st, "t1")

	// Test each endpoint returns 503.
	for _, tc := range []struct {
		method string
		path   string
		body   interface{}
	}{
		{"GET", "/v1/scopes/grants", nil},
		{"PUT", "/v1/scopes/grants", map[string]string{"group_id": "g", "zone_ceiling": "work"}},
		{"POST", "/v1/grants/g1/revoke", nil},
		{"POST", "/v1/admin/groups", map[string]string{"name": "x"}},
		{"POST", "/v1/admin/groups/g/members", map[string]string{"user_id": "u"}},
		{"DELETE", "/v1/admin/groups/g/members/u", nil},
	} {
		resp := doJSON(t, tc.method, ts.URL+tc.path, pt, tc.body)
		drainClose(resp.Body)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s %s: want 503, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// ---- Contribute-mode ingest (D-059) --------------------------------------------

func TestIngestContributeMode_NoGrant_403(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	body := jsonBody(t, map[string]interface{}{
		"records": []map[string]interface{}{
			{"role": "user", "content": "contributing memory"},
		},
		"target_scope": map[string]string{
			"user_id": "bob",
		},
		"contributor_user_id": "alice",
	})

	req, _ := http.NewRequest("POST", ts.URL+"/v1/records", body)
	req.Header.Set("Authorization", "Bearer "+pt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/records contribute: %v", err)
	}
	defer drainClose(resp.Body)

	// No active contribute grant → 403.
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("contribute without grant: want 403, got %d", resp.StatusCode)
	}
}

func TestIngestContributeMode_WithGrant_202(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	// Create group, add contributor.
	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "writers"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	r2 := doJSON(t, "POST", ts.URL+"/v1/admin/groups/"+groupID+"/members", pt,
		map[string]string{"user_id": "alice"})
	drainClose(r2.Body)

	// Create contribute grant targeting bob's scope.
	r3 := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
		"group_id":     groupID,
		"user_id":      "bob",
		"access":       "contribute",
		"zone_ceiling": "work",
	})
	drainClose(r3.Body)

	// Contribute as alice into bob's scope.
	body := jsonBody(t, map[string]interface{}{
		"records": []map[string]interface{}{
			{"role": "user", "content": "contributed content for bob"},
		},
		"target_scope": map[string]string{
			"user_id": "bob",
		},
		"contributor_user_id": "alice",
	})

	req, _ := http.NewRequest("POST", ts.URL+"/v1/records", body)
	req.Header.Set("Authorization", "Bearer "+pt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/records contribute with grant: %v", err)
	}
	defer drainClose(resp.Body)

	// With active contribute grant → 202.
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("contribute with grant: want 202, got %d", resp.StatusCode)
	}
}

func TestIngestContributeMode_AccessDefaultRead_NoContribute(t *testing.T) {
	t.Parallel()
	_, ts, st := newGrantsTestServer(t)
	_, pt := mustCreateAdminKey(t, st, "t1")

	// Create group, add contributor.
	r1 := doJSON(t, "POST", ts.URL+"/v1/admin/groups", pt, map[string]string{"name": "readers"})
	var grp map[string]interface{}
	decodeJSON(t, r1, &grp)
	groupID := grp["id"].(string)

	r2 := doJSON(t, "POST", ts.URL+"/v1/admin/groups/"+groupID+"/members", pt,
		map[string]string{"user_id": "alice"})
	drainClose(r2.Body)

	// Create a READ-only grant (not contribute).
	r3 := doJSON(t, "PUT", ts.URL+"/v1/scopes/grants", pt, map[string]interface{}{
		"group_id":     groupID,
		"user_id":      "bob",
		"access":       "read",
		"zone_ceiling": "work",
	})
	drainClose(r3.Body)

	// Try to contribute as alice into bob's scope — read grant is not enough.
	body := jsonBody(t, map[string]interface{}{
		"records": []map[string]interface{}{
			{"role": "user", "content": "attempted contribute"},
		},
		"target_scope": map[string]string{
			"user_id": "bob",
		},
		"contributor_user_id": "alice",
	})

	req, _ := http.NewRequest("POST", ts.URL+"/v1/records", body)
	req.Header.Set("Authorization", "Bearer "+pt)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/records: %v", err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("read grant should not cover contribute, want 403, got %d", resp.StatusCode)
	}
}
