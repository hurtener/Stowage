# Phase 22 — Episodes & narratives (RFC §6b)

- **Status:** implemented (see "As-built deviations")
- **Owning subsystem(s):** new `internal/episodes`, `internal/store` (+ `EpisodeStore`
  seam + `RecordStore.DistinctSessions` + episode indexes), `internal/lifecycle`
  (detect + narrate sweeps), `internal/boot` (wire the sweeps)
- **RFC sections:** §6b (episodes: boundary detection + narrative construction with
  full provenance; `episode_id` day-one; detection/narration are lifecycle sweeps),
  §5.0/§8.1 (day-one episodes table), §7/§10/P5 (gateway seam, schema-constrained)
- **Depends on phases:** 03 (store + day-one episodes table), 05 (records/occurred_at),
  08 (reconcile/commit + provenance), 14 (lifecycle sweeps), 19 (sweep + gateway
  precedent)
- **Informing briefs:** 06 (mempalace — narrative/temporal positioning), 02
  (LoCoMo — episodic/temporal structure)

## Goal

When this phase is done, a scope's verbatim records are grouped into **episodes**
(coherent temporal units) by a heuristic **boundary-detection** lifecycle sweep, and
a **narration** sweep constructs a `narrative` memory per episode via the gateway —
the concrete path of decisions taken, with full provenance to the episode's records
and `episode_id` linking memory↔episode. Episodes carry id·scope·title·time-range·
status·narrative_memory_id·outcome (the day-one §8.1 shape). Both passes are
supervised lifecycle sweeps (P2); episodic *retrieval* (`GET /v1/episodes`, contrast,
aggregation) is Phase 23.

## Brief findings incorporated

- **RFC §6b / OQ-8:** boundary detection is **heuristic-first** — session structure +
  temporal gaps, no LLM for the boundary decision (the gateway is used only for the
  narrative text). Cheap, deterministic, debuggable; an LLM boundary refiner is a
  later option.
- **P1 provenance:** the narrative memory carries provenance spans to the episode's
  verbatim records — drill-down from narrative → records holds.

## Findings I'm departing from

- **RFC §6b lists episodes, temporal queries, causal links, contrast, aggregation
  together.** Phase 22 ships **only episodes + narratives** (the write/detection
  side); episodic retrieval/contrast/aggregation is Phase 23 and causal links is
  Phase 24 (per the roadmap). No departure from intent — a scope cut.
- **Records are immutable (P1) — they carry no `episode_id`.** So episode↔record
  membership is derived (an episode owns its session's records by time range), and
  detection idempotency comes from "**an episode already exists for this (scope,
  session)**", not from stamping records. Filed in **D-079**.

## Design

### Heuristic boundary detection (v1, OQ-8): one closed session → one episode

The simplest defensible heuristic: a **closed session** (no new records for an idle
window) is one episode; a large intra-session **temporal gap** splits it. v1 ships
the session-as-episode rule with an optional gap split; an LLM/topic-shift refiner is
a documented follow-up.

### Store seam additions

`RecordStore.DistinctSessions(ctx, scope, idleBefore int64, limit int) ([]SessionInfo, error)`
— distinct `(session_id, branch_id)` with `min/max(occurred_at)` and record count,
for sessions whose latest record is older than `idleBefore` (i.e. closed).
Scope-parameterized (P3). Both drivers + conformance.

New `EpisodeStore` seam (interface + sqlite + pgx + conformance):
- `CreateEpisode(ctx, scope, Episode) error`
- `GetEpisodeBySession(ctx, scope, sessionID string) (*Episode, error)` — idempotency gate
- `ListEpisodesNeedingNarrative(ctx, limit int) ([]Episode, error)` — unscoped scan (narration sweep), like `ListUnprocessed`
- `SetEpisodeNarrative(ctx, scope, episodeID, narrativeMemoryID, title string) error`
- `ListEpisodes(ctx, scope, limit, cursor) ([]Episode, string, error)` — for Phase-23 reuse

Forward-only migration: `idx_episodes_tenant_session` (idempotency lookups) and
`idx_episodes_narrative_pending` (partial, `WHERE narrative_memory_id=''`) for the
narration scan. The episodes table itself is day-one (§8.1) — index-only addition.

### `internal/episodes`

- `DetectEpisodes(sessions []store.SessionInfo, gapMs int64) []EpisodeDraft` — pure
  heuristic: one episode per session, split on an intra-session gap > `gapMs`. Pure
  + table-tested.
