// suggestions_parity_test.go proves the Phase-27 (D-087) all-surfaces bar: the
// proactive pull (memory_suggestions) and the governance read/write
// (memory_proactive_config) behave identically across the embedded SDK, the HTTP
// server, and the MCP tool over one shared sqlite store. The proactive engine is the
// single logic core; the surfaces are thin callers. Because the list endpoint is a
// write (it persists offers and dedupes per session), each surface lists under its
// own session id; the deterministic offer fields (trigger_kind, memory_id, title)
// must match. Governance read/write output is fully deterministic and compared
// byte-for-byte. Runs under -race.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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

// canonicalJSONString re-serialises a JSON string into canonical (sorted-key) form.
func canonicalJSONString(t *testing.T, raw string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("canonicalise %q: %v", raw, err)
	}
	return canonicalJSON(t, v)
}

// adminJSON performs an authenticated admin HTTP call and returns the response body.
func adminJSON(t *testing.T, method, url, body, plaintext string) string {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+plaintext)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: status %d", method, url, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

const suggestTenant = "p27-suggest"

func seedSuggestMemory(t *testing.T, cfg config.Config) {
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
	scope := identity.Scope{Tenant: suggestTenant}
	now := time.Now().UnixMilli()
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: "01SUGEXPAAAAAAAAAAAAAAAAAA", Kind: "fact", Content: "rotate the staging cert",
		Status: "active", Importance: 8, Confidence: 0.9, TrustSource: "user_stated", Stability: 5.0,
		ValidUntil: now + int64(time.Hour/time.Millisecond), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
}

func suggestEmbedded(t *testing.T, cfg config.Config, sess string) stowage.SuggestionsResponse {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(suggestTenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	}()
	resp, err := client.Suggestions(ctx, stowage.SuggestionsRequest{SessionID: sess})
	if err != nil {
		t.Fatalf("embedded Suggestions: %v", err)
	}
	return resp
}

func suggestHTTP(t *testing.T, cfg config.Config, sess string) (stowage.SuggestionsResponse, *stowage.Client) {
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
	key, plaintext, err := auth.Generate(suggestTenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	client := stowage.NewHTTP(ts.URL, plaintext)
	resp, err := client.Suggestions(ctx, stowage.SuggestionsRequest{SessionID: sess})
	if err != nil {
		t.Fatalf("http Suggestions: %v", err)
	}
	return resp, &client
}

func suggestMCP(t *testing.T, cfg config.Config, sess string) stowage.SuggestionsResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(suggestTenant), Profile: cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "suggest-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_suggestions", Arguments: mcpserver.SuggestionsInput{SessionID: sess}})
	if err != nil {
		t.Fatalf("CallTool memory_suggestions: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_suggestions IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var resp stowage.SuggestionsResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("remap memory_suggestions → SDK: %v", err)
	}
	return resp
}

// offerShape is the deterministic (clock- and id-independent) part of an offer.
type offerShape struct {
	TriggerKind string
	MemoryID    string
	Title       string
}

func shapeOf(r stowage.SuggestionsResponse) []offerShape {
	out := make([]offerShape, 0, len(r.Suggestions))
	for _, s := range r.Suggestions {
		out = append(out, offerShape{s.TriggerKind, s.MemoryID, s.Title})
	}
	return out
}

func TestSuggestionsParity_AllSurfaces(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant" // proactive enabled; expiring is a gateway-free default class
	seedSuggestMemory(t, cfg)

	emb := suggestEmbedded(t, cfg, "s-emb")
	htp, _ := suggestHTTP(t, cfg, "s-http")
	mcp := suggestMCP(t, cfg, "s-mcp")

	want := []offerShape{{TriggerKind: "expiring", MemoryID: "01SUGEXPAAAAAAAAAAAAAAAAAA", Title: "rotate the staging cert"}}
	if got := shapeOf(emb); canonicalJSON(t, got) != canonicalJSON(t, want) {
		t.Fatalf("embedded offer shape = %s, want %s", canonicalJSON(t, got), canonicalJSON(t, want))
	}
	if canonicalJSON(t, shapeOf(htp)) != canonicalJSON(t, shapeOf(emb)) {
		t.Errorf("HTTP vs embedded offer shape diverge:\n http=%s\n emb=%s", canonicalJSON(t, shapeOf(htp)), canonicalJSON(t, shapeOf(emb)))
	}
	if canonicalJSON(t, shapeOf(mcp)) != canonicalJSON(t, shapeOf(emb)) {
		t.Errorf("MCP vs embedded offer shape diverge:\n mcp=%s\n emb=%s", canonicalJSON(t, shapeOf(mcp)), canonicalJSON(t, shapeOf(emb)))
	}
	// Each surface persisted a positive-scored offer.
	for name, r := range map[string]stowage.SuggestionsResponse{"emb": emb, "http": htp, "mcp": mcp} {
		if len(r.Suggestions) != 1 || r.Suggestions[0].Score <= 0 || r.Suggestions[0].ID == "" {
			t.Errorf("%s: bad offer %+v", name, r.Suggestions)
		}
	}
}

func TestProactiveConfigParity_HTTPvsMCP(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	seedSuggestMemory(t, cfg) // runs migrations on the shared DSN

	// --- HTTP: PUT an override, GET it back; the PUT echo must equal the GET. ---
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	akey, aplain, err := auth.Generate(suggestTenant, auth.RoleAdmin)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(akey); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	override := `{"enabled":true,"threshold":0.33,"budget":3,"classes":{"expiring":true,"recent_episode":true}}`
	httpPut := adminJSON(t, http.MethodPut, ts.URL+"/v1/admin/proactive", override, aplain)
	httpGet := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/proactive", "", aplain)
	if canonicalJSONString(t, httpPut) != canonicalJSONString(t, httpGet) {
		t.Errorf("HTTP PUT echo vs GET diverge:\n put=%s\n get=%s", httpPut, httpGet)
	}

	// --- MCP: read the same scope's governance over a second stack on the shared
	// store; it must equal the HTTP GET (one logic core, thin surfaces). ---
	mcpGet := proactiveConfigMCP(t, cfg, mcpserver.ProactiveConfigInput{Action: "get"})
	if canonicalJSONString(t, mcpGet) != canonicalJSONString(t, httpGet) {
		t.Errorf("MCP get vs HTTP get diverge:\n mcp=%s\n http=%s", mcpGet, httpGet)
	}
}

// proactiveConfigMCP spins an MCP server over a fresh stack on the shared DSN and
// returns memory_proactive_config's structured output as JSON.
func proactiveConfigMCP(t *testing.T, cfg config.Config, in mcpserver.ProactiveConfigInput) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(suggestTenant), Profile: cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "gov-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_proactive_config", Arguments: in})
	if err != nil {
		t.Fatalf("CallTool memory_proactive_config: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_proactive_config IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	return string(b)
}
