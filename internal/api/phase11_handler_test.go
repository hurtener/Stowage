package api_test

// Phase 11 handler tests: drilldown, feedback, citations/resolve.
// Each handler is exercised for the primary happy path and key validation branches.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// --- helpers -----------------------------------------------------------------

// insertTestMemoryWithProvenance inserts a memory + record + provenance row
// and returns (memoryID, recordID). Used by drilldown tests.
func insertTestMemoryWithProvenance(t *testing.T, st store.Store, scope identity.Scope, content string) (memID, recID string) {
	t.Helper()
	nowMs := time.Now().UnixMilli()
	recID = fmt.Sprintf("01rec%016x", nowMs)
	memID = fmt.Sprintf("01mem%016x", nowMs)
	evtID := fmt.Sprintf("01evt%016x", nowMs)

	rec := store.Record{
		ID: recID, Role: "user", Content: content,
		OccurredAt: nowMs, CreatedAt: nowMs,
	}
	if err := st.Records().Append(context.Background(), scope, []store.Record{rec}); err != nil {
		t.Fatalf("append record: %v", err)
	}

	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID:          memID,
			Kind:        "fact",
			Content:     content,
			Context:     "test context",
			Status:      "active",
			Confidence:  0.9,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			ContentHash: memID, // unique per call
			CreatedAt:   nowMs,
			UpdatedAt:   nowMs,
		},
		Provenance: []store.Provenance{
			{
				ID: evtID, MemoryID: memID, RecordID: recID,
				SpanStart: 0, SpanEnd: len(content), TenantID: scope.Tenant,
				CreatedAt: nowMs,
			},
		},
		Events: []store.Event{
			{ID: evtID + "e", Type: "memory.added", SubjectID: memID, Payload: `{}`},
		},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("commit memory: %v", err)
	}
	return memID, recID
}

// insertTestInjection inserts an injection row and returns the injection ID.
func insertTestInjection(t *testing.T, st store.Store, scope identity.Scope, memID, responseID string) string {
	t.Helper()
	injID := fmt.Sprintf("01inj%016x", time.Now().UnixNano())
	inj := store.Injection{
		ID:         injID,
		ResponseID: responseID,
		MemoryID:   memID,
		Rank:       1,
		Score:      0.9,
		Lane:       "lexical",
		CreatedAt:  time.Now().UnixMilli(),
	}
	if err := st.Injections().Append(context.Background(), scope, []store.Injection{inj}); err != nil {
		t.Fatalf("append injection: %v", err)
	}
	return injID
}

// --- POST /v1/drilldown tests ------------------------------------------------

func TestDrilldown_ByMemoryID(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-dd1")
	scope := identity.Scope{Tenant: "tenant-dd1"}

	content := "The capital of France is Paris"
	memID, _ := insertTestMemoryWithProvenance(t, st, scope, content)

	body := jsonBody(t, map[string]string{"memory_id": memID})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/drilldown", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/drilldown: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drilldown by memory_id: got %d want 200", resp.StatusCode)
	}
	var res struct {
		MemoryID string `json:"memory_id"`
		Spans    []struct {
			RecordID string `json:"record_id"`
			Excerpt  string `json:"excerpt"`
		} `json:"spans"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode drilldown response: %v", err)
	}
	if res.MemoryID != memID {
		t.Errorf("memory_id: got %q want %q", res.MemoryID, memID)
	}
	if len(res.Spans) == 0 {
		t.Error("expected at least one span")
	}
	if len(res.Spans) > 0 && res.Spans[0].Excerpt != content {
		t.Errorf("excerpt: got %q want %q", res.Spans[0].Excerpt, content)
	}
}

func TestDrilldown_ByCitation(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-dd2")
	scope := identity.Scope{Tenant: "tenant-dd2"}

	content := "Go is a statically typed compiled language"
	memID, _ := insertTestMemoryWithProvenance(t, st, scope, content)
	responseID := fmt.Sprintf("01rsp%016x", time.Now().UnixNano())
	citationID := insertTestInjection(t, st, scope, memID, responseID)

	body := jsonBody(t, map[string]string{"citation": citationID})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/drilldown", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/drilldown citation: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("drilldown by citation: got %d want 200", resp.StatusCode)
	}
	var res struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.MemoryID != memID {
		t.Errorf("memory_id via citation: got %q want %q", res.MemoryID, memID)
	}
}

func TestDrilldown_ValidationErrors(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-ddv")

	tests := []struct {
		name    string
		body    string
		wantSts int
	}{
		{"neither set", `{}`, http.StatusBadRequest},
		{"both set", `{"memory_id":"x","citation":"y"}`, http.StatusBadRequest},
		{"malformed json", `{bad`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest("POST", ts.URL+"/v1/drilldown",
				strings.NewReader(tc.body))
			req.Header.Set("Authorization", bearerHeader(pt))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /v1/drilldown: %v", err)
			}
			defer drainClose(resp.Body)
			if resp.StatusCode != tc.wantSts {
				t.Errorf("%s: got %d want %d", tc.name, resp.StatusCode, tc.wantSts)
			}
		})
	}
}

func TestDrilldown_CitationNotFound(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-dd404")

	body := jsonBody(t, map[string]string{"citation": "01ZZZZZZZZZZZZZZZZZZZZZZZZ"})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/drilldown", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/drilldown: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing citation: got %d want 404", resp.StatusCode)
	}
}

func TestDrilldown_NoProvenanceReturnsEmpty(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-ddnp")
	scope := identity.Scope{Tenant: "tenant-ddnp"}

	// Insert memory without provenance.
	nowMs := time.Now().UnixMilli()
	memID := fmt.Sprintf("01mnp%016x", nowMs)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID:          memID,
			Kind:        "fact",
			Content:     "memory without provenance",
			Status:      "active",
			Confidence:  0.8,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			ContentHash: memID,
			CreatedAt:   nowMs,
			UpdatedAt:   nowMs,
		},
		Events: []store.Event{
			{ID: fmt.Sprintf("01evnp%016x", nowMs), Type: "memory.added", SubjectID: memID, Payload: `{}`},
		},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("commit memory: %v", err)
	}

	body := jsonBody(t, map[string]string{"memory_id": memID})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/drilldown", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/drilldown: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no provenance: got %d want 200", resp.StatusCode)
	}
	var res struct {
		Spans []interface{} `json:"spans"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Spans) != 0 {
		t.Errorf("expected 0 spans for memory with no provenance, got %d", len(res.Spans))
	}
}

