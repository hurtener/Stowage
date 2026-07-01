# Phase ae4a — lean MCP read (`Text` markdown + episode hook + drill by citation ULID)

- **Status:** implemented (see "As-built deviations" below)
- **Owning subsystem(s):** `internal/mcpserver` (the `memory_retrieve` handler + tool doc); `internal/retrieval` (the `RenderMCP` slot-fill in `render.go` — activating the citation/episode slots ae3 stood up, plus one `RenderReadBody` helper); the `sdk/stowage` + `internal/api` parity surfaces
- **RFC sections:** §4.2 (read path / drill-down), §5.7 (injections & citations), §6b (episodes), §9.2 (MCP surface), §9.5 (one logic core, D-067/D-073)
- **Depends on phases:** **ae3** (the parameterized render core: `RenderMode`, `RenderItem`, `RenderResult`, `Render`, `RenderItemsFromMemoryItems` — the slots ae4a fills); the shipped injections/citations path (`MemoryItem.Citation`, `Injections().Get`); the shipped episodes phase (`store.Memory.EpisodeID`). Lands **after ae3** within Wave 0.
- **Informing briefs:** 06 (mempalace — benchmark-led positioning; the *lean* reader context is the differentiator that beats a 43k-token reader; gateway-free retrieval, so the render is a pure model-free function), 05 (ACE — context engineering; the lean, shaped context is the context-collapse defense), 02 (CC-memory predecessor — surface-sprawl cautionary tale → one render core, thin surfaces, D-067). The empirical reader-lever basis (the `| When:` assertion-date lever, CURRENT/SUPERSEDED sectioning) is eval-derived (D-105/D-109/D-114), inherited through ae3's `Render`.

## Goal

When this phase is done, `memory_retrieve` returns **lean markdown in the `Text`
block** — the model-facing channel Dockyard routes into the agent's context —
instead of today's count-only string (`internal/mcpserver/handlers.go:244`). The
body is produced by ae3's `Render(RenderMCP, …)` with its two affordance slots
now **live**: (a) an **episode hook** per item sourced from the already-loaded
`store.Memory.EpisodeID` (**free — no new store query**), and (b) a **drill
handle equal to the existing per-item citation ULID**, so a follow-up
`memory_drilldown` reuses the existing citation→verbatim path with **zero new
store code**. The full typed result still travels in the `Structured` block for
Apps hosts. The same rendered body is exposed on HTTP and the SDK (a `rendered`
response field) so the capability ships once in the core and identically on all
three single-user read surfaces (D-067/D-073). **This is model-context economy,
not a wire-size win** — the `Text`/`rendered` body travels *alongside* the full
structured payload, so the total payload **grows**; only the model's context
shrinks (M4).

## Brief findings incorporated

- **06 (mempalace):** the win is a *lean* reader context, not a dump — so the MCP
  `Text` becomes the shaped, trimmed body (not raw JSON, not a count). The render
  is a pure, model-free function (no gateway call), so the lean read serves in the
  D-036 degraded path unchanged.
- **05 (ACE):** a single audited render shape is where the CURRENT/SUPERSEDED
  partition, the `| When:` date lever, and now the drill/episode affordances stay
  coherent — one place, not per-surface strings.
- **02 (CC-memory):** surface sprawl is the named predecessor failure → the
  rendered body is composed **once** (`retrieval.RenderReadBody`) and every surface
  is a thin caller; no surface hand-rolls its own markdown.

## Findings I'm departing from

- **The ae3 seam ae4a "fills" is `internal/retrieval/render.go`, created by ae3 —
  not a pre-existing MCP renderer.** As ae3 already recorded (its D-141 scope
  correction), MCP has *no* renderer today: `makeRetrieveHandler` emits a
  count-only `Text` plus the full typed `Structured`
  (`handlers.go:243-246`). ae4a is therefore the **first** time MCP emits a
  rendered `Text` body. Any framing that says "MCP already renders" is wrong. ae4a
  is co-sequenced strictly **after** ae3 — it cannot compile until ae3 lands
  `Render`/`RenderItem`/`RenderResult`/`RenderItemsFromMemoryItems`.
