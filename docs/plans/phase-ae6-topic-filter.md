# Phase ae6 — request-level topic filter (own-scope, fail-open, lane-aware)

- **Status:** implemented (see "As-built deviations" below)
- **Owning subsystem(s):** `internal/retrieval` (a new `filterByTopicOwnScope` + the candidate-window widening); the three retrieve surfaces (`internal/mcpserver`, `internal/api`, `sdk/stowage`); `internal/config` (one knob). **No new table** — reuses `memory_topics` (migration 0011, D-089) and the scope-required `Store.MemoriesTopics` batch reader.
- **RFC sections:** §4.2 (lanes/fusion), §5.3 (scopes — P3), §5.4 (topics), §9.5 (one logic core, D-067/D-073)
- **Depends on phases:** the shipped retrieval lanes/scoring path; the topics phase (`memory_topics` + `MemoriesTopics`, D-089). No in-track dependency (Wave 0). **ae1 and ae9 reuse this filter** — it is the single own-scope fail-open mechanism the read-time agent filter and the topic views are layered on.
- **Informing briefs:** 03 (Engram — topics as extraction magnets; the memory→topic association shape), 04 (CL-Bench — retrieval failure modes / the gain metric the no-underfill AC protects), 06 (mempalace — gateway-free retrieval, D-036; the filter is a store read, never a gateway call).

## Goal

When this phase is done a caller can pass an optional **own-scope topic
include/exclude** on retrieve and get back only memories tagged with an included
topic (and none tagged with an excluded one), **scoped to the caller** (P3, via
`MemoriesTopics`), **without underfilling** the result `limit`, and the filter
**fails open** — a topic-store error returns the caller's own *unfiltered* results
with a degraded marker, the deliberate opposite of grants' fail-closed
`filterByTopic` (D-139). The filter is a pure store read (serves gateway-free,
D-036). It is the read-path mechanism ae1 (read-time agent filter) and ae9 (topic
views) reuse — built once, here.

## Brief findings incorporated

- **03 (Engram):** topics are the extraction-magnet association already modelled by
  `memory_topics` (D-089); ae6 slices on that existing junction rather than inventing
  a tagging scheme.
- **04 (CL-Bench):** underfilling a relevance-truncated pool is a silent recall
  failure that the gain metric punishes; the no-underfill AC + the pre-trim filter
  placement directly defend it.
- **06 (mempalace):** retrieval must serve gateway-free; the topic filter is a store
  membership read with no gateway call, so it works in the D-036 degraded path.

## Findings I'm departing from

- The charter offered two no-underfill remedies — **push the topic predicate into
  each lane's candidate query** vs **widen `scoringK`** — and left the choice to the
  author. **I pin a third, cleaner placement that subsumes both:** filter the **fused
  candidate IDs** with a discrete `MemoriesTopics` call **after RRF fusion but
  *before* the `scoringK` trim** (`retrieval.go:504`→`508`), so the filter runs over
  the **laneK-wide** pool (up to 200), not the truncated one — plus a
  `retrieval.topic_filter_scoring_k` knob that widens the candidate window when a
  topic filter is active. Rationale recorded in D-144:
  1. **Fail-open needs a discrete topic read to fail** (D-139). Lane-pushdown folds
     the topic predicate into each lane's SQL, so a topic-membership failure is
     indistinguishable from a lane failure and cannot cleanly "fall back to
     unfiltered." A discrete `MemoriesTopics` call can — exactly like grants', with
     the error branch inverted.
  2. **Portability.** Lane-pushdown would touch 3 SQL lane builders × 2 drivers **and**
     the `vindex` seam (the vector lane is `Scan`-then-cosine-in-Go, not a ranking-time
     JOIN); the discrete filter touches neither.
  3. **No underfill, honestly bounded.** Filtering the laneK-wide fused pool before the
     `scoringK` trim gives a far larger on-topic window than filtering the trimmed pool;
     the knob widens it further. (The absolute "≥limit on-topic ⇒ no underfill"
     guarantee holds *within the candidate window*; see AC-2.)

## Design

### Surface arg (additive)

Add to `retrieval.Request` (`internal/retrieval/retrieval.go`):

```go
// IncludeTopics keeps only memories tagged with ≥1 of these topic keys (empty = no
// include constraint). ExcludeTopics drops any memory tagged with one of these.
// Own-scope only: this NARROWS the caller's own results; it never widens scope (P3).
IncludeTopics []string
ExcludeTopics []string
```

