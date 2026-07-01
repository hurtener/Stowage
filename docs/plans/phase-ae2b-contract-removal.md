# Phase ae2b — breaking removal of `project_id`/`user_id` from MCP contracts

- **Status:** draft
- **Owning subsystem(s):** `internal/mcpserver` (the 13 tool input contracts + their handler scope-construction sites, the `_meta` intake helper); `internal/config` (a new `mcp.deprecated_args_mode` knob); `docs/` (the sanctioned MCP-vs-HTTP divergence, filed as D-140)
- **RFC sections:** D-125 (sub-tenant targeting), §9.5 (one logic core, tiered surfaces, D-067/D-073, the sanctioned contract-divergence precedent — `assert`'s deliberate HTTP omission)
- **Depends on phases:** **ae7** (the JWT verifier — the C4 gate: `auth.Key`/a JWT must carry a verified `user`/`session` before any arg can be retired) and **ae8** (`identity.ResolveReadScope` — the effective-scope resolver that sources identity from `_meta`/JWT without the args). **Hard gate, not a soft ordering preference:** removing the args before ae7+ae8 exist would collapse MCP sub-tenant targeting to tenant-wide with no replacement (the whole risk this phase exists to avoid). Transitively depends on **ae2** (the `_meta` intake helper `readMetaIdentity`/`metaElseArg` this phase extends to read `_meta.project`) and **ae1** (`identity.Scope.Agent`, inert here but load-bearing for the surrounding surface). A deliberately **late** phase (Wave 4).
- **Informing briefs:** 02 (CC-memory predecessor — surface-sprawl cautionary tale: the fix is one intake seam extended in place, not a second parallel arg-reading path bolted on beside it), 01 (Python predecessor — the scoping pain point this track closes: identity riding in on model-discretionary arguments; this phase is the point where that pattern is finally retired on MCP, not just supplemented), 04 (CL-Bench — a silent tenant-wide fallback is a precision/recall failure the gain metric punishes; the binding risk here is never letting removal produce that fallback silently).

## Goal

When this phase is done, the 13 MCP tool input contracts that carry `project_id`/
`user_id` as the sole D-125 sub-tenant **read/mutate targeting** mechanism no
longer do so. The retirement happens in two sequential, separately-shippable
steps, both owned by this plan:

1. **Deprecation window.** The two args stay on the wire (accepted, unmodified
   JSON schema) and continue to resolve scope exactly as today — through ae8's
   `IdentitySources.ArgUser`/`ArgProject`, its documented **lowest-precedence**
   slot — so **no caller's behaviour changes**. Whenever a call's effective scope
   for a dimension actually comes from the arg (i.e. `_meta`/JWT supplied nothing
   for that dimension), the handler emits one versioned warning event
   (`mcp.legacy_scope_arg_used`, schema-versioned via its JSON payload) naming the
   tool and the still-load-bearing arg(s). A `mcp.deprecated_args_mode` knob
   (`warn` default | `reject`) lets an operator dry-run the removal — `reject`
   turns a still-load-bearing arg into an MCP tool error instead of a silent
   accept — without waiting for a second release.
2. **Removal.** Once telemetry (or a `reject`-mode bake period) shows no
   straggler, `ProjectID`/`UserID` are deleted from the 13 structs (a breaking
   JSON-schema change) and their handler sites stop populating
   `IdentitySources.ArgUser`/`ArgProject`. Identity now resolves purely from
   `_meta`/JWT via `identity.ResolveReadScope` for every one of those 13 tools,
   proven by an effective-scope integration test. `project_id`'s final home
   (M1) is settled as **`_meta.project`** — this phase extends ae2's intake
   helper to read it, closing the gap ae2 deliberately left open.

The **MCP-vs-HTTP identity divergence** this phase completes — MCP identity now
lives in `_meta`/JWT only, HTTP keeps its `?project_id=`/`?user_id=` query-param
and JSON-body projection unchanged — is filed as **D-140**, a sanctioned,
documented contract difference under the one-logic-core rule, not drift.

## Brief findings incorporated

- **02 (CC-memory surface-sprawl):** the predecessor failure mode was divergent,
  copy-pasted per-surface logic. ae2b does not add a second `_meta`-reading path
  next to ae2's `readMetaIdentity` — it **extends** that one helper (adds
  `Project`) and **deletes** the arg fields from the wire contracts, so there is
  exactly one identity-intake seam on MCP before and after this phase.
