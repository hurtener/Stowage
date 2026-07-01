# Phase ae5 — list / browse (most-recent-first, superseded filter)

- **Status:** draft
- **Owning subsystem(s):** `internal/store` (a new scope-required `MemoryStore.ListByScopeRecent` + both drivers + conformance); `internal/retrieval` (a new gateway-free `Browse` core); the three single-user read surfaces (`internal/mcpserver`, `internal/api`, `sdk/stowage`); `internal/config` (one knob).
- **RFC sections:** §5.2 (memories), §5.3 (scopes — P3), §8.1 (Store seam / schema inventory), §9.1–9.5 (tiered surfaces, one logic core D-067/D-073)
- **Depends on phases:** the shipped memories store (`MemoryStore.Insert`/`ListByStatus`, Phase 08); the status/supersede lifecycle (the `"superseded"` status this filter reads, Phase 14/§6c). **No in-track dependency** — lands in Wave 0.
- **Informing briefs:** 02 (CC-memory predecessor — the API/MCP **surface-sprawl** cautionary tale → one logic core, thin surfaces, not a list method per surface; the scoring/lifecycle model that makes "most-recent-first" and "superseded" the two useful walk axes), 01 (Python predecessor — store contention lessons → **keyset** pagination, never `OFFSET`), 04 (CL-Bench — retrieval/recall failure modes; a stable, gap-free page is what a walk-the-memory workflow needs).

## Goal

When this phase is done a caller can **walk a scope's memories** on all three
single-user surfaces (SDK, HTTP, MCP): a `mode=recent` browse returns the scope's
memories **most-recent-first** (`created_at DESC`) with a stable inverted-keyset
cursor, and a `mode=superseded` browse returns the scope's **superseded** memories
by **reusing the existing `ListByStatus(scope,"superseded",…)`** — **no new
superseded query is added** (H4). Both modes are **scope-required** (P3 — tenant
mandatory, no unscoped variant), both are served by **one `Browse` core** in
`internal/retrieval` with thin surfaces, and both are **gateway-free** (D-036 — pure
store reads). The one new persistence concern — the `created_at DESC` ordered scan —
lands as a single new `MemoryStore.ListByScopeRecent` method implemented by **both**
drivers and proven by the shared conformance suite; **no new table, no migration**
(the `memories` table and its `created_at` column already exist, §8.1). One new
config knob, `retrieval.browse_default_limit` (default `30`, bounded), governs the
page size when the caller omits `limit`.

## Brief findings incorporated

- **02 (CC-memory):** surface sprawl is a named predecessor failure → the browse
  capability is **one core** (`retrieval.Browse`) with thin SDK/HTTP/MCP callers, not
  three hand-rolled list handlers (D-067/D-073). Its scoring/lifecycle model is why
  the two walk axes worth shipping are recency (`created_at DESC`) and the
  superseded/stale set — no other filter earns a mode here.
- **01 (Python predecessor):** store-contention lessons → paginate by a **composite
  keyset** (`(created_at,id)`), never `LIMIT/OFFSET` (which re-scans and skips/dupes
  rows under concurrent inserts). ae5 reuses the exact `encodeCursor`/`parseCursor`
  helpers and the DESC keyset already proven by `ListEpisodes`.
- **04 (CL-Bench):** a walk-the-memory workflow depends on a **gap-free, dup-free**
  page sequence; the conformance ordering + full-sweep keyset test protects it.

## Findings I'm departing from

The charter stub (`track-adoption-ergonomics.md` §327–346) is the only ae5 spec on
disk — there is no prior `phase-ae5-browse.md`. Two of its framings collide with
code truth; both are resolved here and filed in **D-143** so the divergence is
explicit, not silent (the ae3/ae6 discipline).

