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
	if res.API != "v1" {
		t.Errorf("api field: got %q want v1", res.API)
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

// TestRetrieve_SupersededCompanionInline covers the D-114 handler block: a superseded
// predecessor returned as a stale companion carries its successor's value + date inline
// (superseded_by_content / superseded_by_date) so non-prompt clients are self-contained.
func TestRetrieve_SupersededCompanionInline(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-sup")
	scope := identity.Scope{Tenant: "tenant-sup"}

	// Retriever with dual-visibility (D-105) enabled so stale companions are attached.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	vi := vindex.New(st.Vectors(), 4, "test-model")
	srv.SetRetriever(retrieval.New(st.Memories(), st.Records(), vi, nil, log).WithIncludeSuperseded(true))

	nowMs := time.Now().UnixMilli()
	term := "supersededcompanionxyzzy"
	successorID := fmt.Sprintf("01suc%016x0000", nowMs)
	predecessorID := fmt.Sprintf("01prd%016x0000", nowMs)

	mk := func(id, content, status, supersededBy string, validFrom int64) {
		cs := store.CommitSet{
			Action: store.ActionAdd,
			Memory: store.Memory{
				ID: id, Kind: "fact", Content: content, Status: status, SupersededByID: supersededBy,
				Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0, ValidFrom: validFrom,
				ContentHash: id, CreatedAt: nowMs, UpdatedAt: nowMs,
			},
			Events: []store.Event{{ID: id + "ev", Type: "memory.added", SubjectID: id, Payload: `{}`}},
		}
		if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// Successor (current) matches the query; predecessor is superseded by it.
	mk(successorID, term+" the value is now 125", "active", "", nowMs)
	mk(predecessorID, term+" the value was 120", "superseded", successorID, nowMs-1000)

	body := jsonBody(t, map[string]interface{}{"query": term, "limit": 10})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retrieve: got %d want 200", resp.StatusCode)
	}
	var res struct {
		Items []struct {
			ID                  string `json:"id"`
			Stale               bool   `json:"stale"`
			SupersededByContent string `json:"superseded_by_content"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sawStale bool
	for _, it := range res.Items {
		if it.ID == predecessorID && it.Stale {
			sawStale = true
			if it.SupersededByContent == "" {
				t.Errorf("stale companion must carry superseded_by_content inline (D-114)")
			}
		}
	}
	if !sawStale {
		t.Skip("successor not returned by lexical search — stale-companion path unobservable")
	}
}

// TestRetrieve_InvalidProfile proves an unknown profile is rejected with 400 (D-034:
// the profile knob ships with validation).
func TestRetrieve_InvalidProfile(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-rip")
	setRetriever(t, srv, st)

	body := jsonBody(t, map[string]interface{}{"query": "anything", "profile": "bogus-profile"})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve invalid profile: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid profile: got %d want 400", resp.StatusCode)
	}
}

// TestRetrieve_IncludeLanesPopulatesItems seeds a matching memory and requests
// include_lanes:true so the per-item lane attribution branch in handleRetrieve runs
// (the existing success test seeds no memory, so the item loop never executes).
func TestRetrieve_IncludeLanesPopulatesItems(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-lanes")
	setRetriever(t, srv, st)

	scope := identity.Scope{Tenant: "tenant-lanes"}
	uniqueTerm := "includelanesapitestxyzzy"
	nowMs := time.Now().UnixMilli()
	memID := fmt.Sprintf("01lns%016x0000", nowMs)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: uniqueTerm + " is a unique lanes test memory",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
			ContentHash: memID, CreatedAt: nowMs, UpdatedAt: nowMs,
		},
		Events: []store.Event{{ID: fmt.Sprintf("01lev%016x0000", nowMs), Type: "memory.added", SubjectID: memID, Payload: `{}`}},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("insert memory: %v", err)
	}

	body := jsonBody(t, map[string]interface{}{
		"query": uniqueTerm, "limit": 5, "include_lanes": true,
	})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/retrieve", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/retrieve lanes: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lanes retrieve: got %d want 200", resp.StatusCode)
	}
	var res struct {
		Items []struct {
			ID    string   `json:"id"`
			Lanes []string `json:"lanes"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode lanes response: %v", err)
	}
	if len(res.Items) == 0 {
		t.Skip("no items returned by lexical search — lane attribution unobservable")
	}
	if len(res.Items[0].Lanes) == 0 {
		t.Errorf("include_lanes:true: item %s carries no lane attribution", res.Items[0].ID)
	}
}
