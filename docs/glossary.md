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
- **Pack** (default topic pack) — a compiled-in set of topic entries applied
  at extraction prompt-build time when a scope has no explicit active topics
  (D-043). Two packs ship: `pack:preferences` (assistant profile —
  personalisation, communication style, durable personal facts) and
  `pack:agent-learnings` (coding-agent/fleet — gotchas, patterns, decisions).
  Packs are virtual: they are never written to the topics table and appear in
  `GET /v1/topics` with `source: pack`. Any explicit active topic disables the
  pack; the `pack:off` sentinel opts out of packs entirely.
