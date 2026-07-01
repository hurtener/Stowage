# Phase ae2 — additive `_meta` identity intake

- **Status:** implemented (see "As-built deviations" below)
- **Owning subsystem(s):** `internal/mcpserver` (the per-handler scope-construction sites + a shared `_meta` intake helper); `internal/identity` (a `ErrTenantMismatch` sentinel for the D-138 fail-closed guard)
- **RFC sections:** §5 (identity & scopes), §9.5 (one logic core, D-067/D-073), and D-125 (sub-tenant targeting)
- **Depends on phases:** **ae1 only** — reuses the `dockyard v1.7.3 → v1.8.0` bump (so `server.RequestMeta(ctx)` exists), the read-only `identity.Scope.Agent` field, and the first `RequestMeta(ctx)["agent_id"]` call site ae1 planted in the retrieve handler. **Not ae7** (C3): intake reads host-injected `_meta` directly; there is no verified token `sub`/`user` to fall back to until the JWT verifier lands, so this phase is strictly additive.
- **Informing briefs:** 01 (Python predecessor — the scoping pain point this closes: identity that arrives from model-discretionary arguments rather than a trustworthy per-call source), 02 (CC-memory predecessor — the surface-sprawl cautionary tale → one shared intake helper, not ~15 divergent copy-paste scope rebuilds), 03 (Engram — the scopes/privacy framing: identity narrows within a hard tenant boundary, it never widens it).

## Goal

When this phase is done, per-call identity flows into the MCP handlers from the
host-injected `_meta` map **alongside** the existing `project_id`/`user_id`/
`session_id` arguments, without breaking a single existing caller. A host that
attaches `_meta.user` / `_meta.session` / `_meta.agent_id` to a tool call narrows
that call's reads **immediately** — because the store already filters a populated
`Scope.User` (`buildScopeWhere`/`buildExactScopeWhere`, D-125) — so **ae2 is where
`_meta`-borne identity first takes effect**. Tenant is *never* sourced from `_meta`:
it comes only from the verified credential, and a present-but-mismatched
`_meta.tenant` **fails closed** with a redacted reject (D-138). A single documented,
tested precedence rule (the host-injected `_meta` value wins over an in-band arg)
governs every site. Nothing is removed — `project_id`/`user_id`/`session_id` keep their argument
home here; their `_meta` home or removal is settled later in ae2b (M1). ae8
(effective-scope resolver + strict/mux posture) and ae2b (contract removal) build
directly on the intake seam this phase plants.

## Brief findings incorporated

- **01 (Python predecessor):** the recurring predecessor scoping failure was
  identity that rode in on caller-supplied, omittable request fields — there was no
  per-call channel the *host* controls out-of-band. Dockyard v1.8's `_meta` seam is
  exactly that channel; ae2 consumes it so identity can come from the host, not only
  the model's argument-filling. (The credential-derived, *non-omittable* end state is
  ae8's job; ae2 opens the host channel.)
- **02 (CC-memory predecessor):** surface sprawl — the same logic re-expressed
  slightly differently at every call site — is a named predecessor failure. ae2
  introduces **one** intake helper (`readMetaIdentity`) that every handler calls,
  rather than open-coding a `_meta` read + tenant guard 15 times. One seam, thin
  callers (D-067 spirit).
- **03 (Engram):** scopes narrow *within* a hard boundary. `_meta` may only supply
  the **non-authorizing** dimensions (user/session/agent); the authorization
  boundary (tenant) is immovable and credential-only — which is precisely the D-138
  fail-closed guard.

## Findings I'm departing from

- **ae1 is a hard prerequisite and is not yet in the code (verified).** The charter
  frames ae2 as "reuses the dockyard v1.8 `_meta` plumbing and the `Scope.Agent`
  field," but at author time `go.mod` still pins `github.com/hurtener/dockyard v1.7.3`
  (line 8) — `server.RequestMeta`/`WithRequestMeta` first appear in **v1.8.0** — and
  `identity.Scope` has **no `Agent` field**. This plan is written assuming ae1 has
  landed. **If ae1 has not landed when ae2 is implemented, ae2 must absorb ae1's
  mechanical prerequisites first** (`go get github.com/hurtener/dockyard@v1.8.0 &&
  go mod tidy`, add `Scope.Agent string`), otherwise the agent-intake acceptance
  criteria are unbuildable. Recorded here so the dependency is not silently assumed.
