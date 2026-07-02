# Phase ae2b — direct removal of `project_id`/`user_id` from MCP contracts

- **Status:** implemented (see "As-built deviations" below)
- **Owning subsystem(s):** `internal/mcpserver` (the 13 tool input contracts + their handler scope-construction sites, the `_meta` intake helper); `docs/` (the sanctioned MCP-vs-HTTP divergence, filed as D-140)
- **RFC sections:** D-125 (sub-tenant targeting), §9.5 (one logic core, tiered surfaces, D-067/D-073, the sanctioned contract-divergence precedent — `assert`'s deliberate HTTP omission)
- **Depends on phases:** **ae7** (the JWT verifier — the C4 gate: `auth.Key`/a JWT must carry a verified `user`/`session` before any arg can be retired) and **ae8** (`identity.ResolveReadScope` — the effective-scope resolver that sources identity from `_meta`/JWT without the args). **Hard gate, not a soft ordering preference:** removing the args before ae7+ae8 exist would collapse MCP sub-tenant targeting to tenant-wide with no replacement (the whole risk this phase exists to avoid). This is a **correctness** gate, not a compatibility gate — it holds even though there is no backwards-compat obligation to protect (see Findings below). Transitively depends on **ae2** (the `_meta` intake helper `readMetaIdentity`/`metaElseArg` this phase extends to read `_meta.project`) and **ae1** (`identity.Scope.Agent`, inert here but load-bearing for the surrounding surface). A deliberately **late** phase (Wave 4).
- **Informing briefs:** 02 (CC-memory predecessor — surface-sprawl cautionary tale: the fix is one intake seam extended in place, not a second parallel arg-reading path bolted on beside it), 01 (Python predecessor — the scoping pain point this track closes: identity riding in on model-discretionary arguments; this phase is the point where that pattern is finally retired on MCP, not just supplemented), 04 (CL-Bench — a silent tenant-wide fallback is a precision/recall failure the gain metric punishes; the binding risk here is never letting removal produce that fallback silently).

## Goal

When this phase is done, the 13 MCP tool input contracts that carry `project_id`/
`user_id` as the sole D-125 sub-tenant **read/mutate targeting** mechanism no
longer do so. **Stowage is pre-launch with zero external callers**, so there is
no backwards-compat obligation to protect: this phase is a **single, direct
removal**, not a phased deprecation. Once ae7 + ae8 have landed (the hard
correctness gate — see Depends on phases), `ProjectID`/`UserID` are deleted
from the 13 structs in one change (a breaking JSON-schema change, but there is
no caller to break), their handler sites stop populating
`IdentitySources.ArgUser`/`ArgProject`, and identity resolves purely from
`_meta`/JWT via `identity.ResolveReadScope` for every one of those 13 tools —
proven by an effective-scope integration test. `project_id`'s final home (M1)
is settled as **`_meta.project`**; this phase extends ae2's intake helper to
read it, closing the gap ae2 deliberately left open.

There is no interim bake-in period during which the retired arguments are
tolerated-but-inert, no schema-versioned notice event emitted on their use,
and no operator-facing toggle to dry-run the change ahead of time. None of
that ceremony has a caller to protect, so it is pure overhead — it is not
built.

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
  failure the gain metric punishes. The binding constraint on this phase is
  that removal must never produce that widening silently. With no interim
  bake-in ceremony to lean on, this is enforced **structurally** instead of by
  telemetry: the hard ae7+ae8 gate guarantees a non-arg identity source
  (`_meta`/JWT) exists and is wired through `ResolveReadScope` *before* the
  args are ever deleted, so there is no window — of any length — in which
  removal could produce a silent tenant-wide fallback that wasn't already
  ae8's own defined, documented posture for an absent identity.

## Findings I'm departing from

- **The charter specified a phased migration; the actual state is pre-launch
  with zero external callers, so the phasing is dropped.** The charter's AC-1
  described a two-step migration: an interim phase where the args stay on
  the wire, tolerated but no longer authoritative, each still-load-bearing use
  emitting a schema-versioned notice event, gated by an operator-configurable
  mode toggle (proceed-and-notify vs. refuse-and-notify), followed by a
  separate removal step once telemetry showed no stragglers. That ceremony
  exists to protect callers who might be depending on the args today. Stowage
  has no such callers — it has not shipped, so there is nothing to migrate and
  no bake-in period buys any safety. This plan drops the interim phase, its
  notice event, its mode toggle, and the detector helper that would have
  computed "is this arg still load-bearing" entirely, and replaces the
  two-step structure with **one** direct removal. **What does not change:**
  the hard ae7+ae8 gate. That gate was never a compatibility mechanism — it is
  the correctness precondition that a non-arg identity source (`_meta`/JWT via
  `ResolveReadScope`) exists *before* the only existing source (the arg) is
  deleted; skipping it would collapse MCP sub-tenant targeting to tenant-wide
  regardless of whether any external caller exists yet. Pre-launch status
  removes the compatibility obligation; it does not touch the correctness one.
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

### `_meta.project` (M1) — extend the shared intake helper, don't duplicate it

ae2's `internal/mcpserver/metaintake.go` (`readMetaIdentity`, `metaIdentity`,
`metaElseArg`) is the one seam every one of the 13 handlers already calls.
ae2b adds a `Project` field to `metaIdentity` and reads `_meta.project` inside
`readMetaIdentity` (ae2 deliberately deferred this — "ae2 does not read
`_meta.project`… its `_meta` home or removal is ae2b"). This closes M1: after
this phase, `_meta.project` is the only channel for project narrowing on MCP.

### Removal

Once ae7 + ae8 have landed (the hard gate — see Depends on phases), one
change:

1. Delete `ProjectID`/`UserID` from the 13 structs listed above.
2. The 13 handler sites stop populating `IdentitySources.ArgProject`/
   `ArgUser` for these tools (there is no field left to read them from);
   identity resolves purely from `_meta`/JWT via
   `identity.ResolveReadScope`/`resolveScope`.
3. `project_id` keeps working as a wire concept **via `_meta.project` only**
   (M1, above; no further change needed here).

There is no separate deprecation PR, no bake period, and no knob to flip —
ae7+ae8 landing is the only precondition, because it is the only thing that
determines whether a non-arg identity source exists to resolve from.

**The honest residual note (stated, not solved away):** `encoding/json`
silently discards an unknown field on `Unmarshal`. If a caller ever sent
`{"project_id": "p1", ...}` after this PR ships, it would get **no error** —
the field is dropped, and the call resolves whatever `_meta`/JWT alone
supplies. Stowage has no such caller today (pre-launch, zero external
integrations), so this is not a migration risk this phase needs to mitigate —
it is a permanent property of the removed wire shape that any *future*
integration must be built against from day one (i.e., against `_meta`/JWT,
never against `project_id`/`user_id` args, which no longer exist).

### D-140 filing

The MCP-vs-HTTP identity divergence this phase completes is filed as **D-140** in
`docs/decisions.md`. HTTP's `scopeFromRequest` (`internal/api/auth.go:72-80`)
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
internal/mcpserver/metaintake.go           # CHANGED — metaIdentity gains Project; readMetaIdentity reads _meta.project
internal/mcpserver/metaintake_test.go      # CHANGED — _meta.project extraction test cases
internal/mcpserver/handlers.go             # CHANGED — the 13 sites stop reading ProjectID/UserID; identity resolves via resolveScope/ResolveReadScope from _meta/JWT
internal/mcpserver/contracts.go            # CHANGED (breaking) — delete ProjectID/UserID from RetrieveInput, PlaybookInput, EpisodesInput, CausalInput, VerifyInput, ReviewInput, TraceInput, DrilldownInput, FeedbackInput, BranchInput, GetInput, RollbackInput, ResolveInput (13 structs; IngestRecord/IngestTargetScope/GrantsInput/ProactiveConfigInput are explicitly untouched — see departures)
test/integration/mcp_effective_scope_test.go   # NEW (§17 — closes a seam opened by ae7/ae8) — real-driver: for each of the 13 tools, a call carrying _meta/a verified JWT claim resolves identity correctly with the args gone; a call with neither behaves exactly as ae8's read_posture already defines for an absent identity, under -race
test/integration/http_mcp_scope_parity_test.go # NEW (§17) — the same identity, asserted via MCP _meta and via HTTP query/body params, resolves to the same effective scope and the same store rows, for each of the 13 affected tools' HTTP mirror
scripts/smoke/phase-ae2b.sh                # NEW
docs/plans/README.md                       # CHANGED — ae-track table / Plans line (ae2b row, draft)
docs/decisions.md                          # CHANGED — D-140 (filed)
```

## Config keys added

None. This phase adds no config knob — there is no interim bake-in period to
make configurable, and the removal itself is not gated by any runtime
setting (only by ae7+ae8 having landed, a code/deploy-order precondition,
not a config toggle).

## Acceptance criteria (binding)

1. **Hard gate honored.** This phase's code changes ship only once ae7 (the
   JWT verifier) and ae8 (`identity.ResolveReadScope`) have landed; the plan
   and its PR description say so explicitly (process-asserted, not
   mechanically gated — there is no code to land before then).
2. **The 13 contracts drop the args; identity still resolves.**
   `ProjectID`/`UserID` no longer appear on `RetrieveInput`, `PlaybookInput`,
   `EpisodesInput`, `CausalInput`, `VerifyInput`, `ReviewInput`, `TraceInput`,
   `DrilldownInput`, `FeedbackInput`, `BranchInput`, `GetInput`,
   `RollbackInput`, `ResolveInput` (grep-asserted). An effective-scope
   integration test (`test/integration/mcp_effective_scope_test.go`) proves
   that for every one of those 13 tools, a call carrying `_meta`/a verified
   JWT claim resolves to the correct scope, with real drivers, under `-race`.
   A call with neither `_meta` nor a JWT claim behaves exactly as ae8's
   `retrieval.read_posture` already defines for an absent identity
   (`compatible` ⇒ tenant-wide; `strict` ⇒ `ErrIdentityRequired`) — this phase
   introduces **no third fallback behaviour** of its own.
3. **The out-of-scope lookalikes are provably untouched.** `IngestRecord`,
   `IngestTargetScope`, `GrantsInput`, and `ProactiveConfigInput` still carry
   their `project_id`/`user_id` (or `project`/`user`) fields unchanged after
   this phase (grep/diff-asserted) — this phase's removal set is exactly the
   13 structs named above, never the full 16-or-more lookalikes.
4. **`project_id`'s M1 resolution lands in code.** `_meta.project` is read by
   `readMetaIdentity` (extends ae2's `metaIdentity`) and reaches
   `IdentitySources.MetaProject` for every one of the 13 tools; after
   removal, `_meta.project` is the *only* MCP channel for project narrowing
   (the arg is gone). A test exercises a `_meta.project`-only call narrowing
   correctly.
5. **D-140 filed and honored.** The MCP-vs-HTTP divergence is documented (this
   plan + the returned decision text); HTTP's `scopeFromRequest` and every
   POST handler's `project_id`/`user_id` body field are byte-unchanged by
   this phase (grep/diff-asserted against `internal/api`); a behavioural
   parity test (`http_mcp_scope_parity_test.go`) proves the same identity
   resolves to the same scope and the same rows via MCP `_meta` and via
   HTTP's query/body projection, for each of the 13 tools' HTTP mirror.
6. **No unscoped read introduced (P3).** ae2b adds no `internal/store` query
   method and no new store predicate (grep-asserted, mirroring ae8's AC-4);
   the three existing scope predicates
   (`buildScopeWhere`/`buildExactScopeWhere`/`vectorStore.Scan`) remain the
   only read filter and still fail closed on an empty tenant.
7. **No config knob added.** `internal/config` gains no new key for this
   phase (grep-asserted: no `deprecated`/`legacy` key appears in
   `internal/config/config.go` after this phase).
8. **Parity + smoke same-PR.** This phase ships `scripts/smoke/phase-ae2b.sh`
   in the same PR as the contract change; prior phases' smoke (ae1/ae2/ae7/
   ae8 in particular) still passes.

## Smoke script

`scripts/smoke/phase-ae2b.sh` — SKIPs gracefully until the surface is built
(today: unconditionally, since ae1/ae2/ae7/ae8 are all unbuilt); then, once
this phase lands:
- assert none of the 13 structs (grep by name in `contracts.go`) declares a
  `project_id`/`user_id` json tag.
- assert `IngestRecord`/`IngestTargetScope`/`GrantsInput` still declare
  `project_id`/`user_id` (the exclusion list untouched).
- assert `internal/mcpserver/metaintake.go`'s `metaIdentity` carries a
  `Project` field (M1).
- assert HTTP's `scopeFromRequest` still projects `?project_id=`/`?user_id=`
  (D-140, unchanged).
- assert no new `internal/store` files/methods (P3 — diffed against the
  base of this phase's branch).
- assert no deprecation/legacy config key exists in `internal/config`.
- `go test ./test/integration/ -run 'MCPEffectiveScope|HTTPMCPScopeParity'`
  passes.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Unit (`metaintake_test.go`):** `_meta.project` extraction (present/absent/
  non-string, mirroring the existing `user`/`session`/`agent_id` cases).
- **Integration (`mcp_effective_scope_test.go`, real drivers — sqlite +
  postgres, §17, `-race`):** seed memories under distinct `{tenant,user}`
  pairs; for each of the 13 tools, a `_meta`/JWT-narrowed call resolves to
  the correct rows; a no-identity call matches ae8's configured posture
  exactly (the ≥1 failure mode: `strict` posture refuses).
- **Integration (`http_mcp_scope_parity_test.go`, real drivers, §17):**
  the same seeded identity, asserted via MCP `_meta` and via HTTP
  `?project_id=`/`?user_id=`, resolves to the same scope and the same rows,
  for each of the 13 tools' HTTP mirror endpoint.
- **Regression:** ae1/ae2/ae3/ae4a/ae5/ae6/ae7/ae8/ae9's existing tests and
  smoke scripts pass unchanged after this phase (checkpoint-audit discipline,
  §17).
- **No new fuzz target.** ae2b removes a wire field (a schema *contraction*);
  it adds no new parse/decode surface. `encoding/json`'s existing
  unknown-field-tolerant `Unmarshal` is the surface whose behaviour on a
  hypothetical legacy caller changes — noted under Design as an honest
  residual note, not fuzzed (there is no invariant to assert beyond "no
  panic," which the existing decode path already proves, and there is no
  such caller pre-launch to construct a fuzz seed from).

## Risks & mitigations

- **Cross-surface confusion (MCP moves, HTTP doesn't).** Mitigated by the
  explicit, decision-backed (D-140) divergence, the untouched-HTTP grep
  assertion (AC-5), and the behavioural (not contractual) parity test.
  Documented in the tool docs so an operator migrating MCP callers does not
  also try to migrate HTTP callers that were never asked to change.
- **Over-scoping the removal to the full 16 (or 14, including
  `ProactiveConfigInput`) lookalikes.** Mitigated by the departures section's
  precise 13-struct audit and AC-3's grep pinning the 3 (4) exclusions —
  a future implementor tempted to "clean up the rest while they're in there"
  would be quietly expanding a read-side-identity phase into an ingest/
  grants/admin-contract change with a different risk profile.
- **Blocking on ae1/ae2/ae7/ae8, all unbuilt at authoring time.** Mitigated
  the way ae8 handled the same situation (D-148): this plan is written
  against ae2's and ae8's *specified* seams (`readMetaIdentity`,
  `IdentitySources`, `ResolveReadScope`) rather than against code that
  exists yet; the hard dependency (ae7+ae8 must land first) is structural,
  not just a nice-to-have ordering — attempting removal before ae8 lands has
  no `IdentitySources.ArgProject`/`ArgUser` slot to fall back from, and
  attempting it before ae7 lands has no verified `user`/`session` claim to
  fall back to, which is exactly the charter's C3/C4 finding restated. This
  gate is unaffected by the pre-launch simplification above — it protects
  correctness (no silent tenant-wide widening), not compatibility.

## Glossary additions

None. The vocabulary the charter anticipated for a phased-removal mechanism
(the interim tolerance period, its notice event, and its operator mode
toggle) is dropped along with the mechanism it named — there is no ceremony
left to name a term for.

## As-built deviations

- **The `_meta.project` wire key: reconciled to `"project"`, not
  `"project_id"`.** ae8 (landed ahead of this phase, Wave 3) already added
  `identity.IdentitySources.MetaProject` and wired `scope.go`'s
  `resolveScope` to populate it — but from `metaString(m, "project_id")`, an
  ae8-authoring-time guess at the eventual M1 key. This plan's own Design
  section is explicit that `_meta.project`'s key is `"project"` (the M1
  resolution: *"`project_id`'s M1 resolution lands in code... `_meta.project`
  is read by `readMetaIdentity`"*), matching the charter's identity-model
  table cited in "Findings I'm departing from". This is not a new decision —
  the plan already settled it — so this phase corrects `scope.go` to
  `metaString(m, "project")`, matching `metaintake.go`'s `readMetaIdentity`
  (also newly reading `_meta.project` in this phase). Both canonical call
  sites now agree on `"project"`; no third site reads it (grep-verified,
  `scripts/smoke/phase-ae2.sh` AC-8, updated below).
- **The removal set is 14, not the plan's literal 13: `BrowseInput` (ae5/D-143,
  Wave 0) is included (orchestrator decision).** `BrowseInput` matches the removed
  pattern EXACTLY — `ProjectID`/`UserID` fields, the identical "scope the read to
  a sub-tenant identity (P3, D-125); empty = tenant-wide" comment, and the same
  `resolveScope(svc, ctx, scopeArgs{Project: in.ProjectID, User: in.UserID})`
  shape at `makeBrowseHandler` — but the plan's enumerated 13 predates it (ae5
  landed in Wave 0, PR #92, AFTER this plan was authored in PR #89). This is the
  same "plans authored ahead of their dependencies" situation D-148 handled — the
  plan's **Goal** ("retire the MCP targeting args … the sole D-125 sub-tenant
  targeting mechanism") describes BrowseInput exactly; it was simply absent from
  the enumeration. Shipping the phase with `memory_browse` still taking model-
  filled `project_id`/`user_id` args while every other read tool drops them would
  leave a surface inconsistency that directly undercuts the phase's own claim
  ("MCP identity now lives in `_meta`/JWT only") and re-introduces, for one tool,
  the exact D-125 model-discretionary-targeting problem the whole track closes.
  So `BrowseInput.ProjectID`/`.UserID` are removed here too (its handler uses
  `scopeArgs{}`, its schema golden regenerated, its unit test converted to inject
  identity via `_meta`), and the removal set / smoke / AC-2 are updated to 14. A
  reasonable plan deviation (CLAUDE.md §4.3) — the plan is updated in the same PR,
  not silently diverged from. The other exclusions (`IngestRecord`,
  `IngestTargetScope`, `GrantsInput`, `ProactiveConfigInput`) are unchanged: they
  are write/contribute/admin targeting, not the D-125 read pattern.
- **`scripts/smoke/phase-ae2.sh` AC-8 updated (not just `phase-ae2b.sh`
  authored).** ae2's own smoke script asserted `_meta.project` is never read
  — true as of ae2, and by ae2's own Design text explicitly deferred to this
  phase ("ae2 does not read `_meta.project`... its `_meta` home or removal is
  ae2b"). Landing this phase makes that specific ae2 assertion stale by
  design, not by regression; AC-8 was updated in this PR to assert
  `_meta.project`, if read at all, is confined to the two canonical files
  (`metaintake.go`, `scope.go`) — preserving its real purpose (catch a
  second, ad hoc `_meta.project` intake path / surface sprawl) instead of
  asserting a now-superseded "never read" invariant. Documented in
  `docs/plans/phase-ae2-meta-intake.md`'s own As-built deviations too (cross-
  referenced there), per the "update whichever artifact is wrong" rule
  (CLAUDE.md, header).
- No other deviations from the Design; the 8 acceptance criteria are met as
  specified (see the smoke script and the verification tails in the PR),
  scoped to the literal 13-struct removal set named in the plan's Goal/Design/
  departures text.

## Decisions filed

- **D-140** — MCP-vs-HTTP identity divergence is a sanctioned contract
  divergence (filed in `docs/decisions.md`). Removal of the args is hard-gated
  on ae7 (verified claim) + ae8 (effective-scope resolver); removal itself is
  direct, with no interim bake-in period, because Stowage is pre-launch with
  zero external callers.
