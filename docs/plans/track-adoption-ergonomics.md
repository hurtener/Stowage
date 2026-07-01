# Track: Agent-identity & read-time scoping (`phase-ae*`) — D-135

> **Status:** charter — drives phase-plan authoring; not itself a phase plan.
> **Branch:** `feat/adoption-ergonomics-track`
> **Purpose:** close Stowage's **read-side identity gap** and sharpen the
> agent-facing read surface (lean reads, browse, topic curation) *without*
> weakening P1–P5. This file guides creation of the `phase-ae*` plans (one
> `cp docs/plans/_template.md` per phase, per CLAUDE.md §16). When a phase plan
> is authored it supersedes the stub here; keep this charter as the wave map and
> dependency graph.

This track is orthogonal post-launch work, same posture as the `phase-h*`
(D-067), `phase-pN-*` (D-126), and `phase-aN-*` (D-131) tracks. Numbered
`phase-aeN-*` so it does not collide with the launch (01–27), post-launch
(22–27), productionization (`h*`), performance (`p*`), or adoption (`a*`) slots
— the `ae` prefix is distinct from the adoption track's `a1`/`a1b`/`a2`/`a3`;
smoke scripts still match the `scripts/smoke/phase-*.sh` gate. Governing posture
is **additive-first**: read identity from `_meta` *alongside* the existing
arguments, never instead of them, until the JWT verifier lands. Every phase
lands behind a green `make preflight`, its own `scripts/smoke/phase-aeN.sh`,
coverage bands (§11), and — for any capability on a multi-surface tier — same-PR
parity tests across all of that tier's surfaces (D-067/D-073).

---

## Why this track exists

Stowage today knows *who the tenant is* and nothing else about *who is asking*.
The verified API key resolves a single dimension — `tenant_id` — and every MCP
seam scopes tenant-only. The usability review against mem0 (4-tool OpenMemory
MCP) and Hindsight (27–30 tools, 43k-token reader) surfaced the concrete gaps:

1. **MCP `retrieve` results never reach a headless agent's context.** Dockyard
   routes structured output to `structuredContent` (UI-facing, kept out of the
   model context); the only model-facing `Text` is a one-line count. Our own
   eval harness bypasses MCP and renders markdown via the SDK — proof the MCP
   read channel is empty.
2. **No way to walk memory.** No general list/browse on any surface; date
   filtering is search-gated; no superseded/stale filter; graph reads need a
   seed id you must already hold.
3. **Identity is in the model's hands, and reads default tenant-wide.** The finer
   dimensions (`project_id`, `user_id`) survive only because ~16 MCP input
   contracts still carry them as model-filled arguments (the sole D-125
   sub-tenant targeting mechanism). The store layer *does* filter on
   `user_id`/`project_id` when the scope carries them
   (`buildScopeWhere`/`buildExactScopeWhere` on both drivers, and the vector lane
   `mv.user_id` predicate) — but those values arrive from **omittable,
   model-discretionary args**, so a caller that omits `user_id` reads
   **tenant-wide** (the MCP handler comment is explicit: *"Empty = tenant-wide
   (back-compat)"*). The real P3 read-side gap is **not** a missing predicate — it
   is the absence of **credential-derived, non-omittable** user/agent scoping.
4. **No agent dimension at read.** `RecordItem.SourceAgent` is a **records-only
   label** (the D-024 day-one signal) — never scoped, filtered, deduped, or read
   on retrieval. There is no read-time notion of "this agent asked this."

Three things changed that make closing the identity gap cheap rather than
structural:

1. **Dockyard v1.8 shipped the inbound `_meta` accessor.** A host can now attach
   per-call identity — an agent id, a user identity — outside the model-filled
   arguments, and a typed handler reads it verbatim via
   `server.RequestMeta(ctx) map[string]any` (setter `server.WithRequestMeta`,
   auto-populated by the tool wrappers on every invocation). Dockyard surfaces
   the host map verbatim and never inspects keys, so the key contract is owned by
   Stowage + Harbor. Stowage already imports that package
   (`internal/mcpserver/server.go`); consuming it is a `go.mod` bump plus a call
   site, not a protocol change.
2. **Harbor's JWT claim shape and verifier seam are on disk and copyable.**
   Stowage can become a *verifier* of Harbor's token (verify-never-mint, same
   issuer/JWKS) so a client authenticates once and talks to both products — no
   invented token format.
3. **Read-time agent filtering needs no schema.** The `memory_topics` junction
   (migration 0011, both drivers) plus the scope-required batch reader
   `MemoriesTopics(ctx, scope, ids)` already power grants' topic filter. Bind an
   agent identity to a small allow/deny topic policy and filter the caller's
   **own-scope** results through that existing junction.

**The explicit win this track banks:** agent filtering *at read* without touching
the 12 denormalized scope tables. Agent is a **read-time identity/filter only** —
no agent column, no migration on a scope table, no dedupe-index / UNIQUE /
DSAR-cascade / buffer→flush threading. Persisting agent would be a 12-table ×
2-dialect migration plus all of that cascade; the read-time model avoids every
line of it.

---

## The identity model (principle)

> Stowage resolves a request's identity along five dimensions. **Tenant is the
> authorization boundary; the rest narrow results inside it.** Agent is a
> *read-time identity and filter only* — it is never persisted on any scope
> table. Stowage **verifies** identity, it never mints it.

| Dimension | Source (in precedence order) | Persisted? | Role / enforcement |
|-----------|------------------------------|------------|--------------------|
| **tenant** | verified API key → Harbor JWT `tenant` claim | yes (12 scope tables, existing) | **P3 authorization boundary.** Resolved in the store layer; no unscoped query exists. The *only* dimension that gates access. |
| **project** | `_meta.project` (its explicit home) → MCP `project_id` arg (additive, until ae2b) | yes (existing columns) | Sub-tenant partition (D-125). Independent dimension — narrows, does not gate. |
| **user** | Harbor JWT `user` claim → `_meta.user` → MCP `user_id` arg (additive) | yes (existing columns) | Sub-tenant partition (D-125). |
| **session** | Harbor JWT `session` claim → `X-Harbor-Session` header → `_meta.session` | yes (existing columns) | Sub-tenant partition (D-125). |
| **agent** | `_meta.agent_id` (host-injected; Dockyard surfaces it verbatim) | **NO — never persisted, no column anywhere** | **Read-time identity/filter only.** agent identity + a small policy binding + the existing `memory_topics` junction → filters the caller's own-scope results. Layered on the store-layer scoped query; it never opens a new unscoped path. |

Three corrections this principle ratifies against the earlier charter draft:

- **Agent is re-tiered to read-time (resolves C1).** The earlier model listed
  agent as a persisted partition; it is not. `identity.Scope` gains an optional
  `Agent` field set **only on the read path** — proven inert because the write
  path binds scope dimensions by name (the `INSERT` column list) and scope
  queries build their `WHERE` field-by-field, so a new `Scope.Agent` can never
  leak into a write or a scope predicate. No `ae*` phase adds a persisted agent
  partition.
