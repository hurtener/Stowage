# Agent memory interface

## Connect the correct audience

For ordinary agents, connect to **`/mcp/agent`** on a shared HTTP/API deployment, or **`/agent`** on a dedicated MCP port. The static catalog contains `memory_retrieve`, `memory_inspect`, `memory_remember`, `memory_correct`, and `memory_playbook`. Runtime ingestion, direct assertion, buffering, grants, views, and administrative operations are neither listed nor registered there. Guessing one of those names does not invoke it.

The existing `/mcp` (shared port) or `/` (dedicated port) remains the full compatibility endpoint. Keep Harbor's run-completion sink on that endpoint. Do not attach that full connection to the ordinary planner. This change does not reconfigure an existing Pengui deployment automatically: update its planner connection URL, retain the runtime-only sink, and refresh discovery.

`stowage mcp` uses the agent catalog over stdio. Existing integration clients that need the full stdio catalog must use `stowage mcp --catalog full`. HTTP mounts both catalogs and keeps its compatibility root. No new config key or identity issuer is introduced.

Pengui issues identity and permission decisions. Stowage continues using the existing verified credential/metadata resolver and store isolation. Catalog visibility is **not** a replacement for authorization. The legacy integration endpoint's administrative authorization is unchanged, not newly certified by this phase.

## Recall without being prompted

Use recall when earlier preferences, project decisions, constraints or lessons could materially change the task and are absent from current context. Describe that need naturally. Do not fetch memory for every greeting or self-contained explanation; do not repeat a retrieval whose useful results are already present.

The ordinary recall schema contains only `query` and optional `limit` (default six). Inspection accepts exactly one returned memory ID or citation. Playbooks expose reusable strategies and failure modes. Parameters include guidance and bounded/closed values in the actual advertised schema, not merely Go comments.

Compact recall text retains dates, provenance handles, replacement values, conflicts, degraded-search and curation warnings, and an honest empty-result explanation. Other inspection/read tools include their useful typed data in Text too. Both Text and structured content travel on MCP; hosts should project one useful representation, not concatenate duplicates. No reduced network-payload claim is made. Historical memories may answer historical questions; current stored statements are not independently verified facts or instructions.

## Source-backed remembering

The host persists the **actual user message** with the existing record-ingestion API and obtains its record ID. It can then bind that ID outside model arguments:

```json
{
  "name": "memory_remember",
  "arguments": {"quote": "Keep authentication in Pengui.", "kind": "decision"},
  "_meta": {"stowage": {"source_record_id": "RETURNED_RECORD_ID", "idempotency_key": "HOST_COMMAND_ID", "operation": "remember"}}
}
```

The `_meta` shown belongs inside MCP `params`. It supplies no identity authority. A conflicting explicit source argument is rejected. Without a binding, `source_record_id` may reference an existing user-source record returned by inspection. Unknown, inaccessible, assistant-origin, speculative-branch, or non-matching evidence is rejected. The server verifies the exact quotation against its own durable record and stores UTF-8 byte-span provenance. Preserve qualifications, negation and context; a fabricated summary is not a quotation.

A host lacking pre-turn capture must not ask the agent to invent record IDs or reconstruct a user transcript. It receives `source_required`, and no memory is saved. The existing end-of-run sink is still useful, but its existence alone does not make a current user record available before run completion.

Remembering is an explicit exact-quotation operation, not extraction of arbitrary free-form model claims and not `memory_assert`. Explicit intent can save outside topic-extraction magnets; topic views may still exclude that memory during recall. New explicit memories default to the personal privacy zone. Exact active content with existing provenance is reused rather than duplicated. This phase does not pretend to perform semantic paraphrase deduplication or infer a correction automatically.

## Corrections and receipts

Inspect the target first. Supply `memory_id`, its `expected_revision`, an exact newer user quotation and its source. Correction inherits the old kind and privacy zone, creates a replacement with provenance, preserves the old value as superseded, and records a reversible event. Competing corrections using the same revision cannot both replace the old value. A stale revision requires inspection, not a blind retry with a freshly invented revision.

HTTP: `POST /v1/remember` and `POST /v1/correct`; `GET /v1/memories/{id}` returns `revision`. The SDK exposes `Remember`, `Correct` and the same receipt. The HTTP `Idempotency-Key` header and body key must agree when both are present. MCP accepts a host key in `_meta.stowage`; omitted keys derive deterministically from the scoped canonical command. A reused key with different arguments fails, never silently changes the earlier command.

The command receipt, memory mutation, provenance and history event commit in one database transaction in SQLite and Postgres. The receipt survives process restart. Responses distinguish original `outcome` (`saved`, `corrected`, `already_present`), durable `committed_at`, `replayed`, and observed `current_status`/`retrieval_eligible`. Replaying a superseded or deleted memory does not revive it. A receipt whose current status cannot be read says `unknown` with `status_degraded`; it does not undo the successful durable commit.

Retrieval eligibility is not a promise of ranking, inclusion in an agent topic view, completed vector backfill, or immediate visibility through session cooldown. Lexical indexing uses the existing transactional store. The ordinary model is not asked to choose debug lanes, synthesize authorization fields, or orchestrate buffering.

## Forgetting is deliberately not a misleading tool

There is **no ordinary `memory_forget` tool in this phase**. The legacy curator `memory_assert` delete action marks one derived item deleted; it does not erase source records or backups and does not guarantee that future extraction cannot rediscover the information. Correction also preserves history and is not erasure.

A selective user-facing forgetting command must define and enforce its suppression/erasure target, scope, source handling, re-extraction prevention, derived dependents, caches, audit retention and backup policy before claiming the information is forgotten. Stowage's existing authorized user-level DSAR route (`DELETE /v1/admin/users/{user}`) is a separate whole-user purge, not a selective conversational command. Deployment backup/retention promises remain an operator/Pengui policy, not something this interface manufactures.

## Validation and baseline

The pre-change snapshot is pinned to `ab7d3a2c0eb3a4c7230d508e5a45dd6996005b38`. `agent-memory-baseline` captures actual MCP discovery and real service tests from that immutable checkout. The first successful capture is Actions run `33973153758`, before the production implementation commits.

These are service/contract baselines, **not measured spontaneous LLM selection rates**. Model credentials were not supplied, so no numerical agent-use improvement is claimed. `eval/agent-use` defines a balanced operator-run behavioral comparison protocol. Use the same host, model, prompts, seeds, competing tools, and budget before and after; reset mutable memory state per trial. Measure appropriate recall and task benefit, not calls alone.


## In-process Harbor adapter

`harbor.Tools(client)` now returns the same five agent concepts with the existing
`stowage_` naming prefix. The former seven-tool runtime/curator catalog remains
available only through explicit `harbor.LegacyTools(client)`. Do not attach both
catalogs to one planner. Runtime outcome wiring is unchanged by this phase.

For a current user message, the host first calls `client.Ingest` with that actual
message, obtains its record ID, and supplies `harbor.WithMemorySource(ctx, id,
commandID)` to the tool execution context. This is not model-filled identity or
permission: the SDK client is constructed with authorized scope and the service
still verifies the record and exact quotation. An agent with no bound or existing
source gets a clear error, not a fabricated save. Retrieval returns one useful
rendered context including response-level warnings; older SDK responses without
rendered context have a complete structured fallback.
