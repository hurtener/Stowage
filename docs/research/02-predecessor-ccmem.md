# Brief 02 — The CC-memory predecessor (Go)

> Context-only; clean-room (D-003). An internal Go memory system for coding
> agents, reviewed 2026-06-10. The strongest scoring/lifecycle model we have
> seen in any memory system; Stowage adopts most of it (D-008).

## Shape

Pure-Go daemon + MCP server + optional API proxy; sqlite (FTS5) + embedded
512-d embeddings; learnings enriched with junction tables (entities, actions,
keywords, anticipated queries); hybrid BM25+vector retrieval with RRF.
Validated on a LoCoMo-style benchmark at 0.86–0.87 vs 0.65 for single-hop RAG.

## The lifecycle model (adopt)

- **Six utility counters** per memory: `match`, `inject`, `use`, `save`,
  `fail`, `noise`. Score rises only with `use`/`save` (save double-weighted);
  `inject` without `use` *lowers* a precision factor — kills "zombie memories"
  that rank high purely from visibility.
- **Decay**: `exp(-Δ/stability)` with stability grown logarithmically by proven
  utility; floors (10 % default, 50 % user-stated). Δ measured in project
  conversation turns. *Known blind spot*: pure turn-based Δ never decays
  memories in dormant projects → Stowage blends activity turns with wall-clock.
- **Contradiction boost** (Pearce–Hall): a correction inherits
  `importance ≥ 4`, reset usage, long stability — outranks what it corrects
  immediately, not after N sessions.
- **Trust-gated supersede**: trust = f(use, save, source multiplier,
  importance). Low trust → replace; medium → replace + warn; high → park the
  newcomer pending confirmation, keep the old active. Chains walked by
  recursive CTE with cycle detection and a 10-hop cap.
- **Trust-source hierarchy**: `user_stated > agreed_upon > agent_suggested >
  llm_extracted`, as scoring multipliers and supersede gates.
- **Hub dampening**: memories matching 4+ distinct query clusters are penalized
  as generic.
- **Write-echo cooldown**: ~30 min suppression on just-stored memories so a
  fact isn't retrieved straight back at the agent that just said it.
- **Quarantine over deletion**: noisy sessions/memories excluded from retrieval
  but kept (auditable, reversible).
- **Session-quality signal**: cheap pattern detectors (consecutive errors,
  edit-build-error loops) compute a "fixation ratio" that down-weights
  extractions from pathological sessions. (Stowage: v1.x candidate.)

## Retrieval ideas (adopt)

- **Anticipated queries**: 3–5 concrete search phrases generated at extraction
  time, indexed in a separate lexical lane and fused separately — catches
  domain phrasings the content itself doesn't contain.
- **Enriched embedding text**: embed content + entities + keywords +
  anticipated queries, not raw content alone.
- **Junction tables** for entities/actions/keywords → precise structured lane.
- **Cheap pre-dedup before LLM work**: SHA-256 exact + bigram-Jaccard (~0.85)
  near-dup checks eliminated ~40 % of reconciliation LLM calls.
- **Context-affinity boosts**: project match (graduated by recency), entity
  match against the working file/dir, domain match.

## Lessons from its weaknesses

- sqlite single-writer contention caused 5–60 s lock waits under concurrent
  extraction → Stowage's sqlite driver serializes writes through a dedicated
  writer goroutine (D-009).
- Brute-force vector scan over BLOBs with no ANN index hurts at 100k+ → vindex
  seam with pgvector / pure-Go HNSW options (Phase 09, OQ-2).
- 70+ MCP tools and 50+ config knobs → deliberate small surface (D-015).
- Proxy mode (intercepting the host's model API for context management) is
  powerful but couples memory to one host's wire protocol → out of scope;
  Harbor owns context assembly (RFC §14).
- Flat-text memories can't answer "why do you believe this?" → provenance refs
  are mandatory in Stowage (P1).
