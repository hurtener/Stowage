// scope_parity_test.go proves the Phase-30 (D-125) per-user read scoping is enforced
// IDENTICALLY across the embedded SDK, the HTTP server, and the MCP tool over one shared
// sqlite store: a review-queue list scoped to user "alice" returns ONLY alice's pending_review
// memory, never bob's, on every surface — and the three responses are byte-identical. The
// review surface is the load-bearing case because its approve/reject MUTATES (a tenant peer must
// not resolve another user's pending memory). Runs under -race.
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

const scopeTenant = "p30-scope"

// aliceMemID / bobMemID are the two users' pending_review memories under one tenant.
const (
	aliceMemID = "01P30ALICEAAAAAAAAAAAAAAAA"
	bobMemID   = "01P30BOBAAAAAAAAAAAAAAAAAA"
)

// seedScopeParity inserts one pending_review memory for alice and one for bob, under the SAME
// tenant but distinct users — written user-scoped via the store (the post-B1 committed state).
func seedScopeParity(t *testing.T, cfg config.Config) {
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
	for _, m := range []struct {
		scope identity.Scope
		mem   store.Memory
	}{
		{identity.Scope{Tenant: scopeTenant, User: "alice"}, store.Memory{
			ID: aliceMemID, Kind: "fact", Content: "alice's uncited claim.", Status: "pending_review",
			Confidence: 0.5, TrustSource: "asserted", Stability: 1.0, CreatedAt: 1_000_000, UpdatedAt: 1_000_000}},
		{identity.Scope{Tenant: scopeTenant, User: "bob"}, store.Memory{
			ID: bobMemID, Kind: "fact", Content: "bob's uncited claim.", Status: "pending_review",
			Confidence: 0.5, TrustSource: "asserted", Stability: 1.0, CreatedAt: 1_000_100, UpdatedAt: 1_000_100}},
	} {
		if err := st.Memories().Insert(ctx, m.scope, m.mem); err != nil {
			t.Fatalf("seed memory %s: %v", m.mem.ID, err)
		}
	}
}

func scopeReviewEmbedded(t *testing.T, cfg config.Config, req stowage.ReviewRequest) stowage.ReviewResponse {
	t.Helper()
	ctx := context.Background()
	// Bind the embedded client to alice via the construction scope (WithUser) — the
	// natural single-user SDK model; per-call req.UserID would work identically.
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(scopeTenant), stowage.WithUser("alice"))
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

func scopeReviewHTTP(t *testing.T, cfg config.Config, req stowage.ReviewRequest) stowage.ReviewResponse {
	t.Helper()
	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetRetriever(stk.Retriever)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	key, plaintext, err := auth.Generate(scopeTenant, auth.RoleAgent)
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

// scopeReviewMCP calls memory_review, narrowing identity via _meta.user (ae2b,
// D-140/M1) — ReviewInput no longer carries a UserID arg, so the caller's
// user is asserted the same way every other MCP identity dimension is:
// through the host-injected _meta, never a wire argument.
func scopeReviewMCP(t *testing.T, cfg config.Config, in mcpserver.ReviewInput, user string) stowage.ReviewResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(scopeTenant), Profile: cfg.Profile,
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "scope-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	params := &mcpsdk.CallToolParams{Name: "memory_review", Arguments: in}
	if user != "" {
		params.Meta = mcpsdk.Meta(map[string]any{"user": user})
	}
	res, err := session.CallTool(ctx, params)
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

// TestScopeParity_ReviewList_AllSurfaces is the Phase-30 D-125 read-isolation parity bar: an
// alice-scoped review list returns alice's pending memory and ONLY alice's — never bob's — and
// the embedded SDK, HTTP, and MCP surfaces are byte-identical (D-067 parity). This is the AC#3
// guard the dual reviews required (the prior smoke only grepped source).
func TestScopeParity_ReviewList_AllSurfaces(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedScopeParity(t, cfg)

	emb := scopeReviewEmbedded(t, cfg, stowage.ReviewRequest{Action: "list", UserID: "alice"})
	htp := scopeReviewHTTP(t, cfg, stowage.ReviewRequest{Action: "list", UserID: "alice"})
	mcp := scopeReviewMCP(t, cfg, mcpserver.ReviewInput{Action: "list"}, "alice")

	// Each surface returns exactly alice's memory, never bob's.
	for name, resp := range map[string]stowage.ReviewResponse{"embedded": emb, "http": htp, "mcp": mcp} {
		var sawAlice, sawBob bool
		for _, it := range resp.Items {
			switch it.ID {
			case aliceMemID:
				sawAlice = true
			case bobMemID:
				sawBob = true
			}
		}
		if !sawAlice {
			t.Errorf("%s: alice-scoped review must list alice's pending memory", name)
		}
		if sawBob {
			t.Errorf("%s: P3 LEAK — alice-scoped review listed bob's memory %q", name, bobMemID)
		}
	}

	// And the three surfaces agree byte-for-byte.
	embJSON := canonicalJSON(t, emb)
	if h := canonicalJSON(t, htp); h != embJSON {
		t.Errorf("review scope parity: embedded vs HTTP diverge:\n embedded=%s\n     http=%s", embJSON, h)
	}
	if m := canonicalJSON(t, mcp); m != embJSON {
		t.Errorf("review scope parity: embedded vs MCP diverge:\n embedded=%s\n      mcp=%s", embJSON, m)
	}
}

// TestScopeParity_HTTPConstructionScope guards the B-1 fix: an HTTP client constructed with
// WithUser("alice") and issuing a review list with NO per-call user must still scope to alice
// (return alice's memory, never bob's). Before the fix WithProject/WithUser were silently dropped
// on the HTTP client → a tenant-wide read, a silent isolation failure on the network surface.
func TestScopeParity_HTTPConstructionScope(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedScopeParity(t, cfg)

	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetRetriever(stk.Retriever)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	key, plaintext, err := auth.Generate(scopeTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	// Construction scope only — NO per-call UserID.
	client := stowage.NewHTTP(ts.URL, plaintext, stowage.WithUser("alice"))
	resp, err := client.Review(ctx, stowage.ReviewRequest{Action: "list"})
	if err != nil {
		t.Fatalf("http Review (construction scope): %v", err)
	}
	var sawAlice, sawBob bool
	for _, it := range resp.Items {
		switch it.ID {
		case aliceMemID:
			sawAlice = true
		case bobMemID:
			sawBob = true
		}
	}
	if !sawAlice {
		t.Error("HTTP WithUser(alice): must list alice's memory")
	}
	if sawBob {
		t.Error("HTTP WithUser(alice) DROPPED (B-1): tenant-wide list surfaced bob's memory")
	}
}

// attemptApproveEmbedded/HTTP/MCP try to APPROVE the given memory id scoped to alice, returning
// the error (nil if it wrongly succeeded). Each opens its own stack over the shared seeded DSN.
func attemptApproveEmbedded(t *testing.T, cfg config.Config, memID string) error {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(scopeTenant), stowage.WithUser("alice"))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	}()
	_, e := client.Review(ctx, stowage.ReviewRequest{Action: "approve", MemoryID: memID, UserID: "alice"})
	return e
}

