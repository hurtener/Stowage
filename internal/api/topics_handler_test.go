package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	"github.com/hurtener/stowage/internal/topics"

	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
)

// newTopicsTestServer creates an API server with the topics service wired.
func newTopicsTestServer(t *testing.T) (*api.Server, *httptest.Server, store.Store) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "topics-api-*.db")
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

	logger := noopServerLog()
	reg := prometheus.NewRegistry()
	srv, err := api.New(cfg, st, logger, reg)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	// Wire topics service (assistant profile — pack:preferences virtual pack).
	topicSvc := topics.New(st.Topics(), logger, "assistant")
	srv.SetTopicService(topicSvc)

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx2)
	})
	return srv, ts, st
}

// noopServerLog returns a logger that discards all output.
func noopServerLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── GET /v1/topics ────────────────────────────────────────────────────────────

func TestTopics_List_VirtualPack(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-topics")

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/topics", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
	var body struct {
		Topics []struct {
			Key    string `json:"key"`
			Source string `json:"source"`
		} `json:"topics"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Topics) == 0 {
		t.Error("want virtual pack topics, got empty list")
	}
	for _, tp := range body.Topics {
		if tp.Source != "pack" {
			t.Errorf("topic %q: want source=pack, got %q", tp.Key, tp.Source)
		}
	}
}

func TestTopics_List_Explicit(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-explicit")

	// PUT one explicit topic.
	body := bytes.NewBufferString(`[{"key":"my-topic","description":"My topic","status":"active"}]`)
	resp, err := doRequest(t, http.MethodPut, ts.URL+"/v1/topics", body, agentKey)
	if err != nil {
		t.Fatalf("PUT /v1/topics: %v", err)
	}
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("PUT: want 200, got %d", resp.StatusCode)
	}

	// GET: should return the explicit topic (not virtual pack).
	resp2, err := doRequest(t, http.MethodGet, ts.URL+"/v1/topics", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/topics: %v", err)
	}
	defer drainClose(resp2.Body)
	var got struct {
		Topics []struct {
			Key    string `json:"key"`
			Source string `json:"source"`
		} `json:"topics"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Topics) != 1 {
		t.Fatalf("want 1 topic, got %d", len(got.Topics))
	}
	if got.Topics[0].Key != "my-topic" {
		t.Errorf("want key=my-topic, got %q", got.Topics[0].Key)
	}
	if got.Topics[0].Source != "explicit" {
		t.Errorf("want source=explicit, got %q", got.Topics[0].Source)
	}
}

func TestTopics_List_NoAuth(t *testing.T) {
	t.Parallel()
	_, ts, _ := newTopicsTestServer(t)
	resp, err := http.Get(ts.URL + "/v1/topics")
	if err != nil {
		t.Fatalf("GET /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", resp.StatusCode)
	}
}

// ── PUT /v1/topics ────────────────────────────────────────────────────────────