- **`project_id` gets an explicit home (resolves M1).** Its home in the new model
  is `_meta.project`; it remains expressible as the MCP `project_id` argument
  **additively** until the breaking arg-removal phase (gated on ae7, with a
  deprecation window — resolves C4). It does **not** travel in the JWT (the JWT
  carries `tenant`/`user`/`session`); project is a host-routing dimension, not an
  auth claim.
- **`layer`/`intent` is dropped from the principle (resolves M2).** No phase owns
  it today and it is not an identity dimension — it is retrieval-output *shaping*,
  which belongs to the retrieval renderer, not the identity model. Folding it into
  the identity principle would conflate "who is asking" with "how to shape the
  answer" and strand an unownable promise. It can return as a scoped
  retrieval-shaping concern (the deferred `phase-ae10`); it leaves the identity
  principle now. *(Rationale recorded here so the drop is explicit, not silent.)*

**Two precision notes the principle is bound to state plainly:**

- **MCP and HTTP identity diverge by design.** MCP reads `user`/`session`/`agent`
  from host-injected `_meta`; HTTP reads them from JWT claims / headers / query
  args. This is a *sanctioned* contract divergence (precedent: `assert`'s
  deliberate HTTP omission), recorded as its own decision (D-140) — not drift.
- **The token win is model-context only, not wire size (resolves M4).** Where the
  renderer (ae4a) emits a lean `Text` payload *and* the full `Structured` JSON,
  both still travel the wire — total payload **grows**; only the model's context
  shrinks. Apps hosts that read both fields receive a larger payload. State this
  precisely wherever the renderer is specced; do not claim a wire-size win.

---

## Wave 0 — Decisions & prerequisites

Wave 0 is **decision + plumbing**, not a memory-feature phase. It settles the
identity decisions the whole track inherits and lands the one mechanical
prerequisite that is already shippable. File each `D-NNN` in `docs/decisions.md`
before the dependent phase plan is approved.

### Prerequisites

- **PREREQ-1 — Dockyard `_meta` accessor: SHIPPED.** Dockyard v1.8.0 is published
  and exposes the inbound `_meta` seam in `runtime/server` exactly as proposed:
  reader `func RequestMeta(ctx context.Context) map[string]any` and setter
  `func WithRequestMeta(ctx, m map[string]any) context.Context` (file
  `runtime/server/tool.go`). Remaining work is mechanical and lives in the first
  consuming phase (ae1): bump `go.mod` line 8 from
  `github.com/hurtener/dockyard v1.7.3` → `v1.8.0`
  (`go get github.com/hurtener/dockyard@v1.8.0 && go mod tidy`) and call
  `server.RequestMeta(ctx)` in the MCP handlers to pull `agent_id` (and any
  host-injected identity). **The earlier placeholder name `MetaFromContext` is
  wrong — the real shipped symbol is `RequestMeta`** (resolves M5). The return
  type is stdlib `map[string]any` (a deliberate P3 handler-boundary choice — no
  raw protocol type leaks to handlers); it is `nil` when no `_meta` was sent.
- **PREREQ-2 — `repos/Harbor` is the ae7 prerequisite.** The JWT-verifier phase
  (ae7) must reimplement Harbor's claim shape + `Validator`/`KeySet`/JWKS pattern
  **verbatim** inside Stowage's `internal/auth` (Harbor's `internal/protocol/auth`
  is `internal/` to its module — Go forbids the cross-module import). Stowage
  ships only the verify half (Validator + JWKS KeySet + middleware) on
  `golang-jwt/jwt/v5 v5.3.1`; it never ports the signer / `harborClaims` minting
  side. **Add `repos/Harbor` to the session before ae7** so the claim shape
  (`iss/sub/aud/exp/nbf/iat/tenant/user/session/scopes`; mandatory triple
  `tenant/user/session` + `exp`) and the asymmetric-only verifier seam are
  copyable. The verifier is a **second** verify mode beside the existing static
  keyring, not a replacement.

### Sequencing constraint (resolves C3)

The additive `_meta` read of `user`/`session`/`agent` can land before ae7. Any
"fall back to the token `sub`" behaviour and any **breaking removal** of the
`project_id`/`user_id` MCP args is **gated on ae7** — until the JWT verifier
exists, `auth.Key` has no `sub`/`user`, `sk_` keys are opaque, and MCP scope is
tenant-only at every seam, so a token-`sub` fallback is impossible and removing
the args would collapse MCP to tenant-wide (a P3/D-125 regression). ae2 is
therefore strictly additive; arg removal (ae2b) is a later, ae7-gated phase with
a deprecation window.

### Wave-0 decisions (next free id is D-135; last shipped is D-134)

| D-NNN | Decision | Resolves |
|-------|----------|----------|
| **D-135** | **Parent track decision.** The `ae*` agent-identity & read-time scoping track exists; agent is read-time identity/filter only with **no schema migration** on and **no agent column** on the 12 scope tables; identity arrives from Harbor JWT (HTTP) and host `_meta` (MCP); Stowage is a verify-never-mint verifier. | track scope, C1, C2 |
| **D-136** | **`aud` strategy for auth-once-talk-to-both.** Each verifier checks *containment* of its own audience id in the token's `aud` (Harbor's `audienceContains` already accepts a string or a `[]string` per RFC 7519, and an empty configured audience disables the check), so one Harbor token verifies at both Harbor and Stowage. No shared-secret audience hack; no invented token format. | ecosystem alignment, ae7 |
| **D-137** | **Multiplexing-vs-strict posture — SETTLED: default STRICT, two orthogonal knobs** (full entry in `docs/decisions.md`). Harbor pins `(tenant, user, scopes)` in the token and multiplexes **only session** (`X-Harbor-Session` replaces the claim; never widens `(tenant, user)`) — so STRICT (`user` = credential claim) is the ecosystem-native default, and *user*-multiplexing is the opt-in. Knob 1 **`identity.multiplexing`** (default `false`): may `_meta.user`/arg override the pinned `user`? — a **per-credential** capability (JWT scope `memory:assert-user` / keyring flag) post-ae7, global flag as pre-ae7 interim. Knob 2 **`retrieval.read_posture`** (`compatible`\|`strict`, default `compatible`, owned by ae8): omitted `user`/agent ⇒ tenant-wide fallback vs refuse. Default ship = byte-identical to today. Resolution rule (generalizes D-138): the credential *pins* a dimension ⇒ a disagreeing `_meta`/arg is **rejected**; it lets the connection *assert* a dimension ⇒ accepted. tenant=pinned always; user=pinned(strict)/assertable(mux); session=always assertable (replace); agent=`_meta`-only, read-time, fail-open. Agent placement CLOSED (`_meta` only — Harbor claims carry no agent field). | C4, P3 boundary, ae2/ae7/ae8 |
| **D-138** | **`_meta.tenant` mismatch handling.** If a host injects a `_meta.tenant` that disagrees with the credential/JWT-verified tenant, the request **fails closed** — `_meta` may never widen or override the authorization boundary; it may only supply non-authorizing dimensions (agent/user/session/project). | P3, C4, ae2 |
| **D-139** | **Topic-views are curation, not isolation.** The agent→topic policy binding filters *which of the caller's own-scope memories surface*; it is a curation/relevance lens, **not** a security boundary. Cross-scope isolation remains the store-layer scope query (P3) + grants; the agent filter **fails open** (returns the caller's own memories on a policy-store error, per D-036), deliberately the opposite of grants' fail-closed `filterByTopic` (which guards cross-scope sharing). The opposite failure semantics are intentional and recorded here to prevent future drift. | C2, H3, D-036, ae1/ae6/ae9 |
| **D-140** | **MCP-vs-HTTP identity divergence is a sanctioned contract divergence.** MCP reads `user`/`session`/`agent` from `_meta`; HTTP reads them from JWT claims / headers / args. Like `assert`'s deliberate HTTP omission, this is an allowed per-surface contract difference under the one-logic-core rule, not a parity violation. | L2, D-067/D-073, ae2b |

