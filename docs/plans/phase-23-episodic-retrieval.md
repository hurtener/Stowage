# Phase 23 — Episodic retrieval (RFC §6b)

- **Status:** implemented
- **Owning subsystem(s):** `internal/episodes` (read/view assembly), `internal/api`,
  `internal/mcpserver`, `sdk/stowage` (the tiered surfaces), `test/integration`
- **RFC sections:** §6b (episodic retrieval: `GET /v1/episodes`, cross-episode
  aggregation; similar-episode contrast), §9.5 / D-067 (one core, thin tiered
  surfaces), §9.1-3 (HTTP/MCP/SDK)
- **Depends on phases:** 22 (episodes + narratives), 09 (retrieval/vindex), 11
  (drill-down), h5/D-072 (the playbook tiered-surface precedent)
- **Informing briefs:** 06 (mempalace — narrative/temporal positioning), 02 (LoCoMo)

## Goal

When this phase is done, a scope's episodes (Phase 22) are readable through a
**`memory_episodes`** capability on all three single-user surfaces (`GET /v1/episodes`
HTTP, the `memory_episodes` MCP tool, `Client.Episodes` SDK) — list (most-recent-first,
paginated) and get-one, each with its narrative; plus an optional `[from,until]` time
window that returns the episodes of a period as a **deterministic structured summary**
(episode narratives, not a raw fragment dump) — the cross-episode aggregation of
RFC §6b. The capability is LLM-free, byte-identical across the three surfaces (parity
test, D-067). Drill-down to the narrative's verbatim records reuses the existing
`/v1/drilldown` via the returned `narrative_memory_id`.

## Findings I'm departing from

- **RFC §6b also lists similar-episode contrast (vector over narratives) and
  optional gateway-synthesized aggregation.** Phase 23 ships the **deterministic**
  retrieval surface (list/get/window) only; **similar-episode contrast and
  LLM-synthesized summaries are deferred to Phase 23b** (D-080). Reasons: (a) the
  deterministic windowed list IS the §6b "structured summary, never a fragment dump";
  (b) similar-episode is a vector search whose cross-surface output is
  non-deterministic, so it cannot share the byte-identical parity test that gates the
  tiered surfaces — it needs its own (fake-embedder) test harness; (c) it adds a
  gateway/vindex dependency to an otherwise gateway-free, always-available endpoint.
  Deferring keeps Phase 23 fully parity-tested and the list/get path degradation-free.

## Design

### Core — `internal/episodes` view assembly (deterministic, gateway-free)

Mirrors `playbook.Assemble`: one function the three surfaces call.

- `EpisodeView{ID, SessionID, Title, Status, Outcome, StartedAt, EndedAt,
  NarrativeMemoryID, Narrative}` — wire-neutral.
- `List(ctx, st, scope, ListOptions) (ListResult, error)`:
  - No filter: `EpisodeStore.ListEpisodes(scope, limit, cursor)` → views (+ narrative
    via `MemoryStore.Get(NarrativeMemoryID)`), passing through the store's cursor.
  - Filter set (`From`/`Until`/`SessionID`): page through (bounded) and filter
    in-service, return the matched window, `NextCursor=""` (a window is a bounded
    period — the structured summary). Documented v1 semantics.
- `Get(ctx, st, scope, id) (*EpisodeView, error)` — `GetEpisode` + narrative; the
  view carries `NarrativeMemoryID` for `/v1/drilldown`.

### Tiered surfaces (D-067 — same core, parity-tested)

