// mcp_handshake_test.go is the §17 integration bar for phase ae11 (method-aware
// MCP handshake auth, D-152): it proves the full dial sequence of an MCP host
// that attaches connections user-agnostically (no per-user credential at connect
// time) and injects a per-user bearer ONLY on tool calls.
//
// Real drivers throughout: a real sqlite store + live pipeline (startStack), a
// real static JWKS FILE (auth.JWKSSource{File}), the real internal/auth Validator
// (verify-never-mint) over a test-only RSA signer, and the real go-sdk streamable
// HTTP client dialing over a real httptest server behind MethodAwareAuthMiddleware.
// No mocks at the auth boundary (CLAUDE.md §17). Runs under -race.
//
// The per-call-bearer host is modelled by perCallBearerRT: it injects the bearer
// on tools/call POSTs ONLY, leaving initialize / notifications/initialized /
// tools/list bearer-less — exactly the motivating consumer-neutral host shape.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
)

// perCallBearerRT models the motivating host: it attaches the connection
// user-agnostically and injects a per-user bearer LAZILY, only on tools/call
// POSTs. The handshake POSTs (initialize, notifications/initialized, tools/list)
// go out bearer-less and must be served by the open-handshake path (D-152).
type perCallBearerRT struct {
	base  http.RoundTripper
	token string
}

func (rt perCallBearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			return nil, err
		}
		r2.Body = io.NopCloser(bytes.NewReader(body))
		r2.ContentLength = int64(len(body))
		var msg struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &msg) == nil && msg.Method == "tools/call" {
			r2.Header.Set("Authorization", "Bearer "+rt.token)
		}
	}
	return rt.base.RoundTrip(r2)
}

// writeStaticJWKS writes key's public JWK Set to a temp file and returns its
// path — the real static-file JWKS source (air-gapped / out-of-band sync path,
// config.JWKSConfig.File).
func writeStaticJWKS(t *testing.T, key authJWTFixtureKey) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(path, key.jwksJSON(), 0o600); err != nil {
		t.Fatalf("write static JWKS file: %v", err)
	}
	return path
}

// startHandshakeMCP boots a jwt-mode MCP-over-HTTP server (real store + pipeline)
// behind MethodAwareAuthMiddleware, keyed off a static JWKS FILE. Returns the
// live server URL.
func startHandshakeMCP(t *testing.T, jwksPath string) string {
	t.Helper()
	cfg := baseConfig(t)
	cfg.Auth = config.AuthConfig{
		Mode:     "jwt",
		Issuer:   "harbor",
		Audience: "stowage",
		JWKS:     config.JWKSConfig{File: jwksPath, MaxStale: 3600},
	}
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	// Seed a cross-user isolation pair under one tenant (criterion 4).
	seedTenantMemory(t, stk.Store, identity.Scope{Tenant: "ae11-iso", User: "alice"}, ae11AliceMemID)
	seedTenantMemory(t, stk.Store, identity.Scope{Tenant: "ae11-iso", User: "bob"}, ae11BobMemID)

	keys, err := auth.NewJWKSKeySet(context.Background(), auth.JWKSSource{File: jwksPath}, 3600*time.Second)
	if err != nil {
		t.Fatalf("NewJWKSKeySet(file): %v", err)
	}
	v, err := auth.NewValidator(keys, auth.WithIssuer("harbor"), auth.WithAudience("stowage"))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	authn := auth.NewJWTAuthenticator(v)

	mcpSrv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.CtxScopeFn(), Profile: cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	handler, err := mcpSrv.HTTPHandler(&server.HTTPOptions{Stateless: true})
	if err != nil {
		t.Fatalf("HTTPHandler: %v", err)
	}
	// The jwt-mode wiring: the method-aware middleware (D-152).
	ts := httptest.NewServer(mcpserver.MethodAwareAuthMiddleware(authn, handler))
	t.Cleanup(ts.Close)
	return ts.URL
}