- **The "superseded reuses `ListByStatus`" AC is ordering-asymmetric with the recent
  browse — deliberately kept.** `ListByStatus` orders `created_at, id` **ASC** in
  **both** drivers (`sqlitestore/memories.go:153`, `pgstore/memories.go:166`) and
  cannot be made DESC without a new query — which H4 explicitly forbids ("no new
  superseded query"). So `mode=recent` is newest-first and `mode=superseded` is
  **oldest-first**. I do **not** paper over this by re-sorting a page in the core:
  reversing a keyset page in memory breaks stable pagination (the `next_cursor`
  contract). The asymmetry is the accepted cost of the H4 simplification, documented
  on the tool/handler and pinned in D-143. (Reaching parity would mean a new
  `created_at DESC` status query — the exact thing H4 rules out.)
- **Scope semantics: the recent browse uses PREFIX (`buildScopeWhere`), not
  EXACT-leaf.** The charter says only "scope-required"; a reading of it as EXACT-leaf
  (`buildExactScopeWhere`, à la `ListActiveInScope`) would make `mode=recent` and
  `mode=superseded` **disagree** — `ListByStatus` uses PREFIX/wildcard
  (`buildScopeWhere`), so with an empty leaf dimension the two modes would return
  different row sets. I pin **PREFIX for both** (`ListByScopeRecent` uses
  `buildScopeWhere`, identical to `ListByStatus` and to the retrieval read lanes), so
  the two modes are scope-consistent and browse matches what a caller already sees on
  retrieve. PREFIX still **fails closed on an empty tenant** (`ErrScopeRequired`), so
  P3 holds — tenant is mandatory; the sub-tenant dimensions narrow. (D-143.)

## Design

### 1. The store seam — `ListByScopeRecent` (the one new persistence concern)

Add to `MemoryStore` (`internal/store/store.go`, next to `ListByStatus:230`):

```go
// ListByScopeRecent returns the scope's memories ordered by (created_at, id)
// DESCENDING — most-recent-first — paginated by an opaque "<millis>:<id>" cursor
// (the inverted keyset: rows strictly BEFORE the cursor, (created_at,id) < cursor).
// cursor "" is the first page. Scope-required (P3): tenant is mandatory via
// buildScopeWhere and there is NO unscoped variant. PREFIX/wildcard scope, matching
// ListByStatus and the retrieval read lanes. Mirrors ListEpisodes' DESC keyset on
// the memories table (ae5, D-143).
ListByScopeRecent(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]Memory, string, error)
```

**SQLite** (`internal/store/sqlitestore/memories.go`) — mirror `ListEpisodes`
(`episodes.go:92`) exactly, on the `memories` table:

```go
whereClause, args, err := buildScopeWhere(scope)      // tenant-required, PREFIX
// ... if cursor != "": parseCursor → ErrBadCursor on malformed
whereClause += " AND (created_at < ? OR (created_at = ? AND id < ?))"
args = append(args, ts, ts, cid)
args = append(args, limit+1)
q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause +
     ` ORDER BY created_at DESC, id DESC LIMIT ?`
// nextCursor = encodeCursor(out[limit-1].CreatedAt, out[limit-1].ID) when len(out) > limit
```

**Postgres** (`internal/store/pgstore/memories.go`) — row-value keyset, DESC:

```go
whereClause, args, next, err := buildScopeWhere(scope, 1)
// ... cursor: whereClause += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, next, next+1)
q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause +
     fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, next)
```

Both reuse the driver's existing `encodeCursor`/`parseCursor` (`cursor.go:13/20`,
identical `"<millis>:<id>"` scheme) and `scanMemory`. **No schema change** —
`memories.created_at` (unix millis, `types.go:23`) already exists and is the sort
key; §8.1 inventory is untouched.

### 2. The `Browse` core (`internal/retrieval/browse.go`, NEW)

Modeled on `episodes.List` (`internal/episodes/view.go:198`): an **LLM-free,
gateway-free** free function that dispatches the two modes to scope-required store
queries. It imports no gateway and constructs no provider request (P5 trivially;
D-036 trivially — it serves in the degraded path).