Mirror on all three input structs with `omitempty` (matching the `include_lanes`
precedent): `RetrieveInput` (`internal/mcpserver/contracts.go`), `retrieveRequest`
(`internal/api/retrieve_handler.go` — note `DisallowUnknownFields`, so the field
must exist there), `RetrieveRequest` (`sdk/stowage/types.go`); thread into
`retrieval.Request{}` at all three call sites (`mcpserver/handlers.go`,
`api/retrieve_handler.go`, `sdk/stowage/embedded.go`; `sdk/stowage/http.go` rides the
JSON tags). JSON keys: `include_topics`, `exclude_topics`.

### The core filter (distinct from grants', error path inverted)

New file `internal/retrieval/topicfilter.go`:

```go
// filterByTopicOwnScope keeps only the caller's OWN-scope candidates whose
// memory_topics membership satisfies include/exclude, via the scope-required
// MemoriesTopics batch read. FAILS OPEN (D-139): on a topic-store error it returns
// the input unchanged with degraded=true — the deliberate opposite of grants'
// fail-CLOSED filterByTopic (which returns nil to never over-share across a grant).
// This is a DISTINCT function; the divergent error semantics are intentional.
func (r *Retriever) filterByTopicOwnScope(
    ctx context.Context, scope identity.Scope, ids []string, include, exclude []string,
) (kept []string, degraded bool)
```

- Operates on **candidate IDs** (no `GetMany` needed — `MemoriesTopics` takes IDs).
- One `MemoriesTopics(ctx, scope, ids)` call → `map[id][]topicKey`. Keep `id` iff
  (`include` empty **or** its topics intersect `include`) **and** its topics do not
  intersect `exclude`.
- **Fail-open:** `MemoriesTopics` error ⇒ log a warning, return `(ids, true)` — the
  full unfiltered candidate set, degraded. (Grants returns `nil`; ae6 returns input.)
- **P3:** `MemoriesTopics` is tenant-scoped and `ids` are already the caller's
  own-scope candidates, so the filter can only **subtract**; it opens no unscoped path.

### Placement in `Retrieve` (the no-underfill point)

1. **Candidate-window widening** — in the K-resolution block
   (`retrieval.go:332-344`), when `len(req.IncludeTopics)+len(req.ExcludeTopics) > 0`,
   floor `scoringK` up to `cfg.TopicFilterScoringK` (which floors `laneK` up via the
   existing `scoringK > laneK` rule), so the lanes pull a wider pool for a filtered
   query.
2. **Filter the fused pool before the trim** — immediately after `fused := rrf(lanes)`
   (`:504`) and **before** `fused = fused[:scoringK]` (`:508`): when a topic filter is
   present, project `fused`→IDs, call `filterByTopicOwnScope`, keep only surviving
   fused entries (order preserved), and set `resp.DegradedTopicFilter` from the
   returned `degraded`. Then the existing `scoringK` trim, `GetMany`, scoring, and
   `limit` trim run unchanged over the already-on-topic pool.

This filters the **laneK-wide** pool (≤200), not the `scoringK`-trimmed pool (≤50),
so on-topic candidates have a large window before any truncation.

### Degraded marker (additive output, parity)

Add `DegradedTopicFilter bool` to `retrieval.Response` and mirror it on the three
output types (next to the existing `Degraded` / `DegradedRerank`, which are already
surfaced) so a caller learns the topic filter did not apply (fail-open transparency).

## Files added or changed

