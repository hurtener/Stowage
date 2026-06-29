# Decisions log (append-only)

Format: `D-NNN — title` / date / context / decision / consequences.
A change to a settled decision is an RFC PR plus a superseding entry, never an edit.

---

## D-001 — Project name: Stowage

2026-06-10. The memory server joins the Portico/Harbor/Dockyard ecosystem and
needed a dock-vocabulary name. **Stowage** — the storage of cargo aboard a
vessel. The Python predecessor's project name must not appear anywhere in this
repository (naming constraint set by the owner); refer to it as "the Python
predecessor".

## D-002 — Module path `github.com/hurtener/stowage`, repo `Stowage`

2026-06-10. Lowercase module path (Go convention) even though the sibling
Harbor uses a capitalized path; GitHub resolves case-insensitively, and we
prefer the conventional form for a fresh module. Binary name: `stowage`.

## D-003 — Clean-room redesign; no predecessor files

2026-06-10. No code, schema, prompt, or doc files from the Python predecessor
or the CC-memory predecessor are copied or vendored. Ideas transfer through
`docs/research/` briefs written for this repo. Enforced by review +
`drift-audit` forbidden-names check.

## D-004 — Go 1.26, CGo-free shipped binary

2026-06-10. Matches Harbor's toolchain and Dockyard's no-CGo discipline.
`modernc.org/sqlite` for the sqlite driver; `-race` test runs are the only
CGo-enabled builds.

## D-005 — No local models; gateway seam with Bifrost as first driver

2026-06-10. The Python predecessor's local SentenceTransformers + CrossEncoder
stack is the single largest deployment/perf liability. All embeddings and LLM
calls go through `internal/gateway`; first driver targets Bifrost
(OpenAI-compatible), the same gateway Harbor fronts. Future providers are new
drivers, never new call sites (RFC P5).

## D-006 — Fidelity first: verbatim records + provenance drill-down

2026-06-10. Adopted from CL-Bench (brief 04): lossy extraction without a
recovery path is the defining failure of benchmarked memory systems. Verbatim
records are append-only; every memory carries provenance; retrieval exposes
drill-down (RFC P1).

## D-007 — Engram pipeline shape: topics → buffers → extract → reconcile → commit

2026-06-10. Adopted from brief 03. Topics gate extraction (noise never enters);
reconciliation is a constrained tool-call decision; buffers are the multi-agent
write surface; ingest is fire-and-forget (RFC P2, §4.1, §6).

## D-008 — Lifecycle model from the CC-memory predecessor

2026-06-10. Six utility counters, decay with utility-grown stability,
contradiction boost, trust-gated supersede, anticipated-queries lane, hub
dampening, cooldown, quarantine (brief 02). Modification: decay blends
scope-activity turns with wall-clock time to fix the dormant-project blind spot.

## D-009 — Store seam with sqlite + postgres drivers; ~12-table target schema

2026-06-10. Dockyard's interface+factory+driver discipline with a shared
conformance suite. sqlite uses a dedicated writer goroutine (the CC-memory
predecessor documented multi-second lock waits under concurrent writers).
Postgres uses pgx + pgvector. Deliberate schema budget (~12 tables) against the
Python predecessor's 48+.

## D-010 — Identity quadruple and scope enforcement in the store layer

2026-06-10. Scope (tenant/project/user/session) matches Harbor's identity
shape. No unscoped query API exists (RFC P3); hard isolation at tenant/user,
soft scoping below, privacy zones gate promotion/export only.

## D-011 — Doc-driven build methodology inherited from Dockyard

2026-06-10. RFC > phase plans > CLAUDE.md/AGENTS.md > briefs > code comments;
phase plans from template with binding acceptance criteria; smoke scripts;
preflight gate; decisions log; glossary; mirror rule. CLAUDE.md adapted from
Dockyard's with project-specific sections retargeted.

## D-012 — Commits unsigned in this repository

2026-06-10. The owner's signing key is work-scoped; this is a personal-account
project. `commit.gpgsign=false` and `tag.gpgsign=false` set in local git
config; identity is the hurtener account.

## D-013 — Private repository; license deferred

2026-06-10. Private under `hurtener/Stowage`. License selection is deferred to
any future publication decision (RFC OQ-5); no LICENSE file until then.

## D-014 — Eval harness is a release gate

2026-06-10. From CL-Bench's gain metric: `stowage eval` ships in-tree, and
negative gain on the standard scenarios blocks release (RFC §12, Phases 13 and 20).

## D-015 — Small MCP surface (6 tools)

2026-06-10. Both predecessors grew sprawling surfaces (50+ endpoints / 70+
tools). Stowage's MCP surface is deliberately six tools; additions require an
RFC amendment. *(Amended by D-018: `memory_playbook` makes it seven.)*

## D-016 — Team sharing via grants, not federation

2026-06-11. Fleet use cases need team-level shared memory; the Python
predecessor solved it with federation graphs + RBAC (8+ tables) — too heavy.
Stowage: a **grant** gives a named group read/contribute access to a slice of
an owner scope, capped by privacy-zone ceiling, optional redaction, enforced in
the store layer like scopes (P3). Cross-tenant federation stays out of scope.
RFC §5.3; own phase (16) in Wave 5.

## D-017 — Reconciliation is reversible; rollback API

2026-06-11. Every destructive reconciliation op (update/merge/supersede) must
be invertible from its event within retention: sources kept as `superseded`,
prior content stored on update events, `POST /v1/memories/{id}/rollback`
restores and tombstones with its own event. The LLM gets to be wrong
recoverably. RFC §6; lands with Phase 15.

## D-018 — ACE built in: outcomes, reflection, deterministic playbooks

2026-06-11. From brief 05 (arXiv 2510.04618). Three capabilities: (1)
outcome-tagged ingestion (success/failure + execution feedback; Harbor task
events as the label-free source); (2) a reflection extraction mode producing
`strategy`/`failure_mode` memories, reconciled like any candidate, with a
multi-epoch re-reflection sweep; (3) `GET /v1/playbook` — deterministic,
sectioned, budget-packed assembly with **no LLM in the assembly path**
(context-collapse defense) and append-biased output for prompt caching. Adds
the `memory_playbook` MCP tool (amends D-015). RFC §6a.

## D-019 — Harbor: speak the protocol, don't build on the runtime

2026-06-11. Stowage's core pipeline is channels + supervision, never Harbor
flows/planner (dependency direction: Harbor depends on Stowage; Stowage must
run standalone). Stowage adopts Harbor's protocol surface instead: identity
quadruple, bus-shaped `memory.*` events, `llm.cost.recorded` governance
semantics. Stowage ops appear as tools *inside* Harbor flows (consolidation,
reflection recipes). The eval harness's agent loop runs on Harbor — the
canonical "Harbor powering a non-agent" showcase. RFC §10.

## D-020 — MCP surface built with Dockyard; console as a Dockyard MCP App

2026-06-11. Phase 17 implements `stowage mcp` with Dockyard (contract-first
typed tools, generated schemas, inspector, validate gates) — Stowage becomes
Dockyard's first external consumer. Post-v1, the operator console ships as a
Dockyard MCP App. RFC §9.2.

## D-021 — Postgres is the principal store; sqlite is the embedded driver

2026-06-11. Refines D-009: postgres (pgx + pgvector) is the recommended
production/fleet/managed-cloud driver; sqlite (modernc, pure Go) is the
embedded/portable driver and must remain CGo-free forever. Both pass the same
conformance suite; the seam is the contract and future backends are new
drivers. RFC §8.1.

## D-022 — Embedded library mode is a supported deployment

2026-06-11. `sdk/stowage` in-process mode (full server in the host process, no
daemon, no network) is a first-class deployment, not a test convenience. Target
picture: Harbor agent + Stowage inside a Wails desktop app. This is a standing
reason behind the no-CGo rule. RFC §2.

## D-023 — Open-source gated on SOTA benchmarks; managed cloud is the business

2026-06-11. Strategy: open-source release as an AI-first capability showcase,
gated on a reproducible report showing state-of-the-art results on public
memory benchmarks (LoCoMo-style ≥ 0.86, CL-Bench gain, ACE online-adaptation
scenarios); monetization via a managed cloud (Engram-style). Consequences:
multi-tenancy/isolation/metering are first-class from day one; everything in
the repo is written as if public (with D-003 hygiene); the control plane is a
separate future codebase (RFC §14); license choice is OQ-5, decided when the
gate is in sight.

## D-024 — Day-one signal capture (the schema contract)

2026-06-11. Roadmap capabilities (episodes, citations, causal links, RL
signals, proactive triggers) consume signals that **cannot be backfilled**:
which memories were injected into which response, when interactions actually
occurred, which branch a turn belonged to, task outcomes. The first migration
set therefore ships the full §8.1 schema (~19 signal-bearing tables —
including `injections`, `links`, `episodes`, `branches`, `suggestions`,
`api_keys`, `scope_settings`) even though their consuming features land in
W6–W8. Guardrail against sprawl: every column must be written by a W1–W3 hot
path and read by a named later phase; extensions beyond the inventory require
an RFC amendment. Amends D-009's table budget.

## D-025 — Injections are the attribution backbone

2026-06-11. Every retrieval records (response_id, memory_id, rank, score)
asynchronously. One table powers citations, response-level like/dislike,
citation-level feedback, use/fail counters, cache hot-set detection, reasoning
traces, and the gain metric. RFC §5.7.

## D-026 — Episodic memory, native temporal indexing, causal links

2026-06-11. Episodes (boundary detection + narrative construction with full
provenance), `occurred_at` indexed per scope from day one (time-window queries
native), typed link graph written by reconciliation from day one
(`supports`/`contradicts`) with inferred `caused_by`/`led_to` later, similar-
episode outcome contrast, cross-episode aggregation. RFC §6b; Wave 6.

## D-027 — Trust layer: citations, verification, traces, review queue

2026-06-11. Citation handles from injections; `POST /v1/verify` entailment
safeguard; uncited agent-generated knowledge parks as `pending_review` (never
silently becomes memory); reasoning traces reconstructable per response_id and
exportable as signed bundles for GDPR/regulatory and third-party audit;
support summary on every retrieval so agents can express calibrated
uncertainty. RFC §6c; Wave 7.

## D-028 — Proactive memory with governance

2026-06-11. The memory service, not the agent, owns proactive surfacing:
trigger engine → standard scoring → threshold + strict per-turn budget;
per-tenant/profile governance (limits, classes, opt-outs) in stored scope
settings, changeable at runtime; accept/dismiss tunes per-trigger confidence
through the six-counter machinery. Temporal pattern mining is an explicit
stretch phase behind the same governance. RFC §6d; Wave 8.

## D-029 — Branches: exploration without contamination

2026-06-11. Records carry `branch_id`; buffers and working memories are
branch-scoped; merge reconciles into the parent, discard expires working
memories (records remain — P1). Protects long sessions and extraction quality
from exploratory tangents. RFC §5.5.

## D-030 — Runtime API-key management; keys never live in config files

2026-06-11. Tenant/agent keys are store-backed with admin endpoints
(create/list/rotate/revoke/bulk-revoke) so onboarding and incident response
never require a restart or YAML edit. Constant-time verification; admin-class
keys for admin surfaces. RFC §9.1.

## D-031 — Read-path SLO and the hot–warm cache

2026-06-11. Binding target: retrieval p99 ≤ 150 ms (cache hit ≤ 20 ms) at
1,000 concurrent sessions on the postgres driver on reference hardware; the
SLO benchmark is a release gate alongside the gain metric. A (query-signature,
scope) result cache + injection-frequency hot set fronts the lanes,
scope-invalidated on writes (per-scope first — OQ-9). RFC §4.2.

## D-032 — Zero-config memory for every agent; Python client ships

2026-06-11. The SDK's job is that no agent re-implements memory plumbing: a
Harbor assemble option wires ingest/retrieve/feedback automatically; a thin
Python client serves the Python agent framework; MCP covers other hosts. An
integration needing hand-written plumbing is an SDK defect. RFC §9.3; Phase 18.

## D-033 — The adversarial scope cut: launch track vs post-launch tracks

2026-06-11. Adversarial review verdict: the 28-phase plan had drifted from
"best memory server for agent builders" toward an enterprise platform roadmap.
Agent builders choose on setup time, retrieval quality (published benchmarks),
latency, cost, and API ergonomics — not on audit hooks or review queues.
**Launch (v1.0) = phases 01–21**: all differentiators (single binary +
embedded mode, fidelity + drill-down, utility scoring, fire-and-forget +
buffers, injections attribution + citations v1, reversibility, grants,
playbooks, SLO'd performance) plus the proof. **Deferred** (signals already
captured by D-024, so zero structural cost): episodes/causal inference
(v1.1), verification/review/traces (v1.2), proactive engine (v1.3), pattern
mining + console + control plane (backlog). RFC §15.

## D-034 — The five-minute rule: zero-config start, profiles, runtime knobs

2026-06-11. Adoption requirements (RFC §9.4): `stowage serve` works with one
secret env var (embedded sqlite, tuned defaults); production adds only a
postgres DSN; three profiles (assistant / coding-agent / fleet) bundle tuned
knob values; tunables live in scope_settings and change at runtime;
`stowage config explain` shows every effective value + provenance; every new
knob ships with default + profile placement + docs in the same PR (the knob
guardrail — the CC-memory predecessor's 50-knob paralysis is the cautionary
tale). Time-to-first-memory < 5 min is a scripted launch smoke (Phase 21).

## D-035 — Evaluation at launch and continuous; the public benchmark suite

2026-06-11. Supersedes the eval-last sequencing (kept D-014's gate). The eval
harness lands immediately after the read path (Phase 13) and runs in CI as a
**benchmark gate** — any later phase that regresses a benchmark or the SLO
does not merge. The suite is the set competitors publish on: **LongMemEval,
LoCoMo, ConvoMem, MemBench**, plus the gain harness and SLO; per-question
results committed; one-command reproduction; launch report includes a
comparison table against published competitor numbers (mempalace's
benchmark-led positioning is the model — brief 06). RFC §12.

**Update (D-095, 2026-06-19):** the SLO portion of this gate is refined — the latency
SLO is a **reference-hardware** release gate (`make slo`, fails on a p99 regression),
deliberately not part of the noisy per-PR CI matrix; the eval benchmark suite remains the
per-PR CI gate. See D-095 for the D-031↔D-035 reconciliation.

## D-036 — Gateway-free degraded retrieval

2026-06-11. From brief 06's zero-API-call mode: the lexical,
anticipated-queries, and structured lanes require no gateway, so retrieval
degrades to those lanes (flagged) instead of failing when the provider is
unreachable; ingest keeps appending and derivation catches up via re-enqueue.
Critical for embedded/desktop (D-022) and for the "memory is infrastructure"
claim. Also adopted from brief 06: temporal-proximity boosting as a Phase 10
scoring input. RFC §4.2.

## D-037 — Timestamps as INTEGER unix-millis; ULIDs as TEXT; counters as INTEGER

2026-06-11. Portable type policy for both drivers: timestamps stored as INTEGER
unix-millis (uniform semantics across sqlite and postgres, no dialect-native
timestamp quirks); IDs are ULIDs stored as TEXT (sortable, human-readable, no
UUID extension required); the six CC-memory utility counters
(match/inject/use/save/fail/noise) are dedicated INTEGER columns (not a JSON
blob). Cross-driver conformance semantics are uniform.

## D-038 — memory_vectors deferred to Phase 09

2026-06-11. The memory_vectors table (embedding storage for pgvector) is
deferred to Phase 09. Embeddings are recomputable from content — they are
caches, not signals — so deferral is not a D-024 violation. This keeps the
pgvector extension and Phase 03's CI dependency-free. The RFC §8.1 table
inventory is otherwise complete. (Deliberate deviation from the full §8.1
inventory; footnote added to docs/plans/phase-03-store-schema.md.)

## D-039 — Coverage override: pgstore band 81 (temporary, tracked)

2026-06-11. CLAUDE.md §11 sets store drivers at 85. pgstore conformance +
EXPLAIN tests reach 81.4 %; the remainder is pgx error branches not reachable
against a healthy service-container database (connection/Exec failures mid-
transaction). Per the documented-override rule (class: hermetic-unreachable;
reason above) the band is set to 81 in `scripts/coverage.json` — to be raised
back toward 85 in Phase 09 when the vindex work adds pg error-path tests.
sqlitestore remains at 85 (achieved).

## D-041 — stdlib http.ServeMux only — no router dependency (Phase 05)

2026-06-11. The Phase 05 HTTP surface (`internal/api`) uses Go 1.22+
`net/http.ServeMux` patterns exclusively (method+path routing, `{id}` path
values). No third-party router is introduced. The API surface is small by
design (D-015); stdlib patterns suffice and keep the binary CGo-free. This
settles the provisional D-040 number noted in the phase-05 plan
(`docs/plans/phase-05-records-ingest.md`), which has been updated to D-041.

## D-040 — OpenAI-compatible wire format driver (base_url-agnostic)

2026-06-11. The gateway driver uses the OpenAI-compatible wire format
(`POST {base_url}/chat/completions` with `response_format: json_schema strict`,
`POST {base_url}/embeddings`) and is base_url-agnostic: it works against
OpenRouter, Bifrost, or any OpenAI-compatible endpoint. Provider-specific
drivers are added only when a wire format actually diverges from this baseline
(P5, RFC §7). All wire structs live exclusively in `internal/gateway/openaicompat`;
no other package may import them (CLAUDE.md §13). This supersedes the
placeholder in D-039's plan entry and is the definitive wire-format decision
for Phase 04.

**Amendment (Phase 09c, D-049):** the driver package was renamed
`internal/gateway/bifrost` → `internal/gateway/openaicompat` and the registry
key changed to `"openaicompat"`. The wire protocol, behavior, and live tests are
unchanged. The `bifrost` package path now denotes the real SDK integration
(see D-049).

## D-042 — Buffer trigger defaults (OQ-3 resolved)

2026-06-11. Resolves the open question OQ-3: what are the starting buffer-flush
trigger thresholds per profile? Chosen starting values (eval harness re-tunes
in Phase 13 per D-035):

| Trigger  | assistant | coding-agent | fleet |
|----------|-----------|--------------|-------|
| count    |        12 |           20 |    30 |
| tokens   |      1500 |         2500 |  4000 |
| max age  |      90 s |        180 s |  120 s |

These are profile-internal constants in `internal/config/profiles.go`
(`BufferTriggersForProfile`) — not operator-tunable top-level config knobs
(D-034 knob guardrail). The `internal/pipeline` package imports them via
`TriggersFromConfig`. Starting values reflect brief 03's multi-agent
accumulation model: assistant is conversational (small, frequent flushes);
fleet is high-throughput (larger batches, moderate age); coding-agent is
session-heavy (larger batches, long age). No new config keys are introduced.

## D-043 — Virtual default topic packs; any explicit topic disables the pack

2026-06-11. Phase 07 introduces extraction gating by topics (brief 03). The
open question was: what happens when a scope has no explicit topics configured?
Options considered: (a) extract nothing — zero-config deployments get no
memories; (b) require operators to always configure topics — too much friction
for personal assistant use; (c) apply compiled-in default packs at prompt-build
time, never persisted.

**Decision:** option (c). Two default packs are compiled in as named constants
in `internal/topics/packs.go`: `pack:preferences` (assistant profile —
personalisation preferences, communication style, durable personal facts) and
`pack:agent-learnings` (coding-agent / fleet — gotchas, patterns, decisions).
The pack is selected by the current profile. When the service's `ActiveTopics`
call finds no stored active topics for a scope, it synthesises the pack entries
in memory and returns them with `source="pack"`. No row is written to the
topics table. Any explicit active topic suppresses the virtual pack entirely
(all-or-nothing: having even one explicit topic means the operator is in
control). The `pack:off` sentinel key (status `active`) opts out of virtual
packs and short-circuits extraction without a gateway call.

**Consequences:** zero-config extraction works out of the box; virtual packs
are transparent (visible via `GET /v1/topics` with `source: pack`); packs
evolve by bumping the constant version, not by schema migrations; the
`pack:off` sentinel keeps the opt-out mechanism explicit rather than requiring
an operator to know to delete all topics to re-enable virtual packs.

## D-044 — Pre-filter thresholds + fast-add path

2026-06-11. Phase 08 reconciliation needs to avoid LLM calls on trivially
cheap cases. Three options considered for the order of pre-filters and the
zero-neighbor case: (a) always call the LLM; (b) call the LLM only when
neighbors exist; (c) call the LLM only when neighbors exist AND the candidate
is not a near-duplicate of any of them.

**Decision:** option (c) plus a documented threshold constant. The reconciliation
flow first checks for an exact SHA-256 hash match (`GetByContentHash`) — zero
cost, guarantees no duplicate content. It then retrieves structural neighbors
(`FindNeighbors`). If no neighbors are found, the candidate is committed as an
active `add` without any gateway call — the **fast-add path**. If neighbors
exist, bigram-Jaccard similarity against each neighbor is computed; a score
≥ 0.85 (the **near-dup threshold**) treats the candidate as the same fact and
discards it without a gateway call. In both discard cases `IncrementCounter(match)`
bumps the matched memory's utility counter so retrieval rank reflects proven reuse.

The trust gate (supersede/update on a high-trust target) uses the formula:
`trust = (0.5 + log1p(use + 2·save)) · source_multiplier · (importance/3)`.
`trust < 1.0` → apply silently; `1.0–3.0` → apply + `reconcile.warned` event;
`≥ 3.0` → the incoming memory commits as `pending_confirmation` with
`supersedes_id` set; the target stays `active` until Phase 15 resolves it.

The **contradiction boost** for superseding memories sets
`importance = max(candidate.Importance, 4)` and adds a stability constant
(`contradictionBoostStabilityDelta = 1.0`, representing ~45 days in the
normalised decay time-constant) so the correction immediately outranks what it
corrected.

All thresholds are compile-time constants with documentation comments naming this
decision; they are profile-internal knobs revisited with eval data (D-035).

**Consequences:** ~40 % reduction in gateway calls for typical workloads (per
brief 02 estimate); exact-dup and near-dup filtering are cheap O(1) and O(N)
operations respectively; the near-dup threshold of 0.85 is conservative enough
to avoid false positives on genuine updates while eliminating near-verbatim
re-statements; the trust gate prevents high-value memories from being silently
overwritten by low-signal candidates.

## D-045 — `Memories().Commit` is the single transactional unit for reconciliation

2026-06-11. Reconciliation outcomes require atomic writes: a memory row,
junction rows (entities, keywords, anticipated-queries), provenance rows, link
rows, status transitions on target memories, and audit event rows — all of which
must either all commit or all roll back. Earlier phases used individual store
methods (Insert, SetStatus, InsertLinks, AddProvenance, Emit) issued sequentially;
a crash between any two would leave partial state.

**Decision:** introduce `Memories().Commit(ctx, scope, CommitSet) error` as the
single transactional unit for all reconciliation outcomes. Each action
(add/update/merge/supersede/discard/park) maps to exactly one `Commit` call.
The SQLite driver executes the full `CommitSet` inside one `exec()` closure
(= one `sql.Tx`). The PostgreSQL driver opens a `pgx.Tx` via `pool.Begin`.
Event rows are written directly into the same transaction (not via
`EventStore.Emit`) so they always carry the latest state and participate in the
same atomicity guarantee.

`CommitSet.Events` must be populated by the reconcile package **before** calling
`Commit` — events carry prior-state JSON snapshots needed for D-017 reversibility.
`CommitSet.FaultHook` is a test-only mid-commit injection point: calling it after
the primary memory row is inserted but before secondary writes exercises the
rollback path without requiring process termination.

**Consequences:** crash safety is guaranteed from Phase 08 onward; D-017
rollback (Phase 15) can reconstruct prior state purely from event payloads;
the `Commit` method is the only place in the codebase that holds an open
transaction for memory mutations — all other mutation helpers (Insert, Update,
SetStatus) remain available for non-reconciliation paths (e.g. sweep, API
admin ops).

## D-046 — Vector storage as float32-LE BLOB/BYTEA; brute-force cosine in Go for v1

2026-06-11. Phase 09 adds a vector retrieval lane. We need embeddings stored
per-memory and a similarity search mechanism.

**Decision:** store vectors as `BLOB` (SQLite) / `BYTEA` (PostgreSQL) in a
new `memory_vectors` table using little-endian float32 encoding.
`internal/vindex` wraps `store.VectorStore` and performs brute-force cosine
similarity in Go after a scope-filtered `Scan`. No pgvector extension is
required; CI stays on `postgres:17`. The `Index` seam accepts alternative
drivers (e.g. gohnsw, pgvector-native) without interface changes.
`vindex.ErrDimsMismatch` is returned for upserts where `len(vec) != dims`.

**Consequences:** v1 is correct to ~100 k vectors/scope; HNSW and pgvector
arrive as new drivers behind the same seam. CI remains free of pgvector.
Float32-LE encoding is deterministic across Go versions and architectures.

## D-047 — Enriched-text embedding; async post-commit; dead-letter + backfill

2026-06-11. We need to populate vectors for memories after they are committed
and keep them current without blocking the reconcile path.

**Decision:** the embed text for a memory is `content + entities + keywords +
anticipated_queries` joined with spaces (enriched text). Embedding is async:
reconcile calls `Embedder.Enqueue` non-blockingly after each successful commit
of an active memory (add/fast-add/supersede/update/merge). If the channel is
full the job is dropped with a log warning; the backfill sweep recovers it.
`Embedder.BackfillSweep` runs at boot (immediate pass) then on a jittered
5–7-minute ticker, calling `VectorStore.ListWithoutVectors` (limit 64) and
re-enqueueing. Gateway embed failures are logged at Warn; retrieval still
serves lexically for that memory (degraded per-memory, not per-system).

**Consequences:** no commit is ever blocked by an embed call (P2 spirit);
the backfill sweep provides crash-recovery for missed embeds; the enriched
text improves semantic recall without requiring schema changes.

## D-048 — HNSW as default vector search (owner directive); sidecar filtering; deletion semantics

2026-06-11. The brute-force cosine scan established in D-046 is correct to
~100k vectors per tenant but does not scale to the volumes Stowage is designed
for. An owner directive requires HNSW as the default from Phase 09b.

**Decision:** adopt `github.com/coder/hnsw v0.6.1` (pure Go, CGo-free,
satisfies §5 P1) as the default vector-search driver (`vindex.driver = "hnsw"`).
The brute-force driver remains as the `"brute"` conformance oracle and debug
fallback. BLOB storage and no-pgvector stance (D-046) are unchanged — the HNSW
graph is an in-memory index rebuilt from BLOBs on first access per tenant.

**Deletion semantics (finding):** `Graph.Delete(key)` in coder/hnsw v0.6.1
performs a hard delete — it removes the node from all layers and replenishes
neighbourhood connectivity. No tombstone set is required.

**Duplicate-key workaround:** `Graph.Add` panics when called with a key that
already exists (internal postcondition `Len == preLen+1` fails when Add
internally deletes-then-reinserts). Workaround: call `graph.Lookup` before
`graph.Add` and explicitly `graph.Delete` if the key exists.

**Search quality (finding):** coder/hnsw v0.6.1 uses a strict "no improvement
over current best result" termination condition that limits recall for random
unit vectors when `fetchN << graphLen`. For graphs with `Len ≤ overFetchCap`
(2048), the driver requests all nodes from `graph.Search` to achieve
near-brute-force recall (≥ 0.99 empirically). For larger graphs, approximate
ANN recall is adequate for real clustered embeddings from language models.

**Consequences:** per-tenant in-memory graphs with per-entry RWMutex; metadata
sidecar (memoryID → scope cols, kind, createdAt) maintained for filtered search
without store round-trips except when pendingMeta is non-empty; lazy build from
`Vectors().Scan` on first Search per tenant; `vindex.driver` config key with
`hnsw` as the validated default.

## D-049 — Real Bifrost SDK driver; HTTP-client driver renamed openaicompat (Phase 09c owner directive)

2026-06-11. The Phase 04 driver named "bifrost" was a from-scratch OpenAI-compatible
HTTP client — useful, but not the intended direct SDK integration. Owner directive
(Phase 09c): remediate with minimum blast radius.

**Decision:**

1. **Rename** `internal/gateway/bifrost` → `internal/gateway/openaicompat`; registry
   key "openaicompat"; config enum "openaicompat". The driver itself is unchanged —
   it remains the right tool for OpenRouter and any OpenAI-compatible endpoint
   (D-040 amended: `bifrost` package path changes, everything else is preserved).

2. **Add** `internal/gateway/bifrost` backed by `github.com/maximhq/bifrost/core`
   (pinned v1.5.15, pure Go, CGo-free — Harbor-proven). The SDK driver implements
   the full `gateway.Gateway` interface (Complete, Embed, Probe, Close) and is
   registered as driver `"bifrost"`. Seam-level services (batching, cache, circuit
   breaker, validation+retry, metering — Phase 04) apply to the SDK driver unchanged
   because they sit above the driver boundary.

3. New config key `gateway.provider` (required when `gateway.driver=bifrost`;
   validated at boot; empty → boot error citing `config.gateway.provider`). The
   key holds any provider name accepted by the SDK (openai, anthropic, gemini, …).

4. Test seam: `bifrostClient` interface wraps SDK's `ChatCompletionRequest` and
   `EmbeddingRequest` methods; unit tests inject a fake without real network.

**Consequences:** zero call-site changes outside `internal/gateway` and config wiring
(P5 preserved); `make build` stays CGo-free; Harbor parity for the SDK driver path;
`openaicompat` remains the live-validated OpenRouter path (live tests unchanged).

## D-050 — Scoring trust: rank multiplier vs supersede-gate (distinct concerns)

2026-06-11. Two unrelated systems both use the word "trust" in Stowage; conflating
them causes design confusion.

**Supersede-gate trust (Phases 08–09):** `memory.trust_source` is a write-time
quality signal that guards supersede eligibility. An `llm_extracted` memory may not
supersede a `user_stated` memory because the higher-trust source is assumed to be
more reliable for long-lived facts. This is a _write-side_ concern (D-025, D-044
fast-add path, D-045 Commit transaction).

**Rank-multiplier trust (Phase 10):** `scoring.TrustMultiplier` re-weights the
retrieval score of a memory at _read time_. The multipliers are:

| trust_source     | multiplier |
|------------------|------------|
| user_stated      | 1.25       |
| agreed_upon      | 1.15       |
| agent_suggested  | 1.00       |
| llm_extracted    | 0.95       |
| (unknown/other)  | 1.00       |

The values reinforce the same ordering as the supersede-gate but operate on a
different axis — they boost retrieval relevance, not data lineage. A `user_stated`
memory is more likely to surface in top results because users have higher intent
signal, not merely because it cannot be overwritten.

**Decision:** keep the two systems separate. `memory.trust_source` is never changed
by the scoring layer. Score multipliers are defined as pure constants in
`internal/scoring` (P5 no-side-effect guarantee); the supersede-gate remains in
`internal/reconcile`. Documentation (glossary, plans) uses "trust multiplier" for
the read-side factor and "trust source" for the write-side lineage field.

**Consequences:** clean separation of read and write concerns; scoring remains a
pure function (no store access); future calibration of read-side multipliers does
not risk breaking the supersede-gate invariant.

## D-051 — Citation handle == injection ULID; response_id is caller-supplied or generated

**Context (Phase 11):** The retrieve envelope needs per-item citation handles that
clients can use to invoke `/v1/drilldown`, `/v1/feedback`, and
`/v1/citations/resolve`. A separate citation token namespace would add complexity
with no benefit.

**Decision:** The citation handle is the injection row's ULID (the primary key of
the `injections` table). One injection row is written per returned memory per
Retrieve call. The row's `id` field is the handle emitted in the v1 envelope's
`items[i].citation` field.

