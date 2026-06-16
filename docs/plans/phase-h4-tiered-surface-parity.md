# Phase h4 — Tiered control-verb surface parity (topics/flush/branches/assert; grants/contribute)

- **Status:** shipped
- **Owning subsystem(s):** `sdk/stowage`, `internal/mcpserver`, `internal/pipeline` (Stage flush/branch already exist), `internal/grants`, `internal/api` (contribute core extraction)
- **RFC sections:** §5.3 (grants/team sharing), §5.4 (topics), §5.5 (branches), §4.1 (buffers/flush), §9.1/§9.2/§9.3 (surfaces)
- **Depends on phases:** h3 (shares the SDK `Client`/`http`/`embedded` trio + MCP server.go — MUST land after h3), 16 grants (D-016/D-059/D-060), 06 buffers, 05 branches (D-029), 07 topics (D-043)
- **Informing briefs:** 03 (buffers/branches/topics pipeline shape), 02 (lifecycle/supersede), 01 (surface-sprawl cautionary tale — keep the MCP set small)
- **Program:** D-067 Wave B (mechanical re-homing — applies the **tiered** model). Pre-reserved decision: **D-071**.

## Goal

When this phase is done, the remaining control/management verbs obey the tiered
parity model (D-067): **single-user** control verbs are reachable on {SDK, MCP,
HTTP}; **multi-user/admin** verbs on {HTTP, MCP} only (never the single-user
embedded SDK). No verb's only consumer is one surface where the tier says
otherwise; the contribute-mode fail-loud shim from h2 becomes a real honored
capability on the server surfaces.

## Brief findings incorporated

- **Brief 03:** flush/branch/topics are pipeline-surface concerns whose control
  belongs on every consumer that drives the pipeline (the SDK and MCP both do).
- **Brief 01 (surface-sprawl cautionary tale):** keep the MCP tool set tight —
  fold related actions into action-tagged tools rather than one tool per verb.

## Findings I'm departing from

- **`memory_assert` reachability (RESOLVED by the owner, 2026-06-15).** Today
  assert (direct add/update/delete, pipeline-bypassing) is MCP-only. It is a
  single-user verb. **Decision: add it to the SDK `Client`, NOT to HTTP** — embedded
  hosts get a direct-write escape hatch, while the HTTP surface intentionally keeps
  writes routed through the ingest pipeline. So `memory_assert` parity target =
  {SDK, MCP}; HTTP deliberately excluded (recorded in D-071). Cleared to build.

## Design

### Tier A — single-user control verbs → add to {SDK, MCP}; HTTP already has them
1. **Topic upsert/delete + `pack:off`** (D-043): MCP already exposes these via
   `memory_topics` (list|upsert|delete); HTTP has PUT/DELETE `/v1/topics`. **Gap:
   the SDK is list-only.** Add `UpsertTopics` / `DeleteTopic` to the SDK `Client`
   (embedded → `stack.TopicSvc`; http → existing routes).
