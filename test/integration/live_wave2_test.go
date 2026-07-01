// live_wave2_test.go is a LIVE validation of Stowage Wave 2's ae7
// Harbor-aligned JWT verifier (D-136/D-147) against the REAL gateway
// (bifrost → OpenRouter: embed + complete + rerank, D-075). It runs a real
// ingest → buffer flush → real LLM extraction → real-embedding retrieve
// round trip with the server in ModeJWT (jwt mode, D-136) — auth.NewValidator
// resolving a real httptest-served JWKS document signed by a test-only RSA
// key (the same test signer auth_jwt_test.go uses) — so every ingest and
// retrieve call on this run is gated by a REAL JWT verification, not a
// keyring stand-in.
//
// Wiring mirrors live_wave1_test.go (ONE shared boot.Stack + boot.Pipeline
// behind BOTH the HTTP API and MCP-over-HTTP surfaces, D-074 co-mount) PLUS
// auth_jwt_test.go's ae7 JWT plumbing (the RSA test signer, the httptest
// JWKS endpoint, auth.NewValidator/auth.NewJWTAuthenticator,
// srv.SetAuthenticator, mcpserver.AuthMiddleware, the jwtBearerRT bearer
// round-tripper + StreamableClientTransport). Those two files' helpers
// (newAuthJWTKey, authJWTFixtureKey.mint/jwksJSON, configAuthJWT, mustJWKS,
// jwtBearerRT, httpGetWithBearer, startStack, waitForLiveMemories,
// waitForExtractionDrained, installLiveTopics, uniqueTenant, resultText,
// decodeStructured, mcpItemsToSDK, idSetOf) are REUSED verbatim — nothing
// here reimplements them.
//
// The three single-user, auth-bearing surfaces exercised here — HTTP,
// MCP-over-HTTP, and the SDK in HTTP mode — all carry the SAME minted JWT as
// an `Authorization: Bearer <token>` header: sdk/stowage/http.go's
// httpClient.do sets exactly that header from the apiKey slot NewHTTP was
// given, so stowage.NewHTTP(url, token) is a genuine third bearer-carrying
// client, not a simulated one.
//
// GATED: skipped unless STOWAGE_LIVE=1 and OPENROUTER_API_KEY are set. NEVER
// runs in CI — it makes real, paid model calls.
//
// Run it:
//
//	set -a; source .env; set +a
//	STOWAGE_LIVE=1 go test ./test/integration/ -run TestLiveWave2 -v -timeout 15m
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// TestLiveWave2_JWTAuth is the LIVE ae7 acceptance bar: a real
// ingest->extract->retrieve round trip against the real gateway, gated end to
// end by a real Harbor-aligned JWT verifier (jwt mode) instead of the
// keyring — on the three auth-bearing single-user surfaces (HTTP,
// MCP-over-HTTP, SDK-over-HTTP) — plus the ae7 auth negatives: no token,
// expired token, and cross-tenant isolation (P3) under a real store read.
func TestLiveWave2_JWTAuth(t *testing.T) {
	if os.Getenv("STOWAGE_LIVE") == "" {
		t.Skip("live gateway test; set STOWAGE_LIVE=1 (needs OPENROUTER_API_KEY) to run")
	}
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set — export it (e.g. `set -a; source .env; set +a`) to run the live wave-2 validation")
	}

	// ── JWT fixture: a real RSA test signer + a real httptest-served JWKS
	//    endpoint (the SAME signer auth_jwt_test.go uses — no mocks at the
	//    auth boundary, CLAUDE.md §17). ──
	key := newAuthJWTKey(t)
	jwksTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(key.jwksJSON())
	}))
	t.Cleanup(jwksTS.Close)

	// ── Config: the real gateway (bifrost → OpenRouter), mirroring
	//    live_wave1_test.go's config builder exactly, PLUS cfg.Auth in jwt
	//    mode pointed at the httptest JWKS above (D-136/D-147). ──
	cfg := *config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = filepath.Join(t.TempDir(), "live-wave2.db")
	cfg.Profile = "assistant"
	cfg.Gateway.Driver = "bifrost"
	cfg.Gateway.Provider = "openrouter"
	cfg.Gateway.BaseURL = "https://openrouter.ai/api"
	cfg.Gateway.RerankBaseURL = "https://openrouter.ai/api/v1"
	cfg.Gateway.APIKey = "env.OPENROUTER_API_KEY" //nolint:gosec // G101: env-var reference, not a credential (D-030)
	cfg.Gateway.Model = "openai/gpt-5.4-nano"
	cfg.Gateway.EmbedModel = "perplexity/pplx-embed-v1-0.6b"
	cfg.Gateway.EmbedDims = 1024
	cfg.Gateway.RerankModel = "cohere/rerank-4-fast"
	cfg.Auth = configAuthJWT(jwksTS.URL, 3600)

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validate: %v", err)
	}

	tenant := uniqueTenant("live-wave2")
	scope := identity.Scope{Tenant: tenant}
	ctx := context.Background()

	// ── ONE shared stack + pipeline behind BOTH the HTTP API and the
	//    MCP-over-HTTP transport (comount-style co-mount, D-074) — every
	//    surface reads/writes the SAME store + retrieval cache. ──
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	installLiveTopics(t, stk.Store, scope)

	// ── The shared JWT Authenticator (D-067 — one core, consumed by BOTH the
	//    HTTP and MCP-over-HTTP surfaces below). ──
	v, err := auth.NewValidator(mustJWKS(t, jwksTS.URL, 3600), auth.WithIssuer("harbor"), auth.WithAudience("stowage"))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	jwtAuthn := auth.NewJWTAuthenticator(v)

	// ── HTTP surface, ModeJWT (SetAuthenticator overrides the zero-config
	//    keyring default, ae7). ──
	httpSrv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	httpSrv.SetPipelineIn(p.In)
	httpSrv.SetStage(p.Stage)
	httpSrv.SetTopicService(stk.TopicSvc)
	httpSrv.SetRetriever(stk.Retriever)
	httpSrv.SetGrantsService(stk.GrantsSvc)
	httpSrv.SetAuthenticator(jwtAuthn)
	apiTS := httptest.NewServer(httpSrv)
	t.Cleanup(apiTS.Close)

	// ── MCP-over-HTTP surface, the SAME authn + the SAME shared stack
	//    (CtxScopeFn resolves the Scope AuthMiddleware injected — mirrors
	//    auth_jwt_test.go's TestAuthJWT_Parity_APIAndMCP_SameScope). ──
	mcpSvc := &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, GrantsSvc: stk.GrantsSvc,
		PipelineIn: p.In, PipelineStage: p.Stage, Log: stk.Log,
		ScopeFn: mcpserver.CtxScopeFn(), Profile: cfg.Profile,
	}
	mcpSrv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, mcpSvc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	mcpHandler, err := mcpSrv.HTTPHandler(nil)
	if err != nil {
		t.Fatalf("HTTPHandler: %v", err)
	}
	mcpHTTPTS := httptest.NewServer(mcpserver.AuthMiddleware(jwtAuthn, mcpHandler))
	t.Cleanup(mcpHTTPTS.Close)

	// ── Mint the JWT for (tenant=T, user=alice, session=s1, scopes=[read]) —
	//    a real RS256 token, real signature, real claims. ──
	const user = "alice"
	const session = "live-wave2-sess"
	token := key.mint(t, tenant, user, session, time.Now().Add(time.Hour), []string{"read"})
	t.Logf("minted JWT: tenant=%s user=%s session=%s scopes=[read] exp=+1h", tenant, user, session)

	// httpReq performs an authenticated (or unauthenticated, when token=="")
	// request against the HTTP API surface.
	httpReq := func(token, method, path string, body any) (int, []byte) {
		t.Helper()
		var reader *strings.Reader
		if body != nil {
			b, merr := json.Marshal(body)
			if merr != nil {
				t.Fatalf("marshal %s %s: %v", method, path, merr)
			}
			reader = strings.NewReader(string(b))
		} else {
			reader = strings.NewReader("")
		}
		req, rerr := http.NewRequestWithContext(ctx, method, apiTS.URL+path, reader)
		if rerr != nil {
			t.Fatalf("new request %s %s: %v", method, path, rerr)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, derr := apiTS.Client().Do(req)
		if derr != nil {
			t.Fatalf("%s %s: %v", method, path, derr)
		}
		defer func() { _ = resp.Body.Close() }()
		out, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			t.Fatalf("%s %s: read body: %v", method, path, rerr)
		}
		return resp.StatusCode, out
	}

	// mcpRawReq performs an authenticated (or unauthenticated) raw POST
	// against the MCP-over-HTTP endpoint. This deliberately bypasses the
	// JSON-RPC protocol handshake — mcpserver.AuthMiddleware runs BEFORE the
	// Dockyard mcp handler on every request regardless of method/body, so the
	// resulting status code is exactly what a real client's initialize call
	// would see, without needing a working session for the negative cases.
	mcpRawReq := func(token string) (int, []byte) {
		t.Helper()
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, mcpHTTPTS.URL, strings.NewReader("{}"))
		if rerr != nil {
			t.Fatalf("new mcp request: %v", rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, derr := http.DefaultClient.Do(req)
		if derr != nil {
			t.Fatalf("mcp raw POST: %v", derr)
		}
		defer func() { _ = resp.Body.Close() }()
		out, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			t.Fatalf("mcp raw POST: read body: %v", rerr)
		}
		return resp.StatusCode, out
	}

	// mcpCallLive calls tool over a FRESH MCP-over-HTTP session carrying
	// token as a Bearer header on every request (jwtBearerRT, mirrors
	// auth_jwt_test.go's TestAuthJWT_Parity_APIAndMCP_SameScope).
	mcpCallLive := func(token, tool string, args any) (*mcpsdk.CallToolResult, error) {
		t.Helper()
		transport := &mcpsdk.StreamableClientTransport{
			Endpoint:             mcpHTTPTS.URL,
			HTTPClient:           &http.Client{Transport: jwtBearerRT{base: http.DefaultTransport, token: token}},
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "live-wave2-client", Version: "0.0.0"}, nil)
		session, serr := client.Connect(ctx, transport, nil)
		if serr != nil {
			return nil, serr
		}
		defer func() { _ = session.Close() }()
		return session.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	}

	// ── SDK surface: the public Go SDK in HTTP mode, apiKey slot carrying the
	//    JWT — sdk/stowage/http.go's httpClient.do sets
	//    "Authorization: Bearer "+c.apiKey unconditionally, so this is a
	//    genuine third bearer-carrying client over the SAME httptest
	//    listener, not a simulated one. ──
	sdkClient := stowage.NewHTTP(apiTS.URL, token)

	// ── 1. Ingest a handful of memories over JWT-authed HTTP — the verified
	//    tenant claim (authKey.TenantID, sourced from the JWT's "tenant"
	//    claim, internal/api/auth.go's scopeFromRequest) stamps the write. ──
	const sessID = "live-wave2-sess-buf"
	ingestRecords := []map[string]any{
		{"role": "user", "content": "My preferred terminal emulator is Alacritty, configured with a Nerd Font and a dark color scheme.", "session_id": sessID, "buffer_key": sessID},
		{"role": "user", "content": "We decided to use Redis as the caching layer in front of the recommendation service for its low-latency reads.", "session_id": sessID, "buffer_key": sessID},
		{"role": "user", "content": "Gotcha: the retry middleware doubles requests if the downstream call times out AND the client also has its own retry loop.", "session_id": sessID, "buffer_key": sessID},
	}
	status, body := httpReq(token, http.MethodPost, "/v1/records", map[string]any{"records": ingestRecords})
	if status >= 300 {
		t.Fatalf("POST /v1/records (jwt-authed): status %d body %s", status, body)
	}
	t.Logf("ingested %d records under JWT auth (session=%s)", len(ingestRecords), sessID)

	// Settle briefly before the explicit flush so it doesn't race the
	// buffer-append (mirrors live_wave0/live_wave1's sleep 3).
	time.Sleep(3 * time.Second)
	status, body = httpReq(token, http.MethodPost, "/v1/buffers/"+sessID+"/flush", map[string]any{"trigger": "explicit"})
	if status >= 300 {
		t.Fatalf("POST /v1/buffers/%s/flush (jwt-authed): status %d body %s", sessID, status, body)
	}
	t.Logf("flushed buffer %s — waiting for the real learner LLM to extract + embed…", sessID)

	mems := waitForLiveMemories(t, ctx, stk.Store, scope, 2, 4*time.Minute)
	waitForExtractionDrained(t, ctx, stk.Store, 90*time.Second)
	if settled, _, lerr := stk.Store.Memories().ListByStatus(ctx, scope, "active", 200, ""); lerr == nil && len(settled) >= len(mems) {
		mems = settled
	}
	t.Logf("real extraction produced %d active memories (settled) under tenant %s", len(mems), tenant)
	for i, m := range mems {
		t.Logf("  memory[%d] id=%s kind=%s content=%q", i, m.ID, m.Kind, m.Content)
	}
	if len(mems) == 0 {
		t.Skip("real extraction produced no usable memories — model nondeterminism, not a Stowage bug")
	}

	// Build a retrieve query guaranteed to lexically hit the first extracted
	// memory (decoupled from exactly which of the 3 statements the live LLM
	// chose to extract), mirroring live_wave0_test.go's derivation.
	queryWords := strings.Fields(mems[0].Content)
	if len(queryWords) > 8 {
		queryWords = queryWords[:8]
	}
	query := strings.Join(queryWords, " ")
	if query == "" {
		t.Skip("real extraction produced empty content — model nondeterminism, not a Stowage bug")
	}
	t.Logf("retrieve query (derived from the first extracted memory): %q", query)

	// ── 2/3. Retrieve the same query over the real gateway on all three
	//    JWT-carrying surfaces (profile=precise so the real cross-encoder
	//    rerank pass runs, D-075). ──
	status, body = httpReq(token, http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": 10, "profile": "precise"})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (jwt-authed): status %d body %s", status, body)
	}
	var httpRet stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpRet); err != nil {
		t.Fatalf("decode HTTP retrieve response: %v (body=%s)", err, body)
	}
	t.Logf("HTTP (jwt) /v1/retrieve rendered:\n%s", httpRet.Rendered)
	if len(httpRet.Items) == 0 {
		t.Fatalf("HTTP (jwt) retrieve returned no items for query %q", query)
	}
	if httpRet.Degraded {
		t.Errorf("HTTP (jwt) retrieve unexpectedly degraded (gateway should be live)")
	}

	mcpRes, merr := mcpCallLive(token, "memory_retrieve", mcpserver.RetrieveInput{Query: query, Limit: 10, Profile: "precise"})
	if merr != nil {
		t.Fatalf("MCP (jwt) memory_retrieve: %v", merr)
	}
	if mcpRes.IsError {
		t.Fatalf("MCP (jwt) memory_retrieve returned IsError: %+v", mcpRes.Content)
	}
	var mcpRetOut mcpserver.RetrieveOutput
	decodeStructured(t, mcpRes, &mcpRetOut)
	mcpText := resultText(t, mcpRes)
	t.Logf("MCP (jwt) memory_retrieve Text:\n%s", mcpText)
	if len(mcpRetOut.Items) == 0 {
		t.Fatalf("MCP (jwt) retrieve returned no items for query %q", query)
	}
	if mcpRetOut.Degraded {
		t.Errorf("MCP (jwt) retrieve unexpectedly degraded (gateway should be live)")
	}

	sdkRet, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: query, Limit: 10, Profile: "precise"})
	if err != nil {
		t.Fatalf("SDK (jwt) Retrieve: %v", err)
	}
	t.Logf("SDK (jwt) Retrieve.Rendered:\n%s", sdkRet.Rendered)
	if len(sdkRet.Items) == 0 {
		t.Fatalf("SDK (jwt) retrieve returned no items for query %q", query)
	}
	if sdkRet.Degraded {
		t.Errorf("SDK (jwt) retrieve unexpectedly degraded (gateway should be live)")
	}

	// Surface parity: the three rendered bodies are byte-identical modulo the
	// per-response citation nonce (each Retrieve call mints its own),
	// mirroring live_wave0_test.go's liveNormalize comparison.
	mcpNorm := liveNormalize(mcpText)
	httpNorm := liveNormalize(httpRet.Rendered)
	sdkNorm := liveNormalize(sdkRet.Rendered)
	if mcpNorm != httpNorm {
		t.Errorf("ae7 jwt: MCP/HTTP normalized rendered bodies diverge:\n mcp:  %q\n http: %q", mcpNorm, httpNorm)
	}
	if mcpNorm != sdkNorm {
		t.Errorf("ae7 jwt: MCP/SDK normalized rendered bodies diverge:\n mcp: %q\n sdk: %q", mcpNorm, sdkNorm)
	}
	t.Logf("ae7 PASS: real ingest->extract->retrieve round trip over the real gateway, JWT-gated on all 3 surfaces (HTTP/MCP-over-HTTP/SDK-over-HTTP), rendered bodies match, degraded=false")

	// ── 4. Auth negatives (the ae7 point). ──

	// (a) NO token -> 401 on both HTTP and MCP-over-HTTP.
	status, _ = httpReq("", http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": 10})
	if status != http.StatusUnauthorized {
		t.Errorf("ae7 negative: HTTP /v1/retrieve with NO token: status %d, want 401", status)
	}
	t.Logf("ae7 negative: HTTP no-token -> %d (want 401)", status)

	mcpStatus, _ := mcpRawReq("")
	if mcpStatus != http.StatusUnauthorized {
		t.Errorf("ae7 negative: MCP-over-HTTP with NO token: status %d, want 401", mcpStatus)
	}
	t.Logf("ae7 negative: MCP-over-HTTP no-token -> %d (want 401)", mcpStatus)

	// (b) EXPIRED token -> rejected. HTTP always maps ANY Authenticate error
	// to 401 (internal/api/auth.go's authMiddleware); MCP-over-HTTP maps only
	// ErrTokenMissing to 401 and every OTHER rejection (including an expired-
	// but-present token) to 403 (internal/mcpserver/server.go's
	// AuthMiddleware doc comment) — both are documented "rejected" outcomes,
	// asserted against their surface's own contract rather than one shared
	// status code.
	expired := key.mint(t, tenant, user, session, time.Now().Add(-time.Hour), []string{"read"})
	status, _ = httpReq(expired, http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": 10})
	if status != http.StatusUnauthorized {
		t.Errorf("ae7 negative: HTTP /v1/retrieve with EXPIRED token: status %d, want 401", status)
	}
	t.Logf("ae7 negative: HTTP expired-token -> %d (want 401)", status)

	mcpStatus, _ = mcpRawReq(expired)
	if mcpStatus != http.StatusForbidden {
		t.Errorf("ae7 negative: MCP-over-HTTP with EXPIRED token: status %d, want 403 (non-ErrTokenMissing rejection)", mcpStatus)
	}
	t.Logf("ae7 negative: MCP-over-HTTP expired-token -> %d (want 403)", mcpStatus)

	// (c) a token for a DIFFERENT tenant sees NONE of tenant T's memories —
	// P3 verified-scope isolation, a real store read returning zero
	// cross-tenant rows. The attacker tenant is fresh (uniqueTenant), so it
	// legitimately has zero memories of its own too.
	attackerTenant := "attacker-tenant-" + tenant
	attackerToken := key.mint(t, attackerTenant, "mallory", "attacker-sess", time.Now().Add(time.Hour), []string{"read"})
	status, body = httpReq(attackerToken, http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": 50, "profile": "precise"})
	if status >= 300 {
		t.Fatalf("ae7 negative: cross-tenant retrieve: status %d body %s", status, body)
	}
	var crossRet stowage.RetrieveResponse
	if err := json.Unmarshal(body, &crossRet); err != nil {
		t.Fatalf("decode cross-tenant retrieve response: %v (body=%s)", err, body)
	}
	if len(crossRet.Items) != 0 {
		t.Errorf("ae7 negative — P3 LEAK: a %q-scoped JWT retrieve for query %q saw %d item(s) that belong to tenant %q: %+v",
			attackerTenant, query, len(crossRet.Items), tenant, crossRet.Items)
	}
	t.Logf("ae7 negative: cross-tenant (%q) retrieve for tenant %q's query -> %d items (want 0)", attackerTenant, tenant, len(crossRet.Items))

	t.Logf("TestLiveWave2_JWTAuth: PASS — real ingest->extract->retrieve round trip over the real gateway, JWT-gated on HTTP/MCP-over-HTTP/SDK-over-HTTP, plus the ae7 auth negatives (no token, expired token, cross-tenant isolation)")
}
