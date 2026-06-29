# Stowage glossary

New terms land here in the same PR that introduces them (CLAUDE.md §14).

- **Record** — an immutable, append-only verbatim interaction (the fidelity
  layer, RFC §5.1). Never embedded wholesale, never mutated.
- **Memory** — a derived, structured abstraction extracted from records (RFC
  §5.2). Always carries provenance.
- **Provenance** — the `(record_id, span)` references linking a memory to the
  verbatim source it was derived from.
- **Drill-down** — expanding a retrieved memory to its verbatim source range
  (the CL-Bench recovery path).
- **Scope** — the identity quadruple `tenant/project/user/session` stamped on
  every record and memory; enforced at write and read in the store layer.
- **Privacy zone** — `public | work | personal | intimate`; gates promotion and
  export, not access (access is scope's job).
- **Topic** (extraction magnet) — a per-scope natural-language description of
  what is worth remembering; extraction only produces candidates that match an
  active topic.
- **Buffer** — a per-(scope, key) accumulator of ingested fragments; flushes to
  extraction on triggers (count, tokens, age, session end, explicit).
- **Candidate** — an extracted not-yet-committed memory awaiting reconciliation.
- **Reconciliation** — the constrained tool-call decision over a candidate and
  its retrieved neighbors: `add | update | merge | supersede | discard`.
- **Trust gate** — the rule that a high-trust memory cannot be silently
  superseded; the newcomer parks as `pending_confirmation` instead.
- **Trust source** — provenance class of a memory's content:
  `user_stated > agreed_upon > agent_suggested > llm_extracted`.
- **Utility counters** — the six independent counts on a memory: `match`,
  `inject`, `use`, `save`, `fail`, `noise`. Rank rises only with use/save.
- **Stability** — the decay time-constant of a memory; grows logarithmically
  with proven utility.
- **Hub dampening** — penalty for memories that match too many distinct query
  clusters (too generic to be useful).
- **Cooldown** — suppression window that stops a just-written memory from being
  retrieved straight back (write echo).
- **Quarantine** — excluding a memory or session from retrieval without
  deleting it.
- **Supersede chain** — the linked history `superseded_by`/`supersedes` across
  memory versions; walked with cycle detection and a hop cap.
- **Contradiction boost** — elevated importance/stability granted to a
  correction so it immediately outranks what it corrected.
- **Lane** — one concurrent retrieval strategy (lexical, vector,
  anticipated-queries, structured); lanes are fused with RRF.
- **Anticipated queries** — 3–5 search phrases generated at extraction time and
  indexed in their own lexical lane.
- **RRF** — reciprocal-rank fusion of lane results.
- **Retrieval profile** — a named retrieval preset (`precise` | `balanced` | `broad`)
  encoding the `{laneK, scoringK, defaultLimit, enableRerank}` tuple; selected per
  `/v1/retrieve` call and config-tunable via the `retrieval:` section (D-103). Distinct
  from the deployment **Profile** below.
- **Dual-visibility** — retrieval surfacing a superseded value alongside its current successor,
  flagged `stale` with a `superseded_by` link, so an agent reasons about a correction's history
  rather than losing it (RFC §6c calibrated uncertainty; `retrieval.include_superseded`, D-105).
- **ScoringK** — the number of fused candidates a retrieval profile scores/reranks; the
  cap on memories that can reach the reader. The per-request `limit` is floored up into
  this window, so a request is never silently clamped below what it asked for (D-103).
- **Gateway** — the single intelligence seam (`internal/gateway`) through which
  every embedding and LLM call flows; drivers: `bifrost`, `mock`.
- **Gain** — the performance delta an agent shows with memory on vs. off (the
  CL-Bench-derived release metric). Measured by the gain harness (Phase 20b,
  D-078): each scenario is run through the reader+judge once with retrieved memory
  context and once with none; `gain = quality_on − quality_off`. Mean gain ≥ 0 is
  an operator-run release gate.
- **Online adaptation** — measuring compounding improvement across a sequential
  task run as the reflection→playbook loop accumulates strategies between tasks
  (ACE; Phase 20b, reported not gated).
- **Memory-on / memory-off** — the two conditions of a gain run: the reader answers
  with retrieved memory context vs with none.
- **Sweep** — a scheduled in-process lifecycle job (decay, dedupe, rollup,
  re-enqueue); jittered, idempotent, singleflight.
- **Re-enqueue sweep** — crash recovery: records past their processing deadline
  re-enter the pipeline.
- **Dead letter** — a pipeline item that exhausted retries; persisted for
  inspection, never silently dropped.
- **Grant** — a store-layer-enforced share of a slice of an owner scope to a
  named group (team), with read or contribute access, a privacy-zone ceiling,
  optional `topic_filter`/`kind_filter` slicing (read grants only — enforced via
  `memory.kind` and the `memory_topics` association, D-089), and optional
  redaction (D-016).
- **memory→topic association** — the `memory_topics` junction linking a memory to
  the extraction topic(s) it pertains to, tagged by the extractor and validated
  against the scope's active topics; backs grant `topic_filter` (RFC §8.1, D-089).
- **Outcome** — the success/failure + execution-feedback tag a record or buffer
  flush can carry; the label-free fuel for reflection (D-018).
- **Reflection** — the outcome-aware extraction mode that distills `strategy`
  and `failure_mode` memories from trajectories (ACE's Reflector, brief 05). The
  write-side runs as a lifecycle sweep feeding the existing reconcile core
  (`internal/reflect`, Phase 19, D-077); it is the LLM-ful counterpart to the
  LLM-free playbook *assembly*.
- **Trajectory (reflection)** — outcome-tagged records grouped by `(session_id,
  branch_id)` with a terminal outcome, ordered by `occurred_at`; the unit a
  reflection pass reflects over (success/failure contrast).
- **Re-reflection** — the multi-epoch reflection sweep that revisits older
  trajectories as the playbook matures; idempotent via reconcile pre-filters +
  a per-scope watermark (D-077).
- **`llm_reflected`** — the trust source stamped on reflection-produced memories,
  distinguishing them from `llm_extracted` (topic extraction) (D-077).
- **Playbook** — the deterministic, sectioned, budget-packed context view over
  a scope's strategy/failure-mode memories (`GET /v1/playbook`); evolves only
  through delta reconciliation, never LLM rewrite (context-collapse defense).
- **Playbook assembly** — the deterministic, LLM-free `internal/playbook.Assemble`
  procedure (D-072): list active `strategy`/`failure_mode`/building-block memories
  in scope (`Store.ListByKinds`), rank within sections by the pure `internal/scoring`
  utility/decay functions with a stable ULID tiebreak, greedy budget-pack to the
  profile-internal token budget, and attach provenance. Append-biased / prefix-stable
  so a host re-fetch keeps its prompt cache warm (ACE's KV-hit property, RFC §6a.3).
- **Context collapse** — the degradation ACE documents when an accumulated
  context is monolithically rewritten by an LLM; the reason playbook assembly
  contains no LLM call.
- **Rollback** — reverting a reconciliation decision from its event, restoring
  prior state and tombstoning the result (D-017).
- **Embedded mode** — running the full Stowage server in-process via
  `sdk/stowage`, no daemon or network (D-022); e.g. inside a Wails desktop app.
- **Injection** — the recorded fact that a memory was handed to an agent for a
  given `response_id`, with rank and score; the attribution backbone (D-025).
- **Citation handle** — the injection ID an agent attaches to a claim;
  resolvable to the memory, its provenance, and metadata (§6c).
- **Support summary** — per-retrieval report of evidence strength and
  agreement/conflict among returned memories; lets agents say "I'm not sure".
- **Reasoning trace** — the reconstructable memory-into-conclusion chain for a
  response (query, injections, drill-downs, verification verdicts), exportable
  as a signed audit bundle.
- **Boundary detection** — the heuristic, gateway-free lifecycle sweep that
  groups records into episodes (one closed session → one episode, intra-session
  gap split; OQ-8 heuristic-first, Phase 22, D-079).
- **Episode** — a detected coherent temporal unit of records, with a generated
  narrative memory carrying full provenance (§6b).
- **Narrative** — the `narrative`-kind memory telling an episode's concrete
  path of decisions, not a vague summary.
- **Causal link** — a `caused_by`/`led_to` typed edge between decision
  memories, explicit or inferred through episode narratives.
- **Causal inference pass** — the once-per-episode, schema-constrained gateway step
  (run inside narration) that proposes confidence-scored `led_to` edges between an
  episode's decision memories, written `source="inferred"` (Phase 24, D-083).
- **Why-traversal** — the deterministic, gateway-free walk of the
  `caused_by`/`led_to` graph from a memory (backward to causes, forward to effects,
  or both) with provenance at every hop; the `memory_causal` capability (Phase 24,
  RFC §5.6/§6b, D-083).
- **Episodic retrieval** — reading episodes + their narratives through the
  `memory_episodes` capability (list / get / time-window), deterministic and
  LLM-free (Phase 23, D-080); the §6b read side over Phase-22 episodes.
- **Arc (living episode)** — a cross-session group of episodes about the same effort
  ("the billing migration"), formed by `relates_to` edges between their narrative
  memories; read via `memory_episodes` `arc_of` (Phase 24b, D-081).
- **Episode threading** — the gateway-free, off-by-default lifecycle sweep that
  clusters recent narrated episodes into arcs by (narrative content word-set overlap OR
  narrative-embedding cosine similarity) ∧ temporal proximity ∧ `(project,user)`
  continuity (Phase 24b, D-081; vector signal added in D-093). The semantic signal reads
  the already-stored narrative vectors (so the sweep stays gateway-free) and widens
  recall to same-arc episodes that share few literal words. Enablement is eval-gated.
- **Claim verification** — the schema-constrained gateway entailment check that a
  claim is supported by its cited memories; the `memory_verify` capability
  (`POST /v1/verify`), degraded-safe to "unclear" (Phase 25, §6c, D-084).
- **Review queue** — the scope-level hold for `pending_review` memories (uncited agent
  assertions): listed and approved (→active) or rejected (→quarantined) via
  `memory_review` (Phase 25, D-084).
- **Reasoning trace** — the read-only, per-`response_id` memory-into-conclusion chain
  (query, injected memories, drill-down spans, typed links, verification verdicts)
  reconstructed on demand from the day-one tables; exported as a signed bundle via
  `memory_trace` (Phase 26, §6c, D-086).
- **Trace bundle** — a reasoning trace plus an optional ed25519 detached signature +
  public key for third-party audit verification; unsigned when no signing key is
  configured (Phase 26, D-086).
- **Episode contrast** — surfacing the most similar past episode and comparing
  outcomes against the current situation.
- **Similar-episode contrast** — ranking the scope's past episodes by
  narrative-vector similarity to a situation (the `memory_episodes` `similar_to`
  query), surfacing each one's outcome + narrative as contrast material; backed by
  `Retriever.SimilarNarratives` (gateway embed + `kind=narrative` vindex),
  degraded-safe (Phase 23b, D-082).
- **Branch** — a session fork for exploration; working memories merge on
  accept or expire on discard, records always remain (D-029).
- **Hot–warm cache** — the (query-signature, scope) result cache plus
  injection-frequency hot set that serves frequent retrievals without a vector
  lookup (D-031).
- **Preference fragments** — `preference`-kind memories from the default
  personalization topic pack ("how this user wants to be answered").
- **Trigger (trigger class)** — a proactive-engine rule that proposes context
  for a session: `recent_episode` (the scope's most recent narrated episode,
  ended within the window), `similar_episode` (a past episode whose narrative
  resembles the query — gateway-backed, degraded-safe), or `expiring` (an active
  memory approaching its `valid_until`). Per-`(scope, class)` accept/dismiss
  tallies tune a confidence multiplier `[0.2, 1]` over the class's scores (§6d,
  D-087).
- **Suggestion (proactive offer)** — a context offer the trigger engine surfaces
  for a session, scored with the same `scoring.Score` machinery as retrieval and
  gated by the scope's governance threshold + budget. Persisted in the
  `suggestions` table with `accept_count`/`dismiss_count` (the feedback signal —
  NOT the six memory utility counters); `pending → accepted | dismissed | expired`.
  Pulled via `GET /v1/suggestions` / `memory_suggestions` (§6d, D-087).
- **Proactive governance** — a scope's effective proactive config (`enabled`,
  `threshold`, `budget`, `classes`): the profile default overlaid by the scope's
  stored `proactive` setting in `scope_settings`. Admin-tier read/write
  (`/v1/admin/proactive`, `memory_proactive_config`); opt-out is `enabled:false`
  (§6d, D-087).
- **Review queue** — `pending_review` candidates (e.g. uncited agent-generated
  claims) awaiting admin approve/reject before becoming memories.
- **Profile** — a named preset (`assistant` | `coding-agent` | `fleet`)
  bundling tuned knob values; the unit of configuration users actually touch
  (D-034).
- **Benchmark gate** — the CI rule (from Phase 13) that a regression on the
  public benchmark suite or the latency SLO blocks merge (D-035).
- **Degraded mode** — gateway-free retrieval over the lexical, anticipated-
  queries, and structured lanes when the provider is unreachable (D-036).
- **Temporal-proximity boost** — scoring input favoring candidates whose
  `occurred_at` is near the query's explicit or implied time window (brief 06).
- **Launch track** — phases 01–21 (v1.0): every differentiator plus the proof;
  post-launch tracks v1.1–v1.3 consume signals already captured (D-033).
- **The Python predecessor** — the internal Python memory server Stowage
  redesigns (its project name is not used in this repository; see D-001/D-003).
- **The CC-memory predecessor** — the internal Go memory system for coding
  agents whose lifecycle model Stowage adopts (brief 02).
- **Fast-add** — committing a no-neighbor candidate as an active memory without
  an LLM decision call (D-044). The common case for a fresh scope; eliminates
  the gateway round-trip when there is nothing to reconcile against.
- **Parked** — a memory in `pending_confirmation` status awaiting human review
  or Phase 15 resolution. Created by the trust gate when an incoming supersede/
  update targets a high-trust memory (score ≥ 3.0), or when the LLM explicitly
  decides `park`. The `supersedes_id` field records which active memory the
  parked candidate intends to replace.
- **Pack** (topic pack) — a compiled-in set of topic entries that can be enabled
  for a scope (D-043, composition added in D-099). Shipping packs: `pack:preferences`
  (personalisation, communication style, durable personal facts), `pack:agent-learnings`
  (gotchas, patterns, decisions), plus the curated `pack:project`, `pack:incidents`, …
  (D-099 backlog). Packs are virtual: never written to the topics table; they appear in
  `GET /v1/topics` with `source: pack:<name>`.
- **Pack composition** (D-099, amending D-043) — a scope's effective topics are the
  deduped **union** of its enabled packs and its explicit topics (explicit wins on key
  collision), capped at `maxActiveTopics`. The `profile` selects an ordered list of
  *default* packs that apply only when the scope has expressed no intent (no explicit
  topics, no enabled packs) — the zero-config path. Resolution lives in
  `topics.Service.ActiveTopics` so SDK/HTTP/MCP share it (D-067).
- **`pack:on:<name>`** (D-099) — a sentinel topic key that enables the compiled-in pack
  `<name>` at a scope, mirroring `pack:off`; the runtime, scope-aware composition lever
  (no YAML knob).
- **`pack:off`** — the sentinel topic key that opts a scope out of packs entirely and
  short-circuits extraction with no gateway call; dominates over enabled packs and
  explicit topics (D-043, unchanged by D-099).
- **Enriched text** — the string fed to the embedding gateway for a memory,
  formed by joining `content + entities + keywords + anticipated_queries` with
  spaces (D-047). Enriching beyond the raw content improves semantic recall
  for the vector lane without requiring schema changes; the FTS5/tsvector lanes
  already operate on content+context separately.
- **Backfill sweep** — a background job that scans for active memories missing
  a vector entry (`memory_vectors` row) and enqueues them for embedding (D-047).
  Runs once at startup (immediate pass) then on a jittered 5–7-minute ticker via
  `Embedder.BackfillSweep`, started by `boot.StartPipeline` on every live path
  (serve, mcp, embedded — D-068; previously serve-only). Provides crash-recovery
  for embed jobs dropped from the bounded channel or lost to gateway failures.
  Limit 64 per pass.
- **ActivityTurns** — the approximate count of records written to a tenant scope
  since the oldest `last_accessed_at` across the current retrieve result set
  (Phase 10). Used by the decay factor to normalise recency: a memory idle for
  5 turns in a quiet scope is less stale than one idle for 5 turns in a busy
  scope. Computed as a single `COUNT` query per retrieve call (not per item).
- **HubSignals** — the number of distinct query clusters (query signatures) that
  have returned a given memory within the recent window (30 days). Derived durably
  from the `query_sig` column on the `injections` table — `COUNT(DISTINCT query_sig)`
  — so the signal survives restart and is shared across processes (D-092, replacing
  the former per-process LRU). Memories with ≥ 4 distinct signals receive a 0.80×
  hub-dampening multiplier in the utility score to counteract generic "hub" content
  dominating results across unrelated queries.
- **Query signature (query_sig)** — a short, stable SHA-256-derived fingerprint of a
  retrieve query's sorted token set (`retrieval.QuerySig`). Two retrieves with the same
  tokens in any order share a signature, so they count as ONE query cluster for hub
  dampening and share a result-cache key. Persisted on each injection row (D-092).
- **SameSession** — true when the retrieve request's `session_id` matches the
  `session_id` of the memory's origin (the scope.Session value at INSERT time).
  Used by the write-echo cooldown: memories created in the current session within
  the last 30 minutes are suppressed (×0.1) to prevent the agent from immediately
  retrieving its own just-written output.
- **Write-echo cooldown** — a scoring factor (×0.1) applied to memories whose
  `created_at` is within the last 30 minutes AND whose origin session matches the
  current retrieve session (`SameSession=true`). Prevents the retrieval pipeline
  from surfacing a memory the agent just wrote, breaking short-term feedback loops
  (Phase 10).
- **Support summary** — a per-response block (`strength`, `top_score`,
  `conflicts`) that characterises the evidence quality of the retrieved set
  (Phase 10, RFC §4.2.5). `strength` is `"weak"`, `"moderate"`, or `"strong"`
  based on the top-3 score mass (thresholds 1.5 and 5.0). `conflicts` lists
  pairs of memory IDs connected by a `contradicts` link within the result set.
- **Utility score** — the final score assigned to a memory after RRF fusion is
  re-weighted by all 11 scoring factors (Phase 10). Replaces the raw RRF score as
  the sort key returned to callers. Computed by `scoring.Score(Inputs)`, which
  is a pure function with no side effects.
- **Trust multiplier** — the read-side scoring factor applied per `trust_source`
  at retrieve time (Phase 10, D-050). Distinct from the supersede-gate trust
  threshold (write-side). See D-050 for the full multiplier table and rationale.
- **Rerank** — a cross-encoder pass applied (precise profile only) to the top
  `rerankSlice` (24) Phase-10 candidates; scores are blended
  `0.6 × rerankNorm + 0.4 × phase10Norm` before final sort (D-052).
- **Rerank blend** — the weighted combination of cross-encoder relevance score
  and Phase-10 utility score: `rerankBlendRerank=0.6`, `rerankBlendScore=0.4`.
  Both are named constants in `internal/retrieval/rerank.go` (D-052).
- **degraded_rerank** — flag set to `true` in the retrieve response envelope when
  the rerank pass failed (network error, breaker open, etc.) and Phase-10 ordering
  was preserved instead (D-052 graceful degradation contract).
- **Generation counter** — a per-scope monotonic uint64 in the result cache;
  bumped O(1) by `InvalidateScope` on any content-changing reconcile commit.
  A cache entry is stale when its stored generation differs from the current
  scope counter (D-053).
- **ScopeInvalidator** — the narrow interface (`InvalidateScope(scope)`) defined
  in `internal/retrieval` that decouples the result cache from the reconcile
  stage without a circular import (D-053).
- **Hot set** — a per-scope LRU of memory IDs ranked by injection frequency
  (how many times a memory was returned in a retrieve response). v1: metrics-only;
  the retrieval fast-path pre-warm is deferred to Phase 13 (D-053).
- **SLO rig** — the standalone measurement harness in `internal/bench/slo/`
  (build tag `slo`) that seeds memories into postgres, fires 1 000 concurrent
  sessions, and reports p50/p95/p99 + cache hit rate. Results are recorded in
  `eval/SLO.md` (D-031, Phase 12).
- **Profiling harness** — the `internal/bench/profile/` load+profile rig (build
  tag `profile`, `make profile`) that boots full embedded stacks across the
  driver/store + entrypoint matrices, drives concurrent ingest/retrieve with sweeps
  running, captures CPU/heap/goroutine/block/mutex profiles, and asserts
  goroutine-stability + idle ceilings + a memory-footprint baseline. Sibling to the
  SLO rig — resource behaviour, not latency (D-126, Phase P1). Baselines in
  `eval/PROFILE.md`.
- **Profiling matrix** — the two grids the harness profiles: the **driver/store**
  matrix `{vindex: hnsw,brute} × {store: sqlite,postgres}` (Postgres on-demand via a
  DSN) and the **entrypoint** matrix `{embedded, serve, mcp}` — so a goroutine or
  memory leak in any driver, backend, or deployment shape is caught (D-126).
- **Goroutine-stability gate** — the post-boot / steady-state / post-drain
  `NumGoroutine` check per cell; `post-drain ≤ post-boot + ε` is the P2
  drain-on-shutdown contract made measurable. For the `serve` entrypoint it is a
  goroutine-climb-across-load-cycles check via the pprof endpoint (D-126).
- **Idle gate** — the zero-traffic ceiling check proving sweeps and tickers
  impose no polling tax at idle: deterministic alloc + goroutine-delta signals
  gate the always-on cut; the noisy idle CPU-time ceiling is on-demand
  (`make profile`) only (D-126).
- **Memory-footprint baseline** — the `HeapAlloc/HeapInuse/HeapSys/StackInuse/Sys`
  snapshot the rig records at post-boot / post-idle / steady-state / post-drain for
  each matrix cell (D-126, Phase P1). Goroutine deltas are environment-independent;
  absolute MiB are machine-specific.
- **Runtime sampler** — `telemetry.RuntimeSampler`: a lifecycle-managed ticker that
  logs a `runtime.sample` line (`NumGoroutine` + `MemStats`) at
  `telemetry.runtime_sample_interval`. The pull-independent complement to the
  GoCollector Prometheus gauges — no custom gauges, no event (D-126, Phase P1).
- **CI eval fixture** — a deterministic conversation + mock script pair in
  `eval/ci-fixtures/` used by the CI eval harness (Phase 13). Fixtures require
  no external network calls; the mock gateway serves scripted `Complete`
  responses from `STOWAGE_MOCK_SCRIPT`.
- **Benchmark gate** — the quality regression check in `eval/harness/gate.go`
  that compares a fresh CI eval run's `answer_context_hit` and latency percentiles
  against committed baselines in `eval/baselines/ci.json`. A regression fails the
  `make eval-ci` step in CI (Phase 13, D-055).
- **Gate-bite test** — `TestEvalCIGateBites` in `eval/harness/runner_test.go`;
  proves the benchmark gate detects a regression by running the harness twice
  (normal + degraded) and asserting the degraded run scores lower (AC-3, D-055).
- **answer_context_hit** — the deterministic CI eval metric: the fraction of
  questions where the expected answer appears in any retrieved item's content
  (case-insensitive, with number-word and either-direction normalization — Phase
  20). Measures retrieval recall across the extract → reconcile → retrieve
  pipeline (Phase 13). Distinct from `answer_quality`; never calls a model.
- **Reader (eval)** — the LLM that answers an eval question from Stowage's
  retrieved context, in judged-QA mode (Phase 20). Free-text answer (it is the
  thing being graded).
- **LLM judge** — the schema-constrained LLM that grades a reader answer against
  the gold answer semantically, emitting a `correct`/`incorrect`/`partial` verdict
  + justification. JSON-schema-constrained through the gateway seam (§10 — no
  free-text JSON parsing of model output); Phase 20.
- **answer_quality** — the judged end-to-end QA metric, (correct + ½·partial)/N —
  the figure comparable to competitors' published LongMemEval accuracy. Opt-in,
  full-mode-only, operator-run (Phase 20, D-076).
- **Judged-QA mode** — the opt-in, full-mode-only reader+judge eval path
  (`STOWAGE_EVAL_JUDGE=1`); distinct from the deterministic retrieval-only
  `answer_context_hit`. Never runs in CI (Phase 20).
- **longmemeval_s** — the distractor-laden LongMemEval haystack (~40–50
  sessions/question) competitors report on; the like-for-like comparison variant
  (vs the `oracle` slice). A first-class registered dataset selected with
  `STOWAGE_EVAL_DATASET=longmemeval_s` (D-096; Phase 20).
- **Dataset registry** — the `eval/datasets` factory mapping a benchmark name →
  `Spec{DataFile, Fetch, Normalize}`; each dataset self-registers in `init()`, so the
  one public-benchmark runner (`harness.RunDataset`) selects any dataset by name —
  longmemeval, longmemeval_s, locomo — rather than forking a runner per dataset (D-096).
- **Gain harness** — the skeleton for measuring whether memory improves task
  completion over a baseline (no-memory) run. Seed scenarios live in
  `eval/gain/scenarios/`. The full Harbor-fleet measurement + online-adaptation
  scenarios are **Phase 20b** (post-Phase-19, D-076 — they consume the
  reflection→playbook loop).
- **Single flush per conversation** — the CI eval design decision (D-054) where
  all sessions of a conversation share one buffer key and are flushed together,
  producing one `Complete` call and one mock script consumption.
- **StartPipeline** — the single canonical post-boot wiring (`boot.StartPipeline`,
  D-068) that turns an opened `Stack` into a live derivation system: the
  buffer/extract/reconcile stages, the lifecycle Manager (all sweeps), and the
  embedding `BackfillSweep`. Shared by `stowage serve`, `stowage mcp`, and
  `sdk/stowage` (NewEmbedded) so the three entrypoints cannot drift apart.
- **ClampExcerpt** — the shared, UTF-8 rune-safe provenance-excerpt shaper
  (`retrieval.ClampExcerpt`, D-069) used by both the server (HTTP+MCP) and the
  embedded SDK drill-down paths, so a span offset landing mid-rune can never
  return invalid UTF-8 (parity-lens BUG-5).
- **FillZeroDefaults** — the embedded path's defaults layer
  (`config.Config.FillZeroDefaults`, D-069): applies the same
  defaults < profile merge `config.Load` runs (gateway model/dims/rerank,
  profile-resolved `telemetry.log_format`) to a programmatically-built config, so
  an in-process host's lanes and fleet behaviour match the server's.
- **ftsMatchArg** — the sqlite FTS5 query sanitiser
  (`internal/store/sqlitestore`, D-069): extracts alphanumeric terms from raw user
  text and ANDs them as quoted string literals, mirroring Postgres
  `plainto_tsquery` robustness so operator/special-char queries can no longer
  hard-error and silently drop the lexical/queries lanes (parity-lens BUG-4).
- **TrySend** — the shared panic-safe non-blocking ingest enqueue
  (`pipeline.TrySend`, D-067 Wave-A checkpoint): used by both the MCP
  `memory_ingest` handler and the embedded SDK `Ingest` so a send racing the
  shutdown `Drain` (closed channel) degrades to a dropped item instead of
  panicking across the API/MCP boundary.
- **Tiered surface parity** — the placement rule for control/management verbs
  (D-067, D-071): single-user verbs (topic write, buffer flush, branch
  fork/merge/discard) are reachable on {SDK, MCP, HTTP}; `memory_assert` is the
  one single-user verb deliberately excluded from HTTP, so it reaches
  {SDK (embedded), MCP} only (writes stay pipeline-routed on HTTP); multi-user/admin
  verbs (grants/group management, contribute-mode honoring) reach {HTTP, MCP} only —
  never the single-user embedded SDK. Each verb has one shared core so the
  surfaces cannot drift.
- **Co-mounted server** — one `stowage serve` process serving both the HTTP API
  and MCP-over-HTTP over a single `boot.Stack` + `boot.StartPipeline` — one
  cache, one pipeline, no cross-process staleness (D-073/D-074). Enabled by the
  opt-in `server.mcp_listen` knob (a second listener); empty keeps `serve`
  single-surface. Two listeners (not one path-prefixed port) because MCP streams
  and must not inherit the REST `WriteTimeout`/middleware.
- **Auto-wired rerank provider** — a synthetic Bifrost custom provider
  (`stowage-rerank`, `BaseProviderType=Cohere`, request path `/rerank`, same
  key/base) the bifrost driver adds so a non-native-rerank primary (e.g.
  OpenRouter) can serve the cross-encoder rerank over its Cohere-shape
  `…/api/v1/rerank` (D-075). Wired iff `gateway.rerank_model` is set and the
  primary `gateway.provider` is not in the native-rerank set
  `{cohere, vllm, bedrock, vertex}`; embed/complete keep routing to the primary.
- **Five-minute rule** — the binding adoption criterion (RFC §9.4): a fresh
  environment with one secret env var (`STOWAGE_GATEWAY_API_KEY`) reaches first-
  memory-stored-and-retrieved in under five minutes, scripted and smoke-gated
  (`scripts/smoke/phase-21-fiveminute.sh`, Phase 21).
- **Forbidden-names history sweep** — the launch-blocking check that the predecessor
  systems' project names appear nowhere in the *entire git history* (not just the
  working tree), enforcing the clean-room predecessor-hygiene rule
  (`scripts/forbidden-history-sweep.sh`, Phase 21).
