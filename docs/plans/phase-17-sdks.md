# Phase 17 — SDKs + zero-config agent wiring

- **Status:** draft
- **Owning subsystem(s):** `sdk/stowage`, `adapters/harbor` (own module),
  `clients/python`, `examples/embedded`
- **RFC sections:** §9.3, §10, §2, D-019/D-022/D-032
- **Depends on phases:** 16
- **Grounding (verified 2026-06-12):** Harbor `github.com/hurtener/Harbor`
  v1.3.1 PUBLIC. Seams: `assemble.Options.PreRegisterTools
  []tools.ToolDescriptor`; `inproc.RegisterFunc[I,O](cat, name, fn, opts...)`;
  identity via `identity.From(ctx)` (TenantID/UserID/SessionID + RunID);
  event bus `Event{Type, Identity, Payload}` with `task.completed`,
  `tool.invoked/completed`, `llm.cost.recorded`. **No per-turn middleware
  hooks exist** → D-032 amendment (D-062): zero-config wiring = memory tools
  auto-registered + EVENT-DRIVEN outcome capture (subscribe task.completed →
  feedback/outcome ingest), not turn middleware.

## Design

### `sdk/stowage` (core module, no new deps)

```go
type Client interface { // one interface, two constructors
    Ingest(ctx, IngestRequest) (IngestResponse, error)       // fire-and-forget
    Retrieve(ctx, RetrieveRequest) (RetrieveResponse, error) // envelope v1
    Drilldown / Feedback / ResolveCitations / Topics / Playbook(stub-aware)
}
func NewHTTP(baseURL, apiKey string, opts...) Client
func NewEmbedded(ctx, config.Config) (Client, func(ctx) error, error) // in-proc boot: store+pipeline+gateway+retrieval, sqlite default — D-022
```
- Embedded constructor reuses the cmd boot helper (exported into an
  `internal/boot` package consumed by cmd + sdk — no logic duplication).
- ONE shared conformance-style test suite runs against BOTH constructors
  (HTTP via httptest-mounted api server; embedded direct) — same-suite is
  the binding parity proof.
- Types mirror the HTTP v1 envelope exactly (citation, response_id,
  degraded flags).

### `adapters/harbor` (SEPARATE go.mod — D-063)

Keeps `github.com/hurtener/Harbor` (and its 67-dep tree) OUT of stowage
core. Module `github.com/hurtener/stowage/adapters/harbor`, requires
stowage (replace ../.. during dev; version on release) + Harbor v1.3.1.

- `Tools(client sdk.Client) []tools.ToolDescriptor` — the seven memory
  operations as in-proc tools (RegisterFunc), identity lifted per call:
  Harbor `identity.From(ctx)` → stowage scope (TenantID→tenant, UserID→user,
  SessionID→session). Drop into `assemble.Options.PreRegisterTools` — ONE
  line for a Harbor app = zero-config (D-032 satisfied at the tools level).
- `WireOutcomes(ctx, bus, client)` — subscribes `task.completed`/
  `task.failed` → outcome-tagged ingest signal + (optional) feedback on the
  response_ids the task's retrieves produced (correlation via a small
  in-adapter map keyed RunID, fed by the tool wrappers).
- `CostBridge(bus)` doc-recipe: stowage's own gateway cost events mirrored
  onto Harbor's bus shape (shape documented; full bridge optional code).
- `docs/recipes/harbor.md`: assemble snippet, flow-tool recipe
  (consolidation as a flow step), event wiring.
- CI: a new workflow job builds/tests adapters/harbor with a go.work or
  replace (document; runs the adapter's unit tests with a fake catalog/bus —
  Harbor test helpers if importable, else minimal fakes).

### `clients/python` (stdlib only)

`stowage_client.py` (urllib): ingest/retrieve/feedback/resolve_citations/
playbook; typed dataclasses; retries on 5xx; README. Smoke: phase-17 smoke
boots serve and runs `python3 clients/python/smoke.py` (skip if python3
absent).

### `examples/embedded`

`examples/embedded/main.go`: NewEmbedded with temp sqlite + mock gateway →
ingest a conversation → flush → retrieve → print citations. Built CGo-free
in the smoke (the Wails posture proof).

## Acceptance criteria (binding)

1. Same-suite parity: the shared suite passes against HTTP AND embedded
   constructors (identical assertions).
2. Embedded example builds CGO_ENABLED=0 and runs sqlite-only offline
   (degraded retrieval allowed) — smoke.
3. Harbor adapter compiles against Harbor v1.3.1; tools register on a real
   ToolCatalog (Harbor's inproc test pattern) and a call round-trips with
   identity lifted correctly (unit test with ctx-stamped Quadruple).
4. WireOutcomes: a synthetic task.completed event produces an outcome ingest
   (fake bus test).
5. Python client smoke green against live serve (ingest→retrieve→feedback).
6. Core go.mod UNCHANGED by Harbor (dependency-cleanliness check in CI: the
   adapter module is the only place Harbor appears).
7. eval-ci green; coverage ≥80 sdk + adapter; race ×3 sdk; smokes 01–17.

## Decisions filed

- D-062: zero-config Harbor wiring = auto-registered tools + event-driven
  outcome capture (no per-turn middleware exists in Harbor; amends D-032's
  ingest-on-turn framing honestly).
- D-063: adapters/harbor is a separate Go module so Harbor's dependency
  tree never enters stowage core; released in lockstep.