Per-phase read/ergonomics decisions (ae3/ae4a/ae5/ae6, and ae4b on promotion)
continue the sequence after the Wave-0 block as **D-141+**, filed in track order;
the owner confirms the exact numbers at author time (see *Open authoring notes*).

A new config knob introduced in this track (the JWT verify-mode selector and its
issuer/JWKS/audience/algorithms/max-stale keys; the agent-filter / agent-views
enable flags and their fail-open defaults; `retrieval.browse_default_limit`)
ships under D-034 — tuned default, placement in every profile, docs, and a smoke
check in the **same** PR — with zero-config start (the static keyring default,
views off) preserved as a smoke-tested invariant. Each Wave-0 decision lands as a
`## D-NNN` entry in `docs/decisions.md` and any new vocabulary (`_meta` seam,
agent identity, agent→topic policy binding, read-time scope, agent view) lands in
`docs/glossary.md` in the same PR.

---

## Wave map

The earlier ordering entangled two things that are in fact **orthogonal**:
read-time identity (additive, zero scope-table migration) and the breaking
removal of the legacy MCP targeting arguments. The corrected sequencing separates
them and gates the breaking work behind the JWT verifier.

| Wave | Phases | Posture |
|------|--------|---------|
| **W0 — ship now (no new auth, no migration on the 12 scope tables)** | **ae3** (renderer parameterized — byte-identical only on the eval path, M3); **ae4a** (lean MCP `Text` + episode hook via `Memory.EpisodeID` + drill by the **existing** per-item citation ULID — zero new store code, H1/H2); **ae5** (most-recent-first browse; superseded reuses `ListByStatus`, H4); **ae6** (own-scope topic filter, fail-**OPEN**, with the H3 lane-pushdown / `scoringK` remedy) | Unblocked; depend on nothing else in this track. **ae4a lands after ae3** (intra-wave: ae3 ships the slots ae4a fills). |
| **W1 — additive read-time identity (+ the Dockyard bump)** | **ae1** (optional read-only `Scope.Agent`; `dockyard v1.7.3 → v1.8.0` + `server.RequestMeta(ctx)`; the `(tenant,agent)→topic` policy binding that feeds ae6's filter); **ae2** (read `user`/`session`/`agent` from `_meta` **alongside** existing args) | Additive; no migration on the 12 scope tables, no breaking change. **ae1 reuses ae6's own-scope filter** (so ae1 follows ae6); **ae2 follows ae1**. |
| **W2 — auth foundation** | **ae7** (JWT verifier reimplemented in `internal/auth`, verify-never-mint, second verify mode beside the static keyring; golden tests need a test signer + injectable clock, L1) | Gates all breaking sub-tenant work. |
| **W3 — curation & enrichment built on identity** | **ae8** (effective-scope resolution + read-side user/agent enforcement — closes the P3 read-side gap); **ae9** (per-agent/per-key topic views); **ae4b** (causal hook — explicit batch links-exist query + latency budget, H2) | Build on W0–W2; no breaking change. |
| **W4 — breaking, post-ae7, deprecation window** | **ae2b** (remove `project_id`/`user_id` from the MCP contracts once the JWT/`_meta` supply sub-tenant targeting); **ae10** (own or drop `layer`/`intent`, M2) | Strictly after ae7; deprecation window before removal. |

**Why ae1/ae2 are additive, not entangled (C1, C2).** ae1 adds an *optional*
`Agent` field to `identity.Scope` consumed **only on the read path**; the write
path binds scope dimensions by name (the `Insert` column list) and scope queries
build their `WHERE` field-by-field, so `Scope.Agent` is provably inert in writes
and dedupe. ae2 reads `user`/`session`/`agent` from the inbound `_meta`
**alongside** the existing arguments. Neither persists agent, neither touches the
12 denormalized scope tables, neither requires a scope-table migration.
`source_agent` stays a records-only read label.

**Why ae2b cannot precede ae7 (C3, C4).** `auth.Key` carries no `sub`/`user`;
`sk_` keys are opaque; MCP scope is tenant-only at every seam. The ~16 MCP input
contracts that carry `project_id`/`user_id` are today the **sole** D-125
sub-tenant targeting mechanism. Removing them before a JWT verifier exists would
collapse MCP to tenant-wide and regress P3/D-125. So ae2 is strictly **additive**
and the breaking arg-removal is split into **ae2b**, gated on ae7 + ae8 and
shipped behind a deprecation window.

**Genuinely ship-now set:** **ae3, ae4a, ae5, ae6** (scoped, fail-open) — plus the
mechanical Dockyard bump, which rides with ae1 in W1. Wave boundaries get a
read-only checkpoint audit (§17).

### Summary table (decision ids: D-135–D-140 settled; D-141+ proposed, track order)

| # | Phase | Owns | RFC | Deps | Decision |
|---|-------|------|-----|------|----------|
| ae3 | Shared render core (eval-mode vs MCP-mode) | `internal/retrieval` (render), `eval/`, `internal/mcpserver` | §4.2, §9.2, §9.5 | — (shipped render path) | D-141 (proposed) |
| ae4a | Lean MCP read — `Text` markdown + episode hook + drill by citation ULID | `internal/mcpserver`, `internal/retrieval`, `sdk/stowage`, `internal/api` | §4.2, §5.7, §6b, §9.2, §9.5 | ae3 | D-142 (proposed) |
| ae5 | List / browse (most-recent-first, superseded filter) | `internal/store` (+ both drivers + conformance), `internal/retrieval`, `sdk/stowage`, `internal/api`, `internal/mcpserver` | §5.2, §5.3, §8.1, §9.1-9.5 | — (memories + supersede phase) | D-143 (proposed) |
| ae6 | Request-level topic filter (own-scope, fail-open, lane-aware) | `internal/retrieval`, surfaces | §4.2, §5.3, §5.4, §9.5 | — (lanes/scoring + topics, D-089) | D-144 (proposed) |
| ae1 | Read-time agent identity dimension (+ Dockyard bump) | `internal/identity`, `internal/store`, `internal/retrieval`, `internal/mcpserver`, `internal/api`, `sdk/stowage` | §5, §5.3, §9.5 | **ae6** | D-135, D-139 |
| ae2 | Additive `_meta` identity intake | `internal/mcpserver`, `internal/identity` | §5, §9.5, D-125 | ae1 | D-137, D-138 |
| ae7 | Harbor-aligned JWT verifier (second mode) | `internal/auth`, `internal/api`, `internal/mcpserver`, `internal/config` | §5.5, §9.5 | — (existing keyring seam) | D-136 |
| ae8 | Effective-scope resolution + read-side enforcement | `internal/identity`, `internal/store`, `internal/retrieval`, `internal/config` | P3/§6, §5, §9.5 | ae2, ae7 | D-137 |
| ae9 | Per-agent / per-key topic views (read-time curation) | `internal/retrieval`, `internal/identity`, `internal/store`, surfaces | §5.3, §6, §9.5 | ae1, ae6 | D-139 |
| ae4b | *(deferred)* Causal hook (batch links-exist) + optional positional drilldown | `internal/store` (+ both drivers + conformance), `internal/reconcile`/`internal/episodes`, `internal/retrieval`, `internal/mcpserver` | §5.6, §5.7, §4.2, §8.1 | ae4a | D-145 (on promotion) |
| ae2b | Breaking removal of `project_id`/`user_id` from MCP contracts | `internal/mcpserver`, `sdk/stowage`, `docs/` | D-125, §9.5 | ae7, ae8 | D-140 |
| ae10 | *(deferred)* `layer`/`intent` read-shaping argument | `internal/retrieval`, surfaces | §6 | ae2, ae3 | — (M2: own-or-drop) |

Plans: `phase-ae3-shared-render-core.md`, `phase-ae4a-lean-mcp-render.md`,
`phase-ae5-browse.md`, `phase-ae6-topic-filter.md`, `phase-ae1-read-time-agent.md`,
`phase-ae2-meta-intake.md`, `phase-ae7-jwt-verifier.md`,
`phase-ae8-effective-scope.md`, `phase-ae9-topic-views.md`,
`phase-ae4b-causal-hook.md` (deferred), `phase-ae2b-contract-removal.md`,
`phase-ae10-read-shaping.md` (deferred) — all draft.

---

## Phase stubs (author one `phase-aeN-*.md` each, from `_template.md`)

Ordered by wave. Each stub keeps the charter format
(Owns/RFC/Deps/Punch-list/Goal/AC sketch/Risk); the full `_template.md` plan
supersedes the stub when authored.

### Wave 0 — ship now

#### `phase-ae3` — shared render core (eval-mode vs MCP-mode)
- **Owns:** `internal/retrieval` render path, parameterized by a `RenderMode`
  (`RenderEval` / `RenderMCP`); the `eval/` and `internal/mcpserver` call sites.
- **RFC:** §4.2 (read path), §9.2 (MCP), §9.5 (one logic core, D-067/D-073).
- **Deps:** the shipped retrieval render path; the ae-track parent (D-135). No
  in-track dependency — lands **before ae4a** within W0.
- **Punch-list:** M3.
- **Goal:** one render function parameterized by `RenderMode`, with **no second
  renderer** for MCP. The eval call path is byte-for-byte unchanged; the
  `RenderMCP` path is a strict superset whose affordance slots (citation handle,
  episode hook) ae4a fills. ae3 ships as a pure refactor with the seam in place —
  the slots are wired but emit the same base body, inert until ae4a.
- **AC sketch:** exactly one render entry point (MCP has no private renderer,
  grep/lint); the eval golden passes unchanged (**byte-identical scoped to the
  eval call path only** — M3); `RenderMCP` base body == `RenderEval` in this
  phase (diff test); the renderer is a pure function (no receiver state), proven
  by a concurrent-reuse test under `-race`.
- **Risk:** eval-golden drift — the inert MCP slots stop the refactor moving eval
  bytes (AC pins it); mode-flag sprawl — `RenderMode` is a two-value call-site
  argument, **not** a D-034 config knob.

#### `phase-ae4a` — lean MCP read (`Text` markdown + episode hook + drill by citation ULID)
- **Owns:** the `internal/mcpserver` retrieve handler; the `RenderMCP` slot fill
  in `internal/retrieval`; SDK/HTTP parity surfaces.
- **RFC:** §4.2 (drill-down), §5.7 (injections/citations), §6b (episodes), §9.2,
  §9.5.
- **Deps:** **ae3** (the parameterized render core); the shipped
  injections/citations path; the shipped episodes phase (`Memory.EpisodeID`).
- **Punch-list:** H1, H2, M4.
- **Goal:** MCP retrieve returns **lean markdown in the `Text` block**
  (model-facing) with (a) an **episode hook** sourced from the already-loaded
  `Memory.EpisodeID` (free — no new store query) and (b) a **drill handle equal
  to the existing per-item citation ULID** so a follow-up drill-down reuses the
  existing citation path with **zero new store code**. The full structured result
  still travels in the `Structured` block for Apps hosts.
- **AC sketch:** lean `Text` + full `Structured`; an episode hook **iff**
  `Memory.EpisodeID != ""`, with **no new store query** (store-call assertion);
  the drill handle is the existing citation ULID and a drill-down using it
  round-trips through the existing path — **no `(response_id, rank)` method**
  (H1); the tool doc *and* plan state the **M4 wire-truth** (total payload grows;
  token win is model-context only; Apps hosts reading both blocks get a larger
  payload); parity across {SDK, HTTP, MCP}.
- **Risk:** implying a wire-size win — AC-M4 forces the honest statement; the
  episode hook tempting a new load, or drift toward a positional
  `(response_id, rank)` drill — both forbidden here and deferred to ae4b.

#### `phase-ae5` — list / browse (most-recent-first, superseded filter)
- **Owns:** a new scope-required `Store.ListByScopeRecent` (`created_at DESC`,
  inverted keyset) on the seam + both drivers + conformance; a `Browse` core in
  `internal/retrieval`; the {SDK, HTTP, MCP} surfaces.
- **RFC:** §5.2 (memories), §5.3 (scopes — P3), §8.1 (Store seam), §9.1-9.3, §9.5.
- **Deps:** the shipped memories store + status/supersede phase. No in-track dep
  (W0).
- **Punch-list:** H4.
- **Goal:** a caller browses a scope's memories **most-recent-first** with keyset
  pagination and filters to **superseded** memories, on all three single-user
  surfaces — one core, thin surfaces, gateway-free.
- **AC sketch:** `ListByScopeRecent` returns memories `created_at DESC` with a
  stable inverted keyset, **scope-required**, on both drivers + conformance; the
  superseded filter **reuses the existing `ListByStatus(scope,'superseded',…)` —
  no new superseded query** (simplified per H4); no unscoped variant (P3); parity
  across {SDK, HTTP, MCP}; serves gateway-free (D-036). Knob
  `retrieval.browse_default_limit` (proposed `30`, bounded) ships D-034-complete.
- **Risk:** keyset-inversion bugs (off-by-one / unstable paging) — conformance
  ordering + keyset tests; temptation to build a superseded query — AC pins reuse
  of `ListByStatus`.

#### `phase-ae6` — request-level topic filter (own-scope, fail-open, lane-aware)
- **Owns:** `internal/retrieval` (lanes, scoring, a new `filterByTopicOwnScope`);
  reuses `memory_topics` + the scope-required `Store.Memories().MemoriesTopics`
  batch reader; the three retrieve surfaces (parity). **No new table.**
- **RFC:** §4.2 (lanes/fusion), §5.3 (P3), §5.4 (topics), §9.5.
- **Deps:** the shipped retrieval lanes/scoring phase; the topics phase
  (`memory_topics`, D-089). No in-track dep (W0).
- **Punch-list:** H3.
- **Goal:** an optional own-scope topic include/exclude on retrieve that returns
  only topic-tagged memories **without underfilling** — the topic predicate is
  pushed **into the lanes** (or `scoringK` widened) so it does not run over a
  relevance-truncated pool — and **fails open** (D-036). This is the read-path
  mechanism the read-time agent filter (ae1) and the topic views (ae9) reuse.
- **AC sketch:** the filter returns only tagged memories scoped to the caller
  (P3, via `MemoriesTopics`); **no underfill** at `limit` when ≥`limit`
  topic-tagged memories exist in scope (a regression test that fails the
  relevance-truncate-then-filter approach); a topic-store error returns the
  caller's own (unfiltered) results with a degraded marker — explicitly
  **opposite** to grants' fail-closed `filterByTopic` (a **distinct** function;
  divergence filed under D-139); the topic arg is **additive**; parity across
  {SDK, HTTP, MCP}.
