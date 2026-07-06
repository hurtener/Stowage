package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/auth"
)

// keyringAuthn builds a jwt-agnostic Authenticator over an empty keyring — good
// enough for the middleware routing tests: open requests never touch it, and
// protected bearer-less/garbage requests exercise the 401/403 split without any
// JWKS machinery (the full jwt dial is proven by the §17 integration test).
func keyringAuthn(t *testing.T) *auth.Authenticator {
	t.Helper()
	return auth.NewKeyringAuthenticator(auth.NewMemKeyring())
}

// ---- classifyRequest table (every rule row, D-152) -------------------------

func TestClassifyRequest(t *testing.T) {
	const (
		initFrame        = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
		initializedFrame = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
		pingFrame        = `{"jsonrpc":"2.0","id":2,"method":"ping"}`
		toolsListFrame   = `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`
		toolsCallFrame   = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_retrieve","arguments":{}}}`
		batchFrame       = `[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`
		// resources/list + prompts/list are static discovery, open since D-152's
		// first conscious extension; the corresponding READS stay protected.
		resListFrame   = `{"jsonrpc":"2.0","id":5,"method":"resources/list"}`
		promptsFrame   = `{"jsonrpc":"2.0","id":6,"method":"prompts/list"}`
		resReadFrame   = `{"jsonrpc":"2.0","id":7,"method":"resources/read","params":{"uri":"x://y"}}`
		promptGetFrame = `{"jsonrpc":"2.0","id":8,"method":"prompts/get","params":{"name":"p"}}`
		unknownFrame   = `{"jsonrpc":"2.0","id":9,"method":"completion/complete"}`
		noMethodFrame  = `{"jsonrpc":"2.0","id":10,"params":{}}`
		malformedFrame = `{"jsonrpc":"2.0","id":11,"method":`
	)

	tests := []struct {
		name     string
		method   string
		body     string
		wantOpen bool
	}{
		{"post initialize is open", http.MethodPost, initFrame, true},
		{"post notifications/initialized is open", http.MethodPost, initializedFrame, true},
		{"post ping is open", http.MethodPost, pingFrame, true},
		{"post tools/list is open", http.MethodPost, toolsListFrame, true},
		{"post resources/list is open (static discovery)", http.MethodPost, resListFrame, true},
		{"post prompts/list is open (static discovery)", http.MethodPost, promptsFrame, true},
		{"get (sse leg) is open", http.MethodGet, "", true},
		{"delete (session teardown) is open", http.MethodDelete, "", true},
		{"post tools/call is protected", http.MethodPost, toolsCallFrame, false},
		{"post resources/read is protected (a read, not discovery)", http.MethodPost, resReadFrame, false},
		{"post prompts/get is protected (a read, not discovery)", http.MethodPost, promptGetFrame, false},
		{"post batch array is protected", http.MethodPost, batchFrame, false},
		{"post unknown method is protected", http.MethodPost, unknownFrame, false},
		{"post missing method is protected", http.MethodPost, noMethodFrame, false},
		{"post malformed body is protected", http.MethodPost, malformedFrame, false},
		{"post empty body is protected", http.MethodPost, "", false},
		{"put is protected", http.MethodPut, initFrame, false},
		{"patch is protected", http.MethodPatch, initFrame, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, "/", body)
			if got := classifyRequest(req); got != tc.wantOpen {
				t.Errorf("classifyRequest(%s %q) = %v, want %v", tc.method, tc.body, got, tc.wantOpen)
			}
		})
	}
}

// TestClassifyRequest_OversizedBodyProtected proves a body over the peek cap is
// protected (rule 4) and its full bytes still reach the downstream handler.
func TestClassifyRequest_OversizedBodyProtected(t *testing.T) {
	// A JSON object whose method IS an open method, padded past the cap: the
	// cap classifies it protected regardless of method (a real handshake frame
	// is never this large), and the body must round-trip intact.
	pad := strings.Repeat("x", handshakePeekLimit+1024)
	oversized := `{"jsonrpc":"2.0","method":"initialize","pad":"` + pad + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(oversized))
	if classifyRequest(req) {
		t.Fatal("oversized body must classify protected (over peek cap)")
	}
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read reconstituted body: %v", err)
	}
	if string(got) != oversized {
		t.Errorf("oversized body not reconstituted byte-identical: got %d bytes, want %d", len(got), len(oversized))
	}
}

