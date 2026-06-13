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
- **Gateway** — the single intelligence seam (`internal/gateway`) through which
  every embedding and LLM call flows; drivers: `bifrost`, `mock`.
- **Gain** — the performance delta an agent shows with memory on vs. off (the
  CL-Bench-derived release metric).
- **Sweep** — a scheduled in-process lifecycle job (decay, dedupe, rollup,
  re-enqueue); jittered, idempotent, singleflight.
- **Re-enqueue sweep** — crash recovery: records past their processing deadline
  re-enter the pipeline.
- **Dead letter** — a pipeline item that exhausted retries; persisted for
  inspection, never silently dropped.
- **Grant** — a store-layer-enforced share of a slice of an owner scope to a
  named group (team), with read or contribute access, a privacy-zone ceiling,
  and optional redaction (D-016).
- **Outcome** — the success/failure + execution-feedback tag a record or buffer
  flush can carry; the label-free fuel for reflection (D-018).
- **Reflection** — the outcome-aware extraction mode that distills `strategy`
  and `failure_mode` memories from trajectories (ACE's Reflector, brief 05).
- **Playbook** — the deterministic, sectioned, budget-packed context view over
  a scope's strategy/failure-mode memories (`GET /v1/playbook`); evolves only
  through delta reconciliation, never LLM rewrite (context-collapse defense).
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
- **Episode** — a detected coherent temporal unit of records, with a generated
  narrative memory carrying full provenance (§6b).
- **Narrative** — the `narrative`-kind memory telling an episode's concrete
  path of decisions, not a vague summary.
- **Causal link** — a `caused_by`/`led_to` typed edge between decision
  memories, explicit or inferred through episode narratives.
- **Episode contrast** — surfacing the most similar past episode and comparing
  outcomes against the current situation.
- **Branch** — a session fork for exploration; working memories merge on
  accept or expire on discard, records always remain (D-029).
- **Hot–warm cache** — the (query-signature, scope) result cache plus
  injection-frequency hot set that serves frequent retrievals without a vector
  lookup (D-031).
- **Preference fragments** — `preference`-kind memories from the default
  personalization topic pack ("how this user wants to be answered").
- **Trigger** — a proactive-engine rule (session start, episode similarity,
  expiring validity) whose confidence is tuned by accept/dismiss feedback.
- **Suggestion** — a threshold-passing proactive offer, tracked with its own
  utility counters (§6d).
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
- **Pack** (default topic pack) — a compiled-in set of topic entries applied
  at extraction prompt-build time when a scope has no explicit active topics
  (D-043). Two packs ship: `pack:preferences` (assistant profile —
  personalisation, communication style, durable personal facts) and
  `pack:agent-learnings` (coding-agent/fleet — gotchas, patterns, decisions).
  Packs are virtual: they are never written to the topics table and appear in
  `GET /v1/topics` with `source: pack`. Any explicit active topic disables the
  pack; the `pack:off` sentinel opts out of packs entirely.
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
  have returned a given memory in the current process lifetime, tracked by the
  in-memory LRU Hub (Phase 10, `internal/retrieval.Hub`). Memories with ≥ 4
  distinct signals receive a 0.80× hub-dampening multiplier in the utility score
  to counteract generic "hub" content dominating results across unrelated queries.
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
- **answer_context_hit** — the primary CI eval metric: the fraction of questions
  where the expected answer string appears (case-insensitive substring match) in
  any retrieved item's content. Measures end-to-end recall across the full
  extract → reconcile → retrieve pipeline (Phase 13).
- **Gain harness** — the skeleton for measuring whether memory improves task
  completion over a baseline (no-memory) run. Seed scenarios live in
  `eval/gain/scenarios/`. The full fleet-loop measurement is Phase 20.
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