- Narrative prompt + schema (`{title, narrative}`, draft-07, schema-constrained §10)
  + `Narrate(ctx, gw, episode, records) (title, narrative string, provenance, error)`
  through `gateway.Gateway` (P5). The narrative content is grounded in the records;
  provenance spans reference them (P1).

### Lifecycle sweeps (`internal/lifecycle/episodes.go`)

Two sweeps following the Phase-19 reflection pattern (advisory locks `0x1407`
detect, `0x1408` narrate; jittered; profile-tunable; off by default except where
enabled):
1. **Detect:** per tenant scope, `DistinctSessions(idleBefore=now−idle)` → for each
   session without an episode (`GetEpisodeBySession`), create episode(s) via
   `DetectEpisodes` (status `closed`, time range, outcome from records).
2. **Narrate:** `ListEpisodesNeedingNarrative` → load records, `Narrate` via gateway,
   commit a `narrative` memory (kind `narrative`, `EpisodeID`=episode.id, provenance,
   `TrustSource:"episodic"`), then `SetEpisodeNarrative`. Idempotent: an episode with
   a narrative is skipped; re-narration would dedupe on content hash.

### Wiring + config

`boot.StartPipeline` registers the sweeps via the lifecycle Manager (a
`SetEpisodes(gw)` setter mirroring `SetReflection`); enabled per profile
(`config.EpisodeConfigForProfile` — like reflection, on for fleet/assistant where
episodic memory is useful, tunable interval/idle/gap). Zero-config start unaffected
(no episodes unless enabled). The narration sweep's gateway calls are metered/evented
like all gateway calls.

## Files added or changed

```text
internal/store/store.go                 # + EpisodeStore seam + RecordStore.DistinctSessions + SessionInfo
internal/store/types.go                 # Episode, SessionInfo types
internal/store/sqlitestore/*.go         # impls
internal/store/pgstore/*.go             # impls
internal/store/migrations/*/0008_*.sql  # episode indexes
internal/store/conformance/*.go         # EpisodeStore + DistinctSessions conformance
internal/episodes/detect.go             # heuristic boundary detection (pure)
internal/episodes/narrate.go            # narrative prompt + schema + Narrate (gateway)
internal/episodes/*_test.go             # detection tables + narrate golden (fakeGateway)
internal/lifecycle/episodes.go          # detect + narrate sweeps; Manager.SetEpisodes
internal/lifecycle/manager.go           # register; Profile episode knobs
internal/config/profiles.go             # EpisodeConfigForProfile
internal/boot/pipeline.go               # wire SetEpisodes when enabled
eval/harness/server.go                  # mirror wiring iff a new constructor is added (parity)
test/integration/episodes_loop_test.go  # records → detect → narrate → episode+narrative
scripts/smoke/phase-22.sh
docs/plans/phase-22-episodes.md ; docs/decisions.md (D-079) ; docs/glossary.md
```

## Config keys added

Profile-internal (`EpisodeConfigForProfile`, like reflection/buffers — not top-level
`config explain` knobs): `{Enabled, DetectInterval, NarrateInterval, IdleWindow,
GapSplit}`. Default enabled where episodic memory helps (assistant, fleet), off for
coding-agent or as tuned. Zero-config start does no episode work unless enabled.

## Acceptance criteria (binding)

1. `EpisodeStore` + `RecordStore.DistinctSessions` exist on both drivers, scope-
   parameterized (P3), pass the shared conformance suite under `-race`; new indexes
   used.
2. Boundary detection is **heuristic + pure** (no gateway): `DetectEpisodes` is
   table-tested (session→episode, gap split); the detect sweep creates one episode
   per closed session, idempotently (re-run creates no duplicate — `GetEpisodeBySession`).
3. Narration is gateway-side + schema-constrained (§10) through `gateway.Gateway`
   (P5); the narrative memory is kind `narrative`, carries `episode_id` + provenance
   to the episode's records (P1); golden prompt/schema test (fakeGateway, no live call).
4. Idempotency: detect twice → no duplicate episodes; narrate twice → no duplicate
   narrative memory (content-hash/skip).
5. Episodes carry the §8.1 shape (title, time range, status, narrative_memory_id,
   outcome); status transitions open→closed are covered.
6. Episode work is profile-gated + off by zero-config default; the gateway narration
   calls are metered/evented.
