// http_mcp_scope_parity_test.go proves ae2b's D-140 behavioural (not
// contractual) parity bar (§17): the SAME seeded identity, asserted via MCP
// _meta.user/_meta.project (ae2b — the project_id/user_id args are gone from
// the 13 MCP contracts) and via HTTP's UNCHANGED ?user_id=/?project_id=
// query-string and JSON-body projection, resolves to the SAME effective scope
// and the SAME store rows — for retrieve (POST body identity) and get (GET
// query-param identity), the two representative HTTP identity-projection
// shapes among the 13 tools' HTTP mirror endpoints. This is the phase's own
// AC-5 test: MCP moved to _meta-only, HTTP did not move at all (D-140), and
// the two surfaces still agree on WHAT resolves, even though HOW they carry
// it now differs. Runs under -race.
package integration

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

const httpMCPParityTenant = "ae2b-http-mcp-parity"

// seedHTTPMCPParity inserts one active memory per user (u1, u2), directly via
// the store, retrievable both lexically (for retrieve) and by ID (for get).
func seedHTTPMCPParity(t *testing.T, cfg config.Config) (u1MemID, u2MemID string) {
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
	now := time.Now().UnixMilli()
	u1MemID = ulid.Make().String()
	u2MemID = ulid.Make().String()
	for _, m := range []struct {
		scope identity.Scope
		id    string
	}{
		{identity.Scope{Tenant: httpMCPParityTenant, User: "u1"}, u1MemID},
		{identity.Scope{Tenant: httpMCPParityTenant, User: "u2"}, u2MemID},
	} {
		if err := st.Memories().Insert(ctx, m.scope, store.Memory{
			ID: m.id, Kind: "fact", Content: "http-mcp parity fixture qhmp " + m.scope.User, Status: "active",
			Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
			ContentHash: ulid.Make().String(), CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed memory %s: %v", m.id, err)
		}
	}
	return u1MemID, u2MemID
}

// httpMCPParityHTTPClient boots an HTTP api.Server over cfg's store and
// returns an authenticated stowage.Client for httpMCPParityTenant.
func httpMCPParityHTTPClient(t *testing.T, cfg config.Config) stowage.Client {
	t.Helper()
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
	key, plaintext, err := auth.Generate(httpMCPParityTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	return stowage.NewHTTP(ts.URL, plaintext)
}

// httpMCPParityMCPCall boots a fresh MCP server bound to httpMCPParityTenant
// and calls memory_retrieve or memory_get with the given _meta identity.
func httpMCPParityMCPCall(t *testing.T, cfg config.Config, name string, in any, meta map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(httpMCPParityTenant), Profile: cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "http-mcp-parity-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	params := &mcpsdk.CallToolParams{Name: name, Arguments: in}
	if len(meta) > 0 {
		params.Meta = mcpsdk.Meta(meta)
	}
	res, cerr := session.CallTool(ctx, params)
	if cerr != nil {
		t.Fatalf("CallTool %s: %v", name, cerr)
	}
	return res
}

// TestHTTPMCPScopeParity_Retrieve is the D-140 behavioural parity bar for the
// POST-body HTTP identity shape: an HTTP retrieve narrowed via
// ?user_id=/body user_id and an MCP retrieve narrowed via _meta.user resolve
// to the SAME rows (u1's memory, never u2's).
func TestHTTPMCPScopeParity_Retrieve(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	u1MemID, u2MemID := seedHTTPMCPParity(t, cfg)

	htp := httpMCPParityHTTPClient(t, cfg)
	ctx := context.Background()
	httpResp, err := htp.Retrieve(ctx, stowage.RetrieveRequest{Query: "qhmp u1", Limit: 10, UserID: "u1"})
	if err != nil {
		t.Fatalf("http Retrieve: %v", err)
	}

	mcpRes := httpMCPParityMCPCall(t, cfg, "memory_retrieve",
		mcpserver.RetrieveInput{Query: "qhmp u1", Limit: 10}, map[string]any{"user": "u1"})
	if mcpRes.IsError {
		t.Fatalf("mcp memory_retrieve returned IsError: %+v", mcpRes.Content)
	}
	var mcpOut mcpserver.RetrieveOutput
	decodeStructured(t, mcpRes, &mcpOut)

	httpIDs := map[string]bool{}
	for _, it := range httpResp.Items {
		httpIDs[it.ID] = true
	}
	mcpIDs := map[string]bool{}
	for _, it := range mcpOut.Items {
		mcpIDs[it.ID] = true
	}

	if !httpIDs[u1MemID] || httpIDs[u2MemID] {
		t.Errorf("http ?user_id=u1 (body): expected only u1's memory, got %v", httpIDs)
	}
	if !mcpIDs[u1MemID] || mcpIDs[u2MemID] {
		t.Errorf("mcp _meta.user=u1: expected only u1's memory, got %v", mcpIDs)
	}
	if len(httpIDs) != len(mcpIDs) {
		t.Errorf("http vs mcp effective scope diverged: http=%v mcp=%v", httpIDs, mcpIDs)
	}
	for id := range httpIDs {
		if !mcpIDs[id] {
			t.Errorf("http vs mcp row-set diverged: http has %s, mcp does not (http=%v mcp=%v)", id, httpIDs, mcpIDs)
		}
	}
}

// TestHTTPMCPScopeParity_Get is the D-140 behavioural parity bar for the
// GET-query-param HTTP identity shape: an HTTP get narrowed via the client's
// ?user_id= construction scope and an MCP get narrowed via _meta.user resolve
// to the SAME memory for its owner, and BOTH refuse a non-owner (the P3
// failure mode, on both surfaces).
func TestHTTPMCPScopeParity_Get(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	u1MemID, _ := seedHTTPMCPParity(t, cfg)
	ctx := context.Background()

	// Owner: HTTP (?user_id=u1 via construction scope) and MCP (_meta.user=u1)
	// both resolve u1's memory.
	htpOwner := httpMCPParityHTTPClientAs(t, cfg, "u1")
	httpGetResp, err := htpOwner.GetMemory(ctx, u1MemID)
	if err != nil {
		t.Fatalf("http GetMemory (owner): %v", err)
	}
	if httpGetResp.Memory.ID != u1MemID {
		t.Errorf("http ?user_id=u1: expected to resolve u1's memory, got %+v", httpGetResp.Memory)
	}

	mcpRes := httpMCPParityMCPCall(t, cfg, "memory_get", mcpserver.GetInput{MemoryID: u1MemID}, map[string]any{"user": "u1"})
	if mcpRes.IsError {
		t.Fatalf("mcp memory_get (owner) returned IsError: %+v", mcpRes.Content)
	}
	var mcpOut mcpserver.GetOutput
	decodeStructured(t, mcpRes, &mcpOut)
	if mcpOut.Memory.ID != u1MemID {
		t.Errorf("mcp _meta.user=u1: expected to resolve u1's memory, got %+v", mcpOut.Memory)
	}

	// Non-owner: HTTP (?user_id=u2) and MCP (_meta.user=u2) BOTH refuse.
	htpOther := httpMCPParityHTTPClientAs(t, cfg, "u2")
	if _, err := htpOther.GetMemory(ctx, u1MemID); err == nil {
		t.Error("P3 LEAK: http ?user_id=u2 resolved u1's memory via GetMemory")
	}
	mcpRes2 := httpMCPParityMCPCall(t, cfg, "memory_get", mcpserver.GetInput{MemoryID: u1MemID}, map[string]any{"user": "u2"})
	if !mcpRes2.IsError {
		t.Error("P3 LEAK: mcp _meta.user=u2 resolved u1's memory via memory_get")
	}
}

// httpMCPParityHTTPClientAs is httpMCPParityHTTPClient with a construction-scope
// user — GetMemory has no per-call user arg (D-125 documented exception), so
// the GET-query-param identity is carried via the client's WithUser option,
// which addScopeParams projects onto ?user_id= on every request.
func httpMCPParityHTTPClientAs(t *testing.T, cfg config.Config, user string) stowage.Client {
	t.Helper()
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
	key, plaintext, err := auth.Generate(httpMCPParityTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	return stowage.NewHTTP(ts.URL, plaintext, stowage.WithUser(user))
}
