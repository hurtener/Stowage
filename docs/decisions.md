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
