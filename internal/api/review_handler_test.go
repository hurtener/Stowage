package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type reviewListBody struct {
	Items []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

type reviewResolveBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func seedPending(t *testing.T, st store.Store, tenant, id, content string, created int64) {
	t.Helper()
	if err := st.Memories().Insert(context.Background(), identity.Scope{Tenant: tenant}, store.Memory{
		ID: id, Kind: "fact", Content: content, Status: "pending_review",
		Confidence: 0.5, TrustSource: "asserted", Stability: 1.0, CreatedAt: created, UpdatedAt: created,
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
}

// TestReview_ListAndApprove covers GET /v1/review + POST /v1/review/{id} approve.
func TestReview_ListAndApprove(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-review"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedPending(t, st, tenant, "pr1", "an uncited claim", 1000)

	// list
	r1, err := doRequest(t, http.MethodGet, ts.URL+"/v1/review", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/review: %v", err)
	}
	defer drainClose(r1.Body)
	var lb reviewListBody
	_ = json.NewDecoder(r1.Body).Decode(&lb)
	if r1.StatusCode != http.StatusOK || len(lb.Items) != 1 || lb.Items[0].ID != "pr1" {
		t.Fatalf("review list: want 200 + [pr1], got %d / %+v", r1.StatusCode, lb.Items)
	}

	// approve
	r2, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/review/pr1", bytes.NewBufferString(`{"action":"approve"}`), agentKey)
	defer drainClose(r2.Body)
	var rb reviewResolveBody
	_ = json.NewDecoder(r2.Body).Decode(&rb)
	if r2.StatusCode != http.StatusOK || rb.Status != "active" {
		t.Fatalf("approve: want 200 + active, got %d / %q", r2.StatusCode, rb.Status)
	}
	mem, _ := st.Memories().Get(context.Background(), identity.Scope{Tenant: tenant}, "pr1")
	if mem.Status != "active" {
		t.Errorf("memory should be active after approve, got %q", mem.Status)
	}
}

// TestReview_RejectAndConflict covers reject + the not-pending 409.
func TestReview_RejectAndConflict(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-review2"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedPending(t, st, tenant, "pr2", "another claim", 1000)

	// reject → quarantined
	r1, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/review/pr2", bytes.NewBufferString(`{"action":"reject"}`), agentKey)
	defer drainClose(r1.Body)
	var rb reviewResolveBody
	_ = json.NewDecoder(r1.Body).Decode(&rb)
	if r1.StatusCode != http.StatusOK || rb.Status != "quarantined" {
		t.Fatalf("reject: want 200 + quarantined, got %d / %q", r1.StatusCode, rb.Status)
	}

	// resolving again (now quarantined, not pending) → 409
	r2, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/review/pr2", bytes.NewBufferString(`{"action":"approve"}`), agentKey)
	defer drainClose(r2.Body)
	if r2.StatusCode != http.StatusConflict {
		t.Errorf("resolve non-pending: want 409, got %d", r2.StatusCode)
	}

	// bad action → 400
	r3, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/review/pr2", bytes.NewBufferString(`{"action":"banana"}`), agentKey)
	defer drainClose(r3.Body)
	if r3.StatusCode != http.StatusBadRequest {
		t.Errorf("bad action: want 400, got %d", r3.StatusCode)
	}

	// missing memory → 404
	r4, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/review/01MISSINGAAAAAAAAAAAAAAAAA", bytes.NewBufferString(`{"action":"approve"}`), agentKey)
	defer drainClose(r4.Body)
	if r4.StatusCode != http.StatusNotFound {
		t.Errorf("missing memory: want 404, got %d", r4.StatusCode)
	}
}

// TestReview_ApproveWithRetriever covers the reviewInvalidator path (retriever wired).
func TestReview_ApproveWithRetriever(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTopicsTestServer(t)
	setRetriever(t, srv, st) // wires a retriever so reviewInvalidator returns its cache
	tenant := "tenant-review-inv"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedPending(t, st, tenant, "pr3", "claim to approve", 1000)

	r, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/review/pr3", bytes.NewBufferString(`{"action":"approve"}`), agentKey)
	defer drainClose(r.Body)
	var rb reviewResolveBody
	_ = json.NewDecoder(r.Body).Decode(&rb)
	if r.StatusCode != http.StatusOK || rb.Status != "active" {
		t.Errorf("approve with retriever: want 200+active, got %d / %q", r.StatusCode, rb.Status)
	}
}
