# Phase ae8 — effective-scope resolution + read-side enforcement

- **Status:** draft
- **Owning subsystem(s):** `internal/identity` (the single resolver + its two knobs' types); `internal/config` (the `retrieval.read_posture` and `identity.multiplexing` knobs); `internal/api`, `internal/mcpserver`, `sdk/stowage` (the thin per-surface source-gathering adapters that call the one resolver); `internal/store`, `internal/retrieval` (**consumers only — no change to the store WHERE**)
- **RFC sections:** P3 (§5.3 — scopes enforced in the store layer, no unscoped query), §5 (identity & scopes), §6 (retrieval), §9.5 (one logic core, D-067/D-073)
- **Depends on phases:** **ae2** (the `_meta` intake source — `user`/`session`/`agent`/`project` from the inbound `_meta` map) and **ae7** (the verified-JWT-claim source). **Transitively ae1** (its read-only `identity.Scope.Agent` field and the `agent_id` `_meta` read the resolver routes) — see *Findings I'm departing from*. Wave 3.
- **Informing briefs:** 02 (CC-memory predecessor — surface-sprawl cautionary tale → one resolver, not per-surface scope-building), 04 (CL-Bench — the failure modes the strict posture defends: a silent tenant-wide read is a recall-precision failure the gain metric punishes), 06 (mempalace — gateway-free retrieval; the resolver is a pure function with no gateway/store I/O, so it works in the D-036 degraded path).

> **Checkpoint reconciliation (D-150).** The resolver step that sets `Scope.Session` from
> `ClaimSession`/`MetaSession`/`ArgSession` is amended: on the **retrieval/browse read
> path** the resolver routes the effective session to the relevance sink
> (`Request.SessionID`) and leaves **`Scope.Session` empty**, so `buildScopeWhere` never
> narrows a read to one session (cross-session recall preserved, **D-150**). The resolver
> still resolves the session value under the D-137 precedence; it just does not place it on
> the read `Scope`. Add a golden/regression row proving a session-bearing token (and
> `strict` posture) still returns cross-session results; the "byte-identical default" claim
> holds only because `compatible`+keyring is unchanged — the session correction applies to
> the JWT/`strict` path too. Writes remain session-stamped.

## Goal

When this phase is done there is **exactly one** function —
`identity.ResolveReadScope` — that turns whatever identity sources are active
(the credential tenant, verified JWT claims, host-injected `_meta`, and the
legacy D-125 args) into the effective **read** `identity.Scope`, applying the
precedence **verified JWT claims > `_meta` > args** (the JWT-claim-over-lower-
assertion tiering is D-137; the `_meta`-over-arg sub-order is the charter
identity-model table, lines 97-98 — **which deliberately reverses ae2's interim
`argElseMeta` arg-wins rule**, see *Findings I'm departing from*) and the D-137
resolution rule
(the credential *pins* a dimension ⇒ a disagreeing assertion is **rejected**; it
lets the connection *assert* a dimension ⇒ **accepted**). Every single-user read
surface (SDK, HTTP, MCP) builds its read scope through this one resolver instead
of the ~18 hand-rolled `Scope{Tenant, Project: arg, User: arg}` literals scattered
across the handlers today. Two orthogonal D-034 knobs land with it:
`retrieval.read_posture` (`compatible`|`strict`, default `compatible` — omitted
`user`/agent ⇒ tenant-wide fallback vs **refuse**) and `identity.multiplexing`
(default `false` — may an assertion override the credential's pinned `user`). The
default ship is **byte-identical to today**: `compatible` posture + no
multiplexing + the pre-ae7 keyring (which pins no `user`) reproduces the current
args-only behaviour exactly. **No store predicate is added** —
`buildScopeWhere`/`buildExactScopeWhere` and the vector lane's `Scan` already
filter on a populated `Scope.User`/`Project`/`Session`; ae8's job is to *populate
that scope from a trustworthy source* and, in `strict` posture, *refuse the silent
tenant-wide fallback* rather than write a new `WHERE`.

## Brief findings incorporated

- **02 (CC-memory):** surface sprawl is a named predecessor failure. The read
  scope is built ad-hoc at ~18 sites today (each MCP handler, `scopeFromRequest`,
  each HTTP POST body, `embeddedClient.callScope`); ae8 collapses them onto one
  core resolver with thin per-surface source adapters (D-067/D-073).
- **04 (CL-Bench):** a read that silently widens to tenant-wide is a
  precision/recall failure the gain metric punishes. The `strict` posture converts
  that silent widening into an explicit refusal; the `compatible` default keeps the
  current behaviour until an operator opts in.
- **06 (mempalace):** retrieval must serve gateway-free. `ResolveReadScope` does
  no I/O (no gateway, no store) — it is a pure function of its inputs, so it works
  unchanged in the D-036 degraded path and is safe for concurrent reuse.

## Findings I'm departing from

- **The charter presumes ae1/ae2/ae7 are on disk; in code truth none of them are,
  and neither are their plan files.** Only `phase-ae3` and `phase-ae6` exist under
  `docs/plans/`; ae1/ae2/ae7/ae8 live solely as sections of the charter
  (`docs/plans/track-adoption-ergonomics.md`, marked "all draft").
  `identity.Scope` has **no `Agent` field** (`internal/identity/identity.go:29`),
  `auth.Key` carries only `TenantID` (no `sub`/`user`,
  `internal/auth/key.go:26`), and the MCP handlers read scope from omittable args,
  never `_meta`. So ae8's declared deps (ae2 `_meta` intake, ae7 JWT claim) **and**
  its transitive prereq (ae1's `Scope.Agent`) are all unbuilt. **ae8 therefore
  cannot fully land before ae1 + ae2 + ae7.** This is handled the way ae3 handled
  "MCP has no renderer to split": ae8 ships the resolver + the two knobs + the
  single wiring point **now**, with the JWT and `_meta` source fields present but
  fed empty until ae7/ae2 populate them — so today the resolver is
  behaviour-identical to the args-only path (`compatible`), and `strict`/mux become
  meaningful as the upstream sources arrive. The resolver is a **pure function of
  an explicit multi-source input struct**, so its full precedence/conflict matrix
  is golden-testable in isolation *before* ae2/ae7 wire real sources. Recorded in
  D-148.
- **ae8 owns *both* D-137 knobs, but `identity.multiplexing` ships in its
  interim (global) form.** The D-137 ledger line notes multiplexing is ultimately a
  **per-credential** capability (a JWT scope `memory:assert-user` / a keyring flag)
  "owned by the ae7 auth side", with a global flag as the pre-ae7 interim. Since
  there is **no `AuthConfig` struct** today (auth is keyring-only), ae8 introduces
  the config surface for the **global interim flag** (`identity.multiplexing`) and
  threads a per-credential `CanAssertUser` capability *field* into the resolver
  input so that ae7 (or a later phase) can populate it from the JWT scope /
  keyring **without touching the resolver**. The knob is ae8's; its per-credential
  realization is ae7's follow-on. Recorded in D-148.
- **The charter frames the gap as "read-side user/agent enforcement". In code the
  store already enforces** (verified: `buildScopeWhere`/`buildExactScopeWhere`
  AND-append `project_id`/`user_id`/`session_id` when set and fail closed on empty
  tenant — `internal/store/{pg,sqlite}store/scope.go`; the vector lane re-implements
  the same predicate inline in `vectorStore.Scan` — `vectors.go:85-109` /
  `:83-103`). So ae8 adds **no `WHERE`** and **no store method**. It states this in
  the plan and pins it with a P3 test (below) so a future reader does not "add the
  missing predicate" that already exists in **three** independent code paths.
- **ae8 deliberately supersedes ae2's interim `argElseMeta` (arg-wins)
  precedence.** ae2 (`phase-ae2-meta-intake.md`) ships and tests the *opposite*
  arg-vs-`_meta` rule — its `argElseMeta` helper and AC-2 ("Precedence: arg wins",
  lines 284-287) resolve an in-band arg **over** a `_meta` value ("an in-band arg
  is the caller's chosen value and WINS; a `_meta` value is the host-injected
  default/fallback"). ae8 is the consolidation phase that replaces the ~18
  hand-rolled scope literals — **including ae2's intake helper** — with the single
  resolver, and it aligns with the **charter identity-model table**
  (`docs/plans/track-adoption-ergonomics.md` lines 97-98: `Harbor JWT user claim →
  _meta.user → MCP user_id arg`), i.e. **`_meta` > arg**. This is a deliberate,
  documented reversal of ae2's *interim* rule, not a silent drift: a `_meta` value
  is host-injected identity while an arg is a model-discretionary omittable value,
  so the host default should win over a model-supplied arg. Both candidates are
  **always within the same tenant** (tenant is pinned first, resolution step 1), so
  this is a within-tenant read-scoping choice, never a P3 boundary change. In any
  pre-ae7 deployment where a caller sends both `user_id=A` (arg) and `_meta.user=B`,
  ae2-as-shipped resolves to `A` and ae8's resolver resolves to `B`; the
  reconciliation below makes that intentional and tested rather than a silent flip.
  Consequences, all discharged in this plan: (a) a golden/regression **and** parity
  row for the arg-and-`_meta`-both-present conflict (AC-1 / AC-9 and the test plan);
  (b) a **follow-up to correct ae2** — its `argElseMeta` helper, AC-2, and the
  arg-vs-`_meta` risk/glossary bullets must be updated to `_meta`-wins so ae2's
  shipped W1 behaviour matches the resolver once ae2's intake routes through
  `ResolveReadScope` (tracked as an ae8 follow-up; ae2's file is not edited in
  ae8's PR). **Attribution note:** the `_meta` > arg *sub-order* comes from the
  charter table, **not** from D-137 — D-137 ranks *credential vs assertion*
  (pin/assert) and supplies only the JWT-claim-over-lower-assertion tiering. Both
  recorded in D-148.

## Design

### The resolver (the whole phase, in one pure function)

New file `internal/identity/resolve.go`. `internal/identity` is the correct home:
it already owns `Scope` and its context plumbing and has **no** store coupling
(the package doc's invariant), so a pure resolver belongs here.

```go
// ReadPosture governs the empty-identity fallback on the read path (D-137 knob 2,
// retrieval.read_posture). It is NOT a store distinction — buildScopeWhere treats an
// empty Scope.User as tenant-wide either way; the posture decides whether the
// resolver ALLOWS that empty-user read or refuses it BEFORE the store call.
type ReadPosture int

const (
	PostureCompatible ReadPosture = iota // default — omitted user/agent ⇒ tenant-wide (today's behaviour)
	PostureStrict                        // omitted user AND agent ⇒ refuse the read
)

// IdentitySources is the raw, per-source identity gathered at a read entry point,
// BEFORE precedence is applied. The resolver applies the precedence
// (verified JWT claims > _meta > D-125 args — JWT-over-assertion tiering per D-137,
// _meta-over-arg per the charter identity-model table, which supersedes ae2's
// interim arg-wins argElseMeta rule) and the D-137 resolution rule internally, so
// every surface resolves identity identically (D-067 one logic core). A surface
// adapter fills only the fields its transport carries (D-140: MCP fills the Meta*
// fields, HTTP fills the Claim*/Arg* fields) and leaves the rest zero.
type IdentitySources struct {
	// Credential — always trustworthy. Tenant is the P3 authorization boundary.
	Tenant        string // credential/JWT-verified tenant; PINNED always
	CredUser      string // the user bound to the credential (ae7 JWT `user`); "" for a bare keyring key
	CanAssertUser bool   // this credential may override the pinned user (JWT scope memory:assert-user / keyring flag) — post-ae7

	// Verified JWT claims (ae7) — highest-precedence connection assertions.
	ClaimTenant  string
	ClaimUser    string
	ClaimSession string

	// Host-injected _meta (ae2) — connection assertions.
	MetaTenant  string
	MetaUser    string
	MetaSession string
	MetaAgent   string // read-time identity only; never persisted, never a WHERE
	MetaProject string

	// Legacy D-125 args — omittable model args, lowest precedence.
	ArgUser    string
	ArgSession string
	ArgProject string
}

// ResolveOptions carries the two D-137 knobs.
type ResolveOptions struct {
	Posture      ReadPosture // retrieval.read_posture
	Multiplexing bool        // identity.multiplexing (global interim flag; per-credential via CanAssertUser post-ae7)
}

// ResolveReadScope merges every active identity source into the effective READ
// Scope, honoring the D-137 resolution rule and read posture. It does NO I/O
// (pure, gateway-free, store-free) and is safe for concurrent reuse. It NEVER
// returns a Scope with an empty Tenant on a nil error (P3). Errors are sentinels:
//   - ErrTenantMismatch  — a claim/_meta tenant disagrees with the credential (D-138)
//   - ErrUserConflict    — the credential pins `user` and a disagreeing assertion is not authorized
//   - ErrIdentityRequired — strict posture and neither user nor agent resolved
func ResolveReadScope(src IdentitySources, opts ResolveOptions) (Scope, error)
```

New sentinels alongside the existing `ErrScopeMissing`/`ErrInvalidScope`:

```go
var (
	ErrTenantMismatch   = errors.New("identity: _meta/claim tenant disagrees with credential tenant")
	ErrUserConflict     = errors.New("identity: assertion disagrees with the credential-pinned user")
	ErrIdentityRequired = errors.New("identity: strict read posture requires a resolved user or agent")
)
```

### Resolution rule (the D-137 realization, dimension by dimension)

Applied in this exact order; every branch is a golden-test row.

1. **tenant — PINNED always (P3 boundary).** `Scope.Tenant = src.Tenant`. If
   `src.ClaimTenant != "" && != src.Tenant`, or `src.MetaTenant != "" && !=
   src.Tenant` ⇒ `ErrTenantMismatch` (fail closed — `_meta`/a claim may never
   widen or override the authorization boundary, D-138). An empty `src.Tenant` ⇒
   the resolved scope fails `Validate()` and the resolver returns that error (no
   tenant, no read — P3).
2. **session — ALWAYS assertable (Harbor parity; never gated by the flag).**
   `Scope.Session =` first non-empty of `ClaimSession`, `MetaSession`, `ArgSession`.
   (The HTTP adapter folds `X-Harbor-Session` into `ClaimSession` before calling;
   the resolver never sees the header.)
3. **project — assertable, no JWT claim (project is host-routing, not an auth
   claim).** `Scope.Project =` first non-empty of `MetaProject`, `ArgProject`.
4. **agent — `_meta` only, read-time.** `Scope.Agent = src.MetaAgent` (requires
   ae1's `Scope.Agent`). It is set on the **read** scope only; it never reaches a
   write INSERT or a scope `WHERE` (proven inert by ae1). The value drives ae6's
   fail-open own-scope agent filter, not a store predicate.
5. **user — PINNED (default) / ASSERTABLE (multiplexing).** Let
   `asserted =` first non-empty of `ClaimUser`, `MetaUser`, `ArgUser`;
   `assertable = opts.Multiplexing || src.CanAssertUser`.
   - `CredUser == ""` (bare keyring, the pre-ae7 world): nothing is pinned ⇒
     `Scope.User = asserted` (fully back-compat — args set the user freely, as
     today).
   - `CredUser != ""`: if `asserted == "" || asserted == CredUser` ⇒
     `Scope.User = CredUser`; else if `assertable` ⇒ `Scope.User = asserted`; else
     ⇒ `ErrUserConflict`.
6. **strict refusal (posture, orthogonal to user-pinning).** If
   `opts.Posture == PostureStrict && Scope.User == "" && Scope.Agent == ""` ⇒
   `ErrIdentityRequired` — refuse the tenant-wide read **before** any store call.
   (Note the two knobs are independent: `multiplexing` decides whether a *disagreeing*
   user assertion is accepted; `read_posture` decides what happens when *no*
   user/agent resolves at all.)
7. Return `Scope{Tenant, Project, User, Session, Agent}` after `Validate()`.

### One resolver, thin per-surface adapters (D-067/D-073, D-140)

Each read surface keeps its own **source-gathering adapter** (a small function
that reads its transport's identity into an `IdentitySources`) and then calls the
**one** `identity.ResolveReadScope`. The adapters differ by design (D-140:
MCP reads `_meta`, HTTP reads JWT/headers/args) — that is the sanctioned contract
divergence, not a parity break; the *core* resolver and the posture/mux behaviour
are shared and parity-tested.

- **MCP (`internal/mcpserver`):** replace the ~15 inline
  `identity.Scope{Tenant: scope.Tenant, Project: in.ProjectID, User: in.UserID}`
  literals (e.g. `handlers.go:177` in `makeRetrieveHandler`) with a shared
  `resolveScope(svc, ctx, in)` helper that fills `IdentitySources` from
  `server.RequestMeta(ctx)` (ae2's `_meta` map) + the args + the credential tenant
  from `ScopeFn`, then calls `ResolveReadScope`. `CanAssertUser` comes from the
  keyring flag once ae7 lands (zero today).
- **HTTP (`internal/api`):** `scopeFromRequest` (`auth.go:72`) and each POST
  handler that builds `identity.Scope{Tenant: authKey.TenantID, Project:
  req.ProjectID, User: req.UserID}` (e.g. `retrieve_handler.go:135`) route through
  an `api` adapter that fills `IdentitySources` from the verified-JWT claims on
  context (ae7), the `X-Harbor-Session` header, and the query/body args.
- **SDK (`sdk/stowage`):** `embeddedClient.callScope` (`embedded.go:57`) fills
  `IdentitySources` from the construction-time scope + per-call project/user and
  calls the resolver; `http.go` rides the JSON args unchanged (posture/mux are
  server-side config, not per-call inputs).

Posture and multiplexing come from config (below), read once at wiring time and
passed as `ResolveOptions` — never a per-request argument (no new arg on any
contract; nothing to fuzz on the wire).

### No store change; P3 preserved

ae8 touches **no** file under `internal/store` and adds **no** query method. The
read scope it produces flows into the unchanged `Retriever.Retrieve`
(`retrieval.go:307`) → the existing per-lane store calls
(`buildScopeWhere`/`vectorStore.Scan`), which already narrow on a populated
`Scope.User`/`Project`/`Session` and fail closed on empty tenant. The P3 guarantee
is: the resolver never emits a `Scope` with an empty `Tenant` on success, and no
new store read path exists — so the store's existing fail-closed predicates remain
the *only* read path (pinned by AC-4).

### Reconciling ae1's fail-OPEN agent filter with strict / ae7's fail-closed token

These operate at **different layers** and are both preserved (documented so a
future reader does not "harmonize" them into a bug):

- **`read_posture=strict`** is a *resolve-time presence* gate: it refuses a read
  when **no** user/agent identity was *supplied*. It does not change what happens
  at *runtime* if a downstream policy store errors.
- **ae1/ae6's agent filter fails OPEN** (D-139/D-036): a policy-store error returns
  the caller's own-scope results, degraded. That is a *runtime* degradation of a
  *curation* lens, not an authorization decision.
- **ae7's token path fails CLOSED** (a bad/expired/missing-claim token is rejected
  before a scope is ever resolved).

Consequence, stated plainly in the plan and the tool docs: a deployment that needs
**hard** isolation-on-error must scope by `user` (store-enforced, fail-closed via
the tenant/user `WHERE`), **not** rely on the agent filter (fail-open curation).
`strict` guarantees an identity is *present*; it does not upgrade the agent filter
to fail-closed.

## Files added or changed

```text
internal/identity/resolve.go              # NEW — ReadPosture, IdentitySources, ResolveOptions, ResolveReadScope, sentinels
internal/identity/resolve_test.go         # NEW — golden precedence matrix, conflict cases, tenant-never-empty property, concurrent-reuse (-race)
internal/identity/identity.go             # CHANGED — Scope gains read-only Agent field IFF ae1 has not already added it (ae1 owns it; see departures)
internal/config/config.go                 # CHANGED — RetrievalConfig.ReadPosture; new IdentityConfig{Multiplexing}+Config.Identity; allKeys; Defaults; get/set; Validate (enum + bool)
internal/config/profiles.go               # CHANGED — read_posture=compatible / multiplexing=false effective in every profile (default; documented)
internal/config/testdata/explain_default.golden  # CHANGED — two new key lines
internal/mcpserver/handlers.go            # CHANGED — resolveScope helper via ResolveReadScope; replaces ~15 inline Scope literals
internal/mcpserver/server.go              # CHANGED — thread posture/mux from config into Services (ResolveOptions)
internal/api/auth.go                      # CHANGED — scopeFromRequest routes through the api source-gathering adapter
internal/api/retrieve_handler.go          # CHANGED — POST read scope built via the adapter + ResolveReadScope
sdk/stowage/embedded.go                   # CHANGED — callScope routes through ResolveReadScope
scripts/smoke/phase-ae8.sh                # NEW
test/integration/effective_scope_test.go  # NEW — real-driver strict-flip: resolved-user read isolates; no-identity read refused (§17, -race)
docs/plans/README.md                      # CHANGED — track table (ae8 row draft)
docs/decisions.md                         # CHANGED — D-148
docs/glossary.md                          # CHANGED — effective read scope, read posture, identity multiplexing, credential pin vs assert
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `retrieval.read_posture` | `compatible` | Enum `compatible`\|`strict`. `compatible` = today's behaviour (omitted `user`/agent ⇒ tenant-wide). `strict` = the resolver refuses a read that resolves to no `user` **and** no agent (`ErrIdentityRequired`), *before* any store call. Home: `RetrievalConfig` (sibling to `include_superseded`). D-034-complete: tuned default, effective in every profile, docs, `allKeys`/get/set/explain, validation (enum membership). Default preserves byte-identical zero-config behaviour. |
| `identity.multiplexing` | `false` | Bool. `false` = a user assertion (`_meta.user`/arg/claim) that **disagrees** with the credential-pinned `user` is rejected (`ErrUserConflict`). `true` (global interim, pre-ae7) = such an assertion is accepted. Post-ae7 the per-credential `IdentitySources.CanAssertUser` (JWT scope `memory:assert-user` / keyring flag) grants the same capability without the global flag. Home: **new** `IdentityConfig` on `Config` (no `AuthConfig` exists). D-034-complete: default, every profile, docs, `allKeys`/get/set/explain, validation (bool). Inert when the credential pins no user (the pre-ae7 keyring world), so zero-config behaviour is unchanged. |

## Acceptance criteria (binding)

1. **One resolver, exhaustive precedence matrix.** `identity.ResolveReadScope` is
   the only function that merges identity sources into a read `Scope`. A golden
   test (`resolve_test.go`) covers every source-precedence combination —
   **JWT-only, `_meta`-only, args-only, mixed, and conflicting** — each with a
   deterministic outcome, applying the order **JWT > `_meta` > args**
   (JWT-over-assertion per D-137; `_meta`-over-arg per the charter table). It
   **must** include the **arg-and-`_meta`-both-present** row (e.g. `ArgUser="A"`,
   `MetaUser="B"` ⇒ `Scope.User="B"`), which pins ae8's deliberate reversal of
   ae2's interim `argElseMeta` arg-wins rule (see *Findings I'm departing from*);
   this row is not covered by AC-6 (args-only).
2. **D-137 resolution rule holds.** *Pins:* a `_meta`/claim tenant disagreeing
   with the credential ⇒ `ErrTenantMismatch` (D-138); a disagreeing user assertion
   with `identity.multiplexing=false` and `CanAssertUser=false` ⇒ `ErrUserConflict`.
   *Asserts:* `_meta.user` under multiplexing (or `CanAssertUser`) is accepted;
   `session` is **always** accepted (replace), independent of both knobs. Each is a
   test row.
3. **Strict posture refuses the tenant-wide fallback.** With
   `retrieval.read_posture=strict` and neither a resolved `user` nor a resolved
   agent, `ResolveReadScope` returns `ErrIdentityRequired` — it **populates/requires
   `Scope.User`** (or agent), it does **not** write a new `WHERE`. In `compatible`
   posture the same input resolves to the tenant-wide scope (unchanged).
4. **P3 — no unscoped read path introduced.** ae8 adds **no** `internal/store`
   query method (grep-asserted: no `internal/store/**` file changed by ae8); the
   resolver never returns a `Scope` with an empty `Tenant` on a nil error (property
   test); the three existing scope predicates
   (`buildScopeWhere`, `buildExactScopeWhere`, `vectorStore.Scan`) remain the only
   read filter and still fail closed on empty tenant (regression assertion).
5. **`identity.multiplexing` defaults `false`; per-credential authority is the
   post-ae7 path.** The default rejects a disagreeing user assertion; a test flips
   the knob (and, separately, sets `CanAssertUser=true`) and shows the assertion is
   accepted. The knob is documented as the pre-ae7 global interim for the
   per-credential JWT scope `memory:assert-user`.
6. **`retrieval.read_posture` defaults `compatible` — no behaviour change on
   upgrade.** A retrieve with no `_meta`/JWT identity and only args behaves
   byte-identically to pre-ae8 (regression test). The knob is documented and
   smoke-checked.
7. **Strict flip integration test (§17).** With real drivers (**sqlite +
   postgres**), flipping `read_posture=strict`: a retrieve carrying a resolved
   `user` returns only that user's rows (scope/identity propagation, end to end
   through the store); a retrieve with no user/agent is **refused**
   (`ErrIdentityRequired`) — the ≥1 failure mode; runs under `-race`.
8. **Fail-open / fail-closed reconciliation documented.** The plan and the resolver
   godoc state that `strict` is a resolve-time *presence* gate, ae1/ae6's agent
   filter fails **open** (D-139), and ae7's token path fails **closed** — the three
   are different layers and are all preserved (a test asserts strict does not turn
   the agent filter fail-closed).
9. **Parity {SDK, HTTP, MCP}.** All three single-user read surfaces build their
   read scope through `ResolveReadScope` (grep asserts no surviving inline
   `Scope{Tenant, Project: …, User: …}` read literal outside the adapters); a
   parity test drives the same source set through each surface's adapter and gets
   the same effective scope and the same strict/mux outcomes — **including the
   arg-and-`_meta`-both-present conflict** (`_meta` wins on every surface, matching
   AC-1's reversal of ae2's interim arg-wins rule).
10. **Knobs D-034-complete.** Both keys ship with a tuned default, placement in
    every profile's effective config, docs, `allKeys`/get/set/explain, validation,
    and smoke checks; zero-config start (`compatible` + `multiplexing=false` +
    keyring) is smoke-tested unchanged.

## Smoke script

`scripts/smoke/phase-ae8.sh` — SKIPs gracefully until the files exist; then:
- assert `internal/identity/resolve.go` defines `ResolveReadScope` + `ReadPosture`.
- assert `retrieval.read_posture` and `identity.multiplexing` are registered
  (`stowage config explain` / `get`) and default to `compatible` / `false`.
- assert ae8 introduced **no** new `internal/store` query method (grep: no
  `func .*Scope.*` added under `internal/store` by this phase; the three predicates
  present and unchanged).
- assert no surviving inline read-scope literal in the MCP/HTTP handlers outside
  the adapters (grep for `Scope{Tenant:.*User: in\.` / `User: req\.`).
- `go test ./internal/identity/ -run Resolve` and the strict-flip integration test
  pass.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Golden/unit (`resolve_test.go`):** the full source matrix (JWT-only,
  `_meta`-only, args-only, mixed, conflicting) × posture × multiplexing ×
  `CanAssertUser`, **including the arg-and-`_meta`-both-present row that asserts
  `_meta` wins over the arg** (the deliberate reversal of ae2's interim
  `argElseMeta` arg-wins rule); the three sentinel error branches
  (`ErrTenantMismatch`,
  `ErrUserConflict`, `ErrIdentityRequired`); the tenant-never-empty **property**
  (fuzz-style table over random source combos asserting `Tenant != ""` on every
  nil-error return); the `compatible`-args-only byte-identical regression;
  session-always-assertable independent of both knobs.
- **Concurrency (§5):** `ResolveReadScope` invoked from N goroutines on shared
  input under `-race` (proves the pure-function claim).
- **Integration (`test/integration/effective_scope_test.go`, real drivers, §17 —
  ae8's deps name ae2/ae7 and it closes the read-scope seam):** strict-flip on
  **both** sqlite + postgres — resolved-user retrieve isolates to that user; a
  no-identity retrieve is refused; scope/identity propagates to the store rows;
  `-race`.
- **Parity test:** the same `IdentitySources` fed through the MCP, HTTP, and SDK
  adapters yields the same effective scope and the same strict/mux
  accept/reject outcomes.
- **Regression:** `TestEvalCI` unmoved (default `compatible` + args-only);
  existing retrieve/browse tests pass unchanged.
- **No new fuzz target on the wire** — ae8 adds no new request field; the resolver
  is exercised by the property table instead (noted in the PR).

## Risks & mitigations

- **A `strict` flip silently dropping results for tenant-wide-reliant callers.**
  Mitigated by the `compatible` default (no upgrade-time change) + a documented
  migration window in the plan/knob docs; strict is opt-in per deployment and the
  refusal is an explicit error, never a silent empty result.
- **Precedence bugs.** Mitigated by the exhaustive golden matrix (AC-1/2) and the
  tenant-never-empty property test (AC-4).
- **Wiring an inline read-scope literal that bypasses the resolver.** Mitigated by
  the grep AC-9 + smoke check; every read surface routes through one adapter.
- **Blocking on ae1/ae2/ae7.** The resolver + knobs + wiring ship independently and
  degrade to args-only (`compatible`) until the upstream sources exist; the source
  fields are present-but-empty until then (documented; mirrors ae3's inert-seam
  posture). ae8 cannot be *fully* validated (JWT/`_meta` rows) until ae2/ae7 land —
  stated so the checkpoint audit expects it.
- **Confusing strict with the agent filter's fail-open.** Mitigated by AC-8 + the
  glossary + the resolver godoc pinning the layer separation.
- **Precedence divergence from dependency ae2 (arg-vs-`_meta`).** ae2 ships an
  interim arg-wins `argElseMeta`; ae8's resolver is `_meta`-wins (per the charter
  table). Mitigated by making the reversal explicit and tested (AC-1 conflict row +
  AC-9 parity) and by a tracked **ae2 follow-up** to flip ae2's
  `argElseMeta`/AC-2/risk/glossary to `_meta`-wins so the shipped W1 behaviour
  converges once ae2 routes its intake through `ResolveReadScope`. The divergence is
  within-tenant only (tenant pinned first), so it is never a P3 breach.

## Glossary additions

- **Effective read scope** — the single `identity.Scope` produced by
  `identity.ResolveReadScope` from all active identity sources (credential tenant,
  verified JWT claims, `_meta`, D-125 args) under the D-137 precedence and
  resolution rule. The one input every read surface hands the store.
- **Read posture** — the `retrieval.read_posture` knob (`compatible`|`strict`):
  whether an omitted `user`/agent falls back to a tenant-wide read (`compatible`,
  default) or is refused (`strict`). A resolve-time presence gate, not a store
  predicate.
- **Identity multiplexing** — the `identity.multiplexing` knob (+ the per-credential
  `memory:assert-user` capability, post-ae7): whether a connection may assert a
  `user` that **disagrees** with the credential-pinned `user`. Default off.
- **Credential pin vs assert** — the D-137 rule the resolver enforces: a dimension
  the credential *pins* (tenant always; user under the default) rejects a
  disagreeing assertion; a dimension it lets the connection *assert* (session
  always; user under multiplexing) accepts it.

## Decisions filed

- **D-148** — Effective-scope resolver (`identity.ResolveReadScope`) with the
  precedence verified JWT > `_meta` > args (JWT-over-assertion tiering per D-137;
  `_meta`-over-arg sub-order per the charter identity-model table) and the D-137
  resolution rule; the
  `retrieval.read_posture` (`compatible`|`strict`, default `compatible`) and
  `identity.multiplexing` (default `false`, per-credential `CanAssertUser` post-ae7)
  knobs; the read-side gap closed **upstream** by populating/requiring `Scope.User`,
  not by adding a store `WHERE` (the store already filters). Implements D-137;
  records the code-truth corrections (deps ae1/ae2/ae7 unbuilt; multiplexing ships
  as the global interim; no store predicate added) and the deliberate reversal of
  ae2's interim `argElseMeta` arg-wins precedence to `_meta`-wins (with the ae2
  follow-up correction tracked).