- **Risk:** underfill (the H3 core risk) — lanes built topic-first or `scoringK`
  widened; copying grants' fail-closed semantics — the separate fail-open
  function + D-139 pin the divergence. Conditional knob
  `retrieval.topic_filter_scoring_k` only if lane-widening is chosen (D-034).

### Wave 1 — additive read-time identity

#### `phase-ae1` — read-time agent identity dimension
- **Owns:** an optional `identity.Scope.Agent` field set **only on the read
  path**; a new `(tenant_id, agent_id) → allow/deny topic-key` **policy-binding**
  store + table (new `Store` method, both drivers + conformance — **not** one of
  the 12 scope tables, carries no memory rows); the agent→topic-keys resolution
  that **feeds ae6's own-scope fail-open filter**; the `go.mod` bump
  `dockyard v1.7.3 → v1.8.0` and the `server.RequestMeta(ctx)["agent_id"]` call
  site in `internal/mcpserver`. Agent read-filter on {SDK, HTTP, MCP}; policy
  admin on {HTTP, MCP}.
- **RFC:** §5 (identity & scopes), §5.3 (`memory_topics` reuse), §9.5
  (D-067/D-073).
- **Deps:** **ae6** (reuses its own-scope fail-open topic filter + the H3 lane
  remedy — the generic filter is built once, in ae6). **Does not depend on ae7**:
  `agent_id` arrives via `_meta`, never the JWT. Performs the Dockyard bump the
  rest of the track builds on (W1).
