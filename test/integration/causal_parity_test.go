// causal_parity_test.go proves the Phase-24 (D-083) all-surfaces-identical bar: the
// deterministic, gateway-free causal why-traversal is BYTE IDENTICAL through the
// embedded SDK, the HTTP server, and the MCP tool, over one shared sqlite store seeded
// with two decision memories and an inferred led_to edge. Runs under -race.
package integration

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

const causalTenant = "p24-causal"

// seedCausal seeds A led_to B (inferred) so a backward traversal from B reaches A.
func seedCausal(t *testing.T, cfg config.Config) {
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
	scope := identity.Scope{Tenant: causalTenant}
	for _, m := range []store.Memory{
		{ID: "01CAUSEAAAAAAAAAAAAAAAAAAA", Kind: "decision", Content: "Decided to deploy v2.", Context: "deploy",
			Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, EpisodeID: "01EPCAUSEAAAAAAAAAAAAAAAAA", CreatedAt: 1_000_000, UpdatedAt: 1_000_000},
		{ID: "01EFFECTAAAAAAAAAAAAAAAAAA", Kind: "decision", Content: "Enabled the deploy lock.", Context: "deploy",
			Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, EpisodeID: "01EPCAUSEAAAAAAAAAAAAAAAAA", CreatedAt: 1_000_100, UpdatedAt: 1_000_100},
	} {
		if err := st.Memories().Insert(ctx, scope, m); err != nil {
			t.Fatalf("seed memory %s: %v", m.ID, err)
		}
	}
	if err := st.Memories().InsertLinks(ctx, scope, []store.Link{{
		ID: "01LINKCAUSALAAAAAAAAAAAAAA", TenantID: causalTenant,
		FromMemory: "01CAUSEAAAAAAAAAAAAAAAAAAA", ToMemory: "01EFFECTAAAAAAAAAAAAAAAAAA",
		Type: "led_to", Source: "inferred", Confidence: 0.9, CreatedAt: 1_000_200,
	}}); err != nil {
		t.Fatalf("seed link: %v", err)
	}
}

func causalEmbedded(t *testing.T, cfg config.Config, req stowage.CausalRequest) stowage.CausalResponse {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(causalTenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	}()
	resp, err := client.Causal(ctx, req)
	if err != nil {
		t.Fatalf("embedded Causal: %v", err)
	}
	return resp
}

func causalHTTP(t *testing.T, cfg config.Config, req stowage.CausalRequest) stowage.CausalResponse {
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
	key, plaintext, err := auth.Generate(causalTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	client := stowage.NewHTTP(ts.URL, plaintext)
	resp, err := client.Causal(ctx, req)
	if err != nil {
		t.Fatalf("http Causal: %v", err)
	}
	return resp
}

func causalMCP(t *testing.T, cfg config.Config, in mcpserver.CausalInput) stowage.CausalResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(causalTenant), Profile: cfg.Profile,
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "causal-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_causal", Arguments: in})
	if err != nil {
		t.Fatalf("CallTool memory_causal: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_causal returned IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var resp stowage.CausalResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("remap memory_causal → SDK: %v", err)
	}
	return resp
}

// TestCausalParity_AllSurfaces is the D-083 all-surfaces-identical bar.
func TestCausalParity_AllSurfaces(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedCausal(t, cfg)

	// Backward from the effect reaches the cause.
	req := stowage.CausalRequest{MemoryID: "01EFFECTAAAAAAAAAAAAAAAAAA", Direction: "backward", Depth: 3}
	emb := causalEmbedded(t, cfg, req)
	htp := causalHTTP(t, cfg, req)
	mcp := causalMCP(t, cfg, mcpserver.CausalInput{MemoryID: "01EFFECTAAAAAAAAAAAAAAAAAA", Direction: "backward", Depth: 3})

	embJSON := canonicalJSON(t, emb)
	if embJSON != canonicalJSON(t, htp) {
		t.Errorf("embedded vs HTTP diverge:\n embedded=%s\n     http=%s", embJSON, canonicalJSON(t, htp))
	}
	if embJSON != canonicalJSON(t, mcp) {
		t.Errorf("embedded vs MCP diverge:\n embedded=%s\n      mcp=%s", embJSON, canonicalJSON(t, mcp))
	}

	// Non-trivially correct: 2 nodes, 1 led_to edge cause→effect.
	if len(emb.Nodes) != 2 || len(emb.Edges) != 1 {
		t.Fatalf("expected 2 nodes + 1 edge, got %d nodes %d edges: %+v", len(emb.Nodes), len(emb.Edges), emb)
	}
	if emb.Edges[0].From != "01CAUSEAAAAAAAAAAAAAAAAAAA" || emb.Edges[0].To != "01EFFECTAAAAAAAAAAAAAAAAAA" || emb.Edges[0].Type != "led_to" {
		t.Errorf("edge wrong: %+v", emb.Edges[0])
	}
	if emb.Root != "01EFFECTAAAAAAAAAAAAAAAAAA" {
		t.Errorf("root wrong: %q", emb.Root)
	}
}

// TestCausalParity_MissingRoot pins the missing-root contract across surfaces
// (D-067): an unknown root returns an empty graph with no error on ALL THREE.
func TestCausalParity_MissingRoot(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedCausal(t, cfg)
	const missing = "01MISSINGAAAAAAAAAAAAAAAAA"

	emb := causalEmbedded(t, cfg, stowage.CausalRequest{MemoryID: missing})
	htp := causalHTTP(t, cfg, stowage.CausalRequest{MemoryID: missing})
	mcp := causalMCP(t, cfg, mcpserver.CausalInput{MemoryID: missing})

	for name, r := range map[string]stowage.CausalResponse{"embedded": emb, "http": htp, "mcp": mcp} {
		if len(r.Nodes) != 0 || len(r.Edges) != 0 {
			t.Errorf("%s: missing root should yield an empty graph, got %d nodes", name, len(r.Nodes))
		}
	}
	if canonicalJSON(t, emb) != canonicalJSON(t, htp) || canonicalJSON(t, emb) != canonicalJSON(t, mcp) {
		t.Errorf("missing-root parity diverged:\n emb=%s\n htp=%s\n mcp=%s", canonicalJSON(t, emb), canonicalJSON(t, htp), canonicalJSON(t, mcp))
	}
}