const (
	ae11AliceMemID = "01AE11ISOALICEAAAAAAAAAAAAA"
	ae11BobMemID   = "01AE11ISOBOBBBBBBBBBBBBBBBB"
)

// TestMCPHandshake_FullDialSequence replays exactly the host dial sequence
// (criterion 5): bearer-less initialize → notifications/initialized → tools/list,
// then a per-user-bearer tools/call on the SAME session — and proves the call
// resolves the JWT's (tenant,user) to the store scope with cross-user isolation
// (criterion 4): alice's bearer sees alice's row, never bob's.
func TestMCPHandshake_FullDialSequence(t *testing.T) {
	key := newAuthJWTKey(t)
	jwksPath := writeStaticJWKS(t, key)
	url := startHandshakeMCP(t, jwksPath)

	aliceToken := key.mint(t, "ae11-iso", "alice", "s1", time.Now().Add(time.Hour), []string{"read"})

	ctx := context.Background()
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:             url,
		HTTPClient:           &http.Client{Transport: perCallBearerRT{base: http.DefaultTransport, token: aliceToken}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ae11-handshake-client", Version: "0.0.0"}, nil)

	// Connect performs the bearer-less initialize + notifications/initialized.
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("bearer-less handshake (initialize+initialized) failed: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// Bearer-less tools/list returns the full static catalog.
	tools, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("bearer-less tools/list failed: %v", err)
	}
	var sawRetrieve, sawBrowse bool
	for _, tl := range tools.Tools {
		switch tl.Name {
		case "memory_retrieve":
			sawRetrieve = true
		case "memory_browse":
			sawBrowse = true
		}
	}
	if !sawRetrieve || !sawBrowse {
		t.Fatalf("bearer-less tools/list missing catalog entries (retrieve=%v browse=%v), got %d tools",
			sawRetrieve, sawBrowse, len(tools.Tools))
	}

	// Per-user-bearer tools/call on the SAME session: resolves alice's scope.
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_browse", Arguments: mcpserver.BrowseInput{}})
	if err != nil {
		t.Fatalf("bearer tools/call memory_browse failed: %v", err)
	}
	if res.IsError {
		cb, _ := json.Marshal(res.Content)
		t.Fatalf("memory_browse returned IsError: %s", cb)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var out mcpserver.BrowseOutput
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode memory_browse output: %v", err)
	}
	var sawAlice, sawBob bool
	for _, m := range out.Memories {
		if m.ID == ae11AliceMemID {
			sawAlice = true
		}
		if m.ID == ae11BobMemID {
			sawBob = true
		}
	}
	if !sawAlice {
		t.Error("alice's bearer must resolve alice's own memory through the opened handshake session")
	}
	if sawBob {
		t.Error("P3 LEAK — alice's bearer saw bob's memory through the opened handshake session (criterion 4)")
	}
}

// TestMCPHandshake_ExpiredTokenOnToolsCall covers the required failure mode: the
// bearer-less handshake still completes, but a tools/call carrying an EXPIRED
// bearer is rejected (403) — the strict per-call gate is unmoved by the open
// handshake.
func TestMCPHandshake_ExpiredTokenOnToolsCall(t *testing.T) {
	key := newAuthJWTKey(t)
	jwksPath := writeStaticJWKS(t, key)
	url := startHandshakeMCP(t, jwksPath)

	expired := key.mint(t, "ae11-iso", "alice", "s1", time.Now().Add(-time.Hour), []string{"read"})

	ctx := context.Background()
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:             url,
		HTTPClient:           &http.Client{Transport: perCallBearerRT{base: http.DefaultTransport, token: expired}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ae11-expired-client", Version: "0.0.0"}, nil)

	// The handshake itself is bearer-less, so Connect succeeds.
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("bearer-less handshake failed: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// The expired-bearer tools/call is rejected at the middleware (403) before
	// the SDK handler — the CallTool surfaces the transport error.
	_, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_browse", Arguments: mcpserver.BrowseInput{}})
	if err == nil {
		t.Fatal("expired-bearer tools/call must be rejected, got nil error")
	}
}