- **Punch-list:** C1, C2, H3, M5.
- **Goal:** let a host narrow a tenant's own retrieval results by the calling
  agent, using only the agent identity from `_meta`, a small policy binding, and
  ae6's existing topic filter — with **zero schema change to the scope tables and
  zero write-path change**.
- **AC sketch:** `Scope.Agent` defaults `""` and is provably inert on the write
  path (no INSERT binds it) and in `buildScopeWhere`/`buildExactScopeWhere` (C1);
  **no agent column**, no dedupe-index / UNIQUE / DSAR-cascade / buffer→flush
  threading, `source_agent` stays records-only (C2); `go.mod` pins
  `dockyard v1.8.0` and the MCP handler reads `RequestMeta(ctx)["agent_id"]` (the
  placeholder `MetaFromContext` is wrong — M5) onto the **read** scope only; the
  policy store/table on **both drivers** passes conformance, scope-required (no
  unscoped variant); the agent filter **fails OPEN** via ae6 (D-139); capability
  on {SDK, HTTP, MCP} with a parity test, policy admin on {HTTP, MCP}; the enable
  flag + fail-open default is a D-034 knob with a smoke check in-PR.
- **Risk:** redundancy with ae6 — resolved by reusing ae6's single filter; the
  fail-OPEN vs grants' fail-CLOSED divergence is a deliberate, documented split
  (D-139) to prevent future "make them consistent" drift.

#### `phase-ae2` — additive `_meta` identity intake
- **Owns:** reading `user`/`session`/`agent` from the inbound `_meta` map
  **alongside** the existing `project_id`/`user_id` handler args; tenant
  **always** from the verified credential; a present-but-mismatched
  `_meta.tenant` **rejected** (fail closed); a documented precedence rule between
  an explicit arg and a `_meta` value. **No contract removal.**
- **RFC:** §5 (identity & scopes), §9.5, D-125 (sub-tenant targeting).
- **Deps:** **ae1 only** — reuses the dockyard v1.8 `_meta` plumbing and the
  `Scope.Agent` field. **Not ae7** (C3): intake reads host-injected `_meta`
  directly; there is no token `sub` to fall back to before the verifier exists.
- **Punch-list:** C3, C4, M1, L2 (preview).
- **Goal:** make per-call identity flow from the host's `_meta` without breaking
  any existing caller — a strictly-additive intake that ae8 (effective-scope) and
  ae2b (contract removal) build on.
- **AC sketch:** handlers populate `Scope.User`/`Scope.Session`/`Scope.Agent`
  from `_meta` when the corresponding arg is absent; explicit args win per the
  documented precedence; tenant is never sourced from `_meta`, and a mismatched
  `_meta.tenant` returns a 4xx-class reject with a redacted reason (D-138);
  callers that do **not** inject `_meta` identity behave **identically** (D-125
  targeting unchanged — C4); the **intended** effect is that a host which *does*
  inject `_meta.user`/`agent` narrows its reads immediately — because the store
  already filters on a populated `Scope.User` — so **ae2 is where `_meta`-borne
  identity first takes effect**, and ae8 adds only the precedence resolver +
  mandatory-strict posture, **not** new store predicates; `project_id` keeps its
  **arg** home here (its `_meta` home, or removal, is settled in ae2b — M1); a
  smoke check in-PR.
