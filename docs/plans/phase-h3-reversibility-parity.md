# Phase h3 — Reconciliation reversibility parity (rollback/confirm/reject/get across surfaces)

- **Status:** in-progress
- **Owning subsystem(s):** `internal/reconcile` (new exported core), `internal/api` (thin callers), `internal/mcpserver` (new tools), `sdk/stowage` (new Client methods)
- **RFC sections:** §6 (reconciliation is reversible), §9.1 (HTTP), §9.2 (MCP), §9.3 (SDK)
- **Depends on phases:** 15/18 rollback+confirmation (D-064, D-065), h1 (StartPipeline), h2 — all shipped
- **Informing briefs:** 03 (Engram pipeline + reconciliation shape), 02 (CC-memory lifecycle/supersede model)
- **Program:** D-067 Wave B (mechanical re-homing — single-user tier). Pre-reserved decision: **D-070**.

## Goal

When this phase is done, reconciliation **reversibility** (D-017/D-064) and
pending-confirmation resolution (D-065) — both binding, single-user capabilities —
are reachable and behave identically on all three surfaces {embedded SDK, HTTP,
MCP}, not HTTP-only. The orchestration that today lives in the HTTP handler is
lifted into an exported `reconcile` core that all three surfaces call, so the
"one logic core, thin surfaces" principle (D-067) holds and the surfaces cannot
drift. Closes the parity blocker "reconciliation rollback reachable only on the
HTTP server path" (`docs/notes/parity-lens-findings.md`, Pattern P1).

## Brief findings incorporated

- **Brief 03:** reconciliation is a core pipeline concern, not an API concern;
  its inverse belongs in the same core layer that owns the forward operation
  (`internal/reconcile`), with surfaces as thin callers.
- **Brief 02:** supersede/confirm semantics (the CC-memory lifecycle model) are
  store-/reconcile-layer concerns; exposing them per-surface must not fork the
  logic.

## Findings I'm departing from

- None. One **open question for the owner** (below): whether `memory_get` and
  `memory_patch` (confirm/reject) ship as distinct MCP tools or are folded into a
  single `memory_admin`-style tool. Recommendation in the Design section.

## Design

### The core (single source of truth)
Lift the rollback orchestration out of `internal/api/memories_handler.go`
(`handleRollbackMemory`, `commitUpdateRollback`, the merge all-or-nothing path,
and the 409 conflict guards — currently ~lines 198-450) into exported
`internal/reconcile` functions:

```go
// Rollback inverts the NEWEST reconciliation event for memory id within scope
// (D-064: newest-event-only, atomic, tombstone=deleted, merge all-or-nothing).
// Returns the restored memory + a typed conflict error (ErrAlreadyRolledBack,
// ErrDownstreamSupersede, ErrNoPriorState) the surfaces map to 409.
func Rollback(ctx context.Context, st store.Store, scope identity.Scope, id string) (*RollbackResult, error)

// Resolve applies confirm|reject to a pending_confirmation memory (D-065),
// riding the supersede path so the resolution is itself reversible.
func Resolve(ctx context.Context, st store.Store, scope identity.Scope, id string, action ConfirmAction) (*ResolveResult, error)
```

`MarshalPriorState` (already exported) stays in `reconcile`. `store.ActionRollback`
is unchanged. Memory **read** (`handleGetMemory`) is a scoped store read; expose
it as `reconcile.GetMemory` *or* call `store.Memories().Get` directly from the
embedded client (it is already scope-enforced) — pick the lower-ceremony path
that keeps the HTTP/SDK/MCP outputs byte-identical.

### Surfaces (thin callers)
- **HTTP** (`internal/api`): `handleRollbackMemory` / the PATCH handler / `handleGetMemory`
  become thin wrappers over the new core — **byte-for-byte behavior-preserving**
  (existing api tests pass unmodified; the emitted `memory.rolled_back` /
  `memory.superseded` event payloads are pinned by golden tests).
- **SDK** (`sdk/stowage`): add to the `Client` interface — `Rollback(ctx, MemoryRef)`,
  `ResolveMemory(ctx, ...)` (confirm/reject), `GetMemory(ctx, id)` — implemented
  by both `embeddedClient` (calls the reconcile core on `stack`) and `httpClient`
  (calls the existing routes). Single-user tier ⇒ on the SDK.
