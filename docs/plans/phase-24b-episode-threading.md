# Phase 24b — Episode threading (cross-session arcs, D-081)

- **Status:** approved
- **Owning subsystem(s):** `internal/lifecycle` (a new gateway-free threading sweep),
  `internal/episodes` (`Arc` read core), `internal/api` / `internal/mcpserver` /
  `sdk/stowage` (the `memory_episodes` `arc_of` query), `internal/config` (profile
  tuning), `test/integration`
- **RFC sections:** §6b (episodic layer — the "arc" / living-episode unit),
  §5.6 (typed `relates_to` links), §9.5/D-067 (one core, thin tiered surfaces),
  §8.1 (schema budget), P4 (forgetting — derived, reversible)
- **Depends on phases:** 22 (episodes + narratives + the detect/narrate sweeps),
  23 (`memory_episodes` surface), 24 (the `links`-over-narratives pattern + the
  lifecycle-sweep-writes-links discipline)
- **Informing briefs:** 06 (mempalace — long-horizon narrative positioning), 02
  (ccmem — bounded idempotent sweeps), 04 (CL-Bench — cross-session reasoning)

## Goal

When this phase is done, Stowage groups a user's **session-episodes into cross-session
arcs** — "the billing-migration effort," "the Q1 outage" — the long-horizon unit a
user actually reasons about. A new **gateway-free** lifecycle sweep clusters recent
narrated episodes by **entity/keyword overlap ∧ temporal proximity ∧ (project,user)
continuity**, above a conservative threshold, and records the grouping as
`relates_to` edges between the episodes' **narrative memories** (the `links` table is
day-one and already carries `relates_to`; narratives are memories — **no new table or
column, no RFC amendment**). A new deterministic `episodes.Arc` read walks those edges
to return an episode's arc (all episodes threaded to it), exposed as a
`memory_episodes` **`arc_of`** query across {SDK, HTTP, MCP} (D-067) — **no new MCP
tool**, so no tool-count change. The sweep ships **off by default** (profile-internal
`ThreadingEnabled`): the *mechanism* lands now; broad *enablement* stays gated on an
episodic-eval win (D-081's discipline).

## Brief findings incorporated

- **06 (mempalace):** the arc is the long-horizon narrative unit; threading turns
  per-session summaries into a living episode — the differentiator vs flat RAG.
- **02 (ccmem):** the threading sweep is bounded (batch + window), advisory-locked,
  jittered, and idempotent — same discipline as detect/narrate.
- **04 (CL-Bench):** cross-session grouping targets the "what was I working on across
  these weeks" reasoning current memory systems miss.

## Findings I'm departing from

- **D-081 was PROPOSED, gated on an episodic-eval win.** This phase **ratifies D-081**
  but ships the threading **mechanism off by default** (`ThreadingEnabled=false` in
  every profile): the code lands and is testable, but it is not enabled in production
  profiles until the episodic eval shows a cross-session-QA/resumption win (D-035).
  This honours both the build directive and the eval-gate. Recorded in the ratified
  D-081.
- **D-081 fork 1 (edges vs container): edges win for v1.** We use `relates_to` edges
  between narrative memories (no new `arcs` table / `parent_id` → no RFC §8.1
  amendment), composing with the Phase-24 causal edges. A parent-arc entity is
  promoted only if an arc-level narrative/retrieval surface is later justified.
- **D-081 clustering signal: content WORD-SET Jaccard + temporal + scope (no vectors in
  v1).** Narrative memories carry no entity/keyword junctions (the narrate sweep writes
  content+provenance only), so the gateway-free signal is a **word-set Jaccard** over
  the narrative content's distinct content words (lowercased alphanumeric tokens ≥3
  runes) ∧ temporal window ∧ same (project,user). Word-set overlap is *topical* —
  unlike character-bigram Jaccard (reconcile's near-dup measure), which saturates on
  any two English prose strings regardless of subject. A `minThreadWords` floor guards
  empty/degenerate narratives (adversarial-review M1). The default `ThreadMinOverlap`
  (0.3 word-overlap) is a conservative eval-tunable placeholder (D-035); narrative-
  vector similarity is a future signal. Fully deterministic — no gateway dependency.

## Design

### Threading sweep — `internal/lifecycle` (gateway-free, off by default)

`runThreadEpisodes` (sibling to detect/narrate, advisory-locked
`episodeThreadLockKey`, jittered, gated by `ThreadingEnabled`):

1. Per tenant, list the most recent narrated episodes (`ListEpisodes` batch, filter to
   `NarrativeMemoryID != ""`), capped at `ThreadBatchSize`.
2. Group candidates by `(ProjectID, UserID)` — only same-owner episodes thread (P3).
3. For each candidate pair within `ThreadWindow` of each other (by `EndedAt`/`StartedAt`
   proximity), load each narrative's entities+keywords (`GetJunctions`) and compute
   set-overlap (Jaccard over the union of entities+keywords). If `>= ThreadMinOverlap`,
   record a `relates_to` edge between the two **narrative memory IDs**, canonicalized
   (from = lexicographically-smaller id) so the pair is order-independent.
4. Idempotent: skip a pair that already has a `relates_to` link (`ListLinks` check).
   Insert survivors via `InsertLinks` (`source="inferred"`), each with an
   `episode.threaded` audit event carrying the overlap score. Reversible: derived edges
   over immutable episodes/narratives — re-clustering never destroys.

> **Deviation (bar-remediation A7, D-093):** the candidate signal was extended from
> word-set Jaccard alone to `(wordJaccard ≥ ThreadMinOverlap) OR (narrative-embedding
> cosine ≥ 0.82)`. The semantic signal reads the already-stored narrative vectors
> (`VectorStore.Scan`, kind="narrative"), so the sweep stays gateway-free (D-081); it is
> degraded-safe (no vectors ⇒ lexical-only). The `episode.threaded` payload gains a
> `signal` field (`lexical|semantic|both`) so consumers can interpret the emitted
> `overlap` value.

Bounded: `ThreadBatchSize` episodes ⇒ O(n²) pairwise within the batch, n small
(default 50); the window + same-owner filters prune most pairs.

### Arc read core — `episodes.Arc` (deterministic, gateway-free)

```go
// Arc returns the episodes threaded to episodeID (its cross-session arc), including
// the seed, ordered most-recent-first. Walks relates_to edges between narrative
// memories (BFS, cycle-safe, capped), maps each connected narrative → its episode.
// Gateway-free. Missing/unarrated seed ⇒ just the seed (or empty if absent).
func Arc(ctx context.Context, st store.Store, scope identity.Scope, episodeID string) ([]EpisodeView, error)
```

Loads the seed episode → its `NarrativeMemoryID` → BFS over `relates_to` links
(`ListLinks(from,"")` + `ListLinks("",to)`, filter `type=relates_to`) collecting
connected narrative memory IDs (visited set; `maxArcNodes` cap) → each narrative's
`EpisodeID` → load those episodes (active narratives only) → `toViews`, most-recent
first. Reuses the existing `toView`. No gateway.

### Surfaces — extend `memory_episodes` with `arc_of` (no new tool)

Input gains `arc_of string`. When set, the handler runs `episodes.Arc(seed)` and
returns the arc as the existing `EpisodesResponse` (episodes list). HTTP
`?arc_of=<episode_id>`; MCP `EpisodesInput.ArcOf`; SDK `EpisodesRequest.ArcOf`. Reuses
the Phase-23 episodes envelope + parity harness; **no new MCP tool, no schema-golden
tool addition** (only the `memory_episodes` input gains a field → golden regenerated).
A missing/unthreaded seed ⇒ a one-element arc (just the seed), or empty for an absent
seed — parity across surfaces.

### Lifecycle of the produced edges (P4)

`relates_to` edges are derived, advisory, reversible. A superseded/quarantined/deleted
narrative is non-active → the arc read skips it (active-only), so a stale edge is
harmless; re-clustering re-derives. Hard delete (retention/DSAR) removes the narrative
and is responsible for its links (existing cascade concern). No decay on edges in v1.

## Files added or changed

```text
internal/lifecycle/threading.go        # runThreadEpisodes sweep (+ test) ; register in manager
internal/lifecycle/manager.go          # ThreadingEnabled/ThreadInterval/ThreadMinOverlap/ThreadWindow/ThreadBatchSize + sweep registration
internal/episodes/view.go              # + Arc (+ test)
internal/config/profiles.go            # EpisodeConfig.Threading* (off by default, all profiles)
internal/boot/pipeline.go              # map EpisodeConfig.Threading* → lifecycle.Profile
internal/api/episodes_handler.go       # ?arc_of= branch
internal/mcpserver/{contracts,handlers}.go  # EpisodesInput.ArcOf + handler branch ; golden regen
sdk/stowage/{types,http,embedded}.go   # EpisodesRequest.ArcOf
test/integration/episodes_parity_test.go  # arc_of parity leg
scripts/smoke/phase-24b.sh
docs/plans/phase-24b-episode-threading.md ; docs/decisions.md (ratify D-081) ; docs/glossary.md
```

## Config keys added

**None top-level.** Threading tuning is **profile-internal** (`config.EpisodeConfig`,
alongside the episode-sweep tuning), re-tuned by eval (D-035): `ThreadingEnabled`
(default **false** — eval-gated), `ThreadInterval` (30m), `ThreadMinOverlap` (0.3),
`ThreadWindow` (30 days), `ThreadBatchSize` (50). Not operator-facing top-level knobs
(consistent with episode tuning; D-034 ceremony N/A).

## Acceptance criteria (binding)

1. **Threading sweep (gateway-free, off by default):** with `ThreadingEnabled=true`,
   the sweep links narrative memories of same-(project,user) episodes within
   `ThreadWindow` whose entity/keyword overlap ≥ `ThreadMinOverlap` via `relates_to`
   (`source=inferred`) + an `episode.threaded` event; idempotent across reruns; never
   calls the gateway. With `ThreadingEnabled=false` (default) it does nothing.
2. **Arc read (deterministic):** `episodes.Arc` returns the seed's threaded episodes
   (cycle-safe, active-only, capped); `internal/episodes` imports no gateway.
3. **Tiered parity (D-067):** `memory_episodes` `arc_of` ships on {SDK, HTTP, MCP};
   a seeded arc is byte-identical across the three; missing/unthreaded seed ⇒ the seed
   alone (or empty for absent), identical on all three.
4. **Scope (P3):** threading and arc reads are scope-enforced; cross-(project,user)
   episodes never thread; cross-tenant never surfaces (tests).
5. **Schema/knob discipline:** no new table/column; threading tuning profile-internal;
   `memory_episodes` input field ⇒ schema golden regenerated; same-PR smoke (§4.2).
6. **D-081 ratified** in `docs/decisions.md` (mechanism shipped, enablement eval-gated).
7. **Gates:** build, `go test -race ./...`, golangci-lint, gofmt, coverage, preflight,
   drift-audit, mirror green.

## Smoke script

`scripts/smoke/phase-24b.sh`: threading sweep present + gateway-free (no
`internal/gateway` import in threading.go); `ThreadingEnabled` defaults false;
`episodes.Arc` present + gateway-free; `memory_episodes` exposes `arc_of` on all three
surfaces; sweep + arc unit tests + parity pass; MCP golden stable; `make eval-ci` green.

## Test plan

- **Unit — threading:** seeded narrated episodes (overlapping entities within window;
  non-overlapping; out-of-window; cross-user) → assert only the right pairs link,
  idempotent across two runs, disabled-by-default does nothing, never calls a gateway.
- **Unit — Arc:** a seeded `relates_to` chain of narratives → assert the arc returns
  all connected episodes, cycle-safe, active-only, scope-isolated, missing seed ⇒ empty.
- **Parity (§17):** `arc_of` byte-identical across embedded/HTTP/MCP over a seeded arc;
  missing-seed parity.

## Risks & mitigations

- **False merges (two unrelated efforts fused).** → conservative `ThreadMinOverlap`,
  same-(project,user) + temporal-window gates, off by default, reversible (derived
  edges). The eval tunes the threshold before enablement.
- **O(n²) pairwise cost.** → `ThreadBatchSize` cap + window + owner pruning; the sweep
  is async/bounded, never on a hot path.
- **Shipping a mechanism the eval hasn't justified.** → off by default; D-081 ratified
  as "mechanism shipped, enablement eval-gated," not "threading on in prod."

## Glossary additions

- **Arc (living episode)** — a cross-session group of episodes about the same effort,
  formed by `relates_to` edges between their narrative memories (Phase 24b, D-081).
- **Episode threading** — the gateway-free lifecycle sweep that clusters recent
  narrated episodes into arcs by entity/keyword overlap ∧ temporal proximity ∧
  (project,user) continuity (Phase 24b, D-081); off by default, eval-gated.

## Decisions filed

- **D-081 (ratified)** — Episode threading ships as a gateway-free lifecycle sweep
  that writes `relates_to` edges between episodes' narrative memories (no new schema),
  with an `episodes.Arc` read exposed as `memory_episodes` `arc_of` across {SDK, HTTP,
  MCP}. Clustering signal v1 = entity/keyword overlap ∧ temporal window ∧ (project,user);
  vector similarity is a future signal. The mechanism ships **off by default**; broad
  enablement is gated on an episodic-eval win (D-035). Edges over a container (no
  `arcs` table) per fork 1.
