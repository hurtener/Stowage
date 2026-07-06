# Phase ae11 — Method-aware MCP handshake auth (open connect, per-call bearer)

- **Status:** approved
- **Owning subsystem(s):** `internal/mcpserver` (the method-aware HTTP auth middleware); `cmd/stowage` (mode-keyed wiring); `internal/auth` untouched (the `Authenticator` core is reused, never forked)
- **RFC sections:** §5.5 (identity & auth — the JWT triple rides the bearer), §5.2 (MCP-over-HTTP transport posture), §9.5 (one logic core, D-067/D-073)
- **Depends on phases:** ae7 (JWT verifier — the per-call bearer this phase relies on), ae8 (effective-scope resolver — per-call identity resolution), ae2b/D-140 (MCP identity from `_meta`/JWT only)
- **Informing briefs:** 01, 02 (surface-sprawl cautionary tales → ONE request classifier feeding the ONE existing `auth.Authenticator`; no second verify path, no per-method auth forks scattered through handlers)

## Goal

When this phase is done, an MCP host that attaches connections **user-agnostically**
(no per-user credential exists at connect time) and injects a per-user bearer
**only on tool calls** can complete the streamable-HTTP handshake against a
Stowage MCP endpoint running `auth.mode=jwt`. Today `mcpserver.AuthMiddleware`
wraps the entire streamable-HTTP handler and 401s a bearer-less `initialize`
before the SDK ever sees it — a transport-level gate that contradicts the
settled per-call identity model (D-135/D-137/D-140: identity arrives per call,
from the verified JWT claim and host `_meta`, never from the connection). This
phase makes the HTTP MCP middleware **JSON-RPC-method-aware and default-deny**
in jwt mode: a small fixed allowlist of identity-free handshake methods is
served unauthenticated; **every** scoped operation (`tools/call`, resource
reads) keeps requiring the per-call bearer exactly as today. Keyring mode is
byte-identical to today. No new config keys.

**Motivating consumer (consumer-neutral, per repo hygiene):** an ecosystem MCP
host whose runtime attaches MCP connections once at the agent level (no user in
scope), then mints and injects a per-user bearer lazily on each `tools/call`
via token exchange. Its dial sequence is: `initialize` → `notifications/initialized`
→ `tools/list` (all bearer-less) → later, per-user-bearer `tools/call`.

## Brief findings incorporated

- **01/02 (surface sprawl):** the predecessor systems forked auth logic per
  surface and per path. ae11 adds exactly one new decision point — a request
  classifier in front of the ONE existing `AuthMiddleware`/`auth.Authenticator`
  chain — and zero changes inside handlers, the authenticator, or the REST API
  middleware. The REST API surface (`internal/api`) is untouched: it has no
  handshake concept, so nothing there changes.

## Findings I'm departing from

- None. D-152 (filed with this phase) settles the new decision: the MCP
  connect-time handshake is identity-free in jwt mode; the per-call bearer is
  the sole identity channel. This *completes* D-135/D-137/D-140 rather than
  departing from them.

## Design

### Request classification (`internal/mcpserver/handshake.go`)

A pure function classifies an inbound HTTP request as **open** (served without
auth) or **protected** (full `AuthMiddleware` behavior):

```go
// classifyRequest reports whether r is an identity-free handshake request
// that jwt mode serves unauthenticated. It never consumes r.Body destructively:
// a peeked POST body is reconstituted onto r.Body for the downstream handler.
func classifyRequest(r *http.Request) (open bool)
```

Classification rules (default-deny — anything not listed is protected):

1. **POST** with a body that parses (within a peek cap) as a **single**
   JSON-RPC object whose `method` is one of the fixed allowlist
   `openMethods = {"initialize", "notifications/initialized", "ping", "tools/list"}`
   → **open**. The tool catalog is static and identity-independent (all tools
   are registered unconditionally in `mcpserver.New`; ae9 views curate
   retrieval *data*, never the tool list), so `tools/list` leaks nothing scoped.