- **Release matrix** — the CGo-free cross-compile artifact set (darwin/linux ×
  amd64/arm64) with published `SHA256SUMS`, produced by `make release` / the release
  workflow (Phase 21).
- **Full-cycle live acceptance** — the operator-run script
  (`scripts/acceptance/full-cycle-live.sh`) that drives the running server through a
  realistic usage cycle over every consumer route (HTTP + MCP-over-HTTP + CLI) with
  the real LLM/embedding/rerank models active, asserting end-to-end correctness — the
  launch acceptance gate run before tagging v0.1 (Phase 21).
- **DSAR cascade** — the Data Subject Access Request cascading delete
  (`OpsStore.DeleteUserData`, `DELETE /v1/admin/users/{user}`): a single-transaction
  erasure of ALL data for one `(tenant, user)` — every user_id-bearing table plus the
  children of the user's memories — in FK-safe order. It is the **only** code path
  allowed to delete verbatim records (the P1 retention/DSAR exception, RFC §13, D-098).
- **`user.purged`** — the tenant-scoped audit event the DSAR cascade emits after a
  purge, carrying the per-table `DSARCounts`; emitted at tenant scope (not the deleted
  user) so it survives the user's own events being purged in the same transaction.
- **Reader / judge (eval)** — the two LLM roles in judged-QA eval (Phase 20, D-076): the
  **reader** answers a benchmark question using ONLY Stowage's retrieved context
  (abstaining when it is insufficient); the **judge** grades that answer against the gold
  answer for semantic equivalence. Both route through the gateway seam (P5).
