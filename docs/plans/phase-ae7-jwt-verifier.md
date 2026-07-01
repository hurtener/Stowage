# Phase ae7 — Harbor-aligned JWT verifier (second mode)

- **Status:** implemented (feat/ae-wave-2)
- **Owning subsystem(s):** `internal/auth` (a new verify-never-mint `Validator` + `KeySet`/`JWKSKeySet` + a mode-aware `Authenticator` resolver); `internal/config` (an `auth` section); `internal/api` + `internal/mcpserver` (the two bearer seams become thin callers of the resolver); `cmd/stowage` (boot-time wiring)
- **RFC sections:** §5.5 (identity & auth — "JWT, asymmetric algorithms only; the triple (tenant, user, session) is in the JWT claims"), §9.5 (one logic core, D-067/D-073); D-030 (runtime key store)
- **Depends on phases:** the existing keyring auth seam (`internal/auth.Verify`, `store.Keys() auth.Keyring`, D-030); `repos/Harbor` in the session (PREREQ-2 — the port source). **Independent of ae1/ae2** (`agent_id` never travels in the JWT). This phase makes a verified `user`/`session` claim **exist** on the request scope and is the C4 gate that unblocks ae2b.
- **Informing briefs:** 02 (CC-memory predecessor — surface-sprawl cautionary tale → **one** `Validator` core, two thin middleware callers, not a second verify path per surface), 06 (mempalace — the config knob-paralysis lesson → every new `auth.*` knob is D-034-complete with a tuned default and zero-config start preserved). The authoritative *shape* source is not a research brief but Harbor's on-disk verifier (`repos/Harbor/internal/protocol/auth/{auth,jwks,middleware}.go`), copyable per PREREQ-2 — stated plainly so the brief-inheritance is honest (§16).

> **Checkpoint reconciliation (D-150).** The token-derived `Scope{Tenant,User,Session}`
> this plan builds is an *identity representation*. Per **D-150**, session must **not**
> become a hard retrieval read predicate: `X-Harbor-Session` / the token `session` claim
> resolve the effective session **value**, but the retrieval/browse read path routes it to
> the relevance sink (`Request.SessionID`) and leaves `Scope.Session` empty (ae8's resolver
> owns that routing), so cross-session recall is preserved. Writes remain session-stamped.

## Goal

When this phase is done a Harbor-minted JWT authenticates against Stowage. A new
**verify-never-mint** JWT `Validator` + JWKS `KeySet` live in `internal/auth`,
reimplemented verbatim from Harbor's verifier (Harbor's `internal/protocol/auth`
is cross-module `internal/`, so Go forbids importing it — the shape is copied, not
linked). The Validator is selected by a new `auth.mode` config knob whose default
is the existing static **keyring**, so zero-config start is byte-identical to today.
In `jwt` mode a bearer JWT is verified against a configured JWKS/static key set,
its mandatory `tenant`/`user`/`session` triple is extracted onto the request
`identity.Scope`, and the `X-Harbor-Session` header replaces the token's session
claim (Harbor parity, D-137). Both HTTP-transport seams — the REST API middleware
and the MCP-over-HTTP middleware — become thin callers of **one** `auth.Authenticator`
resolver (D-067). Stowage never signs; the test signer that mints golden fixtures
lives in test code only (L1).

## Brief findings incorporated

- **02 (CC-memory surface sprawl):** the predecessor forked logic per surface. ae7
  builds a single `auth.Authenticator` (mode → `Scope`+`Role`) that both the API and
  MCP middlewares call; there is no second verify implementation and no per-surface
  JWT parsing.
- **06 (mempalace / knob-paralysis):** the JWT surface adds six operator knobs. Each
  ships D-034-complete (tuned default, present in every profile's effective config,
  docs, `allKeys`/get/set/explain, validation), and the whole set defaults to the
  keyring so `stowage serve` + one gateway secret still boots with no auth config.

## Findings I'm departing from