- **Session's SINK is the existing request/options field, not `Scope.Session`.** The
  charter AC says "handlers populate `Scope.User`/`Scope.Session`/`Scope.Agent` from
  `_meta`." Code truth: the MCP read handlers **deliberately do not** put session in
  the scope — `memory_retrieve` builds `Scope{Tenant, Project, User}` (no `Session`,
  `handlers.go:177`) and threads `in.SessionID` into `retrieval.Request.SessionID`
  (`:193`) as a *relevance* signal; `memory_playbook` threads it into
  `playbook.Options.SessionID` (`:263`). Retrieval is user-scoped, not
  session-scoped by design (cross-session memories stay retrievable; session is a
  temporal-proximity signal). **Newly populating `Scope.Session` would add a store
  `WHERE session_id=…` predicate and change results for every existing caller that
  passes `session_id` — a behaviour change, not an additive intake.** So ae2
  implements the D-137 **session-REPLACE** semantics by computing an *effective
  session* (`_meta.session` else `arg`) and feeding it to the **same sink the handler
  uses today**, never onto `Scope.Session`. (This is exactly D-137's rule — session
  is always assertable/replace — applied without changing the sink.)
- **`X-Harbor-Session` is not wired in ae2.** D-137 pairs `_meta.session` (MCP) with
  `X-Harbor-Session` (HTTP). The header is read **nowhere** in the code today; it is
  an HTTP/JWT-side concern (ae7). `KeyringMiddleware` reads only `Authorization`. Per
  D-140 the MCP surface takes session from `_meta`; the HTTP header is deliberately
  **out of ae2 scope** and lands with the HTTP identity work (ae7). Wiring
  header-reading into `internal/mcpserver` now would be misplaced.
- **ae2 does not implement the strict/mux user gate.** D-137 makes `user`
  pinned-in-strict / assertable-in-mux — but that gate depends on the credential
  *pinning* a user, and pre-ae7 `auth.Key` pins **only tenant** (`key.go:26-33`: no
  `sub`/`user`). So pre-ae7 the credential does not pin `user`, which by D-137's own
  resolution rule makes `user` **assertable** — exactly the additive intake ae2 does.
  The strict/mux enforcement (refuse a disagreeing `_meta.user` once a pinned claim
  exists) is **ae8's** job. The only pinned dimension ae2 enforces is **tenant**
  (D-138). Stated so ae8's author knows ae2 deliberately left the user gate open.

## Design

### The one shared intake helper (no per-handler copy-paste)

New file `internal/mcpserver/metaintake.go`. It is the single place that reads the
inbound `_meta` and enforces the tenant guard, so the ~15 scope-construction sites
call it instead of open-coding a map read.

```go
// metaIdentity is the non-authorizing identity carried by the inbound _meta.
// Tenant is deliberately absent — it is NEVER sourced from _meta (D-138).
type metaIdentity struct {
    User    string // _meta.user
    Session string // _meta.session
    Agent   string // _meta.agent_id  (read-path only; ae1's Scope.Agent)
}

// readMetaIdentity reads the host-injected _meta (dockyard v1.8 server.RequestMeta)
// and (1) enforces the D-138 tenant guard against the authenticated credTenant,
// (2) extracts the non-authorizing dimensions. It is called by EVERY handler right
// after svc.ScopeFn(ctx): read handlers use the returned identity; write/admin
// handlers call it only for the guard and discard the identity. The returned map is
// dockyard's per-call shallow copy — read-only, never retained past the call.
func readMetaIdentity(ctx context.Context, credTenant string) (metaIdentity, error)
```

Guard semantics (D-138, fail closed):

```go
m := server.RequestMeta(ctx) // nil when no _meta was sent
if v, ok := m["tenant"]; ok {
    s, isStr := v.(string)
    if !isStr || s != credTenant {
        // present-but-mismatched (or malformed) tenant → reject, no values leaked
        return metaIdentity{}, fmt.Errorf("mcpserver: %w", identity.ErrTenantMismatch)
    }
}
```

- `_meta.tenant` **absent** ⇒ fine (the common case; identical to today).
- `_meta.tenant` **present and equal** ⇒ fine, no-op.
- `_meta.tenant` **present and different, or non-string** ⇒ **reject** (fail closed).
  The error carries **no tenant values** (neither the injected nor the real one) —
  a redacted reason.

