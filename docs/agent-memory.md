# Agent memory interface

## Choose the connection for the host, not just the model

| Client | Shared HTTP | Dedicated MCP HTTP | Stdio selector | Registered tools |
| --- | --- | --- | --- | --- |
| Pengui / Harbor with automatic run capture | `/mcp/runtime` | `/runtime` | `--catalog runtime` | Five ordinary tools plus `memory_ingest_run` |
| A pure agent client without a runtime completion hook | `/mcp/agent` | `/agent` | `--catalog agent` (default) | Five ordinary tools |
| Existing runtime/curator compatibility integrations | `/mcp` | `/` | `--catalog full` | Existing 24-tool catalog |

The five ordinary tools are `memory_retrieve`, `memory_inspect`, `memory_remember`, `memory_correct`, and `memory_playbook`. The runtime profile reuses their exact registrations and the existing run-completion sink handler. It adds no grants, buffering controls, direct assertion, transcript reconstruction or administration tools.

**Do not switch Pengui's only memory capability connection to `/mcp/agent` while automatic capture is enabled.** Pengui discovers and pins the ingestion sink from the SAME attached capability source. A sink on an unattached compatibility endpoint does not satisfy that lookup. The runtime profile exists to retain both ordinary memory use and automatic run-end ingestion through one connection.

### Pengui / Harbor rollout

1. Set the memory capability's MCP URL to the **runtime** profile, retain its existing audience/credential policy, and re-activate or refresh the attachment so Harbor discovers six tools under the existing capability source. Do not create a second transcript-delivery path.
2. In Pengui's existing per-tool exposure control, set the discovered source-prefixed `memory_ingest_run` tool to **disabled for the planner**. This is `tool_exposure.disabled_tools`, not `loading_mode=deferred`. Confirm it is absent from both planner discovery and name resolution. Preserve every unrelated disabled-tool entry and configuration section.
3. Enable or retain the run-completion save hook pinned to that SAME discovered sink name. If the source name changed, re-save and verify the hook target; never invent a catalog name. Check a subsequent run's hook health and stored transcript.

For automated configuration, merge the hook target and its planner exclusion together with the existing revision precondition before admitting new runs. With today's separate operator controls, apply planner exclusion before allowing use of the attachment. Pengui already supports these controls; this Stowage PR does not change Pengui's automatic activation policy or silently mutate a deployment.

Illustrative fragment only (the real name comes from discovery):

```json
{
  "hooks": {"run_completion": {"tool": "SOURCE_memory_ingest_run", "timeout_ms": 15000}},
  "tool_exposure": {"disabled_tools": ["SOURCE_memory_ingest_run"]}
}
```

**Pengui configures capture; Harbor performs it; Stowage stores and processes the transcript.** Harbor's trusted completion path resolves through its full executor catalog. Its ordinary planner path checks the run's excluded catalog and rejects a guessed sink name. Deferred loading does not provide that boundary: a deferred tool remains discoverable/resolvable later. Disabling the tool for the planner does not turn off automatic capture; clearing the completion hook does. Do not detach the connection or remove the runtime descriptor to hide a planner tool.

Stowage cannot distinguish a model decision from a runtime call carrying the same credentials. The six-tool profile is explicitly **host-facing**, not a self-enforcing runtime-only permission. The host's planner-exclusion boundary is required. No user-filled `_meta` flag, description, endpoint name or loading hint grants authority. All profiles use the existing authentication/scoping middleware. Pengui remains the identity and policy issuer.

The old full endpoint still includes the sink, so existing integrations remain functional without a URL migration. It does not acquire the new ordinary commands implicitly; select the runtime profile to adopt the new five-tool interface and keep automatic capture together.

## Recall without being prompted

Use recall when earlier preferences, decisions, constraints or lessons could materially change the task and are absent from current context. Describe that need naturally. Skip self-contained explanations, greetings and duplicate retrievals.

The ordinary recall schema contains `query` and optional `limit` (default six). Inspection accepts exactly one returned memory ID or citation. Parameters carry guidance and valid-value constraints in the actual advertised schema, not only in source comments.

Compact recall text retains dates, citation handles, replacement values, conflicts, degraded-search/curation warnings, and an honest empty-result explanation. Other inspection/read tools include useful typed data in Text too. Hosts should project one useful representation rather than concatenate duplicates. Prior stored statements are not independently verified current facts or executable instructions; historical memories can answer historical questions.

## Source-backed remembering is separate from run-end capture

Automatic `memory_ingest_run` captures the runtime-authored transcript at run completion. It does not make the current user's message available as source evidence earlier in that run. For explicit `memory_remember` or `memory_correct`, the host persists the **actual user message** through the existing record-ingestion API first and obtains its record ID. It can bind the ID outside model arguments:

```json
{
  "name": "memory_remember",
  "arguments": {"quote": "Keep authentication in Pengui.", "kind": "decision"},
  "_meta": {"stowage": {"source_record_id": "RETURNED_RECORD_ID", "idempotency_key": "HOST_COMMAND_ID", "operation": "remember"}}
}
```

