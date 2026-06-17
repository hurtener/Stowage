// playbook_parity_test.go proves the Phase h5 (D-072) all-surfaces-identical
// bar: the deterministic, LLM-free playbook assembled for one scope is BYTE
// IDENTICAL whether read through the embedded SDK, the HTTP server, or the MCP
// tool — over a real sqlite store shared by all three. Runs under -race.
//
// The three legs run sequentially against ONE shared sqlite DSN seeded with
// fixed-ULID memories, so the memory_ids, scores, ordering, and provenance are
// pinned and any cross-surface divergence is a real bug (not random IDs). Scores
// are stable across legs because the seeded memories have LastAccessedAt=0 (decay
// term = 1.0 regardless of wall-clock), so the assembled JSON does not depend on
// when each leg runs.
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

const playbookTenant = "h5-playbook"

// seedPlaybookMemories inserts a fixed, deterministic set of active
// strategy/failure_mode/gotcha memories (plus one off-kind fact that must be
// excluded) into a store opened on cfg's DSN, then closes it.
func seedPlaybookMemories(t *testing.T, cfg config.Config) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("seed: open store: %v", err)
	}
	defer func() { _ = st.Close(ctx) }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("seed: migrate: %v", err)
	}
	scope := identity.Scope{Tenant: playbookTenant}

	type seed struct {
		id, kind, content string
		use, save, noise  int64
	}
	seeds := []seed{
		{"01AAAAAAAAAAAAAAAAAAAAAAAA", "strategy", "Always write a failing test first.", 12, 6, 0},
		{"01BBBBBBBBBBBBBBBBBBBBBBBB", "strategy", "Prefer composition over inheritance.", 3, 0, 1},
		{"01CCCCCCCCCCCCCCCCCCCCCCCC", "failure_mode", "Never panic across the API boundary.", 5, 1, 0},
		{"01DDDDDDDDDDDDDDDDDDDDDDDD", "gotcha", "Keep the sqlite driver CGo-free.", 2, 0, 0},
		{"01EEEEEEEEEEEEEEEEEEEEEEEE", "fact", "This off-kind fact must be excluded.", 9, 9, 0},
	}
	for _, s := range seeds {
		if err := st.Memories().Insert(ctx, scope, store.Memory{
			ID: s.id, Kind: s.kind, Content: s.content, Status: "active",
			Importance: 3, Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
			UseCount: s.use, SaveCount: s.save, NoiseCount: s.noise,
			CreatedAt: 1_000_000, UpdatedAt: 1_000_000,
		}); err != nil {
			t.Fatalf("seed insert %s: %v", s.id, err)
		}
	}
}

// playbookConfig returns a shared-DSN config with a fixed profile so every leg
// resolves the same profile-internal token budget (D-042).
func playbookConfig(t *testing.T) config.Config {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	return cfg
}

func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func playbookEmbedded(t *testing.T, cfg config.Config) stowage.PlaybookResponse {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(playbookTenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	}()
	resp, err := client.Playbook(ctx, stowage.PlaybookRequest{})
	if err != nil {
		t.Fatalf("embedded Playbook: %v", err)
	}
	return resp
}

func playbookHTTP(t *testing.T, cfg config.Config) stowage.PlaybookResponse {
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

	key, plaintext, err := auth.Generate(playbookTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	client := stowage.NewHTTP(ts.URL, plaintext)
	resp, err := client.Playbook(ctx, stowage.PlaybookRequest{})
	if err != nil {
		t.Fatalf("http Playbook: %v", err)
	}
	return resp
}

// playbookMCP assembles via the in-process MCP transport and maps the tool
// output back onto the SDK response type for an apples-to-apples comparison.
func playbookMCP(t *testing.T, cfg config.Config) stowage.PlaybookResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store:      stk.Store,
		Retriever:  stk.Retriever,
		TopicSvc:   stk.TopicSvc,
		PipelineIn: p.In,
		Log:        stk.Log,
		ScopeFn:    mcpserver.StdioScopeFn(playbookTenant),
		Profile:    cfg.Profile,
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "playbook-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_playbook", Arguments: mcpserver.PlaybookInput{}})
	if err != nil {
		t.Fatalf("CallTool memory_playbook: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_playbook returned IsError: %+v", res.Content)
	}
	var out mcpserver.PlaybookOutput
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode memory_playbook: %v", err)
	}
	// The MCP and SDK wire shapes are byte-identical; re-decode through the SDK
	// type so the comparison is on one canonical representation.
	var resp stowage.PlaybookResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("remap memory_playbook → SDK: %v", err)
	}
	return resp
}

// TestPlaybookParity_AllSurfaces is the AC-5 all-surfaces-identical bar.
func TestPlaybookParity_AllSurfaces(t *testing.T) {
	cfg := playbookConfig(t)
	seedPlaybookMemories(t, cfg)

	// Sequential legs against the shared DSN (no concurrent sqlite handles).
	emb := playbookEmbedded(t, cfg)
	htp := playbookHTTP(t, cfg)
	mcp := playbookMCP(t, cfg)

	embJSON := canonicalJSON(t, emb)
	htpJSON := canonicalJSON(t, htp)
	mcpJSON := canonicalJSON(t, mcp)

	if embJSON != htpJSON {
		t.Errorf("embedded vs HTTP diverge:\n embedded=%s\n     http=%s", embJSON, htpJSON)
	}
	if embJSON != mcpJSON {
		t.Errorf("embedded vs MCP diverge:\n embedded=%s\n      mcp=%s", embJSON, mcpJSON)
	}

	// And the playbook is the expected non-empty, sectioned result (not identically
	// broken): strategy → failure_mode → gotcha, fact excluded, budget populated.
	if len(emb.Sections) != 3 {
		t.Fatalf("expected 3 sections, got %d: %+v", len(emb.Sections), emb.Sections)
	}
	wantKinds := []string{"strategy", "failure_mode", "gotcha"}
	for i, k := range wantKinds {
		if emb.Sections[i].Kind != k {
			t.Errorf("section[%d].Kind = %q, want %q", i, emb.Sections[i].Kind, k)
		}
	}
	// Higher-utility strategy ranks first within its section (append-bias).
	strategySec := emb.Sections[0].Items
	if len(strategySec) != 2 || strategySec[0].MemoryID != "01AAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("strategy section mis-ranked: %+v", strategySec)
	}
	if emb.Budget.TokenBudget != 2000 || emb.Budget.ItemsPacked != 4 || emb.Budget.ItemsTotal != 4 {
		t.Errorf("budget unexpected: %+v", emb.Budget)
	}
}