```go
// BrowseMode selects which ordered scan Browse runs. It is a two-value argument,
// NOT a config knob (mirrors ae3's RenderMode discipline).
type BrowseMode int

const (
    BrowseRecent     BrowseMode = iota // most-recent-first, via Store.ListByScopeRecent (created_at DESC)
    BrowseSuperseded                    // superseded memories, via the EXISTING Store.ListByStatus (created_at ASC — H4)
)

// browseMaxLimit is the hard page cap (resource guard, mirrors episodes' maxLimit).
const browseMaxLimit = 100

// BrowseOptions parameterises a page. DefaultLimit is the config-resolved page size
// used when Limit <= 0 (the surface passes cfg.Retrieval.BrowseDefaultLimit, so the
// knob lives in ONE core call, not three surfaces).
type BrowseOptions struct {
    Mode         BrowseMode
    Limit        int
    Cursor       string
    DefaultLimit int
}

// BrowseResult is one page of memories + the opaque next cursor ("" = last page).
type BrowseResult struct {
    Memories   []store.Memory
    NextCursor string
}

// Browse walks a scope's memories. Scope-required (P3): it delegates only to
// scope-required store queries; there is no unscoped path. Gateway-free (D-036).
func Browse(ctx context.Context, st store.Store, scope identity.Scope, opts BrowseOptions) (BrowseResult, error) {
    limit := opts.Limit
    if limit <= 0 {
        limit = opts.DefaultLimit
    }
    if limit <= 0 || limit > browseMaxLimit {
        limit = browseMaxLimit // clamp (also floors a mis-set/zero default)
    }
    switch opts.Mode {
    case BrowseSuperseded:
        // H4: REUSE the existing status query verbatim — no new superseded method.
        mems, next, err := st.Memories().ListByStatus(ctx, scope, "superseded", limit, opts.Cursor)
        // ... wrap error, return BrowseResult{mems, next}
    default: // BrowseRecent
        mems, next, err := st.Memories().ListByScopeRecent(ctx, scope, limit, opts.Cursor)
        // ...
    }
}
```

`internal/retrieval` is the charter-mandated home (§329); `Browse` touches no
`Retriever` receiver state, so it is a pure package function (no config import — the
knob value arrives via `BrowseOptions.DefaultLimit`).

### 3. Surfaces (parity, single-user read tier {SDK, HTTP, MCP})

All three are thin callers of `retrieval.Browse`; each resolves tenant from the
verified credential and the sub-tenant dims from its args (the existing
`project_id`/`user_id` D-125 mechanism — unchanged by this phase).

- **MCP** (`internal/mcpserver`): register `memory_browse` in `server.go` `New()`
  (via `tool.New[BrowseInput,BrowseOutput]("memory_browse")…`, mirroring
  `memory_episodes:97`); handler `makeBrowseHandler` in `handlers.go` (mirrors
  `makeEpisodesHandler:938` — `svc.ScopeFn(ctx)` for tenant, then
  `scope = identity.Scope{Tenant: scope.Tenant, Project: in.ProjectID, User: in.UserID}`);
  `BrowseInput`/`BrowseOutput`/`BrowseMemoryItem` in `contracts.go`; the tool `Describe`
  text states the **ordering asymmetry** (recent = newest-first; superseded =
  oldest-first, `ListByStatus` reuse). Regenerate the schema goldens under
  `internal/mcpserver/testdata/`.
- **HTTP** (`internal/api`): `GET /v1/memories` route in `server.go` (beside
  `GET /v1/memories/{id}:199`); `handleBrowseMemories` in `memories_handler.go`
  (mirrors `handleEpisodes:38`) reading `?mode=`, `?limit=`, `?cursor=`,
  `?project_id=`, `?user_id=`; scope via `scopeFromRequest`; envelope
  `{memories:[…], next_cursor}`.
- **SDK** (`sdk/stowage`): add `Browse(ctx, BrowseRequest) (BrowseResponse, error)`
  to the `Client` interface (`client.go:10`); `embedded.go` calls `retrieval.Browse`
  in-process; `http.go` issues `GET /v1/memories`; `BrowseRequest`/`BrowseResponse`/
  `BrowseMemory` in `types.go`. `BrowseRequest.Mode` is the string `"recent"` (default)
  / `"superseded"`, mapped to `BrowseMode` in the core-facing layer (a **closed
  enum**, not free-text model output parsing — §10 does not apply, this is a client
  arg).

