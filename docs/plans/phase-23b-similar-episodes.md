# Phase 23b — Similar-episode contrast (RFC §6b)

- **Status:** implemented
- **Owning subsystem(s):** `internal/retrieval` (`SimilarNarratives`), `internal/episodes`
  (`Similar` view core), `internal/api` / `internal/mcpserver` / `sdk/stowage`
  (the `memory_episodes` surfaces), `test/integration`
- **RFC sections:** §6b (similar-episode contrast: "retrieve the most similar past
  episode … and contrast outcomes"), §4.2 (retrieval/vindex), §9.5/D-067 (one core,
  thin tiered surfaces), §7/§10/P5 (gateway seam)
- **Depends on phases:** 22 (episodes + narratives), 23 (`memory_episodes` surface),
  09 (retrieval/vindex), 04 (gateway embed)
- **Informing briefs:** 06 (mempalace — temporal/narrative positioning), 02 (LoCoMo)

## Goal

When this phase is done, `memory_episodes` gains a **`similar_to`** query: given a
situation (free text), it embeds the query and vector-searches the scope's
**narrative** memories (`vindex.Filter{Kinds:["narrative"]}`), returning the most
similar past episodes ranked by similarity — each carrying its outcome + narrative,
which is the §6b contrast material ("last time this happened, here is what worked").
Reuses the existing retrieval embed→vindex machinery; degrades gracefully (gateway
down ⇒ empty + `degraded` flag, never an error); and ships on all three single-user
surfaces with the byte-identical parity test (deterministic under the mock embedder).

## Findings I'm departing from

- **D-080 scoped 23b as "similar-episode contrast + gateway-synthesized summary."**
  This phase ships **similar-episode contrast only**; the **LLM window-synthesis is
  deferred** (recorded in D-082). Reasons: (a) the deterministic windowed list
  (Phase 23) already returns the structured cross-episode summary §6b mandates
  ("never a raw fragment dump"), so synthesis is the explicitly-*optional* §6b step;
  (b) it adds a `Complete`-call path of marginal value over the deterministic list,
  whose output the mock gateway can't make meaningfully parity-stable; (c) it should
  be pulled on a concrete use-case/eval signal (D-035), not shipped on spec. The
  similar-episode contrast is the high-value, well-defined piece and lands fully.

## Design

### Core — `Retriever.SimilarNarratives` (embed + kind-filtered vindex)

```go
// degraded=true (no error) when the gateway/vindex can't serve — callers fall back
// to the deterministic list. Returns parallel episode-id + score slices, ranked.
func (r *Retriever) SimilarNarratives(ctx, scope, query string, k int) (ids []string, scores []float64, degraded bool, err error)
```

Embeds the query (`r.gw.Embed`), `r.vi.Search(scope, vec, k, Filter{Kinds:["narrative"]})`,
loads each hit memory (`r.mem.Get`) to read its `EpisodeID`, returns the episode ids +
scores (skipping narratives with no episode link). Mirrors the Phase-9 degraded-mode
pattern (gateway down ⇒ `degraded`, not fatal). No new vindex/store surface — kind
filtering already exists (Phase 22 added `narrative` narratives as active, embedded by
the backfill sweep).

### View core — `episodes.Similar` (one core, all surfaces)

```go
type NarrativeSearcher interface {
    SimilarNarratives(ctx, scope, query string, k int) (ids []string, scores []float64, degraded bool, err error)
}
func Similar(ctx, st, searcher NarrativeSearcher, scope, query string, k int) (views []EpisodeView, degraded bool, err error)
```

Loads each episode (`GetEpisode`) + its narrative (preserving rank), stamps the
similarity `Score`. The `*retrieval.Retriever` satisfies `NarrativeSearcher`
structurally — no cross-package type sharing, no import cycle (retrieval does not
import episodes). All three surfaces call `episodes.Similar` with the Retriever they
already hold (D-067 one core).

### Surfaces (extend `memory_episodes`)

