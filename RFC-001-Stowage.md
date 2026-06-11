# RFC-001: Stowage — a Go-native memory server for agentic systems

- **Status:** Draft for review
- **Author:** hurtener
- **Created:** 2026-06-10
- **Ecosystem:** Portico (MCP gateway) · Harbor (agent runtime) · Dockyard (MCP server framework) · **Stowage (memory infrastructure)**

---

## 1. Summary

Stowage is a single-binary, CGo-free Go memory server for AI agents. It ingests raw
interactions with a fire-and-forget API, asynchronously extracts structured memories
guided by configurable **topics**, actively **reconciles** new information against
what it already knows (update, supersede, merge, forget), and serves **hybrid
retrieval** (lexical + vector + structured) with utility-driven ranking and
**provenance drill-down** to the verbatim source.

It is a ground-up redesign of our internal Python memory server (referred to
throughout as *the Python predecessor*), informed by four additional sources:

1. *The CC-memory predecessor* — an internal Go memory system for coding agents
   with an exceptionally good scoring/lifecycle model.
2. **Weaviate Engram** — topics-as-magnets, active reconciliation, multi-agent
   buffers, scope isolation, fire-and-forget API.
3. **CL-Bench** (arXiv 2606.05661) — empirical failure modes of current memory
   systems; its headline finding drives our fidelity-first design.
4. **ACE** (arXiv 2510.04618) — agentic context engineering; its
   reflection-and-playbook loop is built in as a server capability (§6a).

Detailed findings live in `docs/research/` (briefs 01–05). No code or files from
the predecessors are vendored here; this repository is a clean-room redesign.

### Why a rewrite, and why Go

The Python predecessor works, but its architecture fights its runtime:

| Python predecessor pain | Stowage answer |
|---|---|
| Polling worker pools (0.25 s embedding queue polls), custom DAG orchestration framework | Goroutine pipeline stages connected by bounded channels; no pollers, no external workers, no job framework |
| GIL workarounds: thread-pool executors around FAISS, per-tenant lock dicts | Native concurrency; `-race`-proven shared structures |
| Local embedding + reranker models (~300 MB+ resident, slow cold start) | **No local models.** Embeddings and LLM calls go through one gateway seam — Bifrost first, other providers later |
| 48+ tables, ~76 k lines, 88 migrations | A deliberately small schema (~12 tables) behind a `Store` seam with sqlite + Postgres drivers |
| FastAPI + Uvicorn + SQLAlchemy async stack | One static binary, stdlib HTTP, `log/slog`, graceful shutdown |
| LLM structured output via DSPy + JSON re-parsing | Typed Go structs + JSON-schema-constrained tool calls through the gateway |

Concurrency, deployment simplicity, and cost (no GPU/CPU-heavy embedding hosts)
are the headline wins. The deeper win is architectural: the rewrite lets us adopt
the Engram pipeline model and the CL-Bench fidelity findings at the foundation
instead of bolting them on.

---

## 2. What Stowage is

```text
Portico  — the MCP gateway        (connects and governs tools)
Harbor   — the agent framework    (builds and runs agents; owns the MCP client)
Dockyard — the MCP Apps framework (builds the MCP servers users touch)
Stowage  — memory infrastructure  (remembers, reconciles, retrieves, forgets)
```

Stowage ships **one CGo-free static binary** — `stowage` — that runs as:

- an **HTTP service** (`stowage serve`) for Harbor fleets and any other client, and
- an **MCP server** (`stowage mcp`) so agent hosts can use memory as tools, and
- a **CLI** for operations (migrate, inspect, evaluate, export).

The same code is consumable as a **library**: `sdk/stowage`'s in-process mode
embeds the full server (pipeline, store, retrieval) inside a host process with
no daemon and no network hop. This is a supported deployment, not a test
convenience — the target picture includes a Harbor agent + Stowage embedded in
a Wails desktop app, which is why CGo-freedom and the pure-Go sqlite driver are
non-negotiable even though Postgres is the principal server-mode store (§8.1).