The `response_id` is a ULID supplied by the caller or generated by the server when
absent. It is echoed at the top level of the v1 response and used to look up all
injections for a response (enabling response-level feedback via
`POST /v1/feedback {response_id, signal}`).

**Consequences:**
- No separate token table or translation layer; the injection store IS the
  citation store.
- Citation handles are therefore ULIDs and are k-sortable by insertion time
  (convenient for debugging).
- Injection rows are append-only; once written they are never mutated except for
  the `feedback` column (set to `"wrong_citation"` by `MarkWrongCitation`).
- The async injection writer (bounded channel, drop-with-metric) ensures
  Retrieve latency is unaffected by storage backpressure (P2 fire-and-forget).

## D-052 — Rerank blend constants, slice size, and degradation contract

2026-06-11. Phase 12 adds a cross-encoder rerank pass to the `precise` retrieval
profile. Open questions: what are the blend weights, the slice size, and the
degradation contract?

**Decision:**

- **Slice size:** `rerankSlice = 24` — the top-24 Phase-10 scored candidates are
  sent to the rerank model. Capped at `rerankDocBudget = 32` (maximum documents
  per call). The rest of the scored set is appended un-reranked.
- **Blend formula:** `final = 0.6 × rerankNorm + 0.4 × phase10Norm` where each
  score is normalised to [0, 1] against the maximum in the candidate slice.
  Both weights are named constants (`rerankBlendRerank`, `rerankBlendScore`) in
  `internal/retrieval/rerank.go`; they are profile-internal knobs, not config
  keys (D-034 guardrail). The eval harness re-tunes them in Phase 13.
- **Degradation contract:** any error from `gateway.Rerank` (including a tripped
  breaker) sets `DegradedRerank: true` in the response envelope and returns the
  candidates in their original Phase-10 utility-score order. The rerank failure
  is never fatal to retrieval. Callers and agents can observe the flag.
- **Profile gating:** only `precise` enables rerank (`Profile.EnableRerank = true`).
  `balanced` and `broad` do not call the rerank model.

**Consequences:** retrieval quality on focused queries (precise profile) is
improved by the cross-encoder while the blend preserves signal from the Phase-10
utility function; the degradation contract makes the model provider entirely
optional (D-036 spirit extended to reranking).

## D-053 — Cache invalidation via per-scope generation counters (OQ-9 resolved)

2026-06-11. Phase 12 ships the hot–warm result cache (D-031). OQ-9 asked: what
invalidation mechanism prevents stale results after a reconcile commit?

**Considered options:**

1. **Scan-based:** on every commit, find and delete all cache entries for the
   scope. O(N) scan against the LRU. Risky under high commit rate.
2. **TTL-only:** let entries expire after 60 s; no explicit invalidation. Simple,
   but stale results survive up to 60 s after a write — unacceptable for coherent
   sessions.
3. **Per-scope generation counters:** a monotonic uint64 per scope string, bumped
   O(1) on `InvalidateScope`. Cache entries store their generation at `Put` time;
   a `Get` compares the stored generation to the current one — stale entries are
   immediately invisible with no scan. Combines with a 60 s TTL for bounded
   memory growth.

**Decision:** option 3 (generation counters). Implemented in `ResultCache`
(`internal/retrieval/cache.go`). The `ScopeInvalidator` interface (one method:
`InvalidateScope(scope)`) is defined in `internal/retrieval` to avoid a
retrieval→reconcile import cycle.

**Wiring:** the reconcile stage calls `cache.InvalidateScope(scope)` after every
content-changing commit (add, update, supersede, merge — not discard or counter
bumps). The reconcile stage receives the invalidator as an optional dependency;
it is nil when running without the result cache. No in-process event bus exists
yet; the narrow interface is the documented choice over event-driven subscription
until Phase X wires a general bus (see D-019).

**Hot-set v1:** the injection-frequency `HotSet` is metrics-only in this phase.
The retrieval fast-path consumption of hot-set IDs for pre-warm GetMany batches
is measured by the SLO rig before wiring deeper — this avoids premature complexity
and is documented here as the Phase 12 scope limit. The SLO rig informs Phase 13.

**Consequences:** invalidation is O(1) on both read and write paths; a cross-scope
cache hit is structurally impossible because the scope string is part of both the
cache key and the generation key (AC-5 of Phase 12 acceptance criteria);
`STOWAGE_CACHE_OFF=1` provides a debug escape hatch without removing the cache
from production deployments.

## D-054 — Single-flush per conversation in CI eval (OQ-10 resolved)

2026-06-11. Phase 13 adds the CI eval harness. OQ-10 asked: should multi-session
conversations flush once per session or once per conversation?

**Considered options:**

1. **Per-session flush:** each session flushes its own buffer independently.
   The mock script has one entry per session. Provenance placeholders must
   reference the correct record IDs for each session (e.g. `{{.R3}}` for the
   second session's first turn). Complex, error-prone template maintenance.
2. **Per-conversation flush (single flush):** all sessions share one buffer key
   (`buffer_key = conv.ID`). A single flush collects all turns across sessions.
   The mock script has one entry per conversation with all candidates. All
   provenance placeholders use `{{.R1}}` (the first record in the batch). Simple,
   deterministic, zero template maintenance overhead.

**Decision:** option 2 (single flush per conversation). All sessions of a
conversation are ingested with `buffer_key = conv.ID`. One explicit flush
produces one `Complete` call, consuming one mock script entry. Mock scripts for
multi-session conversations (conv-03, conv-04, conv-05, conv-08) are consolidated
to a single entry with all candidates, all using `{{.R1}}` provenance.

**Consequences:** the harness runner is simpler; the mock script format is uniform
across single-session and multi-session conversations; the provenance constraint
(`minItems: 1` with a valid record ID) is always satisfied by `{{.R1}}`.

## D-055 — Gate-bite mechanism: lane-based filter over retrieved items (AC-3)

2026-06-11. Phase 13 must include a test proving the benchmark gate "bites" when
a component regresses (AC-3). The cleanest real regression to simulate is the
lexical lane going dark.

**Considered options:**

1. **Mock gateway returns empty candidates:** force zero extraction, no memories
   stored. Gate trivially fails. Too blunt — does not test the retrieval path.
2. **Reduce retrieve limit to 0:** causes all questions to miss. Fake — bypasses
   real retrieval logic entirely.
3. **Lane filter in the harness runner:** when `RunConfig.DisableLane = "lexical"`,
   the runner filters out any retrieved item that has "lexical" in its `lanes`
   slice before scoring. No production code is modified. The retrieval pipeline
   runs normally; only the scorer's input changes.

**Decision:** option 3 (lane filter in the harness runner). `filterByLane` is
a pure in-memory post-processing step on the retrieve response. It is activated
by the test-only `RunConfig.DisableLane` field (never set in production). The
`TestEvalCIGateBites` test asserts the degraded score is strictly lower than the
normal score, proving the gate would detect the regression.

**Wiring:** `STOWAGE_EVAL_DISABLE_LANE` env var read by the runner at test start;
documented in the harness honesty constraint (AC-7) as a test-only hook that does
not touch production behaviour.

**Consequences:** AC-3 is satisfied deterministically; the lane filter exercises
the real `include_lanes=true` retrieve path; no production code paths are
special-cased.

## D-056 — Coverage override: vindex/hnsw band 82 (interleaving variance, tracked)

2026-06-12. hnsw coverage varies 80–86 % across identical runs: the
per-tenant lazy-build vs incremental-upsert concurrency means goroutine
interleaving decides which branches execute under -race. Deterministic
branch tests were added in Phase 12 (raising the floor) but variance
persists. Per the CLAUDE.md §11 override rule (class: scheduling-dependent
branch execution): band set to 82. Follow-up tracked: make the concurrent
branches deterministically reachable (injected scheduler hooks) and restore
85 — candidates for the Wave 5/6 checkpoint audit.

**Note (2026-06-13, D-067 Wave-A checkpoint — band RAISED to 85, variance
resolved).** The follow-up tracked above is done. The variance came from the
incremental-upsert path (built-graph `Upsert` of a new key → `pendingMeta` →
`Search` → `refreshSidecar`) being reachable only when goroutine interleaving
happened to order an upsert-into-a-built-graph before a Search; `refreshSidecar`
sat at 0 % otherwise. `TestHNSW_IncrementalUpsertRefreshSidecar`
(`internal/vindex/hnsw/driver_test.go`) now forces that sequence
DETERMINISTICALLY (build via Search → upsert new key → Search), independent of
`-race` scheduling. Verified stable: `go test -race -cover -count=5
./internal/vindex/hnsw/` reports **89.1 % on all five runs** (was 80–86 %
variable; ~83.7 % locally before the test). Band in `scripts/coverage.json`
raised 82 → 85 (the §11 standard for conformance-tested drivers, ~4 pt headroom
under the stable 89.1 %). Not a silent lowering — a real fix plus a justified
raise.

## D-057 — Advisory locks for lifecycle sweeps (operator-level guard)

2026-06-12. The lifecycle Manager runs 4 sweeps (decay, dedupe, rollup,
re-enqueue) as periodic goroutines. In multi-replica deployments the same
sweep could run concurrently on multiple nodes, causing duplicate expirations,
double-merge artefacts, or duplicate re-enqueue items.

**Options considered:**
1. **Distributed mutex (Redis / etcd):** adds an external runtime dependency
   that contradicts the "no new dependencies" directive and complicates
   single-node deployments.
2. **Database advisory lock (pg_advisory_lock / no-op on SQLite):** zero new
   dependencies, uses the existing store connection, naturally scoped to the DB.
3. **No locking, accept duplicates:** sweeps are idempotent at the record level
   (SetStatus is idempotent, re-enqueue of already-processed record is a no-op
   because ListUnprocessed filters on processed_at == 0). Acceptable, but
   causes unnecessary churn and noisy logs under scale.

**Decision:** option 2 (advisory lock). Each sweep acquires a distinct 64-bit
lock key (decay=0x1401, dedupe=0x1402, rollup=0x1403, reenqueue=0x1404) via
`store.Store.Ops().AdvisoryLock()`. On SQLite the lock is a no-op (single
writer, no multi-replica). On Postgres it maps to `pg_try_advisory_lock`; if
the lock is held by another replica the sweep logs a warning and skips the
cycle (back-off on next ticker fire). Sweeps remain idempotent regardless of
whether the lock fires, providing defence-in-depth.

**Consequences:** No external infrastructure required. Single-node deployments
(SQLite, single Postgres) are unaffected. Multi-replica Postgres deployments
avoid redundant sweep work without coupling replicas.

## D-058 — Grace period for decay expiry via valid_until (D-058)

2026-06-12. Decay factor for a memory can briefly dip to the floor (clamped
minimum) due to an access gap even for memories the user cares about. Expiring
on the first below-floor observation would produce false positives.

**Options considered:**
1. **Immediate expiry on first below-floor:** simple but too aggressive; any
   access gap causes permanent loss.
2. **Count consecutive below-floor sweeps in memory (requires mutable counter):**
   requires an additional column or in-memory state (not safe across restarts).
3. **Timestamp-based grace via valid_until field:** store a "must recover by"
   timestamp on first below-floor observation; expire only when that timestamp
   is passed on a later sweep. `valid_until` already existed on the Memory
   schema for validity-window semantics and is nullable.

**Decision:** option 3 (valid_until grace period). On first below-floor
observation the decay sweep sets `valid_until = now + grace_period` (default:
`DecayGraceSweeps × DecayInterval = 2 × 10m = 20 minutes`). If the memory
recovers (decay rises above floor, e.g. from a use event updating
`last_accessed_at`), `valid_until` is cleared. If the memory is still at or
below floor when `now > valid_until`, it is expired via `Commit(ActionDiscard)`
+ `SetStatus("expired")`. The grace duration is configurable via
`Profile.DecayGraceSweeps` and `Profile.DecayInterval`.

**Consequences:** Short access gaps do not trigger false-positive expiry.
Memories that are genuinely stale expire within `grace + 1 sweep interval` of
the second below-floor observation. The `valid_until` field is reused without
schema changes. The `idx_memories_valid_until` partial index (migration 0004)
makes expiry-candidate scans efficient.

## D-059 — Contribute-mode trust: pool owner's gates; contributor ≤ agent_suggested (OQ-7)

2026-06-12. When an agent holds a `contribute` grant targeting another user's
memory pool, the contributed content commits into the pool owner's scope. The
question (OQ-7) was how to prevent contributors from elevating trust beyond
what the pool owner has implicitly authorised.

**Options considered:**
1. **Block all trust propagation:** contributor memories are always quarantined
   until pool owner confirms. Correct but poor UX — every contribution requires
   manual review.
2. **Mirror pool-owner's trust gates:** new contributed memories enter as
   `llm_extracted` (the pipeline default). The pool owner's existing memories
   already have accumulated UseCount/SaveCount/TrustSource. The trust-gate
   supersession logic (`trustGatePark = 3.0`) naturally parks any contributed
   memory that would supersede a high-trust pool memory, requiring pool-owner
   confirmation.
3. **Introduce a separate TrustSource cap per record:** would require a new
   column on the `records` table and pipeline changes.

**Decision:** option 2 (pool owner's trust gates govern; contributor content
enters as `llm_extracted`, satisfying ≤ `agent_suggested`). Contributed records
are written into the pool owner's scope (project/user/session overridden by
`target_scope`); the reconcile stage processes them as normal pipeline output
with `TrustSource: "llm_extracted"`. The pool owner's accumulated trust scores
mean any supersession of a high-trust memory is parked automatically.
Cross-tenant contribute is unconstructible (same-tenant validation in
`grants.Service.CheckContributeGrant`).

**Consequences:** No schema changes required. Contribute mode 403s for
uncovered callers (no active contribute grant). Covered contributors write
into the pool; pool owner's trust gates handle the rest. Pool owner never loses
high-trust memories to a contributor without explicit confirmation (park path).

## D-060 — Granted reads resolve effective scopes per-request; zone ceilings at creation and read

2026-06-12. Team sharing requires granting a group read/contribute access to a
slice of an owner's memory pool. Two enforcement points were needed: (1) which
scopes a caller may read, (2) which memories within those scopes are visible.

**Options considered for scope resolution:**
1. **Materialise grant membership into a flat lookup table on change:** avoids
   per-request JOIN but adds a write path and invalidation logic.
2. **Per-request EffectiveScopes query:** a single SQL JOIN over
   `grants ⋈ group_members` filtered by caller's user_id and tenant. Slightly
   more expensive than a materialized table but always live (revocation takes
   effect on the next request without a separate invalidation step).

**Decision:** option 2 (per-request, single JOIN). `GrantStore.EffectiveScopes`
issues exactly one extra SQL query per retrieve call (measured: ≤1 extra query).
The result cache is bypassed for multi-scope requests so revocations are always
live. Zone ceiling is validated at grant creation (AC-2: personal/intimate
rejected; only public/work allowed) AND enforced in Go at read time as a
defense-in-depth predicate (`grants.ApplyCeiling`), which hard-caps at `work`
even if a mis-stored ceiling appears in the DB (AC-1).

**Zone ordering (canonical):** `public(0) < work(1) < personal(2) < intimate(3)`,
stored in `store.ZoneOrder` (single definition, D-060).

**Consequences:** Revocation is live on the very next retrieve (no stale cache
window). EffectiveScopes resolution is a single extra JOIN per request — bench
shows no regression on the hot path (no-grants common case fast-path: single
element slice, identical code to pre-Phase-15). Personal/intimate memories
never cross a grant even if mis-stored (AC-1 defense test in conformance suite).

## D-061 — Dockyard integration = runtime-library embedding; manifest/codegen skipped

2026-06-12. Phase 16 wires the MCP surface via `github.com/hurtener/dockyard`
v1.7.3 as a pure Go library dependency — `go get`, no replace directive, no
manifest/codegen workflow.

**Context:** D-020 anticipated a `dockyard validate` manifest gate. The working
pattern (examples/backend-tools-only) shows that the runtime `tool.Builder`
generates JSON Schema from Go types at registration time; the manifest/codegen
workflow applies only to scaffolded CLI projects, not library embedding.

**Decision:** Skip manifest + codegen. The in-repo schema goldens in
`internal/mcpserver/testdata/*.schema.json` (generated via `tool.Builder.Schemas()`
and compared in `TestSchemaGoldens`) are the contract drift gate, replacing the
`dockyard validate` step named in D-020. Golden regeneration: `UPDATE_GOLDEN=1 go
test ./internal/mcpserver/`.

**Amends:** D-020's "validate gates" wording.

**Consequences:** No codegen step in CI. Schema goldens fail on any contract type
rename (AC-6 mutation test). Dockyard dep is a normal public module dep, same
as any other.

## D-062 — Zero-config Harbor wiring = auto-registered tools + event-driven outcome capture

2026-06-12. Phase 17 adds a Harbor integration adapter. D-032 originally framed
"zero-config wiring" as ingest-on-turn via a per-turn middleware hook. Harbor
v1.3.1 exposes no per-turn middleware hook; turns are not a first-class concept
in the Harbor runtime.

**Options considered:**
1. **Middleware shim at the transport layer:** wrap Harbor's HTTP client to
   intercept request/response pairs. Fragile — depends on Harbor's internal
   transport, which is not a public API.
2. **Tool wrappers only (no outcome capture):** register memory tools via
   `PreRegisterTools`; skip outcome wiring. Simple but incomplete — no feedback
   loop from task results to memory quality signals.
3. **Tool wrappers + event-driven outcome capture (chosen):** register the seven
   memory operations as in-proc tools (`inproc.RegisterFunc`) dropped into
   `assemble.Options.PreRegisterTools` — ONE line for a Harbor app. Subscribe to
   `task.completed` / `task.failed` on Harbor's `EventBus`; on each event,
   ingest an outcome-tagged record AND apply a `use`/`fail` quality signal to
   all retrieval response IDs the task's runs produced. Correlation is tracked
   via an in-adapter map keyed by RunID, populated by the tool wrappers when a
   `memory_retrieve` call returns a response_id.

**Decision:** option 3. The adapter exposes two entry points:
`Tools(client) []ToolDescriptor` (tool registration slice) and
`WireOutcomes(ctx, bus, client)` (event subscription). Together they satisfy
the D-032 intent: memory is automatically wired with a single `assemble.Options`
change + one `WireOutcomes` call, with no per-call boilerplate.

**Amends:** D-032's "ingest-on-turn" framing. The mechanism is event-driven,
not turn-intercepting; the effect (automatic memory capture + outcome feedback)
is equivalent.

**Consequences:** The adapter is decoupled from Harbor internals and uses only
`sdk/events`, `sdk/tools`, and `sdk/tools/inproc` — all public APIs. Any future
Harbor per-turn hook can be layered on top without changing the adapter's
public surface.

## D-063 — adapters/harbor is a separate Go module

2026-06-12. Harbor v1.3.1 pulls in a 67-package dependency tree (OpenTelemetry,
gRPC, multiple cloud SDKs). Including Harbor as a dependency of the core stowage
module would force those packages on every stowage consumer, including lightweight
embedded deployments (D-022 Wails posture) and server-only deployments with no
Harbor integration.

**Options considered:**
1. **Single module, optional build tags:** use `//go:build harbor` to gate the
   adapter. Keeps one module but complicates the build matrix and doesn't
   prevent `go mod tidy` from downloading Harbor's deps.
2. **Separate Go module at `adapters/harbor`:** the adapter lives at
   `github.com/hurtener/stowage/adapters/harbor` with its own `go.mod` that
   requires Harbor v1.3.1 and pins stowage via `replace ../..` during
   development (published version on release). Core `go.mod` never mentions
   Harbor.

**Decision:** option 2. `adapters/harbor` is a standalone module released in
lockstep with the main module (same semver tag). CI builds and tests it in a
separate job (`cd adapters/harbor && go build ./... && go test ./...`) using
the replace directive; the public module proxy supplies Harbor at build time.
The local `go.work` (gitignored) wires both modules for development convenience.

**Consequences:** Core `go.mod` is provably Harbor-free (CI grep gate in AC-6).
Consumers of just the memory server never download Harbor's dependency tree.
The adapter module has its own test suite and coverage threshold (≥80%). New
adapter versions can lag the core module by one release without breaking
consumers of either.

## D-064 — Rollback contract: newest-event-only, atomic, tombstone = deleted

