package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type causalBody struct {
	Root  string `json:"root"`
	Nodes []struct {
		MemoryID   string `json:"memory_id"`
		Kind       string `json:"kind"`
		Content    string `json:"content"`
		Provenance []struct {
			RecordID string `json:"record_id"`
		} `json:"provenance"`
	} `json:"nodes"`
	Edges []struct {
		From       string  `json:"from"`
		To         string  `json:"to"`
		Type       string  `json:"type"`
		Confidence float64 `json:"confidence"`
	} `json:"edges"`
	Truncated bool `json:"truncated"`
}

func seedCausalAPI(t *testing.T, st store.Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	for _, m := range []store.Memory{
		{ID: "01CAUSEAPIAAAAAAAAAAAAAAAA", Kind: "decision", Content: "cause", Status: "active",
			Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1},
		{ID: "01EFFECTAPIAAAAAAAAAAAAAAA", Kind: "decision", Content: "effect", Status: "active",
			Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 2, UpdatedAt: 2},
	} {
		if err := st.Memories().Insert(ctx, scope, m); err != nil {
			t.Fatalf("seed memory: %v", err)
		}
	}
	if err := st.Memories().InsertLinks(ctx, scope, []store.Link{{
		ID: "01LINKAPIAAAAAAAAAAAAAAAAA", TenantID: tenant,
		FromMemory: "01CAUSEAPIAAAAAAAAAAAAAAAA", ToMemory: "01EFFECTAPIAAAAAAAAAAAAAAA",
		Type: "led_to", Source: "inferred", Confidence: 0.9, CreatedAt: 3,
	}}); err != nil {
		t.Fatalf("seed link: %v", err)
	}
}

// TestCausal_Backward proves GET /v1/causal walks to the cause.
func TestCausal_Backward(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-causal"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedCausalAPI(t, st, tenant)

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/causal?memory_id=01EFFECTAPIAAAAAAAAAAAAAAA&direction=backward&depth=3", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/causal: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body causalBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Root != "01EFFECTAPIAAAAAAAAAAAAAAA" || len(body.Nodes) != 2 || len(body.Edges) != 1 {
		t.Fatalf("unexpected graph: %+v", body)
	}
	if body.Edges[0].From != "01CAUSEAPIAAAAAAAAAAAAAAAA" || body.Edges[0].Type != "led_to" {
		t.Errorf("edge wrong: %+v", body.Edges[0])
	}
}

// TestCausal_MissingMemoryID proves 400 when memory_id is absent.
func TestCausal_MissingMemoryID(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-causal2"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	resp, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/causal", nil, agentKey)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing memory_id: want 400, got %d", resp.StatusCode)
	}
}

// TestCausal_InvalidDirection proves 400 on a bad direction (not 500).
func TestCausal_InvalidDirection(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-causal-dir"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedCausalAPI(t, st, tenant)

	resp, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/causal?memory_id=01EFFECTAPIAAAAAAAAAAAAAAA&direction=sideways", nil, agentKey)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid direction: want 400, got %d", resp.StatusCode)
	}
}

// TestCausal_MissingRoot proves a 200 empty graph for an unknown root.
func TestCausal_MissingRoot(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-causal3"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedCausalAPI(t, st, tenant)

	resp, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/causal?memory_id=01MISSINGAAAAAAAAAAAAAAAAA", nil, agentKey)
	defer drainClose(resp.Body)
	var body causalBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || len(body.Nodes) != 0 {
		t.Errorf("missing root: want 200 + empty graph, got %d / %d nodes", resp.StatusCode, len(body.Nodes))
	}
}