Harbor's verifier is the port target, but four points are **not** byte-verbatim
because Stowage's surrounding types differ. These are faithful-port caveats
(ratified by D-135's "reimplement... in `internal/auth`" mandate), recorded here so
the divergence is explicit, not silent:

- **Claim parsing is verbatim (no departure) — noted to prevent a wrong "fix."**
  Harbor uses `jwt.MapClaims` (a `map[string]any`) on **both** sign and verify sides
  (`cmd/harbor/tokenclaims.go:42`, `auth.go:421` `claims := jwt.MapClaims{}`). ae7
  parses into a `jwt.MapClaims` and extracts by key — it does **not** introduce a
  typed `Claims` struct. A reviewer tempted to "clean this up" into a struct is
  reintroducing drift from the port source.
- **Identity type mapping.** Harbor's `Verified.Identity` is `identity.Identity{TenantID,UserID,SessionID}`
  validated by `identity.Validate`. Stowage has no `Identity` type — only
  `identity.Scope{Tenant,Project,User,Session}` whose `Validate()` requires **only**
  `Tenant` (`internal/identity/identity.go:65`). ae7 maps `tenant`/`user`/`session`
  claims onto a `Scope` and enforces the **mandatory triple** inline in the Validator
  (each of tenant/user/session must be a non-empty string claim, else
  `ErrIdentityClaimMissing`), because `Scope.Validate()` alone would accept a
  user/session-less token. `Project` is left empty — the JWT carries no project claim
  (charter: project is a host-routing dimension, `_meta` home, not an auth claim).
- **Drop the mandatory `WithRedactor` + `WithEventBus` coupling.** Harbor's
  `NewValidator` fails closed without an `audit.Redactor` and can take an
  `events.EventBus` (`auth.go:250,332`). Stowage's `internal/auth` has neither
  dependency. ae7's audit emit is a plain `slog.Warn` carrying only structural,
  non-secret fields (`kid`, `iss`, `reason`) — **never** the raw token or the claims
  body (CLAUDE.md §7). No redactor is made mandatory; the rejection log is redaction-safe
  by construction. (Wiring `internal/events` into auth is deferred; not needed for the
  verify contract.)
- **Role vs scopes (gotcha #7).** The keyring's `requireAdmin` gate branches on
  `Key.Role` (agent/admin, `internal/api/auth.go:42`); a JWT carries `scopes`, not a
  role. ae7 maps **`scopes` containing `"admin"`** (Harbor's own convention — see the
  claim example in `auth.go:51`) to `RoleAdmin`, else `RoleAgent`. No new knob: the
  mapping reuses the token's scope set. Documented in the plan; a golden test pins it.

## Design

### The one logic core: `auth.Authenticator`

New file `internal/auth/authenticator.go`. This is the D-067 core both HTTP seams
call; the middlewares are thin.

```go
// Mode selects the verify path. Keyring is the zero-config default.
type Mode string
const (
    ModeKeyring Mode = "keyring" // static store-backed API keys (D-030) — default
    ModeJWT     Mode = "jwt"     // Harbor-aligned JWT verification (this phase)
)

// Authenticator resolves a request's bearer credential into an identity Scope +
// Role, by whichever mode is configured. Immutable after construction; safe for
// concurrent use (both middlewares share one).
type Authenticator struct {
    mode      Mode
    keyring   Keyring    // ModeKeyring
    validator Validator  // ModeJWT
}

// Authenticate turns the raw Authorization header value and the optional
// X-Harbor-Session header into the request Scope + Role. Never logs credentials.
//   - ModeKeyring: auth.Verify(keyring, token) → Scope{Tenant}, Role from Key.Role.
//   - ModeJWT:     validator.Validate(ctx, token) → Scope{Tenant,User,Session};
//     X-Harbor-Session (when non-empty) REPLACES the token's session claim (D-137);
//     Role = admin iff scopes⊇{"admin"} else agent.
func (a *Authenticator) Authenticate(ctx context.Context, authz, sessionHdr string) (identity.Scope, Role, error)
```

In `ModeJWT`, `Authenticate` sets the **full** `Scope{Tenant,User,Session}` from the
verified claims — this is the phase's core deliverable: the verified `user`/`session`
claim now **exists** on the request scope. Whether every read handler *consumes*
`Scope.User`/`Session` (versus today's arg-derived scope) is ae8's effective-scope
job; ae7 lands the trustworthy source. `X-Harbor-Session` is ported verbatim from
Harbor's `middleware.go:143` so session is always per-call (D-137).

### The Validator (verbatim port of Harbor `auth.go`)

New file `internal/auth/validator.go`. Reimplements Harbor's `Validator`/`KeySet`/`Verified`
shape:

```go
var AllowedAlgorithms = []string{"RS256","RS384","RS512","ES256","ES384","ES512"} // asymmetric-only

type KeySet interface { KeyByID(kid string) (key crypto.PublicKey, alg string, err error) }

type Verified struct {
    Scope   identity.Scope // Tenant/User/Session from the mandatory triple (Project empty)
    Scopes  []string       // verified scope claim; may be empty
    Subject string         // sub, audited; sub==user per Harbor
    Issuer  string         // iss, audited
}

type Validator interface { Validate(ctx context.Context, rawToken string) (Verified, error) }
func NewValidator(keys KeySet, opts ...Option) (Validator, error) // WithIssuer/WithAudience/WithClock/WithLogger
```

Ported verbatim (the belt-and-braces security posture is the whole point):

- **`jwt.NewParser(jwt.WithValidMethods(AllowedAlgorithms), jwt.WithoutClaimsValidation())`.**
  `WithValidMethods` rejects `HS*`/`none` at the **parser**, before the keyfunc — so
  algorithm-confusion (an `HS256` token signed with the RSA public key as the HMAC
  secret) is structurally impossible. Own `exp`/`nbf` checks run against an injectable
  clock (`WithClock`, L1).
- **keyfunc belt-and-braces:** re-check `isAllowedMethod(t.Method)`; reject a `kid`
  whose resolved `alg` disagrees with the header `alg`; final type-switch that the
  resolved key is `*rsa.PublicKey`/`*ecdsa.PublicKey` (a symmetric key is a confusion
  vector). `ErrJWKSStale` propagates un-masked (fail-closed, distinct from `ErrUnknownKey`).
- **Issuer exact-match; audience containment-match** (`audienceContains` — `aud` may be
  a `string` or `[]string` per RFC 7519; empty configured value disables the check —
  D-136). `exp` mandatory (no-`exp` ⇒ expired); `nbf` optional.
- **Mandatory triple** extracted from the map and enforced inline (the identity-type
  departure above). `sub`/`iss` audited. Ten typed sentinels
  (`ErrTokenMissing`/`Malformed`/`AlgNotAllowed`/`SignatureInvalid`/`TokenExpired`/`TokenNotYetValid`/`UnknownKey`/`IdentityClaimMissing`/`AudienceMismatch`/`IssuerMismatch`)
  plus `ErrJWKSStale`; `mapParserError` translates golang-jwt errors onto them.

### The JWKS KeySet + static KeySet (verbatim port of Harbor `jwks.go`)

New file `internal/auth/jwks.go`. `JWKSKeySet` backed by a `JWKSSource{URL,File}`
(exactly one), stdlib-only JWK parse (RSA `n`/`e`, EC `crv`/`x`/`y`; **`oct`
symmetric key rejected**), TTL cache + single-flight refresh + **max-stale ceiling**
(`ErrJWKSStale` fail-closed), 1 MiB body cap, ≥2048-bit RSA floor, on-curve EC check.
**Fails loud on first load** (a source yielding zero usable asymmetric keys is a
construction error). No background goroutine — refresh is on-demand under the
caller's deadline (nothing to leak). A small `staticKeySet` (new file
`internal/auth/statickeyset.go`, a `map[kid]{key,alg}`) backs the `jwks.file` path
and the test signer; it satisfies the same `KeySet` interface.

### Config-selected mode + boot wiring

New `auth` config section (below). `cmd/stowage/main.go` builds **one**
`*auth.Authenticator` at boot from `cfg.Auth` + `stk.Store.Keys()`:

- `ModeKeyring` (default): `Authenticator{mode, keyring: st.Keys()}`. No Validator built.
- `ModeJWT`: construct the `KeySet` (`NewJWKSKeySet` for `jwks.url`/`file`, synchronous
  fetch) then `NewValidator(keys, WithIssuer, WithAudience)`. A JWKS-unreachable boot
  **fails loud** (D-147) — Stowage does not boot into a mode it cannot enforce.

Then both seams become thin callers:

- `internal/api/auth.go` — `authMiddleware` calls `s.authn.Authenticate(...)` instead
  of `auth.Verify` directly; `requireAdmin` branches on the returned `Role`. In
  `ModeJWT` it synthesizes a back-compat `*auth.Key{TenantID, Role}` onto ctx (so
  `keyFromContext`/`scopeFromRequest` keep compiling) **and** sets the full verified
  `Scope` via `identity.WithScope`.
- `internal/mcpserver/server.go` — add `AuthMiddleware(a *auth.Authenticator, next)`;
  `KeyringMiddleware` becomes a thin wrapper (`AuthMiddleware` with a keyring-only
  authenticator) for back-compat. `CtxScopeFn` is unchanged (it already reads the ctx
  `Scope`; in `ModeJWT` that scope now carries the verified user/session).

`go.mod` promotes `github.com/golang-jwt/jwt/v5 v5.3.1` from `// indirect` (line 59)
to a direct require.

### Error responses stay surface-specific

The API writes `respondJSON(...errBody...)`; the MCP seam writes `http.Error`. ae7
keeps each surface's existing error-body style (both 401/403) — only the *reason*
sentinel is shared. A wire-safe reason string (sentinel name only, never the wrapped
`kid`/detail — CLAUDE.md §7 rule 7) is derived by a ported `reasonForWire`.

## Files added or changed

```text
internal/auth/authenticator.go          # NEW — Mode, Authenticator, Authenticate (the D-067 core)
internal/auth/validator.go              # NEW — AllowedAlgorithms, sentinels, KeySet, Verified, Validator, NewValidator, Validate, mapParserError, audienceContains, extractScopes
internal/auth/jwks.go                   # NEW — JWKSSource, JWKSKeySet, NewJWKSKeySet, KeyByID, parse/resolveKey/parseRSAJWK/parseECJWK, ErrJWKSStale/Source/NoUsableKeys
internal/auth/statickeyset.go           # NEW — staticKeySet (map[kid]{key,alg}) for jwks.file + tests
internal/auth/validator_test.go         # NEW — golden negatives + test signer (RSA/EC) + injectable clock (L1)
internal/auth/jwks_test.go              # NEW — JWKS parse/refresh/max-stale/oct-reject; test signer reused
internal/auth/authenticator_test.go     # NEW — keyring vs jwt resolve, X-Harbor-Session replace, role↔scope map
internal/config/config.go               # CHANGED — AuthConfig; allKeys; Validate; defaults
internal/api/auth.go                    # CHANGED — authMiddleware calls Authenticator; requireAdmin via Role
internal/api/server.go                  # CHANGED — Server carries *auth.Authenticator (built in main, injected)
internal/mcpserver/server.go            # CHANGED — AuthMiddleware(authenticator,next); KeyringMiddleware thin wrapper
cmd/stowage/main.go                     # CHANGED — build one *auth.Authenticator at boot (fail-loud in jwt mode); wire both seams
go.mod                                  # CHANGED — golang-jwt/jwt/v5 v5.3.1 indirect → direct
scripts/smoke/phase-ae7.sh              # NEW
test/integration/auth_jwt_test.go       # NEW — real JWKS (httptest) + test signer, keyring↔jwt, ≥1 failure mode, -race (§17)
docs/decisions.md                       # (unchanged — D-136/D-147 pre-filed in the Wave-0 ledger, 89b54e1)
docs/glossary.md                        # (unchanged — the six terms pre-filed in the Wave-0 ledger, 89b54e1)
docs/plans/README.md                    # CHANGED — ae* track registration line (if not already added by an earlier ae phase)
```

## Config keys added

All under a new `auth` section on `Config`. None is a secret (issuer/audience/JWKS
URL/file are not credentials — no `env.VAR` indirection needed). D-034: every key has
a tuned default that holds across **all three** profiles (`assistant`/`coding-agent`/`fleet`
— none overrides it, matching the `vindex.driver` precedent), docs, `allKeys`/get/set/explain,
and validation.

| Key | Default | Notes |
|-----|---------|-------|
| `auth.mode` | `keyring` | `keyring` \| `jwt`. Keyring is the zero-config default → boot unchanged. |
| `auth.issuer` | `""` | Expected `iss`, **exact-match**. Empty disables the issuer check. |
| `auth.audience` | `""` | Expected `aud`, **containment-match** (D-136). Empty disables the check → one Harbor token verifies at both services. |
| `auth.algorithms` | `""` | Comma-separated subset of the asymmetric allowlist (e.g. `RS256,ES256`). Empty ⇒ all six. Validated as a subset; a non-asymmetric entry (`HS*`/`none`) is rejected at config load. |
| `auth.jwks.url` | `""` | JWKS endpoint. In `jwt` mode **exactly one** of `url`/`file` must be set. |
| `auth.jwks.file` | `""` | Local JWK Set path (air-gapped / out-of-band sync). |
| `auth.jwks.max_stale` | `3600` | Seconds a cached snapshot is honored without a successful refresh before `KeyByID` fails **closed** (`ErrJWKSStale`). `>0`; the ceiling bounds — does not make instantaneous — revocation. |

Validation (`Config.Validate`, fail-loud at boot): `mode ∈ {keyring,jwt}`; when
`mode=jwt` ⇒ exactly one of `jwks.url`/`jwks.file` non-empty, `max_stale>0`, and every
`algorithms` entry ∈ `AllowedAlgorithms`. TTL and min-refresh interval stay
package-internal defaults (like `BufferTriggers`) — not operator knobs — to keep the
surface bounded (06).

## Acceptance criteria (binding)

1. **Verbatim verify posture.** `internal/auth/validator.go` defines `AllowedAlgorithms`
   = the six asymmetric algs only; the parser is built with
   `jwt.WithValidMethods(AllowedAlgorithms)` + `jwt.WithoutClaimsValidation()`; the
   keyfunc re-checks the method and rejects a non-`*rsa.PublicKey`/`*ecdsa.PublicKey`.
   A golden suite (test signer + injectable clock) verifies a Harbor-shaped token and
   **rejects every negative case**: wrong alg (`HS256`/`none`), bad signature, expired
   (`exp` past), not-yet-valid (`nbf` future), missing `exp`, and each missing element
   of the `tenant`/`user`/`session` triple → the matching typed sentinel.
2. **Test signer is test-only (L1).** No non-`_test.go` file in `internal/auth`
   references `rsa.PrivateKey`/`ecdsa.PrivateKey`/`SignedString`/`jwt.MapClaims` for
   *minting* — Stowage never signs. A grep gate asserts the signer lives only in
   `*_test.go`.
3. **JWKS fail-loud then fail-closed (D-147).** `NewJWKSKeySet` returns an error on a
   first-load failure and on a set with zero usable asymmetric keys; an `oct` symmetric
   key is rejected. A snapshot aged past `max_stale` makes `KeyByID` return
   `ErrJWKSStale` (fail closed), proven by a clock-driven test.
4. **Mode config-selected, keyring default, zero-config preserved.** `auth.mode`
   defaults to `keyring`; `stowage serve` with no `auth` config boots and authenticates
   store keys exactly as today (smoke-tested). Flipping `auth.mode=jwt` with no JWKS
   source fails validation at boot (fail-loud, not silent keyring fallback — D-147).
5. **`aud` containment (D-136).** `audienceContains` accepts `aud` as `string` or
   `[]string`; an empty configured `auth.audience` disables the check; a Harbor token
   whose `aud` contains Stowage's configured audience verifies. Unit-tested.
6. **X-Harbor-Session replace (D-137).** In `jwt` mode a non-empty `X-Harbor-Session`
   header replaces the token's `session` claim on the resulting `Scope` while
   `Tenant`/`User` stay token-verified (a request can never widen tenant/user). Tested.
7. **One core, thin surfaces (D-067).** Both `internal/api` and `internal/mcpserver`
   authenticate via `auth.Authenticator.Authenticate` — grep asserts neither calls
   `validator.Validate` nor re-parses a JWT directly. A **parity test** proves the same
   token/keyring credential resolves to the same `Scope`+`Role` through both seams.
8. **Role↔scope mapping.** In `jwt` mode `scopes⊇{"admin"}` ⇒ `RoleAdmin` (admin-gated
   routes pass), else `RoleAgent` (admin routes 403). Golden-tested.
9. **Knobs D-034-complete.** `auth.mode`/`issuer`/`audience`/`algorithms`/`jwks.url`/`jwks.file`/`jwks.max_stale`
   are in `allKeys`, get/set/explain, validated, present in every profile's effective
   config with the tuned default, documented, and smoke-checked.
10. **`go.mod` promotion.** `golang-jwt/jwt/v5 v5.3.1` is a direct require (not `// indirect`).
11. **Integration (§17).** A real-driver test (store keyring for keyring mode; a test
    signer + a real `JWKSKeySet` served over `httptest` for jwt mode) proves
    scope/identity propagation to the store and ≥1 failure mode (expired token or stale
    JWKS → request rejected, ingest/read path never serves a wrong-scope row) under `-race`.

## Smoke script

`scripts/smoke/phase-ae7.sh` — SKIPs gracefully until the files exist; then:
- `internal/auth/validator.go` defines `Validator`, `NewValidator`, `AllowedAlgorithms`
  with RS/ES only and **no** `HS`/`none`.
- test signer is test-only (no `SignedString`/`PrivateKey` in non-`_test.go` auth files).
- `auth.mode` present in config with default `keyring` (`stowage config get auth.mode`).
- the seven `auth.*` keys appear in `stowage config explain`.
- `go.mod` shows `golang-jwt/jwt/v5` as a direct (non-`// indirect`) require.
- both middlewares reference `Authenticator` (no direct `validator.Validate` in surfaces).
- `go test ./internal/auth/ -run 'Validator|JWKS|Authenticator'` passes; the integration
  test passes when present.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Golden/unit (`validator_test.go`):** a test signer mints RSA (RS256/384/512) and
  EC (ES256/384/512) tokens with an injectable clock; positive verify; every negative
  (wrong alg, tampered sig, expired, nbf-future, no-exp, missing each triple element,
  issuer mismatch, audience mismatch/containment, unknown kid, kid↔alg disagreement,
  symmetric key). `HS256`-signed-with-RSA-public-key and `alg:none` are pinned rejected
  (the algorithm-confusion CVE family).
- **JWKS (`jwks_test.go`):** URL (counting `httptest` client) + File sources; TTL
  refresh; single-flight; `kid`-miss bounded refresh; max-stale fail-closed via the
  clock; `oct` rejected; sub-2048 RSA rejected; zero-usable-keys construction error.
- **Authenticator (`authenticator_test.go`):** keyring resolve (Role from Key),
  jwt resolve (Scope triple), `X-Harbor-Session` replace, role↔scope map, mode errors.
- **Parity ({API, MCP}):** the same credential resolves identically through both
  middlewares (AC-7).
- **Integration (`test/integration/auth_jwt_test.go`, §17, real drivers, `-race`):**
  boot both modes; a jwt-mode request with a valid token narrows to the verified scope
  and a store read returns only that scope's rows; an expired token / stale JWKS is
  rejected; keyring mode unchanged.
- **Fuzz:** `FuzzValidate` over the raw-token string (seed: a valid token, a truncated
  token, `alg:none`, oversized) asserting the invariant *no panic, and a non-nil
  `Verified` ⇒ a non-empty tenant/user/session* (prime parse surface, §11).
- **No bench gate** (auth is not a hot reusable loop the SLO tracks); a
  concurrent-reuse test on one shared `Validator`/`Authenticator` under `-race` proves
  the immutable-after-construction posture (§5).

## Risks & mitigations

- **Algorithm-confusion / key-substitution.** Structurally blocked by parser-level
  `WithValidMethods` + the asymmetric allowlist + the keyfunc belt-and-braces re-check
  and key-type switch (all ported); AC-1 pins the `HS256`-with-RSA-key and `none` cases.
- **JWKS unreachable at boot vs runtime (D-147).** Boot in `jwt` mode fails **loud**
  (no silent keyring fallback — the operator asked for JWT). Runtime serves the
  last-good snapshot to the max-stale ceiling, then fails **closed** (`ErrJWKSStale`).
  D-036 governs *retrieval* serving gateway-free, **not** auth — verifying identity
  weakly is not a sanctioned degradation. Reconciled and filed as D-147.
- **Verbatim-port drift.** The four documented departures (identity mapping,
  redactor drop, role↔scope, no typed `Claims`) are the *only* deviations; a
  recorded-fixture test against a real Harbor-minted token (checked into testdata,
  not generated by the shipped binary) guards the wire shape.
- **`keyFromContext` panic in jwt mode.** Mitigated by synthesizing a back-compat
  `*auth.Key{TenantID,Role}` onto ctx in jwt mode so existing handlers/admin gating
  compile and behave; the verified `Scope` is set alongside for ae8.
- **Knob sprawl.** Six knobs, but each is operator-relevant and D-034-complete;
  TTL/min-refresh stay internal (06).

## Glossary additions

- **Verify-never-mint** — Stowage's auth posture: it *verifies* a JWT it did not issue
  (Harbor mints); the signer exists only in ae7 test code, never the shipped binary (L1).
- **JWKS KeySet** — `auth.JWKSKeySet`, the asymmetric-only, TTL-cached, single-flight,
  max-stale-bounded `KeySet` that resolves a JWT `kid` against a published/File JWK Set.
- **Max-stale ceiling** — the age past which a cached JWKS snapshot is no longer
  vouched for and `KeyByID` fails **closed** (`ErrJWKSStale`); bounds but does not make
  instantaneous key revocation (`auth.jwks.max_stale`).
- **Audience containment** — the `aud` check: the verifier passes iff its configured
  audience id is *contained* in the token's `aud` (`string` or `[]string`); an empty
  configured audience disables it, so one Harbor token verifies at both services (D-136).
- **Test signer** — the test-only RSA/EC JWT minter (with an injectable clock) that
  produces golden fixtures; it never ships (L1).
- **`X-Harbor-Session`** — the per-request session header that replaces the token's
  `session` claim in `jwt` mode, keeping tenant/user token-verified (D-137).

## Decisions filed

- **D-136** — `aud` strategy for auth-once-talk-to-both: each verifier checks
  *containment* of its own audience id in the token's `aud` (string or `[]string`, RFC
  7519); an empty configured audience disables the check; one Harbor token verifies at
  both Harbor and Stowage. (First implemented here.)
- **D-147** — JWKS-unreachable behaviour: `jwt` mode fails **loud at boot** (no silent
  keyring fallback) and fails **closed at runtime** past the max-stale ceiling
  (`ErrJWKSStale`); the keyring stays the default *mode* but is never an implicit
  fallback for a mis/unconfigured JWT mode. D-036's gateway-free-degradation rule
  scopes retrieval, not identity verification.

## As-built deviations (§4.3)

`repos/Harbor` was not in the implementation session, so the plan itself (not
Harbor's source) was the binding spec, per the orchestrator's brief. Five
reasonable, small deviations from the plan's illustrative snippets, recorded
here so they are explicit, not silent:

1. **`api.New`/`mcpserver.KeyringMiddleware` keep their existing signatures;
   `Server.SetAuthenticator` is the injection point.** The plan's Design
   section says "`Server` carries `*auth.Authenticator` (injected from
   main)". `internal/api.New` is called at ~20 existing call sites across
   tests and `eval/harness` with no authenticator parameter. Rather than
   break every call site, `api.New` now builds a default
   `auth.NewKeyringAuthenticator(st.Keys())` internally — byte-identical to
   pre-ae7 behavior — and a new `Server.SetAuthenticator` (mirroring the
   existing `SetGateway`/`SetRetriever` setter pattern) lets
   `cmd/stowage/main.go` override it in `jwt` mode. `mcpserver.KeyringMiddleware`
   is unchanged as specified (a thin wrapper over the new `AuthMiddleware`).
2. **`auth.WithAlgorithms([]string) Option`** was added as a fifth
   `Validator` option (the plan's Design snippet enumerated
   `WithIssuer/WithAudience/WithClock/WithLogger`). The `auth.algorithms`
   config knob (§Config keys) has no effect without a way to narrow the
   parser's accepted method set below the full `AllowedAlgorithms`, so this
   option is necessary, not optional; `cmd/stowage/main.go`'s
   `buildAuthenticator` wires it from `cfg.Auth.AlgorithmList()`.
3. **`mapParserError` recovers `ErrAlgNotAllowed` vs `ErrSignatureInvalid` via
   a pre-parse header-only `alg` peek (`peekAlg`), not from golang-jwt's error
   alone.** golang-jwt v5's `Parser.ParseWithClaims` returns the SAME
   `jwt.ErrTokenSignatureInvalid` for both a `WithValidMethods` rejection and
   an actual bad signature (a deliberate library choice — it does not leak
   which failure occurred at the wire level). `jwt.WithValidMethods` remains
   the sole *enforcement* point (AC-1 unweakened); `peekAlg` decodes only the
   header segment, never influences whether the token is accepted, and exists
   purely so the two failure modes map to distinct audit-log/wire-safe
   reasons and distinct golden-test assertions.
4. **The smoke script checks `stowage config explain`, not `stowage config
   get`.** The plan's Smoke section illustratively wrote `stowage config get
   auth.mode`; the CLI has no `config get` subcommand (only `explain` —
   `cmd/stowage/main.go`'s `configUsage`), matching the ae5/ae6 smoke
   precedent. The script also captures `config explain` output once and greps
   the captured string repeatedly, rather than piping directly from the
   binary per check, to avoid a SIGPIPE-under-`pipefail` flake on a
   short-circuiting `grep -q`.
5. **`JWKSKeySet` is a TTL/max-stale wrapper around an atomically-swapped
   `*staticKeySet` snapshot**, not two structurally separate url/file code
   paths. Both `jwks.url` and `jwks.file` sources produce a fresh
   `staticKeySet` after each successful parse (`doRefresh`); `KeyByID`
   refreshes on demand (kid-miss or TTL elapsed, single-flighted) and serves
   the current snapshot if it is within `max_stale`, else fails closed
   (`ErrJWKSStale`). This satisfies the plan's "`staticKeySet` … backs the
   `jwks.file` path" framing while keeping one fetch/parse/cache
   implementation instead of two.

## Dual-review resolutions (post-implementation)

Two independent adversarial reviews (security/crypto + tests/config/parity) found
no auth-bypass or fail-open bug; the algorithm-confusion, JWKS fail-closed, and
mandatory-triple guarantees hold under adversarial tracing. The confirmed
should-fix/nit findings are resolved in this PR:

1. **JWKS negative-`kid` refresh amplification (both reviewers, should-fix).** A
   kid-miss unconditionally forced a synchronous JWKS fetch, so an *unauthenticated*
   caller could force one outbound fetch per request with a valid-format token
   carrying a fresh random `kid` (golang-jwt resolves the key before verifying the
   signature) — a self-DoS + JWKS-source amplification vector, and the min-refresh
   interval the plan's Design named was not implemented. **Fixed** in
   `internal/auth/jwks.go`: a package-internal `jwksMinRefreshInterval` (10s) gates
   the kid-miss refresh path (tracked via `lastAttemptAt`, injectable clock), so a
   flood of forged kids forces at most one outbound fetch per window; the TTL-elapsed
   path is unchanged. `TestJWKS_KidMissBoundedRefresh` rewritten to a clock-driven
   test asserting an in-window burst refetches zero times and an out-of-window miss
   refetches once.
2. **`parseRSAJWK` exponent bound (Rev1, nit).** `big.Int.Int64()` is undefined past
   int64; an oversized/malformed `e` in a JWKS document produced a garbage exponent.
   **Fixed**: reject an `e` that is not an odd public exponent ≥3 and ≤32 bits.
3. **AC-7 parity scope (Rev2, low).** MCP does not consume `Role` (it has no
   role-gated routes; pre-existing), so the parity test proves **Scope** parity
   API↔MCP and Role parity holds **by construction** (one shared `Authenticate` call,
   D-067). The keyring-credential cross-surface path is exercised by the pre-existing
   comount tests (shared authenticator). Recorded here rather than adding a redundant
   keyring integration test — an accepted, documented completeness footnote, not a
   defect.
4. **Stale file list (Rev2, nit).** D-136/D-147 and the six glossary terms were
   pre-filed in the Wave-0 ledger (89b54e1); the Files list above is corrected to
   show them unchanged by ae7.