One `memory_episodes` capability with input `{limit?, cursor?, from?, until?,
session_id?, id?}` and output `{episodes:[EpisodeView], next_cursor}`:
- **HTTP** `GET /v1/episodes` (`internal/api/episodes_handler.go`): tenant scope from
  the auth key (a tenant query matches all the tenant's episodes — `buildScopeWhere`);
  `?id=` → get-one (0/1 episodes); `?from=&until=&session_id=&limit=&cursor=` → list.
- **MCP** `memory_episodes` (`internal/mcpserver`): contracts + handler (`ScopeFn`) +
  schema golden (D-061).
- **SDK** `Client.Episodes` (`sdk/stowage`): HTTP mode (`GET /v1/episodes?...`) +
  embedded mode (`episodes.List/Get` off `c.stack.Store`).

### Parity (D-067)

`test/integration/episodes_parity_test.go` mirrors `playbook_parity_test.go`: seed a
shared sqlite DSN with **fixed-ULID** episodes + narrative memories (inserted
directly — no live narration gateway), read through embedded/HTTP/MCP, assert
byte-identical JSON + the expected ordering/content.

## Files added or changed

```text
internal/episodes/view.go              # EpisodeView + List/Get (deterministic)
internal/episodes/view_test.go         # list/get/window table tests
internal/api/episodes_handler.go       # GET /v1/episodes + route
internal/api/server.go                 # route registration
internal/mcpserver/{contracts,server,handlers}.go  # memory_episodes tool
internal/mcpserver/testdata/memory_episodes.*.schema.json  # golden (D-061)
sdk/stowage/{client,types,http,embedded}.go  # Client.Episodes
test/integration/episodes_parity_test.go     # all-surfaces-identical
scripts/smoke/phase-23.sh
docs/plans/phase-23-episodic-retrieval.md ; docs/decisions.md (D-080) ; docs/glossary.md
```

## Config keys added

None — read-only retrieval; episodes are profile-gated at generation (Phase 22).

## Acceptance criteria (binding)

1. `memory_episodes` (list + get + `[from,until]` window) is reachable on HTTP, MCP,
   and the SDK (HTTP + embedded), all calling the one `internal/episodes` view core.
2. The capability is **LLM-free** and degradation-free (no gateway dependency);
   `internal/episodes` view path imports no provider SDK (P5 holds trivially).
3. **Parity:** embedded == HTTP == MCP byte-identical JSON for a seeded fixture
   (the D-067 bar), with a non-trivial assertion on ordering/content.
4. Scope (P3): reads are scope-parameterized; a tenant key sees the tenant's
   episodes; `?session_id=`/window narrow in-service.
5. The window query returns a structured episode-narrative summary (not a fragment
   dump); get-one returns the narrative + `narrative_memory_id` for drill-down.
6. A new MCP tool ⇒ schema golden updated (D-061); a new endpoint/tool/SDK method ⇒
   same-PR smoke (§4.2).
7. Gates: build, `go test -race ./...`, golangci-lint, gofmt, coverage, preflight,
   drift-audit, mirror green.

## Smoke script

`scripts/smoke/phase-23.sh`: `GET /v1/episodes` route registered; `memory_episodes`
tool registered + schema golden present; SDK `Episodes` on the Client interface;
`internal/episodes` view tests + the parity test pass; eval-ci unaffected.

## Test plan

- Unit: `List`/`Get` (no-filter pagination, window filter, session filter, narrative
  attach, empty) — table-driven over a real sqlite store.
- Parity (§17): all-surfaces-identical over a fixed fixture.
- Schema golden: `memory_episodes` input/output.

## Risks & mitigations

- **Filtered pagination semantics** → v1: window/session queries return the bounded
  matched set with `NextCursor=""` (documented); unfiltered list paginates exactly.
- **N+1 narrative fetch** → bounded by page size; acceptable for coarse episodes; a
  batch `GetMany` is a follow-up if needed.
- **Surface drift** → the parity test is the mechanical guard (independently-declared
  wire structs, byte-compared).

## Glossary additions

- **Episodic retrieval** — reading episodes + their narratives through the
  `memory_episodes` capability (list / get / time-window), deterministic and
  LLM-free (Phase 23, D-080); the §6b read side over Phase-22 episodes.

## Decisions filed

- **D-080** — Episodic retrieval ships as the deterministic `memory_episodes`
  capability (list + get + `[from,until]` window) across {SDK, HTTP, MCP} with a
  byte-identical parity test (D-067), reusing the Phase-22 `EpisodeStore` + a new
  gateway-free `internal/episodes` view core. The windowed list is the §6b
  cross-episode "structured summary, never a fragment dump." **Similar-episode
  contrast (vector over narratives) and gateway-synthesized aggregation are deferred
  to Phase 23b** — their output is non-deterministic (incompatible with the
  byte-identical parity bar) and adds a gateway/vindex dependency to an otherwise
  always-available read path; they get their own fake-embedder test harness.