func attemptApproveHTTP(t *testing.T, cfg config.Config, memID string) error {
	t.Helper()
	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetRetriever(stk.Retriever)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	key, plaintext, err := auth.Generate(scopeTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	client := stowage.NewHTTP(ts.URL, plaintext)
	_, e := client.Review(ctx, stowage.ReviewRequest{Action: "approve", MemoryID: memID, UserID: "alice"})
	return e
}

func attemptApproveMCP(t *testing.T, cfg config.Config, memID string) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(scopeTenant), Profile: cfg.Profile,
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "scope-mutate-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "memory_review",
		Arguments: mcpserver.ReviewInput{Action: "approve", ID: memID},
		Meta:      mcpsdk.Meta(map[string]any{"user": "alice"}),
	})
	if err != nil {
		return err
	}
	if res.IsError {
		return errMCPTool // a tool-level error counts as a (correct) rejection
	}
	return nil
}

var errMCPTool = stubErr("mcp tool returned IsError")

type stubErr string

func (e stubErr) Error() string { return string(e) }

// TestScopeParity_ReviewResolve_CrossUserDenied is the Phase-30 D-125 MUTATE-isolation bar: an
// alice-scoped APPROVE of bob's pending_review memory must FAIL on every surface (the store
// narrows the resolve's WHERE to alice → bob's row is not found), and bob's memory must remain
// pending_review. This is the load-bearing case D-125/AC#3 names (review approve/reject MUTATES —
// a tenant peer must not resolve another user's memory); the prior pass left it untested.
func TestScopeParity_ReviewResolve_CrossUserDenied(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedScopeParity(t, cfg)

	for name, attempt := range map[string]func(*testing.T, config.Config, string) error{
		"embedded": attemptApproveEmbedded,
		"http":     attemptApproveHTTP,
		"mcp":      attemptApproveMCP,
	} {
		if err := attempt(t, cfg, bobMemID); err == nil {
			t.Errorf("%s: P3 MUTATE LEAK — alice approved bob's pending memory %q (must be denied)", name, bobMemID)
		}
	}

	// bob's memory must still be pending_review (unmutated) — read back via a side store.
	ctx := context.Background()
	side, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("side store open: %v", err)
	}
	defer func() { _ = side.Close(ctx) }()
	m, err := side.Memories().Get(ctx, identity.Scope{Tenant: scopeTenant, User: "bob"}, bobMemID)
	if err != nil {
		t.Fatalf("read back bob's memory: %v", err)
	}
	if m.Status != "pending_review" {
		t.Errorf("bob's memory status = %q, want pending_review (cross-user approve must not have mutated it)", m.Status)
	}
}
