# Phase ae12 — `memory_ingest_run`: the Harbor run-completion sink (D-153)

- **Status:** approved
- **Owning subsystem(s):** `internal/records` (the payload→records converter core), `internal/mcpserver` (the thin `memory_ingest_run` tool caller), `internal/pipeline` consumed (eager `FlushKey`)
- **RFC sections:** §9.2 (MCP server), §5 (identity/scopes — D-124 scope-authoritative writes), §9.1 (auth — the per-call bearer, D-152)
- **Depends on phases:** ae11/D-152 (the open handshake + per-call bearer this path rides), ae7 (JWT verifier), phase 16 (the MCP surface), phase 6 (buffers/flush — `Stage.FlushKey`)
- **Informing briefs:** 01, 02 (surface-sprawl cautionary tales → the conversion is ONE core function; the MCP tool is a thin caller; `memory_ingest`'s contract is untouched), 04 (CL-Bench fidelity → the transcript lands verbatim, both roles, unfiltered — extraction decides what matters, the sink never drops content)

## Goal

When this phase is done, a Harbor runtime's run-completion hook can dispatch its
pinned `RunCompletionPayload` (format_version 1) as a `tools/call` to a new
24th MCP tool, **`memory_ingest_run`**, and the full ordered transcript lands as
verbatim records in the acting user's scope — stamped per-user by the verified
per-call JWT (never by the payload), grouped into one extraction buffer per run,
and eagerly flushed so extraction begins promptly. Today the hook's dispatch to
`memory_ingest` fails schema validation (`IngestInput` declares none of the
payload's 13 keys; `additionalProperties:false` rejects it before the handler
runs — correctly: the two shapes share zero field names). `memory_ingest`'s
verbatim-record contract stays byte-identical for its existing callers.

This is also the template for the **run-completion-sink pattern** (D-153): any
capability that wants to be a Harbor auto-save target exposes a tool that
accepts `RunCompletionPayload` and adapts it internally to its own storage.

## Brief findings incorporated

- **01/02 (surface sprawl):** one converter function in the core
  (`records.FromRunCompletion`), one thin MCP handler calling the same
  stamp→append→enqueue path `memory_ingest` uses. No second ingest
  implementation, no fork of the pipeline enqueue.
- **04 (fidelity-first):** every `conversation[]` entry is stored verbatim in
  order — both roles, including tool-line/preamble kinds. The sink filters
  nothing; extraction and reconciliation decide relevance downstream (P1).

## Findings I'm departing from

- **The ask's "map the identity quad into scope server-side" is rejected**
  (D-153 settles this): the payload's `tenant_id`/`user_id` ride `arguments` —
  the omittable, host-constructed channel D-140 exists to distrust. Scope
  resolves from the verified credential + `_meta` exactly like `memory_ingest`;
  the payload quad is **cross-checked, fail-closed on mismatch** (D-138
  analog), and D-124's scope-authoritative store write is the structural
  backstop (a record can never escape the bearer's scope even unmapped).
- **The run outcome is stored across `outcome` + `outcome_detail`, not `outcome`
  alone** (a §4.3 implementation deviation from the mapping table, documented in
  the table row): the `outcome` column's day-one CHECK constraint rejects
  Harbor's vocabulary, so the precise outcome lands verbatim in the unconstrained
  `outcome_detail` and a `success`/`failure` projection lands in `outcome` (which
  the reflection sweep consumes). No signal is lost.
- **The ask's "confirmed" transcript-entry shape (`{role, content}` only) is
  wrong at source** — `TranscriptEntry` is
  `{role, kind, content, step, at?}` (five fields, `at` an optional RFC3339
  timestamp). Mirroring only two would reproduce the ST-2
  `additionalProperties` failure one level down, on the first entry carrying
  `kind`. The contract mirrors all five.

## Design

### The wire contract (`internal/mcpserver/contracts.go`)

Mirrors Harbor's pinned `RunCompletionPayload` (format_version 1) exactly —
field names, types, and optionality as Harbor marshals them (`time.Time` ⇒
RFC3339 strings on the wire):

```go
// IngestRunEntry mirrors Harbor's TranscriptEntry (format_version 1) — all
// five fields, so an entry carrying kind/step/at validates (ST-2's lesson).
type IngestRunEntry struct {
    Role    string `json:"role"`           // "user" | "assistant"
    Kind    string `json:"kind,omitempty"` // goal/steering/preamble/tool-line/final (accepted, not enum-validated)
    Content string `json:"content"`
    Step    int    `json:"step,omitempty"`
    At      string `json:"at,omitempty"`   // RFC3339; per-entry assertion time when present
}

// IngestRunInput mirrors Harbor's RunCompletionPayload, format_version 1.
type IngestRunInput struct {
    FormatVersion   int              `json:"format_version"`
    TenantID        string           `json:"tenant_id"`
    UserID          string           `json:"user_id"`
    SessionID       string           `json:"session_id"`
    RunID           string           `json:"run_id"`
    AgentID         string           `json:"agent_id,omitempty"`
    Outcome         string           `json:"outcome"`
    StartedAt       string           `json:"started_at"`   // RFC3339
    CompletedAt     string           `json:"completed_at"` // RFC3339
    DurationMS      int64            `json:"duration_ms"`
    StepCount       int              `json:"step_count"`
    ToolInvocations int              `json:"tool_invocations"`
    Conversation    []IngestRunEntry `json:"conversation"`
}

type IngestRunOutput struct {
    IDs      []string `json:"ids"`
    Enqueued bool     `json:"enqueued"`
    Flushed  bool     `json:"flushed"` // eager FlushKey succeeded (best-effort)
}
```

Validation (handler, fail-loud typed errors):
- `format_version != 1` → rejected with a message naming the supported version
  (a future Harbor v2 fails loudly, never misparses — that is what the pin is
  for).
- `conversation` empty → rejected (an empty transcript is a caller bug).
- `run_id` empty → rejected (it is the buffer key).
- `completed_at`/`started_at`/`at` parse as RFC3339 when non-empty; a parse
  failure rejects (never a silent zero timestamp).

### Identity (the D-153 rule)

1. Scope := `svc.ScopeFn(ctx)` + the D-138 `readMetaIdentity` tenant guard —
   byte-identical to `makeIngestHandler`'s opening.
2. Cross-check: `in.TenantID != "" && in.TenantID != scope.Tenant` → reject
   (fail closed). `in.UserID != "" && scope.User != "" && in.UserID !=
   scope.User` → reject (fail closed). In keyring mode (`scope.User == ""`,
   no verified user), the payload `user_id` fills the user dimension of the
   *records* (D-124 lets a per-record value fill a dimension the scope left
   empty) — documented: per-user isolation on this path is only
   credential-verified in jwt mode, which is the motivating deployment.
3. No contribute-mode on this tool. `agent_id` is metadata (`SourceAgent`),
   never an isolation key — same posture as Harbor's own comment.

### The converter core (`internal/records/runsink.go`)

```go
// FromRunCompletion converts a Harbor run-completion payload (format_version 1)
// into ingest inputs: one records.Input per conversation entry, in order.
func FromRunCompletion(p RunCompletion) ([]Input, error)
```

Per-entry mapping (the `records.Input` fields):

| Payload | records.Input | Note |
|---|---|---|
| `conversation[i].role` | `Role` | both roles, verbatim order |
| `conversation[i].content` | `Content` | verbatim (P1) |
| `conversation[i].at` (when set) else `completed_at` | `OccurredAt` | per-entry assertion time when Harbor provides it |
| `session_id` | `SessionID` | writes stay session-stamped (D-150) |
| `agent_id` | `SourceAgent` | metadata, never isolation |
| `outcome` | `Outcome` + `OutcomeDetail` | run outcome stamped on **all** records (D-024 signal; owner decision). **Deviation (§4.3):** the records `outcome` column is CHECK-constrained to `{'', 'success', 'failure'}` (day-one schema) and the Phase-19 reflection sweep keys off that axis, so Harbor's richer vocabulary (goal/no_path/…) cannot be stored verbatim in `outcome`. It is projected — `goal → success`, every other terminal outcome → `failure` — and the PRECISE run outcome is preserved verbatim in the free-text `outcome_detail` column. The D-024 signal is captured losslessly and stays queryable; see `records.projectRunOutcome`. |
| `run_id` | *(BufferKey, threaded by the handler)* | one run = one extraction buffer |
| `kind`, `step` | *(dropped)* | wire-validated for shape fidelity; ordering is preserved by append order; content is self-contained. Revisit if drilldown needs them. |
| `started_at`, `duration_ms`, `step_count`, `tool_invocations`, `format_version` | *(validated, dropped)* | run observability, not memory signal |

`RunCompletion` is the core-side typed payload (the mcpserver contract converts
into it) so the converter is surface-agnostic — a future HTTP surface is a thin
caller of the same core (deliberate MCP-only tiering this phase, see below).

### The handler (`internal/mcpserver/handlers.go`)

`makeIngestRunHandler(svc)`: validate → cross-check identity → convert via the
core → then **the same path as `memory_ingest`**: `records.New` per input,
`Store.Records().Append(ctx, scope, …)` (D-124 scope-authoritative),
`pipeline.TrySend` per record with `BufferKey: run_id` (P2: non-blocking,
panic-safe), then **eager flush**: `svc.PipelineStage.FlushKey(ctx, scope,
runID, "run_completion")` — best-effort (a flush error degrades to
`Flushed:false`, never fails the call: the records are durable and the idle
sweep remains the backstop, D-036 posture). ACK.

Shared-code discipline: the stamp→append→enqueue body is extracted into a
helper both `makeIngestHandler` and `makeIngestRunHandler` call (no
copy-paste), or the run handler builds `[]IngestRecord` and reuses the
existing flow — implementor's choice, but ONE implementation.

### Tiering (deliberate MCP-only — a sanctioned omission)

The run-completion sink ships on **MCP only**. The auto-save-target pattern is
an MCP-host contract (Harbor dispatches `tools/call`); no HTTP/SDK consumer
exists. Like `assert`'s deliberate HTTP omission, this is a documented tiering
decision (D-153), not drift: the converter lives in the core so a future HTTP
surface is a thin caller added when a consumer arrives.

### Catalog + count assertions

The tool registers unconditionally in `mcpserver.New` → the static catalog
grows 23 → 24. In the same PR: `scripts/smoke/phase-16.sh` (`WANT` + expected
names), the `New` doc comment, the boot-log tool count, and any other `23`
assertions found by grep. `memory_ingest_run` is on the open-handshake
`tools/list` like every tool; calling it requires the per-call bearer (D-152 —
unchanged, it is just a tool).

## Files added or changed

```text
internal/records/runsink.go              # RunCompletion + FromRunCompletion (core converter)
internal/records/runsink_test.go         # table + golden mapping tests + fuzz
internal/mcpserver/contracts.go          # IngestRunInput/Entry/Output
internal/mcpserver/handlers.go           # makeIngestRunHandler (thin caller)
internal/mcpserver/server.go             # register memory_ingest_run (24th tool)
internal/mcpserver/testdata/memory_ingest_run.input.schema.json   # generated
internal/mcpserver/testdata/memory_ingest_run.output.schema.json  # generated
internal/mcpserver/handlers_runsink_test.go  # handler tests (identity cross-check etc.)
test/integration/mcp_runsink_test.go     # §17: real marshaled payload over JWT dial
scripts/smoke/phase-ae12.sh              # smoke checks
scripts/smoke/phase-16.sh                # 23→24 catalog refresh
docs/decisions.md                        # D-153
docs/glossary.md                         # "run-completion sink"
docs/plans/ae-implementation-roadmap.md  # follow-up entry
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| *(none)* | — | The eager flush reuses the existing pipeline; format pin is a code constant (D-034: no knob without a consumer). |

## Acceptance criteria (binding)

1. **A byte-faithful marshaled `RunCompletionPayload` (format_version 1, all 13
   keys, entries carrying `kind`/`step`/`at`) validates and ingests** — proven
   with a fixture marshaled from a struct mirroring Harbor's, via a real
   `tools/call` over the JWT dial (integration test).
2. **Every conversation entry lands as one verbatim record, in order**, with
   `role`/`content` verbatim, `session_id`/`source_agent`/`outcome` stamped,
   `occurred_at` = entry `at` when present else `completed_at`, and the store
   row's `user_id` = the **JWT user** (D-124), never a divergent payload value.
3. **Identity cross-check fails closed**: payload `tenant_id` ≠ credential
   tenant → rejected; payload `user_id` ≠ verified JWT user → rejected; both
   with typed errors and no partial write.
4. **`format_version: 2` is rejected** with an error naming the supported
   version; malformed `completed_at` is rejected; empty `conversation` and
   empty `run_id` are rejected.
5. **All records share `buffer_key = run_id`** and an eager flush is requested
   (`Flushed:true` on the happy path); a flush failure degrades to
   `Flushed:false` with records durable and enqueued (P2 intact — the ACK
   never waits on extraction).
6. **`memory_ingest` is byte-identical**: its contract schema, handler
   behavior, and existing tests are unchanged (schema golden diff = empty).
7. **Catalog integrity**: `tools/list` returns 24 tools including
   `memory_ingest_run`; phase-16 smoke passes at the new count; prior phases'
   smoke scripts still pass.
8. **MCP-only tiering is enforced deliberately**: no HTTP route or SDK method
   is added; D-153 documents the omission; the converter compiles in
   `internal/records` with no mcpserver import (surface-agnostic core).
9. `scripts/smoke/phase-ae12.sh` reports `OK ≥ 6`, `FAIL = 0`.

## Smoke script

`scripts/smoke/phase-ae12.sh` — boots jwt-mode MCP-over-HTTP (static JWKS +
sqlite, per the ae11 smoke's minter), then:

- bearer-less `tools/list` → catalog contains `memory_ingest_run` (24 tools)
- bearer-less `memory_ingest_run` call → 401 (it is a tool like any other)
- JWT call with a full 13-key format_version-1 payload (entries carry
  `kind`/`step`/`at`) → 200, `ids` count = entry count
- sqlite check: records stamped with the JWT tenant/user + `outcome` + shared
  session
- JWT call with `format_version: 2` → tool error naming version 1
- JWT call with mismatched `tenant_id` → rejected
- `memory_ingest` regression: a plain records call still succeeds

## Test plan

- **Unit (core):** `FromRunCompletion` table — mapping rows, `at`-else-
  `completed_at`, empty conversation/run_id/bad timestamps rejected; a
  **fuzz target** (`FuzzFromRunCompletion`) asserting no panic and
  len(inputs) == len(conversation) on success (prime decode surface, §11).
- **Golden:** generated input/output schemas committed under `testdata/`
  (Dockyard contract-first); a golden test marshals a Harbor-shaped struct and
  asserts it validates against the generated input schema — the ST-2 failure
  mode is pinned by a test.
- **Handler:** identity cross-check matrix (jwt user match/mismatch/absent;
  tenant mismatch), flush degrade path, `memory_ingest` untouched-schema diff.
- **Integration (§17):** real sqlite + static JWKS + real go-sdk client over
  the ae11 open handshake; dispatch the marshaled fixture as the per-call-
  bearer `tools/call`; assert per-user rows + cross-user isolation negative +
  the buffer flush observed (extraction enqueued). `-race`.
- **Coverage:** `internal/records` ≥ 85 (conformance-adjacent), `internal/mcpserver`
  ≥ its band.

## Risks & mitigations

- **Harbor contract drift** (a 14th key, a new entry field): `format_version`
  is the gate — Harbor pins the shape at 1 and bumps on change; our schema
  mirrors v1 exactly and a v2 fails loudly. The golden marshal-validate test
  breaks in CI if our mirror ever diverges from the fixture shape.
- **Large transcripts vs transport limits:** the handler is P2-shaped (append
  + enqueue, no extraction inline); body-size limits are the transport's
  existing posture, unchanged. Documented: a pathological transcript rejected
  by the body cap fails loudly at the transport, never partially ingests.
- **Flush racing shutdown:** `FlushKey` errors degrade to `Flushed:false`
  (records durable, idle sweep backstop) — never a panic across the MCP
  boundary, never a lost record.

## Glossary additions

- **Run-completion sink** — an MCP tool that accepts Harbor's pinned
  `RunCompletionPayload` (format_version 1) and adapts it internally to the
  capability's own storage, making the capability a Harbor auto-save target.
  Stowage's is `memory_ingest_run` (D-153): identity from the verified
  per-call bearer (payload quad cross-checked, fail-closed), transcript
  verbatim, one extraction buffer per run, eager flush.

## Decisions filed

- D-153: the run-completion-sink pattern (`memory_ingest_run`) — payload
  identity is cross-checked never scope-authoritative; conversion in the core;
  MCP-only tiering sanctioned; format_version pinned.
