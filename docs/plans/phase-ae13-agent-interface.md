# Phase ae13 — Agent interface and source-backed explicit memory

- **Status:** implemented; release gates are the attached PR checks. Live-model behavioral comparison remains unmeasured.
- **Owning subsystems:** MCP, retrieval rendering, reconciliation, store transactions, HTTP, SDK, Harbor adapter.
- **RFC sections:** 5, 6c, 9.2, 9.5, 10 and the ae13 amendment.
- **Depends on:** ae2b, ae4a, ae7, ae8, ae12.
- **Review:** PR #112; owner-approved first two memory-usability phases.

## Goal

An ordinary planner sees task-oriented recall, inspection, remember, correction and playbook operations rather than runtime/admin machinery. Explicit writes preserve source evidence and return durable, idempotent receipts. Pengui remains the identity/policy issuer; there is no replacement authentication system and no direct-assert alias.

## Findings incorporated

The previous static catalog mixed 24 operations, descriptions exposed implementation details, schemas omitted parameter guidance, and lean retrieval omitted response-level warnings. The assertion escape hatch bypassed normal provenance and reconciliation. Runtime capture already existed and is preserved separately.

## Baseline and explicit limitation

Before production changes, capture actual MCP discovery and race-enabled MCP/retrieval/reconciliation service behavior from immutable `ab7d3a2c0eb3a4c7230d508e5a45dd6996005b38`. First successful capture: Actions run `33973153758`. The retained read-only baseline workflow always uses that source commit, independently of feature changes.

**A live spontaneous-agent-selection baseline was not run because no model credentials were available.** Contract tests are not a substitute for that measurement. Balanced positive/negative cases and an operator-run comparison protocol live in `eval/agent-use`; no numeric adoption or task-quality gain is claimed. This is the remaining measurement limitation, not a fabricated pass criterion.

## Design and implemented seams

`mcpserver.NewAgent` exposes five operations at `/mcp/agent` on a shared HTTP port, `/agent` on a dedicated port, and default stdio. `New` retains the full static integration catalog. `--catalog full` retains full stdio compatibility. Both HTTP catalogs use the same existing auth wrapper. Runtime/admin names are unregistered on the agent server, not merely hidden. Catalog visibility is not authorization.

The Harbor adapter's `Tools` defaults to the same five concepts with the `stowage_` prefix; `LegacyTools` retains its old seven operations explicitly. A host can bind an already-persisted actual user record with `WithMemorySource`; neither this helper nor MCP `_meta.stowage` supplies identity authority.

Typed schema inference remains Dockyard-owned. The Stowage declaration wrapper adds task descriptions and semantic parameter constraints through the public registration API. Actual discovery goldens cover the ordinary catalog. Read projections preserve useful contents, provenance, dates, stale-value replacements, conflicts, degradation and fail-open curation warnings. Benchmark rendering retains its pinned text separately from the agent-facing evidence framing.

`reconcile.Remember` and `Correct` are one shared source-backed command core used by MCP, HTTP and embedded SDK. They validate exact UTF-8 user quotations against owned durable records, reject generated/foreign/branch evidence, and create byte-span provenance. Explicit intent bypasses extraction magnets but never scope/provenance controls. New explicit memories default to the personal zone. Corrections inherit the old type/privacy, require the inspected semantic revision and newer source evidence, and preserve reversible supersession history. Exact active same-session content with provenance may be reused; semantic paraphrase deduplication is not claimed.

An optional `CommitSet.Command` reserves a receipt and verifies source/target snapshots in the same transaction as memory, provenance and history effects. The existing events table provides durable idempotency; no new table or migration. SQLite serializes the write transaction; Postgres uses source/target row locks. Different arguments with the same scoped key fail. Current memory status is observed independently on replay, which cannot revive a deleted or superseded value. Retrieval eligibility does not guarantee rank, view inclusion, completed embeddings or bypassing cooldown.