2. **GET** (the streamable-HTTP SSE listen stream) → **open**. It carries only
   server-initiated frames; Stowage initiates none that carry scoped data. Any
   future server-push feature that would emit scoped frames on this stream
   must revisit D-152 explicitly (noted in the decision entry).
3. **DELETE** (session teardown) → **open**. Ends a session addressed by an
   unguessable `Mcp-Session-Id`; touches no scoped data. A bearer-less client
   must be able to tear down the session it opened bearer-less.
4. Everything else — `tools/call`, resource/prompt methods, a JSON **array**
   (batch), an unparseable or oversized body, an unknown method, any other
   HTTP verb — → **protected**.

Body peek mechanics: read at most `handshakePeekLimit` (64 KiB — handshake
frames are tiny; a body larger than the cap is by definition not a handshake)
via `io.LimitReader`, decode with stdlib `encoding/json` into a minimal
`struct{ Method string }`, then reconstitute `r.Body` as
`io.NopCloser(io.MultiReader(bytes.NewReader(peeked), r.Body))` so the SDK
handler sees the full original body. Decode failure, arrays, and cap overflow
all classify **protected** (fail-closed). The classifier allocates nothing per
request beyond the peek buffer and is safe under concurrent use (no shared
state).

### Middleware (`internal/mcpserver/server.go` or `handshake.go`)

```go
// MethodAwareAuthMiddleware serves identity-free MCP handshake requests
// (classifyRequest) without authentication and delegates everything else to
// AuthMiddleware(a, next) unchanged. Wired only when auth.mode=jwt (D-152).
func MethodAwareAuthMiddleware(a *auth.Authenticator, next http.Handler) http.Handler
```

- Open requests go straight to `next` with **no scope on context** — safe
  because open methods never reach a tool handler; `CtxScopeFn` still fails
  loudly if any protected code path were somehow reached without scope
  (defense in depth, unchanged).
- Protected requests take the *existing* `AuthMiddleware` path byte-for-byte:
  same 401 (`"authorization required"`) / 403 (`"forbidden"`) split, same
  scope + keyID context injection, same `X-Harbor-Session` handling. The
  middleware wraps `AuthMiddleware(a, next)` internally — one enforcement
  implementation, zero duplication (D-067 discipline).

### Wiring (`cmd/stowage/main.go`, `runMCP`)

```go
if cfg.Auth.Mode == string(auth.ModeJWT) {
    handler = mcpserver.MethodAwareAuthMiddleware(authn, handler)
} else {
    handler = mcpserver.AuthMiddleware(authn, handler) // keyring: unchanged
}
```

Keyring mode keeps the strict gate: a keyring client owns its static credential
at connect time, so there is no reason to open its handshake, and existing
deployments observe zero behavior change. Stdio mode is untouched (no
per-request auth by design, D-020/AC-4).

**As-built deviation — jwt-mode MCP-over-HTTP is stateless (required, not a
knob).** The §17 integration test surfaced a wiring gap the plan's sketch did
not anticipate: the go-sdk's **stateful** streamable transport binds a session's
tool-handler context to the request that *established* the session — the
bearer-less, open `initialize` (`server.Connect(req.Context(), …)`). It caches
that request's (empty) scope for the session's whole life, so a per-call bearer
injected on a later `tools/call` POST lands on a request context the handler
never sees, and the handler resolves "no authenticated scope" — criterion 4/5
fail. This directly contradicts D-152's own invariant ("sessions never cache a
scope; auth is per-HTTP-request"), so **statelessness is required by D-152, not a
deviation from it**: in jwt mode both MCP-over-HTTP wiring points build the
transport with `server.HTTPOptions{Stateless: true}`, so each POST resolves its
own identity. Security posture is unchanged (a non-nil `*HTTPOptions` whose
`Security` is the zero value still resolves to `DefaultHTTPSecurity`), and
Stowage's MCP surface is tools-only (no server-initiated
sampling/elicitation/roots), so statelessness costs nothing. Consequence: in jwt
mode no `Mcp-Session-Id` is issued (criterion 1's "issued in stateful mode"
parenthetical applies only to keyring mode); the client still composes the full
dial sequence over one reused transport (criterion 5). Keyring mode keeps the
stateful default (`nil`) — byte-identical to today.