7. **Integration test** (real sqlite + mock gateway): ingest a session's records →
   force detect → force narrate → assert an episode exists with a `narrative` memory
   linked (`episode_id` + provenance); scope-isolated; ≥1 failure mode (gateway error
   on narration → episode stays un-narrated, retried next sweep, no partial commit).
8. Gates: build, `go test -race ./...`, golangci-lint, gofmt, `make coverage`,
   preflight, drift-audit, mirror green; parity test green.

## Smoke script

`scripts/smoke/phase-22.sh` (SKIP-graceful): `internal/episodes` routes through the
gateway seam (P5) + schema-constrained narrate (§10); detect is gateway-free (grep);
EpisodeStore + DistinctSessions on both drivers; detect/narrate sweeps registered;
episode profile-gating unit test; episodes unit + integration tests; eval-ci green.

## Test plan

- Unit/table: `DetectEpisodes` (session→episode, gap split, empty); narrate prompt +
  schema golden (fakeGateway).
- Conformance: EpisodeStore (create/get-by-session/list-needing-narrative/set-narrative/
  list, scope isolation) + DistinctSessions (idle filter, ordering, scope) under `-race`.
- Idempotency: detect/narrate twice.
- Integration (§17): the episode loop (real store + mock gateway, ≥1 failure mode).

## Risks & mitigations

- **Session-as-episode is coarse** → ship the gap split; document the LLM/topic-shift
  refiner as a follow-up; episodic retrieval (Phase 23) works on whatever granularity.
- **Re-narration cost/dup** → narration skips episodes that already have a narrative;
  content-hash dedups a forced re-run.
- **Open sessions** → detect only closed sessions (idle window) so an in-progress
  session isn't prematurely episode-d.
- **Gateway error on narrate** → episode stays un-narrated, retried next sweep; no
  partial episode/memory (commit is transactional).
- **Cost** → profile-gated + jittered intervals + idle window bound the work.

## Glossary additions

- **Episode** — a coherent temporal unit of records (one closed session, v1),
  carrying a title, time range, status, outcome, and a narrative memory (§6b).
- **Narrative memory** — the `narrative`-kind memory constructed per episode by the
  narration sweep: the concrete path of decisions, with provenance to the episode's
  records.
- **Boundary detection** — the heuristic lifecycle sweep grouping records into
  episodes (session structure + temporal gaps; no LLM — OQ-8).

## Decisions filed

- **D-079** — Episodes are detected by a heuristic, gateway-free boundary sweep
  (one closed session → one episode, intra-session gap split; OQ-8 heuristic-first);
  episode↔record membership is derived (records are immutable — no `episode_id` on
  records), and detection idempotency is "an episode already exists for this (scope,
  session)". Narration is a separate gateway sweep producing a schema-constrained
  `narrative` memory (kind `narrative`, `episode_id` + provenance, `TrustSource:
  "episodic"`). Episode work is profile-gated, off by zero-config default. Episodic
  retrieval is Phase 23; causal links Phase 24.

## As-built deviations (§4.3)

Adversarial review (pre-merge) surfaced two majors, both fixed; D-079 holds.

1. **Full-scope episodes (not tenant-only).** The initial cut detected/created
   episodes at tenant scope, dropping project/user — which made narratives invisible
   to user-scoped retrieval (Phase 23's consumer) and risked a cross-user merge when
   `session_id` collides within a tenant. Fixed: `RecordStore.DistinctSessions`
   carries `project_id`/`user_id` (grouped on the full identity), episodes are
   created at `{Tenant, Project, User}`, and narrative memories are committed at the
   owning user scope (retrievable across that user's sessions). P3-faithful.
2. **Idempotent narration recovery.** If a sweep committed the narrative memory but
   crashed/failed before linking it, the next sweep re-narrated → `ErrDuplicateContent`
   → previously skipped, stranding the episode forever and re-burning the gateway.
   Fixed: on `ErrDuplicateContent` the sweep looks the memory up by content hash
   (`GetByContentHash`) and links it — two identical narratives share one memory and
   both episodes are linked (proven by `TestEpisodeSweeps_DuplicateNarrativeRelinks`).
3. **Provenance spans are byte offsets** (`len(content)`), matching the rest of the
   codebase (drill-down indexes bytes), not rune counts (minor fix).

Also: gap-split detection across a partial-creation failure is best-effort
(`GapSplit` defaults to 0/off in v1); the LLM/topic-shift boundary refiner remains a
documented follow-up.