Memory is treated as **infrastructure, not a feature** (Engram's framing): it gets
storage-layer guarantees — predictable latency, hard isolation, durability,
lifecycle management — and an async pipeline so callers never block on memory I/O.

**Positioning.** Stowage is built to be shown: the eval harness (§12) targets
state-of-the-art results on public memory benchmarks as the proof point for an
eventual open-source release, with a managed-cloud offering (Engram-style) as
the monetization path. Multi-tenancy, scope isolation, and metering are
therefore first-class from day one, and everything in this repository is
written as if it becomes public (D-003 hygiene).

### 2.1 Five binding properties

A change that weakens any of these is wrong — amend this RFC first.

1. **P1 — Fidelity first.** Verbatim interaction records are durable and never
   silently discarded. Every derived memory carries provenance to its source
   range; retrieval can always drill down from an abstraction to the verbatim
   record. Rationale: CL-Bench's core finding — "the bottleneck in current memory
   systems lies not in storage capacity but in extraction and retrieval fidelity."
   Lossy compression without a recovery path is the defining failure of the
   systems it benchmarked.
2. **P2 — Fire-and-forget writes.** The ingest API acknowledges after durable
   append of the verbatim record (single fsync-bounded write) and returns.
   Extraction, reconciliation, embedding, and indexing happen asynchronously in
   supervised goroutine stages. No external worker fleet, no polling loops.
3. **P3 — Scopes are enforced at write AND read.** Every record and memory is
   stamped with an identity scope (tenant → project → user → session) at write
   time; every query is filtered by scope at the storage layer, not in handler
   code. Cross-scope leakage is structurally impossible, not policy-discouraged.
4. **P4 — Memory must forget.** Reconciliation (update/supersede/merge/delete),
   utility-driven decay, contradiction handling, and quarantine are first-class
   subsystems, not batch afterthoughts. A memory system that only accretes is
   actively harmful (CL-Bench: stale memories made agents *worse* than no memory).
5. **P5 — No local models; one intelligence seam.** Every embedding, extraction,
   reconciliation, and rerank call goes through the `gateway` seam. The first
   driver is Bifrost (OpenAI-compatible gateway, same dependency Harbor uses);
   additional providers are new drivers, never new call sites. The shipped binary
   is CGo-free and model-free.

---

## 3. Informing findings (condensed)

Full briefs: `docs/research/01-predecessor-python.md`, `02-predecessor-ccmem.md`,
`03-engram.md`, `04-cl-bench.md`, `05-ace.md`.

**From the Python predecessor — keep the ideas, not the weight.** Hybrid BM25 +
vector retrieval with fusion; privacy zones; confidence as a composed,
multi-signal value; nightly consolidation (decay, dedupe, rollup) with idempotency
markers; knowledge kinds (fact / preference / assertion) with truthiness;
importance reinforcement on feedback; audit events for every lifecycle action.

**From the CC-memory predecessor — the best lifecycle model we've seen.**
Six independent utility counters (`match`, `inject`, `use`, `save`, `fail`,
`noise`) instead of one hit counter — visibility alone never raises a memory's
rank; an Ebbinghaus-style decay where stability *grows* with proven usefulness;
contradiction-boost (a correction outranks what it corrects immediately); trust
gates on supersede (a battle-tested memory resists silent replacement); a
dedicated **anticipated-queries** retrieval lane (3–5 search phrases generated at
extraction time, indexed separately); junction tables for entities/actions/
keywords enabling precise structured search; hub dampening (memories matching
everything are penalized as generic); a cooldown that stops just-written memories
from echoing straight back; quarantine instead of deletion for suspect sessions.

**From Engram — the pipeline shape.** Topics as natural-language magnets that
gate extraction (you state what matters; everything else is never extracted);
active reconciliation as an LLM tool-call decision over retrieved neighbors
(add / update / merge / supersede / discard / delete); buffers that collect
fragments across runs and agents and flush on triggers; scope primitives with
hard isolation; fire-and-forget ergonomics.

**From ACE (arXiv 2510.04618) — self-improving contexts as a memory-server
capability.** ACE's Generator/Reflector/Curator loop turns agent trajectories
into an evolving, itemized playbook: insights are distilled from successes and
*failures*, stored as discrete bullets with IDs and helpful/harmful counters,
updated by deltas merged deterministically — never by monolithic LLM rewrites,
which it shows cause "context collapse" (an 18k-token context compressed to 122
tokens in one rewrite, dropping accuracy below baseline). Stowage's memory
model is already ACE's bullet model (itemized entries, IDs, counters, delta
reconciliation, grow-and-refine dedup); §6a builds in the missing pieces —
outcome-tagged ingestion, a reflection extraction mode, and deterministic
playbook assembly — so a fleet's experience compounds without any per-agent
prompt engineering.