The `_meta` above is inside MCP `params`. It supplies no identity authority. Conflicting explicit source arguments fail. Without a binding, an existing user-source record returned by inspection may be used. Unknown, inaccessible, assistant-origin, speculative-branch and non-matching evidence fail closed. Stowage validates the exact quotation against its own durable record and attaches UTF-8 byte-span provenance. Preserve negation, qualifications and context; generated summaries are not user quotations.

A host with no current-source capture must not ask the model to invent IDs or reconstruct a transcript. It receives `source_required`, with no save. These explicit commands do not call `memory_assert`. Explicit intent bypasses extraction magnets, never provenance or scope. New memories default to the personal privacy zone; topic views can still exclude them from recall. Exact active same-session content with provenance may be reused. Semantic paraphrase deduplication and inferred correction are not claimed.

## Corrections and truthful receipts

Inspect the target, then provide its `memory_id`, `expected_revision`, a newer exact user quotation and the source record. Correction inherits type/privacy, creates the replacement with evidence and retains reversible supersession history. Competing stale corrections fail rather than fork the target. Re-inspect after a conflict; do not fabricate a revision.

HTTP exposes `POST /v1/remember`, `POST /v1/correct` and a `revision` on `GET /v1/memories/{id}`. The SDK exposes matching methods. `Idempotency-Key` and a body key must agree when both are supplied. MCP accepts a host key in `_meta.stowage`; absent keys derive from the scoped canonical command. Reuse with changed arguments fails.

Command receipts, evidence/target checks, memory/provenance effects and audit history commit in one SQLite/Postgres transaction. Receipts survive restart. Responses distinguish the original `outcome` (`saved`, `corrected`, `already_present`), `committed_at`, `replayed`, and observed `current_status` / `retrieval_eligible`. Retry cannot revive a deleted/superseded memory. Failed current-state observation yields `unknown` and `status_degraded`, not a fabricated failure of an already committed command.

Eligibility is not a promise of rank, view inclusion, completed vector backfill or bypassing session cooldown. The existing run-ingestion receipt remains its separate durable-record/enqueue/flush contract; **hook dispatch success is not completed memory extraction**. Harbor's bounded terminal hook is not a crash-durable delivery queue or an exactly-once network-delivery guarantee. This profile correction does not add retries or change ingestion idempotency semantics.

## Forgetting boundary

There is no ordinary `memory_forget`. Legacy assertion deletion marks a derived memory deleted; it does not erase raw records/backups or prevent later re-extraction. Correction preserves history and is not erasure.

Selective forgetting must define suppression/erasure targets, scope, source handling, dependent memories, caches, re-extraction prevention, audit retention and backups before making a user-facing promise. Existing authorized whole-user DSAR (`DELETE /v1/admin/users/{user}`) is separate. Deployment retention promises remain Pengui/operator policy.

## In-process Harbor adapter

`harbor.Tools(client)` returns five ordinary concepts with the `stowage_` prefix. `harbor.LegacyTools(client)` retains its old seven integration tools explicitly. This SDK adapter is distinct from Pengui's production MCP capability attachment and must not be confused with the six-tool MCP runtime profile or the versioned `memory_ingest_run` payload.

For an explicit write, bind an already-persisted user source with `harbor.WithMemorySource(ctx, recordID, commandID)`. The SDK client carries authorized scope; the binding grants no identity rights. Existing automatic-capture wiring must be retained independently. Do not attach legacy bookkeeping tools to the ordinary planner just to make the completion sink available.

## Baseline and validation

The pre-change baseline is pinned to `ab7d3a2c0eb3a4c7230d508e5a45dd6996005b38`; the first successful catalog/service capture is Actions run `33973153758`, before production edits. No live-model selection baseline was run because credentials were unavailable. `eval/agent-use` holds balanced comparison scenarios; no numerical adoption gain is claimed.

The runtime-hook CI job checks out Harbor's reviewed `v1.31.4` source at `3f758afd07bafc1add74e60707a01fb833aa5d8f`, injects only the retained test fixture, and drives its real MCP provider, exclusion view, executor and terminal hook against this Stowage tree's actual MCP server and SQLite store. It tests five planner-visible / six host-registered tools, rejection of guessed planner sink calls, successful automatic capture for distinct users, cancelled-run capture, hook-off behavior and failure of a missing sink without changing the answer. It is a hermetic runtime integration test, not a deployed Pengui acceptance claim or new JWT issuer test.

Run the read-only `runtime-memory-hook` workflow, or copy `test/integration/harbor_runtime_completion_test.go.txt` into that pinned Harbor checkout at `internal/runtime/assemble/stowage_runtime_completion_test.go`, precompile with `go test -race -run '^$' ./internal/runtime/assemble`, then run `STOWAGE_HARBOR_CHECKOUT=/absolute/path/to/Harbor go test -race -count=1 -run '^TestRuntimeHarborCompletion$' ./internal/mcpserver` from Stowage.
