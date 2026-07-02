// auth_jwt_test.go is the §17 integration bar for phase ae7 (the Harbor-aligned
// JWT verifier, D-136/D-147, AC-11): boots BOTH auth.Mode with real drivers
// (sqlite store, an httptest-served JWKS endpoint) and proves —
//
//   - jwt mode: a valid token narrows the request to its verified Scope.Tenant
//     and a store-backed GET /v1/memories read returns ONLY that tenant's rows,
//     never a peer tenant's (the P3 bar the plan asks for — see the "why tenant,
//     not user" note below).
//   - jwt mode: an expired token is rejected (401) and a JWKS gone stale past
//     max_stale is rejected (401) — the two D-147 failure modes.
//   - keyring mode is unaffected by the ae7 refactor (a plain Bearer key still
//     authenticates exactly as before).
//   - the API and MCP-over-HTTP surfaces resolve the SAME JWT to the SAME
//     Scope (AC-7 parity — one Authenticator core, D-067).
//
// Why TENANT, not per-user, narrowing: ae7's mandate is to make the verified
// user/session claim EXIST on the request Scope (identity.WithScope carries
// the full Scope{Tenant,User,Session}) — whether existing read handlers
// CONSUME Scope.User for filtering (vs. today's arg-derived scope) is
// explicitly ae8's effective-scope job (plan §Design). Tenant is the one
// dimension already consumed end-to-end today (scopeFromRequest ->
// retrieval.Browse -> the store's scope-WHERE), so it is the honest,
// in-scope P3 proof for this phase; a user-level proof would test
// not-yet-built ae8 behavior.
//
// Real drivers throughout (sqlite store, a real httptest-served JWKS
// endpoint, the real internal/auth.Validator/JWKSKeySet) — no mocks at the
// auth boundary (CLAUDE.md §17). Runs under -race.
package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/store"
)

// ---- a self-contained RSA test signer + JWKS server (integration-local; a
// deliberate, independent re-implementation of the AC-2 test-only signer
// idea — internal/auth's own signer_test.go helpers are unexported to that
// package and are not meant to leak here) --------------------------------

type authJWTFixtureKey struct {
	kid  string
	priv *rsa.PrivateKey
}

func newAuthJWTKey(t *testing.T) authJWTFixtureKey {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return authJWTFixtureKey{kid: "ae7-it-kid", priv: priv}
}

func (k authJWTFixtureKey) jwksJSON() []byte {
	pub := k.priv.PublicKey
	doc := map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"kid": k.kid,
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	}
	b, _ := json.Marshal(doc)
	return b
}

func (k authJWTFixtureKey) mint(t *testing.T, tenant, user, session string, exp time.Time, scopes []string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"tenant":  tenant,
		"user":    user,
		"session": session,
		"iss":     "harbor",
		"aud":     "stowage",
		"sub":     user,
		"scopes":  scopes,
		"iat":     time.Now().Unix(),
		"exp":     exp.Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = k.kid
	signed, err := tok.SignedString(k.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// seedTenantMemory inserts one active memory for scope, returning its id.
func seedTenantMemory(t *testing.T, st store.Store, scope identity.Scope, id string) {
	t.Helper()
	ctx := context.Background()
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: id, Kind: "fact", Content: "content for " + id, Status: "active",
		Confidence: 0.9, TrustSource: "asserted", Stability: 1.0,
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("seed memory %s: %v", id, err)
	}
}

type browseWire struct {
	Memories []struct {
		ID string `json:"id"`
	} `json:"memories"`
}

func httpGetWithBearer(t *testing.T, url, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// ---- AC-11: jwt mode narrows to the verified tenant scope -----------------

func TestAuthJWT_ValidToken_NarrowsToTenantScope(t *testing.T) {
	key := newAuthJWTKey(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(key.jwksJSON())
	}))
	defer ts.Close()

	cfg := baseConfig(t)
	cfg.Auth = configAuthJWT(ts.URL, 3600)
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	const acmeMemID = "01AE7ACMEAAAAAAAAAAAAAAAAA"
	const betaMemID = "01AE7BETAAAAAAAAAAAAAAAAAA"
	// Seed under the token's USER — ae8 makes the JWT `user` claim narrow reads
	// (the read-side gap closure, D-148): acmeToken carries user=alice, betaToken
	// user=bob, so a user-scoped memory is what each token now resolves to.
	seedTenantMemory(t, stk.Store, identity.Scope{Tenant: "ae7-acme", User: "alice"}, acmeMemID)
	seedTenantMemory(t, stk.Store, identity.Scope{Tenant: "ae7-beta", User: "bob"}, betaMemID)

	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	v, err := auth.NewValidator(mustJWKS(t, ts.URL, 3600), auth.WithIssuer("harbor"), auth.WithAudience("stowage"))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	srv.SetAuthenticator(auth.NewJWTAuthenticator(v))
	ts2 := httptest.NewServer(srv)
	t.Cleanup(ts2.Close)

	acmeToken := key.mint(t, "ae7-acme", "alice", "s1", time.Now().Add(time.Hour), []string{"read"})
	betaToken := key.mint(t, "ae7-beta", "bob", "s1", time.Now().Add(time.Hour), []string{"read"})

	status, body := httpGetWithBearer(t, ts2.URL+"/v1/memories", acmeToken)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/memories (acme token): status %d body %s", status, body)
	}
	var acmeOut browseWire
	if err := json.Unmarshal(body, &acmeOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sawAcme, sawBeta bool
	for _, m := range acmeOut.Memories {
		if m.ID == acmeMemID {
			sawAcme = true
		}
		if m.ID == betaMemID {
			sawBeta = true
		}
	}
	if !sawAcme {
		t.Error("acme-scoped token must see acme's memory")
	}
	if sawBeta {
		t.Error("P3 LEAK — acme-scoped JWT saw beta's memory")
	}

	status, body = httpGetWithBearer(t, ts2.URL+"/v1/memories", betaToken)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/memories (beta token): status %d body %s", status, body)
	}
	var betaOut browseWire
	if err := json.Unmarshal(body, &betaOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sawAcme, sawBeta = false, false
	for _, m := range betaOut.Memories {
		if m.ID == acmeMemID {
			sawAcme = true
		}
		if m.ID == betaMemID {
			sawBeta = true
		}
	}
	if !sawBeta {
		t.Error("beta-scoped token must see beta's memory")
	}
	if sawAcme {
		t.Error("P3 LEAK — beta-scoped JWT saw acme's memory")
	}
}