**From CL-Bench — what kills memory systems.** Lossy extraction that can't be
recovered at retrieval time; stale memories actively misleading agents after
distribution shift; retrieval that misses; over-reliance on recency. Its
prescriptions are our pillars: hybrid verbatim-recent + abstracted-old memory,
multiple retrieval paths (similarity, recency, structured, outcome), downweight/
prune on shift evidence, and a **gain metric** — measure memory by the
performance delta it produces, not by recall of the store. Stowage ships an
evaluation harness because of this paper: a memory server that can't demonstrate
positive gain is not done.

---

## 4. Architecture overview

```text
                       ┌────────────────────────────────────────────────┐
 client (Harbor agent, │  stowage serve / stowage mcp                   │
 MCP host, REST)       │                                                │
   │                   │  api/        identity, validation, routing     │
   │ ingest ───────────┼──► records   durable verbatim append ──► ACK   │
   │ (returns in ~ms)  │       │                                        │
   │                   │       ▼ (async, supervised goroutine stages)   │
   │                   │  pipeline:  buffer ─► extract ─► reconcile ─► commit
   │                   │               │         │            │          │
   │                   │               │      topics       neighbors     │
   │                   │               │      (magnets)    (retrieval)   │
   │                   │       ┌───────┴──────────┴────────────┴───────┐ │
   │                   │       │ gateway seam: embeddings + LLM calls  │ │
   │                   │       │ driver: bifrost (more later)          │ │
   │                   │       └───────────────────────────────────────┘ │
   │ retrieve ─────────┼──► retrieval: lexical ∥ vector ∥ structured     │
   │                   │      ► fuse (RRF) ► score ► budget ► drill-down │
   │                   │                                                │
   │                   │  lifecycle: decay · dedupe · rollup · sweep    │
   │                   │  (jittered tickers, singleflight, idempotent)  │
   │                   │                                                │
   │                   │  store seam: {sqlite, postgres}                │
   │                   │  index seam: {FTS5/tsvector, pgvector/Go-HNSW} │
   │                   │  events: typed stream (slog + bus adapter)    │
   └───────────────────┴────────────────────────────────────────────────┘
```

### 4.1 Write path

1. `POST /v1/records` (or MCP `memory_ingest`, or SDK call) with identity scope,
   the interaction content, and optional hints (session, turn, source agent,
   and — for ACE reflection, §6a — a task outcome: success/failure plus
   execution feedback).
2. Handler validates, stamps scope + ULID, **appends the verbatim record**, ACKs.
   Target p99 under 15 ms on sqlite, dominated by the durable write.
3. The record ID is sent down a bounded channel into the pipeline:
   - **Buffer** — fragments accumulate per (scope, buffer key). Flush triggers:
     item count, token estimate, max age, session end, explicit flush. This is
     the multi-agent write surface: many agents feed one buffer without blocking
     each other (Engram §buffers).
   - **Extract** — on flush, one gateway call per buffer with the scope's active
     **topics** in the prompt; output is a JSON-schema-constrained list of
     candidate memories (kind, content, entities, keywords, anticipated queries,
     importance, confidence). No topic match → no memory (noise never enters).
   - **Reconcile** — for each candidate, retrieve nearest existing memories in
     scope (hybrid), then one gateway tool-call decides: `add`, `update`
     (rewrite an existing memory), `merge`, `supersede` (trust-gated), or
     `discard`. Cheap pre-filters run first: SHA-256 exact-duplicate check and
     bigram-Jaccard near-duplicate check eliminate most LLM calls.
   - **Commit** — winning operations are applied transactionally; embeddings for
     new/updated memories are requested in batches through the gateway; lexical
     and vector indexes are updated; events are emitted for every mutation.