- **01 (Python predecessor):** the recurring predecessor pain was identity
  arriving only via model-discretionary request fields, with no trustworthy
  per-call channel. This phase is the point where that pattern is *retired* on
  MCP (not merely supplemented, as ae2 was) — the args go away, `_meta`/JWT is
  the only remaining channel.
- **04 (CL-Bench):** a read that silently widens to tenant-wide is exactly the
  failure the gain metric punishes. The binding constraint on this phase (stated
  in the charter and restated below) is that removal must never produce that
  widening silently — hence the `reject`-mode dry-run and the warning telemetry,
  not a single flag-day cutover.

## Findings I'm departing from

- **"Accepted-but-ignored" cannot mean *functionally* ignored during the
  deprecation window, or the window itself becomes the P3 regression it exists
  to prevent.** The charter's AC-1 says the deprecation phase is "args
  accepted-but-ignored, each use emits a versioned warning event." Read
  literally — the arg parsed off the wire but not applied to scope — a straggler
  caller who has not yet started injecting `_meta`/using a JWT loses its
  sub-tenant targeting the moment the deprecation-window PR merges, i.e.
  **before** anyone had a chance to notice the warning and migrate. That is
  precisely the "MCP collapses to tenant-wide" regression the binding rules
  forbid. This plan departs from the literal reading: during the **warn**
  sub-mode, the arg keeps doing exactly the scope-resolution work it does today
  (fed into ae8's `IdentitySources.ArgUser`/`ArgProject` as the documented
  lowest-precedence source — unchanged from ae8's own design). "Ignored" is
  realized as *deprecated and telemetered*, not *inert*: a golden test pins that
  a warn-mode call with only the legacy arg (no `_meta`/JWT) resolves to the
  **same** scope as before this phase, while also emitting the warning — which
  is exactly what the charter's own AC-1 asks for immediately afterward ("a
  golden test pins the warning event AND the unchanged behaviour"). The
  `reject` sub-mode is where an operator can *choose* to make the arg
  genuinely inert (as a controlled dry-run, opt-in, reversible by a config
  flip) ahead of the irreversible code-removal step. This departure is what
  makes AC-1's two clauses ("ignored" + "unchanged behaviour") consistent with
  each other and with the binding "never tenant-wide" rule; it does not touch
  the charter's actual constraint (ae2b must not leave a tenant-wide window).
- **The charter's "~16 contracts" is not the precise removal set; the precise
  count is 13.** A full audit of `internal/mcpserver/contracts.go` finds
  **16** distinct MCP input-side types carrying a `project_id`/`user_id`-tagged
  field: `IngestRecord` (:16-18), `IngestTargetScope` (:40-41), `RetrieveInput`
  (:70-72), `PlaybookInput` (:126-128), `EpisodesInput` (:187-189),
  `CausalInput` (:221-223), `VerifyInput` (:266-268), `ReviewInput` (:288-291),
  `TraceInput` (:319-321), `DrilldownInput` (:330-332), `FeedbackInput`
  (:359-362), `BranchInput` (:449-451), `GrantsInput` (:467-478), `GetInput`
  (:574-576), `RollbackInput` (:594-596), `ResolveInput` (:611-613) — 16 in
  total, which is where the charter's "~16" comes from. But only **13** of
  them carry the literal comment *"ProjectID/UserID scope the … to a
  sub-tenant identity (P3, D-125); empty = tenant-wide"* and feed the exact
  pattern `identity.Scope{Tenant: scope.Tenant, Project: in.ProjectID, User:
  in.UserID}` in `handlers.go` (lines 177, 258, 314, 399, 554, 587, 615, 758,
  944, 1020, 1063, 1087, 1128) — `RetrieveInput`, `PlaybookInput`,
  `EpisodesInput`, `CausalInput`, `VerifyInput`, `ReviewInput`, `TraceInput`,
  `DrilldownInput`, `FeedbackInput`, `BranchInput`, `GetInput`, `RollbackInput`,
  `ResolveInput`. The other **3** carry the same field names for a *different*
  purpose entirely and are explicitly **out of scope** for this phase:
  - `IngestRecord` — per-record `project_id`/`user_id` are **write-time**
    identity stamps on a verbatim record (P1); removing them would be an
    ingest/fidelity change, not a read-targeting one, and is not this track's
    concern (the track charter is scoped to the **read-side** identity gap).
  - `IngestTargetScope` — the explicit **contribute-mode** cross-scope write
    target (D-059/D-071), gated by an active contribute grant. This is a
    deliberate, grant-authorized targeting mechanism, not the D-125
    "omittable-arg-defaults-tenant-wide" pattern this phase retires; removing
    it would break the grants contribute feature outright.
  - `GrantsInput` — its `UserID`/`ProjectID` target a **group member** or a
    **grant's owner scope** (Tier B admin, `{HTTP, MCP}` only) — the caller is
    naming *someone else's* identity to administer, not narrowing its own read.
    There is no tenant-wide-fallback risk here to retire.
  Also excluded, for a related reason: `ProactiveConfigInput` (`:660-668`,
  handler `handlers.go:1214`) carries sub-tenant targeting via `User`/`Project`
  fields (JSON tags `user`/`project`, **not** `user_id`/`project_id`) — same
  shape, different wire name, and not named by the charter's "`project_id`/
  `user_id`" phrasing. It is left untouched by this phase; a follow-up can
  fold it in once `_meta.project`/`_meta.user` are the norm, but doing so here
  would silently widen this phase's stated contract beyond "the `project_id`/
  `user_id` MCP args."
  This correction narrows the phase's `Files added or changed` and Acceptance
  Criteria to the 13 structs actually described by the charter's Goal
  ("retire the MCP targeting args … the sole D-125 sub-tenant targeting
  mechanism"), rather than 16 or 14.
- **Line numbers cited throughout are pre-ae-track code truth, not the code
  ae2b will actually touch.** At authoring time none of ae1/ae2/ae7/ae8 has
  landed (`identity.Scope` has no `Agent` field; `go.mod` pins `dockyard
  v1.7.3`; `handlers.go` still builds scope with the raw
  `identity.Scope{Tenant: scope.Tenant, Project: in.ProjectID, User:
  in.UserID}` literal at the 13 lines above). By the time ae7+ae8 land, ae2
  will have replaced those literals' *identity source* (via `readMetaIdentity`/
  `metaElseArg`) and ae8 will have replaced their *construction* (via a shared
  `resolveScope(svc, ctx, in)` adapter calling `identity.ResolveReadScope`).
  The line numbers here are load-bearing only as **evidence for the 13-struct
  count and the removal list**; the actual diff ae2b lands touches whatever
  ae2/ae8 leave behind — the shared adapter and the shared intake helper, not
  13 independent literals. Mirrors ae8's own "code-truth corrections" posture
  (D-148) for the same reason: this track's plans are authored ahead of their
  dependencies landing.
- **The charter frames `project_id`'s M1 resolution as open ("dropped, or
  carried as a `_meta` key").** The track's own identity-model table (line 97
  of the charter) already answers this: *"project | `_meta.project` (its
  explicit home) → MCP `project_id` arg (additive, until ae2b)."* This plan
  does not reopen that choice; it **executes** it — `_meta.project` is
  `project_id`'s permanent home, and the arg is dropped in the removal step.
  Recorded as the explicit M1 resolution below (not a new decision — the
  charter already settled it; this phase is where it takes effect in code).