- **Risk:** arg-vs-`_meta` ambiguity — a single documented, tested precedence
  rule; tenant-spoofing via `_meta` — the mismatch-reject + credential-only
  tenant.

### Wave 2 — auth foundation

#### `phase-ae7` — Harbor-aligned JWT verifier (second mode)
- **Owns:** a **verify-never-mint** JWT `Validator` + JWKS `KeySet` + bearer
  middleware, **reimplemented** in Stowage's `internal/auth` (Harbor's
  `internal/protocol/auth` is cross-module `internal/`, not importable), on
  `golang-jwt/jwt/v5 v5.3.1`; a **second** verify mode beside the static keyring,
  config-selected, with the keyring as the zero-config default.
- **RFC:** §5.5 (identity/auth), §9.5; D-030 (key store).
- **Deps:** the existing keyring auth seam; `repos/Harbor` in the session
  (PREREQ-2). Independent of ae1/ae2 — can land in parallel (W2). This is the
  phase that makes a verified `sub`/`user`/`session` claim **exist** and the C4
  gate that unblocks ae2b.
- **Punch-list:** C3 (root), L1, `aud` strategy (D-136).
- **Goal:** let a Harbor-minted JWT authenticate against Stowage by reimplementing
  Harbor's verifier shape verbatim (claim set
  `iss/sub/aud/exp/nbf/iat/tenant/user/session/scopes`; mandatory triple
  `tenant/user/session` + `exp`; asymmetric-only RS/ES allowlist with `HS*`/`none`
  rejected at the parser via `WithValidMethods`; exact-match issuer;
  containment-match audience; mandatory redactor), so one token verifies at both
  services without inventing a token format — while the keyring path keeps
  working.
- **AC sketch:** the Validator verifies a Harbor-shaped token and rejects every
  negative case (wrong alg, bad sig, expired, missing claim) under golden tests
  with a **test signer + injectable clock** (L1 — the signer lives in test code
  only, never the shipped binary); the JWKS KeySet fails loud on first load and
  fails closed past the max-stale ceiling; verifier mode is config-selected with
  the keyring default (zero-config start smoke-tested); each new knob (issuer,
  JWKS URL/file, algorithms, audience, max-stale, mode selector) ships
  D-034-complete; the `aud` containment strategy is filed (D-136); parity across
  its tier's surfaces.
- **Risk:** algorithm-confusion / key-substitution — structurally blocked by
  parser-level `WithValidMethods` + the asymmetric allowlist (port Harbor's
  belt-and-braces re-check); JWKS unreachable at boot — fail-loud, keyring remains
  the default mode (open: fail-closed vs keyring fallback under D-036 — settle in
  the plan).

### Wave 3 — curation & enrichment built on identity

#### `phase-ae8` — effective-scope resolution + read-side enforcement
- **Owns:** the single resolver that merges **credential tenant + `_meta`
  (user/session/agent) + JWT claims (when ae7 mode is active)** into the effective
  **read** `Scope`, plus a **strict-vs-compatible posture flag** (a D-034 knob,
  ratified under D-137). It does **not** add a store-layer predicate —
  `buildScopeWhere`/`buildExactScopeWhere` and the vector lane already filter on
  `Scope.User`/`Scope.Project` when set (`internal/store/*/scope.go`,
  `internal/store/*/vectors.go`). The gap it closes is **upstream**: user/agent
  arrive today only from omittable model args, so reads silently fall back to
  tenant-wide. ae8 makes identity **credential/`_meta`-derived** and, in `strict`
  posture, **non-omittable** — refusing the tenant-wide fallback rather than adding
  a `WHERE`.
- **RFC:** P3 / §6 (scope enforcement in the store layer — no unscoped query), §5,
  §9.5.
- **Deps:** **ae2** (the `_meta` intake source) and **ae7** (the JWT-claim
  source) (W3). Resolution precedence: verified JWT claims when present, else
  `_meta`, else the D-125 args — so identity has a deterministic home regardless
  of mode.
- **Punch-list:** P3 read-side gap; posture flag (D-137).
- **Goal:** produce one authoritative effective read scope from whichever identity
  sources are active, and (in `strict` posture) require a credential/`_meta`-derived
  `user`/agent rather than letting an omitted arg default to tenant-wide. The
  store-layer narrowing on a populated `Scope.User` **already exists**; ae8's job is
  to populate it from a trustworthy source and forbid the silent tenant-wide
  fallback.
- **AC sketch:** a resolver + golden tests cover every source-precedence
  combination (JWT-only, `_meta`-only, args-only, mixed, conflicting) with a
  deterministic outcome; in `strict` posture the resolver **refuses a tenant-wide
  read** when no credential/`_meta` `user`/agent is present (it asserts the
  resolver *populates/requires* `Scope.User` — **not** that a new `WHERE` is
  written, since `buildScopeWhere`/`buildExactScopeWhere` already filter on it); a
  store-layer test proves no unscoped read path is introduced (P3); the posture flag defaults to
  `compatible` (no behaviour change on upgrade), is documented, and is
  smoke-checked; flipping to `strict` is covered by an integration test with real
  drivers proving scope/identity propagation and ≥1 failure mode (§17); it honours
  ae1's fail-OPEN agent filter and ae7's fail-closed token posture (the two
  failure modes reconciled and documented); parity across {SDK, HTTP, MCP}.
- **Risk:** a `strict` flip silently dropping results for callers who relied on
  tenant-wide reads — the `compatible` default + a documented migration window;
  precedence bugs — exhaustive golden coverage of the source matrix.

#### `phase-ae9` — per-agent / per-key topic views (read-time curation)
- **Owns:** generalizing ae1's single agent→topic binding into named, switchable
  **VIEWS** keyed by `(tenant_id, subject_kind, subject_id, view_name) →
  {allow_topics, deny_topics}` (still **not** a scope table, no memory rows);
  apply-a-view on {SDK, HTTP, MCP}; view admin (create/update/delete/list) on
  {HTTP, MCP}. Reuses `memory_topics` + `MemoriesTopics`.
- **RFC:** §5.3 (topic-keyed slicing), §6 (retrieval), §9.5.
- **Deps:** **ae1** (read-time `Scope.Agent` from `_meta` + the agent→topic policy
  binding) and **ae6** (the own-scope fail-open filter mechanism + the H3 lane
  remedy) (W3).
- **Punch-list:** D-139 (curation-not-isolation), H3 (inherited lane remedy).
- **Goal:** generalize ae1's single allow/deny binding into named views bound to a
  subject — an `agent_id` (from `_meta`, ae1) or, when no agent identity is
  present, the **verified key id** — a read-time lens that narrows which
  topic-tagged **own-scope** memories surface, with no agent column and no
  migration on the 12 scope tables.