- **MCP** (`internal/mcpserver`): add `memory_rollback`, `memory_get`, and a
  confirm/reject tool. **Recommendation:** one `memory_resolve` tool with an
  `action: confirm|reject` field + a separate `memory_rollback` + `memory_get`
  (mirrors the HTTP verbs cleanly; keeps each tool single-purpose per D-015's
  small-surface discipline). Owner confirms tool granularity (open question).

### Concurrency / atomicity
The core uses the existing single-transaction `Memories().Commit` unit (D-045);
no new transactional surface. The reconcile functions are stateless (scope +
store passed in), safe under concurrent use.

## Files added or changed

```text
internal/reconcile/rollback.go      # NEW — Rollback/Resolve/typed conflict errors (lifted from api)
internal/api/memories_handler.go    # thin callers; behavior-preserving
internal/mcpserver/server.go        # register memory_rollback, memory_get, memory_resolve
internal/mcpserver/handlers.go      # handlers calling the reconcile core
internal/mcpserver/contracts.go     # typed tool I/O + schema goldens
sdk/stowage/client.go               # Rollback/ResolveMemory/GetMemory on the interface
sdk/stowage/embedded.go             # embedded impls (reconcile core)
sdk/stowage/http.go                 # http impls (existing routes)
sdk/stowage/suite_test.go           # AC-1 cross-impl suite covers the new verbs
scripts/smoke/phase-h3.sh           # NEW — rollback reachable + identical on SDK + MCP
test/integration/reversibility_parity_test.go  # NEW
docs/plans/README.md                # hardening-track rows
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| (none) | — | Pure re-homing; no new knobs (D-034 not engaged). |

## Acceptance criteria (binding)

1. `reconcile.Rollback` + `reconcile.Resolve` exist and own the orchestration;
   `internal/api` handlers are thin callers with NO rollback/confirm logic of
   their own (the merge all-or-nothing + 409 guards live in the core).
2. **Behavior preservation:** existing `internal/api` rollback/confirm tests pass
   **unmodified**; golden tests pin the `memory.rolled_back` / `memory.superseded`
   event payloads (prior-state JSON) so the inverse is byte-identical to today.
3. Rollback, confirm/reject, and memory-get are reachable on the SDK `Client`
   (embedded **and** http impls) and as MCP tools.
4. **Both-paths-identical bar:** `test/integration/reversibility_parity_test.go`
   drives ingest→reconcile(supersede)→rollback through embedded **and** server
   (real sqlite) and asserts the restored memory + emitted events are identical;
   `-race`. (Same scenario passes on both paths in THIS PR.)
5. D-064 conflict guards (double-rollback, downstream-supersede, missing snapshot)
   return 409/typed error identically on all three surfaces.
6. New MCP tools have schema goldens; new SDK methods + MCP tools + smoke checks
   ship in this PR (§4.2/§13).

## Smoke script

`scripts/smoke/phase-h3.sh` — build; via the embedded SDK: ingest→force a
supersede→rollback→assert prior memory restored; via `stowage mcp` (stdio):
`memory_rollback` on a superseded memory→assert restored; assert a double
rollback returns the conflict error on both. SKIP-graceful pre-build.

## Test plan

- **Integration (§17):** reversibility parity across embedded + server, real
  sqlite, ≥1 conflict path, `-race`.
- **Golden:** rollback/supersede event payloads (behavior-preservation).
- **Unit:** reconcile core conflict-guard table; SDK suite_test covers the 3 new
  verbs on both impls; MCP schema goldens.

## Risks & mitigations

- *Risk:* lifting the handler logic changes the emitted event shape. *Mitigation:*
  golden tests pin payloads; existing api tests unmodified (AC-2).
- *Risk:* merge rollback all-or-nothing semantics subtly differ once centralized.
  *Mitigation:* the integration test covers a merge rollback; conflict-guard table.

## Glossary additions

- **reconcile.Rollback / Resolve** — the exported reversibility core shared by all
  surfaces (supersedes the API-resident rollback orchestration).

## Decisions filed

- **D-070** — reconciliation reversibility (rollback/confirm/reject/get) is owned
  by an exported `internal/reconcile` core and reachable identically on {SDK, MCP,
  HTTP}; the HTTP handler becomes a thin caller (one-core-many-surfaces, D-067).