A tiny pure precedence helper expresses the documented rule once:

```go
// metaElseArg returns the host-injected _meta value when set, else the in-band
// arg fallback. The documented precedence: _meta is the host's trusted,
// out-of-band channel and WINS; an in-band arg is model-filled and untrustworthy
// for identity, so it is only the fallback. Equal to today when meta=="" (no
// caller sends _meta yet), i.e. metaElseArg("", arg) == arg.
func metaElseArg(meta, arg string) string { if meta != "" { return meta }; return arg }
```

`_meta` value extraction is defensive (`map[string]any`): a `metaString(m, key)`
returns `""` for a missing or non-string value — a malformed non-authorizing key is
simply ignored (fail-open on the *non-authorizing* dims; only tenant fails closed).
The keys ae2 reads are exactly `user`, `session`, `agent_id`. **ae2 does not read
`_meta.project`** — `project_id` keeps its argument home (M1); its `_meta` home or
removal is ae2b.

### The identity-domain sentinel (the `internal/identity` touch)

Add to `internal/identity/identity.go`:

```go
// ErrTenantMismatch is returned when an inbound _meta (or, later, an HTTP header)
// supplies a tenant that disagrees with the authenticated credential. _meta may
// supply non-authorizing dimensions (user/session/agent/project) but NEVER the
// authorization boundary — a mismatch fails closed (D-138).
var ErrTenantMismatch = errors.New("identity: _meta tenant does not match authenticated credential")
```

It lives in `internal/identity` (not `mcpserver`) because it is an identity-domain
error the HTTP side and ae8's resolver reuse when the header/JWT path lands. The
message is value-free by construction (nothing to redact at the call site).

### The intake rule applied at the call sites

The rule at every per-call scope-construction site:

1. **Tenant guard runs everywhere.** Right after `base, err := svc.ScopeFn(ctx)`,
   call `mi, err := readMetaIdentity(ctx, base.Tenant)`; on error return it (the
   handler surfaces an MCP tool error — an HTTP-4xx-class reject with a redacted
   reason). This guarantees a tenant-spoofing `_meta.tenant` is rejected **on any
   tool**, read or write.
2. **User (`_meta`-else-arg).** Replace `User: in.UserID` with
   `User: metaElseArg(mi.User, in.UserID)`. `Project` stays `in.ProjectID`
   (arg home, M1). For the few tenant-only read handlers with no `user_id` arg
   (e.g. `memory_suggestions`), the effective user is simply `mi.User`.
   **Per-action caveat — `memory_proactive_config` (`:1214`):** this handler
   builds its scope **once** and reuses it for both `action=get`
   (`proactive.Resolve` — a read) and `action=set` (`proactive.WriteGovernance` —
   a persist). Do **not** apply `metaElseArg` at the shared scope-build line: that
   would flow `_meta.user` into the **set** write path and persist governance
   config under a `_meta`-derived `{tenant,user}` key — a persistence-behaviour
   change this read-intake phase deliberately defers (see the guard-only write
   rationale below). Instead keep the shared scope build on `in.User` (so the
   D-138 guard still runs on both actions), and compute the effective read scope
   (`User: metaElseArg(mi.User, in.User)`) **inside the `case "get"` arm only**,
   just before `proactive.Resolve`; leave `case "set"` (`WriteGovernance`) on the
   arg-only scope. `_meta.user` intake for this handler is therefore scoped to the
   read action alone.
3. **Session-REPLACE (`_meta`-else-arg, existing sink).** Where a handler threads a
   session value downstream, replace `in.SessionID` with
   `metaElseArg(mi.Session, in.SessionID)` at that **existing sink**
   (`retrieval.Request.SessionID`, `playbook.Options.SessionID`,
   `proactive.Evaluate`, the episode/causal windows, `branch` fork) — **never** onto
   `Scope.Session` (see departures). Session as a read-time relevance signal, never
   a hard `Scope.Session` store predicate, is ratified track-wide as **D-150** —
   ae7/ae8 must not narrow retrieval by session either, preserving cross-session
   recall.
4. **Agent (read-path only).** Set `Scope.Agent = mi.Agent` on the **read**
   handlers. `Scope.Agent` is provably inert until ae6/ae9 read it (no store
   predicate yet); it is placed **only** on read scopes and never on a
   write/mutate/INSERT scope, so it can never reach a scope table (charter C2, P1).

