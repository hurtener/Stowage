package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// playbookBody mirrors the GET /v1/playbook wire envelope for decoding.
type playbookBody struct {
	Sections []struct {
		Title string `json:"title"`
		Kind  string `json:"kind"`
		Items []struct {
			MemoryID string  `json:"memory_id"`
			Kind     string  `json:"kind"`
			Content  string  `json:"content"`
			Score    float64 `json:"score"`
		} `json:"items"`
	} `json:"sections"`
	Budget struct {
		TokenBudget int `json:"token_budget"`
		ItemsPacked int `json:"items_packed"`
		ItemsTotal  int `json:"items_total"`
	} `json:"budget"`
	// These must be absent — the stub placeholder is gone (D-072).
	Stub    *bool  `json:"stub,omitempty"`
	Entries *[]any `json:"entries,omitempty"`
}

// TestPlaybook_Real proves GET /v1/playbook returns a real sectioned playbook.
func TestPlaybook_Real(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-playbook"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	scope := identity.Scope{Tenant: tenant}
	if err := st.Memories().Insert(context.Background(), scope, store.Memory{
		ID: ulid.Make().String(), Kind: "strategy", Content: "Write tests first.",
		Status: "active", Importance: 3, Confidence: 0.9, TrustSource: "llm_extracted",
		Stability: 1.0, UseCount: 5, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/playbook", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/playbook: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body playbookBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Stub != nil || body.Entries != nil {
		t.Errorf("stub placeholder still present: %+v", body)
	}
	if len(body.Sections) != 1 || body.Sections[0].Kind != "strategy" {
		t.Fatalf("want one strategy section, got %+v", body.Sections)
	}
	if body.Sections[0].Items[0].Content != "Write tests first." {
		t.Errorf("unexpected item: %+v", body.Sections[0].Items)
	}
	if body.Budget.TokenBudget <= 0 || body.Budget.ItemsPacked != 1 {
		t.Errorf("unexpected budget: %+v", body.Budget)
	}
}

// TestPlaybook_SessionFilter proves the ?session_id= param narrows assembly.
func TestPlaybook_SessionFilter(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-playbook-sess"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	ctx := context.Background()

	if err := st.Memories().Insert(ctx, identity.Scope{Tenant: tenant, Session: "s1"}, store.Memory{
		ID: ulid.Make().String(), Kind: "strategy", Content: "Session one.",
		Status: "active", Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
		UseCount: 5, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed s1: %v", err)
	}
	if err := st.Memories().Insert(ctx, identity.Scope{Tenant: tenant, Session: "s2"}, store.Memory{
		ID: ulid.Make().String(), Kind: "strategy", Content: "Session two.",
		Status: "active", Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
		UseCount: 5, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("seed s2: %v", err)
	}

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/playbook?session_id=s1", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/playbook?session_id=s1: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body playbookBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Budget.ItemsTotal != 1 {
		t.Errorf("session filter not applied: ItemsTotal=%d, want 1", body.Budget.ItemsTotal)
	}
	if len(body.Sections) == 1 && body.Sections[0].Items[0].Content != "Session one." {
		t.Errorf("session filter returned wrong memory: %+v", body.Sections[0].Items)
	}
}

// TestPlaybook_EmptyScope proves an empty scope returns 200 with no sections.
func TestPlaybook_EmptyScope(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	_, agentKey := mustCreateAgentKey(t, st, "tenant-playbook-empty")

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/playbook", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/playbook: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body playbookBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sections) != 0 {
		t.Errorf("empty scope returned %d sections, want 0", len(body.Sections))
	}
	if body.Budget.TokenBudget <= 0 {
		t.Errorf("budget not populated on empty scope: %+v", body.Budget)
	}
}
