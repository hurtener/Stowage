package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPprofAdminHandler_NoAuth proves that a request with no Authorization header
// returns 401.
func TestPprofAdminHandler_NoAuth(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	ts := httptest.NewServer(srv.PprofAdminHandler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth: got %d want 401", resp.StatusCode)
	}
}

// TestPprofAdminHandler_BadKey proves that a bogus Bearer token returns 401.
func TestPprofAdminHandler_BadKey(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)
	ts := httptest.NewServer(srv.PprofAdminHandler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest("GET", ts.URL+"/debug/pprof/", nil)
	req.Header.Set("Authorization", "Bearer sk_bogus")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /debug/pprof/ bad key: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad key: got %d want 401", resp.StatusCode)
	}
}

// TestPprofAdminHandler_AgentKey proves that a valid agent (non-admin) key
// returns 403 — pprof is admin-only (D-126).
func TestPprofAdminHandler_AgentKey(t *testing.T) {
	t.Parallel()
	srv, _, st := newTestServer(t)
	_, agentPT := mustCreateAgentKey(t, st, "tenant-pprof-agent")

	ts := httptest.NewServer(srv.PprofAdminHandler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest("GET", ts.URL+"/debug/pprof/", nil)
	req.Header.Set("Authorization", bearerHeader(agentPT))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /debug/pprof/ agent key: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("agent key: got %d want 403", resp.StatusCode)
	}
}

// TestPprofAdminHandler_AdminKey proves that a valid admin key returns 200 on
// GET /debug/pprof/.
func TestPprofAdminHandler_AdminKey(t *testing.T) {
	t.Parallel()
	srv, _, st := newTestServer(t)
	_, adminPT := mustCreateAdminKey(t, st, "tenant-pprof-admin")

	ts := httptest.NewServer(srv.PprofAdminHandler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest("GET", ts.URL+"/debug/pprof/", nil)
	req.Header.Set("Authorization", bearerHeader(adminPT))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /debug/pprof/ admin key: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("admin key: got %d want 200", resp.StatusCode)
	}
}

// TestPprofAdminHandler_NotOnPublicMux proves that the public API mux does NOT
// serve the pprof surface — GET /debug/pprof/ on the normal server returns 404
// (CLAUDE.md §7; D-126).
func TestPprofAdminHandler_NotOnPublicMux(t *testing.T) {
	t.Parallel()
	_, ts, _ := newTestServer(t)

	resp, err := http.Get(ts.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/ on public mux: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("pprof on public mux: got %d want 404 (must not be mounted on public API)", resp.StatusCode)
	}
}