Input gains `similar_to string` + `k int`; output gains `degraded bool` and a per-
episode `score`. When `similar_to` is set the handler runs `episodes.Similar` (ranked
by similarity); otherwise the existing deterministic `List`. HTTP `?similar_to=&k=`;
MCP `EpisodesInput.SimilarTo/K`; SDK `EpisodesRequest.SimilarTo/K`. The api.Server,
mcpserver.Services, and SDK embedded all already hold the Retriever.

### Determinism / parity

The mock gateway's embeddings are deterministic, so a seeded store yields identical
vector rankings across embedded/HTTP/MCP — the byte-identical parity test extends to
the `similar_to` path. Degraded mode (no gateway) is unit-tested with a fake searcher.

## Files added or changed

```text
internal/retrieval/retrieval.go        # + SimilarNarratives (+ test)
internal/episodes/view.go              # + NarrativeSearcher + Similar (+ test)
internal/api/episodes_handler.go       # ?similar_to=&k= branch; score/degraded wire
internal/mcpserver/{contracts,handlers}.go  # EpisodesInput.SimilarTo/K; EpisodeItem.Score; degraded; golden
sdk/stowage/{types,http,embedded}.go   # EpisodesRequest.SimilarTo/K; Episode.Score; EpisodesResponse.Degraded
test/integration/episodes_parity_test.go  # similar_to parity leg
scripts/smoke/phase-23b.sh
docs/plans/phase-23b-similar-episodes.md ; docs/decisions.md (D-082) ; docs/glossary.md
```

## Config keys added

None — read-only; reuses the gateway already configured for retrieval.

## Acceptance criteria (binding)

1. `SimilarNarratives` embeds via the gateway seam (P5) and vector-searches
   kind=`narrative`; gateway/vindex failure ⇒ `degraded=true`, empty, **no error**
   (graceful degradation, D-036).
2. `episodes.Similar` is the single core all three surfaces call; the
   `memory_episodes` `similar_to` path returns episodes ranked by similarity with a
   `score`, each carrying outcome + narrative (the contrast material).
3. Parity: embedded == HTTP == MCP byte-identical for a seeded `similar_to` query
   (deterministic mock embedder).
4. The deterministic list/get/window path is unchanged and remains gateway-free.
5. New MCP input/output fields ⇒ schema golden regenerated (D-061); new params ⇒
   same-PR smoke (§4.2).
6. Gates: build, `go test -race ./...`, golangci-lint, gofmt, coverage, preflight,
   drift-audit, mirror green.

## Smoke script

`scripts/smoke/phase-23b.sh`: `SimilarNarratives` present + degraded-safe (unit);
`episodes.Similar` core present; `memory_episodes` exposes `similar_to` on all
surfaces; parity (incl. similar) passes; schema golden stable; eval-ci unaffected.

## Test plan

- Unit: `SimilarNarratives` (ranked results; degraded on nil gateway) via a seeded
  store + mock gateway; `episodes.Similar` (rank preserved, score stamped, missing
  episode skipped, degraded passthrough) via a fake searcher.
- Parity (§17): `similar_to` leg byte-identical across surfaces.

## Risks & mitigations

- **Embedding cost on every similar query** → bounded by `k`; only on the opt-in
  `similar_to` path; the default list path stays embedding-free.
- **Narrative not yet embedded** (backfill lag) → it simply won't rank; the list
  path still surfaces it. No error.
- **Degraded mode** → explicit `degraded` flag; callers fall back to the
  deterministic list.

## Glossary additions

- **Similar-episode contrast** — ranking the scope's past episodes by narrative-
  vector similarity to a situation, surfacing their outcomes for contrast (§6b,
  Phase 23b, D-082); the `memory_episodes` `similar_to` query.

## Decisions filed

- **D-082** — Similar-episode contrast ships as a `memory_episodes` `similar_to`
  query backed by `Retriever.SimilarNarratives` (gateway embed + kind=narrative
  vindex), degraded-safe, one `episodes.Similar` core across {SDK, HTTP, MCP} with
  byte-identical parity (deterministic mock embedder). The §6b **gateway-synthesized
  window summary is deferred** (the deterministic window list already serves the
  structured-summary need; synthesis is pulled on an eval/use-case signal).