```text
internal/retrieval/topicfilter.go        # NEW — filterByTopicOwnScope (fail-open)
internal/retrieval/topicfilter_test.go   # NEW — unit: include/exclude/both/empty/fail-open
internal/retrieval/retrieval.go          # CHANGED — Request{IncludeTopics,ExcludeTopics}; Response{DegradedTopicFilter}; widen + pre-trim filter wiring
internal/config/config.go                # CHANGED — retrieval.topic_filter_scoring_k (field, allKeys, get/set, validate)
internal/mcpserver/contracts.go          # CHANGED — RetrieveInput{include_topics,exclude_topics}; RetrieveOutput{degraded_topic_filter}
internal/mcpserver/handlers.go           # CHANGED — thread the args + marker
internal/mcpserver/testdata/memory_retrieve.{input,output}.schema.json  # CHANGED — regen
internal/api/retrieve_handler.go         # CHANGED — retrieveRequest/retrieveResponse + threading
sdk/stowage/types.go                     # CHANGED — RetrieveRequest/RetrieveResponse fields
sdk/stowage/embedded.go                  # CHANGED — thread args (in-process call site)
scripts/smoke/phase-ae6.sh               # NEW
docs/decisions.md                        # CHANGED — D-144 (+ D-139 filed here, first fail-open filter)
docs/glossary.md                         # CHANGED — own-scope topic filter, fail-open
test/integration/retrieve_topicfilter_test.go  # NEW — real-driver no-underfill + fail-open (§17)
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `retrieval.topic_filter_scoring_k` | `100` | Candidate window (floors `scoringK`, and `laneK` via the existing rule) used **only when a topic filter is active**, so the fused pool is wide enough to hold ≥`limit` on-topic candidates. Flat scalar on `RetrievalConfig` (sibling to `include_superseded`) — it is a cross-cutting behaviour value, not a per-profile window. D-034-complete: tuned default, present in every profile's effective config, docs, `allKeys`/get/set/explain, validation (`>0`, `≤ maxLimit`-class ceiling). Inert when no topic filter is passed, so zero-config behaviour is unchanged. |

## Acceptance criteria (binding)

1. **Own-scope, P3.** With `include_topics`/`exclude_topics`, results contain only the
   caller's own-scope memories whose `memory_topics` membership satisfies the predicate
   (via `MemoriesTopics`); no cross-scope row ever appears (store-layer scope query
   unchanged — the filter only subtracts).
2. **No underfill (the H3 core AC).** When ≥`limit` on-topic memories exist in scope
   **within the candidate window**, the result has `limit` items. A regression test
   **fails** the naive "filter the `scoringK`-trimmed pool" approach and **passes** the
   pinned "filter the fused (laneK-wide) pool before the trim, with the widened window"
   approach, using a fixture where on-topic memories rank below the default `scoringK`.
3. **Fail-OPEN (D-139).** A `MemoriesTopics` error returns the caller's own *unfiltered*
   results with `DegradedTopicFilter=true` — proven by fault injection — and is
   explicitly the **opposite** of grants' fail-closed `filterByTopic` (which returns
   `nil`). `filterByTopicOwnScope` is a **distinct** function (grep asserts both exist).
4. **Additive.** A request with no topic args is byte-identical to today (regression);
   `include_topics=[]`/`exclude_topics=[]` is a pass-through.
5. **Parity {SDK, HTTP, MCP}.** The include/exclude args and the `DegradedTopicFilter`
   marker exist on all three surfaces with a parity test; the MCP schema goldens are
   regenerated.
6. **Knob D-034-complete.** `retrieval.topic_filter_scoring_k` ships with default, every
   profile placement, docs, `allKeys`/get/set/explain, validation, and a smoke check;
   zero-config (no topic arg) behaviour unchanged.
7. **Gateway-free (D-036).** The filter performs no gateway call; it serves in the
   degraded path.

## Smoke script

`scripts/smoke/phase-ae6.sh` — SKIPs until built; then:
- `internal/retrieval/topicfilter.go` defines `filterByTopicOwnScope`, distinct from
  grants' `filterByTopic` (both grep-present).
- `retrieval.topic_filter_scoring_k` is registered (`stowage config explain` / `get`).
- `include_topics`/`exclude_topics` present in all three input contracts; the MCP schema
  golden carries them.
- `go test ./internal/retrieval/ -run TopicFilter` and the parity test pass.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Unit (`topicfilter_test.go`):** include-only, exclude-only, both, empty pass-through,
  no-match → empty, and **fail-open** (injected `MemoriesTopics` error ⇒ input returned,
  `degraded=true`). Contrast assertion vs grants' `filterByTopic` returning `nil`.
- **Integration (`test/integration/`, real drivers, §17 — ae6 consumes the topics seam
  D-089 + the retrieval lanes):** the **no-underfill** fixture (on-topic memories ranked
  below default `scoringK`, prove `limit` filled) on **both** sqlite + postgres; the
  **fail-open** path with a forced topic-store error; identity/scope propagation (a topic
  filter never returns another scope's row); `-race`.
- **Parity test:** include/exclude + `DegradedTopicFilter` across {SDK, HTTP, MCP}.
- **Regression:** no-topic-arg retrieval byte-identical; `TestEvalCI` unmoved.

## Risks & mitigations

- **Underfill (H3, the core risk).** Mitigated by filtering the laneK-wide fused pool
  *before* the `scoringK` trim + the widening knob; AC-2's regression test pins it and
  documents the window bound (honest, not an absolute claim past the window).
- **Copying grants' fail-closed semantics by accident.** Mitigated by a **distinct**
  `filterByTopicOwnScope` function + D-139 + the glossary entry recording the
  intentional opposite error path; a unit test asserts the divergence.
- **Knob default too low ⇒ underfill on sparse topics.** Default `100` (≥ balanced
  laneK; covers the realistic case) with validation; operators tune per deployment.
- **`DisallowUnknownFields` 400s.** The HTTP `retrieveRequest` must carry the new fields
  or external callers passing them get rejected — covered by the parity + a 200 test.

## Glossary additions

- **Own-scope topic filter** — `retrieval.filterByTopicOwnScope`: an optional
  include/exclude on retrieve that narrows the caller's **own-scope** results to topic-
  tagged memories via `memory_topics`/`MemoriesTopics`. **Fails open** (returns the
  unfiltered own-scope results, `DegradedTopicFilter=true`, on a topic-store error) —
  the deliberate opposite of grants' fail-closed `filterByTopic`. A curation/relevance
  lens, **not** a P3 isolation boundary (D-139). Reused by ae1 and ae9.
- **`DegradedTopicFilter`** — a retrieve-response marker that the topic filter could not
  be applied (topic-store error) and unfiltered own-scope results were returned (D-036
  fail-open transparency).

## As-built deviations

- **The Retriever carries a built-in `defaultTopicFilterScoringK = 100` fallback,
  not just the config-supplied value.** The plan's config knob (default `100`)
  covers the production boot path (`internal/boot/boot.go` calls
  `WithTopicFilterScoringK(cfg.Retrieval.TopicFilterScoringK)`), but a `Retriever`
  built directly via `New`/`NewWithInjections` without that call (unit tests, or
  any future caller that skips config wiring) would otherwise widen by `0` —
  silently defeating the no-underfill guarantee. `Retrieve` now falls back to the
  same tuned default (`100`) when `topicFilterScoringK <= 0`. Purely additive
  safety net; the config knob is still the single source of truth for production.
- **Topic-filtered requests bypass the result cache (`ResultCache`).** The cache
  key (`Get`/`Put` in `retrieval.go`) does not carry `IncludeTopics`/`ExcludeTopics`.
  Rather than widen the key (more cache-key surface, more invalidation paths), a
  request with a topic filter is excluded from both the cache read and write —
  mirroring the existing `!req.Debug && !multiScope` bypass precedent exactly (one
  more `&& !hasTopicFilter(req)` clause on both gates). Undocumented in the
  original design section but necessary for correctness: without it, two requests
  differing only in topic filter (same scope/querySig/profile/session/window/
  kinds/includeLanes/limit) could serve each other's cached result set.
- **Test-function naming uses a `TopicFilter*`/`Test*_TopicFilter*` convention**
  (e.g. `TestTopicFilterOwnScope_IncludeOnly`, not `TestFilterByTopicOwnScope_...`)
  so `go test ./internal/retrieval/... -run TopicFilter` (the smoke script's and
  this PR's own verification command) catches every new unit test, not just the
  three whose names happened to contain "TopicFilter". A purely cosmetic choice,
  called out because it deviates from mirroring the private function name
  (`filterByTopicOwnScope`) verbatim.
- **The AC-2 no-underfill regression is proven twice, not once.** The plan's Test
  plan section lists the no-underfill fixture under Integration only; this PR adds
  a second, self-contained proof at the `internal/retrieval` package level
  (`TestTopicFilter_NoUnderfill_PinsPreTrimPlacement`) that independently replays
  the REJECTED naive "filter the scoringK-trimmed pool" approach over the same raw
  lane data (via `retrieval.ExportRRF`) and asserts it would yield zero on-topic
  survivors, before asserting the real (pinned) `Retrieve` call fills `limit`. This
  makes the contrast the plan describes ("must FAIL the naive approach and PASS
  the pinned approach") an executable assertion, not just a design narrative.
- **The integration fail-open test covers HTTP + MCP, not the embedded SDK.** SDK
  parity for the SUCCESS path (no-underfill, exclude, additive no-op) is proven on
  all three surfaces. The FAIL-OPEN fault-injection sub-test
  (`TestTopicFilter_FailsOpen_HTTPAndMCP`) covers HTTP and MCP only: the embedded
  SDK's `stowage.NewEmbedded` always constructs its own `*retrieval.Retriever`
  inside `boot.Open` with no exposed seam to swap in a fault-injecting
  `store.MemoryStore` from a test. The fail-open `Retrieve` behavior itself
  (the load-bearing correctness property) is proven directly and unconditionally
  at the `internal/retrieval` package level
  (`TestRetrieve_TopicFilterFailsOpen_DegradedMarker`), so this is a coverage gap
  in surface-wiring redundancy only, not in the property being tested.

## Decisions filed

- **D-139** — Topic-views/filters are curation, not isolation; the agent/topic filter
  **fails open** (returns the caller's own memories on a topic-store error), deliberately
  opposite to grants' fail-closed `filterByTopic`. (Wave-0 decision; first implemented
  here as the own-scope filter ae1/ae9 reuse.)
- **D-144** — Own-scope topic filter placed **after RRF fusion, before the `scoringK`
  trim**, via a discrete `MemoriesTopics` read (fail-open), with the
  `retrieval.topic_filter_scoring_k` widening knob — chosen over lane-pushdown because a
  discrete read is required for clean fail-open and is portable across all four lanes
  (the vector lane is not a ranking-time SQL JOIN).