**As-built deviation — both MCP-over-HTTP wiring points updated.** The plan named
only `runMCP`. `stowage serve`'s co-mounted MCP port (D-074) is the *same*
MCP-over-HTTP surface and must behave identically, so the mode-keyed middleware
and stateless selection are applied at both call sites via two shared helpers
(`mcpAuthHandler`, `mcpHTTPOptions`) — shipping a state where `stowage mcp --http`
opens the handshake but the co-mounted port does not would be exactly the
surface-drift CLAUDE.md §6 warns against.

### RFC touch-up (same PR)

Add one sentence to the RFC auth posture: in jwt mode the MCP-over-HTTP
connect-time handshake (`initialize`, `notifications/initialized`, `ping`,
`tools/list`, the SSE GET leg, session DELETE) is served unauthenticated; every
`tools/call` and resource operation requires the per-call bearer (D-152).

**As-built deviation — RFC section reference.** The plan cited "§5.5 (identity &
auth)", but RFC §5.5 is "Branches"; the RFC's actual auth-posture statement is
the "Auth v1:" paragraph under §9.1 (HTTP/MCP surface). Per CLAUDE.md §2 (the RFC
wins; fix the plan), the amendment landed on that paragraph.

### Concurrency & failure posture

- The classifier and middleware are stateless; safe under concurrent use.
- Fail-closed everywhere: any ambiguity in classification → protected → the
  existing 401/403 path. There is no code path where a classification error
  yields an *authenticated* scope.
- P3 unchanged: scope still enters context only via the verified credential;
  store-layer scoping untouched.

## Files added or changed