## Files added or changed

Core command and tests: `internal/reconcile/remember*.go`.
Store guard/revision/receipt support: `internal/store/commands.go`, both SQL-driver `commands.go` files, CommitSet/EventStore seams and transaction callers.
Agent catalog, descriptions, schemas, bindings and tests: `internal/mcpserver/{agent,catalog,source_binding_test}.go`, registration/handler contracts and `testdata/agent/`.
Reader projection: `internal/retrieval/{reader_response,render}.go` and tests; HTTP/MCP/SDK callers.
Public commands/revisions: `internal/api/explicit_handler*.go`, routes and memory inspection; `sdk/stowage/explicit.go`, Client and inspection contracts.
Host integration: `cmd/stowage/mcp_agent*.go`, CLI mounting; `adapters/harbor/agent*.go` and explicit legacy wiring.
Baseline/scenarios: `.github/workflows/agent-memory-baseline.yml`, `eval/agent-use/`.
Migration/semantics: `docs/agent-memory.md`, README, changelog, RFC amendment, decision log, glossary.

## Configuration and compatibility

No new environment/config key. There is one CLI selector (`--catalog agent|full`) and fixed HTTP paths. Existing deployed planner connections must switch to the agent endpoint and refresh discovery; keep run-completion capture on the full runtime-only connection. Custom Go Client implementations must implement Remember/Correct. Hosts must persist current user evidence before a write or use an existing source reference. This phase does not silently reconfigure Pengui deployments or add automatic per-turn retrieval.

## Acceptance and executable tests

1. Pre-change catalog/service baseline is independently captured; empirical model-selection limitation remains explicit.
2. Five-tool discovery and rejection of runtime/admin calls are tested on the real MCP wire and actual HTTP paths.
3. Task guidance, schema descriptions, enums, bounds and inspection exclusivity reach advertised schemas; invalid wire inputs fail.
4. Useful source content and response limitations remain visible to text-only readers and the adapter's final context result.
5. Remember/correct validate source evidence and do not call Assert; missing sources cannot produce a save claim.
6. Same-key concurrency, different-body conflicts, deleted/superseded replay, SQLite restart and competing corrections are covered.
7. Postgres correction tests use an isolated schema to avoid other test packages' public-table truncation. Both drivers use the same command contract.
8. HTTP/SDK/MCP round-trips expose usable inspection revisions, preserve history and reject foreign/fabricated evidence.
9. Existing full-catalog/run-completion and adapter legacy tests remain. Repository race/build/vet/coverage/eval/lint/drift checks are retained, not weakened.
10. Forgetting is defined accurately without exposing an incomplete erasure promise.

## Smoke script

`bash scripts/smoke/phase-ae13.sh`. Set `STOWAGE_TEST_PG_DSN` to include Postgres tests; without it those are explicitly skipped. The script tests command durability, scoped evidence, wire schemas, bindings, warnings, HTTP/MCP/SDK integration and the adapter. No provider calls or live-agent selection claims.

## Forgetting boundary

No ordinary `memory_forget` is shipped. Legacy derived-memory deletion does not erase raw records/backups or prevent re-extraction. Correction preserves history. Existing authorized whole-user DSAR remains separate. A selective forgetting tool requires a complete suppression/erasure contract covering source scope, dependent memories, caches, re-extraction, audit retention and deployment backups.

## Risks and mitigations

Source references are untrusted until resolved. Retrieval is evidence, not an instruction channel. A committed receipt is not a promise of ranking or vector readiness. Host credentials and existing grants retain authority independently of tool discovery. Runtime/admin compatibility exposure is unchanged and is not newly certified by this usability phase.

## Glossary and decisions

Agent catalog, source-backed explicit command and processing receipt are added to the glossary. The ae13 RFC amendment and decision entry record surface separation, exact-source semantics, transactional receipts, compatibility changes and the deliberate forgetting boundary.
