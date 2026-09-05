# Phase ae13 — Agent-oriented memory interface and trustworthy explicit writes

- **Status:** in-progress
- **Owning subsystem(s):** MCP, retrieval rendering, reconciliation, SDK, HTTP
- **RFC sections:** 5, 6c, 9.2, 9.5, 10
- **Depends on phases:** ae2b, ae4a, ae7, ae8, ae12
- **Informing briefs:** `docs/research/INDEX.md`; the owner-approved source-level agent-surface review (2026-09-05).

## Goal

An ordinary agent sees a small, task-oriented memory interface rather than runtime and administration machinery. Explicit remembering and correction preserve bound source evidence, return truthful receipts, and are idempotent; no direct-assert shortcut is presented as remembering. Pengui remains the issuer and authorization authority.

## Brief findings incorporated

The current catalog mixes 24 agent, runtime, and administrator operations. Source comments do not reach generated parameter schemas. Compact retrieval text omits response-level warnings. Direct assertions bypass provenance and reconciliation. Automatic run capture is already a separate contract and must keep working.

## Findings I'm departing from

Do not rename every existing operation or break the runtime sink. Do not claim an empirical agent-selection improvement from schema or handler tests. Do not expose an ambiguous `forget` promise when the existing operation only changes a derived memory's status.

## Design

Capture the unmodified `ab7d3a2c0eb3a4c7230d508e5a45dd6996005b38` MCP catalog and service behavior before changing production code. Preserve the pinned baseline as a reproducible CI artifact. Live-model selection evaluation remains separately identified and never substituted by scripted or lexical selection scores.

Use thin, tiered projections over shared services. Keep runtime/admin compatibility explicit, and make the ordinary planner catalog small. Source-backed writes must validate evidence within verified scope, bind provenance server-side, reject inconsistent idempotency replays, and distinguish durable acceptance from searchable completion. Corrections may not destroy the old value without a reversible event and verified replacement evidence. No new identity issuer, model-asserted identity, or locally inferred permissions.

## Files added or changed

`internal/mcpserver/`, `internal/reconcile/`, `internal/retrieval/`, `sdk/stowage/`, `internal/api/`, `eval/agent-use/`, related smoke and documentation files. Final inventory is updated with implementation.

## Config keys added

No new config key is authorized by this initial plan. Any needed surface-profile control must be documented with its default and backward-compatibility behavior before implementation.

## Acceptance criteria (binding)

1. Pinned pre-change catalog and handler baseline is captured independently of implementation.
2. Ordinary catalog omits runtime/admin operations; explicit compatibility surface preserves existing consumers and checks calls independently of discovery.
3. Task-oriented descriptions and parameter guidance/constraints reach actual MCP discovery.
4. Model-facing results retain useful contents, provenance, conflicts, freshness, and degradation warnings.
5. Remembering and correction require source evidence and never delegate to direct Assert.
6. Durable idempotency and truthful replay/status semantics are covered by concurrency and restart tests.
7. Shared core ships through SDK, HTTP, and MCP with parity tests and scoped failures.
8. Forgetting limitations and retention boundaries are documented without claiming full erasure.
9. Existing run-completion capture continues working.
10. Race, schema, smoke, lint, and drift gates are checked; unexecuted live-model evaluations are clearly marked.

## Smoke script

`scripts/smoke/phase-ae13.sh`.

## Test plan

Pinned baseline catalog + real handler tests; model-use positive and negative cases; schema/wire tests; source binding, cross-scope, correction, retry/concurrency/restart, parity, render-warning tests; existing SQLite/Postgres conformance.

## Risks & mitigations

Discovery is not authorization. Source references are not trusted until resolved. Durable acceptance is not retrieval readiness. A deleted derived memory is not erased raw history. CI cannot manufacture live-model behavioral measurements without a configured provider.

## Glossary additions

Agent catalog; source-backed explicit memory; processing receipt; idempotency replay.

## Decisions filed

An RFC/decision amendment will record final surface separation and explicit-write semantics in the implementation commit.