// TestClassifyRequest_BodyReconstitution proves criterion 8: after an
// open-classified peek, the downstream handler receives the byte-identical body.
func TestClassifyRequest_BodyReconstitution(t *testing.T) {
	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"c","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_retrieve","arguments":{"query":"x"}}}`,
	}
	for _, b := range bodies {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(b))
		_ = classifyRequest(req)
		got, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read reconstituted body: %v", err)
		}
		if string(got) != b {
			t.Errorf("body not reconstituted byte-identical:\n got: %s\nwant: %s", got, b)
		}
	}
}

// ---- MethodAwareAuthMiddleware routing (criteria 1-3, 6, 8) ----------------

// spyHandler records whether it was reached (the "downstream SDK handler").
type spyHandler struct {
	reached bool
	gotBody []byte
}

func (s *spyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.reached = true
	s.gotBody, _ = io.ReadAll(r.Body)
	w.WriteHeader(http.StatusOK)
}

// TestMethodAwareAuthMiddleware_OpenBypass proves criteria 1 & 2: a bearer-less
// initialize / tools/list reaches the downstream handler with no auth, and the
// downstream body is byte-identical (criterion 8).
func TestMethodAwareAuthMiddleware_OpenBypass(t *testing.T) {
	frames := map[string]string{
		"initialize": `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		"tools/list": `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			spy := &spyHandler{}
			mw := MethodAwareAuthMiddleware(keyringAuthn(t), spy)
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(frame))
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if !spy.reached {
				t.Fatalf("%s: open request did not reach downstream handler", name)
			}
			if rec.Code != http.StatusOK {
				t.Errorf("%s: status %d, want 200", name, rec.Code)
			}
			if string(spy.gotBody) != frame {
				t.Errorf("%s: downstream body not byte-identical:\n got %s\nwant %s", name, spy.gotBody, frame)
			}
		})
	}
}

// TestMethodAwareAuthMiddleware_ProtectedRequiresBearer proves criterion 3: a
// bearer-less tools/call is 401 "authorization required"; a bad token is 403
// "forbidden" — the exact strict-middleware contract, and the downstream handler
// is never reached.
func TestMethodAwareAuthMiddleware_ProtectedRequiresBearer(t *testing.T) {
	toolsCall := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_retrieve","arguments":{}}}`

	t.Run("no bearer -> 401", func(t *testing.T) {
		spy := &spyHandler{}
		mw := MethodAwareAuthMiddleware(keyringAuthn(t), spy)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(toolsCall))
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status %d, want 401", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "authorization required") {
			t.Errorf("body %q, want it to contain %q", rec.Body.String(), "authorization required")
		}
		if spy.reached {
			t.Error("protected tools/call must NOT reach the downstream handler without auth")
		}
	})

	t.Run("bad bearer -> 403", func(t *testing.T) {
		spy := &spyHandler{}
		mw := MethodAwareAuthMiddleware(keyringAuthn(t), spy)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(toolsCall))
		req.Header.Set("Authorization", "Bearer not-a-real-key")
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status %d, want 403", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "forbidden") {
			t.Errorf("body %q, want it to contain %q", rec.Body.String(), "forbidden")
		}
		if spy.reached {
			t.Error("protected tools/call must NOT reach the downstream handler with a bad token")
		}
	})

	t.Run("batch array without bearer -> 401 (default-deny)", func(t *testing.T) {
		batch := `[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`
		spy := &spyHandler{}
		mw := MethodAwareAuthMiddleware(keyringAuthn(t), spy)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(batch))
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("batch array: status %d, want 401", rec.Code)
		}
		if spy.reached {
			t.Error("batch array must be treated as protected, not reach the handler bearer-less")
		}
	})
}