2. **Buffer flush (explicit/session_end)**: `pipeline.Stage.FlushKey` exists and
   is held by serve. Add `Flush(ctx, key, trigger)` to the SDK `Client` (embedded
   retains `p.Stage` from h1's `StartPipeline`; http → POST `/v1/buffers/{key}/flush`)
   and a `memory_flush` MCP tool.
3. **Branch fork/merge/discard** (D-029, incl. discard→SkipPromotion): add SDK
   `Client` branch methods + a `memory_branch` MCP tool (action: fork|merge|discard),
   both calling the same core the HTTP `branches_handler` uses (lift any
   handler-resident branch logic into a shared helper first if needed).
4. **`memory_assert`**: add to the SDK per the resolved open question.

### Tier B — multi-user/admin verbs → add to {MCP} to match HTTP; NOT the SDK
5. **Grants/groups/membership management** (D-016, §5.3): expose
   `CreateGroup/ListGroups/AddMember/RemoveMember/ListMembers/CreateGrant/ListGrants/RevokeGrant`
   (`internal/grants.Service`, already constructed on every `boot.Stack`) as MCP
   tools — recommend a single action-tagged `memory_grants` tool (sprawl
   discipline) — to match the HTTP admin routes. **Not added to the SDK** (embedded
   is single-user; their absence there is correct-by-design, D-067).
6. **Contribute-mode honoring**: replace h2's MCP fail-loud with real honoring —
   `memory_ingest` on MCP applies the same `GrantsSvc.CheckContributeGrant` +
   pool-owner scope override the HTTP handler does. Extract that resolution into a
   shared core helper (consumed by HTTP + MCP) so the two server surfaces cannot
   drift. **Not added to the SDK.**

### Staging (file-collision, per playbook §3 / D-067)
h4 shares the SDK `Client`/`http`/`embedded` trio and `internal/mcpserver/server.go`
with h3, so **h4 lands after h3** (sequential). Within h4, Tier A (touches the SDK
trio + MCP) and Tier B (touches MCP + grants core only, SDK untouched) **may** be
split into two sequential sub-PRs if the single PR is too large — Tier B does not
touch the SDK, but both touch `mcpserver/server.go`, so they serialize on that
file. Decide at implementation; keep D-071 as the single decision for both.

## Files added or changed

```text
sdk/stowage/client.go / embedded.go / http.go   # Tier A verbs (topics-write, flush, branch, assert)
sdk/stowage/suite_test.go                         # cross-impl coverage of new verbs
internal/mcpserver/server.go / handlers.go / contracts.go  # memory_flush, memory_branch, memory_grants; contribute honoring; schema goldens
internal/grants/...                               # (if needed) expose service methods cleanly
internal/api/records_handler.go                   # extract contribute resolution into a shared core helper
internal/pipeline / internal/api/branches_handler.go  # (if needed) lift branch logic into a shared helper
scripts/smoke/phase-h4.sh                         # NEW
test/integration/surface_parity_test.go           # NEW
docs/plans/README.md
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| (none) | — | Re-homing existing capabilities; no new knobs. |

## Acceptance criteria (binding)

1. **Tier A reachable on {SDK, MCP, HTTP}, identical behavior:** topic
   upsert/delete, buffer flush, branch fork/merge/discard (+ `memory_assert` per
   the resolved open question) — proven by the SDK suite_test (embedded + http)
   and MCP schema goldens.
2. **Tier B reachable on {HTTP, MCP}, ABSENT from the SDK:** grants/group
   management + contribute-mode honoring on MCP match HTTP; a test asserts the SDK
   `Client` does NOT expose them (single-user boundary is enforced, not just
   documented).
3. **Contribute-mode honored on MCP:** `memory_ingest` with `target_scope` +
   a valid contribute grant writes into the pool-owner scope (h2's fail-loud is
   replaced); without a grant it is rejected (not silently mis-scoped). The
   grant-check + scope-override is a shared core helper used by HTTP and MCP.
4. **Both-paths/surfaces-identical bar:** `test/integration/surface_parity_test.go`
   exercises each Tier-A verb through embedded + server and asserts identical
   observable effect; `-race`.
5. Every new MCP tool / SDK method has a smoke check + schema golden in this PR
   (§4.2/§13); the MCP tool count change is reflected in the D-015/D-018 surface
   note.

## Smoke script

`scripts/smoke/phase-h4.sh` — build; SDK: upsert a topic + flush a buffer + fork/
discard a branch and assert effects; `stowage mcp`: `memory_flush`, `memory_branch`,
`memory_grants` (create group+grant), and a contribute `memory_ingest` into a
granted pool; assert the SDK has NO grants/contribute method (compile-time/
reflection check). SKIP-graceful pre-build.

## Test plan

- **Integration (§17):** Tier-A surface parity (embedded+server), contribute-mode
  honoring on MCP with a real grant, `-race`.
- **Unit:** SDK suite_test for Tier-A verbs on both impls; the tier-boundary test
  (SDK lacks Tier-B); MCP schema goldens; contribute grant-check table.

## Risks & mitigations

- *Risk:* MCP surface sprawl (D-015). *Mitigation:* action-tagged tools
  (`memory_branch`, `memory_grants`) instead of one-per-verb; note the count delta.
- *Risk:* contribute honoring re-introduces the silent-mis-scope class if the
  shared helper is bypassed. *Mitigation:* single shared helper for HTTP+MCP +
  the grant-check table test + the no-grant rejection AC.
- *Risk:* large PR. *Mitigation:* optional Tier-A / Tier-B sequential split.

## Glossary additions

- **Tiered surface parity** — single-user verbs on {SDK, MCP, HTTP}; multi-user/
  admin verbs on {HTTP, MCP} only (D-067).

## Decisions filed

- **D-071** — tiered control-verb surface parity: single-user verbs (topic write,
  flush, branches, assert) reachable on {SDK, MCP, HTTP}; multi-user verbs (grants
  management, contribute honoring) on {HTTP, MCP} only via a shared core helper;
  the SDK does not expose multi-user verbs by design (D-067 tiered model).