// TestAuthJWT_ExpiredTokenRejected covers AC-11's first failure mode.
func TestAuthJWT_ExpiredTokenRejected(t *testing.T) {
	key := newAuthJWTKey(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(key.jwksJSON())
	}))
	defer ts.Close()

	cfg := baseConfig(t)
	cfg.Auth = configAuthJWT(ts.URL, 3600)
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	v, err := auth.NewValidator(mustJWKS(t, ts.URL, 3600), auth.WithIssuer("harbor"), auth.WithAudience("stowage"))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	srv.SetAuthenticator(auth.NewJWTAuthenticator(v))
	ts2 := httptest.NewServer(srv)
	t.Cleanup(ts2.Close)

	expired := key.mint(t, "ae7-acme", "alice", "s1", time.Now().Add(-time.Hour), []string{"read"})
	status, _ := httpGetWithBearer(t, ts2.URL+"/v1/memories", expired)
	if status != http.StatusUnauthorized {
		t.Errorf("GET /v1/memories (expired token): status %d, want 401", status)
	}
}

// TestAuthJWT_StaleJWKSRejected covers AC-11's second failure mode (D-147):
// once the JWKS snapshot ages past max_stale without a successful refresh,
// KeyByID fails CLOSED and the request is rejected — clock-driven, no
// wall-clock sleep.
func TestAuthJWT_StaleJWKSRejected(t *testing.T) {
	key := newAuthJWTKey(t)
	var failing atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(key.jwksJSON())
	}))
	defer ts.Close()

	cfg := baseConfig(t)
	cfg.Auth = configAuthJWT(ts.URL, 300) // 5 minute ceiling
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	clk := &authJWTClock{}
	clk.set(time.Unix(1_700_000_000, 0))

	keys, err := auth.NewJWKSKeySet(context.Background(), auth.JWKSSource{URL: ts.URL}, 300*time.Second,
		auth.WithJWKSClock(clk.now), auth.WithJWKSTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewJWKSKeySet: %v", err)
	}
	v, err := auth.NewValidator(keys, auth.WithClock(clk.now))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetAuthenticator(auth.NewJWTAuthenticator(v))
	ts2 := httptest.NewServer(srv)
	t.Cleanup(ts2.Close)

	longLivedToken := key.mint(t, "ae7-acme", "alice", "s1", clk.now().Add(24*time.Hour), []string{"read"})

	// Within max_stale: still verifies.
	status, body := httpGetWithBearer(t, ts2.URL+"/v1/memories", longLivedToken)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/memories (fresh JWKS): status %d body %s", status, body)
	}

	// The JWKS source starts failing and the clock advances past max_stale —
	// KeyByID must fail closed (D-147); the request is rejected.
	failing.Store(true)
	clk.set(clk.now().Add(10 * time.Minute))
	status, _ = httpGetWithBearer(t, ts2.URL+"/v1/memories", longLivedToken)
	if status != http.StatusUnauthorized {
		t.Errorf("GET /v1/memories (stale JWKS): status %d, want 401 (ErrJWKSStale, D-147)", status)
	}
}

