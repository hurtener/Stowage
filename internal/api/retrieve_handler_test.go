package api_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// setRetriever wires a degraded-mode retriever (nil gateway → degraded:true) to srv.
func setRetriever(t *testing.T, srv *api.Server, st store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	vi := vindex.New(st.Vectors(), 4, "test-model")
	r := retrieval.New(st.Memories(), vi, nil, log)
	srv.SetRetriever(r)
}

// --- POST /v1/retrieve tests -------------------------------------------------

// TestRetrieve_Unauthorized proves 401 when no auth header is provided.
func TestRetrieve_Unauthorized(t *testing.T) {
	t.Parallel()
	_, ts, _ := newTestServer(t)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve",
		strings.NewReader(`{"query":"test"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("retrieve no auth: got %d want 401", resp.StatusCode)
	}
}

// TestRetrieve_NilRetriever proves 503 when SetRetriever has not been called.
func TestRetrieve_NilRetriever(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-nilr")

	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve",
		strings.NewReader(`{"query":"hello"}`))
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("retrieve nil retriever: got %d want 503", resp.StatusCode)
	}
}

// TestRetrieve_EmptyQuery proves 400 when query is the empty string.
func TestRetrieve_EmptyQuery(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-eq")
	setRetriever(t, srv, st)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve",
		strings.NewReader(`{"query":""}`))
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("retrieve empty query: got %d want 400", resp.StatusCode)
	}
}

// TestRetrieve_Success proves 200 with a valid response envelope.
// No memories are seeded so items will be empty; degraded:true because gateway is nil.
func TestRetrieve_Success(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-rs")
	setRetriever(t, srv, st)

	body := jsonBody(t, map[string]interface{}{
		"query":         "what is the capital of France",
		"limit":         10,
		"include_lanes": true,
	})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("retrieve: got %d want 200", resp.StatusCode)
	}

	var res struct {
		Items    []interface{} `json:"items"`
		Degraded bool          `json:"degraded"`
		API      string        `json:"api"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if res.API != "v0" {
		t.Errorf("api field: got %q want v0", res.API)
	}
	if !res.Degraded {
		t.Error("expected degraded:true when gateway is nil")
	}
}

// TestRetrieve_WrongContentType proves 415 when Content-Type is not application/json.
func TestRetrieve_WrongContentType(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-rwct")
	setRetriever(t, srv, st)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve",
		strings.NewReader(`{"query":"test"}`))
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "text/plain")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve wrong ct: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("retrieve wrong content-type: got %d want 415", resp.StatusCode)
	}
}

// TestRetrieve_MalformedJSON proves 400 when the request body is not valid JSON.
func TestRetrieve_MalformedJSON(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-rmj")
	setRetriever(t, srv, st)

	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve",
		strings.NewReader(`{not json`))
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve malformed: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("retrieve malformed json: got %d want 400", resp.StatusCode)
	}
}