// --- POST /v1/feedback tests -------------------------------------------------

func TestFeedback_MemoryLevel(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-fb1")
	scope := identity.Scope{Tenant: "tenant-fb1"}

	nowMs := time.Now().UnixMilli()
	memID := fmt.Sprintf("01mfb%016x", nowMs)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "feedback test memory",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: memID, CreatedAt: nowMs, UpdatedAt: nowMs,
		},
		Events: []store.Event{{ID: fmt.Sprintf("01efb%016x", nowMs), Type: "memory.added", SubjectID: memID, Payload: `{}`}},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("commit memory: %v", err)
	}

	body := jsonBody(t, map[string]string{"memory_id": memID, "signal": "use"})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/feedback", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/feedback: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feedback memory: got %d want 200", resp.StatusCode)
	}
	var res struct {
		Applied int    `json:"applied"`
		Signal  string `json:"signal"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Applied != 1 {
		t.Errorf("applied: got %d want 1", res.Applied)
	}
	if res.Signal != "use" {
		t.Errorf("signal: got %q want use", res.Signal)
	}
}

func TestFeedback_ResponseLevel(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-fb2")
	scope := identity.Scope{Tenant: "tenant-fb2"}

	nowMs := time.Now().UnixMilli()
	memID := fmt.Sprintf("01mfb2%015x", nowMs)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "response feedback memory",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: memID, CreatedAt: nowMs, UpdatedAt: nowMs,
		},
		Events: []store.Event{{ID: fmt.Sprintf("01efb2%015x", nowMs), Type: "memory.added", SubjectID: memID, Payload: `{}`}},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("commit memory: %v", err)
	}
	responseID := fmt.Sprintf("01rsp2%015x", nowMs)
	_ = insertTestInjection(t, st, scope, memID, responseID)

	body := jsonBody(t, map[string]string{"response_id": responseID, "signal": "save"})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/feedback", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/feedback: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feedback response: got %d want 200", resp.StatusCode)
	}
	var res struct {
		Applied int `json:"applied"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Applied < 1 {
		t.Errorf("applied: got %d want >= 1", res.Applied)
	}
}

func TestFeedback_WrongCitation(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-fb3")
	scope := identity.Scope{Tenant: "tenant-fb3"}

	nowMs := time.Now().UnixMilli()
	memID := fmt.Sprintf("01mfb3%015x", nowMs)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "wrong citation test",
			Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: memID, CreatedAt: nowMs, UpdatedAt: nowMs,
		},
		Events: []store.Event{{ID: fmt.Sprintf("01efb3%015x", nowMs), Type: "memory.added", SubjectID: memID, Payload: `{}`}},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("commit memory: %v", err)
	}
	responseID := fmt.Sprintf("01rsp3%015x", nowMs)
	citID := insertTestInjection(t, st, scope, memID, responseID)

	body := jsonBody(t, map[string]string{"citation": citID, "signal": "wrong_citation"})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/feedback", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/feedback wrong_citation: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feedback wrong_citation: got %d want 200", resp.StatusCode)
	}
	// Verify memory counters incremented.
	mem, err := st.Memories().Get(context.Background(), scope, memID)
	if err != nil {
		t.Fatalf("get memory: %v", err)
	}
	if mem.NoiseCount != 1 {
		t.Errorf("noise_count: got %d want 1", mem.NoiseCount)
	}
	if mem.FailCount != 1 {
		t.Errorf("fail_count: got %d want 1", mem.FailCount)
	}
}

