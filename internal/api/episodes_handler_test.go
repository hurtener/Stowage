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
		ID                string  `json:"id"`
		SessionID         string  `json:"session_id"`
		Title             string  `json:"title"`
		Status            string  `json:"status"`
		Outcome           string  `json:"outcome"`
		StartedAt         int64   `json:"started_at"`
		NarrativeMemoryID string  `json:"narrative_memory_id"`
		Narrative         string  `json:"narrative"`
		Score             float64 `json:"score"`
	} `json:"episodes"`
	NextCursor string `json:"next_cursor"`
	Degraded   bool   `json:"degraded"`
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

// TestEpisodes_Arc proves GET /v1/episodes?arc_of= returns the threaded arc (D-081).
func TestEpisodes_Arc(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-episodes-arc"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	mk := func(epID, narrID, content string, start int64) {
		if err := st.Memories().Insert(ctx, scope, store.Memory{
			ID: narrID, Kind: "narrative", Content: content, Status: "active", Importance: 3,
			Confidence: 0.8, TrustSource: "episodic", Stability: 1.0, EpisodeID: epID, CreatedAt: start, UpdatedAt: start,
		}); err != nil {
			t.Fatalf("narr: %v", err)
		}
		if err := st.Episodes().CreateEpisode(ctx, scope, store.Episode{
			ID: epID, SessionID: epID + "-s", Title: epID, Status: "closed", Outcome: "success",
			StartedAt: start, EndedAt: start + 100, NarrativeMemoryID: narrID, CreatedAt: start, UpdatedAt: start,
		}); err != nil {
			t.Fatalf("ep: %v", err)
		}
	}
	mk("01EPARCONEAAAAAAAAAAAAAAAA", "01NARCONEAAAAAAAAAAAAAAAAA", "shipped the launch", 1000)
	mk("01EPARCTWOAAAAAAAAAAAAAAAA", "01NARCTWOAAAAAAAAAAAAAAAAA", "continued the launch", 2000)
	if err := st.Memories().InsertLinks(ctx, scope, []store.Link{{
		ID: "01LARCAAAAAAAAAAAAAAAAAAAA", TenantID: tenant, FromMemory: "01NARCONEAAAAAAAAAAAAAAAAA",
		ToMemory: "01NARCTWOAAAAAAAAAAAAAAAAA", Type: "relates_to", Source: "inferred", Confidence: 0.7, CreatedAt: 3000,
	}}); err != nil {
		t.Fatalf("link: %v", err)
	}

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/episodes?arc_of=01EPARCONEAAAAAAAAAAAAAAAA", nil, agentKey)
	if err != nil {
		t.Fatalf("GET arc: %v", err)
	}
	defer drainClose(resp.Body)
	var body episodesBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || len(body.Episodes) != 2 {
		t.Fatalf("arc: want 200 + 2 episodes, got %d / %d", resp.StatusCode, len(body.Episodes))
	}
	if body.Episodes[0].ID != "01EPARCTWOAAAAAAAAAAAAAAAA" || body.Episodes[1].ID != "01EPARCONEAAAAAAAAAAAAAAAA" {
		t.Errorf("arc order wrong: %+v", body.Episodes)
	}
}

// TestEpisodes_SimilarNoRetriever proves ?similar_to= returns 200 empty+degraded
// when no retriever is wired (graceful degradation, D-036/D-082).
func TestEpisodes_SimilarNoRetriever(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-episodes-simnr"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedEpisodesAPI(t, st, tenant)

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/episodes?similar_to=launch&k=3", nil, agentKey)
	if err != nil {
		t.Fatalf("similar (no retriever): %v", err)
	}
	defer drainClose(resp.Body)
	var body episodesBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || !body.Degraded || len(body.Episodes) != 0 {
		t.Errorf("no-retriever similar: want 200+degraded+empty, got %d deg=%v n=%d", resp.StatusCode, body.Degraded, len(body.Episodes))
	}
}

// TestEpisodes_SimilarDegraded proves ?similar_to= returns 200 empty+degraded when
// the retriever's gateway is down (nil gateway → SimilarNarratives degrades; D-082).
func TestEpisodes_SimilarDegraded(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTopicsTestServer(t)
	tenant := "tenant-episodes-simdeg"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedEpisodesAPI(t, st, tenant)
	setRetriever(t, srv, st) // nil-gateway retriever → degraded

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/episodes?similar_to=the+launch+story", nil, agentKey)
	if err != nil {
		t.Fatalf("similar (degraded): %v", err)
	}
	defer drainClose(resp.Body)
	var body episodesBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || !body.Degraded || len(body.Episodes) != 0 {
		t.Errorf("degraded similar: want 200+degraded+empty, got %d deg=%v n=%d", resp.StatusCode, body.Degraded, len(body.Episodes))
	}
}
