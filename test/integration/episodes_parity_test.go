// episodes_parity_test.go proves the Phase-23 (D-080) all-surfaces-identical bar:
// the deterministic episodic-retrieval read is BYTE IDENTICAL through the embedded
// SDK, the HTTP server, and the MCP tool, over one shared sqlite store seeded with
// fixed-ULID episodes + a narrative memory. Runs under -race.
package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/mock" // register the mock gateway for seed embeds
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

const episodesTenant = "p23-episodes"

func seedEpisodes(t *testing.T, cfg config.Config) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("seed: open: %v", err)
	}
	defer func() { _ = st.Close(ctx) }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("seed: migrate: %v", err)
	}
	scope := identity.Scope{Tenant: episodesTenant}

	// A narrative memory for e1 (fixed ULID).
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: "01NARRAAAAAAAAAAAAAAAAAAAA", Kind: "narrative", Content: "Planned and shipped the launch under a lock.",
		Context: "Launch", Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "episodic",
		Stability: 1.0, EpisodeID: "01EPONEAAAAAAAAAAAAAAAAAAA", CreatedAt: 1_000_000, UpdatedAt: 1_000_000,
	}); err != nil {
		t.Fatalf("seed narrative: %v", err)
	}
	mk := func(id, sess, title string, start, end int64, narr, outcome string) store.Episode {
		return store.Episode{ID: id, SessionID: sess, Title: title, Status: "closed", Outcome: outcome, StartedAt: start, EndedAt: end, NarrativeMemoryID: narr, CreatedAt: start, UpdatedAt: start}
	}
	// e1 (older, narrated) + e2 (newer, not narrated). List is most-recent-first → [e2, e1].
	if err := st.Episodes().CreateEpisode(ctx, scope, mk("01EPONEAAAAAAAAAAAAAAAAAAA", "sess-1", "Launch", 1_000_000, 1_000_500, "01NARRAAAAAAAAAAAAAAAAAAAA", "success")); err != nil {
		t.Fatalf("seed e1: %v", err)
	}
	if err := st.Episodes().CreateEpisode(ctx, scope, mk("01EPTWOAAAAAAAAAAAAAAAAAAA", "sess-2", "Debug", 2_000_000, 2_000_500, "", "failure")); err != nil {
		t.Fatalf("seed e2: %v", err)
	}
}

// seedEpisodeVectors embeds e1's narrative (mock gateway, deterministic) and
// upserts it into the shared store's vector BLOBs, so every surface's hnsw index
// rebuilds the same vector and the similar_to path is non-trivially correct +
// byte-identical (Phase 23b, D-082).
func seedEpisodeVectors(t *testing.T, cfg config.Config) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("seed vectors: open: %v", err)
	}
	defer func() { _ = st.Close(ctx) }()
	gw, err := gateway.Open(ctx, cfg.Gateway, slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("seed vectors: gateway: %v", err)
	}
	defer func() { _ = gw.Close(ctx) }()
	const narrative = "Planned and shipped the launch under a lock."
	emb, err := gw.Embed(ctx, gateway.EmbedRequest{Inputs: []string{narrative}})
	if err != nil {
		t.Fatalf("seed vectors: embed: %v", err)
	}
	vi := vindex.New(st.Vectors(), cfg.Gateway.EmbedDims, cfg.Gateway.EmbedModel)
	if err := vi.Upsert(ctx, identity.Scope{Tenant: episodesTenant}, "01NARRAAAAAAAAAAAAAAAAAAAA", emb.Vectors[0]); err != nil {
		t.Fatalf("seed vectors: upsert: %v", err)
	}
}

func episodesEmbedded(t *testing.T, cfg config.Config, req stowage.EpisodesRequest) stowage.EpisodesResponse {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(episodesTenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	}()
	resp, err := client.Episodes(ctx, req)
	if err != nil {
		t.Fatalf("embedded Episodes: %v", err)
	}
	return resp
}

func episodesHTTP(t *testing.T, cfg config.Config, req stowage.EpisodesRequest) stowage.EpisodesResponse {
	t.Helper()
	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetStage(p.Stage)
	srv.SetRetriever(stk.Retriever)
	ts := httptest.NewServer(srv)
	defer func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	}()
	key, plaintext, err := auth.Generate(episodesTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	client := stowage.NewHTTP(ts.URL, plaintext)
	resp, err := client.Episodes(ctx, req)
	if err != nil {
		t.Fatalf("http Episodes: %v", err)
	}
	return resp
}

func episodesMCP(t *testing.T, cfg config.Config, in mcpserver.EpisodesInput) stowage.EpisodesResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(episodesTenant), Profile: cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	defer func() {
		shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	}()
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "episodes-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_episodes", Arguments: in})
	if err != nil {
		t.Fatalf("CallTool memory_episodes: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_episodes returned IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var resp stowage.EpisodesResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("remap memory_episodes → SDK: %v", err)
	}
	return resp
}