4. Every stage is a supervised goroutine pool with bounded queues, per-stage
   retry with backoff, a dead-letter table for poisoned items, and graceful
   drain on shutdown. Backpressure propagates by channel depth — never by
   dropping a durable record (P1: the verbatim record is already safe; pipeline
   loss only ever delays derivation, and the sweeper re-enqueues unprocessed
   records).

### 4.2 Read path

1. `POST /v1/retrieve` with scope, query, and an optional retrieval profile.
2. Lanes run concurrently (errgroup): **lexical** (FTS5/tsvector BM25),
   **vector** (cosine over gateway embeddings), **anticipated-queries** (lexical
   over the generated-phrases index), **structured** (entity/keyword/kind
   filters). Reciprocal-rank fusion merges lanes.
3. **Scoring** re-ranks fused candidates: utility boost (from the six counters),
   decay, trust/source weight, scope affinity, recency path, hub dampening,
   cooldown suppression. Optional API rerank (gateway) for the top slice.
4. **Budgeting** packs results to the caller's token budget.
5. **Drill-down**: every returned memory carries provenance refs; callers (or the
   server, when a memory is marked low-fidelity) can expand to the verbatim
   record range in one call. This is the CL-Bench recovery path: abstraction for
   cheap recall, verbatim for ground truth.
6. Retrieval emits feedback hooks: the caller reports which memories were
   injected/used/ignored, feeding the utility counters.

---

## 5. Memory model

### 5.1 Records (the fidelity layer)

Immutable, append-only verbatim interactions.

```text
record: id (ULID) · scope · session_id · turn · role(s) · content ·
        source_agent · token_estimate · created_at · processed_at
```

Records are lexically indexed (deep search over raw history is a retrieval lane
for power callers) but never embedded wholesale and never mutated. Retention is a
scope-level policy (default: keep; DSAR-style deletion cascades to derived
memories).

### 5.2 Memories (the abstraction layer)

```text
memory:  id · scope · kind · content · context · status ·
         entities[] · keywords[] · anticipated_queries[] ·
         importance (1–5) · confidence (0–1) · trust_source ·
         counters {match, inject, use, save, fail, noise} ·
         stability · last_accessed_at · valid_until? ·
         provenance [{record_id, span}] · supersedes? · superseded_by? ·
         created_at · updated_at
```

- **Kinds:** `fact`, `preference`, `decision`, `gotcha`, `pattern`, `task`,
  `narrative`, plus the ACE reflection kinds `strategy` and `failure_mode`
  (extensible enum; kinds carry default scoring weights; reflection kinds are
  the building blocks of playbooks, §6a).
- **Status:** `active`, `pending_confirmation` (trust-gated supersede),
  `superseded`, `quarantined`, `expired`, `deleted` (tombstone).
- **Trust source hierarchy:** `user_stated` > `agreed_upon` > `agent_suggested` >
  `llm_extracted` — multipliers in scoring and gates on supersede.
- **Six utility counters** (CC-memory predecessor): `match` (came back in a
  search), `inject` (was placed in context), `use` (caller marked it useful),
  `save` (caller explicitly persisted/acted on it), `fail` (was injected and the
  task failed), `noise` (caller flagged irrelevant). Rank rises only with `use`/
  `save`; `inject` without `use` *lowers* precision weight — no zombie memories.
- **Stability & decay:** score decays as `exp(-Δ/stability)` where Δ blends
  scope-activity turns and wall-clock time (fixes the predecessor's pure
  turn-based blind spot for dormant projects), and stability grows
  logarithmically with proven utility. Floors: 10 % default, 50 % for
  `user_stated`.

### 5.3 Scopes and privacy

Identity quadruple (Harbor convention): `tenant / project / user / session`.

- **Hard isolation** at tenant and user boundaries — enforced in the store layer
  (every query is scope-parameterized; there is no unscoped query API).
- **Soft scoping** below: project-shared memories, session-scoped working
  memories, configurable promotion (session → project) through reconciliation.
- **Privacy zones** on memories (`public`/`work`/`personal`/`intimate`) cap how
  far a memory can be shared or promoted; carried over from the Python
  predecessor, simplified: zones gate *promotion and export*, scopes gate *access*.
