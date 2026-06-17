// comount_test.go proves the Phase h6 (D-074) co-mount: ONE serve-style process
// exposes BOTH the HTTP API and the MCP-over-HTTP surface over a SINGLE
// boot.Stack + boot.StartPipeline (one ingest channel, one retriever cache, one
// sweep set). It asserts:
//
//	AC-1  both surfaces answer — REST POST /v1/records + /v1/retrieve, and MCP
//	      CallTool memory_ingest + memory_retrieve, over the SAME stack.
//	AC-2  cache-coherence (the point): a memory ingested+flushed via the HTTP
//	      surface is returned by an MCP memory_retrieve with NO stale window —
//	      the same stk.Retriever cache backs both listeners.
//	AC-3  shutdown stops BOTH listeners before p.Drain closes the ingest channel
//	      (the h1 ingress-before-Drain invariant) with no send-on-closed panic.
//
// Real drivers: sqlite store + the gateway mock driver (the sanctioned test
// exception, CLAUDE.md §17 — extraction is scripted so assertions are
// deterministic). The MCP surface is driven over a REAL second HTTP listener via
// the streamable client transport with a Bearer key — exactly the runServe
// co-mount path (KeyringMiddleware + HTTPHandler). Runs under -race.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
)

// bearerRT injects the agent Bearer key on every MCP request so the co-mounted
// KeyringMiddleware resolves the request scope from the authenticated key
// (D-030/P3). It clones the request per the RoundTripper contract.
type bearerRT struct {
	base  http.RoundTripper
	token string
}

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r2)
}

// comountFixture is one serve-style process: a single stack + pipeline behind
// two real HTTP listeners (the HTTP API and MCP-over-HTTP), mirroring runServe.
type comountFixture struct {
	apiURL  string
	mcpURL  string
	key     string // plaintext agent key
	stk     *boot.Stack
	p       *boot.Pipeline
	apiHTTP *http.Server
	mcpHTTP *http.Server
	closed  bool
}