- **`RenderResult` field-name reconciliation.** ae3's plan prose refers in one
  place to `Render(RenderMCP, items).Body`, but the `RenderResult` type it defines
  exposes `ContextBlock` (+ `Lines`/`CurrentOnly`), **not** a `.Body` field. ae4a
  reads **`.ContextBlock`** — the assembled reader body — and adds a
  `RenderReadBody(items) string` convenience that composes the mapper + `Render`
  so no surface repeats the two-call idiom. (Recorded in D-142 so the naming gap is
  closed in code, not left latent.)
- **Parity means a `rendered` field on HTTP/SDK, not a `Text` block.** MCP's
  `tool.Result` has a first-class model-facing `Text`; HTTP/SDK return JSON. To
  ship the *same* capability on all three (D-067/D-073) without inventing an MCP-
  only feature, HTTP `retrieveResponse` and SDK `RetrieveResponse` gain a
  `rendered` string carrying the identical `RenderReadBody` output. This grows the
  JSON payload — stated honestly per M4, and consistent with ae3's note that
  HTTP/SDK were renderer-free JSON mirrors that ae4a (not ae3) extends.
- **The episode hook and drill handle are always-on affordances, not knobs.** They
  are deterministic projections of already-returned data; per ae3's AC7 `RenderMode`
  stays a two-value call-site argument. **ae4a adds no config key** (see Config).

## Design

### 1. Activate the `RenderMCP` slots in `internal/retrieval/render.go` (ae3's file)

ae3 shipped `RenderItem` with two slots wired but **inert** for both modes:

```go
Citation  string // per-item injection ULID = drill handle (ae4a fills)
EpisodeID string // store.Memory.EpisodeID → episode hook (ae4a fills)
```

ae4a edits **only the `RenderMCP` branch** of `Render` so that, per item, it
appends:

- a **drill handle** — `[cite:<Citation>]` — whenever `Citation != ""`; and
- an **episode hook** — `[episode:<EpisodeID>]` — **iff** `EpisodeID != ""`.

`RenderEval`'s output is **untouched** (its `RenderItem` slots are always zero —
eval decodes over the wire and carries neither field — and the `RenderMCP`-only
branch never runs for `RenderEval`). This is the one phase in which `RenderMCP`
and `RenderEval` **diverge**; ae3's "base body equal / slots inert" diff test
(its AC4/AC5) is **intentionally revised** here to assert the divergence instead
(`RenderMCP` emits the handles, `RenderEval` does not), while ae3's eval
byte-freeze (`TestReaderPrompt_Golden`, `TestEvalCI`) still passes unchanged
because those exercise only `RenderEval`.

The exact marker syntax (`[cite:…]` / `[episode:…]` appended to each item line
inside the CURRENT/SUPERSEDED sections ae3 assembles) is pinned by a `RenderMCP`
golden so ae4b and later phases cannot drift it.

### 2. The one-place composition helper (core, pure)

Add next to `Render`:

```go
// RenderReadBody renders the model-facing lean markdown body for a retrieval
// response. It is the ONE place the RenderMCP mode and the MemoryItem→RenderItem
// mapper are composed, so every surface (MCP Text, HTTP rendered, SDK Rendered)
// emits a byte-identical reader body. Pure: no receiver, no store, no ctx, no
// gateway call (D-036 gateway-free; the source data is already loaded on the
// Response). The episode hook reads item.Memory.EpisodeID — already populated by
// the retrieval GetMany — so no new store query is issued.
func RenderReadBody(items []MemoryItem) string {
    return Render(RenderMCP, RenderItemsFromMemoryItems(items)).ContextBlock
}
```

`RenderItemsFromMemoryItems` (shipped by ae3) already carries `it.Citation` and
`it.Memory.EpisodeID` into the slots. The **signature is the no-new-query proof**:
`RenderReadBody` takes `[]MemoryItem` and returns a `string` — it has no `Store`,
no `context.Context`, and issues no I/O. The episode hook is a field read on data
the handler already holds.

### 3. MCP handler flip (`internal/mcpserver/handlers.go`)

