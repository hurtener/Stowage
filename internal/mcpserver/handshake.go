package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/hurtener/stowage/internal/auth"
)

// handshakePeekLimit bounds how many bytes of a POST body classifyRequest reads
// to find the JSON-RPC method (D-152). MCP handshake frames (initialize,
// notifications/initialized, ping, tools/list) are tiny — a body larger than
// this cap is, by definition, not a handshake and is classified protected
// without decoding. 64 KiB is generous headroom over any real handshake frame.
const handshakePeekLimit = 64 << 10 // 64 KiB

// openMethods is the fixed, code-constant allowlist of identity-free JSON-RPC
// handshake methods served WITHOUT authentication in auth.mode=jwt (D-152).
// Every one is identity-independent by construction: the tool catalog is static
// (all tools registered unconditionally in New; ae9 views curate retrieval
// DATA, never the tool list), and initialize/initialized/ping carry no scoped
// data. Anything not in this map is protected — default-deny. Extending it is a
// conscious one-line change reviewed against D-152.
var openMethods = map[string]bool{
	"initialize":                true,
	"notifications/initialized": true,
	"ping":                      true,
	"tools/list":                true,
}

// classifyRequest reports whether r is an identity-free MCP handshake request
// that jwt mode serves unauthenticated (D-152). It is DEFAULT-DENY: any
// ambiguity — an unparseable body, a JSON-RPC batch array, a body over the peek
// cap, an unknown method, a non-handshake HTTP verb — classifies PROTECTED
// (open=false), so a classification error can never yield an authenticated
// scope. It is pure and allocates nothing beyond the peek buffer, so it is safe
// under concurrent use.
//
// It never consumes r.Body destructively: after peeking a POST body it
// reconstitutes r.Body (the peeked prefix followed by whatever remains unread)
// so the downstream SDK handler sees the byte-identical original body on BOTH
// branches (open and protected).
func classifyRequest(r *http.Request) (open bool) {
	switch r.Method {
	case http.MethodGet:
		// The streamable-HTTP SSE listen stream. It carries only
		// server-initiated frames; Stowage initiates none that carry scoped
		// data (D-152 point 1 — revisit if a future server-push feature would).
		return true
	case http.MethodDelete:
		// Session teardown, addressed by an unguessable Mcp-Session-Id; touches
		// no scoped data. A bearer-less client must be able to tear down the
		// session it opened bearer-less.
		return true
	case http.MethodPost:
		// Fall through to the body peek below.
	default:
		// Any other verb is not part of the handshake — fail closed.
		return false
	}

	if r.Body == nil {
		return false
	}

	// Peek at most handshakePeekLimit+1 bytes: the extra byte lets us detect a
	// body that overflows the cap without reading it all into memory.
	peeked, err := io.ReadAll(io.LimitReader(r.Body, handshakePeekLimit+1))
	// Reconstitute r.Body unconditionally (even on a read error) so the
	// downstream handler receives the full, byte-identical body: the peeked
	// prefix followed by any bytes still unread on the original body.
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(peeked), r.Body))
	if err != nil {
		return false
	}

	// Over the peek cap ⇒ too large to be a handshake frame ⇒ protected.
	if len(peeked) > handshakePeekLimit {
		return false
	}

	// A SINGLE JSON-RPC object with an allowlisted method is open. Decoding a
	// batch array, or any malformed body, into this struct fails ⇒ protected.
	var msg struct {
		Method string `json:"method"`
	}
	if jsonErr := json.Unmarshal(peeked, &msg); jsonErr != nil {
		return false
	}
	return openMethods[msg.Method]
}

// MethodAwareAuthMiddleware serves identity-free MCP handshake requests
// (classifyRequest) without authentication and delegates everything else to the
// existing AuthMiddleware(a, next) UNCHANGED — one enforcement implementation,
// zero duplicated auth logic (D-067). Wired only when auth.mode=jwt (D-152); in
// keyring mode the strict AuthMiddleware stays in place (a keyring client owns
// its static credential at connect time, so there is no reason to open its
// handshake).
//
// Open requests go straight to next with NO scope on context — safe because the
// open methods never reach a tool handler, and CtxScopeFn still fails loudly if
// any protected code path were somehow reached without scope (defense in
// depth). Protected requests take the existing AuthMiddleware path byte-for-byte
// (same 401 "authorization required" / 403 "forbidden" split, same scope +
// key-id context injection, same X-Harbor-Session handling). Classification
// ambiguity always falls through to the protected path — fail closed.
func MethodAwareAuthMiddleware(a *auth.Authenticator, next http.Handler) http.Handler {
	protected := AuthMiddleware(a, next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if classifyRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})
}
