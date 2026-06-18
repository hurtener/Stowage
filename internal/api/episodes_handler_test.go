package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type episodesBody struct {
	Episodes []struct {
		ID                string `json:"id"`
		SessionID         string `json:"session_id"`
		Title             string `json:"title"`
		Status            string `json:"status"`
		Outcome           string `json:"outcome"`
		StartedAt         int64  `json:"started_at"`
		NarrativeMemoryID string `json:"narrative_memory_id"`
		Narrative         string `json:"narrative"`
	} `json:"episodes"`
	NextCursor string `json:"next_cursor"`
}

func seedEpisodesAPI(t *testing.T, st store.Store, tenant string) {
	t.Helper()
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: "01NARRAPIAAAAAAAAAAAAAAAAA", Kind: "narrative", Content: "the launch story",
		Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "episodic", Stability: 1.0,
		EpisodeID: "01EPAPIONEAAAAAAAAAAAAAAAA", CreatedAt: 1000, UpdatedAt: 1000,
	}); err != nil {
		t.Fatalf("seed narrative: %v", err)
	}
	if err := st.Episodes().CreateEpisode(ctx, scope, store.Episode{
		ID: "01EPAPIONEAAAAAAAAAAAAAAAA", SessionID: "s1", Title: "Launch", Status: "closed",
		Outcome: "success", StartedAt: 1000, EndedAt: 2000, NarrativeMemoryID: "01NARRAPIAAAAAAAAAAAAAAAAA",
		CreatedAt: 1000, UpdatedAt: 1000,
	}); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
}

// TestEpisodes_List proves GET /v1/episodes returns episodes + narrative.
func TestEpisodes_List(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-episodes"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedEpisodesAPI(t, st, tenant)

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/episodes", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/episodes: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body episodesBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Episodes) != 1 || body.Episodes[0].ID != "01EPAPIONEAAAAAAAAAAAAAAAA" {
		t.Fatalf("unexpected episodes: %+v", body.Episodes)
	}
	if body.Episodes[0].Narrative != "the launch story" || body.Episodes[0].Outcome != "success" {
		t.Errorf("episode fields wrong: %+v", body.Episodes[0])
	}
}

// TestEpisodes_GetByID + window + missing-id cover the handler branches.
func TestEpisodes_GetByID_Window_Missing(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-episodes2"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedEpisodesAPI(t, st, tenant)

	// get-one
	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/episodes?id=01EPAPIONEAAAAAAAAAAAAAAAA", nil, agentKey)
	if err != nil {
		t.Fatalf("get-one: %v", err)
	}
	defer drainClose(resp.Body)
	var body episodesBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || len(body.Episodes) != 1 {
		t.Fatalf("get-one: want 200+1, got %d / %d", resp.StatusCode, len(body.Episodes))
	}

	// window [from=1500] → still matches (ends 2000 ≥ 1500)
	r2, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/episodes?from=1500", nil, agentKey)
	defer drainClose(r2.Body)
	var b2 episodesBody
	_ = json.NewDecoder(r2.Body).Decode(&b2)
	if len(b2.Episodes) != 1 {
		t.Errorf("window from=1500 should match, got %d", len(b2.Episodes))
	}

	// missing id → 200 empty list (parity with embedded/MCP, not 404).
	r3, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/episodes?id=01MISSINGAAAAAAAAAAAAAAAAA", nil, agentKey)
	defer drainClose(r3.Body)
	var b3 episodesBody
	_ = json.NewDecoder(r3.Body).Decode(&b3)
	if r3.StatusCode != http.StatusOK || len(b3.Episodes) != 0 {
		t.Errorf("missing id: want 200 + empty list, got %d / %d", r3.StatusCode, len(b3.Episodes))
	}
}