2026-06-12. `POST /v1/memories/{id}/rollback` (D-017's consumer, executing the
master plan's skipped slot 15) inverts the NEWEST reconciliation event
(`memory.updated`/`memory.merged`/`memory.superseded`) for the target memory.
Older events are unreachable until newer ones are unwound — chains unwind one
step at a time, newest-first, which also bounds cycles. The inverse runs as a
single atomic `ActionRollback` commit: full row restore (scalars +
entity/keyword/query junctions + provenance, replace semantics) from the
prior-state snapshot, result rows located via `superseded_by_id` and
tombstoned with status `deleted`. Merge rollback is all-or-nothing across all
sources (every sibling must carry its snapshot or the call 409s). Every
restored row emits `memory.rolled_back` carrying the PRE-rollback state, so
rollbacks are themselves auditable. Conflict guards return 409: double
rollback, downstream supersede of the result row, missing/unparseable
snapshot.

**Consequences:** the P4 reversibility promise is mechanically closed; the
reconciler (and the new confirm sweep) can be wrong recoverably. Requires
`EventStore.ListBySubject` + migration 0006 (subject index) on both drivers.

## D-065 — OQ-4 resolved: pending_confirmation auto-resolves via the supersede path

2026-06-12. Parked (`pending_confirmation`) memories resolve three ways: (1)
TTL — after `confirmTTL` (default 72 h, profile knob) the NEWER memory wins
(OQ-4's lean-yes); (2) repeated independent extraction — identical-content
re-extractions increment the parked row's match counter (new pre-commit
parked-duplicate lookup; the active-only hash index never fires for parked
rows, so today re-extractions silently create duplicate parked rows — fixed
here) and `confirmRepeats` (default 2) promotes early; (3) explicit `PATCH
/v1/memories/{id}` with `confirm`/`reject` (reject → `expired`). All
promotions ride the SUPERSEDE path against the parked row's `supersedes_id`
target — full prior-state event on the target — so every auto-resolution is
itself reversible via D-064's rollback. Trust gates are not re-applied at
promotion: TTL/threshold/human action IS the gate's resolution. The RFC's
assert/correct PATCH actions stay in v1.2 trust extensions.

**Consequences:** parked memories stop being a roach motel; a fifth sweep
(confirm) joins the lifecycle manager under the Phase 14 idempotency/
singleflight contract.

## D-066 — vindex/hnsw: graph.Delete is forbidden; invalidate-and-rebuild instead

2026-06-12. CI (and local repro at -count=40) hit SIGSEGVs inside coder/hnsw
v0.6.1 `Graph.Add`: upstream `Delete` removes a node from `layer.nodes` and
calls `isolate()`, but HNSW adjacency is asymmetric — inbound edges from nodes
the deleted node doesn't list survive as dangling references. A later Add can
traverse to the deleted node, adopt its key as the inter-layer elevator, and
dereference `layer.nodes[*elevator]` == nil. v0.6.1 is the latest upstream;
no fixed release exists. The driver previously did Delete-then-Add for
duplicate-key upserts (working around a separate upstream duplicate-key Add
bug) and hard Deletes for removals — both paths could corrupt the graph and
crash a production server.

**Decision:** the in-memory graph is append-only. Duplicate-key upserts and
deletes invalidate the tenant graph (`built=false`, fresh graph) and the next
Search lazy-rebuilds from the vector store, which is already the boot path.
Amends D-048's hard-delete finding. Cost: one rebuild per tenant after a
replace/delete burst, amortized by the existing lazy-build; vector-store rows
remain the source of truth either way.

**Consequences:** no upstream graph mutation bug is reachable; rebuild
correctness is covered by the existing recall-floor and conformance tests;
re-embedded memories become searchable on the next query rather than
immediately (cache-rebuild semantics, identical content).

## D-067 — Productionization parity-lens hardening program

2026-06-13. A productionization-hardening program run per
`docs/notes/productionization-playbook.md` (§7 homes the generic method onto
Stowage). The findings doc is `docs/notes/parity-lens-findings.md`; this entry
records the program and pre-reserves its per-phase decision numbers.

**The lens (owner-confirmed at GATE 1, parity — both seams co-equal):**
*"Same code, same seams" must be literally true — every capability, lifecycle,
and semantic must be reachable and behave identically on the embedded-sqlite path
AND the server-over-Postgres path; any behavior/lifecycle/capability that exists,
works, runs, or is observable on only one of the two — in EITHER direction — is a
parity finding.* Two axes checked together: the **path axis** (embedded
`sdk/stowage` vs server `cmd/stowage`→`internal/api`+`internal/mcpserver`) and the
**backend axis** (sqlite vs Postgres, `internal/store`). Unlike Harbor's
single-favored-path lens, neither side is privileged; divergence in either
direction is the bug.

**Tiered refinement (owner, 2026-06-13 — resolves both GATE-2 questions):** the
flat lens is qualified by capability tier. Governing principle, verbatim intent:
*logic should be one but access is through different surfaces (to avoid drift)* —
one core, thin surfaces. (1) **Embedded `sdk/stowage` is inherently single-user**;
team sharing (grants/groups management, contribute-mode) and tenant admin
(API-key management) are **not** embedded capabilities by design. (2) **`stowage
mcp` is a server access point, not an embeddable** — co-equal with HTTP over the
running stack (MCP adds management-via-agents so consumers aren't forced through a
proprietary UI), so it must run the full pipeline. Parity is therefore tiered:
single-user capabilities reach {embedded SDK, HTTP, MCP}; multi-user/admin
capabilities reach {HTTP, MCP} only; backend parity (sqlite↔Postgres) unchanged.
This reclassifies the "grants/contribute unreachable on embedded" findings as
correct-by-design (they become HTTP↔MCP server-surface drift instead).

**Audit calibration:** 11 read-only investigators (one per §7.2 seam) + an
adversarial skeptic on every blocker/major, pipelined per seam. 40 agents; 35
findings (9 blocker / 20 major / 6 minor); 28 skeptic-survived, 1 refuted, several
recalibrated. Verdict: the **backend axis is essentially clean** (the Store seam's
shared conformance suite exercises both drivers; the only substantive divergence
is FTS `MATCH` vs `plainto_tsquery`); the **path axis is not** — the headline is
that `stowage mcp` starts no pipeline/lifecycle, so MCP `memory_ingest` is a
silent dead-end, plus a privileged HTTP-handler stratum (rollback, grants
management, topic writes, buffer flush, branch ops, contribute-mode) absent from
the embedded SDK surface.

**Wave structure (triage by change-type, §3):**
- **Wave A — correctness + honesty:** the outright bugs (MCP-no-pipeline,
  contribute-mode silent mis-scope, embedded config-validation/D-030 bypass,
  sqlite FTS hard-error, embedded rune-safety, embedded gateway-defaults) + doc
  honesty. The behavior-changing MCP fix is its own `fix` PR + decision + E2E.
- **Wave B — mechanical re-homing / surface-parity (tiered):** lift
  handler-resident orchestration into the shared service layer; expose single-user
  verbs (rollback/confirm/reject, topic write, flush, branches, `memory_assert`)
  on {SDK, MCP, HTTP} and multi-user verbs (grants management, contribute-mode) on
  {HTTP, MCP} only — never the SDK. Byte-for-byte / both-paths-identical behavior
  bar; staged by file-collision (the SDK `Client`/`http`/`embedded` trio is shared
  by the single-user items) not logical grouping.
- **Wave C — finish or formally defer half-shipped primitives:** playbook stub,
  runtime API-key (HTTP+MCP) management, any verb Wave B deferred.
- **Wave D — decision-shaped RFC remainder:** the GATE-2 questions are resolved
  (embedded=single-user; mcp=server access point that runs the stack); the
  remainder is the **server deployment shape** (one `serve` process exposing both
  HTTP+MCP over one stack — preferred — vs a separate full-stack `mcp` process)
  and codifying the *one-core-many-surfaces* principle + tiered capability matrix +
  `boot.StartPipeline` as the canonical anti-drift seam in the RFC.

**Principles (binding for this program):**
1. **One logic core, thin tiered surfaces** (owner's governing principle) — a
   capability is implemented once in the core/service layer; surfaces (SDK, HTTP,
   MCP) are thin callers. Single-user capabilities reach all three; multi-user/
   admin capabilities reach the two server surfaces (HTTP+MCP); drift between
   surfaces of one core is the bug.
2. **Single source of post-boot wiring** — pipeline + lifecycle + backfill start
   through one shared helper consumed by serve, mcp, and embedded; hand-duplicated
   wiring is the root of the flagship bug.
3. **Both-paths-identical bar** — a re-homing/parity fix lands its conformance
   scenario passing embedded AND server in the SAME PR.
4. **Bidirectional parity (within tier)** — a capability reachable/observable on
   only one in-tier surface/backend is a finding regardless of which side favors
   it.
4. **Same-PR repair** (§4.4) — fix what the work surfaces, wherever it lives; name
   it in the PR body.
5. **Honesty over silence** — a declared-but-ignored field (contribute-mode on
   MCP) is worse than absence; fail loud or honor it.

**Pre-reserved decision numbers** (parallel agents append their own entries
without numbering collisions; unused numbers are released with a dated note):
- **D-068** — Wave A: shared `boot.StartPipeline` + MCP pipeline/lifecycle parity
  (the flagship fix).
- **D-069** — Wave A: embedded correctness/honesty bundle (config validation +
  D-030 guard, gateway defaults, FTS sanitization, rune-safe drill-down,
  contribute-mode fail-loud).
- **D-070** — Wave B: `reconcile.Rollback` core + single-user reversibility parity
  (rollback/confirm/reject/get-patch on SDK + MCP + HTTP).
- **D-071** — Wave B: tiered surface-parity — single-user control verbs (topic
  write, buffer flush, branch ops, `memory_assert`) on SDK + MCP; multi-user verbs
  (grants management, contribute-mode honoring) on MCP to match HTTP (not SDK).
- **D-072** — Wave C: finish-or-defer half-shipped primitives (playbook, API-key
  HTTP+MCP management, deferred verbs).
- **D-073** — Wave D: server deployment shape + one-core-many-surfaces principle +
  tiered capability matrix + `boot.StartPipeline` codified in the RFC.

**Consequences:** the program is the citable plan for closing the "same code, same
seams" gap; single-user binding capabilities (P1 fidelity drill-down, P4 forgetting
+ reversibility) reach parity across SDK + HTTP + MCP, and the multi-user tier
(P3 grants/team-sharing management) reaches parity across the two server surfaces
(HTTP + MCP) while staying off the single-user embedded path; the structural
`StartPipeline` seam prevents the three entrypoints from silently diverging again.
Wave A does not begin until the owner approves this entry and the findings doc
(GATE 2).

**Note (2026-06-13) — Wave-A checkpoint outcomes (`chore(checkpoint)`, gates
Wave B).** The read-only Wave-A checkpoint audit (§17 / playbook §4.5) ran and its
punch list was resolved in one chore PR. No new decision numbers consumed
(D-070/D-071 stay reserved for Wave B). Outcomes:

1. **FAIL fixed — `stowage mcp --http` drain-before-shutdown race.** `runMCP`
   ran `httpSrv.Shutdown` in an unawaited goroutine, so `ListenAndServe`
   returning (listeners closed) let the deferred `p.Drain` close the ingest
   channel while an in-flight MCP handler could still be enqueuing — a
   send-on-closed-channel panic across the MCP boundary (CLAUDE.md §13). Fix:
   `runMCP` now awaits `Shutdown` (a `shutdownDone` barrier) before the deferred
   Drain, mirroring `serve`'s synchronous Shutdown-before-Drain order
   (`cmd/stowage/main.go`). Defense-in-depth: a shared panic-safe non-blocking
   enqueue `pipeline.TrySend` (`internal/pipeline/enqueue.go`) now backs BOTH the
   MCP `memory_ingest` handler and the SDK `Ingest`, so a late send degrades to
   `Enqueued=false` instead of panicking. Proof:
   `TestMCPIngestAfterDrainNoPanic` (`test/integration/pipeline_parity_test.go`)
   + `TestTrySend` (`internal/pipeline/enqueue_test.go`).

2. **Eval harness hand-wiring — sanctioned exception (lower-risk choice).** The
   harness keeps hand-wiring the pipeline rather than routing through
   `boot.StartPipeline`: its CI-determinism needs (suppress auto-flush to protect
   the per-conversation mock-script offset; parked mutating sweeps; a retriever
   built without rerank/grants to hold the committed CI baseline) cannot be
   expressed through the shared seam without test-only knobs AND would change eval
   SCORING — risking the D-035 gate in a PR that gates Wave B. Recorded as a
   documented exception in `eval/harness/server.go`, kept honest by
   `TestHarnessStageParity` (asserts the harness wires the same stage
   constructors `boot.StartPipeline` does — the Phase-h1 AC-1 grep does not scan
   `eval/`).

3. **Eval gate-bite hardened.** `TestEvalCIGateBites` now asserts that disabling
   EACH production lane (vector, queries, lexical) individually degrades, via the
   lane FILTER alone — the limit cap is decoupled (`RunConfig.CapLimitToOne`).
   A keyword-phrased fixture (`ci-q-lex-01`, answer "Kafka") makes the lexical
   `LexicalSearch` path load-bearing (its AND-over-all-tokens semantics surface
   nothing for stopword-heavy natural-language questions). The `queries` lane
   exercises the shared `ftsMatchArg` FTS-MATCH path BUG-4 actually broke.

4. **REPORT.md honesty.** The real-gateway headline rows (n=10=0.30, n=50=0.20,
   2026-06-12) are annotated "computed pre-BUG-4 (D-069); lexical/queries lanes
   inactive — pending re-baseline"; a re-baseline follow-up is filed (Phase-20
   scope, needs an API key — not attempted here).

5. **h2 plan Status** flipped to **shipped**.

6. **D-056 hnsw band** raised 82 → 85 via a deterministic incremental-upsert/
   `refreshSidecar` test; 5-run `-race` coverage stable at 89.1 % (see the dated
   note in D-056).

7. **NITs.** `runMCP` now boots the stack + pipeline on `context.Background()`
   (not the signal ctx) so the embedder/stages/sweeps live through Drain like
   `serve`/embedded (was: embedder killed at SIGTERM before Drain).
   `config.FillZeroDefaults` now resolves the profile-specific
   `telemetry.log_format` (fleet → json), so the embedded fleet matches the
   server fleet. New helper vocabulary (`ClampExcerpt`, `FillZeroDefaults`,
   `ftsMatchArg`, `TrySend`) added to `docs/glossary.md`.

**Note (2026-06-16) — Wave-B checkpoint outcomes (`chore(checkpoint)`, gates
Wave C).** The read-only Wave-B checkpoint audit (§17 / playbook §4.5) ran and
its punch list (3 FAIL / 8 WARN / 3 NIT) was resolved in one chore PR. No new
decision numbers consumed (D-072/D-073 stay reserved for Wave C/D). The
content-changing fixes amend h3/h4 surface behavior and are recorded as dated
notes under D-070 (cache-in-core + events Reason) and D-071 (topics single-core +
contribute guard). Outcomes:

1. **FAIL-1/2 fixed — MCP write verbs skipped D-053 cache invalidation.**
   `memory_rollback`/`memory_resolve`/`memory_assert` committed via the reconcile
   core then returned without busting the per-scope retrieval cache, so a
   same-scope `memory_retrieve` served stale results for up to the 60 s TTL — a
   cross-surface divergence from HTTP/SDK. Fixed by pushing invalidation INTO the
   reconcile core (the durable one-core path; see D-070 note) so no surface can
   forget; the per-surface invalidation in HTTP + SDK was removed so invalidation
   happens exactly once.
2. **FAIL-3 fixed — topics upsert/delete is now a single shared core.** MCP +
   HTTP built topic rows inline and the MCP build skipped the `active|paused`
   status validation `topics.Service` enforces. All three write surfaces now route
   through `topics.Service.{Upsert,Delete}` (see D-071 note).
3. **META-FIX — MCP surface added to parity coverage.** The h3/h4 parity suites
   only compared embedded↔HTTP; `internal/mcpserver/parity_test.go` now proves
   each MCP write (rollback/resolve-confirm/assert) busts the cache and that
   `memory_topics` upsert rejects an invalid status — the tests that would have
   caught FAIL-1..3.
4. **WARNs.** Contribute guard aligned (HTTP now rejects `contributor_user_id`
   without `target_scope`, matching MCP); SDK http client maps 409+code →
   `*reconcile.ConflictError` and 404 → `store.ErrNotFound` so both impls are
   `errors.Is`-matchable (suite asserts it); events Reason change documented
   (D-070 note); stale `pipeline/branch.go` godoc corrected; phase-h3 Status →
   shipped; `TestTriggerMatrix` flake annotated (did not reproduce 110×-race).
5. **NITs.** `reconcile.GetMemory` junction-read error now logs a slog warning;
   the MCP contribute-rejection test asserts zero records enqueued/written; the
   glossary "Tiered surface parity" entry fixed (`memory_assert` → {SDK, MCP}).

**Note (2026-06-16) — Wave-C checkpoint outcomes (`chore(checkpoint)`, gates
Wave D).** The read-only Wave-C checkpoint audit (§17 / playbook §4.5) over h5
(D-072) came back **0 FAIL / 3 WARN / 2 NIT** — the cleanest boundary yet: h5 was
empirically confirmed LLM-free (transitively, via `go list -deps`), deterministic,
budget-bounded, scope-enforced on both drivers, and byte-identical across {SDK,
HTTP, MCP} and {sqlite, Postgres}. No new decision numbers consumed (D-073 stays
reserved for Wave D). Hardening applied in one chore PR:

1. **WARN — §6 no-gateway lint hardened to transitive.** The AST check saw only
   direct imports; added `TestPlaybookNoGatewayImport_Transitive` (walks
   `go list -deps`) so an indirect gateway pull through another package is caught.
2. **WARN — playbook parity test now seeds provenance.** The all-surfaces parity
   comparison previously left `Provenance` nil (omitempty), so the nested array —
   three independently-declared wire structs, the likeliest drift site — was
   unproven; a provenance row is now seeded so the byte-comparison exercises it.
3. **NIT — single-item-over-budget edge pinned** by `TestAssembleSingleItemOverBudget`
   (one item costing more than the whole budget → 0 packed, 0 tokens used).
4. **NIT — stale CLI usage text** dropped the `(lands in Phase NN)` suffixes from the
   shipped `serve`/`mcp`/`eval` commands in `cmd/stowage`.
5. **WARN (housekeeping, no code) — the local `main` working checkout was stale**
   (session-start commit); all program work was done in fresh worktrees branched
   from `origin/main`, so nothing is affected. Flagged to the owner to fast-forward.

## D-068 — `boot.StartPipeline` is the single post-boot live-wiring seam

2026-06-13. D-067 Wave A, flagship correctness fix (Phase h1; plan
`docs/plans/phase-h1-mcp-pipeline-parity.md`). Implements the structural remedy
pre-reserved as D-068 in D-067.

**Decision.** Turning an opened `boot.Stack` into a *live* derivation system —
the buffer/extract/reconcile pipeline stages, the lifecycle Manager (all five
sweeps), and the embedding `BackfillSweep` — happens in exactly one place:
`boot.StartPipeline(ctx, *Stack, config.Config) (*Pipeline, error)`. The returned
`*Pipeline` exposes `In` (the fire-and-forget ingest channel, P2), `Stage` (the
buffer stage, for the flush/branch verbs Wave B surfaces), and `Drain(ctx)` (stops
the sweeps + backfill, closes the channel, then drains the stages in dataflow
order; idempotent). `boot.Open` stays the static-stack builder; it no longer
implies any serving.

**Why.** `runMCP` booted the stack, accepted `memory_ingest`, and started **no
stages** — MCP-ingested records durably appended, enqueued into a consumer-less
channel, filled it, then silently dropped while the tool reported success
(parity-lens BUG-1). The post-boot wiring was hand-duplicated across
`runServe`/`runMCP`/`NewEmbedded` (Pattern P2) and had drifted; `BackfillSweep`
ran on serve only. Centralizing the wiring makes the three entrypoints share one
supervised path so they cannot diverge again.

**Scope.** `runServe`, `runMCP`, and `sdk/stowage.NewEmbedded` all obtain their
live system from `StartPipeline` and `Drain` it on shutdown. The API server's
ingest channel is now injectable (`Server.SetPipelineIn`) so serve sends onto the
shared `StartPipeline`-owned channel; the standalone/test path keeps the
server-owned channel. The MCP buffer-control verbs themselves remain Wave B — this
phase only makes ingested records *progress*.

**Behavior-changing.** MCP ingest now produces memories (and MCP/embedded now run
the lifecycle sweeps and `BackfillSweep`). Shipped as its own `fix` PR with an E2E
(`test/integration/pipeline_parity_test.go` proves serve/mcp/embedded yield the
same reconciled memory + provenance under `-race`; `scripts/smoke/phase-h1.sh`
drives the real `stowage mcp` binary). Closes the flagship parity blocker (D-067).

**Same-PR repair.** `StartPipeline.Drain` stops the lifecycle sweeps *before*
closing the ingest channel. The prior `runServe` order closed the channel
(`api.Shutdown`) and stopped the lifecycle Manager last, so a re-enqueue sweep
firing in that window could panic sending on a closed channel (a `select`/default
does not save a send on a closed channel). The corrected reverse-dependency drain
fixes this latent race for every entrypoint.

## D-069 — Wave A embedded correctness + honesty bundle

2026-06-13. D-067 Wave A correctness + honesty punch-list (Phase h2; plan
`docs/plans/phase-h2-correctness-honesty.md`). Six independent fixes from the
parity-lens findings (BUG-2..BUG-5, Pattern P3, doc honesty). No new config
knobs (D-034 not engaged) — this bundle makes *existing* validation/defaults fire
on the embedded path and removes silent semantic divergences.

**Decision.**

1. **Embedded config validation + D-030 guard (BUG-3).** `sdk/stowage.NewEmbedded`
   runs the same `config.Config.Validate()` the server runs before `boot.Open`,
   including the D-030 secret-indirection guard (a literal `gateway.api_key`
   instead of an `env.VAR` reference fails closed). An invalid embedded config
   returns an error from the constructor — never a half-built stack (nil client +
   nil closer on the error path).

2. **Embedded gateway defaults (Pattern P3).** `NewEmbedded` applies the same
   defaults layer the server gets via `config.Load`, through the new
   `config.(*Config).FillZeroDefaults` (fills every zero-valued field from
   `config.Defaults()`, preserving caller-set values). The embedding dims/model,
   rerank model, and chat model are now populated identically to the server under
   a documented-minimal config, so the embedded vector + rerank lanes are no
   longer silent no-ops. Embedded still requires an explicit `Store.DSN` for
   sqlite (checked before the fill) so it never silently writes to the server's
   default path.

3. **sqlite FTS query sanitization (BUG-4).** The sqlite lexical/queries lane
   builds its FTS5 `MATCH` argument via `ftsMatchArg`: it extracts the
   alphanumeric terms from the user text (dropping every operator/special
   character), wraps each as an FTS5 string literal, and ANDs them (space =
   implicit AND). This mirrors the Postgres `plainto_tsquery('simple')`
   robustness profile — operator/special-char input (`"`, `OR`, `*`, `:`, `-`,
   unbalanced quotes, …) yields a result set or a clean empty, never a hard error
   that silently drops both lexical lanes. Exact FTS5↔`plainto_tsquery` hit-set
   equivalence is not claimed (engine difference); the no-crash invariant is
   covered by `FuzzFTSQueryArg`.

4. **Embedded rune-safe drill-down (BUG-5).** The drill-down excerpt shaper is now
   a single shared `retrieval.ClampExcerpt` (UTF-8-boundary-safe) consumed by the
   HTTP handler, the MCP handler, and the embedded SDK. The embedded path
   previously raw-byte-sliced the provenance span and could split a multi-byte
   rune (invalid UTF-8); sharing one function makes the three surfaces unable to
   diverge again.

5. **Contribute-mode fail-loud on MCP (BUG-2 / honesty).** MCP `memory_ingest`
   declared `target_scope` / `contributor_user_id` but ignored them — a silent
   mis-scope into the caller's own pool. The handler now **rejects** the call with
   a clear error when either field is set, before any store write. Full HTTP↔MCP
   contribute honoring remains Wave B; per the tiered capability model these are
   multi-user verbs (server surfaces only — never the single-user embedded SDK,
   which omits the fields entirely).

6. **Doc honesty.** The MCP `memory_ingest` tool description and the
   `contracts.go` godoc no longer imply contribute-mode works on MCP; they state
   it is rejected and point at HTTP `/v1/records`.

**Why.** The embedded-sqlite path and the server-over-Postgres path must be
literally "same code, same seams" (D-067 lens). These six were the Wave A
"fix now, no design" defects: a security-adjacent validation bypass, a silent
lane no-op, a sqlite-only robustness cliff, an invalid-UTF-8 excerpt, and a
silent grant-gated mis-scope — each closed without new knobs.

**Same-PR repairs.** (a) `newTestStore` in `internal/store/sqlitestore` now takes
`testing.TB` so the FTS fuzz seed setup can reuse it. (b) The duplicated
`clampExcerpt` copies in `internal/api` and `internal/mcpserver` (and their
tests) were removed in favour of the shared `retrieval.ClampExcerpt`; the table
test moved to `internal/retrieval`.

## D-070 — Reconciliation reversibility (rollback/confirm/reject/get) owned by an exported `internal/reconcile` core

**Status.** Accepted (Phase h3, D-067 Wave B). Pre-reserved by D-067.

**Context.** Reconciliation reversibility (D-017/D-064) and pending-confirmation
resolution (D-065) are binding single-user capabilities, but the orchestration
lived entirely inside the Phase 18 HTTP handlers (`internal/api/memories_handler.go`):
the newest-event inverse walk, the merge all-or-nothing path, the prior-state
restore, and the 409 conflict guards. The parity lens (`docs/notes/parity-lens-findings.md`,
Pattern P1) flagged "reconciliation rollback reachable only on the HTTP server
path" — the embedded SDK and the MCP surface could not roll back or resolve at
all, and any future re-implementation per surface would drift.

**Decision.**
1. The reversibility core is owned by `internal/reconcile` (new `rollback.go`):
   `Rollback(ctx, store, scope, id) (*RollbackResult, error)`,
   `Resolve(ctx, store, scope, id, ConfirmAction) (*ResolveResult, error)`, and
   `GetMemory(ctx, store, scope, id) (*MemoryView, error)`, plus typed conflict
   errors (`ErrAlreadyRolledBack`, `ErrNoPriorState`, `ErrInvalidPriorState`,
   `ErrDownstreamSupersede`, `ErrIncompleteSnapshots`, `ErrNotParked`) carrying
   the stable Phase-18 wire codes. The core rides the existing single-transaction
   `Memories().Commit` unit (D-045) — no new transactional surface.
2. The capability is reachable identically on **{SDK, MCP, HTTP}** (one core,
   many thin surfaces — D-067): `internal/api` handlers are thin callers mapping
   the typed errors to 404/409/200; `sdk/stowage` adds `GetMemory`/`Rollback`/
   `ResolveMemory` on the `Client` (embedded calls the core on the stack, http
   calls the existing routes); `internal/mcpserver` adds `memory_get`,
   `memory_rollback`, `memory_resolve`.
3. **Behavior-preserving re-homing.** The lift changed no behavior: the Phase 18
   `internal/api` rollback/confirm/get tests pass UNMODIFIED, and a golden test
   (`reconcile.TestMarshalPriorState_Golden`) pins the prior-state JSON so the
   `memory.rolled_back` / `memory.superseded` payloads stay byte-identical. The
   both-paths-identical bar is proven by `test/integration/reversibility_parity_test.go`
   (embedded SDK ⇄ HTTP server, real sqlite, ≥1 conflict path, `-race`).

**Surface-count note (against D-015/D-018).** The MCP tool count grows from 7 to
10 (memory_get, memory_rollback, memory_resolve). Per D-015's small-surface
discipline these are three single-purpose tools mirroring the HTTP verbs
(GET / POST rollback / PATCH) rather than one overloaded `memory_admin` tool —
each maps 1:1 to a core function. Schema goldens added under
`internal/mcpserver/testdata/`. These are single-user verbs surfaced on the
embedded SDK (tiered model, D-067); contribute/grant multi-user verbs remain
HTTP/MCP-only and land in h4 (D-071).

**Consequences.** A reversibility behavior change is now a single-site edit in
`internal/reconcile`; the three surfaces cannot drift. `MarshalPriorState` stays
in `reconcile` and now has a matching `parsePriorState` inverse colocated with
the core that consumes it.

**Note (2026-06-16) — Wave-B checkpoint: cache invalidation moved into the core,
+ events Reason prefix.** The checkpoint audit found the MCP reversibility verbs
(`memory_rollback`/`memory_resolve`) and `memory_assert` skipped the D-053
per-scope cache invalidation that the HTTP handlers and the embedded SDK
performed after the call — so an MCP-driven write left a same-scope
`memory_retrieve` serving stale cached results for up to 60 s (FAIL-1/2).

Per the one-core principle, invalidation is now pushed INTO the reconcile core
rather than left to each surface: `reconcile.Rollback`/`Resolve`/`Assert` take an
optional, nil-safe `ScopeInvalidator` (variadic) and invalidate after a
content-changing commit — Rollback and Assert always, Resolve only on a confirm
(`res.Invalidate`, never on reject). All three surfaces now pass their retrieval
cache (or an untyped-nil interface when no retriever is wired) and NO surface
invalidates separately, so invalidation happens exactly once and no surface can
forget it. Surfaces use a `scopeInvalidator()` helper that returns an untyped-nil
interface (a typed-nil `*ResultCache` would panic on use). Proven by
`internal/mcpserver/parity_test.go` (MCP CacheHit flips false after each write).

**events/v1 Reason prefix (intended, documented).** When h3 lifted the rollback
orchestration out of `internal/api` into `internal/reconcile`, the
`memory.rolled_back` / `memory.superseded` event `Reason` strings changed from an
`"api: …"` prefix to `"reconcile: …"` — correct, since the reconcile core now
owns and emits them. The consumed event contract (type, subject_id, payload —
the D-017 prior-state JSON, golden-locked by `TestMarshalPriorState_Golden`) is
unchanged; only the human-readable Reason prefix moved to name the emitting core.
This is recorded as an intended events/v1 change (CLAUDE.md §8), not a silent one;
the convention is "Reason is prefixed by the package that emits the event."

## D-071 — Tiered control-verb surface parity (topics/flush/branches/assert; grants/contribute)

**Status:** accepted (Phase h4, D-067 Wave B).

**Context.** After D-070 the reversibility trio was on {SDK, MCP, HTTP}, but the
remaining control/management verbs were unevenly placed: topic write was HTTP+MCP
but SDK was list-only; buffer flush and branch fork/merge/discard were HTTP-only;
`memory_assert` was MCP-only; grants/group management was HTTP-only; and MCP
contribute-mode was a fail-loud shim (h2). The tiered parity model (D-067) decides
where each verb belongs.

**Decision.** Apply the tiered model:

- **Tier A — single-user control verbs → {SDK, MCP, HTTP}:** topic upsert/delete
  (+ `pack:off`, D-043), buffer flush (explicit/session_end), branch
  fork/merge/discard (discard→SkipPromotion, D-029), and `memory_assert`.
- **Tier B — multi-user/admin verbs → {HTTP, MCP} only, never the single-user SDK:**
  grants/group/membership management and contribute-mode honoring (D-016/D-059).

**`memory_assert` reachability (owner-resolved open question).** Assert is a
single-user, pipeline-bypassing direct write. It is added to the **SDK and MCP**
but **deliberately NOT to HTTP** — embedded hosts get a direct-write escape hatch
while the HTTP surface keeps all writes routed through the ingest pipeline. The
HTTP `Client` implementation returns `ErrAssertHTTPUnsupported`.

**Shared cores (surfaces cannot drift).** Each re-homed verb has ONE core the thin
surfaces call:
- assert → `internal/reconcile.Assert` (MCP handler + embedded SDK).
- branch fork/merge/discard → `internal/pipeline.{ForkBranch,MergeBranch,DiscardBranch}`
  (HTTP handler lifted onto it; MCP + embedded SDK call it). Discard's flush is now
  synchronous in the core (the prior HTTP `go FlushBranch(r.Context())` also fixed a
  latent request-context-cancellation race).