Replace the count-only `Text` at `handlers.go:244`:

```go
return tool.Result[RetrieveOutput]{
    Text:       retrieval.RenderReadBody(resp.Items), // lean markdown (was: "Retrieved %d item(s)…")
    Structured: out,                                  // full typed result, unchanged
}, nil
```

The `RetrieveOutput` / `RetrieveItem` mapping loop (`handlers.go:202-223`) is
**unchanged** — `Structured` keeps every field it has today. When `resp.Items` is
empty, `RenderReadBody` returns ae3's empty-context sentinel (ae3's empty-context
render case); the `Structured` block still carries `response_id`, so the drill and
feedback loop is intact.

### 4. Drill-down: reuse the existing citation path verbatim (H1)

No handler change. `memory_drilldown` already resolves a citation ULID to a
memory via `svc.Store.Injections().Get(ctx, scope, in.Citation)` →
`inj.MemoryID` (`handlers.go:326-332`). The `[cite:<ULID>]` handle ae4a emits **is
that same ULID** (`MemoryItem.Citation`, minted per item at retrieve time), so a
reader that copies the handle back into `memory_drilldown` round-trips through the
unchanged path with **zero new store code**.

**Explicitly forbidden here (H1):** no `(response_id, rank)` positional drill
method is added. The store carries the raw materials (`InjectionStore.ListByResponse`
is rank-ascending; `Injection.Rank` exists) but wiring a positional lookup **is**
new injections-store code and is deferred to ae4b. A grep AC asserts no such method
appears.

### 5. HTTP + SDK parity (`rendered` field)

- **HTTP** (`internal/api/retrieve_handler.go`): add `Rendered string
  \`json:"rendered,omitempty"\`` to `retrieveResponse`; set it from
  `retrieval.RenderReadBody(resp.Items)` after the item loop. The request type is
  untouched (no new input field).
- **SDK** (`sdk/stowage/types.go`): add `Rendered string
  \`json:"rendered,omitempty"\`` to `RetrieveResponse`. The embedded client
  (`sdk/stowage/embedded.go`) sets it from `RenderReadBody(resp.Items)` at the
  in-process call site; the HTTP-mode client (`sdk/stowage/http.go`) rides the JSON
  tag — both decode the identical body.

All three read bodies come from the single `RenderReadBody` call, so a parity test
can assert MCP `Text` == HTTP `rendered` == SDK `Rendered` byte-for-byte over one
fixture.

### 6. Tool doc — the M4 wire-truth (`internal/mcpserver/server.go:84`)

Extend the `memory_retrieve` `Describe(…)` string to state the honest contract:

> *Returns a lean markdown reader body in the model-facing `Text` block (episode
> hooks + per-item `[cite:…]` drill handles for `memory_drilldown`) and the full
> typed result in `Structured`. The lean body shrinks the model's context, not the
> wire payload — both blocks travel, so a host reading both receives a larger
> payload, not a smaller one.*

The same M4 statement lives in this plan (this section) and the D-142 entry.

### Concurrency & degradation

`RenderReadBody`/`Render` are pure functions with no receiver state (ae3's AC6
concurrent-reuse test covers `Render`; ae4a extends the fixture to the populated
slots). The render performs **no gateway call**, so a degraded (gateway-free)
retrieval still renders its lexical/structured results with hooks and handles
intact (D-036). The episode hook and drill handle **fail soft by construction**: a
missing `EpisodeID`/`Citation` simply omits that marker — there is no error path to
fail on.

## Files added or changed