// TestMethodAwareAuthMiddleware_RealServerInitialize proves criterion 1
// end-to-end: a bearer-less initialize against a REAL mcpserver.New server
// (srv.HTTPHandler(nil)) completes the handshake — HTTP 200 with a Mcp-Session-Id.
func TestMethodAwareAuthMiddleware_RealServerInitialize(t *testing.T) {
	srv := newHandshakeTestServer(t)
	handler, err := srv.HTTPHandler(nil)
	if err != nil {
		t.Fatalf("HTTPHandler: %v", err)
	}
	ts := httptest.NewServer(MethodAwareAuthMiddleware(keyringAuthn(t), handler))
	defer ts.Close()

	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"handshake-test","version":"0"}}}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL, strings.NewReader(init))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST initialize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer-less initialize: status %d body %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Mcp-Session-Id") == "" {
		t.Error("bearer-less initialize must issue a Mcp-Session-Id (stateful mode)")
	}
	if !strings.Contains(string(body), "protocolVersion") {
		t.Errorf("initialize result missing protocolVersion: %s", body)
	}
}

// TestAuthMiddleware_KeyringInitialize401 proves criterion 6: in keyring mode
// the strict AuthMiddleware still 401s a bearer-less initialize — byte-identical
// to today (this phase does not touch that path).
func TestAuthMiddleware_KeyringInitialize401(t *testing.T) {
	srv := newHandshakeTestServer(t)
	handler, err := srv.HTTPHandler(nil)
	if err != nil {
		t.Fatalf("HTTPHandler: %v", err)
	}
	ts := httptest.NewServer(AuthMiddleware(keyringAuthn(t), handler))
	defer ts.Close()

	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL, strings.NewReader(init))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST initialize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("keyring bearer-less initialize: status %d, want 401 (strict gate intact)", resp.StatusCode)
	}
}

// TestMethodAwareAuthMiddleware_Concurrent proves the middleware is safe under
// concurrent reuse (§5 reusable-artifact rule): a shared middleware instance
// serves mixed open/protected requests concurrently with correct routing.
func TestMethodAwareAuthMiddleware_Concurrent(t *testing.T) {
	open := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	protected := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_retrieve","arguments":{}}}`

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	mw := MethodAwareAuthMiddleware(keyringAuthn(t), next)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(2 * n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(open))
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("concurrent open: status %d, want 200", rec.Code)
			}
		}()
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(protected))
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("concurrent protected: status %d, want 401", rec.Code)
			}
		}()
	}
	wg.Wait()
}

// ---- FuzzClassifyRequest (criterion 7) -------------------------------------

// FuzzClassifyRequest asserts the classifier's invariants over arbitrary POST
// bodies: it never panics; a body that does not decode into a single JSON-RPC
// object with an allowlisted method is protected (open=false); and the peeked
// body is reconstituted byte-identical for the downstream handler.
func FuzzClassifyRequest(f *testing.F) {
	seeds := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_retrieve"}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
		`{"method":`,
		``,
		`null`,
		`{"method":"initialize"`,
		`  {"method":"tools/list"}  `,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		open := classifyRequest(req) // must never panic

		// Invariant: the reconstituted body is byte-identical.
		got, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read reconstituted body: %v", err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("reconstituted body differs: got %d bytes, want %d", len(got), len(body))
		}

		// Invariant: open ⇒ the body decodes into a single object whose method
		// is allowlisted. Contrapositive: a decode failure ⇒ protected.
		var msg struct {
			Method string `json:"method"`
		}
		decodeOK := json.Unmarshal(body, &msg) == nil
		if open {
			if len(body) > handshakePeekLimit {
				t.Errorf("open classification for an over-cap body (%d bytes)", len(body))
			}
			if !decodeOK {
				t.Error("open classification for a body that does not decode into a JSON-RPC object")
			}
			if !openMethods[msg.Method] {
				t.Errorf("open classification for a non-allowlisted method %q", msg.Method)
			}
		}
	})
}

// ---- helpers ----------------------------------------------------------------

// newHandshakeTestServer builds a real Stowage MCP server with a minimal
// Services (nil store — the handshake methods under test never reach a store).
func newHandshakeTestServer(t *testing.T) *server.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	srv, err := New(server.Info{Name: "stowage-handshake-test", Version: "0.0.1"}, &Services{
		Log:     log,
		ScopeFn: CtxScopeFn(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}