- topic write → `internal/topics.Service.{Upsert,Delete}` (embedded SDK). HTTP/MCP
  retain their pre-existing inline store calls (byte-identical construction;
  golden-locked).
- contribute → `internal/grants.Service.AuthorizeContribute` + `ContributeContext.ApplyTo`
  (grant-check + scope-override) consumed by BOTH the HTTP `records_handler` and the
  MCP `memory_ingest` handler. h2's MCP fail-loud is replaced: with a covering
  contribute grant the records are stamped with the pool-owner scope; without one the
  request is rejected (never a silent mis-scope).

**Single-user boundary is enforced, not documented.** A reflection test over the SDK
`Client` interface (`sdk/stowage.TestClientTierBoundary`) fails the build if any
Tier-B verb name (Grant/Group/Member/Contribute) ever appears, and asserts every
Tier-A verb is present.

**Surface-count note (against D-015/D-018).** The MCP tool count grows from 10 to
**13**: `memory_flush`, `memory_branch` (action: fork|merge|discard), and the Tier-B
`memory_grants` (action-tagged: create_group/list_groups/add_member/remove_member/
list_members/create_grant/list_grants/revoke_grant). Per D-015's small-surface
discipline the branch and grants verbs are folded into action-tagged tools rather
than one tool per verb. Schema goldens added under `internal/mcpserver/testdata/`.

**Consequences.** The both-surfaces-identical bar is proven by
`test/integration/surface_parity_test.go` (embedded SDK ⇄ HTTP, real sqlite,
`-race`); MCP contribute honoring + rejection by `internal/mcpserver`
(`contribute_test.go`, real store + grants service). A control-verb behavior change
is now a single-site edit in its core.

**Note (2026-06-16) — Wave-B checkpoint: topics single-core + contribute guard.**
This entry originally noted "HTTP/MCP retain their pre-existing inline store calls
(byte-identical construction; golden-locked)" for topic writes. The checkpoint
audit found that the MCP inline build skipped the `active|paused` status
validation `topics.Service` enforces — a Tier-A parity divergence (FAIL-3). That
exception is now withdrawn: ALL three write surfaces (MCP + HTTP, SDK already did)
route through `topics.Service.{Upsert,Delete}`, the single core. The service now
wraps validation failures (empty key, bad status, empty set) with a
`topics.ErrInvalidTopic` sentinel so the HTTP handler maps `errors.Is` →
400 (validation) vs 500 (store error); the MCP handler surfaces the error as a
tool error. MCP-surface validation is proven by `internal/mcpserver/parity_test.go`.

**Contribute-guard parity.** The audit also found the HTTP `/v1/records` handler
silently ignored `contributor_user_id` when `target_scope` was absent (dropping
attribution and ingesting into the caller's own scope), while MCP rejected it.
Both surfaces now reject loudly (HTTP 400 / MCP tool error) when
`contributor_user_id` is set without `target_scope` — identical behavior. The MCP
contribute-rejection test additionally asserts a rejected request is fully inert
(zero records enqueued or written).

## D-072 — Deterministic, LLM-free playbook assembly finished on {SDK, MCP, HTTP}

**Status:** accepted (Phase h5, D-067 Wave C).

**Context.** Phase 17 shipped the playbook surface as a placeholder: `Client.Playbook`
returned `PlaybookResponse{Stub:true}` (guarded by `ErrPlaybookStub`), the MCP
`memory_playbook` tool returned a "not implemented" error, and there was no
`GET /v1/playbook` route. The deterministic assembly itself (RFC §6a.3) — the one
genuinely half-shipped launch primitive — was never built. ACE (brief 05) requires
the playbook to be a *deterministic view over itemized memories*, append-biased for
host prompt-cache warmth, never a monolithic LLM rewrite (context-collapse defense).

**Decision.**

- **New `internal/playbook` package (LLM-free).** `Assemble(ctx, st, scope, opts)`
  is a pure projection: it lists active `strategy`/`failure_mode`/`decision`/`gotcha`/
  `pattern` memories in scope, scores each with the pure `internal/scoring` functions
  (unit fused base ⇒ utility/decay only), greedy budget-packs by score (stable ULID
  tiebreak, budget never exceeded), groups into kind-ordered sections, and attaches
  provenance for P1 drill-down. It NEVER imports the gateway — enforced by
  `TestPlaybookNoGatewayImport` (the CLAUDE.md §6 lint for the new package). Output is
  byte-identical run-to-run and append-biased (a new lower-ranked memory appends at the
  section tail, leaving the existing prefix stable — the ACE KV-cache property).
- **New `Store.ListByKinds(ctx, scope, kinds)` seam method.** Active-only,
  scope-enforced (P3 — no unscoped variant), ordered `(created_at, id)`. Implemented on
  BOTH the sqlite and postgres drivers and proven by the shared conformance suite
  (`MemoryListByKinds`, `MemoryListByKindsScopeIsolation`): kind filter, active-only,
  scope isolation, empty-scope guard. It is a store *view* ranked by the playbook layer,
  not a retrieval query.
- **Profile-internal budget (D-034/D-042).** `config.PlaybookBudgetForProfile`
  (assistant 2000 / coding-agent 3000 / fleet 4000) — no new top-level operator knob.
- **Reachable identically on {SDK, MCP, HTTP}** (single-user READ verb, D-067 tier):
  `GET /v1/playbook` (new route + handler), the embedded + HTTP SDK `Client.Playbook`,
  and the MCP `memory_playbook` tool all call the one `playbook.Assemble` core and emit
  the byte-identical wire envelope (`sections[]{title,kind,items[]{memory_id,kind,
  content,score,provenance}}`, `budget`). Parity is proven by
  `test/integration/playbook_parity_test.go` (embedded SDK ⇄ HTTP ⇄ MCP, real sqlite,
  `-race`).
- **Stubs removed.** `ErrPlaybookStub` and the `PlaybookResponse{Entries, Stub}` shape
  are deleted; the MCP "not implemented" handler and `PlaybookOutput{Error}` are
  replaced with the real typed contract (schema golden regenerated). The old shape only
  ever returned `Stub:true` — no real consumer existed (the harbor adapter's
  `stowage_playbook` tool is updated to the new sectioned shape in the same change).
- **MCP tool count unchanged (13).** `memory_playbook` already existed as a stub; it is
  now real. No new tool is added.

**Deviation from the phase plan (documented).** The plan's `Options` sketch listed
`TopicKeys` for topic restriction. Memories carry no topic-key column in the v1 schema
(RFC §8.1), so a topic filter would be a dead knob (D-024); `Options` exposes only
`SessionID` (session-affinity) and `TokenBudget`. Sections are grouped by kind.

**Scope boundary (not a departure).** Reflection extraction + the re-reflection sweep
(RFC §6a.1-2 — the LLM *write* side that produces `strategy`/`failure_mode` memories)
remain roadmap Phase 19. This phase builds only the read/assembly path; it views
whatever building-block memories already exist (via topic extraction + `memory_assert`).

**Key/credential-admin tier exception (owner, 2026-06-16).** Recorded here so it is a
conscious choice, not drift: runtime API-key/credential management is **HTTP-admin-only**
by design (D-030) and deliberately NOT exposed on MCP — distinct from grants/team-sharing
admin which stays {HTTP, MCP} (D-071). The D-067 tiered model now reads: single-user →
{SDK, MCP, HTTP}; team/grants admin → {HTTP, MCP}; key/credential admin → {HTTP} only.
No h6 — Wave C is h5 alone.

## D-073 — Server deployment shape + the one-core/tiered-surfaces invariant (Wave D; closes D-067)

2026-06-17. The decision-shaped close of the productionization program (D-067).
Wave D is an RFC amendment, not an implementation phase: it ratifies the server
deployment shape and codifies the invariants Waves A–C established so future
phases inherit them. Amends RFC §9.2 (deployment shape) + adds RFC §9.5 (one
logic core, thin tiered surfaces); adds the matching binding rule to CLAUDE.md §6
(mirrored to AGENTS.md).

**Decision 1 — deployment shape (owner, 2026-06-17): one process, both surfaces.**
A single Stowage **server** deployment is one process exposing BOTH the HTTP API
and the MCP-over-HTTP surface over one `boot.Stack` + one `boot.StartPipeline` —
one result cache, one lifecycle sweep set, no cross-process cache-staleness.
`stowage mcp` over **stdio** stays a separate lightweight single-host mode;
`sdk/stowage` in-process embeds the same stack with no daemon. Rejected: the
status-quo two-process shape (separate `serve` + `mcp`), whose per-process
in-memory caches can serve reads that are stale relative to the other process's
writes (the D-053 scope-generation counter is in-process) and which doubles the
lifecycle sweep set (tolerated only via D-057 advisory locks). **Follow-up
(named, not yet built):** a small implementation phase to co-mount the MCP-HTTP
handler onto `stowage serve` over the shared stack; until it lands, operators run
the two processes and accept the documented cache-coherence caveat.

**Decision 2 — codify "one logic core, thin tiered surfaces" (D-067 program
outcome).** Every capability is implemented once in the core/service layer;
`sdk/stowage`, `internal/api`, and `internal/mcpserver` are thin callers, and a
capability's side effects (cache invalidation, validation, events) live in the
core so no surface can omit them. Reachability is tiered: single-user (incl.
playbook) → {SDK, HTTP, MCP}; team/grants admin → {HTTP, MCP}; key/credential
admin → {HTTP} only; backend → {sqlite, Postgres}. A new capability ships on all
its tier's surfaces in the same PR with a parity test that includes MCP. The
invariant is held by mechanical seams: `boot.StartPipeline`, core-owned cache
invalidation (`reconcile`), single-core reversibility/topics/contribute, the
`internal/playbook` transitive no-gateway lint, and MCP-inclusive surface-parity
tests.

**Program close.** D-067 Wave A (D-068 flagship `StartPipeline` + MCP pipeline
parity; D-069 embedded correctness/honesty), Wave B (D-070 reversibility parity;
D-071 tiered control-verb parity), Wave C (D-072 deterministic playbook assembly),
and three read-only checkpoint audits (gates between waves) are all shipped. The
"same code, same seams" gap the parity lens opened is closed: every single-user
capability — including reversibility and the playbook — is reachable and
behaves identically across {SDK, HTTP, MCP} and {sqlite, Postgres}; the
multi-user/admin tiers are reached on their designated surfaces with the SDK
single-user boundary test-enforced. **Explicitly deferred (recorded, not drift):**
reflection extraction + the re-reflection sweep (§6a.1-2 → Phase 19); playbook
topic-grouping (needs a memory↔topic schema link → RFC §8.1 amendment); the DSAR
retention cascade (→ Phase 21); grants `RedactionProfile` application (later);
and the co-mount implementation follow-up named in Decision 1.

**Consequences.** The deployment shape and the one-core/tiered invariant are now
binding (RFC §9.2/§9.5, CLAUDE.md §6). Future phases extend the core and inherit
all-surface reachability + parity testing by default, rather than re-discovering
the drift the program corrected.

## D-074 — `stowage serve` co-mounts MCP-over-HTTP on a second listener over the shared stack (D-073 follow-up)

2026-06-17. Phase h6. Implements the co-mount follow-up named (not built) in
D-073 Decision 1 — the canonical one-process/both-surfaces deployment shape
(RFC §9.2/§9.5). Pure process wiring; no capability is re-implemented.

**Decision.** `stowage serve` optionally co-mounts the MCP-over-HTTP surface on a
SECOND listener over the SAME `boot.Stack` + `boot.StartPipeline` as the HTTP API
— one result cache (`stk.Retriever`), one ingest channel (`p.In`), one buffer
stage (`p.Stage`), one lifecycle sweep set. A write via the HTTP surface is
therefore immediately reflected by an MCP retrieve with no stale window (the
D-073 cache-coherence win is structural, not a sync protocol). The co-mounted MCP
is the SAME `internal/mcpserver` handlers (h3/h4/h5) — `ScopeFn = CtxScopeFn()`,
tenant from the authenticated key via `KeyringMiddleware` over the store keyring.

**One knob (D-034).** `server.mcp_listen`, default **empty (opt-in)**. Empty keeps
`stowage serve` single-surface (HTTP API only), binding exactly one port — the
zero-config shape is unchanged, no surprise second bound port for existing
deployments. Set (e.g. `:8081`) enables the canonical both-surfaces shape with one
config line. Validated as a host:port distinct from `server.listen`; surfaced by
`config explain`; documented on the `ServerConfig.MCPListen` field; same-PR smoke
(`scripts/smoke/phase-h6.sh`). It is a boot/listen concern, so — like
`server.listen` — it is a top-level default applying to all profiles, not a
profile-override map entry.

**Two listeners, not one path-prefixed port.** Rejected mounting MCP under the
api `http.Server` (e.g. `/mcp`): the api server sets a REST-correct `WriteTimeout`
and a body-limit + request-logging middleware chain, whereas the MCP HTTP
transport streams and deliberately runs with NO `WriteTimeout` (only
`ReadHeaderTimeout`), so a shared port would let `WriteTimeout` truncate MCP
streams and wrap MCP in REST middleware. The shared `Stack`+`Pipeline` (not a
shared port) is what delivers cache-coherence.

**Shutdown order (h1 ingress-before-Drain invariant).** On signal, BOTH listeners
are shut down — `srv.Shutdown(ctx)` AND `mcpHTTP.Shutdown(ctx)`, both awaited —
BEFORE `p.Drain(ctx)` closes the ingest channel, so no in-flight REST/MCP handler
can send on a closed channel (no panic across the boundary). `-race`-proven in
`test/integration/comount_test.go`.

**Unchanged.** `stowage mcp` (stdio + standalone `--http`) is untouched. When
`server.mcp_listen` is empty, `runServe` binds exactly one port as before.

**Consequences.** Operators get the canonical single-process both-surfaces shape
with one config line; the two-process shape (with its documented cross-process
staleness caveat) remains available by simply not setting the knob.

**Note (2026-06-17) — gate-integrity repair surfaced by h6 (§4.4).** Implementing
h6 surfaced that `scripts/smoke/phase-16.sh` had asserted exactly 7 MCP tools and
was FAILING (exit 2) since h3/h4 grew the surface to 13 — yet `make preflight`
still reported "preflight OK". Root cause: the `preflight` smoke loop (`Makefile`)
ran each `scripts/smoke/phase-*.sh` **without checking its exit code** (no
`set -e`, no `|| rc=1`), silently tolerating a failing smoke; and CI
(`.github/workflows/ci.yml`) does not run preflight/smokes at all (build/vet/test/
coverage/eval-ci/check-mirror/drift-audit only), so the drift was invisible
end-to-end. Fixed here: (1) phase-16.sh updated to the current canonical 13-tool
surface; (2) the `preflight` target now fails if ANY smoke exits non-zero
(verified — a deliberately broken smoke makes `make preflight` exit non-zero with
"preflight FAILED"). **Recommendation (not done here — flagged for the owner):**
add a smoke/preflight job to CI so smoke drift is caught at the CI gate, not only
the local pre-commit hook.

## D-075 — bifrost auto-wires a Cohere-shape custom rerank provider (full OpenRouter stack); benchmark rebased onto bifrost

2026-06-17. Phase h7. Closes the gap proven during investigation: bifrost's
built-in `openrouter` provider does not implement rerank, but a Bifrost **custom
provider** (`BaseProviderType: Cohere`, `RequestPathOverrides{rerank: "/rerank"}`)
reranks against OpenRouter's `…/api/v1/rerank` successfully (verified live
2026-06-17: `cohere/rerank-4-fast` returned real sorted scores). All provider
wiring stays in `internal/gateway` (P5/D-067/RFC §9.5); the openaicompat driver
(D-040) and its rerank live test remain valid.

**Decision.** When `gateway.driver=bifrost`, `gateway.rerank_model` is set, AND
the primary `gateway.provider` is **not** native-rerank, the bifrost `Account`
ALSO exposes a synthetic custom provider `stowage-rerank`
(`internal/gateway/bifrost/account.go`): `GetConfiguredProviders` →
`[primary, stowage-rerank]`; `GetConfigForProvider("stowage-rerank")` → a
`ProviderConfig` with `CustomProviderConfig{BaseProviderType: Cohere,
AllowedRequests{Rerank: true}, RequestPathOverrides{RerankRequest: "/rerank"}}`
and `NetworkConfig.BaseURL = rerank_base_url || base_url`;
`GetKeysForProvider("stowage-rerank")` → the same key with the REQUIRED wildcard
`Models: {"*"}` (an empty Models yields "no keys found that support model"). One
Bifrost `Account` legitimately exposes multiple providers, so embed/complete route
to the primary and rerank routes to the custom one under one client+key. The
`Driver` records a `rerankProvider` field (== `stowage-rerank` when auto-wired,
else the primary) and sets `bfReq.Provider = d.rerankProvider` in `Rerank`;
embed/complete are unchanged. Metering + the circuit breaker still apply (the
custom rerank flows through the same `Driver.Rerank`).

**Native-rerank set** (no custom provider added — rerank routes to the primary):
`{cohere, vllm, bedrock, vertex}` (`isNativeRerankProvider`).

**Never silent / graceful degradation (D-036/AC-3).** The auto-wire is logged at
boot (info level: provider name + base URL + path + base_provider_type +
rerank_model; NEVER the key). On a backend without a Cohere-shape `/rerank` the
call errors → the existing `DegradedRerank` path, not a panic.

**One knob (D-034).** `gateway.rerank_base_url`, default **empty (→ reuse
`base_url`)** — for the rare case rerank lives on a different host than
embed/complete. Validated as an absolute URL (scheme + host) when set; surfaced by
`config explain`; documented on `GatewayConfig.RerankBaseURL`;
`STOWAGE_GATEWAY_RERANK_BASE_URL` env override; same-PR smoke
(`scripts/smoke/phase-h7.sh`). The `/rerank` path is a constant for the auto-wired
Cohere-shape provider, not a knob.

**Benchmark rebase.** The full-mode eval (`eval/harness`) is rebased off
`openaicompat` onto **bifrost** + the operator's cheaper models: provider
`openrouter`, memory-formation model `inception/mercury-2`, embed
`perplexity/pplx-embed-v1-0.6b` @ 1024 dims, rerank `cohere/rerank-4-fast`. The
harness now honors `STOWAGE_EVAL_PROVIDER`/`STOWAGE_EVAL_RERANK_MODEL` and wires
the retriever with `WithRerankModel` in full mode, and the runner issues
`precise`-profile retrieves (new `RunConfig.EnableRerank` toggle) so the
cross-encoder actually runs. Rerank stays **OFF** for the deterministic mock CI
run (the toggle defaults off and CI never sets `STOWAGE_EVAL_GATEWAY`), so the
committed CI baseline (`make eval-ci`) is unaffected. A fresh full-mode run on the
new config is operator-run (needs `OPENROUTER_API_KEY`, not CI).

## D-076 — Roadmap recut to v0.1 (launch after 27 + hardening); Phase 20 judged QA pulled ahead of Phase 19

2026-06-17. Owner directive (supersedes the launch-scope framing of **D-033**;
RFC §12/§15 amended in the same PR).

**Launch scope.** v0.1 launches after **phases 01–27 + a terminal hardening
gate**, not at Phase 21. The capabilities D-033 deferred to post-launch tracks —
episodic & temporal (22–24), trust extensions (25–26), proactive (27) — are pulled
**into** the v0.1 launch scope. The hardening & launch work (former Phase 21
content: security pass, external docs, cross-compile matrix + checksums, public-repo
audit, five-minute-rule smoke) runs **last, after Phase 27**, as the terminal v0.1
gate. D-033's structural insight stands: the day-one schema (§5.0/D-024) already
captures these features' unbackfillable signals, so folding them in costs nothing
structurally — only sequencing changed. Phase *numbers* are kept as stable
identifiers (no file renumber); the track framing in `docs/plans/README.md` is
reframed accordingly. (Open item flagged for owner review: physical renumber vs
stable-identifier reframing — this plan chose stable identifiers.)

**Phase 20 before Phase 19.** Phase 20 (judged eval + competitor table) is pulled
**ahead of** Phase 19 (reflection write-side): the judged headline number does not
depend on reflection. Evidence — the last n=10 full run scored
`answer_context_hit=0/10` while retrieval was excellent (right memory at rank 1
nearly every question); every miss was a metric artifact (paraphrase, number form,
or an answer needing a reasoning/aggregation step a reader supplies). The
reader+judge is exactly what credits these.

**The judged-QA metric.** `answer_quality` = (correct + ½·partial)/N, produced by an
**opt-in, full-mode-only** path: a reader LLM answers from Stowage's retrieved
context; an LLM judge grades the answer vs the gold answer semantically. The judge
call is **JSON-schema-constrained through the gateway seam** (§10 — free-text JSON
parsing of model output is forbidden; P5 — no provider SDK under `eval/`).
`answer_quality` is the competitor-comparable launch figure, run on the
`longmemeval_s` distractor haystack. The retrieval-only `answer_context_hit`
(deterministic, now normalized for number-word + either-direction matches) stays the
**CI gate metric** — LLM-free, never a paid call in CI; `make eval-ci` stays
deterministic. The judged number is operator-run (needs `OPENROUTER_API_KEY`).

**The reflection-dependent slice is carved out as Phase 20b.** The RFC §12 items
that consume the reflection→playbook loop — the Harbor-fleet **gain harness**
(memory-on vs off delta) and the **online-adaptation (ACE) scenarios** — are split
out of Phase 20 and run **after Phase 19** ships reflection. Phase 20 proper is the
reflection-independent core (judged QA + public suite + competitor table).

Consequences: `docs/plans/phase-20-eval-finalization.md` is authored for the Phase
20 core; Phase 19's reflection write-side remains next in roadmap order after Phase
20; Phase 20b is a named follow-up gated on Phase 19; the eval report
(`eval/REPORT.md`) with the judged `answer_quality` + competitor table remains the
open-source launch gate (D-023/D-035), now shipping at the v0.1 (01–27) boundary.

## D-077 — Reflection write-side is a sweep-driven stage feeding the existing reconcile core

2026-06-17. Phase 19 implements the ACE reflection write-side (RFC §6a.1-2; the
deterministic playbook *read* side already shipped via D-072). Settles the eight
design decisions surfaced by the seam map (plan: `phase-19-reflection.md`).

**Architecture.** Reflection is a **lifecycle sweep**, not a per-buffer-flush mode
beside topic extraction. It reads outcome-tagged records from the store, assembles
trajectories, calls the gateway to distill `strategy`/`failure_mode` candidates, and
emits `pipeline.CandidateBatch` into the **unchanged** reconcile stage — so reflection
memories dedupe/update/supersede under the same trust gates as any candidate. (RFC
§6a.2 says "alongside topic extraction"; we depart because outcome is not on the
in-flight `pipeline.Item`/`FlushedBuffer` and a trajectory spans multiple flushes —
the RFC itself runs multi-epoch reflection as a sweep. Departure recorded here.)

