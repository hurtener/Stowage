// trace_parity_test.go proves the Phase-26 (D-086) memory_trace all-surfaces bar: the
// reconstructed reasoning-trace CONTENT is BYTE IDENTICAL through the embedded SDK, the
// HTTP server, and the MCP tool over one shared sqlite store, and every surface signs
// with the same configured key. (generated_at + the signature over it are per-export,
// so the content comparison zeroes generated_at; signing is verified in unit tests.)
package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
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

const traceTenant = "p26-trace"

// seedTrace seeds a response: a record, a memory with provenance, an injection, and the
// captured query + verdict events (keyed by response_id).
func seedTrace(t *testing.T, cfg config.Config) {
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
	scope := identity.Scope{Tenant: traceTenant}
	_ = st.Records().Append(ctx, scope, []store.Record{{ID: "01TRECAAAAAAAAAAAAAAAAAAAA", Role: "user", Content: "What is the capital of France?", OccurredAt: 1_000_000, CreatedAt: 1_000_000}})
	_ = st.Memories().Insert(ctx, scope, store.Memory{ID: "01TMEMAAAAAAAAAAAAAAAAAAAA", Kind: "fact", Content: "Paris is the capital of France.", Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1_000_000, UpdatedAt: 1_000_000})
	_ = st.Memories().AddProvenance(ctx, scope, []store.Provenance{{ID: "01TPVAAAAAAAAAAAAAAAAAAAAA", MemoryID: "01TMEMAAAAAAAAAAAAAAAAAAAA", RecordID: "01TRECAAAAAAAAAAAAAAAAAAAA", SpanStart: 0, SpanEnd: 30, TenantID: traceTenant, CreatedAt: 1_000_000}})
	_ = st.Injections().Append(ctx, scope, []store.Injection{{ID: "01TINJAAAAAAAAAAAAAAAAAAAA", ResponseID: "resp-trace-1", MemoryID: "01TMEMAAAAAAAAAAAAAAAAAAAA", Rank: 0, Score: 0.9, Lane: "vector", WasCited: true, CreatedAt: 1_000_000}})
	_ = st.Events().Emit(ctx, scope, store.Event{ID: "01TEVQAAAAAAAAAAAAAAAAAAAA", TenantID: traceTenant, Type: "retrieve.query", SubjectID: "resp-trace-1", Payload: `{"query":"What is the capital of France?","support":"strong","degraded":false}`, CreatedAt: 1_000_000})
	_ = st.Events().Emit(ctx, scope, store.Event{ID: "01TEVVAAAAAAAAAAAAAAAAAAAA", TenantID: traceTenant, Type: "verify.verdict", SubjectID: "resp-trace-1", Payload: `{"claim":"Paris is the capital","verdict":"entailed","confidence":0.9}`, CreatedAt: 1_000_100})
}

// testTraceKey returns a fixed base64 ed25519 seed for deterministic signing in tests.
func testTraceKey(t *testing.T) string {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(seed)
}

func traceEmbedded(t *testing.T, cfg config.Config, req stowage.TraceRequest) stowage.TraceResponse {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(traceTenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	}()
	resp, err := client.Trace(ctx, req)
	if err != nil {
		t.Fatalf("embedded Trace: %v", err)
	}
	return resp
}

func traceHTTP(t *testing.T, cfg config.Config, req stowage.TraceRequest) stowage.TraceResponse {
	t.Helper()
	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetRetriever(stk.Retriever)
	srv.SetTraceSigner(stk.TraceSigner)
	ts := httptest.NewServer(srv)
	defer func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	}()
	key, plaintext, err := auth.Generate(traceTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	client := stowage.NewHTTP(ts.URL, plaintext)
	resp, err := client.Trace(ctx, req)
	if err != nil {
		t.Fatalf("http Trace: %v", err)
	}
	return resp
}

func traceMCP(t *testing.T, cfg config.Config, in mcpserver.TraceInput) stowage.TraceResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TraceSigner: stk.TraceSigner, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(traceTenant), Profile: cfg.Profile,
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "trace-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_trace", Arguments: in})
	if err != nil {
		t.Fatalf("CallTool memory_trace: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_trace returned IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var resp stowage.TraceResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("remap memory_trace → SDK: %v", err)
	}
	return resp
}

// TestTraceParity_AllSurfaces is the D-086 all-surfaces-identical bar. The reconstructed
// content is byte-identical across embedded/HTTP/MCP; generated_at (and the signature
// over it) are per-export and excluded from the content comparison.
func TestTraceParity_AllSurfaces(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	cfg.Trace.SigningKey = "env.STOWAGE_TEST_TRACE_KEY"
	t.Setenv("STOWAGE_TEST_TRACE_KEY", testTraceKey(t))
	seedTrace(t, cfg)

	req := stowage.TraceRequest{ResponseID: "resp-trace-1"}
	emb := traceEmbedded(t, cfg, req)
	htp := traceHTTP(t, cfg, req)
	mcp := traceMCP(t, cfg, mcpserver.TraceInput{ResponseID: "resp-trace-1"})

	// Content parity: zero the per-export generated_at, then compare the trace bodies.
	content := func(r stowage.TraceResponse) string {
		r.Trace.GeneratedAt = 0
		return canonicalJSON(t, r.Trace)
	}
	if content(emb) != content(htp) {
		t.Errorf("trace content embedded vs HTTP diverge:\n emb=%s\n htp=%s", content(emb), content(htp))
	}
	if content(emb) != content(mcp) {
		t.Errorf("trace content embedded vs MCP diverge:\n emb=%s\n mcp=%s", content(emb), content(mcp))
	}

	// Non-trivially correct: the chain has the injected memory with its provenance,
	// the query, and the verdict.
	if emb.Trace.Query != "What is the capital of France?" {
		t.Errorf("query not reconstructed: %q", emb.Trace.Query)
	}
	if len(emb.Trace.Items) != 1 || emb.Trace.Items[0].MemoryID != "01TMEMAAAAAAAAAAAAAAAAAAAA" || len(emb.Trace.Items[0].Provenance) != 1 {
		t.Fatalf("items/provenance wrong: %+v", emb.Trace.Items)
	}
	if len(emb.Trace.Verdicts) != 1 || emb.Trace.Verdicts[0].Verdict != "entailed" {
		t.Errorf("verdict not reconstructed: %+v", emb.Trace.Verdicts)
	}
	// Every surface signed with the same configured key.
	if !emb.Signed || !htp.Signed || !mcp.Signed {
		t.Errorf("all surfaces should sign: emb=%v htp=%v mcp=%v", emb.Signed, htp.Signed, mcp.Signed)
	}
	if emb.PublicKey != htp.PublicKey || emb.PublicKey != mcp.PublicKey {
		t.Errorf("signing public key diverged across surfaces")
	}
}