### Which handlers are touched (grounded in the code map)

Every handler gains the **tenant guard** (step 1). Identity intake (steps 2–4)
applies as follows:

| Handler (site) | Guard | User | Session | Agent |
|---|---|---|---|---|
| `memory_retrieve` (`:177`/`:193`) | ✓ | ✓ | ✓ (→`Request.SessionID`) | ✓ |
| `memory_playbook` (`:258`/`:263`) | ✓ | ✓ | ✓ (→`Options.SessionID`) | ✓ |
| `memory_drilldown` (`:314`) | ✓ | ✓ | — | ✓ |
| `memory_get` (`:554`) | ✓ | ✓ | — | ✓ |
| `memory_episodes` (`:944`) | ✓ | ✓ | ✓ (→window/session arg) | ✓ |
| `memory_causal` (`:1020`) | ✓ | ✓ | — | ✓ |
| `memory_trace` (`:1128`) | ✓ | ✓ | — | ✓ |
| `memory_verify` (`:1063`) | ✓ | ✓ | — | ✓ |
| `memory_review` (`:1087`, list) | ✓ | ✓ | — | ✓ |
| `memory_suggestions` (`:1155`) | ✓ | ✓ (`mi.User`; no arg) | ✓ (→`Evaluate`) | ✓ |
| `memory_proactive_config` (`:1214`, get/set) | ✓ | ✓ (`get` arm only — see step 2 caveat) | — | ✓ |
| `memory_feedback` (`:399`) | ✓ | ✓ | — | — (write) |
| `memory_rollback` (`:587`) | ✓ | ✓ | — | — (mutate) |
| `memory_resolve` (`:615`) | ✓ | ✓ | — | — (mutate) |
| `memory_branch` (`:758`) | ✓ | ✓ | ✓ (fork) | — (mutate) |
| `memory_ingest` (`:32`) | ✓ | — | — | — (write; per-record fields, out of ae2) |
| `memory_assert` (`:496`) | ✓ | — | — | — (write; no sub-scope arg, out of ae2) |
| `memory_topics` (`:638`) | ✓ | — | — | — (tenant-scoped mgmt) |
| `memory_flush` (`:719`) | ✓ | — | — | — (control verb) |
| `memory_grants` (`:817`) | ✓ | — | — | — (admin; owner scope built from args) |

Rationale for the "guard-only" set: `memory_ingest`/`memory_assert` are write paths
whose scope is a per-record or direct write target — sourcing a persisted dimension
from `_meta` on a write is a persistence-behaviour change, deliberately deferred out
of this read-intake phase; `memory_topics`/`memory_flush`/`memory_grants` are
tenant-scoped or admin verbs with no sub-tenant read to narrow. `memory_proactive_config`
is the one dual-action handler: its `action=set` arm (`WriteGovernance`) is a **write**
and stays arg-only for the same reason, while only its `action=get` arm takes
`_meta.user` intake (step 2 caveat). Agent is set on read scopes only. Every one of
them still runs the D-138 guard, so a spoofed `_meta.tenant` is rejected uniformly.

### Additivity guarantee

For every touched site: when no `_meta` identity is injected, `mi` is the zero value
(`RequestMeta` returns `nil`), so `metaElseArg("", arg) == arg`, `Scope.Agent == ""`
(inert), and the guard is a no-op — the resolved scope and every downstream call are
**byte-identical to today**. No existing caller sends `_meta`, so this holds for
every site unconditionally: the precedence flip (`_meta` wins when both are present)
is invisible until a host actually starts injecting `_meta`. D-125 targeting
(arg-supplied `project_id`/`user_id`) is unchanged (C4). The behaviour delta appears
*only* when a host injects `_meta`.

### Surfaces & parity (D-067 / D-140)