- **Reasoning effort** — an optional per-`CompleteRequest` knob (`"none"|"minimal"|"low"|
  "medium"|"high"`) requesting provider extended thinking (D-100); empty sends no reasoning
  parameter. Used by the eval reader (medium) for harder multi-hop questions.
- **Per-request model override** — an optional `CompleteRequest.Model` (D-100) that swaps
  the completion model for one call without a second gateway — e.g. a strong eval reader
  (Sonnet 4.6) over the cheap extraction model.
- **LongMemEval magnet set** — the 12 compiled-in extraction topics
  (`eval/harness/topics_seed.go`, D-101) seeded for a full-mode LongMemEval run so
  topic-gated extraction captures the breadth of probed facts (events, dates, possessions,
  relationships, numbers, updates) rather than only the default preferences pack.
- **Conversation context (reconcile)** — the raw provenance turns of the candidate and its neighbors, supplied to the supersede/merge decision so the model distinguishes a correction from a distinct fact that merely shares words (Phase 29b, D-108).
- **Assertion date / `occurred_at`** — when a fact was stated in conversation (vs `created_at`, when extracted). Captured on the memory as `ValidFrom` and surfaced at retrieval as `occurred_at` / "When:", so a reader can reason temporally and date-resolve stale values (Phase 29c, D-109).

**scope-authoritative write** — the records `Append` rule (D-124): a declared scope dimension
(project/user/session) wins; the per-record value only fills a dimension the scope left empty, so a
write can never escape its authorized scope (P3).

**Five-minute minimum** — the single secret (`STOWAGE_GATEWAY_API_KEY`) a real-driver
`stowage serve` needs to boot. With the gateway defaulting to the Bifrost/OpenRouter stack
(D-131), one key reaches completion, embedding, and rerank; a missing key fails loud at boot
naming the var (RFC §9.4).

**`mock` escape hatch** — `STOWAGE_GATEWAY_DRIVER=mock` boots a keyless, no-provider gateway
for hermetic tests and offline runs. `mock` is a first-class driver, no longer the default
(D-131).

**Per-learner-stage model** — an optional completion model pinned to one learner stage
(`gateway.extract_model` / `reconcile_model` / `reflect_model`), each falling back to
`gateway.model` when empty. Lets a cheap extractor run alongside a stronger
reconciler/reflector through one gateway (D-132). Distinct from per-concern provider
keys (a1b), which split the provider/credential rather than the model.