- **Grants — team sharing without federation.** A memory always belongs to its
  owner scope; a **grant** gives a named group (a team) `read` or `contribute`
  access to a slice of that scope (filterable by topic and kind), capped by a
  privacy-zone ceiling (`work` shareable, `personal`/`intimate` never) and an
  optional redaction profile. Grants are enforced in the store layer exactly
  like scopes (P3) and every grant/revoke is an event. This is the fleet
  primitive: many agents across users contribute to and read a team memory
  pool — one agent's `failure_mode` becomes every teammate's avoided mistake —
  without the predecessor's federation-graph machinery (RFC §14 keeps full
  cross-tenant federation out of scope).

### 5.4 Topics (extraction magnets)

Per-scope configuration, natural language:

```yaml
topics:
  - key: food-prefs
    description: "Dietary preferences, allergies, favorite cuisines"
  - key: infra-gotchas
    description: "Surprising behaviors of our deploy pipeline and CI"
```

Extraction prompts are assembled from active topics; a candidate that matches no
topic is never created. Topics double as a retrieval facet and a lifecycle
boundary (deleting a topic can expire its memories). Default topic packs ship for
the common cases (assistant personalization, coding-agent learnings).

---

## 6. Reconciliation (the forget machinery)

On each candidate memory, after the cheap dedup pre-filters:

1. Retrieve top-k neighbors in scope (hybrid, same machinery as the read path).
2. One gateway call with constrained tool choice:
   `add | update(id, new_content) | merge(ids, content) | supersede(id) | discard`.
3. **Trust gates** on destructive decisions: superseding a memory with high
   trust score (`f(use, save, source, importance)`) parks the new memory as
   `pending_confirmation` instead of replacing — the old one stays active until
   confirmation (explicit feedback or repeated independent extraction).
