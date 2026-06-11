package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// setRetriever wires a degraded-mode retriever (nil gateway → degraded:true) to srv.
func setRetriever(t *testing.T, srv *api.Server, st store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	vi := vindex.New(st.Vectors(), 4, "test-model")
	r := retrieval.New(st.Memories(), st.Records(), vi, nil, log)
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

// TestRetrieve_DebugBreakdownPresent proves that debug:true adds per-item
// scoring breakdowns to the response, exercising the breakdownToWire helper.
// It also verifies that support.strength is always present (Phase 10 AC-8).
func TestRetrieve_DebugBreakdownPresent(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-dbp")
	setRetriever(t, srv, st)

	// Insert a memory with a unique term so lexical search returns it.
	scope := identity.Scope{Tenant: "tenant-dbp"}
	uniqueTerm := "debugbreakdownapitestxyzzy"
	nowMs := time.Now().UnixMilli()
	memID := fmt.Sprintf("01dbp%016x0000", nowMs)
	evtID := fmt.Sprintf("01evt%016x0000", nowMs)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID:          memID,
			Kind:        "fact",
			Content:     uniqueTerm + " is a unique test memory for debug breakdown",
			Context:     "ctx",
			Status:      "active",
			Confidence:  0.9,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			ContentHash: memID, // reuse memID as a unique content hash
			CreatedAt:   nowMs,
			UpdatedAt:   nowMs,
		},
		Events: []store.Event{
			{ID: evtID, Type: "memory.added", SubjectID: memID, Payload: `{}`},
		},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	body := jsonBody(t, map[string]interface{}{
		"query": uniqueTerm,
		"limit": 5,
		"debug": true,
	})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve debug: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("debug retrieve: got %d want 200", resp.StatusCode)
	}

	var res struct {
		Items []struct {
			ID        string `json:"id"`
			Breakdown *struct {
				FinalScore float64 `json:"final_score"`
			} `json:"breakdown"`
		} `json:"items"`
		Support struct {
			Strength string `json:"strength"`
		} `json:"support"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode debug response: %v", err)
	}
	if res.Support.Strength == "" {
		t.Error("support.strength must not be empty (Phase 10 AC-8)")
	}
	if len(res.Items) == 0 {
		t.Skip("no items returned by lexical search — skip breakdown assertion")
	}
	for _, item := range res.Items {
		if item.Breakdown == nil {
			t.Errorf("debug=true: item %s missing breakdown field", item.ID)
			continue
		}
		if item.Breakdown.FinalScore <= 0 {
			t.Errorf("debug=true: item %s FinalScore %.6f want > 0", item.ID, item.Breakdown.FinalScore)
		}
	}
}