// setupComount boots ONE stack + pipeline and serves the api and the MCP handler
// over two ephemeral 127.0.0.1 listeners — the literal runServe co-mount wiring.
// A t.Cleanup force-tears-down if the test did not call shutdown itself.
func setupComount(t *testing.T, cfg config.Config, tenant string) *comountFixture {
	t.Helper()
	scope := identity.Scope{Tenant: tenant}

	stk, p := startStack(t, cfg)
	installTopic(t, stk.Store, scope)

	// HTTP API listener.
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetStage(p.Stage)
	srv.SetTopicService(stk.TopicSvc)
	srv.SetRetriever(stk.Retriever)
	srv.SetGrantsService(stk.GrantsSvc)

	apiLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("api listen: %v", err)
	}
	apiHTTP := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = apiHTTP.Serve(apiLn) }()

	// MCP-over-HTTP listener over the SAME stk + p — co-mount (D-074).
	mcpSrv, err := mcpserver.New(server.Info{Name: "stowage", Title: "Stowage Memory MCP Server", Version: "test"}, &mcpserver.Services{
		Store:         stk.Store,
		Retriever:     stk.Retriever,
		TopicSvc:      stk.TopicSvc,
		GrantsSvc:     stk.GrantsSvc,
		PipelineIn:    p.In,    // SAME ingest channel
		PipelineStage: p.Stage, // SAME buffer stage
		Log:           stk.Log,
		ScopeFn:       mcpserver.CtxScopeFn(), // tenant from the authenticated key
		Profile:       cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	mcpHandler, err := mcpSrv.HTTPHandler(nil)
	if err != nil {
		t.Fatalf("mcp HTTPHandler: %v", err)
	}
	mcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mcp listen: %v", err)
	}
	mcpHTTP := &http.Server{
		Handler:           mcpserver.KeyringMiddleware(stk.Store.Keys(), mcpHandler),
		ReadHeaderTimeout: 10 * time.Second, // no WriteTimeout — MCP streams
	}
	go func() { _ = mcpHTTP.Serve(mcpLn) }()

	key, plaintext, err := auth.Generate(tenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	f := &comountFixture{
		apiURL:  "http://" + apiLn.Addr().String(),
		mcpURL:  "http://" + mcpLn.Addr().String(),
		key:     plaintext,
		stk:     stk,
		p:       p,
		apiHTTP: apiHTTP,
		mcpHTTP: mcpHTTP,
	}
	t.Cleanup(func() {
		if f.closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = apiHTTP.Close()
		_ = mcpHTTP.Close()
		_ = p.Drain(ctx)
		_ = stk.Close(ctx)
	})
	return f
}

// shutdown replicates runServe's exact shutdown ORDER: stop BOTH HTTP listeners
// (await both Shutdowns) BEFORE p.Drain closes the ingest channel. A panic here
// (e.g. a send on the closed ingest channel) fails the test.
func (f *comountFixture) shutdown(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := f.apiHTTP.Shutdown(ctx); err != nil {
		t.Fatalf("api Shutdown: %v", err)
	}
	if err := f.mcpHTTP.Shutdown(ctx); err != nil {
		t.Fatalf("mcp Shutdown: %v", err)
	}
	if err := f.p.Drain(ctx); err != nil {
		t.Fatalf("pipeline Drain after dual shutdown: %v", err)
	}
	if err := f.stk.Close(ctx); err != nil {
		t.Fatalf("stack Close: %v", err)
	}
	f.closed = true
}

// restPost POSTs JSON to the api listener with the agent key.
func (f *comountFixture) restPost(t *testing.T, path string, body any) []byte {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, f.apiURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("REST POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		t.Fatalf("REST POST %s: status %d body %s", path, resp.StatusCode, out)
	}
	return out
}

// mcpSession connects an MCP client to the co-mounted MCP listener over real
// HTTP with the agent Bearer key.
func (f *comountFixture) mcpSession(t *testing.T, ctx context.Context) *mcpsdk.ClientSession {
	t.Helper()
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:             f.mcpURL,
		HTTPClient:           &http.Client{Transport: bearerRT{base: http.DefaultTransport, token: f.key}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "comount-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func mcpCall(t *testing.T, ctx context.Context, session *mcpsdk.ClientSession, name string, args any, out any) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s returned IsError: %+v", name, res.Content)
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("decode %s result: %v", name, err)
		}
	}
}