ae2 changes the **MCP** intake path only. The underlying capability (scoped reads)
already exists identically on {SDK, HTTP, MCP}; ae2 adds a new *source* for the
non-authorizing dimensions on the MCP surface. Per **D-140**, MCP-vs-HTTP identity
*source* divergence (MCP reads `_meta`; HTTP reads args/headers/claims) is a
**sanctioned** contract divergence, not a parity violation (the same precedent as
`assert`'s deliberate HTTP omission). The parity that must hold — and is tested — is
**behavioural**: a `_meta.user`-narrowed MCP read resolves to the *same effective
scope* an HTTP `user_id`-narrowed read resolves to, and the store returns the same
rows. No new SDK/HTTP capability ships, so no SDK/HTTP contract change is required in
this PR.

### Concurrency & fidelity posture

`readMetaIdentity`, `metaElseArg`, `metaString` are pure functions over the per-call
context and their arguments — no receiver state, no package-level mutable state
(safe under `-race`, matching the handlers' existing statelessness). The dockyard map
is treated as read-only and never retained past the call (its documented per-call
shallow-copy contract). P1/P2 are untouched: no verbatim record is read or written
differently; ingest's fire-and-forget path is not on any ae2 change (ingest gets only
the guard). P3 is *strengthened*, never weakened: `_meta` can only narrow within the
credential tenant and can never widen or override it (D-138); no unscoped query path
is introduced.

## Files added or changed

```text
internal/mcpserver/metaintake.go        # NEW — readMetaIdentity (D-138 guard), metaElseArg, metaString
internal/mcpserver/metaintake_test.go   # NEW — guard (absent/equal/mismatch/non-string), precedence, extraction, -race reuse
internal/mcpserver/handlers.go          # CHANGED — call readMetaIdentity at each site; metaElseArg on User + effective session; Scope.Agent on read handlers
internal/identity/identity.go           # CHANGED — add ErrTenantMismatch sentinel
internal/identity/identity_test.go      # CHANGED — ErrTenantMismatch is a distinct, value-free sentinel
go.mod / go.sum                         # CHANGED IFF ae1 has not landed — dockyard v1.7.3 → v1.8.0 (else already bumped by ae1)
scripts/smoke/phase-ae2.sh              # NEW
test/integration/meta_intake_test.go    # NEW — real-driver: _meta.user narrows reads; no-_meta identical; tenant-mismatch reject (§17)
docs/plans/README.md                    # CHANGED — flip ae2 to draft in the ae-track table (delta returned, not edited here)
docs/decisions.md                       # CHANGED — D-138 (delta returned, not edited here)
docs/glossary.md                        # CHANGED — _meta identity intake, tenant guard, session-replace, meta-else-arg precedence (delta returned)
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| _(none)_ | — | ae2 is **pure additive intake**. It adds no MCP tool, no endpoint, no config knob — the identity dimensions already exist. D-137's posture knobs (`identity.multiplexing`, `retrieval.read_posture`) belong to **ae8**, not here. Zero-config start (the static-keyring, tenant-only default) is unchanged and stays smoke-tested (charter D-034 invariant). |

## Acceptance criteria (binding)

1. **`_meta` narrows reads (first-effect).** A `memory_retrieve` call carrying
   `_meta.user="u1"` (and no `user_id` arg) resolves to `Scope{Tenant, User:"u1"}`
   and the store returns only `u1`'s own-scope rows — proven with **real drivers**
   (sqlite + postgres), no new store predicate added. This is where `_meta`-borne
   identity first takes effect.
2. **Precedence: `_meta` wins.** When both `user_id="u_arg"` and `_meta.user="u_meta"`
   are present, the effective user is `u_meta`; when only `_meta.user` is present it is
   `u_meta`; when only the arg is present it is the arg (table test on
   `metaElseArg` + a handler-level test).
3. **Tenant fail-closed (D-138).** A call carrying `_meta.tenant` that disagrees with
   the credential tenant returns an error (`identity.ErrTenantMismatch`), surfaced as
   an MCP tool error (HTTP-4xx-class), with a reason that leaks **no tenant value**;
   `_meta.tenant` equal to the credential is a no-op; a non-string `_meta.tenant`
   also rejects (fail closed). The guard runs on **every** handler (read and write).
4. **Tenant only from the credential.** A grep/lint asserts no handler sources
   `Scope.Tenant` from `_meta`; `Tenant` is always `base.Tenant` from `svc.ScopeFn`.
5. **Additive / no-`_meta` identical (C4).** A request that injects **no** `_meta`
   identity produces a byte-identical resolved scope and downstream call to today, on
   every touched handler (regression test); D-125 arg targeting is unchanged.
6. **Session-REPLACE without a `Scope.Session` predicate.** The effective session is
   `_meta.session` when present, else the `session_id` arg, fed to the handler's
   **existing** sink (`retrieval.Request.SessionID` / `playbook.Options.SessionID` /
   etc.); a grep asserts no read handler writes `_meta` session into
   `Scope.Session`, so no new store `session_id` predicate is introduced (the
   departure is enforced by test).
7. **Agent read-path only.** `Scope.Agent` is set on read handlers from
   `_meta.agent_id` and on **no** write/mutate scope; it is provably inert (no INSERT
   binds it, `buildScopeWhere`/`buildExactScopeWhere` do not reference it — inherited
   from ae1's C1 proof; ae2 adds an assertion that no write handler sets it).
8. **`project_id` keeps its arg home (M1).** `_meta.project` is **not** read in ae2;
   `project_id` remains an argument on every contract that has it today (grep).
9. **One intake seam (no sprawl).** `readMetaIdentity` is the only place
   `server.RequestMeta` is called from `internal/mcpserver` (grep asserts a single
   call site); handlers call the helper, they do not open-code the map read.
10. **dockyard v1.8.0 present.** `go.mod` pins `github.com/hurtener/dockyard v1.8.0`
    (bumped by ae1, or by ae2 if ae1 has not landed) so `server.RequestMeta` compiles.
11. **Smoke check in-PR** (below), `OK ≥ count(criteria)`, `FAIL = 0`; prior phases'
    smoke still passes.

## Smoke script

`scripts/smoke/phase-ae2.sh` — SKIPs gracefully until the surface is built; then:
- assert `internal/mcpserver/metaintake.go` exists and defines `readMetaIdentity`.
- assert `identity.ErrTenantMismatch` is defined in `internal/identity`.
- assert `go.mod` pins `dockyard v1.8.0` (AC-10).
- assert exactly one `server.RequestMeta(` call site in `internal/mcpserver` (AC-9).
- assert no handler sets `Tenant:` from a `_meta`/`RequestMeta` value (AC-4) and no
  read handler writes `_meta` session into `Scope{...Session:` (AC-6).
- assert `_meta.project`/`RequestMeta(...)["project"]` is **not** read (AC-8).
- `go test ./internal/mcpserver/ -run MetaIntake` and
  `go test ./internal/identity/ -run TenantMismatch` pass.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Unit (`metaintake_test.go`):** `readMetaIdentity` — `_meta` absent (nil map),
  `_meta.tenant` equal (no-op), mismatched (reject), non-string (reject), and clean
  extraction of `user`/`session`/`agent_id` (missing/non-string → `""`);
  `metaElseArg` truth table; a concurrent-reuse test calling the helpers from N
  goroutines on shared context under `-race`.
- **Identity (`identity_test.go`):** `ErrTenantMismatch` is a distinct sentinel, its
  message contains no tenant value (a literal-free assertion), and it is `errors.Is`
  -matchable through the handler's `%w` wrap.
- **Handler golden/regression:** for `memory_retrieve` (and one mutate,
  `memory_rollback`): (a) no-`_meta` request → the resolved scope + downstream
  `retrieval.Request` are unchanged vs a pre-ae2 fixture (AC-5); (b) `_meta.user`
  narrows; (c) `_meta`-wins precedence when both arg and `_meta` are present;
  (d) `_meta.tenant` mismatch rejects with a
  redacted reason (AC-3). Uses `server.WithRequestMeta(ctx, m)` to inject `_meta` in
  the test (dockyard exports the setter for exactly this).
- **Integration (`test/integration/meta_intake_test.go`, real drivers, §17 — ae2's
  Deps name ae1/`internal/identity` and it consumes the dockyard `_meta` seam):**
  seed memories under `{tenant, user:u1}` and `{tenant, user:u2}`; a `_meta.user=u1`
  retrieve returns only `u1`'s rows and a no-`_meta` retrieve returns tenant-wide
  (identical to today) on **both** sqlite and postgres (identity/scope propagation);
  the **failure mode** is the `_meta.tenant` mismatch reject; run under `-race`.
- **No new fuzz target.** ae2 adds no new parse/decode surface — `_meta` is a
  `map[string]any` the host owns and dockyard already validated at the protocol
  boundary; the helper only type-asserts and string-compares. (Noted in the PR.)

## Risks & mitigations

- **arg-vs-`_meta` ambiguity.** Mitigated by a **single** documented precedence rule
  (`metaElseArg`: the host-injected `_meta` wins) applied at every site through one
  helper, with a truth table test — no per-handler variation (the 02
  surface-sprawl lesson).
- **Tenant spoofing via `_meta`.** Mitigated by the D-138 fail-closed guard running on
  **every** handler and by AC-4's grep that tenant is credential-only; `_meta` can
  only ever *narrow* within the credential tenant.
- **Silent behaviour change from populating `Scope.Session`.** Mitigated by routing
  the effective session to the existing sink, never `Scope.Session` (AC-6 grep + the
  no-`_meta` regression), so no new store `session_id` predicate appears.
- **ae1 not landed.** Mitigated by AC-10 + the smoke `go.mod` check + the explicit
  departure: ae2 absorbs the dockyard bump and `Scope.Agent` if ae1 has not shipped.
- **Redacted-reason leak.** Mitigated by making `ErrTenantMismatch` value-free at the
  sentinel and never formatting tenant values into the wrap (a literal-free test on
  the surfaced error string).

## Glossary additions

- **`_meta` identity intake** — the ae2 mechanism by which the MCP handlers read the
  non-authorizing identity dimensions (`user`, `session`, `agent_id`) from the
  host-injected inbound `_meta` (dockyard v1.8 `server.RequestMeta`) **alongside** the
  existing `project_id`/`user_id`/`session_id` arguments. Additive: no `_meta` ⇒
  identical behaviour to arg-only.
- **Tenant guard (D-138)** — the fail-closed check that a present `_meta.tenant`
  equals the credential-verified tenant; a mismatch (or a non-string value) rejects
  the request with a redacted reason. `_meta` may supply non-authorizing dimensions,
  never the authorization boundary.
- **Session-REPLACE** — the D-137 semantics ae2 implements on MCP: the effective
  session is `_meta.session` when set, else the `session_id` arg, fed to the handler's
  existing session sink — never added as a `Scope.Session` store predicate.
- **`_meta`-else-arg precedence** — the documented resolution rule for a
  non-authorizing dimension: the host-injected `_meta` value wins over an in-band
  argument when both are present; the arg is only the fallback (`metaElseArg`).

## As-built deviations

- **Two handlers not enumerated in the Design's handler table are also
  guarded/intake'd.** `memory_agent_policy` and `memory_browse` landed with ae1/ae5
  respectively, after this plan's handler table was authored against the
  then-current code map. AC-3 ("the guard runs on **every** handler, read and
  write") is binding regardless of the table's enumeration, so both were folded
  in for correctness rather than left as a tenant-guard gap:
  - `memory_agent_policy` — guard-only (tenant-scoped admin verb, no sub-tenant
    read to narrow — matches the `memory_grants` rationale exactly).
  - `memory_browse` — guard + user (`metaElseArg(mi.User, in.UserID)`) + agent
    (`mi.Agent`), no session (`BrowseOptions` has no session dimension) — matches
    the `memory_get` pattern exactly.
  Both are exercised by the existing `TestAgentPolicyHandler_*` / browse-handler
  test suites plus the package's `-run MetaIntake` regression tests; no plan
  Design text needed to change, only the table's completeness.
- **`ae1`'s `dockyard v1.8.0` bump and `Scope.Agent` field were already landed**
  (commit `d72d535`) by the time ae2 was implemented, so the "ae1 not yet in the
  code" contingency in "Findings I'm departing from" did not need to be exercised
  — `go.mod`/`identity.Scope.Agent` were reused as-is.
- No other deviations from the Design; all 11 acceptance criteria are met as
  specified (see the smoke script and the verification tails in the PR).

## Decisions filed

- **D-138** — `_meta.tenant` mismatch fails closed: `_meta` may supply non-authorizing
  dimensions (user/session/agent/project) but may never widen or override the
  credential-verified authorization boundary; a present-but-mismatched (or non-string)
  `_meta.tenant` rejects the request with a redacted reason. First implemented here as
  the intake tenant guard; the sentinel `identity.ErrTenantMismatch` is reused by the
  HTTP path and ae8. (ae2 also *implements* the already-settled **D-137** additive
  intake — `_meta`-else-arg precedence, session-REPLACE, tenant pinned / user
  assertable pre-ae7 — but files no new id for it, per the charter.)