// TestAuthJWT_KeyringModeUnchanged proves keyring mode still works exactly as
// before the ae7 refactor (AC-11's third bar).
func TestAuthJWT_KeyringModeUnchanged(t *testing.T) {
	cfg := baseConfig(t) // Auth zero-value; api.New defaults to keyring internally
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	const memID = "01AE7KEYRINGAAAAAAAAAAAAAA"
	seedTenantMemory(t, stk.Store, identity.Scope{Tenant: "ae7-keyring"}, memID)

	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	k, plaintext, err := auth.Generate("ae7-keyring", auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(k); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	status, body := httpGetWithBearer(t, ts.URL+"/v1/memories", plaintext)
	if status != http.StatusOK {
		t.Fatalf("GET /v1/memories (keyring): status %d body %s", status, body)
	}
	var out browseWire
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var saw bool
	for _, m := range out.Memories {
		if m.ID == memID {
			saw = true
		}
	}
	if !saw {
		t.Error("keyring-mode auth must still resolve the request tenant correctly")
	}
}

// ---- AC-7: API and MCP resolve the SAME JWT to the SAME Scope -------------

// jwtBearerRT injects a JWT Bearer token on every MCP request.
type jwtBearerRT struct {
	base  http.RoundTripper
	token string
}

func (b jwtBearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r2)
}

func TestAuthJWT_Parity_APIAndMCP_SameScope(t *testing.T) {
	key := newAuthJWTKey(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(key.jwksJSON())
	}))
	defer ts.Close()

	cfg := baseConfig(t)
	cfg.Auth = configAuthJWT(ts.URL, 3600)
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	const memID = "01AE7PARITYAAAAAAAAAAAAAAA"
	// Parity token carries user=alice; ae8 narrows the read to that user (D-148).
	seedTenantMemory(t, stk.Store, identity.Scope{Tenant: "ae7-parity", User: "alice"}, memID)

	v, err := auth.NewValidator(mustJWKS(t, ts.URL, 3600), auth.WithIssuer("harbor"), auth.WithAudience("stowage"))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	authn := auth.NewJWTAuthenticator(v)

	// API surface.
	apiSrv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	apiSrv.SetAuthenticator(authn)
	apiTS := httptest.NewServer(apiSrv)
	t.Cleanup(apiTS.Close)

	// MCP-over-HTTP surface, SAME authn (D-067 — one core).
	mcpSrv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, PipelineIn: p.In,
		Log: stk.Log, ScopeFn: mcpserver.CtxScopeFn(), Profile: cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	mcpHandler, err := mcpSrv.HTTPHandler(nil)
	if err != nil {
		t.Fatalf("HTTPHandler: %v", err)
	}
	mcpHTTP := httptest.NewServer(mcpserver.AuthMiddleware(authn, mcpHandler))
	t.Cleanup(mcpHTTP.Close)

	token := key.mint(t, "ae7-parity", "alice", "s1", time.Now().Add(time.Hour), []string{"read"})

	// HTTP: GET /v1/memories.
	status, body := httpGetWithBearer(t, apiTS.URL+"/v1/memories", token)
	if status != http.StatusOK {
		t.Fatalf("HTTP GET /v1/memories: status %d body %s", status, body)
	}
	var httpOut browseWire
	if err := json.Unmarshal(body, &httpOut); err != nil {
		t.Fatalf("decode HTTP: %v", err)
	}

	// MCP: memory_browse over real HTTP with the SAME token.
	ctx := context.Background()
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:             mcpHTTP.URL,
		HTTPClient:           &http.Client{Transport: jwtBearerRT{base: http.DefaultTransport, token: token}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ae7-parity-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_browse", Arguments: mcpserver.BrowseInput{}})
	if err != nil {
		t.Fatalf("CallTool memory_browse: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_browse returned IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var mcpOut mcpserver.BrowseOutput
	if err := json.Unmarshal(b, &mcpOut); err != nil {
		t.Fatalf("decode MCP: %v", err)
	}

	var httpSaw, mcpSaw bool
	for _, m := range httpOut.Memories {
		if m.ID == memID {
			httpSaw = true
		}
	}
	for _, m := range mcpOut.Memories {
		if m.ID == memID {
			mcpSaw = true
		}
	}
	if !httpSaw {
		t.Error("HTTP surface: expected to see the tenant's memory")
	}
	if !mcpSaw {
		t.Error("MCP surface: expected to see the SAME tenant's memory (AC-7 parity)")
	}
}

// ---- helpers ----------------------------------------------------------------

// configAuthJWT builds the config.AuthConfig for jwt mode against jwksURL.
func configAuthJWT(jwksURL string, maxStaleSeconds int) config.AuthConfig {
	return config.AuthConfig{
		Mode: "jwt", Issuer: "harbor", Audience: "stowage",
		JWKS: config.JWKSConfig{URL: jwksURL, MaxStale: maxStaleSeconds},
	}
}

// mustJWKS is a small NewJWKSKeySet wrapper for test call sites that don't
// need to tune the clock/TTL.
func mustJWKS(t *testing.T, url string, maxStaleSeconds int) auth.KeySet {
	t.Helper()
	ks, err := auth.NewJWKSKeySet(context.Background(), auth.JWKSSource{URL: url}, time.Duration(maxStaleSeconds)*time.Second)
	if err != nil {
		t.Fatalf("NewJWKSKeySet: %v", err)
	}
	return ks
}

// authJWTClock is a small race-safe mutable clock (mirrors internal/auth's
// jwks_test.go atomicTime — duplicated here since that helper is unexported
// to internal/auth).
type authJWTClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *authJWTClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *authJWTClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