## Design

### Step 1 — deprecation window (accepted, telemetered, unchanged behaviour)

**No contract change in this step.** The 13 structs keep their
`ProjectID`/`UserID` fields byte-identical. Two additions, both in
`internal/mcpserver`:

**(a) Extend the shared intake helper, don't duplicate it.** ae2's
`internal/mcpserver/metaintake.go` (`readMetaIdentity`, `metaIdentity`,
`metaElseArg`) is the one seam every one of the 13 handlers already calls.
ae2b adds a `Project` field to `metaIdentity` and reads `_meta.project` inside
`readMetaIdentity` (ae2 deliberately deferred this — "ae2 does not read
`_meta.project`… its `_meta` home or removal is ae2b"). This closes M1: after
this phase, `_meta.project` is the only channel for project narrowing on MCP.

**(b) The legacy-arg detector + warning event.** A small helper next to the
existing intake:

```go
// legacyArgUse reports, per dimension, whether a call's effective scope value
// came from the deprecated project_id/user_id arg rather than from _meta/JWT —
// i.e. whether the arg is still load-bearing for this call. It does not decide
// what happens next (warn vs reject); callers apply the configured mode.
type legacyArgUse struct {
    Project bool // in.ProjectID != "" && mi.Project == "" (and no JWT-claim project — none exists)
    User    bool // in.UserID != "" && higher-precedence sources supplied nothing
}

func detectLegacyArgUse(mi metaIdentity, in scopeArgs) legacyArgUse
```

`scopeArgs` is the small struct each of the 13 handlers already has inline
(`ProjectID`/`UserID` from `in`). `detectLegacyArgUse` mirrors — does not
reimplement — ae8's own precedence order (`_meta`/claim beats arg), so the
"is the arg still doing work" question is answered the same way
`ResolveReadScope` answers "what wins."

When `detectLegacyArgUse` reports true for either dimension, the handler:

- in **`warn`** mode (`mcp.deprecated_args_mode=warn`, the default): proceeds
  exactly as it does today (the arg still feeds `IdentitySources.ArgProject`/
  `ArgUser`, ae8's documented lowest-precedence slot — no resolution change)
  and emits one event via the existing audit-trail mechanism:

  ```go
  _ = st.Events().Emit(ctx, scope, store.Event{
      ID: ulid.Make().String(), TenantID: scope.Tenant,
      Type: "mcp.legacy_scope_arg_used", SubjectID: toolName,
      Reason: "project_id/user_id MCP arg still load-bearing; migrate to _meta or a JWT claim before removal (D-140)",
      Payload: legacyArgUsePayload{SchemaVersion: 1, Tool: toolName, Args: firedArgs}.marshal(),
      CreatedAt: now,
  })
  ```

  This reuses `internal/store.EventStore.Emit` — the RFC §5.8/D-024 audit
  trail already used by every other mutation/lifecycle event
  (`memory.pending_review`, `memory.superseded`, …) — **not** a new
  `internal/events` SSE bus. CLAUDE.md §8 and the repo layout describe a
  future "versioned, consumable `events/v1`" stream; that package does not
  exist in this codebase today (confirmed: no `internal/events` directory,
  no `EventBus`/`Emit` type outside `store.EventStore` and the unrelated
  Harbor-adapter `harborevents.EventBus`). This plan's "versioned warning
  event" is realized as a `store.Event` whose `Type` follows the existing
  dot-namespaced convention (`mcp.legacy_scope_arg_used`, alongside
  `memory.superseded`, `gateway.call`, …) and whose **payload carries an
  explicit `schema_version` field** — the versioning the charter asks for,
  applied to the mechanism that actually exists rather than to one that
  doesn't. Best-effort emit (`_ =`), matching every other audit-event call
  site in the codebase — a dropped warning event never blocks a read (P2/D-036
  spirit: telemetry failure must not fail the call).

- in **`reject`** mode: the handler returns an MCP tool error (mirroring
  `identity.ErrTenantMismatch`'s style — an HTTP-4xx-class reject, a redacted,
  value-free reason) *instead of* resolving the read, and still emits the
  warning event (so `reject` mode's rejections are themselves auditable). This
  lets an operator flip one config value to see, without a second deploy,
  exactly which calls would break under removal — the dry-run the charter's
  own risk section calls for ("the deprecation window + warning telemetry
  surface stragglers before the breaking step").

Both modes run through **one** shared call site
(`applyLegacyArgPolicy(ctx, svc, toolName, mi, in) error`) added to
`metaintake.go`, called by all 13 handlers right after `readMetaIdentity` —
no per-handler branching, matching ae2's one-seam precedent.

### Step 2 — removal (breaking)

A separate, later PR (this plan governs both, but they ship as two releases
with a bake period between them, gated on zero `mcp.legacy_scope_arg_used`
events in `reject`-mode dry-run telemetry):

1. Delete `ProjectID`/`UserID` from the 13 structs listed above.
2. Delete `detectLegacyArgUse`/`applyLegacyArgPolicy` and the
   `mcp.deprecated_args_mode` knob (dead code once there is nothing left to
   detect).
3. The 13 handler sites stop populating `IdentitySources.ArgProject`/
   `ArgUser` for these tools (there is no field left to read them from);
   identity resolves purely from `_meta`/JWT via
   `identity.ResolveReadScope`/`resolveScope`.
4. `project_id` keeps working as a wire concept **via `_meta.project` only**
   (M1, closed in Step 1(a); no further change needed here).

**The honest residual risk (stated, not solved away):** `encoding/json`
silently discards an unknown field on `Unmarshal`. A caller that still sends
`{"project_id": "p1", ...}` after this PR ships gets **no error** — the field
is dropped, and the call resolves whatever `_meta`/JWT alone supplies (which,
for a caller that never migrated, is likely nothing, i.e. tenant-wide). The
deprecation window's `reject`-mode bake period is the *only* thing standing
between "no stragglers observed" and "a straggler silently regresses to
tenant-wide" — there is no way to make an unknown-field removal itself
fail loud on the wire without inventing a stricter decoder for every MCP tool
(rejected as disproportionate; see Risks).

### `mcp.deprecated_args_mode` knob (D-034)

Home: `MCPConfig` (`internal/config/config.go:35-40`), alongside
`stdio_tenant` — it is an MCP-surface-specific knob, not a `retrieval.*` or
`identity.*` one (this phase changes no resolution precedence, only whether a
still-load-bearing legacy arg is tolerated or rejected). This is the
"deprecation-window-length" knob the punch list anticipates, realized as a
**mode** rather than a **duration**: a wall-clock cutover would need a clock
dependency and a scheduled config flip disproportionate to the problem, and
would still require an operator decision about when it's safe — a mode
switch gives the operator that same decision without inventing a scheduler.

### D-140 filing (not edited into `docs/decisions.md` by this agent — see the
task's return-value contract)

The MCP-vs-HTTP identity divergence this phase completes is filed as **D-140**
(full text returned at the end of this task, per instruction, for the human/
maintainer to append). HTTP's `scopeFromRequest` (`internal/api/auth.go:72-80`)
and every POST handler's `project_id`/`user_id` body fields are **unchanged**
by this phase — HTTP keeps its query-param/body projection permanently. This
is the same class of sanctioned per-surface difference as `assert`'s
deliberate HTTP omission (D-067's one-logic-core rule tolerates a *contract*
difference as long as the *capability* and its side effects are not
duplicated or forked — which holds here: there is one `ResolveReadScope`
core, and MCP/HTTP differ only in which `IdentitySources` fields their thin
adapters populate).

### Surfaces & parity (D-067/D-073)

This phase changes **only** the MCP surface's wire contracts. **HTTP is
untouched** (D-140 — its query-param/body projection is the sanctioned
divergent path). **SDK is untouched**: `sdk/stowage`'s HTTP-mode client talks
to the (unchanged) HTTP endpoints, so it still sends `project_id`/`user_id`
in the wire body exactly as today; the SDK's embedded-mode `callScope`
constructs scope in-process from caller-supplied Go values, which is a
different (non-`_meta`) identity channel entirely and out of this phase's
scope. Because no capability is added or removed on SDK/HTTP — only MCP's
*targeting mechanism* changes — the D-067/D-073 parity obligation here is
**behavioural, not contractual**: a parity test proves that an MCP call
narrowed via `_meta.user`/JWT and an HTTP call narrowed via `?user_id=`
resolve to the *same effective scope* and the store returns the same rows,
for every one of the 13 affected tools' HTTP-mirror endpoints. This is the
same behavioural-parity posture ae2 and ae8 already establish for the
`_meta`-vs-arg source divergence; ae2b is the phase where the MCP side of
that divergence becomes permanent instead of additive.

## Files added or changed

```text
internal/mcpserver/metaintake.go           # CHANGED (step 1) — metaIdentity gains Project; readMetaIdentity reads _meta.project; NEW detectLegacyArgUse, applyLegacyArgPolicy, legacyArgUsePayload
internal/mcpserver/metaintake_test.go      # CHANGED (step 1) — _meta.project extraction; legacy-arg detection matrix; warn/reject behaviour; concurrent-reuse (-race)
internal/mcpserver/handlers.go             # CHANGED (step 1) — all 13 sites call applyLegacyArgPolicy after readMetaIdentity; CHANGED (step 2) — ProjectID/UserID no longer read at those 13 sites
internal/mcpserver/contracts.go            # CHANGED (step 2, breaking) — delete ProjectID/UserID from RetrieveInput, PlaybookInput, EpisodesInput, CausalInput, VerifyInput, ReviewInput, TraceInput, DrilldownInput, FeedbackInput, BranchInput, GetInput, RollbackInput, ResolveInput (13 structs; IngestRecord/IngestTargetScope/GrantsInput/ProactiveConfigInput are explicitly untouched — see departures)
internal/config/config.go                  # CHANGED (step 1) — MCPConfig gains DeprecatedArgsMode; allKeys; Defaults; get/set; Validate (enum); CHANGED (step 2) — knob + its Validate branch removed
internal/config/profiles.go                # CHANGED (step 1) — mcp.deprecated_args_mode=warn effective in every profile
internal/config/testdata/explain_default.golden  # CHANGED (step 1) — one new key line; CHANGED (step 2) — line removed
test/integration/legacy_arg_removal_test.go # NEW (step 2, §17 — closes a seam opened by ae7/ae8) — real-driver: pre-removal fixture calls that used to rely on project_id/user_id now resolve identically via _meta/JWT; a genuinely unmigrated caller (no _meta, no JWT claim) is proven to fall back exactly to ae8's compatible/strict posture (never a silent extra-wide read beyond what ae8 already defines) under -race
test/integration/http_mcp_scope_parity_test.go # NEW (step 1, §17) — the same identity, asserted via MCP _meta and via HTTP query/body params, resolves to the same effective scope and the same store rows, for each of the 13 affected tools' HTTP mirror
scripts/smoke/phase-ae2b.sh                # NEW
docs/plans/README.md                       # CHANGED — ae-track table, ae2b row flipped to shipped (delta returned, not edited here)
docs/decisions.md                          # CHANGED — D-140 (full text returned at the end of this task, not edited here)
docs/glossary.md                           # CHANGED — deprecation window, versioned warning event, legacy scope arg, deprecation mode (delta returned, not edited here)
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `mcp.deprecated_args_mode` | `warn` | Enum `warn`\|`reject`. `warn`: a still-load-bearing `project_id`/`user_id` on any of the 13 tools resolves exactly as before this phase and emits `mcp.legacy_scope_arg_used`. `reject`: the same detection instead returns an MCP tool error (dry-run of removal, reversible by flipping the knob back). Removed entirely in Step 2 (nothing left to detect once the fields are gone). Home: `MCPConfig` (`internal/config/config.go`), sibling to `mcp.stdio_tenant`. D-034-complete: tuned default (`warn` — zero-config, byte-identical behaviour on upgrade), effective in every profile, documented, `allKeys`/get/set/explain, validated (enum membership), smoke-checked. |

## Acceptance criteria (binding)

1. **Deprecation phase — unchanged behaviour, telemetered.** For each of the 13
   tools (`memory_retrieve`, `memory_playbook`, `memory_drilldown`,
   `memory_get`, `memory_episodes`, `memory_causal`, `memory_trace`,
   `memory_verify`, `memory_review`, `memory_feedback`, `memory_rollback`,
   `memory_resolve`, `memory_branch`): a call whose `project_id`/`user_id`
   arg is still load-bearing (no `_meta`/JWT value for that dimension)
   resolves to the **same** effective scope it does today (golden/regression
   test), and emits exactly one `mcp.legacy_scope_arg_used` event
   (`store.EventStore.Emit`, `Payload.schema_version=1`) naming the tool and
   the fired arg(s). A call whose dimension is already supplied by `_meta`/JWT
   emits **no** warning (the arg is not load-bearing there — nothing to warn
   about).
2. **`reject` mode dry-runs removal.** With `mcp.deprecated_args_mode=reject`,
   the same still-load-bearing call is rejected with a redacted,
   value-free MCP tool error instead of resolving, and still emits the
   warning event; flipping the knob back to `warn` restores today's
   behaviour with no code change.
3. **Removal phase — the 13 contracts drop the args; identity still
   resolves.** `ProjectID`/`UserID` no longer appear on `RetrieveInput`,
   `PlaybookInput`, `EpisodesInput`, `CausalInput`, `VerifyInput`,
   `ReviewInput`, `TraceInput`, `DrilldownInput`, `FeedbackInput`,
   `BranchInput`, `GetInput`, `RollbackInput`, `ResolveInput` (grep-asserted).
   An effective-scope integration test (`test/integration/legacy_arg_removal_test.go`)
   proves that for every one of those 13 tools, a call carrying `_meta`/a
   verified JWT claim resolves to the identical scope it resolved to
   pre-removal via the arg, with real drivers, under `-race`. A call with
   neither `_meta` nor a JWT claim behaves exactly as ae8's
   `retrieval.read_posture` already defines for an absent identity
   (`compatible` ⇒ tenant-wide, unchanged from ae8; `strict` ⇒
   `ErrIdentityRequired`) — this phase introduces **no third fallback
   behaviour** of its own.
4. **The 3 out-of-scope lookalikes are provably untouched.** `IngestRecord`,
   `IngestTargetScope`, `GrantsInput`, and `ProactiveConfigInput` still carry
   their `project_id`/`user_id`(or `project`/`user`) fields unchanged after
   both steps (grep/diff-asserted) — this phase's removal set is exactly the
   13 structs named above, never the full 16-or-more lookalikes.
5. **`project_id`'s M1 resolution lands in code.** `_meta.project` is read by
   `readMetaIdentity` (extends ae2's `metaIdentity`) and reaches
   `IdentitySources.MetaProject` for every one of the 13 tools; after Step 2,
   `_meta.project` is the *only* MCP channel for project narrowing (the arg
   is gone). A test exercises a `_meta.project`-only call narrowing correctly.
6. **D-140 filed and honored.** The MCP-vs-HTTP divergence is documented (this
   plan + the returned decision text); HTTP's `scopeFromRequest` and every
   POST handler's `project_id`/`user_id` body field are byte-unchanged by
   this phase (grep/diff-asserted against `internal/api`); a behavioural
   parity test (`http_mcp_scope_parity_test.go`) proves the same identity
   resolves to the same scope and the same rows via MCP `_meta` and via
   HTTP's query/body projection, for each of the 13 tools' HTTP mirror.
7. **No unscoped read introduced (P3).** ae2b adds no `internal/store` query
   method and no new store predicate (grep-asserted, mirroring ae8's AC-4);
   the three existing scope predicates
   (`buildScopeWhere`/`buildExactScopeWhere`/`vectorStore.Scan`) remain the
   only read filter and still fail closed on an empty tenant.
8. **Knob D-034-complete (Step 1); cleanly removed (Step 2).**
   `mcp.deprecated_args_mode` ships with a tuned default (`warn`), is present
   in every profile's effective config, is documented, appears in
   `allKeys`/get/set/explain, is validated (enum membership), and is
   smoke-checked; Step 2's PR removes the knob and its Validate branch in the
   same PR that removes the fields (no dangling dead config).
9. **Parity + smoke same-PR.** Both steps ship a `scripts/smoke/phase-ae2b.sh`
   check (extended, not duplicated, across the two PRs); prior phases' smoke
   (ae1/ae2/ae7/ae8 in particular) still passes after each step.

## Smoke script

`scripts/smoke/phase-ae2b.sh` — SKIPs gracefully until the surface is built
(today: unconditionally, since ae1/ae2/ae7/ae8 are all unbuilt); then, once
Step 1 lands:
- assert `mcp.deprecated_args_mode` is registered in config with default `warn`
  (`stowage config get mcp.deprecated_args_mode`).
- assert `internal/mcpserver/metaintake.go` defines `detectLegacyArgUse` and
  `applyLegacyArgPolicy`, and `metaIdentity` carries a `Project` field.
- assert `go test ./internal/mcpserver/ -run LegacyArg` passes (warn/reject
  behaviour + unchanged-scope regression).
Once Step 2 lands (superseding the Step-1-only checks above):
- assert none of the 13 structs (grep by name in `contracts.go`) declares a
  `project_id`/`user_id` json tag.
- assert `mcp.deprecated_args_mode` is **absent** from config (fully retired).
- assert `IngestRecord`/`IngestTargetScope`/`GrantsInput` still declare
  `project_id`/`user_id` (the exclusion list untouched).
- `go test ./test/integration/ -run LegacyArgRemoval|HTTPMCPScopeParity` passes.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Unit (`metaintake_test.go`):** `_meta.project` extraction (present/absent/
  non-string, mirroring the existing `user`/`session`/`agent_id` cases);
  `detectLegacyArgUse` truth table (arg-only, `_meta`-only, both, neither) ×
  the 13 tools; `applyLegacyArgPolicy` in `warn` (proceeds + emits) and
  `reject` (rejects + still emits) modes; a concurrent-reuse test under
  `-race`.
- **Golden/regression (per tool, Step 1):** a fixture call using only the
  legacy arg resolves to the identical scope and downstream call as the
  pre-ae2b fixture (byte-for-byte, proving "unchanged behaviour"); a fixture
  call using only `_meta`/a JWT claim emits no warning.
- **Integration (`legacy_arg_removal_test.go`, real drivers — sqlite +
  postgres, §17, Step 2, `-race`):** seed memories under distinct
  `{tenant,user}` pairs; for each of the 13 tools, a `_meta`/JWT-narrowed
  call post-removal returns the same rows a pre-removal arg-narrowed call
  returned; a no-identity call matches ae8's configured posture exactly
  (the ≥1 failure mode: `strict` posture refuses).
- **Integration (`http_mcp_scope_parity_test.go`, real drivers, §17, Step 1):**
  the same seeded identity, asserted via MCP `_meta` and via HTTP
  `?project_id=`/`?user_id=`, resolves to the same scope and the same rows,
  for each of the 13 tools' HTTP mirror endpoint.
- **Regression:** ae1/ae2/ae3/ae4a/ae5/ae6/ae7/ae8/ae9's existing tests and
  smoke scripts pass unchanged after both steps (checkpoint-audit discipline,
  §17).
- **No new fuzz target.** ae2b removes a wire field (a schema *contraction*);
  it adds no new parse/decode surface. `encoding/json`'s existing
  unknown-field-tolerant `Unmarshal` is the surface that changes behaviour on
  legacy input — noted under Risks, not fuzzed (there is no invariant to
  assert beyond "no panic," which the existing decode path already proves).

## Risks & mitigations

- **A caller still depending on the args at removal (the charter's named
  risk).** Mitigated in two layers: (1) the deprecation window's warning
  telemetry surfaces stragglers while behaviour is still unchanged (Finding:
  "accepted-but-ignored" departure); (2) the `reject`-mode dry-run lets an
  operator verify zero stragglers *before* the breaking PR ships, without
  waiting for a second release cycle. **Stated plainly, not solved away:**
  `encoding/json`'s silent unknown-field drop means a caller who never ran
  against `reject` mode and never fixed the warning will silently regress to
  whatever `_meta`/JWT alone supplies (typically tenant-wide under the
  `compatible` posture) the moment Step 2 ships — there is no wire-level way
  to make that caller's request fail loud without a stricter decoder this
  plan deliberately does not add (disproportionate to a two-field
  deprecation). The bake-period discipline (documented in the plan, enforced
  operationally, not mechanically) is the real mitigation.
- **Cross-surface confusion (MCP moves, HTTP doesn't).** Mitigated by the
  explicit, decision-backed (D-140) divergence, the untouched-HTTP grep
  assertion (AC-6), and the behavioural (not contractual) parity test.
  Documented in the tool docs so an operator migrating MCP callers does not
  also try to migrate HTTP callers that were never asked to change.
- **Over-scoping the removal to the full 16 (or 14, including
  `ProactiveConfigInput`) lookalikes.** Mitigated by the departures section's
  precise 13-struct audit and AC-4's grep pinning the 3 (4) exclusions —
  a future implementor tempted to "clean up the rest while they're in there"
  would be quietly expanding a read-side-identity phase into an ingest/
  grants/admin-contract change with a different risk profile.
- **`mcp.deprecated_args_mode` becoming permanent scope creep.** Mitigated by
  AC-8's requirement that Step 2 removes the knob in the same PR that removes
  the fields — there is nothing left to warn about or reject once the args
  are gone, so the knob has a defined end of life, unlike most D-034 knobs.
- **Blocking on ae1/ae2/ae7/ae8, all unbuilt at authoring time.** Mitigated
  the way ae8 handled the same situation (D-148): this plan is written
  against ae2's and ae8's *specified* seams (`readMetaIdentity`,
  `IdentitySources`, `ResolveReadScope`) rather than against code that
  exists yet; the hard dependency (ae7+ae8 must land first) is structural,
  not just a nice-to-have ordering — attempting Step 1 before ae8 lands has
  no `IdentitySources.ArgProject`/`ArgUser` slot to keep the arg
  load-bearing in, and attempting it before ae7 lands has no verified
  `user`/`session` claim to fall back to, which is exactly the charter's C3/
  C4 finding restated.

## Glossary additions

- **Deprecation window** — the period, on the MCP surface, during which a
  soon-to-be-removed argument (here, `project_id`/`user_id`) is still
  accepted and still resolves scope exactly as before, while its use is
  telemetered (a versioned warning event) and optionally made rejectable
  (`mcp.deprecated_args_mode=reject`) as a dry-run of the eventual breaking
  removal. Distinct from simply "ignoring" the argument, which would
  reproduce the exact tenant-wide regression the window exists to prevent.
- **Versioned warning event** — a `store.Event` (the existing RFC §5.8/D-024
  audit-trail mechanism, `internal/store.EventStore.Emit`) whose `Payload`
  JSON carries an explicit `schema_version` field, used to signal a
  deprecated-but-still-functioning code path without requiring the
  not-yet-built `internal/events` SSE stream. The `Type` string follows the
  existing dot-namespaced convention (e.g. `mcp.legacy_scope_arg_used`).
- **Legacy scope arg** — a `project_id`/`user_id` MCP tool argument once it
  has an `_meta`/JWT-borne replacement source (ae2/ae7/ae8); "still
  load-bearing" means the argument is, for a given call, the *only* source
  supplying that scope dimension (no higher-precedence `_meta`/JWT value
  exists for it).
- **Deprecation mode** — the `mcp.deprecated_args_mode` knob (`warn`|
  `reject`) governing what happens when a legacy scope arg is detected as
  still load-bearing: proceed-and-telemeter, or refuse-and-telemeter. Retired
  in the same PR that removes the underlying arguments.

## Decisions filed

- **D-140** — MCP-vs-HTTP identity divergence is a sanctioned contract
  divergence (full text returned at the end of this task for the maintainer
  to append to `docs/decisions.md`; not edited by this agent per the task's
  instructions).
