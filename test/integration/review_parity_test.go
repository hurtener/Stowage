// review_parity_test.go proves the Phase-25 (D-084) memory_review list bar: the
// review-queue list is BYTE IDENTICAL through the embedded SDK, the HTTP server, and
// the MCP tool over one shared sqlite store (list is read-only; approve/reject mutate
// and are covered by internal/trust unit tests). Runs under -race.
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

const reviewTenant = "p25-review"

// seedReview inserts two pending_review memories.
func seedReview(t *testing.T, cfg config.Config) {
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
	scope := identity.Scope{Tenant: reviewTenant}
	for _, m := range []store.Memory{
		{ID: "01RMEMONEAAAAAAAAAAAAAAAAA", Kind: "fact", Content: "An uncited claim A.", Status: "pending_review",
			Confidence: 0.5, TrustSource: "asserted", Stability: 1.0, CreatedAt: 1_000_000, UpdatedAt: 1_000_000},
		{ID: "01RMEMTWOAAAAAAAAAAAAAAAAA", Kind: "fact", Content: "An uncited claim B.", Status: "pending_review",
			Confidence: 0.5, TrustSource: "asserted", Stability: 1.0, CreatedAt: 1_000_100, UpdatedAt: 1_000_100},
		// A non-pending memory that must NOT appear in the queue.
		{ID: "01RMEMACTAAAAAAAAAAAAAAAAA", Kind: "fact", Content: "An active memory.", Status: "active",
			Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1_000_200, UpdatedAt: 1_000_200},
	} {
		if err := st.Memories().Insert(ctx, scope, m); err != nil {
			t.Fatalf("seed memory %s: %v", m.ID, err)
		}
	}
}

func reviewEmbedded(t *testing.T, cfg config.Config, req stowage.ReviewRequest) stowage.ReviewResponse {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(reviewTenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	}()
	resp, err := client.Review(ctx, req)
	if err != nil {
		t.Fatalf("embedded Review: %v", err)
	}
	return resp
}

func reviewHTTP(t *testing.T, cfg config.Config, req stowage.ReviewRequest) stowage.ReviewResponse {
	t.Helper()
	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
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
	key, plaintext, err := auth.Generate(reviewTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	client := stowage.NewHTTP(ts.URL, plaintext)
	resp, err := client.Review(ctx, req)
	if err != nil {
		t.Fatalf("http Review: %v", err)
	}
	return resp
}

func reviewMCP(t *testing.T, cfg config.Config, in mcpserver.ReviewInput) stowage.ReviewResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(reviewTenant), Profile: cfg.Profile,
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "review-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_review", Arguments: in})
	if err != nil {
		t.Fatalf("CallTool memory_review: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_review returned IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var resp stowage.ReviewResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("remap memory_review → SDK: %v", err)
	}
	return resp
}

// TestReviewParity_List is the D-084 review-list all-surfaces-identical bar.
func TestReviewParity_List(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedReview(t, cfg)

	emb := reviewEmbedded(t, cfg, stowage.ReviewRequest{Action: "list"})
	htp := reviewHTTP(t, cfg, stowage.ReviewRequest{Action: "list"})
	mcp := reviewMCP(t, cfg, mcpserver.ReviewInput{Action: "list"})

	embJSON := canonicalJSON(t, emb)
	if embJSON != canonicalJSON(t, htp) {
		t.Errorf("review: embedded vs HTTP diverge:\n embedded=%s\n     http=%s", embJSON, canonicalJSON(t, htp))
	}
	if embJSON != canonicalJSON(t, mcp) {
		t.Errorf("review: embedded vs MCP diverge:\n embedded=%s\n      mcp=%s", embJSON, canonicalJSON(t, mcp))
	}
	// Non-trivially correct: exactly the two pending_review memories, oldest-first
	// (FIFO — the store's ListByStatus order); the active memory is excluded.
	if len(emb.Items) != 2 {
		t.Fatalf("expected 2 pending items, got %d: %+v", len(emb.Items), emb.Items)
	}
	if emb.Items[0].ID != "01RMEMONEAAAAAAAAAAAAAAAAA" || emb.Items[1].ID != "01RMEMTWOAAAAAAAAAAAAAAAAA" {
		t.Errorf("review list order wrong: %+v", emb.Items)
	}
}

// TestReviewParity_Empty pins the empty-queue contract across surfaces (D-067): an
// empty review queue is identical on embedded/HTTP/MCP (the items tag is omitempty on
// all three, so the empty case can't diverge).
func TestReviewParity_Empty(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	// No seed → empty queue.
	emb := reviewEmbedded(t, cfg, stowage.ReviewRequest{Action: "list"})
	htp := reviewHTTP(t, cfg, stowage.ReviewRequest{Action: "list"})
	mcp := reviewMCP(t, cfg, mcpserver.ReviewInput{Action: "list"})

	if len(emb.Items) != 0 || len(htp.Items) != 0 || len(mcp.Items) != 0 {
		t.Fatalf("empty queue should yield no items: emb=%d htp=%d mcp=%d", len(emb.Items), len(htp.Items), len(mcp.Items))
	}
	if canonicalJSON(t, emb) != canonicalJSON(t, htp) || canonicalJSON(t, emb) != canonicalJSON(t, mcp) {
		t.Errorf("empty-queue parity diverged:\n emb=%s\n htp=%s\n mcp=%s", canonicalJSON(t, emb), canonicalJSON(t, htp), canonicalJSON(t, mcp))
	}
}