- **AC sketch:** a view bound to `agent_id` narrows that agent's own-scope
  retrieval to the allowed topic keys; an unbound agent sees unfiltered own-scope
  results; a key-id view applies when `_meta` carries no `agent_id`, and agent
  precedence wins when both exist; a views-store read error returns the caller's
  full own-scope results (**fail-open** — D-139, deliberately opposite to grants'
  fail-closed), proven by fault injection; a view **never** returns a row outside
  the caller's scope (P3 parity test across surfaces); view admin round-trips
  (create → apply → update → apply → delete) on both drivers via the conformance
  suite; knobs (`retrieval.agent_views.enabled=false`,
  `…on_policy_error=open`, `…subject_precedence=agent,key`) ship D-034-complete
  with smoke.
- **Risk:** "harmonizing" the fail-open vs grants' fail-closed semantics into a
  bug — prevented by D-139 + glossary; curation mistaken for isolation — a view
  can only **subtract** from own-scope, never widen; the tenant from the verified
  credential/JWT remains the only P3 boundary.

#### `phase-ae4b` *(deferred)* — causal hook (batch links-exist) + optional positional drilldown
- **Owns:** a new scope-required batch `Store.LinksExist(ctx, scope, ids) →
  map[string]bool` (one round-trip, both drivers + conformance); a causal-hook
  render slot; an explicit read-path latency budget; optionally a positional
  `(response_id, handle)` drilldown on the injections store (which **is** new
  store code per H1, hence gated here, not ae4a).
- **RFC:** §5.6 (typed links), §5.7 (injections), §4.2 (drill-down), §8.1 (Store
  seam).
- **Deps:** **ae4a** (the episode hook + render slots); the shipped typed-links
  phase. **Deferred** — promote on a confirmed host need (W3 when promoted).
- **Punch-list:** H2, H1.
- **Goal (when promoted):** add a per-item "has causal edges" marker to MCP
  retrieval **without a hot-path N+1** — a single batch links-exist query with a
  measured latency budget — plus an optional positional drilldown.
- **AC sketch:** `LinksExist` is one batch round-trip (no per-item `ListLinks`),
  scope-required, on both drivers + conformance; the causal hook stays within the
  documented read-path latency budget (measured, bench-gated against the SLO band,
  D-031/D-095); the hook **fails open** (D-036 — a batch error omits the hook,
  retrieval still serves); any positional `(response_id, handle)` drilldown is the
  **only** new injections-store method and is conformance-tested; parity across
  {SDK, HTTP, MCP}; knob `retrieval.causal_hook=false` ships D-034-complete on
  promotion.
- **Risk:** hot-path N+1 (the whole point — batch method, bench-gated budget);
  scope-creep into the positional drill (kept optional, gated behind a confirmed
  host need). The smoke script SKIPs until promoted.

### Wave 4 — breaking, post-ae7, deprecation window

#### `phase-ae2b` — breaking removal of `project_id`/`user_id` from MCP contracts
- **Owns:** the deprecation and removal of the `project_id`/`user_id` MCP **input
  args** (~16 contracts) now that identity flows via `_meta`/JWT — a deprecation
  window (args accepted-but-ignored, emitting a versioned warning event), then
  removal; and the **sanctioned MCP-vs-HTTP identity divergence** (MCP moves to
  `_meta`, HTTP keeps its query-param projection).
- **RFC:** D-125 (sub-tenant targeting), §9.5 (tiered surfaces + the sanctioned
  contract-divergence precedent — `assert`'s HTTP omission).
- **Deps:** **ae7** (JWT carries user/session — the C4 gate) and **ae8**
  (effective-scope resolution sources identity without the args). A deliberately
  late phase (W4).
- **Punch-list:** C4 (resolution), L2, M1 (resolution).
- **Goal:** retire the MCP targeting args once identity has a verified,
  `_meta`/JWT-borne home, without ever leaving a window in which MCP targeting
  silently degrades to tenant-wide.
- **AC sketch:** deprecation phase — args accepted-but-ignored, each use emits a
  versioned warning event, a golden test pins the warning and the unchanged
  behaviour; removal phase (post-window) — the ~16 contracts drop the args, and an
  effective-scope test proves identity still resolves from `_meta`/JWT for every
  previously-arg-targeted call; the MCP-vs-HTTP divergence is documented and filed
  (D-140); `project_id`'s final home is settled (dropped, or carried as a `_meta`
  key — M1); SDK/MCP parity + smoke checks for the changed contracts in-PR.
- **Risk:** a caller still depending on the args at removal — the deprecation
  window + warning telemetry surface stragglers before the breaking step;
  cross-surface confusion — the explicit, decision-backed (D-140) MCP-vs-HTTP
  divergence.

#### `phase-ae10` *(deferred)* — `layer`/`intent` read-shaping argument
- **Owns:** an additive, read-time, parameterized retrieval-shaping input on
  `internal/retrieval` + the MCP/HTTP/SDK retrieve surfaces (no schema change;
  shaped at the renderer/lane layer ae3 parameterizes) — **or** the formal removal
  of the promise from the track principle.
- **RFC:** §6 (retrieval shaping).
- **Deps:** **ae2** (the additive `_meta` read) and **ae3** (the parameterized
  renderer) (W4). **Deferred** — owner confirms scope.
- **Punch-list:** M2.
- **Goal:** resolve the M2 unowned promise — either own `layer`/`intent` as a
  scoped retrieval-output-shaping argument (it is shaping, not identity — which is
  why it left the identity principle) or drop it and amend the principle in the
  same PR.
- **AC sketch:** if owned — an additive read-shaping arg threaded through the
  parameterized renderer with parity + smoke; if dropped — the principle amended
  and this stub deleted in the same PR (no dangling promise).
- **Risk:** conflating "who is asking" (identity) with "how to shape the answer"
  (shaping) — the reason M2 left the principle; keep the stub thin until the owner
  picks own-or-drop.

---

## Cross-cutting requirements (every `phase-ae*`)

- **All-tier parity, same PR (D-067/D-073).** Each capability is implemented once
  in the core/service layer; SDK, HTTP, and MCP are thin callers. Single-user read
  capabilities (apply-a-view, agent filter, drilldown, episode/causal hooks,
  browse, lean read) ship on **{SDK, HTTP, MCP}** with a parity test (MCP
  included) in the same PR. View/policy admin is **{HTTP, MCP}**. The JWT verify
  mode (ae7) is an auth surface across all of its tier's surfaces. A capability's
  side effects (cache invalidation, validation, events) live in the core so no
  surface can omit them.
- **Knob guardrail (D-034).** Every new knob — the ae7 verifier knobs (issuer /
  JWKS URL+file / algorithms / audience / max-stale / mode selector), the
  agent-filter / `agent_views.enabled` / `on_policy_error` / `subject_precedence`
  knobs, `retrieval.browse_default_limit`, the conditional
  `retrieval.topic_filter_scoring_k`, and (on promotion) `retrieval.causal_hook` —
  ships with a tuned default, placement in **every** profile, docs, and a smoke
  check in the same PR. Zero-config start (static keyring default; views off)
  stays smoke-tested.