```text
internal/mcpserver/handshake.go          # classifier + MethodAwareAuthMiddleware
internal/mcpserver/handshake_test.go     # unit + middleware tests + fuzz target
cmd/stowage/main.go                      # mode-keyed middleware selection in runMCP
test/integration/mcp_handshake_test.go   # host dial-sequence integration test (§17)
scripts/smoke/phase-ae11.sh              # smoke checks
RFC-001-Stowage.md                       # §5.5 one-sentence amendment (D-152)
docs/decisions.md                        # D-152
docs/glossary.md                         # "handshake methods (MCP)"
docs/plans/ae-implementation-roadmap.md  # post-track follow-up entry
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| *(none)* | — | Behavior is keyed off the existing `auth.mode`; the allowlist is a code constant. D-034: no knob without a consumer — none exists for tuning the allowlist. |

## Acceptance criteria (binding)

1. **jwt mode, bearer-less `initialize` succeeds** — a POST with no
   `Authorization` header completes the MCP initialize handshake (HTTP 200,
   valid result, `Mcp-Session-Id` issued in stateful mode).
2. **jwt mode, bearer-less `tools/list` succeeds** and returns the same static
   catalog an authenticated caller sees (tool names + count identical).
3. **jwt mode, bearer-less `tools/call` is rejected 401** with body
   `authorization required` — the exact status/body contract the strict
   middleware emits today; a bad/expired token on `tools/call` is 403.
4. **jwt mode, authenticated `tools/call` resolves per-call identity
   end-to-end**: a valid JWT's `(tenant, user)` reaches the store-layer scope
   (proven by a cross-user isolation negative: user A's bearer cannot read
   user B's rows through the opened-handshake session).
5. **The full host dial sequence composes**: bearer-less `initialize` →
   `notifications/initialized` → `tools/list`, then per-user-bearer
   `tools/call` on the *same* session succeeds — one integration test replays
   exactly this sequence against a real HTTP server, real sqlite store, and a
   static JWKS file.
6. **keyring mode is byte-identical to today**: bearer-less `initialize` is
   still 401; the existing MCP smoke/tests for keyring auth pass unchanged.
7. **Default-deny holds**: a JSON-RPC batch array, an unknown method, an
   unparseable body, and a body over the peek cap are all classified
   protected (401 without bearer) — proven by table test; the classifier
   fuzz target (`FuzzClassifyRequest`) asserts no panic and
   unparseable ⇒ protected, with a seed corpus.
8. **Downstream body integrity**: after an open-classified peek, the SDK
   handler receives the byte-identical request body (round-trip test).
9. `scripts/smoke/phase-ae11.sh` reports `OK ≥ 8`, `FAIL = 0`; prior phases'
   smoke scripts still pass.

## Smoke script

`scripts/smoke/phase-ae11.sh` — boots the built binary in jwt mode with a
static JWKS file and sqlite store, then, with `curl`:

- bearer-less `initialize` → 200 + session id
- bearer-less `notifications/initialized` → accepted
- bearer-less `tools/list` → catalog with `memory_retrieve` present
- bearer-less `tools/call memory_retrieve` → 401 `authorization required`
- test-signed JWT `tools/call` → 200 (mock gateway / degraded path is fine)
- batch-array POST without bearer → 401
- reboot in keyring mode: bearer-less `initialize` → 401
- `stowage serve` zero-config start unaffected (existing invariant re-checked)

## Test plan

- **Unit (table):** `classifyRequest` over every rule row (open methods, each
  HTTP verb, batch, malformed, oversized, unknown method, missing method).
- **Middleware:** httptest server, jwt + keyring modes, criteria 1–3, 6, 8;
  concurrent-use test under `-race` (reusable artifact rule, §5).
- **Fuzz:** `FuzzClassifyRequest` — seed corpus of real MCP frames; invariants:
  never panics, decode-failure ⇒ protected, body reconstitution preserves bytes.
- **Integration (§17):** `test/integration/mcp_handshake_test.go` — real
  sqlite driver, real JWKS static file, ae7's test signer; replays the full
  dial sequence (criterion 5) + the cross-user isolation negative
  (criterion 4) + one failure mode (expired token on `tools/call` → 403).
  Runs under `-race`.
- **Coverage:** `internal/mcpserver` stays ≥ its configured band; new file
  fully covered by the table/fuzz/middleware tests.

## Risks & mitigations

- **SDK dial shape drift** (go-sdk changes what a client sends pre-auth, e.g.
  an eager `resources/list`): default-deny means a new pre-auth method fails
  loudly (401) rather than leaking; the allowlist is one constant to extend
  consciously. The integration test replays the real SDK client dial, so a
  dockyard/go-sdk upgrade that changes the sequence breaks the test, not
  production silently.
- **Body-peek breaking streaming/large POSTs:** open methods are tiny; the cap
  classifies anything larger as protected and passes the body through
  untouched on both branches (round-trip test, criterion 8).
- **Session fixation worry** (open `initialize` mints sessions): sessions are
  identity-free by design here — auth is per-HTTP-request, so a session never
  caches a scope; an attacker with a session id still cannot execute a scoped
  call without a valid bearer. Noted in D-152.
- **Anonymous session churn** (unauthenticated connects consuming session
  state): acceptable for v1 — the SDK bounds per-session state and idle
  sessions expire; if a deployment needs connect-time gating later, D-152
  records that it must ride a non-`Authorization` header as a separate,
  explicitly-added concern (never the bearer channel).

## Glossary additions

- **handshake methods (MCP):** the fixed, identity-free JSON-RPC methods
  (`initialize`, `notifications/initialized`, `ping`, `tools/list`) plus the
  transport's SSE GET leg and session DELETE, served without authentication
  in `auth.mode=jwt` (D-152). Everything else on the MCP surface requires the
  per-call bearer.

## Decisions filed

- D-152: MCP connect-time handshake is identity-free in jwt mode; the
  per-call bearer is the sole identity channel on the MCP-over-HTTP surface.
