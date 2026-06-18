// verify_parity_test.go proves the Phase-25 (D-084) memory_verify all-surfaces bar:
// the claim-verification wiring (resolve citations → schema-constrained gateway
// entailment) is BYTE IDENTICAL through the embedded SDK, the HTTP server, and the MCP
// tool, over one shared sqlite store + the deterministic mock gateway. Runs under -race.
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

const verifyTenant = "p25-verify"

// seedVerify inserts a memory + a citation handle (injection) pointing at it.
func seedVerify(t *testing.T, cfg config.Config) {
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
	scope := identity.Scope{Tenant: verifyTenant}
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: "01VMEMAAAAAAAAAAAAAAAAAAAA", Kind: "fact", Content: "Paris is the capital of France.",
		Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: 1_000_000, UpdatedAt: 1_000_000,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	if err := st.Injections().Append(ctx, scope, []store.Injection{{
		ID: "01VCITAAAAAAAAAAAAAAAAAAAA", ResponseID: "resp-1", MemoryID: "01VMEMAAAAAAAAAAAAAAAAAAAA",
		Rank: 0, Score: 0.9, CreatedAt: 1_000_000,
	}}); err != nil {
		t.Fatalf("seed injection: %v", err)
	}
}

func verifyEmbedded(t *testing.T, cfg config.Config, req stowage.VerifyRequest) stowage.VerifyResponse {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(verifyTenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	}()
	resp, err := client.Verify(ctx, req)
	if err != nil {
		t.Fatalf("embedded Verify: %v", err)
	}
	return resp
}

func verifyHTTP(t *testing.T, cfg config.Config, req stowage.VerifyRequest) stowage.VerifyResponse {
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
	srv.SetGateway(stk.Gateway)
	ts := httptest.NewServer(srv)
	defer func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	}()
	key, plaintext, err := auth.Generate(verifyTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	client := stowage.NewHTTP(ts.URL, plaintext)
	resp, err := client.Verify(ctx, req)
	if err != nil {
		t.Fatalf("http Verify: %v", err)
	}
	return resp
}

func verifyMCP(t *testing.T, cfg config.Config, in mcpserver.VerifyInput) stowage.VerifyResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, Gateway: stk.Gateway, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(verifyTenant), Profile: cfg.Profile,
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "verify-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_verify", Arguments: in})
	if err != nil {
		t.Fatalf("CallTool memory_verify: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_verify returned IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var resp stowage.VerifyResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("remap memory_verify → SDK: %v", err)
	}
	return resp
}

// TestVerifyParity_AllSurfaces is the D-084 verify all-surfaces-identical bar. The
// mock gateway returns a deterministic verdict, so the resolve→entailment wiring is
// byte-identical across embedded/HTTP/MCP.
func TestVerifyParity_AllSurfaces(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedVerify(t, cfg)

	emb := verifyEmbedded(t, cfg, stowage.VerifyRequest{Claim: "Paris is the capital of France.", Citations: []string{"01VCITAAAAAAAAAAAAAAAAAAAA"}})
	htp := verifyHTTP(t, cfg, stowage.VerifyRequest{Claim: "Paris is the capital of France.", Citations: []string{"01VCITAAAAAAAAAAAAAAAAAAAA"}})
	mcp := verifyMCP(t, cfg, mcpserver.VerifyInput{Claim: "Paris is the capital of France.", Citations: []string{"01VCITAAAAAAAAAAAAAAAAAAAA"}})

	embJSON := canonicalJSON(t, emb)
	if embJSON != canonicalJSON(t, htp) {
		t.Errorf("verify: embedded vs HTTP diverge:\n embedded=%s\n     http=%s", embJSON, canonicalJSON(t, htp))
	}
	if embJSON != canonicalJSON(t, mcp) {
		t.Errorf("verify: embedded vs MCP diverge:\n embedded=%s\n      mcp=%s", embJSON, canonicalJSON(t, mcp))
	}
	// The gateway WAS reached (citation resolved → not the empty short-circuit), so the
	// verdict is a real value and not degraded.
	if emb.Degraded {
		t.Errorf("gateway present ⇒ not degraded, got %+v", emb)
	}
	if emb.Verdict != "entailed" && emb.Verdict != "not_entailed" && emb.Verdict != "unclear" {
		t.Errorf("unexpected verdict: %q", emb.Verdict)
	}
}