// TestComount_BothSurfaces_CacheCoherence is the AC-1 + AC-2 bar.
func TestComount_BothSurfaces_CacheCoherence(t *testing.T) {
	scriptPath := seedMockScript(t)

	cfg := baseConfig(t)
	tenant := "comount-tenant"
	f := setupComount(t, cfg, tenant)

	ctx := context.Background()
	session := f.mcpSession(t, ctx)

	// ── AC-1: REST ingest answers; AC-2: drive extraction off the HTTP surface ──
	restIngest := func(n int) string {
		recs := make([]map[string]any, n)
		for i := range recs {
			recs[i] = map[string]any{"role": "user", "content": recContent, "session_id": paritySess}
		}
		var ir struct {
			IDs []string `json:"ids"`
		}
		_ = json.Unmarshal(f.restPost(t, "/v1/records", map[string]any{"records": recs}), &ir)
		if len(ir.IDs) == 0 {
			t.Fatalf("REST ingest: no ids")
		}
		return ir.IDs[0]
	}

	firstID := restIngest(countTrigger - 1)
	writeExtractionScript(t, scriptPath, firstID)
	_ = restIngest(1) // crosses the count trigger → flush → extract → reconcile

	mcpRetrieve := func() ([]retrieved, error) {
		var out mcpserver.RetrieveOutput
		mcpCall(t, ctx, session, "memory_retrieve", mcpserver.RetrieveInput{Query: parityQuery, Limit: 5}, &out)
		res := make([]retrieved, 0, len(out.Items))
		for _, it := range out.Items {
			res = append(res, retrieved{ID: it.ID, Kind: it.Kind, Content: it.Content, Citation: it.Citation})
		}
		return res, nil
	}

	// AC-2: the HTTP-ingested+flushed memory becomes visible to an MCP retrieve.
	// (poll covers only the async derivation latency; the cross-surface visibility
	// is structural — same stk.Retriever.)
	mcpItems := pollRetrieve(t, mcpRetrieve)
	mcpMem := assertParityItem(t, mcpItems)

	// ── AC-2, the sharp edge: NO stale window across surfaces ──
	// A REST retrieve warms the (query, scope) cache entry; an IMMEDIATE MCP
	// retrieve (no poll) must return the SAME memory id from that SAME cache. If
	// the two surfaces did not share one stk.Retriever, the MCP read could miss.
	var restRR struct {
		Items []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Content string `json:"content"`
		} `json:"items"`
	}
	_ = json.Unmarshal(f.restPost(t, "/v1/retrieve", map[string]any{"query": parityQuery, "limit": 5}), &restRR)
	if len(restRR.Items) == 0 {
		t.Fatalf("REST retrieve returned no items after MCP saw the memory (cache not shared?)")
	}
	var restMemID string
	for _, it := range restRR.Items {
		if it.Content == wantContent && it.Kind == wantKind {
			restMemID = it.ID
		}
	}
	if restMemID == "" {
		t.Fatalf("REST retrieve did not return the expected memory: %+v", restRR.Items)
	}

	immediate, _ := mcpRetrieve() // single shot, no poll — must hit the shared cache
	immMem := assertParityItem(t, immediate)
	if immMem.ID != restMemID || immMem.ID != mcpMem.ID {
		t.Errorf("cross-surface memory id diverges (shared cache violated): mcp=%q rest=%q firstMCP=%q",
			immMem.ID, restMemID, mcpMem.ID)
	}

	// ── AC-1: the MCP surface ALSO answers memory_ingest ──
	var ingOut mcpserver.IngestOutput
	mcpCall(t, ctx, session, "memory_ingest", mcpserver.IngestInput{
		Records: []mcpserver.IngestRecord{{Role: "user", Content: recContent, SessionID: "mcp-sess"}},
	}, &ingOut)
	if len(ingOut.IDs) == 0 {
		t.Fatalf("MCP memory_ingest returned no ids")
	}
}

// TestComount_ShutdownStopsBothBeforeDrain is the AC-3 bar: both listeners are
// shut down before p.Drain closes the ingest channel — no send-on-closed panic.
func TestComount_ShutdownStopsBothBeforeDrain(t *testing.T) {
	_ = seedMockScript(t)

	cfg := baseConfig(t)
	tenant := "comount-shutdown"
	f := setupComount(t, cfg, tenant)

	ctx := context.Background()
	session := f.mcpSession(t, ctx)

	// Drive a few enqueues on BOTH surfaces so the pipeline has in-flight work
	// when shutdown begins (exercises ingress-before-Drain under real traffic).
	for i := 0; i < 3; i++ {
		_ = f.restPost(t, "/v1/records", map[string]any{
			"records": []map[string]any{{"role": "user", "content": recContent, "session_id": paritySess}},
		})
	}
	var ingOut mcpserver.IngestOutput
	mcpCall(t, ctx, session, "memory_ingest", mcpserver.IngestInput{
		Records: []mcpserver.IngestRecord{{Role: "user", Content: recContent, SessionID: paritySess}},
	}, &ingOut)
	if len(ingOut.IDs) == 0 {
		t.Fatalf("MCP memory_ingest returned no ids")
	}
	_ = session.Close()

	// runServe shutdown order: stop BOTH listeners, await both, THEN Drain. A
	// send on the closed ingest channel would panic across the boundary.
	f.shutdown(t)
}

// seedMockScript seeds an empty STOWAGE_MOCK_SCRIPT (read lazily per Complete
// call) and returns its path; writeExtractionScript overwrites it once the
// provenance record id is known. t.Setenv forbids t.Parallel — these co-mount
// tests are intentionally sequential.
func seedMockScript(t *testing.T) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "mockscript.json")
	if err := os.WriteFile(scriptPath, []byte("[]"), 0o600); err != nil {
		t.Fatalf("seed mock script: %v", err)
	}
	t.Setenv("STOWAGE_MOCK_SCRIPT", scriptPath)
	return scriptPath
}