func TestFeedback_ValidationErrors(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-fbv")

	tests := []struct {
		name    string
		body    string
		wantSts int
	}{
		{"no signal", `{"memory_id":"x"}`, http.StatusBadRequest},
		{"no target", `{"signal":"use"}`, http.StatusBadRequest},
		{"multiple targets", `{"memory_id":"x","response_id":"y","signal":"use"}`, http.StatusBadRequest},
		{"wrong_citation on memory", `{"memory_id":"x","signal":"wrong_citation"}`, http.StatusBadRequest},
		{"wrong signal on citation", `{"citation":"x","signal":"use"}`, http.StatusBadRequest},
		{"unknown signal", `{"memory_id":"x","signal":"bogus"}`, http.StatusBadRequest},
		{"malformed json", `{bad`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest("POST", ts.URL+"/v1/feedback",
				strings.NewReader(tc.body))
			req.Header.Set("Authorization", bearerHeader(pt))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /v1/feedback: %v", err)
			}
			defer drainClose(resp.Body)
			if resp.StatusCode != tc.wantSts {
				t.Errorf("%s: got %d want %d", tc.name, resp.StatusCode, tc.wantSts)
			}
		})
	}
}

func TestFeedback_WrongCitationNotFound(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-fb404")

	body := jsonBody(t, map[string]string{"citation": "01ZZNOTFOUND0000000000000Z", "signal": "wrong_citation"})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/feedback", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/feedback: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("wrong_citation missing: got %d want 404", resp.StatusCode)
	}
}

// --- POST /v1/citations/resolve tests ----------------------------------------

func TestCitationsResolve_HappyPath(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-cit1")
	scope := identity.Scope{Tenant: "tenant-cit1"}

	content := "Kubernetes orchestrates containers"
	memID, _ := insertTestMemoryWithProvenance(t, st, scope, content)
	responseID := fmt.Sprintf("01rsp1%015x", time.Now().UnixNano())
	citID := insertTestInjection(t, st, scope, memID, responseID)

	body := jsonBody(t, map[string]interface{}{"citations": []string{citID}})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/citations/resolve", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/citations/resolve: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: got %d want 200", resp.StatusCode)
	}
	var res struct {
		Items []struct {
			Citation string `json:"citation"`
			Found    bool   `json:"found"`
			Memory   *struct {
				ID      string `json:"id"`
				Content string `json:"content"`
			} `json:"memory"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items: got %d want 1", len(res.Items))
	}
	item := res.Items[0]
	if item.Citation != citID {
		t.Errorf("citation: got %q want %q", item.Citation, citID)
	}
	if !item.Found {
		t.Error("found: got false want true")
	}
	if item.Memory == nil {
		t.Fatal("memory must not be nil when found=true")
	}
	if item.Memory.ID != memID {
		t.Errorf("memory.id: got %q want %q", item.Memory.ID, memID)
	}
}

func TestCitationsResolve_NotFoundReturnsPerItemMiss(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-cit2")

	body := jsonBody(t, map[string]interface{}{"citations": []string{"01NOTFOUND0000000000000000"}})
	req, _ := http.NewRequest("POST", ts.URL+"/v1/citations/resolve", body)
	req.Header.Set("Authorization", bearerHeader(pt))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/citations/resolve: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve not found: got %d want 200 (per-item miss, not 404)", resp.StatusCode)
	}
	var res struct {
		Items []struct {
			Found bool `json:"found"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items: got %d want 1", len(res.Items))
	}
	if res.Items[0].Found {
		t.Error("found: got true want false for missing citation")
	}
}

func TestCitationsResolve_ValidationErrors(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-citv")

	tests := []struct {
		name    string
		body    string
		wantSts int
	}{
		{"empty citations", `{"citations":[]}`, http.StatusBadRequest},
		{"missing citations key", `{}`, http.StatusBadRequest},
		{"malformed json", `{bad`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, _ := http.NewRequest("POST", ts.URL+"/v1/citations/resolve",
				strings.NewReader(tc.body))
			req.Header.Set("Authorization", bearerHeader(pt))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST /v1/citations/resolve: %v", err)
			}
			defer drainClose(resp.Body)
			if resp.StatusCode != tc.wantSts {
				t.Errorf("%s: got %d want %d", tc.name, resp.StatusCode, tc.wantSts)
			}
		})
	}
}
