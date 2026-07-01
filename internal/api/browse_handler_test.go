package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type browseBody struct {
	Memories []struct {
		ID        string `json:"id"`
		Content   string `json:"content"`
		Status    string `json:"status"`
		CreatedAt int64  `json:"created_at"`
	} `json:"memories"`
	NextCursor string `json:"next_cursor"`
}

func seedBrowseMemories(t *testing.T, st store.Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	mk := func(id, status string, createdAt int64) {
		if err := st.Memories().Insert(ctx, scope, store.Memory{
			ID: id, Kind: "fact", Content: "content-" + id, Status: status,
			Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	mk("m1", "active", 1000)
	mk("m2", "active", 2000)
	mk("m3", "superseded", 3000)
}

// TestBrowse_RecentDefault proves GET /v1/memories (default mode=recent) returns
// the scope's memories most-recent-first, status-agnostic (ae5, D-143).
func TestBrowse_RecentDefault(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-browse-recent"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedBrowseMemories(t, st, tenant)

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/memories", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/memories: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body browseBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Memories) != 3 {
		t.Fatalf("want 3 memories, got %d: %+v", len(body.Memories), body.Memories)
	}
	if body.Memories[0].ID != "m3" || body.Memories[1].ID != "m2" || body.Memories[2].ID != "m1" {
		t.Errorf("expected most-recent-first order, got %+v", body.Memories)
	}
}

// TestBrowse_SupersededMode proves ?mode=superseded returns only superseded rows,
// oldest-first (the deliberate H4 asymmetry).
func TestBrowse_SupersededMode(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-browse-superseded"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedBrowseMemories(t, st, tenant)
	// A second superseded row so ordering is non-trivial.
	ctx := context.Background()
	if err := st.Memories().Insert(ctx, identity.Scope{Tenant: tenant}, store.Memory{
		ID: "m0", Kind: "fact", Content: "content-m0", Status: "superseded",
		Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 500, UpdatedAt: 500,
	}); err != nil {
		t.Fatalf("seed m0: %v", err)
	}

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/memories?mode=superseded", nil, agentKey)
	if err != nil {
		t.Fatalf("GET superseded: %v", err)
	}
	defer drainClose(resp.Body)
	var body browseBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || len(body.Memories) != 2 {
		t.Fatalf("want 200+2, got %d/%d: %+v", resp.StatusCode, len(body.Memories), body.Memories)
	}
	// ListByStatus orders created_at ASC — oldest-first.
	if body.Memories[0].ID != "m0" || body.Memories[1].ID != "m3" {
		t.Errorf("expected oldest-first order, got %+v", body.Memories)
	}
	for _, m := range body.Memories {
		if m.Status != "superseded" {
			t.Errorf("non-superseded row in superseded mode: %+v", m)
		}
	}
}

// TestBrowse_UnknownMode proves an unrecognised mode is rejected 4xx, never
// silently defaulted (AC-7).
func TestBrowse_UnknownMode(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-browse-badmode"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/memories?mode=bogus", nil, agentKey)
	if err != nil {
		t.Fatalf("GET bogus mode: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400 for unknown mode, got %d", resp.StatusCode)
	}
}

// TestBrowse_BadCursor proves a malformed cursor is rejected 4xx, not a panic
// or a silent first page (AC-8).
func TestBrowse_BadCursor(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-browse-badcursor"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/memories?cursor=not-a-cursor", nil, agentKey)
	if err != nil {
		t.Fatalf("GET bad cursor: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400 for a malformed cursor, got %d", resp.StatusCode)
	}
}

// TestBrowse_LimitAndPagination proves an explicit ?limit= paginates via
// next_cursor.
func TestBrowse_LimitAndPagination(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-browse-paginate"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedBrowseMemories(t, st, tenant)

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/memories?limit=2", nil, agentKey)
	if err != nil {
		t.Fatalf("GET limit=2: %v", err)
	}
	defer drainClose(resp.Body)
	var body browseBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || len(body.Memories) != 2 {
		t.Fatalf("want 200+2, got %d/%d", resp.StatusCode, len(body.Memories))
	}
	if body.NextCursor == "" {
		t.Fatal("expected a next_cursor — a third row remains")
	}

	resp2, err := doRequest(t, http.MethodGet, ts.URL+"/v1/memories?limit=2&cursor="+body.NextCursor, nil, agentKey)
	if err != nil {
		t.Fatalf("GET page2: %v", err)
	}
	defer drainClose(resp2.Body)
	var body2 browseBody
	_ = json.NewDecoder(resp2.Body).Decode(&body2)
	if resp2.StatusCode != http.StatusOK || len(body2.Memories) != 1 {
		t.Fatalf("page2: want 200+1, got %d/%d", resp2.StatusCode, len(body2.Memories))
	}
	if body2.NextCursor != "" {
		t.Errorf("expected an empty cursor on the last page, got %q", body2.NextCursor)
	}
}