Mode is a **closed string enum** on the wire (`"recent"`|`"superseded"`, default
`"recent"` when empty); an unknown value is rejected `4xx`/error (never silently
treated as recent).

### 4. Config knob

`retrieval.browse_default_limit` — a flat scalar on `RetrievalConfig`
(`internal/config/config.go:63`), sibling to `include_superseded`. Wired in the
five canonical places: the struct field, `allKeys` (`:228`), `Defaults()` (`:330`,
`= 30`), the `get` switch (`:802`), the `set` switch (`:931`), and `Validate` (`> 0`
and `≤ browseMaxLimit` = 100). Regenerate `internal/config/testdata/explain_default.golden`.

### Error handling & concurrency

- Malformed cursor → `store.ErrBadCursor` from `parseCursor` (surfaced as a `4xx` on
  HTTP/MCP, an error on SDK). Empty tenant → `store.ErrScopeRequired` (fail closed).
- `Browse` is a pure function; the store drivers are already concurrency-safe
  (proven by the conformance/`-race` suite). A concurrent-reuse test covers `Browse`.

## Files added or changed

```text
internal/store/store.go                                   # CHANGED — MemoryStore.ListByScopeRecent on the seam
internal/store/sqlitestore/memories.go                    # CHANGED — ListByScopeRecent (DESC inverted keyset, buildScopeWhere)
internal/store/pgstore/memories.go                        # CHANGED — ListByScopeRecent (row-value DESC keyset)
internal/store/conformance/conformance.go                 # CHANGED — MemoryListByScopeRecent (+ ordering/keyset/scope/tie/bad-cursor subtests)
internal/retrieval/browse.go                              # NEW — BrowseMode, BrowseOptions, BrowseResult, Browse()
internal/retrieval/browse_test.go                         # NEW — unit: recent/superseded dispatch, limit-default/clamp, concurrent-reuse (-race)
internal/mcpserver/server.go                              # CHANGED — register memory_browse
internal/mcpserver/handlers.go                            # CHANGED — makeBrowseHandler
internal/mcpserver/contracts.go                           # CHANGED — BrowseInput/BrowseOutput/BrowseMemoryItem
internal/mcpserver/testdata/memory_browse.{input,output}.schema.json  # NEW — regen goldens
internal/api/server.go                                    # CHANGED — GET /v1/memories route
internal/api/memories_handler.go                          # CHANGED — handleBrowseMemories + wire envelope
sdk/stowage/client.go                                     # CHANGED — Client.Browse
sdk/stowage/types.go                                      # CHANGED — BrowseRequest/BrowseResponse/BrowseMemory
sdk/stowage/embedded.go                                   # CHANGED — Browse via retrieval.Browse (in-process)
sdk/stowage/http.go                                       # CHANGED — Browse via GET /v1/memories
internal/config/config.go                                 # CHANGED — retrieval.browse_default_limit (field, allKeys, defaults, get/set, validate)
internal/config/testdata/explain_default.golden           # CHANGED — regen (new key)
scripts/smoke/phase-ae5.sh                                # NEW
test/integration/browse_test.go                           # NEW — real-driver keyset sweep + scope isolation + parity (§17)
docs/plans/README.md                                      # CHANGED — ae* track registration (draft line for ae5)
docs/decisions.md                                         # CHANGED — D-143
docs/glossary.md                                          # CHANGED — browse, inverted keyset
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `retrieval.browse_default_limit` | `30` | Page size for a `Browse` call that omits `limit`. Flat scalar on `RetrievalConfig` (sibling to `include_superseded`) — a cross-cutting behaviour value, not a per-profile candidate window, so its tuned default is **present in every profile's effective config** via `Defaults()` (profiles inherit it; no per-profile override needed). D-034-complete: tuned default, `allKeys`/get/set/explain wiring, docs, validation (`> 0`, `≤ 100` hard cap), and a smoke check — all in this PR. Inert for callers that pass an explicit `limit`, so **zero-config start is unchanged**. |

## Acceptance criteria (binding)

1. **DESC inverted keyset, both drivers, conformance.** `MemoryStore.ListByScopeRecent`
   returns the scope's memories ordered `created_at DESC, id DESC`; a full keyset
   sweep (`cursor="" → next → …`) visits **every** row exactly once with **no gap and
   no duplicate**, including a `created_at` **tie** (two rows same millis) — proven by
   `conformance.MemoryListByScopeRecent` running against **both** sqlite and postgres.
2. **Scope-required, no unscoped variant (P3).** `ListByScopeRecent` fails closed with
   `store.ErrScopeRequired` on an empty tenant; a cross-tenant / cross-user row **never**
   appears in another scope's page (conformance isolation subtest). No unscoped list
   method is added to the seam.
3. **Superseded reuses `ListByStatus` — no new superseded query (H4).** `Browse`'s
   `BrowseSuperseded` path calls the **existing** `st.Memories().ListByStatus(scope,
   "superseded", limit, cursor)`; a grep/AST assertion confirms **no** new
   `ListSuperseded`/`ListByScopeSuperseded`/status-scoped-DESC method exists on the seam
   or in either driver. The ordering asymmetry (recent DESC vs superseded ASC) is stated
   on the surface docs and filed in D-143.
4. **One core, thin surfaces, parity {SDK, HTTP, MCP}.** The capability is implemented
   once in `retrieval.Browse`; SDK/HTTP/MCP are thin callers with **no** private browse
   logic. A parity test asserts the same `(scope, mode, limit, cursor)` yields the same
   memory ids + `next_cursor` across all three surfaces (MCP included).
5. **Gateway-free (D-036).** `Browse` performs no gateway call (grep: no gateway import
   in `browse.go`); browse serves with the gateway unreachable.
6. **Knob D-034-complete.** `retrieval.browse_default_limit` ships with default `30`,
   `allKeys`/get/set/explain wiring (the internal `get`/`set` switches in `config.go`;
   no new `config get` CLI subcommand), the regenerated `explain_default.golden`, docs,
   validation (`> 0`, `≤ 100`), and a smoke check that reads the key back via
   `stowage config explain` (the only config CLI surface — it iterates `allKeys`, so the
   newly-registered key appears with default `30`, origin `default`); a `limit`-omitting
   browse returns `30` items when ≥30 exist, an explicit `limit` overrides, and
   `limit > 100` is clamped to 100. Zero-config start unchanged.
7. **Closed mode enum.** `mode` accepts only `"recent"` (default) and `"superseded"`;
   an unknown value is rejected, never silently defaulted.
8. **Cursor errors.** A malformed cursor returns `store.ErrBadCursor` (surfaced as a
   `4xx`/error), not a panic or a silent first page.

## Smoke script

`scripts/smoke/phase-ae5.sh` — SKIPs gracefully until each surface exists; then:
- `internal/store/store.go` declares `ListByScopeRecent`; both drivers implement it.
- `conformance.go` registers `MemoryListByScopeRecent`.
- **no new superseded query** — grep asserts no `ListSuperseded`/`ListByScopeSuperseded`
  on the seam or drivers; `browse.go` references `ListByStatus`.
- `internal/retrieval/browse.go` defines `Browse` + `BrowseMode`; no gateway import.
- `retrieval.browse_default_limit` is registered (`stowage config explain | grep retrieval.browse_default_limit` shows default `30`, origin `default`).
- `GET /v1/memories` route present; `memory_browse` tool present; SDK `Browse` present.
- `go test ./internal/store/... -run 'ListByScopeRecent'`, `go test ./internal/retrieval/ -run Browse`, and the parity test pass.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Conformance (`internal/store/conformance`, both drivers — §9/§11):**
  `MemoryListByScopeRecent` — DESC ordering; full keyset sweep (no gap/dup);
  `created_at` tie handled by the `id` tiebreak; scope isolation
  (cross-tenant/cross-user); empty-tenant → `ErrScopeRequired`; malformed cursor →
  `ErrBadCursor`; empty scope page.
- **Unit (`internal/retrieval/browse_test.go`):** mode dispatch (recent →
  `ListByScopeRecent`, superseded → `ListByStatus"superseded"`); default-limit applied
  when `Limit<=0`; clamp at `browseMaxLimit`; unknown-mode rejection; **concurrent-reuse**
  of `Browse` from N goroutines under `-race` (§5).
- **Integration (`test/integration/browse_test.go`, real drivers, §17 — ae5 consumes
  the store seam and closes a new public interface across three surfaces):** a
  multi-page recent sweep on **both** sqlite + postgres proving the surfaces return the
  same gap-free ordered sequence; a scope-isolation case (another user's memories never
  appear); the superseded mode returns only `"superseded"` rows; ≥1 **failure mode**
  (bad cursor → error, no panic); `-race`.
- **Parity test:** SDK/HTTP/MCP return identical ids + `next_cursor` for a fixed
  `(scope, mode, limit, cursor)`.
- **Config (`internal/config`):** default is `30`; override + validation (`0`/`101`
  rejected); `explain_default.golden` regenerated.
- **Regression:** MCP schema goldens regenerated; existing memory/episode surfaces
  unchanged.
- **No new fuzz target** — the cursor parser (`parseCursor`) is unchanged and already
  covered; note this in the PR.

## Risks & mitigations

- **Keyset-inversion off-by-one / unstable paging (the core risk).** The DESC keyset
  is `(created_at,id) < cursor` with `next = last-row cursor` — copied verbatim from
  the proven `ListEpisodes`. AC-1's full-sweep + `created_at`-tie conformance test on
  both drivers is the gate; a tie test specifically catches the classic "drop rows
  that share a timestamp" bug.
- **Temptation to build a `created_at DESC` superseded query.** Forbidden by H4 and
  AC-3 (grep gate + D-143). The cost — superseded browse is oldest-first — is
  documented on the surface, not hidden.
- **Scope-semantics drift between modes.** Mitigated by pinning **PREFIX** for both
  (`ListByScopeRecent` uses `buildScopeWhere`, matching `ListByStatus`); D-143 records
  the choice so a later "make it EXACT-leaf" edit re-opens the decision, not the code.
- **Unbounded page / resource exhaustion.** `browseMaxLimit` (100) clamps both an
  explicit `limit` and a mis-set default; AC-6.
- **`explain_default.golden` staleness failing CI.** Regenerated in the same PR (a
  standard new-knob step); the smoke check reads the key back.

## Glossary additions

- **Browse** — `retrieval.Browse`: the LLM-free, gateway-free scoped walk over a
  scope's memories, on {SDK, HTTP, MCP}. Two modes: `recent` (`created_at DESC`, via
  `Store.ListByScopeRecent`) and `superseded` (via the existing
  `Store.ListByStatus(scope,"superseded",…)` — `created_at ASC`, H4). Distinct from
  **retrieve** (relevance-ranked, gateway-embedded): browse is deterministic,
  order-by-time, and needs no query text.
- **Inverted keyset** — the `created_at DESC, id DESC` pagination scheme whose cursor
  selects rows **strictly before** the last row (`(created_at,id) < cursor`), the
  descending mirror of the ascending `(created_at,id) > cursor` keyset used by
  `ListByStatus`. Stable under concurrent inserts (unlike `LIMIT/OFFSET`).

## Decisions filed

- **D-143** — Most-recent-first browse via a new scope-required
  `Store.ListByScopeRecent` (`created_at DESC` inverted keyset, PREFIX scope, both
  drivers + conformance); the superseded filter **reuses the existing
  `ListByStatus(scope,"superseded",…)` — no new superseded query** (H4). Records the two
  charter-vs-code departures: (a) the deliberate ordering asymmetry (recent DESC,
  superseded ASC) accepted as the cost of the H4 reuse, and (b) PREFIX (not EXACT-leaf)
  scope for `ListByScopeRecent`, chosen so both browse modes are scope-consistent.