**The eight resolutions.** (1) **Trigger:** sweep-only, fed by already-ingested
outcomes — no new caller surface (a forced run uses the existing
`STOWAGE_SWEEP_FORCE`/`RunForce`). (2) **Trajectory:** outcome-tagged records grouped
by `(session_id, branch_id)` with a terminal outcome, ordered by `occurred_at`,
success/failure contrast. (3) **Prompt/schema:** a dedicated reflection prompt +
schema + reflection-only kind enum in a new `internal/reflect` package; the topic
`ValidKinds` is NOT widened (topic extraction can never emit reflection kinds and
vice-versa). (4) **Seed weights:** applied in the reflection candidate constructor
(per-kind seed importance/stability; `TrustSource:"llm_reflected"`, distinct from
`llm_extracted`) — rather than retrofitting kind-aware scoring across the engine
(RFC §338's "default scoring weights" interpreted as constructor seeds). (5)
**Cross-kind supersede:** reflection reconciliation restricts neighbors to
`Kinds:["strategy","failure_mode"]` so a strategy cannot supersede a fact. (6)
**Re-reflection idempotency:** per-scope watermark (last reflected `occurred_at`) +
an epoch counter via the existing `job_markers` table; reconcile content-hash +
near-dup pre-filters guarantee re-runs add nothing; every Nth sweep re-reflects a
wider trailing window as the playbook matures. (7) **Wiring:** one reconcile core,
two producers (extract + reflection) via a fan-in merge in `boot.StartPipeline`; the
eval-harness `server.go` reference wiring + `stageparity_test.go` updated to match.
(8) **Links:** reflection reuses the existing `reconciler` link source (no
link-schema change); reflection origin is visible via the `llm_reflected` trust
source.

**Schema budget (D-024).** No new table/column: the `outcome`/`occurred_at` columns
exist since the day-one schema; Phase 19 adds only a forward-only **index**
(`idx_records_tenant_outcome_occurred`) backing the new scope-parameterized
`RecordStore.ListByOutcome` (both drivers, conformance-tested). No RFC amendment
needed.

**Lifecycle (P4).** Reflection memories decay/supersede/quarantine like other
derived memories (verbatim records untouched — P1); a refined strategy superseding
its predecessor is rollback-reversible (D-017).

**Knobs (D-034).** `lifecycle.reflect_{enabled,interval,batch_size,epoch_every}`,
default **off** except the fleet profile (the fleet-learning loop is fleet-first);
zero-config start unaffected. The Phase 20b gain-fleet harness measures whether the
loop actually compounds.

## D-078 — Gain harness uses the eval reader as the agent-loop stand-in; gain is an operator-run release gate

2026-06-18. Phase 20b (RFC §12) ships the gain harness and the online-adaptation
measurement.

**Harbor substitution.** RFC §12 specifies the gain harness uses "a Harbor fleet as
the agent loop." Harbor is a **separate codebase** (the ecosystem's agent framework)
and is not a dependency of this repo, so the gain harness instead uses the **Phase-20
eval reader as the stand-in agent loop**: each scenario's eval question is answered
by the reader with retrieved memory context (**on**) and with none (**off**), and the
Phase-20 judge scores both. `gain = quality(on) − quality(off)` where
`quality(correct)=1, partial=0.5, incorrect=0`. This measures the RFC's quantity
(does memory improve the agent's answer?) without coupling eval to Harbor's wire
protocol; a Harbor-driven runner can later replace the reader behind the same
`GainResult` contract.

**Release gate.** Mean aggregate gain ≥ 0 on the standard scenarios is a release gate
(RFC §12: negative gain fails release), asserted in the **operator-run** full-mode
path (`STOWAGE_EVAL_GAIN=1`) — never in CI (no paid LLM in CI). The deterministic CI
tests cover the pure scoring/aggregation and a fakeGateway on-vs-off delta.

**Online adaptation.** Sequential outcome-tagged tasks run through the **Phase-19
reflection→playbook loop**: between tasks the reflection sweep distills strategies
and the assembled playbook is injected into the next task's reader context; the
quality trajectory across tasks is the compounding signal (ACE). This is **reported,
not release-gated** — the gain delta is the gate. Both runs are opt-in, full-mode,
operator-run; `make eval-ci` is unaffected. No new config knob (eval env vars only;
D-034 not applicable).

## D-079 — Episodes detected by a heuristic gateway-free sweep; narration is a separate schema-constrained gateway sweep

2026-06-18. Phase 22 (RFC §6b) ships episodes + narratives (the write/detection
side; episodic retrieval is Phase 23, causal links Phase 24).

**Heuristic boundary detection (OQ-8 heuristic-first).** A boundary-detection
lifecycle sweep groups records into episodes with **no LLM**: v1 maps one closed
session (no new records for an idle window) to one episode, splitting on a large
intra-session temporal gap. An LLM/topic-shift boundary refiner is a documented
follow-up. The gateway is used ONLY for the narrative text, never the boundary
decision — cheap, deterministic, debuggable.

**Derived episode↔record membership.** Records are immutable (P1), so they carry no
`episode_id`; an episode owns its session's records by time range. Detection
idempotency is therefore "**an episode already exists for this (scope, session)**"
(`EpisodeStore.GetEpisodeBySession`), not a record stamp. New store surface:
`RecordStore.DistinctSessions` (closed-session enumeration) + an `EpisodeStore` seam
(create / get-by-session / list-needing-narrative / set-narrative / list) on both
drivers + conformance; episode indexes are an index-only migration (the episodes
table is day-one §8.1).

**Narration.** A separate narration sweep constructs a `narrative`-kind memory per
episode via a **schema-constrained** gateway call (§10) through the gateway seam
(P5), carrying `episode_id` + provenance to the episode's records (P1) and
`TrustSource:"episodic"`; it sets `episodes.narrative_memory_id`. Idempotent:
narrated episodes are skipped; a forced re-run dedups on content hash. Both sweeps
follow the Phase-19 reflection pattern (advisory locks, jitter, profile-gated, off by
zero-config default); narration gateway calls are metered/evented.

## D-080 — Episodic retrieval: deterministic memory_episodes capability across the tiered surfaces; similar-episode + synthesis deferred

2026-06-18. Phase 23 (RFC §6b) ships the episodic *read* side over the Phase-22
episodes.

**Deterministic memory_episodes capability.** A new gateway-free `internal/episodes`
view core (`List` + `Get`, mirroring `playbook.Assemble`) is exposed as one
single-user capability on all three surfaces (D-067): `GET /v1/episodes` (HTTP), the
`memory_episodes` MCP tool, and `Client.Episodes` (SDK HTTP + embedded). Input
`{limit?, cursor?, from?, until?, session_id?, id?}`; output `{episodes, next_cursor}`,
each episode carrying its narrative content + `narrative_memory_id` (for `/v1/drilldown`).
A byte-identical cross-surface parity test (mirroring the playbook parity test) is the
mechanical anti-drift guard. The `[from,until]` window returns a scope's episode
narratives for a period — the §6b cross-episode "structured summary, never a raw
fragment dump." Reuses the Phase-22 `EpisodeStore` (`ListEpisodes`/`GetEpisode`); no
new store surface, no config knob (read-only). Scope-parameterized (P3): a tenant key
sees the tenant's episodes; session/window narrow in-service.

**Deferred to Phase 23b.** Similar-episode contrast (vector search over narrative
memories, kind-filtered via `vindex.Filter{Kinds:["narrative"]}`) and
gateway-synthesized window summaries are deferred: their output is non-deterministic
and cannot share the byte-identical parity bar that gates the tiered surfaces (they
need a fake-embedder harness), and they add a gateway/vindex dependency to an
otherwise always-available, gateway-free read path. The deterministic surface here is
the foundation they build on.

## D-081 — Episode threading: group session-episodes into cross-session arcs (RATIFIED, Phase 24b)

> **Status: RATIFIED 2026-06-18 (Phase 24b).** The mechanism ships; broad *enablement*
> stays eval-gated (see "Ratified shape" at the end). The original proposal sketch is
> retained below for the record.

**Ratified shape (Phase 24b).** Episode threading ships as a **gateway-free lifecycle
sweep** (`runThreadEpisodes`, sibling to detect/narrate, advisory-locked + jittered)
that groups recent narrated episodes of the same `(project,user)`, within a temporal
window, whose narrative **content word-set Jaccard** (distinct content words, topical — not
character-bigram, which saturates on any prose) ≥ `ThreadMinOverlap`, by writing a
canonical `relates_to` edge between their **narrative memories** (`source="inferred"`)
+ an `episode.threaded` event — **no new table or
column** (the `links` table + `relates_to` are day-one; narratives are memories), no
RFC §8.1 amendment. Idempotent (skip already-linked pairs), reversible (derived edges
over immutable episodes). Fork-1 resolved to **edges, not a container** (no `arcs`
table). Clustering signal v1 = content word-set Jaccard ∧ temporal window ∧
`(project,user)`; **narrative-vector similarity is a future signal** (narratives carry
no entity/keyword junctions, so content-Jaccard is the conservative gateway-free start).
The read is `episodes.Arc` (deterministic, gateway-free BFS over `relates_to`,
active-only, capped), exposed as a `memory_episodes` **`arc_of`** query across {SDK,
HTTP, MCP} (D-067; no new MCP tool) with a byte-identical parity test. Threading tuning
is **profile-internal** (`EpisodeConfig.Threading*`), and the sweep ships **OFF by
default in every profile** — broad enablement is gated on an episodic-eval win
(cross-session QA / resumption; D-035). So Phase 24b lands the *mechanism*; turning it
on in production is the eval's call.

---

**Original proposal (pre-ratification sketch, retained for the record):**

**Problem.** Phase 22 detects an episode as one *closed session* (structural +
temporal-gap heuristic, D-079). But the unit a user actually reasons about is the
**arc** — "the billing-migration effort," "the Q1 outage" — which spans many
sessions over days/weeks. Different sessions are often semantically the *same living
episode*. Today nothing groups them; Phase 23's `[from,until]` window is a blunt
time proxy, not a semantic thread.

**Proposal.** A deterministic lifecycle sweep (sibling to detect/narrate/reflect)
clusters recent episodes into arcs and records the grouping. Clustering signal:
`narrative-vector similarity ∧ entity/keyword overlap ∧ temporal proximity ∧
(project,user) continuity`, above a conservative threshold. The grouping *decision*
is **gateway-free** (heuristic-first, like Phase 22 detection); an optional gateway
call writes the arc *title/summary* only (detect-free → narrate-LLM mirror).

**Reuses, doesn't reinvent.** Arc-grouping is one step beyond the Phase-23b
similar-episode contrast (same kind-filtered narrative vectors,
`vindex.Filter{Kinds:["narrative"]}`), and the "are these the same?" gate mirrors
reconcile's bigram-Jaccard + entity-neighbor + threshold discipline.

**Two open forks (decide at pull time).**
1. **Edges vs container.** Start with episode↔episode `relates_to` links (no new
   table — the links graph exists, composes with Phase-24 causal edges, reversible).
   Promote to a parent **arc** entity (a `parent_id` on `episodes` or a small `arcs`
   table → RFC §8.1 amendment, D-024 budget) only when the eval justifies an
   arc-level narrative/retrieval surface.
2. **Risk: false merges** (two unrelated tasks fused). Same class as cross-kind
   supersede; mitigate with a conservative threshold + entity-overlap gate, and keep
   it **reversible** (derived grouping over immutable episodes/records — re-cluster,
   never destroy).

**Why it matters / why gated.** Turns the episodic layer from session summaries into
long-horizon "living memory" (the differentiator vs flat RAG); it's the natural home
for Phase-24 causal traversal and Phase-27 resumption/proactive ("you're back on the
migration thread"). But it's "elegant, needs evidence": **gated on an episodic-eval
win** (does grouping improve cross-session QA / resumption?) per the D-035 discipline,
not shipped on intuition.

**Sequencing.** After Phase 23b (vector-over-narratives) and Phase 24 (causal links)
— by then the vector machinery and the episode-edge graph exist and threading is
mostly wiring. Not a v0.1-launch blocker.

## D-082 — Similar-episode contrast ships as a memory_episodes similar_to query; LLM window-synthesis deferred

2026-06-18. Phase 23b (RFC §6b) ships the **similar-episode contrast** half of what
D-080 deferred — the §6b "retrieve the most similar past episode and contrast
outcomes" behaviour — and **defers the gateway-synthesized window summary**.

**Capability.** `memory_episodes` gains a `similar_to` (free-text situation) query
with an optional `k` (default 5). It is backed by a new
`Retriever.SimilarNarratives` (gateway embed → `vindex.Search` filtered to
`kind=narrative` → load each hit's `EpisodeID`), wrapped by one `episodes.Similar`
view core that loads each episode + narrative and stamps the similarity `score`. The
core all three single-user surfaces call (D-067): `GET /v1/episodes?similar_to=&k=`
(HTTP), `EpisodesInput.SimilarTo/K` (MCP), `EpisodesRequest.SimilarTo/K` (SDK HTTP +
embedded). Output adds a per-episode `score` and a top-level `degraded` flag.

**No import cycle.** `episodes.Similar` takes a `NarrativeSearcher` interface that
`*retrieval.Retriever` satisfies structurally — the episodes view core stays
gateway-free (P5; retrieval does not import episodes).

**Degraded-safe (D-036).** Gateway/vindex unreachable ⇒ `degraded=true`, empty
results, **no error** — callers fall back to the deterministic `List`. The default
list/get/window path is unchanged and remains embedding-free.

**Parity holds despite the gateway.** The `mock` embedder is deterministic, so a
seeded store yields identical vector rankings across embedded/HTTP/MCP — the
byte-identical parity test (D-080's anti-drift guard) extends to the `similar_to`
leg (`TestEpisodesParity_Similar`), seeding the narrative vector into the shared
store BLOBs so every surface's hnsw index rebuilds the same vector.

**Deferred: LLM window-synthesis.** D-080's other deferred piece — a
gateway-synthesized cross-episode window summary — is **not** shipped here. The
deterministic windowed `List` (Phase 23) already returns the §6b structured summary
("never a raw fragment dump"), so synthesis is the explicitly-*optional* §6b step;
it adds a `Complete` path of marginal value whose output the mock gateway cannot make
parity-stable, and it should be pulled on a concrete eval/use-case signal (D-035),
not shipped on spec.

**No new config knob, no new schema** (read-only; reuses the retrieval gateway +
vindex).

## D-083 — Causal inference is a narration sub-step; why-traversal is a gateway-free memory_causal capability

2026-06-18. Phase 24 (RFC §5.6, §6b) ships **inferred causal links** + **why-traversal**
over the day-one `links` table — no new table or column, **no RFC §8.1 amendment**
(the `caused_by`/`led_to`/`inferred` enum values are day-one).

**Inference as a narration sub-step.** Rather than a standalone sweep (which would
need a new processed-marker column to avoid re-inferring 0-edge episodes), causal
inference runs **inside the Phase-22 narration step**, gated by the same
`narrative_memory_id`-absent check that makes narration idempotent — so it runs
**exactly once per episode**. After producing the narrative, the sweep gathers the
episode's decision-class memories (`decision|task|gotcha|pattern|strategy|failure_mode`)
via a new reverse-provenance store method `ListMemoriesByRecords` (both drivers +
conformance; index `idx_provenance_record`), asks the gateway for `led_to` edges
(schema-constrained `Complete`, P5/D-040), confidence-gates them
(`EpisodeConfig.CausalMinConfidence`, default 0.6), and commits the surviving edges
(`source="inferred"`) **atomically with the narrative** via `CommitSet.Links` + one
`causal.inferred` audit event each. A gateway/inference failure leaves the episode
narrated without edges (best-effort, advisory layer); re-inference is an explicit
future reindex, never a silent re-run (the §10 reindex discipline).

**Profile-internal knob (not top-level).** `CausalMinConfidence` lives in
`EpisodeConfig` alongside the episode-sweep intervals — a profile-internal constant
re-tuned by eval (D-035), **not** an operator-facing top-level config knob, so the
D-034 knob ceremony (profiles/explain/zero-config) does not apply (consistent with
how episode tuning + the playbook budget are handled). This is the one deviation from
the phase plan, which had proposed a top-level `lifecycle.causal_min_confidence`.

**Why-traversal is deterministic + gateway-free.** `causal.Traverse` (in `traverse.go`,
which imports no gateway — the file-level guard; `infer.go` is the only gateway-touching
file) walks `led_to`/`caused_by` from a memory, normalizing **both** stored
orientations to canonical cause→effect, in `backward` (causes — the default),
`forward` (effects), or `both`; it includes only **active** memories (non-active
endpoints are not traversed and their edges omitted), attaches provenance per node
(P1 drill-down at every hop), is cycle-safe (visited set), and caps `depth`
(`maxDepth=10`) + node budget (200) with a `truncated` flag (no silent truncation,
§11). It ships as the `memory_causal` capability across {SDK, HTTP, MCP} (D-067) with
a byte-identical parity test (deterministic — no gateway in the read); a missing/
non-active root ⇒ empty graph, no error (parity with `memory_episodes` get-missing).

**Cross-episode causality deferred** to Phase 24b (episode threading, D-081): Phase 24
scopes causality within a single episode's narrative frame.

## D-084 — Verification (memory_verify) + review queue (memory_review) on the single-user tier; producer is the explicit assert review flag

2026-06-18. Phase 25 (RFC §6c) ships the two trust safeguards that Phase 11 (citations)
left for later — **claim verification** and the **uncited-claim review queue** — and
defers reasoning-trace export to Phase 26.

**Claim verification.** `POST /v1/verify` / `memory_verify` / SDK `Verify` take a claim
+ citation handles, resolve the cited memories (the shared `trust.ResolveCited` over the
Phase-11 injection store), and run a **schema-constrained gateway entailment check**
(`trust.Verify`, P5/D-040) returning `{verdict∈{entailed,not_entailed,unclear},
confidence, explanation}`. Gateway-unreachable (or nil) ⇒ `unclear`+`degraded`, no error
(D-036) — the safeguard never blocks. Empty citations ⇒ `unclear`, no gateway call.
Ships on the single-user tier {SDK, HTTP, MCP} (D-067); parity is proven with the
deterministic mock gateway (byte-identical verdict across surfaces).

**Review queue (scope-level, not credential-admin).** Uncited agent assertions park as
`pending_review` (inert — not retrieved) and are listed + approved (→`active`,
retrieval-cache invalidated) or rejected (→`quarantined`, reversible — held, not
deleted) via `memory_review` (`GET /v1/review` + `POST /v1/review/{id}` / MCP
`memory_review` `{action:list|approve|reject}` / SDK `Review`). Resolution is atomic +
reversible (a `memory.review_approved`/`memory.review_rejected` event carries the prior
state, D-017), mirroring the Phase-18 confirm/reject discipline. The queue is a
**scope-level single-user-tier** capability (the scope owner reviews their own pending
memories at `/v1/review`), **not** an operator/credential-admin (`/v1/admin/*`)
function — RFC's "admin queue" is satisfied by a scope-level review/moderation surface.

**Producer = explicit `review` flag on `memory_assert`.** `AssertParams.Review` (SDK +
MCP, assert being Tier-A {SDK, MCP}, D-071) parks the asserted memory as
`pending_review` + a `memory.pending_review` event. **Automatic uncited-claim detection
is deferred**: routing "agent-generated extraction without citations" to review needs a
citation-on-ingest signal Stowage doesn't capture yet + an eval to tune false positives.
Phase 25 ships the full mechanism (verify + park + queue + reversible resolve) and the
explicit producer; auto-detection is a future eval-gated enhancement.

**No new schema** — `pending_review` + `quarantined` are day-one memory statuses (§8.1);
no new table/column, no RFC amendment. Two new MCP tools (count 15 → 17). Reasoning-
trace export stays Phase 26 (D-027/D-076).

## D-085 — Wave-D checkpoint (phases 23b/24/24b/25): findings fixed + accepted follow-ups

2026-06-18. A read-only checkpoint audit (CLAUDE.md §17, four adversarial passes:
wiring, invariants, test-quality, lifecycle-correctness) reviewed the just-merged
cycle. The cores, scopes (P3), gateway isolation (P5), and D-067 parity were found
clean. Fixed in the `chore(checkpoint)` PR:

- **Threading self-edge on shared narratives.** Phase-22 content-dedup (D-079)
  intentionally lets two same-user episodes with identical narratives **share** one
  narrative memory (N:1, not 1:1). Phase-24b threading then tried to thread such a pair
  and would write a self-referential `relates_to` edge (M→M). Fixed: `runThreadEpisodes`
  skips pairs whose episodes share a `NarrativeMemoryID`. (An earlier attempt to instead
  *prevent the share* by leaving the colliding episode un-narrated was reverted — it
  suppressed narration and **regressed the eval benchmark gate** (`answer_context_hit`
  0.85→0.76, D-035), confirming the dedup feeds retrieval and must stay.)
- **Promised fuzz targets delivered.** `FuzzCausalProposals` (causal index-mapping/
  decode) and `FuzzVerifyVerdict` (verify verdict decode/coercion) were promised in the
  24/25 plans but absent; added with seed corpora (§11).
- **Verify parity strengthened.** The `memory_verify` parity test now scripts a
  deterministic `entailed` verdict (`STOWAGE_MOCK_SCRIPT`) and asserts it propagates
  identically across {SDK,HTTP,MCP}, instead of only the coerced `unclear` default.
- **No-silent-truncation.** `causal.Traverse` now flags `Truncated` when the depth
  frontier has unexpanded neighbors (§11); `episodes.Arc` enforces `maxArcNodes`
  per-append; boot warns when a profile enables threading without episodes.

**Accepted follow-ups (not regressions; tracked, not blocking this checkpoint):**
- **Resolve double-resolve is not a compare-and-swap.** `trust.Resolve` and the
  pre-existing `reconcile.Resolve` (Phase 18) read-then-commit the status flip; two
  concurrent resolves of the same memory could race. Low-probability for a single-scope
  queue; the proper fix is a conditional `UPDATE … WHERE status=…` across both paths
  (a shared-store change deferred to avoid touching the Phase-18 confirm path here).
- **`links` has no `UNIQUE(from_memory,to_memory,type)`.** Idempotency rests on the
  app-level `ListLinks` pre-check + advisory locks (verified correct today); a partial
  unique index would be defense-in-depth.

## D-086 — Reasoning traces: reconstructed on demand, retention = source rows, ed25519-signed export (OQ-10 settled)

2026-06-18. Phase 26 (RFC §6c) ships reasoning-trace export and settles OQ-10.

**Reconstructed on demand, never stored.** A trace is assembled read-only from the
day-one tables (`internal/traces.Reconstruct`): for a `response_id`, the injections
(`ListByResponse`) → per injected memory its kind/content/status + drill-down
provenance spans (`GetJunctions` + record excerpts) + typed out-links (`ListLinks`),
plus the query + verification verdicts from response-keyed events. Because no trace is
persisted, **OQ-10's "retention class" is exactly the retention of the source
injections/events/records** — there is no separate trace store, retention column, or
sweep; the retention/DSAR cascade over the day-one tables governs traces for free.

**Two unbackfillable §6c signals now captured (D-024), schema-neutrally.** The query
text and verify verdicts were not in the day-one tables; both are written to `events`
keyed by `response_id` (event `SubjectID = response_id`, payload JSON — no new
table/column): `retrieve.query` is emitted on the **async** injection-writer path
(zero added retrieve latency, P2-respecting); `verify.verdict` is emitted by a new
`trust.VerifyClaim` core (resolve + verify + capture) that all three verify surfaces
now call (D-067 consolidation).

**Signed export.** `memory_trace` (`GET /v1/traces/{response_id}` + MCP + SDK,
single-user tier) returns a bundle: the trace + an optional **ed25519** detached
signature over the canonical trace JSON + the public key (CGo-free stdlib). The
signing key is operator-provided and env-indirected — config `trace.signing_key`
(an `env.VAR` ref to a base64 32-byte seed, D-030; validated fail-loud at boot);
empty ⇒ `signed:false`, bundle still returned (dev/zero-config). Per-export
`generated_at` (and the signature over it) are not byte-identical across surfaces; the
parity test compares the reconstructed content (timestamp zeroed) + that all surfaces
sign with the same key. `internal/traces` imports no gateway (deterministic read).

**No new schema.** Reconstruction reuses injections/events/records/provenance/links;
capture rides the events JSON payload. No table/column added; no RFC §8.1 amendment.

## D-087 — Proactive engine: gateway-light trigger rules, per-scope governance, accept/dismiss confidence tuning, no new schema (Phase 27)

2026-06-18. Phase 27 (RFC §6d) ships the proactive trigger engine and settles how
"the memory volunteers context" is realised without a knob explosion or a new table.

**Pull model, agent-initiated.** Stowage owns no session lifecycle (Harbor does), so
the agent PULLS at turn start: `GET /v1/suggestions?session_id=&query=` evaluates the
scope's enabled trigger rules and returns the budgeted, governance-gated offers. The
list endpoint is a write — it persists each offer as a `pending` suggestion (the
feedback + dedupe record) and emits a `suggestion.offered` event — so a repeated call
within a session does not re-offer the same memory (dedupe against the session's
any-status history). `POST /v1/suggestions/{id}` `{action: accept|dismiss}` resolves
an offer via compare-and-swap on `status='pending'` (double-resolve ⇒ `ErrNotPending`
⇒ 404). The CAS + its audit event (`suggestion.accepted`/`suggestion.dismissed`) live
in one `proactive.ResolveOffer` core that all three surfaces call (D-067 — no surface
can omit the event); the lifecycle sweep emits `suggestion.expired`. All four §6d
lifecycle events ship (§8 audit trail).

**Three trigger rules, scored by the retrieval scorer (no new scoring).** `recent_episode`
(the scope's most recent narrated episode ended within 7 days) and `expiring` (an
active memory whose `valid_until` is within 3 days) are **gateway-free**;
`similar_episode` (the past episode whose narrative resembles the query) embeds via the
injected `NarrativeSearcher` and is **degraded-safe** (D-036). Each rule's pre-utility
relevance becomes the `FusedScore` fed to `scoring.Score`, so a proactively-pushed
memory is subject to the same use/noise/decay/trust shaping as a pulled one — a noisy
or decayed memory can never be louder when pushed than when retrieved.

**Feedback tuning realises §6d "nothing static" (P4).** Per-`(scope, trigger_class)`
accept/dismiss tallies (the suggestions table's two counters — NOT the six memory
counters) drive a Laplace-smoothed confidence multiplier
`(accepted+1)/(accepted+dismissed+1)`, clamped to `[0.2, 1]`: a class the scope keeps
dismissing decays toward the floor and falls under the threshold; accepts restore it.
The multiplier scales the class's candidate scores before the threshold/budget gate.

**Per-scope governance in `scope_settings`, profile-defaulted, opt-out.** The effective
config (`enabled`, `threshold`, `budget ≤ 20`, `classes`) is the profile default
overlaid by the scope's stored `proactive` setting (RFC §6d "stored scope settings, not
config files"); a malformed stored setting fails safe to OFF. Profile defaults are
profile-internal (NOT top-level knobs, D-034): assistant on (threshold 0.45, budget 2),
fleet on (0.55, budget 1), coding-agent off — and the gateway-touching `similar_episode`
class is **off by default in every profile**, so a zero-config start makes no proactive
gateway calls (D-036/D-034). Governance read/write is an **admin-tier** capability
(`GET/PUT /v1/admin/proactive` + `memory_proactive_config`, {HTTP, MCP} only, D-067);
the single-user `memory_suggestions` ships on {SDK, HTTP, MCP}.

**Forgetting (P4).** An expiry sweep (`internal/lifecycle`, gateway-free, advisory-locked,
jittered, idempotent) GCs `pending` offers older than 24h (`status → 'expired'`, not
counted as accept or dismiss) so a missed offer does not permanently suppress
re-offering. Suggestions are derived/disposable — no provenance, no reconciliation.

**No new schema.** The `suggestions` and `scope_settings` tables are already day-one
§8.1 inventory; Phase 27 only wires them through the Store seam (both drivers + shared
conformance) and adds one index-only migration (`0010`). Tool count 18 → 20.

**Review-hardened behaviours (two adversarial passes — UX/value + depth/breadth).**
(a) Offers carry the memory's **content inline** — the offer is the volunteered
context, not a pointer. (b) `session_id` is **required** for list (`ErrSessionRequired`
→ 400): the per-session dedupe is the anti-spam defence. (c) Admin governance writes
**PATCH** (`proactive.WriteGovernance` core) — a one-field set never zero-wipes the
rest. (d) Feedback is **windowed to 30 days** so a suppressed class recovers (not
silenced forever). (e) `Get`/`Resolve` enforce **full scope** (P3 — no cross-user
resolve within a tenant). (f) `suggestion.expired` fires only for offers the sweep
**actually** transitioned (`ExpirePending` returns the real set via `RETURNING`).

**Deferred (recorded, not dropped).** Temporal pattern-mining (time-series frequency
analysis + an automation surface) is out of scope for v1 — a stretch follow-up. Also
deferred: per-scope (vs per-session) "already-offered" tracking; enabling the
gateway-backed `similar_episode` class by default (off everywhere for zero-config);
a uniform MCP-transport admin role gate (HTTP enforces it; MCP follows the existing
`memory_grants` tenant-scoped pattern).

## D-088 — Gateway-call usage events: async + scoped for complete/rerank; batched embed is Prom-metered only

2026-06-19. Bar-remediation (audit #2). §10 requires every gateway call to be metered
AND emitted as an event; the meter previously only drove Prometheus counters (a false
"Phase 05 wires the Meter to the event store" comment masked the gap), so cost
governance and the audit trail saw no provider usage.

**The PromMeter drives an optional `UsageEventEmitter`** (interface defined in the
gateway seam, no store import) wired at boot to a store-backed adapter. It emits a
`gateway.call` (complete) / `gateway.rerank` event carrying `{op, model, tokens/units,
cost}`.

**Async + non-blocking (§8).** The adapter enqueues onto a bounded channel (cap 512,
drop-on-full like the injection writer, D-025) and a drain goroutine performs the
durable `Events().Emit`. So a gateway call on the SLO-bound retrieval read path
(rerank, query embed) is never delayed by an events INSERT; the emitter's drain
goroutine is closed at shutdown.

**Scoped to the caller.** Events are attributed to the scope in ctx. Request-path
calls (retrieve, verify) already carry scope from the API boundary; the scope-less
pipeline/sweep calls (extract, reflect, narrate, causal-infer) now `identity.WithScope`
their ctx before the gateway call.

**Batched embed is Prom-metered ONLY (deliberate).** The embed path runs through the
async batcher, which coalesces inputs **across tenants** into one provider call on a
background ctx — so no single tenant legitimately owns the cost and the tenant-scoped
events table cannot attribute it. Embed token/cost therefore stays governed at the
Prometheus layer (`gateway_*_tokens_total{op="embed"}`); a process-level embed usage
event is deferred to the future `events/v1` stream (which is not tenant-scoped). This
is a documented boundary, not a silent gap.

**Reindex guard (audit #6).** Boot reads the distinct persisted embedding models
(`VectorStore.DistinctModels`, both drivers) and FAILS CLOSED if any differs from
`config.gateway.embed_model` or if the read errors — a model change is an explicit
reindex, never a silent mix of incompatible embeddings (§10).

**New event types.** `gateway.call`, `gateway.rerank` join the `subsystem.event`
convention; `Event.Type` is free-form so no enum change. Recorded here as the event-
stream contract addition (§8).

## D-089 — Grant topic_filter/kind_filter enforced; memory→topic association added (RFC §8.1 amendment)

2026-06-19. Bar-remediation (audit #3). Grant `topic_filter`/`kind_filter` (RFC §5.3)
were stored and surfaced on all three surfaces but enforced NOWHERE — a grant intended
to share a topic/kind slice silently exposed the entire owner scope (up to the zone
ceiling). Security-load-bearing over-share.

**kind_filter** maps to `memory.kind` and is enforced now. **topic_filter** had no
backing association — memories never recorded which extraction topic they pertain to —
so it was unenforceable without a schema change.

**RFC §8.1 amendment: `memory_topics` junction** (memory_id, topic_key, tenant_id;
migration 0011, both drivers). A memory is linked to the topic(s) it pertains to. The
extractor tags each candidate with the applicable topic keys (new optional `topics`
field on the candidate schema v2 + prompt v2 instruction); the extract stage validates
the tags against the scope's ACTIVE topic keys (drops hallucinated/inactive keys) before
the junction is written. Candidates with no topic match get no topic links (and thus
never satisfy a topic_filter — correct).

**Enforcement** is per-granted-scope, post-query in the retrieval fan-out (defense-in-
depth, the same model as the zone ceiling): `ScopedQuery` carries the grant's
`KindFilter`/`TopicFilter` (populated by `EffectiveScopes`, both drivers); after
`GetMany` for a granted scope, `kind_filter` filters by `memory.kind` and `topic_filter`
filters by the `memory_topics` link (batch `MemoriesTopics`). topic_filter **fails
closed**: if topic membership cannot be read, the granted scope's memories are dropped,
never over-shared. The caller's OWN scope is never filtered.

**Contribute grants reject filters.** `topic_filter`/`kind_filter` slice extracted
read memories; a contribute grant authorizes writing raw records (no kind/topic until
extraction), so a filter there is unenforceable — `CreateGrant` rejects it
(`ErrFilterOnContribute`) rather than silently authorizing anything.

**No LLM-quality risk to extraction.** The `topics` field is optional; the mock-gateway
eval (no topics in its scripted candidates) and existing extraction behaviour are
unchanged. The schema/prompt version bumps regenerate the goldens.

**Filter semantics + a recall note (D-089 follow-ups).** A grant with BOTH topic_filter
and kind_filter applies them as AND (sequential narrowing — the safe/more-restrictive
direction). Enforcement is post-fetch (defense-in-depth, like the zone ceiling), so a
heavily-filtered grant can consume ranking budget with non-matching memories and lower
recall of the caller's own results; pushing kind_filter into the per-scope lane queries
(LexicalSearch/FindNeighbors/vindex already accept Kinds) is a recall optimization for a
follow-up. Neither affects the security property (filtering only ever drops, never adds).

## D-090 — Reconcile augments structural neighbors with semantic (vector) neighbors

2026-06-19. Bar-remediation (simplifications A4/A5). Reconciliation found dedup/update/
supersede candidates ONLY by exact entity/keyword token overlap (structural) — so a
candidate that is the SAME fact phrased differently, sharing no token, was never
surfaced as a neighbor (missed dedup + missed contradiction/supersede). The vector lane
was fully built and stored but not consulted by reconcile.

Reconcile now embeds each candidate's enriched text (content+entities+keywords+queries,
reusing the D-047 builder) and runs a vindex search, MERGING the semantic neighbors
(cosine ≥ 0.70 floor) into the structural set so the candidate reaches the LLM reconcile
DECISION. Reflection candidates restrict the vector search to reflection kinds, mirroring
the structural filter (D-077 #5).

**Cosine drives RECALL, never auto-discard (A5, review-corrected).** The fast near-dup
auto-discard stays LEXICAL only (bigram-Jaccard ≥ 0.85 = near-identical surface form). A
cosine-only auto-discard would silently swallow corrections — a polarity flip ("X works"
vs "X does not work") embeds at high cosine but must reach the LLM, which detects the
contradiction and SUPERSEDES (Pearce-Hall, P4, brief 02). So semantic similarity only
WIDENS what the LLM sees; the LLM (not a threshold) decides dedup vs supersede.

Degraded-safe (D-036): when the vindex/gateway is unwired or the embed/search fails,
reconcile falls back to structural-only neighbors (the prior behaviour) — no write-path
hard dependency on the gateway. Wired via `ReconcileStage.SetVIndex` in boot.

## D-091 — Shared char/word-blended token ESTIMATE in internal/tokenize (BPE rejected)

2026-06-19. Bar-remediation (simplification A1). Three call sites estimated token counts
with an inlined `len(s)/4` — the extraction transcript clamp (pipeline.roughTokens), the
playbook budget (playbook.estimateTokens), and the day-one record TokenEstimate signal
(records.New, D-024). Three copies of one heuristic is drift, and bare `len/4` under-counts
normal prose by ~25% (English averages ~0.75 words/token, i.e. ~4.7 chars/token only for
dense text; whitespace makes prose cheaper per char but the WORD count then dominates).

**One leaf package.** `internal/tokenize` (zero deps) owns the single `Estimate(s) int`;
the three sites now delegate. It is an ESTIMATE driving clamping/budgeting, never
correctness or billing (the gateway meters real provider tokens, D-088).

**Algorithm: MAX of the two rules of thumb**, not mean. `max(bytes/4, ceil(words/0.75))`.
Because the estimate gates a CLAMP/BUDGET, under-counting is the dangerous direction (it
would silently overflow the context window); max never under-counts versus either rule.
Whitespace-sparse text (code, base64 blobs, CJK) tracks the byte rule; word-spaced text
tracks the word rule. Always ≥1 for non-empty input ("a tiny memory is never free").

The byte rule uses `len(s)` (bytes), NOT rune count — deliberately. Multibyte scripts
(CJK) cost ~1 BPE token per CHARACTER, far more than bytes/4, so counting runes would
under-count them (the dangerous direction). Bytes/4 is also the exact conservative
heuristic the three call sites used before this package, so no site's estimate dropped.

**Pure-Go BPE tokenizer REJECTED (reversing an earlier lean toward tiktoken-go).** An
exact tokenizer (tiktoken port) would either fetch the BPE vocab over HTTP at runtime —
breaking the offline single-binary guarantee — or embed it: cl100k ~1.6MB + o200k ~3.4MB
(~6.7MB total with the others) baked into an otherwise lean CGo-free static binary. That
is a poor trade for an estimate that never affects correctness, and it couples the binary
to a specific provider's vocab when the gateway is provider-agnostic (P5). The lean binary
is a differentiator; a clamping estimate does not justify spending multiple MB on it.

## D-092 — Hub dampening is durable, derived from injection query_sig (per-process LRU removed)

2026-06-19. Bar-remediation (simplification A6). The hub-dampening signal — "how many
DISTINCT query clusters returned this memory in the recent window", used by scoring to
penalise generic content (brief 02, CC-mem D-008) — was a per-process in-memory LRU
(`internal/retrieval.Hub`, 4096-entry). It reset to zero on every restart/deploy and was
not shared across processes, so a fresh process applied NO hub dampening until it had
re-observed enough traffic. That is a v1- simplification: the signal is inherently
cross-session and accumulating, so it must be durable.

**Derived from the injections table, not a new table.** The retrieve path already writes
one durable, scoped, async injection row per returned memory per retrieve (the attribution
backbone, D-025/D-051). Adding a single `query_sig` column (the same stable sorted-token
signature already used for the result-cache key) makes the hub signal a pure query:
`COUNT(DISTINCT query_sig) … WHERE memory_id = ? AND created_at >= ? AND query_sig <> ''`.
A new `InjectionStore.HubSignals(scope, memoryIDs, sinceMs)` returns it batched (one query
for all candidates), proven by the shared conformance suite on both drivers. No new table,
no new write path — the per-process LRU is deleted (only `QuerySig` and the window constant
remain in hub.go). RFC §8.1 amended (the one `query_sig` column); migration 0012.

**Recency window = 30 days** (`hubWindowMs`, a tuning constant, not a knob — D-034). The
old LRU bounded recency by capacity (4096 entries); the durable signal bounds it by time.

**Covering index, not just a filter index.** Migration 0012's index is
`(memory_id, created_at, query_sig)` — query_sig is included so both drivers satisfy the
`COUNT(DISTINCT query_sig)` from an index-only scan. A genuine hub memory has the MOST
injection rows (it is returned by the most queries), so the distinct count would otherwise
be the most expensive heap scan exactly for the memories this targets — on the latency-gated
retrieve path (D-035). The covering index keeps the hot case cheap.

**Signal source: actually-injected, not candidates.** The old LRU recorded every CANDIDATE
returned by a lane; injections record the final RANKED/returned set. "Returned to the agent"
is the truer hub signal than "appeared in a candidate pool", and it is the set we already
persist. THIS retrieve's own injection (carrying its query_sig) is written async after the
ACK, so it counts toward FUTURE retrieves, not the current one — correct for an accumulating
breadth signal (self-counting a single call toward its own dampening was never meaningful at
the threshold of 4 distinct clusters).

**Degraded-safe (D-036).** When the injection store is unwired (`retrieval.New` without
injections) or the HubSignals query errors, retrieval applies NO dampening (signals = 0) and
logs — never a hard dependency on the read. Empty-query_sig rows (pre-migration / non-retrieve
injections) are excluded from the count, so the migration is backfill-free.

## D-093 — Episode threading uses narrative-vector similarity, not word-overlap alone

2026-06-19. Bar-remediation (simplification A7). The episode-threading sweep
(`internal/lifecycle/threading.go`, Phase 24b, D-081) grouped cross-session episodes into
arcs by a single LEXICAL signal — content-word Jaccard overlap between the episodes'
narrative memories. Word overlap misses the case embeddings exist to catch: two episodes
that are the SAME arc described with different vocabulary share few literal words but sit
close in vector space. The narratives are already embedded (the embed sweep wrote their
vectors; retrieval's SimilarNarratives consumes them, D-082) — the threading sweep simply
wasn't reading them. A lexical-only threading signal is a v1- simplification.

**Add the semantic signal, gateway-free.** threadTenant now scans the tenant's stored
narrative-kind vectors once (`VectorStore.Scan`, kind="narrative") and attaches each
candidate's embedding. A pair threads on EITHER signal: `wordJaccard ≥ ThreadMinOverlap`
OR `cosine ≥ threadMinCosine`. The OR WIDENS recall (semantic similarity adds same-arc
pairs lexical overlap misses); it never narrows it. The sweep stays gateway-free (D-081)
— it reads the SAME stored vectors the embed sweep already wrote, never calling the
gateway to embed. The recorded link confidence is the stronger of the two qualifying
signals.

**Cosine floor is a package const (0.82), not a knob.** `threadMinCosine` mirrors the
reconcile cosine-floor precedent (D-090): a recall-widening threshold internal to the
algorithm, not a tunable surface, so it skips the D-034 knob ceremony. Narrative prose is
long, so genuinely-related arcs embed high; 0.82 is conservative against spurious
cross-arc edges. The existing `ThreadMinOverlap` (lexical) knob is unchanged.

**Degraded-safe (D-036).** When the vector scan fails, the vindex is unwired, or a
narrative was never embedded (degraded ingest), `narrativeCosine` returns 0 and the pair
relies on the lexical signal alone — exactly the prior behaviour. No write-path hard
dependency on vectors; the whole sweep remains OFF BY DEFAULT and eval-gated (D-035).

**Non-destructive, reversible.** Threading only writes `relates_to` edges (Source=
"inferred") over immutable narratives — the same reversible, idempotent edges as before;
only the candidate-selection signal widened. No schema change, no new config.

## D-094 — Claim verification captures the verdict against EVERY cited response, not just the first

2026-06-19. Bar-remediation (simplification A8). `trust.VerifyClaim` captures the
entailment verdict as a `verify.verdict` event keyed by `response_id` for the reasoning
trace (D-086). It keyed the event to the FIRST resolvable citation's response only — so
when a caller verified a claim citing memories injected across SEVERAL responses, only
the first response's trace recorded the verdict; the other responses' traces were silently
incomplete. The reasoning trace is an audit contract (RFC §6c) — a per-response trace that
silently drops the verdict for a claim its citations supported is a v1- simplification.

**Fix: emit to every distinct cited response.** `resolveCitedWithResponse` now returns all
DISTINCT response IDs the citations resolve to (first-seen order); `VerifyClaim` emits the
`verify.verdict` event once per response. The verdict itself is unchanged — the claim is
verified ONCE against the full cited memory set (one claim, one entailment check); only the
trace capture fans out, so each contributing response's trace is complete. No new schema,
no contract-shape change (same event type/payload, keyed per response). Scope-enforced
(P3) and degraded-safe (gateway failure ⇒ unclear+degraded, the verdict still captured).

## D-095 — The retrieval SLO gate bites; it is a reference-hardware release gate, not a per-PR CI job

2026-06-19. Bar-remediation (audit finding #4). The SLO rig (`internal/bench/slo`,
D-031) measured p99 but, on a miss, only logged an ADVISORY (`t.Logf`) — it never failed.
And `eval/SLO.md` claimed "Phase 13 gates CI on this benchmark — a regression blocks merge",
which was false on three counts: the rig is behind the `slo` build tag (excluded from
`go test ./...`), skips without a postgres DSN, and never failed on a miss. A gate that is
documented as enforcing but cannot fail is a v1- simplification.

**The gate now bites.** `TestSLO` calls `t.Fatalf` when the measured p99 exceeds the budget
(`-slo.maxp99`, default = the 150 ms binding target). A read-path change that regresses the
SLO now fails the build when `make slo` is run.

**It is a reference-hardware release gate, not a per-PR CI job — deliberately.** D-031 binds
the 150 ms target to *reference hardware*; asserting it on noisy shared CI runners would
flake (and seeding 10k memories + 5 000 concurrent retrieves per PR is disproportionate).
So the SLO stays the `make slo` reference-hardware gate (operator/release-env run, numbers
recorded in `eval/SLO.md`), while the eval benchmark suite (`make eval-ci`) is the gate that
runs on every CI build (D-035). This reconciles D-035's "runs in CI" with D-031's "reference
hardware": the eval benchmarks gate per-PR; the latency SLO gates on reference hardware. The
`-slo.maxp99` flag lets a slower-than-reference environment raise the budget deliberately
without recompiling; the binding number is always taken at 150 ms on reference hardware.
Recording the actual numbers remains operator-run (the bar-remediation eval/SLO track).

## D-096 — One public-benchmark runner behind a dataset registry (LoCoMo wired; longmemeval_s + dataset-gain runnable)

2026-06-19. Bar-remediation (audit finding #7). The eval harness had a LongMemEval
oracle run path (the fullmode test hardcoded the longmemeval path + normalizer), but:
LoCoMo had a fetcher + normalizer that NO runner consumed (built, unwired); longmemeval_s
(the distractor headline variant) was reachable only by overriding a URL env var with no
runner entry; and there was no factory mapping a dataset name → its fetch/normalize. Built-
but-unrunnable benchmarks and a copy-paste-per-dataset runner are a v1- simplification of
the D-035 benchmark commitment.

**One runner, dataset as a parameter.** A dataset **registry** (`eval/datasets`) maps a name
→ `Spec{DataFile, Fetch, Normalize}`; each dataset subpackage self-registers in `init()`
(blank-imported by the runner/CLI), so adding a benchmark is a new package + one Register
call — no central switch. The generic `harness.RunDataset` (extracted from the old
longmemeval-only fullmode body) ingests the needed conversations, settles, scores, and
optionally judges ANY normalized dataset. `TestFullMode` now selects the dataset via
`STOWAGE_EVAL_DATASET` (longmemeval | longmemeval_s | locomo) and runs them all through
that one path; the CLI `eval fetch` and `TestGainDatasetMode` (gain memory-on/off over a
dataset's questions, reusing the `judgeOnOff` primitive) resolve through the same registry.

**Wiring is CI-proven; numbers stay operator-run.** `TestRunDataset_Wiring` drives a tiny
dataset end-to-end through `RunDataset` with the MOCK gateway and a scripted extraction
(deterministic, no paid call), and `TestDatasetRegistry` asserts the three datasets resolve
— so the dataset→runner wiring can never silently rot. The benchmark NUMBERS (live gateway)
remain operator-run via the fullmode entries; REPORT.md carries the runner docs + the
operator-fill placeholders for longmemeval_s / LoCoMo / dataset-gain.

**A latent async-buffer race fixed.** Ingest enqueues to the buffer stage asynchronously
(records_handler non-blocking channel → stage goroutine), so a flush issued right after the
ACK could race the buffer-append and no-op (the old longmemeval path only avoided this by
ingesting many conversations before flushing). `RunDataset` now waits via
`TestServer.WaitForBuffered` (BufferStore.ListDue) before each flush — deterministic for
single- and multi-conversation datasets alike.

## D-097 — OQ-5 resolved: Stowage ships under Apache-2.0

2026-06-20. Phase 21 (terminal hardening & launch gate). RFC OQ-5 left the open-source
license + timing open, gated on the SOTA benchmark report being in sight (§12) — now it
is (the judged-QA number + the dataset runners, D-076/D-096). The owner picks **Apache-2.0**.

**Why Apache-2.0.** Permissive with an explicit patent grant — the ecosystem-standard
choice for Go infrastructure, and the right fit for a four-part interoperating ecosystem
(Portico/Harbor/Dockyard/Stowage) where downstream adoption + contribution should be
frictionless. The benchmark-led launch (brief 06 — "benchmarks ARE the marketing") is
built to win open-source adoption; a permissive license maximizes that.

**BSL-style (cloud-protective) alternative — considered and rejected for v0.1.** RFC OQ-5
floated a source-available license to protect against managed-cloud free-riding. Rejected:
(a) it would dampen the open adoption the launch is designed to win; (b) the managed-cloud
control plane is explicitly a SEPARATE codebase (§14), so the commercialization-protection
concern belongs there, not in this repo, which keeps the seams clean for it (identity,
metering events, per-scope ceilings) without needing a restrictive license here. If a
managed offering later warrants it, that is a future decision in the control-plane repo,
not a re-license of this one.

The LICENSE file + the launch artifacts land in Phase 21; until merged there is no LICENSE
file (superseding the "no LICENSE file until then" stance of the earlier D-entries).

## D-098 — DSAR cascading delete: the sanctioned P1 verbatim-delete exception

2026-06-23. Phase 21 (launch hardening). Acceptance criterion §21.1.5 requires a
DSAR (Data Subject Access Request) export + **cascading delete per (tenant, user)**;
the live-acceptance gate's last red (#57) was `DELETE /v1/admin/users/{user}` still
returning 501 (`handleDSARStub`). The DSAR cascade is implemented as a single Store-seam
method, `OpsStore.DeleteUserData(ctx, scope) (DSARCounts, error)`, wired to the admin-only
HTTP route.

**It is the one and only verbatim-delete exception.** CLAUDE.md §6 / P1 forbids deleting
or mutating verbatim `records` outside "the explicit retention/DSAR cascade". This method
**is** that cascade: it is the sole code path that deletes `records` rows. Every other path
treats verbatim records as immutable.

**Cascade semantics (binding).**
- **Scope (P3).** `scope` MUST carry both `Tenant` and `User`; either empty ⇒
  `ErrScopeRequired`. There is no tenant-wide or unscoped purge. The HTTP handler takes the
  tenant from the caller's **admin** key (an admin can only purge within their own tenant)
  and the user from the `{user}` path segment.
- **One transaction, FK-safe order (children → parents).** SQLite runs it as one `exec`
  closure (one `sql.Tx`); Postgres as one `pool.Begin`→`tx.Commit`. Order: the children of
  the user's memories (`provenance`, `memory_entities`, `memory_keywords`, `memory_queries`,
  `memory_topics`, `memory_vectors`, `links`, `feedback`, `injections`) → the user_id-scoped
  tables (`topics`, `suggestions`, `buffer_items`, `scope_settings`, `group_members`,
  `grants`, `branches`, `episodes`) → `memories` → `records` (the P1 delete) → the user's
  own `events`. The `topics` (extraction-magnet) table and the `memory_topics` junction are
  distinct and both swept; `DSARCounts` reports them under separate keys
  (`topics` vs `memory_topics`).
- **Cross-user references are swept too.** Tables with a non-`ON DELETE CASCADE` FK to
  `memories`/`records` (`injections`, `links`, `provenance`, `feedback`) are deleted by
  membership in the user's memory/record set **in addition to** by `user_id`, so a row
  another user owns that *cites* the purged user's memory (a team-shared retrieval injection,
  an inferred cross-memory link) is removed — otherwise the FK-restricted `memories`/`records`
  deletes would fail and a dangling reference to the erased user would survive. `grants` are
  deleted by **owner** `user_id` (the grant's owner column), the user being erased.
- **Audit survives the purge.** A `user.purged` event is emitted **at tenant scope**
  (`user_id` NULL, subject = the purged user) *after* the user's own events are deleted, so it
  is not itself purged. Its payload carries the per-table `DSARCounts` + the purged user id —
  the durable record that the erasure happened and what it covered (CLAUDE.md §8: every
  mutation emits an event with its reason).
- **Counts returned.** `DSARCounts` reports a row count per table; the handler returns it as
  `200 {user, counts}`. A user with no data is a no-op that still emits the event (zero count).

**Both drivers + conformance.** Implemented on `sqlitestore` and `pgstore`; the shared
conformance suite proves completeness (every populated table reaches zero, counts > 0),
cross-user isolation (a second user's own rows survive; their rows referencing the purged
user are removed), cross-tenant isolation (the same user id in another tenant is untouched),
and the empty-scope guard. The `feedback` table currently has no writer in the codebase, but
the cascade covers it defensively so a future writer needs no DSAR change (schema-complete).

**Scope of the sweep — what is and isn't reached.** The cascade removes (a) every row in a
`user_id`-bearing table owned by the user, and (b) every row in any table that holds an
FK-enforced reference to the user's memories/records (including rows another user owns that
*cite* them). It deliberately does NOT chase **soft (non-FK) TEXT references** — e.g.
`memories.supersedes_id`/`superseded_by_id`, `suggestions.memory_id`/`episode_id`,
`episodes.narrative_memory_id`. In practice these only point within a single scope
(reconciliation, suggestion offers, and narration are per-(tenant,user)), so a *cross-user*
soft reference to a purged memory is not produced by any current code path; the in-scope ones
are deleted with the user's own rows. If a future feature creates cross-user soft references,
sweeping them becomes a follow-up — called out here rather than silently assumed.
Likewise `feedback` has no `user_id` column (and no writer yet): it is erased by
`memory_id`-membership, so feedback the user left on *another* user's memory would not be
attributable for erasure; revisit if a feedback writer that records authorship lands.

**Schema note.** No new tables/columns — the cascade operates over the existing RFC §8.1
inventory, so the D-024 schema-budget guardrail is untouched.

## D-099 — Topic packs compose; `pack:on:<name>` enables a pack at a scope (amends D-043)

2026-06-23. RFC §5.4. D-043 established compiled-in virtual default packs with an
**all-or-nothing** rule: a single profile-selected pack, and *any* explicit topic
suppressed it entirely. In practice that is two limitations: (1) only one pack can
ever be active, and (2) you cannot layer your own topics on top of a pack — adding
one explicit topic nukes the pack. Real deployments want composition: an assistant
working in a project wants the personalization pack *and* a project pack *and* a few
bespoke topics at once. This decision **amends D-043** (it does not repeal it — the
zero-config default-pack behavior and the `pack:off` opt-out are preserved).

**Decision — packs are additively composable; `profile` decouples from pack selection.**

- **Effective topics = a deduped union.** For a scope, `topics.Service.ActiveTopics`
  returns `union( entries of every ENABLED pack , explicit topics )`, deduped by topic
  key with **explicit > pack** (and, among packs, first-enabled wins). The `source`
  field becomes `explicit` or `pack:<name>` (was the bare `pack`) so `GET /v1/topics`
  shows each entry's origin.
- **Enabling a pack is a sentinel topic, mirroring `pack:off`.** An active topic row
  whose key is `pack:on:<name>` enables the compiled-in pack `<name>` at that scope.
  This keeps composition **scope-aware and runtime-configurable through the existing
  topics API/SDK/MCP** — no new YAML knob (D-034 untouched; the `pack:off` precedent,
  D-043, is the pattern).
- **`profile` selects an ORDERED LIST of default packs** (currently one element each:
  `assistant → [pack:preferences]`, `coding-agent`/`fleet → [pack:agent-learnings]`).
  Treating it as a list — rather than the single pack of D-043 — future-proofs
  multi-default profiles (e.g. a `coding-assistant` profile that defaults to
  `[pack:preferences, pack:agent-learnings]`) without another algorithm change.
  `profile` keeps its other roles unchanged (extraction token budget, scoring).
- **Default packs apply only when the scope expressed no intent.** Resolution order:
  1. `pack:off` present → suppress the **pack layer** (the profile default AND any
     `pack:on`); explicit topics are kept. The result short-circuits extraction (no
     gateway call) only when no explicit topics remain. `pack:off` dominates the pack
     layer, not explicit topics — preserving D-043, under which `pack:off` was ignored
     when explicit topics were present and opted out when it stood alone.
  2. else `union(enabled-pack entries, explicit topics)` is non-empty → return it (the
     profile's default packs are NOT auto-added — the operator is in control, the spirit
     of D-043's all-or-nothing, now at the granularity of "expressed any intent").
  3. else → the profile's default-pack list (the zero-config path, unchanged from D-043).

  > **Clarified during Phase 28 implementation (in-PR, no substance change):** step 1's
  > earlier wording ("return nil") was self-contradictory with "preserving D-043" — a bare
  > `return nil` would also drop explicit topics, which D-043 never did. The
  > D-043-preserving reading is authoritative and is what ships: `pack:off` suppresses
  > only packs; explicit topics survive; the short-circuit fires only on an empty result.
  > RFC §5.4 carries the matching wording.
- **Bounded composition (no silent caps).** The composed set is capped at a package
  constant `maxActiveTopics` (a recall/cost guardrail internal to the algorithm, like
  the reconcile cosine floor of D-090 — *not* a knob, so it skips D-034 ceremony). When
  the union exceeds the cap, **explicit topics are kept first**, then pack entries by
  enable order; the dropped entries are logged and emitted as an event (the "no silent
  truncation" principle), never quietly discarded.
- **One logic core (D-067).** All of the above lives in `topics.Service.ActiveTopics`,
  so SDK, HTTP, and MCP inherit composition identically; no surface re-implements it.

**New packs shipped with the mechanism.** Composition is only as valuable as the packs
it can combine. The implementing phase ships, alongside the existing `pack:preferences`
and `pack:agent-learnings`, at least `pack:project` (domain glossary, ownership,
environments, runbooks, conventions — distinct from the broader agent-learnings) and
`pack:incidents` (outages, root causes, postmortem lessons → a `gotcha`/`failure_mode`
generator). A documented backlog — `pack:product`, `pack:people`, `pack:compliance`,
`pack:research`, and a narrow `pack:coding-style` split out of agent-learnings — extends
coverage later; packs are compiled-in constants, so adding one is a code change with a
golden-test update, never a schema migration.

**Consequences.** Backward compatible: a scope with no topics still gets its profile's
default pack; a scope with explicit topics still uses them (now composable, not
suppressive); `pack:off` is unchanged. Packs stay virtual (never persisted) and
transparent (`GET /v1/topics` shows `source: pack:<name>`). The cost ceiling is explicit
(`maxActiveTopics`) rather than emergent. **Process:** this is a settled-decision change,
so it lands as an RFC §5.4 amendment + this superseding entry; the resolution-logic,
sentinel, cap, and new packs are implemented in a named follow-up phase (the phase-07
plan carries a forward pointer here), not in this docs PR.

## D-100 — Gateway seam: optional per-request model override + reasoning effort

2026-06-23. Eval-enablement for the LongMemEval benchmark run. The `gateway.CompleteRequest`
gains two OPTIONAL fields, both zero-valued by default so every existing caller is
byte-for-byte unchanged:

- **`Model string`** — overrides the gateway's configured completion model for a single
  call (empty = the configured model). It lets one gateway answer eval questions with a
  strong **reader** model (e.g. `anthropic/claude-sonnet-4.6`) while extraction/reconcile
  keep using the cheap configured model — without standing up a second gateway (the
  D-076 "second gateway" deviation is resolved by this instead).
- **`ReasoningEffort string`** — requests provider reasoning / extended thinking at
  `"none"|"minimal"|"low"|"medium"|"high"` (empty = no reasoning param). The bifrost
  driver maps it to bifrost's native `ChatParameters.Reasoning.Effort`; the openaicompat
  driver maps it to the OpenAI-style `reasoning_effort` field. Providers that don't
  support it ignore it.

**Why on the seam (P5), not a per-caller hack.** Both are genuine, reusable provider
capabilities, and routing them through `CompleteRequest` keeps `internal/gateway` the
single place that knows wire formats. Both drivers (`bifrost`, `openaicompat`) honor
them; the `mock` driver ignores them (scripted responses). Golden/unit tests assert (a)
the default path is unchanged (no `reasoning`/`reasoning_effort` emitted, configured
model used) and (b) the override path sets the model + reasoning. The eval harness wires
the reader/judge through `JudgeQuestionWith(ReaderOpts{Model, ReasoningEffort})`.

**Scope notes.** Metering still attributes a completion to the gateway's *configured*
model (the override is an eval-harness convenience; per-model cost attribution is not a
goal here). No new config knob (D-034 untouched) — the override is request-scoped, set by
the eval harness from `STOWAGE_EVAL_READER_MODEL` / `STOWAGE_EVAL_READER_EFFORT`. No schema
change.

## D-101 — LongMemEval extraction needs a seeded topic set, not the default preferences pack

2026-06-23. Eval-enablement. Extraction is topic-gated (D-007/D-043/D-099): a candidate
that matches no active topic is never created. A full-mode LongMemEval run previously
relied on the assistant profile's virtual `pack:preferences` (4 personalization topics),
so the breadth of facts LongMemEval probes — events, dates, possessions, relationships,
numbers, knowledge-updates, temporal facts — had **no home** and was silently dropped,
starving retrieval on most questions.

**Decision.** The full-mode runner seeds a broad, compiled-in **LongMemEval extraction-
magnet set** (`eval/harness/topics_seed.go`, 12 tight non-overlapping topics tuned to the
LongMemEval question taxonomy) at the eval scope via the live `PUT /v1/topics` surface,
before ingestion (`RunDatasetOpts.SeedTopics`). Explicit topics suppress the default pack
(D-099), which is intended — the run wants exactly this set. CI/mock runs do not seed
(their extraction is scripted). This is an eval-harness fixture decision, not a product
default: production scopes still choose topics/packs per D-099.

## D-102 — Structured-output schemas are OpenAI-strict-compliant; value constraints live in server validation

2026-06-23. A learner-model investigation surfaced a real product gap: Stowage's
gateway schemas could not be used with **OpenAI** (or Azure OpenAI) structured
outputs — `gateway.Complete` returned **HTTP 400** for every OpenAI model. The cause
was Stowage's own schemas, not the models: OpenAI's *strict* structured-output mode
(the strictest, best-specified of any provider) forbids the JSON-Schema **validation**
keywords `minLength` / `minimum` / `maximum` / `minItems` / `maxItems` / `pattern` /
`format` / `multipleOf`, and requires **every** property to appear in `required` with
`additionalProperties: false` on every object. Our `CandidateSchema` (and four others)
used `minimum`/`maximum`/`minItems`/`minLength` and left some properties optional. A
provider-agnostic memory product (P5) that can't accept the most common provider is a
defect — "we don't support OpenAI" is not shippable.

**Key realization (why this is safe):** those value/length keywords buy *nothing* at
generation time. Constrained decoding enforces **shape** — types, `enum`, `required`,
`additionalProperties` — not value ranges; OpenAI rejects the keywords outright and
lenient providers (Gemini, etc.) silently ignore them. What actually keeps values in
range is (a) the **prompt** ("rate importance 1–5; confidence 0.0–1.0") and (b)
**server-side validation** that already re-checks every field (`isValidCandidate`:
empty content, importance ∈ [1,5], confidence ∈ [0,1], provenance ≥ 1; spans clamped).
So the keywords were redundant *and* the thing breaking compatibility.

**Decision.** All gateway structured-output schemas (D-040 / §10) are made
OpenAI-strict-compliant:
- **Drop** all value/length validation keywords. Value ranges are documented in each
  field's `description` (a prompt-level hint) and enforced server-side, where they
  always actually were.
- **Every property is in `required`** with `additionalProperties: false` on every
  object. Conditionally-relevant fields (e.g. reconcile `content`/`target_ids`/`links`)
  become required with empty-value semantics (`""`/`[]`); the server interprets them
  per-action (`validateDecision`), exactly as before.

**Schemas updated:** `CandidateSchema` (extract, v2→v3), `reflectionSchema` (reflect,
v1→v2), `DecisionSchema` (reconcile), `narrativeSchema` (episodes), `causalSchema`
(causal), `verifySchema` (trust). The eval reader/judge schemas were already compliant.

**Consequences.** Stowage's gateway now works with OpenAI/Azure structured outputs and
is *more* compatible everywhere (a strict-compliant schema is a subset all providers
accept). Verified by direct probes (the same `CandidateSchema` went 400→200 on
`gpt-5.4-nano` and `gpt-4o-mini`) and a live extraction run (gpt-5.4-nano: hundreds of
memories, zero schema failures). The change also *rescued* models that were previously
flaky for extraction (`inception/mercury-2`). No safety lost: server validation is
unchanged. A regression guard asserts the exported schemas carry none of the forbidden
keywords.

## D-103 — Retrieval profile windows are config-tunable; the per-request limit is honored under rerank

**Context.** The three retrieval profiles (`precise`, `balanced`, `broad`) encoded their
`{laneK, scoringK, defaultLimit}` windows as hardcoded package presets. `precise`
(the rerank profile) capped `scoringK` at **10**: only the top-10 fused candidates were
scored/reranked, and the final result was trimmed to `limit` *after* that. So a caller
asking `/v1/retrieve` for `limit: 25` with rerank on silently received **≤10** memories —
the `limit` knob was a no-op past the preset, and there was no way to widen it.

A LongMemEval K-sweep (re-scoring a frozen learn store at increasing K, brute vindex)
exposed this: `answer_context_hit` rose 0.24→0.32 from K=5→10 and then **flat-lined** at
every K≥10 with exactly 10 items returned per question — the `scoringK=10` wall, not a
recall ceiling. Memories are compressed (~30–40 tokens each; the K=5 context was ~166
tokens vs a ~6,200-token raw haystack — a ~37× dividend), so feeding *more* of them is
nearly free. The cap was leaving recall on the table for no token saving.

**Decision.**
1. **Honor `limit` under every profile, including rerank.** `Retrieve` floors the scoring
   window up to the requested limit (`scoringK = max(prof.ScoringK, limit)`) and the lane
   window up to that (`laneK = max(prof.LaneK, scoringK)`). The reranker now sees the full
   requested window; `limit` is the honest K knob on all surfaces. A request can never be
   silently clamped below what it asked for (still bounded by the hard `maxLimit=50`).
2. **Make the profile windows config-tunable** via a new optional `retrieval:` section
   (`precise`/`balanced`/`broad`, each `{lane_k, scoring_k, default_limit}`). A zero/omitted
   field inherits the built-in preset, so the all-empty default reproduces the shipped
   presets exactly — the tuned default that applies in every deployment profile (D-034).
   Reranking is **not** configured here — it remains a property of the `precise` profile,
   wired via `gateway.rerank_model`. Validation: non-negative, `scoring_k ≤ lane_k`,
   `default_limit ≤ 50`. Wired in `boot` via `Retriever.WithProfiles(retrieval.BuildProfiles(...))`.

**Evidence.** With the floor fix, a rerank-on K-sweep over the same frozen learn store
climbed monotonically: `answer_context_hit` 0.28 (K=5) → 0.32 (10) → 0.36 (20) → 0.38 (30)
→ 0.40 (40) → **0.44 (50)** — ~+57% relative, still rising at the `maxLimit=50` cap, while
per-question context stayed cheap (~1,025 tokens at K=30, still ~6× compression). The
plateau was the bug, not the data.

**Consequences.** The retrieval `limit` finally behaves as advertised with rerank. Operators
who want a deeper rerank window (or a different recall/latency posture per profile) set
`retrieval.precise.scoring_k` etc. instead of editing the binary. No behavior change for
existing deployments (empty section = presets). Whether the recall lift translates to higher
*judged* answer quality is measured separately (the judged K-sweep); `answer_context_hit` is
the substring proxy and undercounts.

## D-104 — Numeric corrections bypass the lexical near-dup auto-discard (route to supersede)

Phase 29. The reconcile near-dup pre-filter auto-discarded any candidate whose
`BigramJaccard` ≥ 0.85 against a neighbor, on the documented assumption that a real
contradiction is never *lexically* near-identical (a polarity flip embeds at high cosine
but reads differently). That assumption has a hole: a **numeric correction** — "120 stars"
→ "125 stars", "6 months" → "9 months", "30 minutes" → "45 minutes" — IS lexically
near-identical (one token differs) yet is a contradiction. The old path silently discarded
the correction AND bumped the stale neighbor's `match_count`, *raising the stale value's
retrieval rank* — the exact LongMemEval stale-value miss (see [[longmemeval-miss-analysis]]).

**Decision.** Before the near-dup auto-discard, `NumeralsDiverge(candidate, neighbor)` checks
whether the two carry a different multiset of numerals (commas stripped so "5,850"=="5850").
If they diverge, the candidate is NOT discarded and the neighbor's match_count is NOT bumped —
it falls through to the LLM decision, which supersedes the older value. Deterministic, no
embedding/cosine cost. Paraphrase dups with identical numerals still take the fast discard.
Pairs with D-105's prompt change (supersede-on-different-value) and the false-supersede metric.

## D-107 — Assistant extraction buffer window coarsened for context retention

Phase 29. The assistant profile flushed the extraction buffer at Count=12 / Tokens=1500 /
MaxAge=90s. The low token cap meant extraction often saw only a few turns at a time, so a
memory could be written shorn of the disambiguating context stated turns earlier — e.g.
"about 30 minutes" losing the "each way" qualifier that the fuller assertion kept. That both
starves the reader and *manufactures* false contradictions reconcile then cannot match.

**Decision.** Coarsen the assistant (and fallback) buffer triggers to Count=18 / Tokens=2500 /
MaxAge=180s (D-042 family; tuned default in every profile, D-034) so extraction sees more
conversation per call and emits fewer, richer, better-contextualized memories. Paired with the
Phase-29 extraction-prompt change (PromptTemplateVersion 3) requiring qualifier/unit/scope
retention and a populated `context`. Guardrail: an over-broad-merged-memory eval metric watches
the merge-two-distinct-facts hazard a wider window introduces.

## D-105 — Superseded memories are retained-and-flagged in retrieval (dual-visibility, §6c)

Phase 29 (H5). Once supersede fires (D-104/D-107 made it fire), the retired value is
hidden from retrieval (the read path filters `status='active'`). That is the "keep one"
behaviour; the human directed instead that BOTH values stay retrievable but the stale one
be **flagged**, so the agent can reason "you said X, then Y" (RFC §6c calibrated
uncertainty) rather than silently losing the history.

**Decision.** Retrieval optionally surfaces the superseded predecessors of the returned
memories, flagged `Stale` with a `superseded_by` link to the current value, demoted (score
×0.5, ranked below their successor) and bounded (`maxStaleCompanions`=8 per response) so
dual-visibility can never blow up the context. Driven off the existing scoped
`ListSupersededBy` (P3 — no unscoped variant); best-effort (a lookup error drops that
item's history, never fails the retrieve). Surfaced on **all read surfaces** (HTTP, MCP,
SDK item payloads: `stale` + `superseded_by`) per D-067. The eval reader prompt and any
consumer is told to prefer the current value and not hedge against an `[OUTDATED]`-tagged item.

Config knob `retrieval.include_superseded` (default **true** — dual-visibility is the
intended behaviour; D-034 tuned default in every profile). Operators who want active-only
retrieval set it false. This is a read-time property and composes with the write-time
supersede fixes: write-time decides which value is current; read-time decides whether the
agent also sees the retired one (flagged).

## D-106 — Reconcile winner-selection is deterministic by assertion order (record-ULID turn order)

Phase 29. Supersede fires (D-104/D-105) but was picking the WRONG winner ~half the time:
after a re-learn, Fitbit's correct "9 months" was superseded by the stale "6 months", and
painting kept "4" over the gold "5". Cause: the reconcile decision prompt frames the
candidate as "the newest assertion", but when two contradictory values land in the SAME
flush (more likely after the D-107 window coarsening), they are extracted together and the
LLM's arbitrary candidate output order — not conversation order — decided which superseded
which. So the winner was a coin-flip.

`occurred_at` cannot break the tie here: in the LongMemEval oracle it is session-granular
(~60 distinct values over 1036 records), so both values in one session share it. Record IDs,
however, are ULIDs — monotonic in ingestion order, which IS conversation/turn order.

**Decision.** Before processing a flush's candidates, the reconcile worker stable-sorts them
ascending by `candidateAssertionKey` = the LATEST source-record ULID among a candidate's
provenance (turn order). The older assertion commits first; the newer one then supersedes it,
so the CURRENT value wins deterministically regardless of LLM emission order. Cross-flush
contradictions were already handled (a later flush is genuinely newer); this fixes only the
within-flush ambiguity. Supersede reversibility (D-070) is unchanged. This realizes the
D-024 "occurred_at recency" intent at a finer (turn) granularity than session occurred_at.

## D-108 — The reconcile supersede/merge decision is context-aware (sees the original turns)

Phase 29b. The reconcile decision (add/update/merge/supersede/park) is an LLM call that saw
ONLY the derived memories — the candidate plus neighbor memories — with no conversational
context. So when two memories carried different values, the model could not tell a *correction
of one fact* from *two distinct facts that share words*, and over-superseded: the commute
"45 minutes each way" and "about 30 minutes" (arguably audiobook-listening time vs work-commute
time) were merged, and D-106 then kept the wrong one. Detection (H4) would only make
over-supersede worse; the right lever is giving the existing decision more CONTEXT.

**Decision.** `ReconcileStage` takes an optional `RecordStore` (`SetRecordStore`; nil ⇒ current
behaviour, degrade-safe). Before the decision, it assembles a bounded conversation-context block —
the raw provenance turns behind the candidate (`Candidate.Provenance` → `RecordStore.GetMany`)
and behind each neighbor (`GetJunctions(neighbor).Provenance` → `GetMany`) — capped at
`maxContextRecords`=12, deduped, best-effort (a fetch error proceeds with no block). `BuildUserPrompt`
renders it as "## Original conversation context" and the system prompt instructs: use it to decide
whether the candidate CORRECTS the neighbor (→ supersede/update) or states a DIFFERENT fact that
merely shares words/numbers (→ add); when the turns don't show them as the same fact, prefer add.

Safe under P2 (reconcile is async, off the ingest ACK) and reversible (D-070 unchanged). No schema
change, no new store method (`GetMany`/`GetJunctions` already exist). Targets the over-supersede
residue directly and sharpens every decision, lower-risk than H4.

## D-109 — Memories capture the assertion (conversation) date and retrieval surfaces it

Phase 29c. Investigating the SOTA gap (Hindsight reports 0.946 on longmemeval_s), their
published injection output showed each "memory" carries a **timestamp** ("When: May 28, 2023" +
`_mentioned:`) and their reader's own reasoning cites dates to answer temporal/knowledge-update
questions. Two findings on our side: (1) memories never captured the assertion date —
`candidateToMemory` left `ValidFrom=0`; (2) worse, the eval harness **discarded the dataset's
session timestamps at ingest** and stamped `occurred_at = now`, so every record/memory shared
"today" — useless for temporal reasoning or date-resolving stale values.

**Decision.** Capture the assertion date end-to-end as a day-one signal (D-024) and surface it:
- Extract stamps `Candidate.OccurredAt` = earliest `occurred_at` among the candidate's provenance
  records (the conversation date, not extraction time).
- `candidateToMemory` sets `Memory.ValidFrom = Candidate.OccurredAt`.
- Retrieval surfaces it as `occurred_at` on the item across all read surfaces (HTTP/MCP/SDK), D-067.
- The eval reader renders `| When: <YYYY-MM-DD>` per memory so it can do temporal reasoning and
  pick the latest value among conflicting ones at read time — a cheaper complement to write-time
  supersede for the stale-value/knowledge-update class.
- The eval harness now passes the dataset's real per-turn timestamps to `record.occurred_at`
  (the ingest API already accepted `occurred_at`; the harness was sending 0 → now).

Production already supported caller-supplied `occurred_at` on ingest; this closes the
memory-side capture + retrieval surfacing gap. Reversibility/scoping unchanged. Needs a re-learn
to populate real dates on the existing eval store.

## D-110 — Decay grace is computed in milliseconds (unit-bug fix)

Phase 29d (audit finding #5). `lifecycle/decay.go` computed the below-floor grace as
`int64(DecayGraceSweeps) * int64(DecayInterval)`. `DecayInterval` is a `time.Duration`
(nanoseconds), so `int64(DecayInterval)` is a nanosecond count used as **milliseconds**
against `nowMs` (UnixMilli) — a ~10^6x inflation (a 10-minute interval × 2 sweeps yields a
~38-YEAR grace). Net effect: `valid_until` is set so far in the future that **decay never
expires anything** — P4's primary forgetting mechanism was dead in production. The existing
test only asserted `valid_until != 0`, which the inflated value satisfies, so it shipped.

**Decision.** Compute grace as `int64(DecayGraceSweeps) * DecayInterval.Milliseconds()`
(matching the pattern in `reflect.go`/`threading.go`). The regression test now bounds the
grace to ≈ DecayGraceSweeps×DecayInterval in ms (well under an hour for the test profile), so
a nanosecond/ms unit error fails the test. No data migration needed (existing inflated
`valid_until` rows simply expire on schedule once recomputed on the next below-floor sweep).

## D-111 — Lifecycle consolidation: deterministic survivor, numeral-aware merge, full scope

Phase 29d (audit findings #1, #2). The lifecycle dedupe sweep was miswired three ways:
(a) it kept `target` (the arbitrary neighbor) as the survivor — no date/trust logic, so a numeric
CORRECTION could be merged away keeping the stale value; (b) it had no numeral guard (the D-104
fix lived only in reconcile); (c) it ran at `{tenant}` scope, so `FindNeighbors` matched across
DIFFERENT USERS and the merged survivor was written with NULL project/user/session — a P3 leak +
P1 orphaning.

**Decision.**
1. **Shared survivor rule (D-067):** `reconcile.SelectSurvivor(a,b) (winner,loser)` — later
   `ValidFrom` (assertion date) → higher trust tier → higher importance → later `CreatedAt` →
   larger ULID. Used by the sweep (and available to reconcile) so the rule can't drift.
2. **Numeral-aware merge:** the sweep keeps the SURVIVOR's content/date; when the pair is
   numeral-divergent (a correction, `reconcile.NumeralsDiverge`) it keeps ONLY the survivor's
   surface (entities/keywords/queries) so the stale value's wording can't pollute/resurface;
   for a true duplicate it unions the surface. Counters unioned; both originals tombstoned with
   prior-state (reversible, D-070).
3. **Full-scope sweep:** dedup runs per `(tenant,project,user)` via a new scoped
   `Memories().DistinctScopes` enumerator (both drivers + conformance), mirroring how
   episodes/threading iterate — never cross-user, survivor inherits correct scope columns (P3).

## D-115 — Retrieval rerank precedes the limit trim; cache key includes Kinds + IncludeLanes

Phase 29d (audit #9, #8). (a) The cross-encoder rerank ran AFTER trimming the scored pool to the
requested `limit`, so it could only reorder the already-cut top-`limit` — never PROMOTE a more
relevant memory that fell just below the cutoff. Rerank now runs over the full scored pool
(≤ ScoringK) and the trim-to-`limit` happens after, so the cross-encoder can pull a better
candidate into the result set. (b) The result-cache key omitted `Kinds` and `IncludeLanes`, both
of which change the result set / item payload, so a kind-filtered or lanes-on request could
collide with a plain one within the 60s TTL and return the wrong items. Both are now in the key
(Kinds sorted for order-independence).

## D-116 — Rollup digests every memory it supersedes (no silent content loss)

Phase 29d (audit #6). The session rollup set `Targets: promotable` (supersedes ALL of a session's
promotable memories) but `buildDigestContent` capped the digest at the first 10, emitting
"[+N more]" — so memories 11+ were RETIRED with their content omitted from the digest, silently
lost (e.g. the newest correction in a long session). The digest must represent everything it
supersedes. Decision: `buildDigestContent` includes all promotable memories' content (the count
cap removed). A session digest is bounded in practice — rollup only fires on idle, aged sessions
of working memory. Regression test asserts all 12 contents appear and no "[+N more]" elision.

## D-117 — Review approve/reject are reversible (un-quarantine path)

Phase 29d (audit #10). `trust.Resolve` flips a `pending_review` memory to `active` (approve) or
`quarantined` (reject) and captures the prior state in `memory.review_approved` /
`memory.review_rejected` events — but those types were NOT in `reconcile.isRestorable`, so
`Rollback` refused them: the advertised reversibility (and the only un-quarantine path) was dead
(D-084). Both are plain in-place status flips whose prior state is captured the same way as
`memory.updated`, so they invert via the same `commitSimpleRollback` path. Added both to
`isRestorable` and the rollback switch. Regression test: reject → Rollback restores `pending_review`.

## D-118 — Lifecycle sweeps invalidate the retrieval cache

Phase 29d (audit #15). The reconcile stage invalidates the retrieval result-cache after a
content-changing commit (D-053), but the lifecycle Manager held no invalidator — so after a
decay-expire, dedupe-merge, or rollup, the cache kept serving the now-expired/merged memory for
up to its 60s TTL (P4 staleness). Added `Manager.SetScopeInvalidator` (the same
`reconcile.ScopeInvalidator` seam) and a nil-safe `invalidateScope` call after each
status-mutating sweep; boot wires the retriever cache into the Manager.

## D-114 — Superseded items are self-contained for non-prompt (MCP) clients

Phase 29d (Idea 1). Dual-visibility (D-105) flagged a superseded memory and linked its successor
by ID, and the eval reader got a prompt section — but a real MCP client controls its own prompt
and only sees the retrieve item, where `superseded_by` was a bare ID requiring a second lookup.
Decision: a stale item now carries the successor's VALUE + assertion DATE inline
(`superseded_by_content`, `superseded_by_date`) across all read surfaces (HTTP/MCP/SDK), so any
client is self-contained: "this was superseded by «current value» on «date»". Populated in
`attachStaleCompanions` from the retrieved successor; the eval reader renders it in the [OUTDATED]
tag. (Configurable history depth beyond the immediate successor is a noted follow-up.)

## D-119 — Dedupe sweep isolates partitions with exact-leaf scope (29d B1)

Phase 29d adversarial review (two independent passes, both BLOCKING). D-111 iterated
`DistinctScopes` per (tenant,project,user) but seeded each pass from `ListActiveForDecay`, which
filters `tenant_id` ONLY — so the candidate batch was the whole tenant and a per-user pass could
load another user's memory as the seed, find a same-content neighbour, and merge across users
(P3 + P1). A second mechanism: `buildScopeWhere` treats an empty leaf as "omit the predicate"
(prefix/wildcard), so a NULL-user/NULL-project bucket re-wildcarded across users. Decision: the
dedupe sweep uses EXACT-leaf scope semantics — an empty project/user/session matches `IS NULL`,
never wildcards. New `Memories().ListActiveInScope` (exact candidate list) + `buildExactScopeWhere`
in both drivers + `NeighborQuery.ExactScope` (FindNeighbors honours exact matching when set, default
false preserves the topic-extraction caller's wildcard behaviour). `ListActiveForDecay` and the
decay/rollup sweeps are unchanged (they want tenant-wide). The eval ingests at tenant-only scope, so
its memories live in the NULL-leaf bucket — exact-leaf is what makes that bucket dedupe in isolation
rather than be skipped. Guarded by a conformance test (exact isolation incl. NULL bucket + a
DistinctScopes→ListActiveInScope round-trip) and a sweep-level cross-user test (mutation-verified to
fail on the tenant-wide seed).

## D-120 — Decay expire is reversible; rollup is a reversible many-to-one merge (29d S1)

Phase 29d adversarial review. D-110 activated the decay-expire path (previously dead via a ~10^6×
grace inflation) without its reverse half: `memory.expired` was absent from `isRestorable` and its
payload used a nested `{memory,junctions,decay_factor}` shape incompatible with the flat
`parsePriorState`, so `Rollback` returned `ErrNoPriorState` (P4 violation). Separately, the rollup
sweep committed `ActionMerge` (N sources → 1 digest) but emitted per-source `memory.superseded`,
which routes to `rollbackSuperseded` — restoring only the one subject and stranding the N-1 siblings
on a tombstoned digest. Decisions: (1) decay emits a flat `MarshalPriorState` snapshot of the restore
TARGET (status=active, valid_until cleared) and `memory.expired` is restorable via the in-place path,
so un-expiry restores a fresh active memory; (2) rollup emits `memory.merged` per source so
`rollbackMerged` restores ALL siblings atomically via `ListSupersededBy`. Both guarded by
round-trip tests (rollup mutation-verified to fail on the superseded event type).

## D-121 — Cache invalidation matches the tenant-keyed retrieve cache; cache key includes effective limit (29d S3/S5/N3)

Phase 29d adversarial review (two passes contradicted; resolved empirically). The retrieval result
cache keys its generation counter by the REQUEST scope, and every retrieve surface
(`api/retrieve_handler.go`, `sdk/stowage/embedded.go`) builds the request scope TENANT-ONLY. So a
sweep must `InvalidateScope` at tenant granularity: the D-118 dedupe path invalidated at full
`{tenant,project,user}` scope, bumping `gens["t/p/u"]`, which the tenant-keyed Get never checks →
stale serve for the 60s TTL. Decisions: (1) dedupe invalidates at `{Tenant: scope.Tenant}`; decay/
rollup were already tenant-only (correct) — the rollup personal-zone-only early-return path and the
confirm/`promoteParked` path gained the missing invalidation. (2) D-115's cache key omitted the
effective `limit`, but the cached value is the post-trim `items[:limit]` slice returned verbatim — so
two requests differing only by `limit` collided within the TTL; the effective limit is now part of the
cache key. Invalidation-scope granularity must equal retrieve cache-key granularity. Also tightened:
dedupe audit events now name the real survivor/loser (the survivor became dynamic in D-111), the
`lifecycle.dedupe` event subject is the merged row, and the payload carries survivor_id/loser_id/merged_id.

## D-122 — The eval harness runs a production-faithful consolidation pass before scoring

Phase 29d exit gate. The eval harness parks the mutating lifecycle sweeps (decay/dedupe/rollup)
at 24h intervals so CI stays deterministic — but that means the public-benchmark baseline scored
the RAW post-ingest store, never running the near-dup merge + supersede consolidation (the 29d
machinery) that production runs on a timer. Decision: `RunDatasetOpts.Consolidate` runs ONE
synchronous, deterministic `RunConsolidation` pass (lifecycle `RunForce`: decay/dedupe/rollup/
reenqueue/confirm — reflect/episodes stay off in the harness) after the final settle and before
scoring, then re-settles so re-enqueued work drains and the embedder reindexes merged rows.
`TestFullMode` enables it by default (opt out with `STOWAGE_EVAL_NO_CONSOLIDATE` to measure the
delta); it is auto-skipped on `SkipIngest` (K-sweep re-scores already consolidated). The harness
also now wires `lcMgr.SetScopeInvalidator(retriever.Cache())` (D-118 parity with boot) so a merge
during the pass doesn't leave a stale row served from the 60s cache. The background sweeps stay
parked — this is the explicit equivalent, giving a more realistic baseline without timer nondeterminism.

## D-123 — Reader/judge upgrades for the LongMemEval re-baseline (AMB parity)

Phase 29d exit gate, informed by the vectorize-io/agent-memory-benchmark reader/judge conventions
and the 100q miss taxonomy (temporal-reasoning 0.30, single-session-preference 0.50). Four changes,
all in the eval path except the cap:

1. **Question Date injection.** The reader never received the reference "now" the question is asked
   at, so relative-time questions ("how many days/months since X") were unanswerable — the largest
   contributor to the temporal-reasoning collapse. Added `Question.Date` (dataset), parsed from
   LongMemEval `question_date` in the normalizer (YYYY-MM-DD), threaded to `BuildReaderPrompt` and
   rendered as a `Question Date:` line when present (empty = omitted, back-compat).
2. **Per-question-shape reader rules** in `BuildReaderPrompt`'s system prompt: counting→enumerate-
   then-count, location→include place name, recommendation→describe the KIND (don't invent recs —
   the single-session-preference answer shape), comparative/date-diff→scoped abstention when a side
   is missing, entity-mismatch→say so. Adapted from AMB's LongMemEval reader.
3. **Per-category judge leniency** in `BuildJudgePrompt` (now takes the category): temporal off-by-one,
   knowledge-update updated-value-wins, preference recall-is-enough — mirroring the LongMemEval-standard
   judges AMB adopts, so we grade on the same bar. 3-way verdict (correct/partial/incorrect) retained;
   empty category = generic rubric (back-compat).
4. **maxLimit 50→100** (`internal/retrieval/retrieval.go`) so K-sweeps can probe K up to 100; memories
   are ~36 tokens each so the ceiling is cheap. Production resource-guard const; no new knob.

Not changed: the reader context is already lean plain-text (`[N] content | When: date`), NOT JSON —
the markdown-vs-JSON token saving is already captured (our `[N]` numbering is leaner than AMB's
`## Memory N`). Guards: TestReaderPrompt_QuestionDateAndShapeRules, TestJudgePrompt_PerCategoryLeniency,
TestNormalize_QuestionDate. All testable on the persisted store via a judged K=50 re-score with no
re-learning.

## D-124 — Scope-authoritative record write (Append fills per-record scope)

Phase 30. `Records().Append` (both drivers) bound `project_id/user_id/session_id` from the batch
`scope` arg only — never from each `store.Record` — so a `/v1/records` ingest with a tenant-only
scope dropped every per-record project/user/session to NULL (confirmed: all 2056 eval records NULL).
This broke session-keyed features (episodes never fired — `DistinctSessions` filters `session_id IS
NOT NULL`) and meant the write path couldn't carry per-user data. Decision: bind each dimension as
`scopeOrRecord(scope.X, rec.X)` — the **scope WINS when set** (a record can never override a declared
non-empty scope dimension); the per-record value only fills a dimension the scope left empty.
`scope.Tenant` stays authoritative + the fail-closed guard stays. Guarded by conformance
`RecordAppendScopeFill` (fill + scope-wins, both drivers).

**Scope of the P3 guarantee (precise).** This makes the scope dimensions that ARE set un-escapable,
but it is NOT a claim that the store re-verifies per-record identity against the API key. Live ingest
passes a tenant-only scope (the auth key carries only TenantID — D-030/D-125), so on that path the
record's project/user/session are trusted as supplied — a tenant key CAN attribute a record to any
user_id it claims. That is the deliberate app-supplies-identity model (the calling app, Harbor, owns
end-user attribution; Stowage hard-isolates *reads* to whatever identity is supplied). The hard P3
boundary enforced in the store is the TENANT; project/user are isolation dimensions the trusted caller
populates, not independently-authenticated principals. The same applies to the derived-memory commit
(B1, below): `scopeOrRecord` there fills from the candidate's memory fields only when the commit scope
left a dimension empty.

## D-125 — Read scope is caller-supplied per request (tenant = auth boundary)

Phase 30. Every retrieve surface built a TENANT-ONLY scope, so the lexical+vector lanes returned all
users' memories within a tenant — violating the RFC's "hard isolation at tenant AND user" (line 364)
for the multi-user-per-tenant model. Decision: the **tenant is the auth/trust boundary** (the API key
carries TenantID); **project/user are caller-supplied per-request read sub-scopes** (HTTP/MCP request
fields `project_id`/`user_id`; SDK `RetrieveRequest.ProjectID/UserID`), exactly as ingest accepts
per-record identity — the calling app (Harbor) supplies the end-user, Stowage hard-isolates the query
to it via the store's `buildScopeWhere`. Empty project/user = tenant-wide (back-compat). Grants
(Phase 16 `EffectiveScopes`) still widen reads from the caller's real scope. `session_id` stays the
Phase-10 cooldown/affinity signal, NOT a read-isolation boundary. No per-user keys in v1 (heavier
key-issuance model, deferred). Guarded by `TestRetrieve_UserScopeIsolation` + SDK/HTTP/MCP parity.

**Dual-review remediation (the read filter is only as good as the write + the cache + every surface).**
The first pass scoped *only* `/v1/retrieve`; two independent adversarial reviews found that inert and
the phase was completed across four fronts:
- **B1 — derived memories were written tenant-only.** The pipeline built the flush/commit scope as
  `{Tenant: rec.TenantID}` (`pipeline.go` processItem + the age-flush in `tickScan`), discarding
  `rec.Project/User`, so every memory persisted with user_id=NULL and the D-125 read filter matched
  nothing (tenant-wide still leaked). Fix: the flush scope carries `rec.Project/User` (Session is
  deliberately NOT propagated — a memory is a cross-session abstraction and reconcile dedupe keys on
  tenant/project/user); the memory commit binds `scopeOrRecord` (mirrors D-124). Guarded by
  `TestFlushScopeFromRecord`.
- **B2 — the result cache key was lossy.** `identity.Scope.String()` nested User inside `if Project!=""`
  so `{T,user:alice}` and `{T,user:bob}` both keyed to "T" → a cache HIT served one user another's
  items once B1 landed. Fix: a non-lossy 4-dimension `scopeCacheKey` + an ancestor-summed `scopeGen`
  (a per-user cached read is still busted by the tenant-wide `InvalidateScope` the sweeps emit, and a
  per-user invalidate busts only that user). `hotset` uses the same key. Guarded by
  `TestResultCache_UserKeyNonLossy` + `TestResultCache_AncestorInvalidation`.
- **B3 — every OTHER single-user read+mutate surface was still tenant-only.** Extended the
  per-request project/user sub-scope to playbook, episodes, causal, drilldown, citations, review
  (list + the approve/reject MUTATE), memories get/rollback/patch, traces, feedback, verify, and
  branches — across HTTP (query params for GET via `scopeFromRequest`, body fields for POST/PATCH),
  MCP (input fields + a per-handler merge), and the SDK (request fields + construction-time
  `WithProject`/`WithUser` + a `callScope` per-call override). Guarded by
  `TestScopeParity_ReviewList_AllSurfaces` (embedded/HTTP/MCP byte-identical + alice-only).
- **B4 — `Scope.Validate()` required a contiguous chain** (user implied project), contradicting
  `buildScopeWhere` (each dimension independent) and the `{Tenant,User}`-no-project shape. Relaxed
  to require only Tenant; project/user/session are independent optional dimensions.

**Second-round dual review (the isolation machinery is correct; the survivors were producers/surfaces that fed it the wrong scope — the same illusory pattern).** Two more independent adversarial reviews found and fixed:
- **Embedder backfill stripped project/user (BLOCK).** `reconcile/embedder.go` backfillPass enqueued `{Tenant: m.TenantID}` only — and the backfill sweep is the *guaranteed-recovery* embed path (the primary enqueue is best-effort: dropped on full queue, dead-lettered on gateway outage). A tenant-only vector escapes its scope AND is excluded from the owning user's vector-lane read → a user-scoped memory becomes semantically unfindable. Fix: enqueue `{Tenant, Project: m.ProjectID, User: m.UserID}` (session stays empty, matching the memory row). Guarded by `TestEmbedder_BackfillSweep_PreservesUserScope`.
- **HTTP client dropped `WithProject`/`WithUser` (BLOCK).** `NewHTTP` discarded the construction-scope options → an HTTP SDK client built `NewHTTP(url, key, WithUser("alice"))` silently read tenant-wide. Fix: `httpClient` stores project/user; each method resolves an effective scope (`effScope`: per-call value, else construction default — parity with embedded `callScope`) and sends it (query params for GET, body fields for POST/PATCH). Guarded by `TestScopeParity_HTTPConstructionScope`.
- **Review approve/reject MUTATE was untested cross-surface (BLOCK).** The parity test only covered the read (`list`). Added `TestScopeParity_ReviewResolve_CrossUserDenied`: an alice-scoped approve of bob's pending memory must FAIL on embedded/HTTP/MCP and leave bob's memory `pending_review` (the store narrows the resolve's WHERE to alice → bob's row is not found).
- **hnsw filter failed OPEN on missing sidecar meta (hardening).** `vindex/hnsw` `filterCandidates` emitted a candidate with no provenance meta even under a sub-scoped query. No reachable instance found (every `graph.Add` pairs a meta set), but flipped to fail CLOSED: drop a meta-less node when any of project/user/session is set.
- **Rollup digest was written tenant-only (latent, hardened).** `lifecycle/rollup.go` promoted a session digest under `{Tenant}` with no user/project. Currently unreachable (the pipeline produces no session-scoped memories — true before B1 too, so NOT a B1 regression), but a session belongs to one user, so the digest now inherits the owner's project/user (`ownerScope` from the grouped memories) — it can't leak if a session-scoped memory ever exists.

**Third-round convergence sweep (exhaustive enumeration of every `identity.Scope{}` producer) — one last stripper, then PASS.** A reviewer enumerated all ~86 production scope-construction sites; 85 were legitimately scoped, 1 was a real stripper:
- **Reflection sweep wrote the distilled memory tenant-only (HIGH).** `lifecycle/reflect.go` assembled per-user trajectories (`reflect.AssembleTrajectories` groups records by project/user/session/branch, so each `Trajectory` carries its owner) but emitted the `CandidateBatch` with the tenant-only sweep scope. The `pipeline.Candidate` has no owner fields, so the distilled `strategy`/`failure_mode` memory — and its vector via `enqueueEmbed` — persisted `project_id/user_id=NULL`: unfindable by its own user, visible tenant-wide. Same stripper class as the pipeline/backfill paths. Fix: build `trajScope = {Tenant, traj.ProjectID, traj.UserID}` and use it for the gateway-attribution ctx, the `reflect.Reflect` call, and the emitted batch (the tenant-only `scope` stays correct only for the `ListByOutcome` tenant-wide scan). Guarded by `TestReflectSweep_BatchCarriesTrajectoryOwner`. With this, the sweep confirmed convergence: no remaining per-user-data write/read feeds a tenant-only scope.

**Documented exceptions / known minor limitations:**
- **Bare-param SDK methods carry construction-scope only.** `GetMemory(ctx, id)`, `MergeBranch`/`DiscardBranch(ctx, branchID)` have no per-call request struct, so they scope by the client's construction `WithProject`/`WithUser` (honored on both embedded and HTTP now) but cannot be overridden per call. Changing the `Client` interface signatures is a broad breaking change deferred past v1; the construction-scope path is sufficient for the single-user-client model.
- **Cache freshness under project-asymmetric reads (minor, TTL-bounded, NOT a leak).** `scopeGen` keys the user-ancestor as `{Tenant, Project, User}`; a read scope `{Tenant, User}` with no project (key `{T,"",alice}`) is not busted by a reconcile that invalidates a project-bearing scope `{T,proj,alice}`. The key still includes the user, so no cross-user leak — only a stale (own) result for up to the 60s TTL, and only when read/write project dimensions are asymmetric. Accepted; tightening it would require enumerating per-user projects.

## D-127 — Supersede direction is date-ordered, not arrival-ordered (deterministic guard)

Post-Phase-30 LongMemEval root-cause (handoff sub-case a). The reconciler let an
**OLDER-dated fact supersede a NEWER one**: "The user has completed 5 projects…"
(valid_from 2023-10-09) ended up `superseded`, superseded_by the older "4 projects…"
(valid_from 2023-08-16), leaving the stale value current (qid `06db6396`,
knowledge-update). Same class for an in-place UPDATE, which overwrites the target's
content and would bury the newer value identically.

**The model was not wrong about the pair.** "4 projects" and "5 projects" are the same
fact at two times — a genuine supersede pair, correctly identified. The only defect is
**direction/ordering**: which member wins. D-106 made within-flush direction deterministic
(process candidates oldest-asserted first so the newer supersedes the older), but it only
orders WITHIN one flush. Across sessions the commit order is ingestion/processing order,
not assertion order — so the newer fact can land in the store first, and a later-arriving
OLDER candidate then "supersedes" it. Arrival order, not the date, was deciding the winner.

**Decision: assertion date is the deterministic authority for supersede/update direction.**
A new guard in `reconcile.commit` (`candidateOlderThanTarget`) compares the candidate's
`OccurredAt` (→ `Memory.ValidFrom`, the conversation date, D-024/D-109) against the target's
`ValidFrom`. When the candidate is strictly older (both dates known; equal/unknown dates
fall through to the existing path), the supersede is **honored but re-ordered**: the newer
target stays active and the older candidate is committed as a `superseded` memory whose
`superseded_by_id` points at the target (`commitInvertedSupersede`). The candidate is never
discarded (P1) — the supersede relationship is preserved in the correct direction, and
dual-visibility (D-105/D-114) surfaces "superseded by «the current value»". The guard runs
REGARDLESS of the LLM verdict and the trust gate, and applies to both `supersede` and
`update` (an older update would bury the newer content the same way).

**Reversibility (D-017).** The re-ordered case is a NON-destructive `ActionAdd` of a
born-superseded row — no existing memory is mutated, so rollback simply tombstones the added
row. We deliberately do NOT emit `memory.superseded` here: that event is restorable and,
since the older candidate has no prior ACTIVE state, a rollback would flip the direction and
re-bury the newer fact. A `reconcile.warned` event records the date-ordering for audit. The
normal (correct-direction) supersede path and its `memory.superseded` rollback are unchanged.

**Scope / known limitations (follow-ups, NOT fixed here):**
- **Distinct-fact collapse (handoff sub-case b)** — a goal ("$200 raised goal") vs an outcome
  ("$250 raised") wrongly judged "same fact" and superseded. This is an over-supersede /
  same-fact mis-judgment by the reconcile LLM, not a direction error; the date guard does not
  address it (the two should coexist). Tracked separately.
- **Both-active coexistence** — if the older and newer facts arrive in separate flushes and the
  model judges each an `add` (never proposing a supersede), both stay active and the stale one
  can still be retrieved. The date guard only adjudicates direction once a supersede/update
  pair is proposed; detecting an unproposed same-fact pair is the model's job.

Tested by `TestCandidateOlderThanTarget` (predicate) and `TestStageDateDirectionGuard`
(end-to-end both directions via the realistic near-dup→numeral-divergence→LLM-supersede path,
plus the normal-direction rollback round-trip). Pre-existing bug (Phase 30 did not touch
reconcile); fixed ahead of the gpt-5.4-nano 100q re-baseline so it cannot corrupt that DB.

## D-128 — Learner reasoning effort is an eval knob (STOWAGE_EVAL_MODEL_EFFORT)

Post-Phase-30 baseline work. To run a reasoning-only learner (openai/gpt-5.4-nano) for the
LongMemEval re-baseline we need to pass a provider reasoning-effort on the extraction and
reconcile completion calls. gpt-5.4-nano REJECTS disabled reasoning (HTTP 400 "Reasoning is
mandatory for this endpoint and cannot be disabled") and treats `effort=low` as the floor;
at `low` it stays schema-compliant and all-passing while cutting extraction latency markedly
(2–17 s vs 14–33 s at default). `gateway.CompleteRequest.ReasoningEffort` already existed
(D-100) and the eval reader/judge already set it (STOWAGE_EVAL_READER_EFFORT), but
`pipeline.ExtractStage` and `reconcile.ReconcileStage` never set it on their decision calls.

**Decision: add an eval-only env knob `STOWAGE_EVAL_MODEL_EFFORT`, parallel to the existing
`STOWAGE_EVAL_READER_EFFORT`.** Both stages gain an optional `reasoningEffort` field with a
`SetReasoningEffort` setter (matching the existing optional-setter pattern — SetEmbedder,
SetVIndex, SetRecordStore); the field defaults to "" so the production path and the default
gemini learner are unaffected (no reasoning param sent). The eval harness
(`eval/harness/server.go`) reads the env var once and applies it to BOTH the extract and
reconcile stages (they call the same learner model, so they share the effort). The eval
scripts default it to "" and document it.

**Why an eval knob, not a D-034 production config key.** The need is eval-driven (choosing a
reasoning-only LEARNER model for the benchmark), exactly like the reader/judge effort, which
is also an eval-only env var with no production-config-knob treatment. Production extraction
uses a non-reasoning model where the param is irrelevant, so adding it to `GatewayConfig`
(allKeys/Explain/profiles/golden/example/smoke) would be config surface with no production
default to tune — YAGNI. A production reasoning-effort config key is deferred until a real
production need for a reasoning extraction model exists; the gateway field and the stage
setters are already in place to make that a small follow-up. Verified by wiring tests
(`TestExtract_ReasoningEffortWiring`, `TestStageReasoningEffortWiring`): effort flows to the
request; unset sends no param.

## D-129 — Reconcile decision context budgets turns PER NEIGHBOR, not one shared pool

Follow-up to D-127/D-108. The reconcile decision (add/update/merge/supersede/park) is fed raw
conversation turns so it can tell a correction of the same fact from a distinct fact that
merely shares words/numbers (D-108, prompt rule 8). That context used a single shared budget
of 12 turns, spent candidate-first then neighbor-by-neighbor until exhausted. With the
augmented neighbor set (up to ~8 structural + ~8 semantic), only the first 2–4 neighbors got
ANY source turns; the rest appeared as their one-line memory content only. So rule 8 — the
distinct-fact discriminator — could not fire for most neighbors, while prompt rules 2–3 push
hard toward "supersede" on any same-subject value difference. This is a direct cause of the
over-supersede / distinct-fact-collapse class (handoff sub-case b: a "$200 goal" wrongly
superseded by a "$250 raised" outcome).

**Decision: allocate the turn budget per source.** The candidate gets up to
`maxCandidateContextTurns = 4` of its own turns; EACH neighbor gets up to
`maxNeighborContextTurns = 3`, under a global ceiling `maxContextRecords = 40` (cost backstop,
raised from 12). Every neighbor the decision may supersede now arrives with its own
conversation frame, so rule 8 can adjudicate each one. Tested by
`TestBuildReconcileContext_PerNeighborBudget` (candidate capped at 4; all three crowded
neighbors get their guaranteed 3 — none starved). Pairs with D-130 (the learner writes
self-contained content so the one-line summary itself is meaningful even without the turns).

## D-130 — Extracted memory content must be self-contained (names subject + purpose)

Follow-up to D-129. The reader AND the reconciler see a memory primarily as its one-line
`content`. An over-compressed extraction like "$200 raised goal" is uninterpretable on its own
— $200 goal for what? a target or an actual? — which both starves the reader and makes the
reconciler unable to tell a goal from an outcome (so it over-supersedes). The disambiguating
frame lived only in the separate `context` field (rule 5), leaving `content` free to be a bare
fragment.

**Decision: strengthen extraction prompt rule 2** — content must be a complete statement that
NAMES its subject and purpose, fully interpretable without the conversation or the `context`
field, never a bare value/fragment ("The user set a $200 fundraising goal for a charity cycling
event", never "$200 raised goal"), with goals/targets/results each phrased as distinct named
statements. `PromptTemplateVersion` bumped 5 → 6; golden files regenerated. This is the
upstream half of the sub-case-b fix (D-129 is the downstream half): better content makes both
the crowded-neighbor summaries and the reader answers meaningful, and reduces same-fact
mis-collapse at the reconcile decision.

## D-126 — Performance & resource hardening track (harness-first; pprof off-by-default; advisory-then-promote)

2026-06-25. Owner-initiated. Across the launch track (01–30) and the productionization
program (h1–h7) Stowage never had a profiling phase: there is **no** `net/http/pprof`,
`runtime/trace`, or `MemStats`/`NumGoroutine` sampling anywhere in `internal/`, `cmd/`,
or `sdk/`. The only runtime measurement we ship is the latency SLO (`make slo`,
D-031/D-095) — a reference-hardware p99 stopwatch on the retrieval read path. That says
nothing about CPU at idle, heap growth, goroutine leaks, or block/mutex contention,
even though P2 (fire-and-forget, **supervised goroutine stages that drain on
shutdown**) and the whole pipeline/sweeps/vindex architecture are concurrency-heavy and
have only ever been *asserted* safe, never profiled. Not a launch product yet; not time
constrained — the right moment to build the fundamentals.

**Decision.** Open a **Performance & resource hardening track** (`phase-pN-*`),
orthogonal to the launch roadmap in the same way the D-067 productionization track is.
Four ratified choices (owner-confirmed 2026-06-25):

1. **Harness-first structure.** The lead phase (**P1**, `phase-p1-profiling-harness.md`)
   builds the instrumentation + load/profile rig + leak detection and **commits
   baselines** (`eval/PROFILE.md`); it does **not** fix what it finds. Each leak or
   inefficiency the harness surfaces lands as a scoped `phase-pN` follow-up gated by the
   baseline — the eval continuous model (D-035), not one open-ended "fix everything"
   mega-phase whose acceptance criteria can't be called done.
2. **pprof off-by-default, auth-gated, on a separate listener.** `net/http/pprof` is an
   operator tool that leaks internal state and allows CPU-burning captures, so it is
   **never** mounted on the public API mux. New knob `server.pprof_listen` (default
   empty = no listener, zero-config preserved per D-034); when set it starts a second
   `http.Server` with explicit timeouts, loopback-default, gated by the same
   constant-time admin-key check as the admin API (CLAUDE.md §7).
3. **Advisory-then-promote gating.** `go.uber.org/goleak` (already transitive in
   `go.sum`) wires into every goroutine-launching package's `TestMain`, and the rig's
   goroutine-stability + idle ceilings are **advisory** first — they record measured
   numbers and only fail on a configured regression once a clean baseline is committed.
   Promotion to a hard CI gate is per-package/per-ceiling and deliberate, so the harness
   PR is never blocked on pre-existing leaks (couples infra to cleanup otherwise).
4. **Scope: in-process Go concurrency AND backends under load.** The rig profiles the Go
   concurrency (pipeline, sweeps, vindex sidecar, boot supervisor drain, gateway
   batching) **and** backend-under-load behaviour — pgx pool saturation and the sqlite
   dedicated-writer goroutine contention (RFC §8.2, the CC-memory lesson behind D-022) —
   the full picture for a memory server.

**Relationship to the SLO (explicit).** Resource profiling is a **sibling** of the
latency SLO under `internal/bench/`, not folded into it: different question (CPU/heap/
goroutine resource behaviour vs p99 latency), different cost (the noisy long-duration +
Postgres cuts stay on-demand like `make slo`/`make bench`, out of the per-PR matrix per
D-095; the sqlite + mock-gateway cut is the fast always-on CI cut). The
goroutine-stability gate (`post-drain NumGoroutine ≤ post-boot + ε`) is the P2
drain-on-shutdown contract made measurable; the idle gate (zero-traffic CPU/alloc
ceiling) is the direct rebuttal to the brief-01 polling-worker-pool tax P2 was designed
to remove. No prior decision is reversed; §11 observability is extended, not contradicted.

**Implementation notes (P1 as built, 2026-06-29).** P1 shipped across six commits on
`feat/phase-p1-implementation`. Deviations from the plan text, all recorded in
`docs/plans/phase-p1-profiling-harness.md` (§4.3):

- **pprof is admin-role-gated**, not merely key-gated: heap/goroutine dumps are
  process-global (not tenant-scoped), so the listener reuses
  `(*api.Server).authMiddleware(_, requireAdmin=true)` (401/403/200). The dedicated
  listener sets **no `WriteTimeout`** — a streaming `/debug/pprof/profile?seconds=N`
  capture must not be truncated (the reason it is a separate listener, mirroring D-074).
- **The runtime sampler logs; it does not register custom gauges or emit an event.**
  `collectors.NewGoCollector()` (already in `telemetry.New`) covers the Prometheus path;
  the sampler's value is the pull-independent `runtime.sample` log line. The typed event
  was dropped (no events package; events are tenant-scoped, samples are process-global).
- **goleak is wired via a shared `internal/leakcheck.Run(m, Advisory)` helper**, not raw
  `goleak.VerifyTestMain` (which hard-exits on a leak). Advisory-then-promote is a
  one-line `Advisory`→`Strict` flip per package. The plan's target list over-counted —
  `proactive`/`scoring`/`traces`/`mcpserver` launch no goroutines; **`vindex` did and had
  no coverage** (added with the matrix).
- **The rig profiles two matrices** (owner request, "measure all drivers"): the
  driver/store grid `{hnsw,brute}×{sqlite,postgres}` and the entrypoint shapes
  `{embedded,serve,mcp}`, each capturing a **memory footprint** at every sample point, not
  just goroutine counts. `serve`/`mcp` are profiled by spawning the real binary (serve via
  the pprof endpoint — dogfooding; mcp as a clean-shutdown check).
- **Only `hnsw`+`brute` vindex drivers exist**; the `pgvector`-native ANN driver named in
  §3 is unbuilt — a principal-Postgres parity gap filed as **issue #87**, deferred to
  after this wave (out of P1 scope).
- **First baseline is all-green** (`eval/PROFILE.md`): every driver/store cell and both
  measurable entrypoints PASS goroutine-stability with no leak (post-drain deltas −26..−28;
  serve climb −1; all shapes drain cleanly ≤ ~5 ms). The harness found the system healthy
  on its first run — exactly the "build + baseline, don't fix" outcome the track intends.
  The rig stays behind `-tags=profile` (`make profile`), out of the per-PR matrix.

## D-131 — Adoption & ergonomics track; gateway defaults to the real Bifrost/OpenRouter stack

2026-06-29. Owner-initiated. Opens the **Adoption & ergonomics track** (`phase-aN-*`,
numbered so it does not collide with the launch `01–27`, productionization `h*`, or
performance `p*` slots) and lands its lead phase a1. Three first-five-minutes gaps drove
it; this entry settles the gateway-default half.

**Context.** The shipped `gateway.driver` default was `mock` — so `stowage serve` + one
secret wired a *synthetic* gateway, contradicting RFC §9.4 ("`stowage serve` with exactly
one secret … runs a working server") and `docs/getting-started.md` (which already
documented Bifrost as the default). The code was the drift, not the docs.

**Decision.**
1. **Flip the default gateway to the real driver.** `Defaults()` now ships
   `driver=bifrost`, `provider=openrouter`, the **live-validated** embed/rerank ids
   (`embed_model=perplexity/pplx-embed-v1-0.6b`, `embed_dims=1024`,
   `rerank_model=cohere/rerank-4-fast` — the proven full stack in
   `internal/gateway/bifrost/live_test.go`), and the owner-chosen baseline learner
   `model=openai/gpt-5.4-nano`. `base_url`/`rerank_base_url` ship **empty**: the bifrost
   driver supplies OpenRouter's `…/api` and `…/api/v1` when provider=openrouter
   (`applyProviderBaseDefaults`, P5 — the driver owns wire details), so empty keeps its
   "native endpoint / reuse base_url" meaning for every other provider and a
   non-OpenRouter bifrost config is never silently misrouted to openrouter.ai. One
   OpenRouter key reaches all three lanes (completion + embedding + the auto-wired
   Cohere-shape rerank, D-075), so the five-minute start is genuinely one secret.
   Reaffirms D-034's "one secret"; aligns the code with RFC §9.4. Amends D-049 (provider
   is now defaulted, not unset).
2. **`mock` stays a first-class driver**, no longer the default — `STOWAGE_GATEWAY_DRIVER=mock`
   is the keyless, hermetic/offline escape hatch (used by every serve-booting smoke and the
   eval harness). `FillZeroDefaults` also fills `provider` (so the all-defaults embedded
   bifrost stack validates the D-049 rule) but deliberately NOT `base_url`/`rerank_base_url`
   (empty must keep its native-endpoint meaning; the driver supplies OpenRouter's).
3. **Fail loud on the minimum.** A real driver with an unresolved `STOWAGE_GATEWAY_API_KEY`
   aborts boot (the bifrost driver already failed closed naming the exact env var); boot now
   appends the five-minute-minimum hint and the `mock` escape hatch. Probe failure stays a
   degraded warning (D-036), not a boot error.

**Deviation from the a1 plan (CLAUDE.md §4.3).** Per-concern provider/key/base_url overrides
(embed/rerank) were planned inside a1 to avoid forcing one provider when OpenRouter could not
serve embeddings. OpenRouter **does** serve embeddings (`perplexity/pplx-embed-v1-0.6b`, eval-
proven), so per-concern keys are now *optionality*, not a requirement for the one-key start.
They are deferred to a follow-up (a1b) so a1 ships as a clean default-flip unit; an operator can
still repoint the single provider/key/base_url today. Tracked, not dropped.

**Consequences.** `stowage serve` + `STOWAGE_GATEWAY_API_KEY` is a working real server on
embedded SQLite; the explain golden, the rerank-base-url default test, and every serve smoke
were updated/verified green. The README quickstart prose (MCP opt-in honesty + the real
minimum-var block) lands in a3.