// TestEpisodesParity_AllSurfaces is the D-080 all-surfaces-identical bar.
func TestEpisodesParity_AllSurfaces(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedEpisodes(t, cfg)

	emb := episodesEmbedded(t, cfg, stowage.EpisodesRequest{})
	htp := episodesHTTP(t, cfg, stowage.EpisodesRequest{})
	mcp := episodesMCP(t, cfg, mcpserver.EpisodesInput{})

	embJSON := canonicalJSON(t, emb)
	if embJSON != canonicalJSON(t, htp) {
		t.Errorf("embedded vs HTTP diverge:\n embedded=%s\n     http=%s", embJSON, canonicalJSON(t, htp))
	}
	if embJSON != canonicalJSON(t, mcp) {
		t.Errorf("embedded vs MCP diverge:\n embedded=%s\n      mcp=%s", embJSON, canonicalJSON(t, mcp))
	}

	// Non-trivially correct: 2 episodes, most-recent-first, e1 carries its narrative.
	if len(emb.Episodes) != 2 {
		t.Fatalf("expected 2 episodes, got %d: %+v", len(emb.Episodes), emb.Episodes)
	}
	if emb.Episodes[0].ID != "01EPTWOAAAAAAAAAAAAAAAAAAA" || emb.Episodes[1].ID != "01EPONEAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("episode order wrong: %+v", emb.Episodes)
	}
	if emb.Episodes[1].Narrative != "Planned and shipped the launch under a lock." {
		t.Errorf("e1 narrative not attached: %q", emb.Episodes[1].Narrative)
	}
	if emb.Episodes[1].Outcome != "success" || emb.Episodes[0].Outcome != "failure" {
		t.Errorf("outcomes wrong: %+v", emb.Episodes)
	}
}

// TestEpisodesParity_Similar is the Phase-23b similar-episode contrast parity bar
// (D-082): a seeded similar_to query is BYTE IDENTICAL across embedded/HTTP/MCP
// (deterministic mock embedder) and ranks e1 (the narrated, embedded episode) first.
func TestEpisodesParity_Similar(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedEpisodes(t, cfg)
	seedEpisodeVectors(t, cfg)

	const query = "Planned and shipped the launch under a lock."
	emb := episodesEmbedded(t, cfg, stowage.EpisodesRequest{SimilarTo: query, K: 5})
	htp := episodesHTTP(t, cfg, stowage.EpisodesRequest{SimilarTo: query, K: 5})
	mcp := episodesMCP(t, cfg, mcpserver.EpisodesInput{SimilarTo: query, K: 5})

	embJSON := canonicalJSON(t, emb)
	if embJSON != canonicalJSON(t, htp) {
		t.Errorf("similar: embedded vs HTTP diverge:\n embedded=%s\n     http=%s", embJSON, canonicalJSON(t, htp))
	}
	if embJSON != canonicalJSON(t, mcp) {
		t.Errorf("similar: embedded vs MCP diverge:\n embedded=%s\n      mcp=%s", embJSON, canonicalJSON(t, mcp))
	}

	// Non-trivially correct: e1 (the embedded, narrated episode) ranks first with a
	// score; e2 has no narrative vector so does not appear.
	if len(emb.Episodes) == 0 {
		t.Fatalf("similar: expected ≥1 episode, got 0")
	}
	if emb.Episodes[0].ID != "01EPONEAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("similar: expected e1 ranked first, got %+v", emb.Episodes)
	}
	if emb.Episodes[0].Score <= 0 {
		t.Errorf("similar: expected a positive similarity score, got %v", emb.Episodes[0].Score)
	}
	if emb.Episodes[0].Narrative != "Planned and shipped the launch under a lock." {
		t.Errorf("similar: e1 narrative not attached: %q", emb.Episodes[0].Narrative)
	}
}

// TestEpisodesParity_GetMissing pins the get-not-found contract across surfaces
// (D-067): a missing id returns an empty list with no error on ALL THREE — `id`
// is a filter on /v1/episodes, not a REST resource path (adversarial-review fix).
func TestEpisodesParity_GetMissing(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedEpisodes(t, cfg)
	const missing = "01MISSINGAAAAAAAAAAAAAAAAA"

	emb := episodesEmbedded(t, cfg, stowage.EpisodesRequest{ID: missing})
	htp := episodesHTTP(t, cfg, stowage.EpisodesRequest{ID: missing})
	mcp := episodesMCP(t, cfg, mcpserver.EpisodesInput{ID: missing})

	for name, r := range map[string]stowage.EpisodesResponse{"embedded": emb, "http": htp, "mcp": mcp} {
		if len(r.Episodes) != 0 {
			t.Errorf("%s: missing id should yield an empty list, got %d", name, len(r.Episodes))
		}
	}
	if canonicalJSON(t, emb) != canonicalJSON(t, htp) || canonicalJSON(t, emb) != canonicalJSON(t, mcp) {
		t.Errorf("missing-id parity diverged:\n emb=%s\n htp=%s\n mcp=%s", canonicalJSON(t, emb), canonicalJSON(t, htp), canonicalJSON(t, mcp))
	}
}