```text
internal/retrieval/render.go            # CHANGED (ae3's file) — activate RenderMCP Citation+EpisodeID slots; add RenderReadBody()
internal/retrieval/render_test.go       # CHANGED (ae3's file) — RenderMCP golden with live slots; RenderMCP≠RenderEval divergence; slot-omit cases; slot concurrency
internal/mcpserver/handlers.go          # CHANGED — retrieve Text = retrieval.RenderReadBody(resp.Items)
internal/mcpserver/server.go            # CHANGED — memory_retrieve Describe() states the M4 wire-truth
internal/mcpserver/handlers_test.go     # CHANGED — Text is now the rendered body (episode hook iff EpisodeID; [cite:…] handle); no-new-store-query assertion
internal/api/retrieve_handler.go        # CHANGED — retrieveResponse.Rendered from RenderReadBody
sdk/stowage/types.go                    # CHANGED — RetrieveResponse.Rendered
sdk/stowage/embedded.go                 # CHANGED — set Rendered at the in-process call site
scripts/smoke/phase-ae4a.sh             # NEW
test/integration/retrieve_lean_read_test.go  # NEW — real-driver: citation-ULID drill round-trip + episode hook + parity + a failure mode (§17)
docs/plans/README.md                    # CHANGED — ae4a row → draft (registered by ae3; ae4a flips status)
docs/decisions.md                       # CHANGED — D-142
docs/glossary.md                        # CHANGED — lean MCP read / rendered read body, episode hook, drill handle
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| _(none)_ | — | The episode hook and the citation drill handle are **always-on** deterministic projections of already-returned data; per ae3's AC7 `RenderMode` stays a two-value call-site argument, **not** a D-034 config knob. Zero-config start is unchanged (smoke asserts the retrieve `Text` renders with no config set). |

## Acceptance criteria (binding)

1. **Lean `Text` + full `Structured` (MCP).** `memory_retrieve` returns the
   `RenderReadBody` markdown in `Text` (no longer the count string) **and** the
   complete `RetrieveOutput` in `Structured` unchanged. A handler test pins both.
2. **Episode hook iff `EpisodeID`, no new store query.** The rendered body contains
   an `[episode:<id>]` hook for an item **exactly when** `item.Memory.EpisodeID !=
   ""`, and for none otherwise. The hook adds **zero** store calls: proven by
   `RenderReadBody`'s signature (`func([]MemoryItem) string` — no `Store`, no
   `ctx`) **and** a store-spy assertion that the render step issues no
   `Injections()`/`Episodes()`/`Memories()` call beyond the retrieve itself.
3. **Drill handle = existing citation ULID, round-trips existing path.** Each item's
   handle in the body is exactly its `MemoryItem.Citation` ULID; feeding that handle
   to `memory_drilldown` resolves via the unchanged `Injections().Get` →
   `inj.MemoryID` path and returns the item's provenance spans. **No
   `(response_id, rank)` method** is added — a grep asserts no new `InjectionStore`
   method and no positional-drill handler (H1, deferred to ae4b).
4. **M4 wire-truth stated in the tool doc AND this plan.** The `memory_retrieve`
   `Describe()` string states that both blocks travel and the total payload grows
   (context economy, not wire economy); a test asserts the doc string contains that
   statement, and this plan's Design §6 records it.
5. **Parity {SDK, HTTP, MCP}.** HTTP `retrieveResponse.rendered` and SDK
   `RetrieveResponse.Rendered` carry the identical body; a parity test asserts MCP
   `Text` == HTTP `rendered` == SDK `Rendered` byte-for-byte over one fixture, all
   sourced from the single `RenderReadBody`.
6. **`RenderMCP` diverges from `RenderEval`; eval stays byte-frozen.** A render test
   asserts `RenderMCP` emits the `[cite:…]`/`[episode:…]` markers and `RenderEval`
   does **not**; ae3's `TestReaderPrompt_Golden` and `TestEvalCI` pass **unchanged**
   (eval exercises only `RenderEval`).
7. **Gateway-free (D-036).** The render performs no gateway call; the lean body
   renders on a degraded retrieval (a test builds the body from a degraded
   `Response`).
8. **No config key; zero-config unchanged.** No new key in `internal/config`
   (grep); `stowage serve` with no config renders the lean `Text` (smoke).

## Smoke script

`scripts/smoke/phase-ae4a.sh` — SKIPs gracefully until the surface exists; then one
line per check:

- `retrieval.RenderReadBody` is defined in `internal/retrieval/render.go`.
- `internal/mcpserver/handlers.go` retrieve handler no longer emits the count-only
  `"Retrieved %d item(s)"` `Text` (asserts the flip landed).
- the `memory_retrieve` `Describe()` string in `server.go` contains the M4
  wire-truth phrase ("payload grows" / "context, not the wire").
- no `(response_id, rank)` / positional drill method exists on `InjectionStore`
  (grep — H1 guard).
- `rendered` field present on the HTTP `retrieveResponse` and SDK `RetrieveResponse`.
- no `RenderMode`/render literal added to `internal/config` (not a knob).
- `go test ./internal/retrieval/ -run Render` and
  `go test ./internal/mcpserver/ -run Retrieve` pass.
- `OK ≥ count(criteria)`, `FAIL = 0`, exit nonzero iff any FAIL.

## Test plan

- **Render unit/golden (`render_test.go`, extends ae3's):** a `RenderMCP` golden
  with populated `Citation`+`EpisodeID` slots (both markers present); an item with
  `Citation` but no `EpisodeID` (drill handle present, episode hook absent — AC2
  boundary); the `RenderMCP` ≠ `RenderEval` divergence over a shared fixture
  (AC6); an empty-items case; a concurrent-reuse run over populated slots under
  `-race` (extends ae3's AC6 concurrency).
- **MCP handler (`handlers_test.go`):** `Text` is the rendered body (contains the
  fixture's citation ULID and, for an episode-bearing item, its episode hook);
  `Structured` unchanged; a **store-spy** run asserting the render step issues no
  additional store call (AC2); a degraded-`Response` render (AC7).
- **Parity test:** MCP `Text` == HTTP `rendered` == SDK `Rendered` byte-for-byte
  (AC5).
- **Integration (`test/integration/retrieve_lean_read_test.go`, real drivers, §17
  — ae4a closes the ae3 render seam, consumes injections/citations + episodes, and
  adds the public `RenderReadBody`):** ingest→retrieve on **both** sqlite and
  postgres, extract the `[cite:…]` handle from the rendered body, feed it to
  `memory_drilldown`, and assert it round-trips to the correct verbatim spans (AC3);
  assert an episode-bearing memory surfaces its hook (AC2); scope propagation (a
  drill with a citation from another scope fails — the **failure mode**); `-race`.
- **Regression:** ae3's `TestReaderPrompt_Golden`, `TestReaderPrompt_SupersededSection`,
  `TestEvalCI`, `TestEvalCIGateBites` pass unchanged (AC6).
- **No new fuzz target:** ae4a adds no parse surface (the body is *emitted*, never
  re-parsed — ae3 already removed the `[OUTDATED]` re-parse).

## Risks & mitigations

- **Implying a wire-size win (the headline risk).** Mitigated by AC4 — the honest
  M4 statement is forced into the tool doc, this plan, and D-142; a test pins the
  doc string.
- **Episode hook tempting a new store load.** Mitigated by AC2 — `RenderReadBody`'s
  store-free/ctx-free signature makes a new query impossible without changing the
  signature, plus a store-spy assertion. The hook reads `Memory.EpisodeID` already
  on the `Response`.
- **Positional-drill drift.** Mitigated by AC3 + the smoke grep — no
  `(response_id, rank)` method here; it is ae4b's, gated because it *is* new store
  code.
- **Moving eval bytes.** Mitigated by AC6 — the markers live in the `RenderMCP`
  branch only; `RenderEval`'s inputs carry zero-valued slots and its branch never
  emits them, so the eval golden is untouched.
- **Empty-result `Text`.** Mitigated by reusing ae3's empty-context render case;
  `Structured` still carries `response_id` so the loop stays intact.

## Open question (recorded, per the owner's request)

**Positional short drill-handle vs the 26-char citation ULID.** ae4b introduces a
positional `(response_id, handle)` drilldown; a short positional handle (e.g. a
1–2-char index the reader passes back) would cost far fewer tokens in the rendered
body than repeating a 26-char citation ULID per item. **ae4a defaults to the
charter's citation-ULID handle** — it reuses the existing drill path with **zero
new store code**, which the positional handle cannot (it *is* new injections-store
code, gated to ae4b by H1). The tradeoff: ~26 chars × N items of body tokens now,
vs deferring the token trim until ae4b can add the positional map safely. If the
positional handle is later promoted forward, it should **replace** the ULID marker
in the `RenderMCP` branch (one render change) and land with its store method
together — not be split across phases. **Decision for ae4a: keep the citation
ULID; defer the positional shortcut to ae4b** (recorded in D-142).

## Glossary additions

- **Lean MCP read (rendered read body)** — the model-facing markdown body
  `retrieval.RenderReadBody` produces for a retrieval response, carried in the MCP
  `Text` block and the HTTP/SDK `rendered` field. Shrinks the model's context, not
  the wire payload (both the body and the full structured result travel — M4).
- **Episode hook** — the `[episode:<id>]` marker the `RenderMCP` body appends to an
  item when `store.Memory.EpisodeID != ""`; sourced from already-loaded data, no
  new store query (D-142).
- **Drill handle** — the per-item `[cite:<ULID>]` marker in the `RenderMCP` body;
  equal to the item's existing citation ULID (`MemoryItem.Citation`), so
  `memory_drilldown` reuses the existing citation→verbatim path with no new store
  code (D-142). (The positional short handle is deferred to ae4b.)

## Decisions filed

- **D-142** — Lean MCP read: `memory_retrieve` `Text` becomes ae3's
  `Render(RenderMCP, …)` body via a single `retrieval.RenderReadBody` helper (reads
  `RenderResult.ContextBlock`, reconciling ae3's `.Body` prose); the episode hook
  is sourced from the already-loaded `Memory.EpisodeID` with no new store query; the
  drill handle equals the existing citation ULID so drill-down reuses the existing
  path with zero new store code (no `(response_id, rank)` method — deferred to
  ae4b); the same body is exposed on HTTP/SDK as `rendered` for parity; the token
  win is **model-context only, not wire size** (M4). Records the open positional-
  handle tradeoff as deferred to ae4b.

## As-built deviations

- **The AC5 "byte-for-byte over one fixture" parity test compares NORMALIZED
  live-surface output, not three calls to `RenderReadBody` over one literal Go
  fixture.** `MemoryItem.Citation` is a fresh ULID minted inside
  `Retriever.Retrieve` (`ulid.Make()`), so three independent live retrieves —
  one per surface — can never be literally byte-identical even though all three
  call the identical `RenderReadBody`. `TestRetrieveLeanRead_SurfaceParity`
  (`test/integration/retrieve_lean_read_test.go`) exercises the real MCP, HTTP,
  and embedded-SDK code paths end to end (proving the wiring, not just the pure
  function), then normalizes each body by substituting its own citation value
  with a placeholder before the byte comparison — the one field that is
  legitimately unique per response. `internal/retrieval/render_test.go` and
  `internal/mcpserver/handlers_test.go` separately pin the pure-function and
  single-surface byte-equality claims (`TestRenderReadBody_ComposesMapperAndRender`,
  `TestHandlerRetrieve_TextEqualsRenderReadBody`) with a literal fixture, so the
  "one core function" claim is still proven exactly, just split across two test
  layers instead of one.
- **`test/integration` gains its first postgres-gated subtests.** No prior file
  in this package exercised the `postgres` driver; `retrieve_lean_read_test.go`
  extends the established `STOWAGE_TEST_PG_DSN` env-gate from
  `internal/store/pgstore/pgstore_test.go` (SKIP, not fail, when unset) rather
  than inventing a new gating scheme, and blank-imports `internal/store/pgstore`
  for driver registration. Postgres subtests use a ULID-suffixed tenant per run
  instead of `TRUNCATE` (P3 tenant scoping already isolates repeated runs
  against a persistent instance).
- **The store-spy (AC2) wraps `store.Store` via interface embedding** —
  `countingStore` (`internal/mcpserver/handlers_test.go`) embeds the real
  `store.Store` and overrides only `Injections()`/`Episodes()`/`Memories()` to
  count calls, rather than hand-implementing the full interface. This was not
  specified at the plan's code-sketch level but is a mechanical, structural
  proof of the same claim the plan describes (RenderReadBody's `Store`-free
  signature).