- **Graceful degradation (D-036).** The own-scope topic filter (ae6), the agent
  filter (ae1), the topic views (ae9), and the causal hook (ae4b) **fail open** to
  the caller's own-scope results on a policy/store error. ae7 must define
  gateway-/JWKS-unreachable behaviour (fail-closed vs keyring fallback) explicitly
  in its plan. Retrieval must still serve gateway-free.
- **P3 in the store layer.** Agent identity, views, and `layer`/`intent` are
  **read-time filters layered on top of** the store's scoped queries — never a new
  unscoped query API. The tenant from the verified key/JWT remains the enforced
  boundary; agent/view only narrow within it. **No agent column on the 12 scope
  tables**; the policy/views table is not a scope table and carries no memory rows.
- **Forward-only migrations.** The only new durable surface is the ae1
  policy-binding table (generalized to named views by ae9) and ae5's new ordered
  query path — both forward-only, both drivers, conformance-suite proven. **No
  migration for agent identity on the scope tables.**
- **Auth aligns with Harbor (verify-never-mint).** ae7 reimplements Harbor's
  Validator/KeySet/JWKS shape in Stowage's own `internal/auth` (cross-module
  `internal/` import is forbidden), same `golang-jwt/jwt/v5`, same claim shape
  (`iss/sub/aud/exp/nbf/iat/tenant/user/session/scopes`; mandatory
  `tenant/user/session` + `exp`; asymmetric-only). Stowage never signs. `agent_id`
  arrives via MCP `_meta`, **not** via the JWT — keep the two seams separate.
- **Decisions & glossary, same PR.** The track parent decision (D-135) plus the
  Wave-0 ledger (D-136–D-140) and the per-phase read/ergonomics entries (D-141+).
  New vocabulary (`_meta` seam, agent identity, agent→topic policy binding,
  read-time scope, agent view, render mode, episode hook, drill handle, browse,
  inverted keyset, own-scope topic filter) lands in `docs/glossary.md`.
- **Smoke same PR.** Every new CLI command / endpoint / MCP tool / SDK API /
  config key gets a `scripts/smoke/phase-aeN.sh` check in the same PR
  (`OK ≥ count(criteria)`, `FAIL = 0`); prior phases' smoke still passes.

---

## Open authoring notes

- **README registration line.** Register the track in `docs/plans/README.md` after
  the launch / post-launch / `h*` / `p*` / `a*` tables, mirroring the precedents,
  with the boilerplate:
  > *An orthogonal, post-launch track that gives Stowage read-time agent identity
  > and per-agent curation without persisting agent on any of the 12 scope tables.
  > Numbered `phase-aeN-*` so it does not collide with the launch (01–27),
  > post-launch (22–27), productionization (`h*`), performance (`p*`), or adoption
  > (`a*`) slots; smoke scripts still match the `scripts/smoke/phase-*.sh` gate.*

  Follow it with the `# | Phase | Owns | RFC | Deps | Decision` summary table
  above and a `Plans:` line listing the `phase-aeN-slug.md` filenames with
  shipped/draft status.
- **Naming.** Use `phase-aeN-slug` for plans and `scripts/smoke/phase-aeN.sh` for
  smoke — the `ae` prefix is distinct from the adoption track's
  `a1`/`a1b`/`a2`/`a3`.
- **Decision IDs.** Next free id is **D-135** (last shipped D-134). D-135 is the
  track parent; the Wave-0 ledger is **D-135–D-140** (settled here). The per-phase
  read/ergonomics entries are **D-141+** (proposed: ae3 → D-141 parameterized
  renderer; ae4a → D-142 citation-ULID drill + episode-now; ae5 → D-143 DESC
  inverted-keyset browse + superseded reuses `ListByStatus`; ae6 → D-144 own-scope
  fail-open lane-aware topic filter; ae4b → D-145 batch links-exist on promotion),
  filed in track order — the owner confirms the exact numbers at author time. Each
  settled or departed-from finding is an entry in `docs/decisions.md`.
- **L1 — ae7 golden-test testability.** ae7's golden tests need a **test signer**
  (to mint fixtures) and an **injectable clock** (`WithClock`) — Harbor tokens are
  signed and carry `exp`/`nbf`. Wire both as test fixtures; **do not** port
  Harbor's `harborClaims`/signers into the shipped binary (the signer lives in
  test code only).
- **M5 — the mechanical Dockyard bump.** Concrete first step in ae1:
  `go get github.com/hurtener/dockyard@v1.8.0 && go mod tidy`, then call the real
  exported `server.RequestMeta(ctx)` / `server.WithRequestMeta(ctx, m)` in
  `internal/mcpserver` — reconcile any lingering charter reference to
  `MetaFromContext` against these real symbols.
- **ae6 lane integration is an explicit open design choice.** Push the topic
  predicate **into each lane's candidate query** vs **widen `scoringK` when a
  topic filter is present**. Either satisfies the no-underfill AC; the plan author
  pins one (it determines whether `retrieval.topic_filter_scoring_k` ships).
- **ae9 default knobs.** Confirm the tuned defaults (proposed:
  `on_policy_error=open`, `subject_precedence=agent,key`) before authoring.
- **Stub hygiene.** ae4b and ae10 stay thin until the owner promotes or drops
  them; if `layer`/`intent` (ae10) is dropped, amend the track principle in the
  same PR (no dangling promise). ae4b's smoke SKIPs until promoted.
- **RFC section numbers.** Cited best-effort (read path §4.2, memories §5.2,
  scopes §5.3, topics §5.4, identity/auth §5.5, typed links §5.6, injections §5.7,
  retrieval/episodic §6/§6b, store seam §8.1, surfaces §9.1–9.5). There is no
  standalone "retrieval" section — confirm each against `RFC-001-Stowage.md` while
  authoring; the RFC wins.
- **Informing briefs.** Cite the exact `docs/research/INDEX.md` brief id in each
  plan (reader-levers for ae3/ae4a; topic-magnet/D-089 for ae6/ae9; causal/temporal
  for ae4b) — a plan citing no concrete brief is a drift signal (§16).
- **Open posture decisions to settle before the dependent plan.** ~~D-137
  (multiplexing-vs-strict)~~ **SETTLED 2026-06-30** — default STRICT, two orthogonal
  knobs (`identity.multiplexing` default `false`, per-credential authority post-ae7;
  `retrieval.read_posture` default `compatible`); session always per-call; see the
  D-137 entry in `docs/decisions.md`. Still open: ae7 JWKS-unreachable behaviour
  (fail-closed vs keyring fallback under D-036); the ae2b deprecation-window length;
  whether `project` should also be accepted from a future Harbor JWT claim (parked:
  M1 home = `_meta`; the JWT does not carry project today).
- **Mirror.** Any `CLAUDE.md` touch must keep `AGENTS.md` verbatim-identical
  (`make check-mirror`); run `make drift-audit` + `make preflight` before
  committing each phase.