func TestTopics_Upsert_Valid(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-upsert")

	body := bytes.NewBufferString(`[{"key":"k1","description":"desc1"},{"key":"k2","description":"desc2","status":"paused"}]`)
	resp, err := doRequest(t, http.MethodPut, ts.URL+"/v1/topics", body, agentKey)
	if err != nil {
		t.Fatalf("PUT /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
	var got map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["upserted"] != 2 {
		t.Errorf("want upserted=2, got %d", got["upserted"])
	}
}

func TestTopics_Upsert_EmptyArray(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-empty-arr")

	body := bytes.NewBufferString(`[]`)
	resp, err := doRequest(t, http.MethodPut, ts.URL+"/v1/topics", body, agentKey)
	if err != nil {
		t.Fatalf("PUT /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

func TestTopics_Upsert_InvalidStatus(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-bad-status")

	body := bytes.NewBufferString(`[{"key":"k","status":"deleted"}]`)
	resp, err := doRequest(t, http.MethodPut, ts.URL+"/v1/topics", body, agentKey)
	if err != nil {
		t.Fatalf("PUT /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

func TestTopics_Upsert_EmptyKey(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-empty-key")

	body := bytes.NewBufferString(`[{"key":""}]`)
	resp, err := doRequest(t, http.MethodPut, ts.URL+"/v1/topics", body, agentKey)
	if err != nil {
		t.Fatalf("PUT /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

func TestTopics_Upsert_NotJSON(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-not-json")

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/topics", bytes.NewBufferString(`not json`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", bearerHeader(agentKey))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("want 415, got %d", resp.StatusCode)
	}
}

// ── DELETE /v1/topics/{key} ───────────────────────────────────────────────────

func TestTopics_Delete_ExistingKey(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-del")

	// First upsert a topic.
	body := bytes.NewBufferString(`[{"key":"del-me","description":"to be deleted"}]`)
	resp, err := doRequest(t, http.MethodPut, ts.URL+"/v1/topics", body, agentKey)
	if err != nil {
		t.Fatalf("PUT /v1/topics: %v", err)
	}
	drainClose(resp.Body)

	// Now delete it.
	resp2, err := doRequest(t, http.MethodDelete, ts.URL+"/v1/topics/del-me", nil, agentKey)
	if err != nil {
		t.Fatalf("DELETE /v1/topics/del-me: %v", err)
	}
	defer drainClose(resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("want 200, got %d", resp2.StatusCode)
	}
}

func TestTopics_Delete_NonExistentKey(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-notfound")

	resp, err := doRequest(t, http.MethodDelete, ts.URL+"/v1/topics/no-such-key", nil, agentKey)
	if err != nil {
		t.Fatalf("DELETE /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("want 404, got %d", resp.StatusCode)
	}
}

func TestTopics_Delete_ThenListReturnsVirtualPack(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-del-revert")

	// PUT a topic.
	putBody := bytes.NewBufferString(`[{"key":"temp-topic","description":"temp"}]`)
	resp, err := doRequest(t, http.MethodPut, ts.URL+"/v1/topics", putBody, agentKey)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	drainClose(resp.Body)

	// DELETE it.
	resp2, err := doRequest(t, http.MethodDelete, ts.URL+"/v1/topics/temp-topic", nil, agentKey)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	drainClose(resp2.Body)

	// GET: scope now has no active explicit topics → virtual pack returned.
	resp3, err := doRequest(t, http.MethodGet, ts.URL+"/v1/topics", nil, agentKey)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer drainClose(resp3.Body)
	var body struct {
		Topics []struct {
			Source string `json:"source"`
		} `json:"topics"`
	}
	if err := json.NewDecoder(resp3.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Topics) == 0 {
		t.Error("want virtual pack topics after delete, got none")
	}
	for _, tp := range body.Topics {
		if tp.Source != "pack" {
			t.Errorf("want source=pack after delete, got %q", tp.Source)
		}
	}
}

// TestTopics_List_NoTopicService proves GET /v1/topics returns 503 when the
// topic service has not been wired (SetTopicService not called).
func TestTopics_List_NoTopicService(t *testing.T) {
	t.Parallel()

	f, err := os.CreateTemp(t.TempDir(), "topics-nosvc-*.db")
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

	logger := noopServerLog()
	reg := prometheus.NewRegistry()
	srv, err := api.New(cfg, st, logger, reg)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	// Intentionally do NOT call srv.SetTopicService.

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx2)
	})

	_, agentKey := mustCreateAgentKey(t, st, "tenant-nosvc")
	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/topics", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/topics: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", resp.StatusCode)
	}
}

// TestTopics_Upsert_MalformedJSON proves PUT /v1/topics returns 400 when the
// request body is valid JSON but not a JSON array (decode error path).
func TestTopics_Upsert_MalformedJSON(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-malformed")

	// Send malformed JSON with correct Content-Type header.
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v1/topics",
		bytes.NewBufferString(`{not valid json`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerHeader(agentKey))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /v1/topics malformed: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400, got %d", resp.StatusCode)
	}
}

// ── helper ────────────────────────────────────────────────────────────────────

// doRequest sends an HTTP request with optional JSON body and auth.
func doRequest(t *testing.T, method, url string, body *bytes.Buffer, authKey string) (*http.Response, error) {
	t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(method, url, body)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if authKey != "" {
		req.Header.Set("Authorization", bearerHeader(authKey))
	}
	return http.DefaultClient.Do(req)
}