4. **Contradiction boost:** when a correction lands, it inherits
   `max(importance, 4)` and elevated stability so it immediately outranks what it
   corrected (CC-memory predecessor's Pearce–Hall move).
5. Supersede chains are walked with cycle detection (recursive CTE, hop cap).
6. Every decision is an event with the model's stated reason — auditable memory.

**Reversibility (binding).** Every destructive reconciliation operation is
invertible from its event within the retention window: `merge` and `supersede`
keep their sources as `superseded` (linked, never deleted); `update` events
store the prior content. `POST /v1/memories/{id}/rollback` (or
rollback-by-event-id) restores the prior state and tombstones the result of the
reverted operation, emitting its own event. Reconciliation is an editor with
undo, not a shredder — the LLM gets to be wrong recoverably.

Scheduled lifecycle (jittered tickers + singleflight + idempotency markers, all
in-process):

- **Decay sweep** — recompute effective scores, expire below-floor memories.
- **Dedupe sweep** — cluster near-duplicates that slipped past write-time checks.
- **Rollup** — periodically merge stale session-scoped working memories into
  project digests (verbatim records remain — P1).
- **Re-enqueue sweep** — any record past a processing deadline goes back through
  the pipeline (crash recovery without a job framework).
- **Shift detection (v1.x):** when a scope's recent `fail`/`noise` rates spike
  against a memory cluster, downweight the cluster and surface a
  `memory.shift_suspected` event (CL-Bench's distribution-shift prescription).

---

## 6a. Self-improvement built in (ACE)

Stowage makes the ACE loop (brief 05) a server capability so fleets compound
experience without per-agent prompt engineering. Three pieces complete what the
memory model already provides:

1. **Outcome-tagged ingestion.** Records and buffer flushes can carry a task
   outcome (`success`/`failure` + execution feedback). Harbor task-completion
   events are the natural label-free source (ACE's key result: reflection works
   from execution feedback alone). Outcomes also feed the `use`/`fail` counters
   of the memories that were injected into the task (§4.2.6).
2. **Reflection extraction mode.** Alongside topic extraction, an outcome-aware
   reflector pass distills `strategy` and `failure_mode` candidates from
   trajectories ("what worked, what to avoid, why"), iteratively refined, then
   reconciled like any candidate — so strategies dedupe, update, and supersede
   under the same trust gates. Multi-epoch reflection (re-reflecting over old
   outcome-tagged records as the playbook matures) runs as a lifecycle sweep.
3. **Deterministic playbook assembly.** `GET /v1/playbook` renders a curated,
   sectioned context for a scope: strategies and failure modes grouped by
   topic, ranked by utility counters, budget-packed, with provenance refs. The
   assembly path contains **no LLM call** — ACE shows monolithic LLM rewrites
   cause context collapse; Stowage's playbook is a deterministic view over
   itemized memories whose evolution happens only through delta reconciliation.
   Output is stable and append-biased so host-side prompt caching stays warm
   (ACE reports 91.8 % KV-cache hit rates from exactly this property).

A Harbor fleet's loop: agents run → outcomes ingest (fire-and-forget) →
reflection + reconciliation evolve the team playbook (shared via grants, §5.3)
→ every agent's next session starts from `GET /v1/playbook`.

---

## 7. The gateway seam (embeddings + LLM)

```go
type Gateway interface {
    Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)      // batched
    Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error) // JSON-schema-constrained
}
```

- **Driver: `bifrost`** (v1) — OpenAI-compatible chat + embeddings endpoints; the
  same gateway Harbor fronts its LLM traffic with. Configuration mirrors
  Harbor's conventions (`api_key: env.BIFROST_API_KEY`, fail-closed at boot).
- Embeddings are **batched** (size + flush-interval), concurrent across batches,
  retried with backoff and circuit breaking; dimensions are pinned per index and
  validated at boot (an index is bound to model+dims; changing models is an
  explicit reindex operation, never silent).
- Extraction/reconciliation calls use **constrained decoding** (JSON schema /
  forced tool choice) — no free-text JSON parsing.
- Per-scope **cost metering**: every gateway call emits tokens + computed cost as
  an event; scope-level ceilings can refuse non-essential work (rerank first,
  then extraction depth) before blowing a budget.
- Embedding cache keyed by (model, content hash) — reconciliation rewrites often
  re-embed near-identical content.

No local inference of any kind ships in the binary (P5). A future `local` driver
could front llama.cpp-style servers — as an HTTP driver, still no CGo.

---

## 8. Storage

### 8.1 Store seam

All durable state behind one interface with a conformance suite every driver
must pass (Dockyard's store-seam discipline). **Postgres is the principal
store** for server deployments (fleets, managed cloud); sqlite is the embedded/
portable driver (dev, desktop embedding, single-user). Both are full citizens
of the conformance suite — the seam, not either driver, is the contract, and
future backends arrive as new drivers behind it.

- **`postgres`** (pgx, principal): tsvector lexical lanes, pgvector HNSW for
  vectors, advisory locks for scheduler singleflight across replicas,
  connection pooling for fleet concurrency.
- **`sqlite`** (modernc.org/sqlite, pure Go, embedded): FTS5 for lexical lanes,
  vectors as BLOBs with an in-memory pure-Go HNSW (persisted snapshots) above
  brute-force fallback. This driver is what makes the Wails-embedded
  deployment (§2) possible — it must stay CGo-free forever.

Migrations are forward-only, embedded, applied on boot (configurable), one
sequence per driver. Target schema is ~12 tables: records, memories, three
junction tables (entities, keywords, anticipated_queries), topics, buffers,
provenance, events, dead_letters, job_markers, feedback.

### 8.2 Concurrency posture

sqlite driver: WAL mode, single-writer worship via a dedicated writer goroutine
(serialized writes, no lock-wait storms — the CC-memory predecessor's documented
contention pain), readers unconstrained. postgres driver: pool via pgx,
transactions per commit batch.

---

## 9. API surface

### 9.1 HTTP (v1, stable JSON contracts)

```text
POST /v1/records            fire-and-forget ingest (single or batch; optional outcome)
POST /v1/buffers/{key}/flush
POST /v1/retrieve           hybrid retrieval with profile + budget
GET  /v1/playbook           deterministic curated context for a scope (§6a)
POST /v1/drilldown          provenance expansion to verbatim ranges
POST /v1/feedback           use/save/fail/noise signals per memory
GET  /v1/memories/{id}      inspect (with provenance + chain)
PATCH /v1/memories/{id}     assert/correct/quarantine (user-stated writes)
POST /v1/memories/{id}/rollback   revert a reconciliation decision (§6)
GET/PUT /v1/scopes/{scope}/topics
GET/PUT /v1/scopes/{scope}/grants  team sharing with zone ceilings (§5.3)
GET  /v1/events             SSE stream (scoped)
GET  /healthz · /readyz · /metrics
```

Auth v1: per-tenant API keys (constant-time compare), identity scope claims in
the key record; Portico can front this for anything fancier. mTLS/JWT deferred.

### 9.2 MCP server

`stowage mcp` exposes a deliberately small tool set (the Python predecessor's
50-endpoint surface and the CC-memory predecessor's 70 tools are both cautionary
tales): `memory_ingest`, `memory_retrieve`, `memory_playbook`,
`memory_drilldown`, `memory_feedback`, `memory_assert`, `memory_topics`.

The MCP surface is **built with Dockyard**: tool contracts are typed Go structs
with generated schemas, validated by Dockyard's quality gates and exercised
through its inspector — making Stowage Dockyard's first external consumer.
Post-v1, the operator console (browse memories, walk supersede chains, drill
provenance, edit topics/grants, watch the event stream) ships as a **Dockyard
MCP App** rendered inline in the host's chat surface.

### 9.3 Go SDK

`sdk/stowage` — typed client over HTTP plus an in-process mode (embed the whole
server in a Harbor process for single-binary deployments). Registers naturally
as Harbor in-proc tools via `inproc.RegisterFunc`.

---

## 10. Harbor integration — speak the protocol, don't build on the runtime

Stowage is the showcase of Harbor's protocol powering something agentic that is
**not an agent**. The integration is deliberately one-directional: Harbor
depends on Stowage for memory; Stowage's core never depends on Harbor's runtime
(no dependency cycle, and Stowage must run standalone for non-Harbor hosts).
The internal pipeline is a deterministic four-stage flow — it needs channels
and supervision, not a planner; building it on an agent runtime would repeat
the Python predecessor's custom-orchestration mistake.

What Stowage *does* adopt is Harbor's protocol surface:

- **Identity:** Stowage adopts Harbor's identity quadruple verbatim; an SDK
  helper lifts Harbor's `identity.Identity` into a Stowage scope.
- **Events:** Stowage's event stream maps onto Harbor's bus event shape; an
  adapter publishes `memory.*` events (`memory.committed`, `memory.superseded`,
  `memory.rolled_back`, `memory.cost_recorded`, `memory.shift_suspected`) with
  full identity attribution — so the Harbor console shows memory pipelines,
  reconciliation decisions, and per-scope memory cost next to agent runs, with
  zero Stowage-side UI.
- **Governance:** gateway cost events follow Harbor's `llm.cost.recorded`
  semantics, so Harbor's per-identity cost ceilings govern memory's LLM spend
  exactly as they govern agents'.
- **Tools:** recipe + helper for registering the memory tool set on a Harbor
  `ToolCatalog` (in-proc when embedded, HTTP otherwise, MCP for non-Harbor hosts).
- **Flows, where they belong:** Stowage operations appear as tools *inside*
  Harbor flows — an operator-triggered "consolidate project memory" typed DAG,
  a post-task-group reflection flow — memory ops as flow nodes, never memory
  built from flow nodes.
- **Buffers ↔ tasks:** Harbor background tasks and subagents write to a shared
  buffer key derived from (session, task group); flush on task-group completion
  is the default multi-agent learning loop, and task outcomes feed reflection
  (§6a).
- **The eval harness runs on Harbor** (§12): the gain harness's agent loop is a
  Harbor fleet — the canonical dogfood, a fleet measurably better with Stowage
  than without, observed through the Harbor console.

---

## 11. Observability

- `log/slog` everywhere (JSON in prod, text in dev), identity-stamped, secrets
  redacted.
- Typed event stream for every memory mutation and lifecycle decision —
  the audit trail *is* the event log; SSE + optional Harbor-bus adapter.
- Prometheus metrics: pipeline depths, stage latencies, gateway tokens/cost,
  retrieval lane timings, fusion sizes, decay/dedupe sweep stats.
- OTel traces optional, off by default, behind the telemetry seam.

---

## 12. Evaluation (a deliverable, not an afterthought)

`stowage eval` ships in-tree, and it is also the marketing artifact: the
open-source release is gated on demonstrating **state-of-the-art results on
public memory benchmarks**, published as a reproducible report.

- **Gain harness** (CL-Bench-inspired): scripted multi-session scenarios run
  twice — memory on vs. off — with a Harbor fleet as the agent loop (§10);
  reports the performance delta. Negative gain on the standard scenarios fails
  release.
- **LoCoMo-style retrieval benchmark** (the CC-memory predecessor demonstrated
  0.86+ vs 0.65 single-hop RAG; we adopt the methodology and target ≥ that
  bar): recall@k and answer accuracy over long conversations.
- **Online-adaptation scenarios** (ACE-inspired, AppWorld-style): contexts
  evolve during evaluation via the reflection → playbook loop; measures
  compounding improvement across sequential tasks.
- **Go benchmarks** for ingest ACK latency, pipeline throughput, retrieval p99
  at 10k/100k/1M memories per scope.

---

## 13. Security & privacy

- No hardcoded secrets; env-indirected config (`env.VAR` convention, fail-closed).
- Scope enforcement in the store layer (P3); no unscoped query API exists.
- Privacy zones gate promotion/export; DSAR-style export and cascading delete
  per (tenant, user).
- HTTP hardening explicit (timeouts, body limits, Origin/Content-Type checks on
  the SSE surface); never SDK defaults.
- Gateway payloads are the only data leaving the box; per-scope redaction
  profiles can mask configured patterns before extraction calls (v1.x).

---

## 14. Non-goals (v1)

- No UI/console in v1 (the event stream, CLI, and Harbor console are the
  operator surface; the Stowage Console ships post-v1 as a Dockyard MCP App —
  §9.2).
- No cross-tenant federation (the Python predecessor's federation graphs are
  deliberately out). Team sharing *within* a tenant is in scope via grants
  (§5.3) — that covers the fleet use case without the graph machinery.
- No managed-cloud control plane in this repository (billing, org signup,
  cluster orchestration). The product keeps the seams clean for it — identity,
  metering events, per-scope ceilings — but the control plane is a separate
  codebase when it comes.
- No local embedding/reranker models (P5).
- No proxy/context-window-management mode (the CC-memory predecessor's proxy is
  clever but couples memory to a specific host's wire protocol; Harbor owns
  context assembly).
- No automatic persona synthesis, code-graph indexing, or plan tracking — those
  are agent-framework concerns (Harbor's), not memory-server concerns.

---

## 15. Phasing

See `docs/plans/README.md`. Waves: **W1 foundation** (scaffold/CI, config +
identity + telemetry, store seam + migrations, gateway seam + bifrost driver),
**W2 write path** (records + ingest API with outcomes, buffers, extraction +
reflection, reconciliation), **W3 read path** (lanes + fusion, scoring,
drill-down + feedback, optional rerank), **W4 lifecycle & sharing** (sweeps,
supersede chains + rollback, grants), **W5 surfaces & proof** (MCP server on
Dockyard, Go SDK + Harbor recipes + embedded mode, playbooks, eval harness,
hardening + open-source readiness).

---

## 16. Open questions

- **OQ-1:** Default embedding model + dims via Bifrost (pins index format) —
  decide in Phase 4 with a small retrieval-quality bake-off through the gateway.
- **OQ-2:** sqlite vector path — pure-Go HNSW from day one, or brute-force first
  and HNSW when scale demands? (Phase 9 spike; brute-force is correct and simple
  up to ~100k vectors/scope.)
- **OQ-3:** Buffer flush defaults (count/tokens/age) — tune in Phase 6 against
  the eval harness.
- **OQ-4:** Does `pending_confirmation` need a TTL that auto-resolves in favor of
  the newer memory? (Lean yes; decide in Phase 14.)
- **OQ-5:** Open-source license and timing — release is gated on the SOTA
  benchmark report (§12); pick the license (Apache-2.0 vs BSL-style
  cloud-protective) when the gate is in sight. Private until then.
- **OQ-6:** Playbook delivery for prompt caching — exact stability contract
  (section ordering, append-bias rules) so host-side KV caches stay warm;
  decide in the playbooks phase with measurements.
- **OQ-7:** Grants and contribute-mode reconciliation — when a teammate's agent
  contributes to a granted pool, whose trust hierarchy applies? Decide in the
  grants phase.
