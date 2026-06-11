# Stowage — Master Phase Plan

> Authoritative for phase ordering, dependencies, and cross-cutting conventions.
> Each phase gets its own `phase-NN-slug.md` (from `_template.md`) **before**
> implementation starts (CLAUDE.md §16). Acceptance criteria in phase plans are
> binding.

## Cross-cutting conventions

- Every phase lands behind a green `make preflight` and its own
  `scripts/smoke/phase-NN.sh`.
- Coverage bands per CLAUDE.md §11. Seams ship with conformance suites.
- A phase that opens a seam ships the `mock`/in-memory driver in the same phase.
- Wave boundaries get a read-only checkpoint audit (CLAUDE.md §17).

## Phase index

### Wave 1 — Foundation

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 01 | Scaffold & CI | repo, Makefile, CI, hooks, drift-audit | §2 | — |
| 02 | Config, identity, telemetry | `internal/config`, `internal/identity`, `internal/telemetry` | §5.3, §11 | 01 |
| 03 | Store seam + drivers | `internal/store` {sqlite, postgres}, migrations, conformance suite | §8 | 02 |
| 04 | Gateway seam + bifrost driver | `internal/gateway` {bifrost, mock}, batching, metering, embedding cache | §7 | 02 |

### Wave 2 — Write path

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 05 | Records + ingest API | `internal/records`, `internal/api` (ingest), fire-and-forget ACK | §4.1, §5.1, §9.1 | 03 |
| 06 | Buffers | `internal/pipeline` (buffer stage), flush triggers | §4.1 | 05 |
| 07 | Topics + extraction | `internal/topics`, extract stage, candidate schema | §5.4, §4.1 | 04, 06 |
| 08 | Reconciliation + commit | `internal/reconcile`, dedup pre-filters, trust gates, commit txn, events | §6 | 07 |

### Wave 3 — Read path

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 09 | Retrieval lanes + fusion | `internal/retrieval` (lexical, vector, anticipated-queries, structured; RRF), `internal/vindex` | §4.2 | 03, 04, 08 |
| 10 | Scoring & ranking | utility counters, decay, trust weights, hub dampening, cooldown | §5.2 | 09 |
| 11 | Drill-down + feedback API | provenance expansion, `/v1/feedback`, counter updates | §4.2, §9.1 | 10 |
| 12 | Rerank (optional lane) | gateway rerank pass, budget packing | §4.2 | 10 |

### Wave 4 — Lifecycle & sharing

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 13 | Sweeps | `internal/lifecycle`: decay, dedupe, rollup, re-enqueue; job markers; singleflight | §6 | 08, 10 |
| 14 | Supersede chains, confirmation & rollback | chain walking, cycle caps, `pending_confirmation` resolution, contradiction boost, rollback API | §6 | 13 |
| 15 | Grants & team sharing | `internal/grants`: groups, read/contribute grants, zone ceilings, redaction hooks | §5.3 | 09, 11 |

### Wave 5 — Surfaces & proof

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 16 | MCP server (Dockyard) | `internal/mcpserver`, 7-tool surface built with Dockyard | §9.2 | 11 |
| 17 | Go SDK + Harbor recipes + embedded mode | `sdk/stowage` (HTTP + in-process), Harbor tool/bus adapters, flow recipes | §9.3, §10, §2 | 11 |
| 18 | Reflection & playbooks | outcome reflection mode, re-reflection sweep, `internal/playbook`, `GET /v1/playbook` | §6a | 08, 11, 13 |
| 19 | Eval harness | `eval/`: gain harness on a Harbor fleet, LoCoMo-style benchmark, online-adaptation scenarios, perf benchmarks | §12 | 17, 18 |
| 20 | Hardening, open-source readiness & v1 | security pass, docs, CHANGELOG, release build matrix, public-repo audit | §13 | all |

## Phase detail blocks

### Phase 01 — Scaffold & CI
Repo skeleton matching CLAUDE.md §3; Makefile targets (all no-op gracefully);
golangci config; CI (build, test -race, lint, mirror, drift-audit); pre-commit
hook; smoke template + phase-01 smoke. **Risks:** none. **Criteria:** `make
preflight` green on a fresh clone; CI green; mirror enforced.

### Phase 02 — Config, identity, telemetry
Typed config with `env.VAR` indirection, fail-loud `Validate()`; identity scope
type (tenant/project/user/session) + ctx helpers; slog setup (JSON/text), secret
redaction; Prometheus registry. **Risks:** over-modeling config before consumers
exist — keep keys phase-gated. **Criteria:** invalid config fails boot with
file:line precision; identity round-trips ctx; no secret appears in logs (test).

### Phase 03 — Store seam + drivers
`Store` interface sized to W2 needs (records, memories, junctions, topics,
events, job markers, dead letters); postgres driver (pgx — the principal
production store, D-021) + sqlite driver (modernc, WAL, dedicated writer
goroutine — the embedded/portable driver); embedded forward-only migrations;
shared conformance suite both drivers pass; scope-parameterized query builders
only. **Risks:** schema churn — seam grows method-by-method with phases, per
CLAUDE.md §9. **Criteria:** conformance suite green on both drivers under
`-race`; concurrent write/read test on sqlite shows no lock-wait storms.

### Phase 04 — Gateway seam + bifrost driver
`Gateway` interface (Embed batched, Complete schema-constrained); bifrost driver
(OpenAI-compatible chat + embeddings; key via `env.`; fail-closed); mock driver;
request batching, retry/backoff, circuit breaker; (model, content-hash) embed
cache; token/cost metering events; model+dims pinning. Resolves **OQ-1** with a
small bake-off. **Risks:** provider drift — golden wire tests. **Criteria:**
golden request/response tests; cache hit path proven; cost events emitted;
boot fails on dims mismatch.

### Phase 05 — Records + ingest API
Verbatim record append (ULID, scope stamp, token estimate, optional outcome
tag — RFC §6a.1); `POST /v1/records` (single/batch) with ACK after durable
write; lexical indexing of records; pipeline enqueue (bounded channel);
retention/DSAR cascade stubs. **Criteria:**
ingest p99 < 15 ms (sqlite, local bench); ACK never waits on the pipeline
(test: pipeline stalled, ingest still ACKs); records immutable (no update API).

### Phase 06 — Buffers
Per (scope, key) accumulation; triggers: count, token estimate, max age,
session end, explicit flush; many-writers-one-buffer proven under `-race`;
crash recovery via re-enqueue sweep contract (full sweep lands in 13).
Resolves **OQ-3** defaults. **Criteria:** trigger matrix table-tested; flush
exactly-once per trigger under concurrent writers.

### Phase 07 — Topics + extraction
Topic CRUD per scope (+ default packs); extract stage: one gateway call per
flush, topics in prompt, JSON-schema-constrained candidate list (kind, content,
entities, keywords, anticipated queries, importance, confidence); no-topic-match
⇒ no candidate; golden prompt tests. (The outcome-aware *reflection* mode is
deliberately deferred to Phase 18 — this phase ships topic extraction only.)
**Criteria:** candidates carry provenance spans; prompt goldens; extraction
failure → dead letter + event, never data loss.

### Phase 08 — Reconciliation + commit
SHA-256 exact + bigram-Jaccard near-dup pre-filters; neighbor retrieval;
constrained tool-call decision (add/update/merge/supersede/discard); trust gates
(`pending_confirmation` parking); transactional commit; embedding upserts;
mutation events with reasons. **Criteria:** pre-filters eliminate LLM calls on
duplicate replay (test); trust gate blocks low-trust supersede of high-trust
memory; every mutation has an event; full write-path integration test
(ingest → memory committed) with mock gateway + real store.

### Phase 09 — Retrieval lanes + fusion
`vindex` seam: pgvector driver + sqlite path (brute-force first; pure-Go HNSW
behind the seam when scale demands — resolves **OQ-2**); concurrent lanes via
errgroup; RRF fusion; structured filters (entity/keyword/kind/zone). **Criteria:**
lane-level timings in metrics; fusion golden tests; scope isolation proven at
the retrieval layer (cross-scope query returns nothing, test on both drivers).

### Phase 10 — Scoring & ranking
Six-counter utility model; decay (activity+wall-clock blend, stability growth,
floors); trust/source multipliers; scope affinity; hub dampening; write-echo
cooldown. **Criteria:** scoring is a pure, table-tested function; documented
score breakdown returned in debug mode.

### Phase 11 — Drill-down + feedback API
`/v1/drilldown` provenance expansion; `/v1/retrieve` response carries provenance
refs + budget packing; `/v1/feedback` updates counters; retrieval profiles.
**Criteria:** drill-down returns exact verbatim spans; feedback mutates counters
and is visible in next retrieval's scores; end-to-end read-path integration test.

### Phase 12 — Rerank (optional lane)
Gateway rerank over top slice; profile-gated; cost-ceiling aware (degrades
gracefully). **Criteria:** rerank improves eval-harness retrieval metric on the
fixture set; disabled by default.

### Phase 13 — Sweeps
Jittered tickers; singleflight (advisory locks on postgres); idempotency
markers; decay sweep, dedupe sweep, rollup, re-enqueue sweep. **Criteria:**
sweeps are idempotent (run twice ⇒ same state); crash mid-sweep recovers;
re-enqueue picks up records whose pipeline derivation was lost.

### Phase 14 — Supersede chains, confirmation & rollback
Chain walk with cycle detection + hop cap; `pending_confirmation` resolution
(confirm on repeated independent extraction or explicit feedback; TTL
auto-resolve — resolves **OQ-4**); contradiction boost; reversible
reconciliation contract (D-017): prior state recoverable from every
update/merge/supersede event, `POST /v1/memories/{id}/rollback`. **Criteria:**
chain property tests (no cycles, cap honored); confirmation matrix
table-tested; rollback round-trip test for each op type (apply → rollback →
state equals before, with both events present).

### Phase 15 — Grants & team sharing
`internal/grants` (D-016): named groups; read/contribute grants over a slice of
an owner scope (filterable by topic/kind); privacy-zone ceilings; redaction
hooks; store-layer enforcement (grants join scopes in the query builders —
still no unscoped API); grant/revoke events; `GET/PUT /v1/scopes/{scope}/grants`.
Resolves **OQ-7** (contribute-mode trust). **Criteria:** zone ceiling proven
(`personal` never crosses a grant, test on both drivers); revocation takes
effect on next query; contribute-mode reconciliation respects the pool owner's
trust gates; cross-tenant grants impossible by construction.

### Phase 16 — MCP server (Dockyard)
Seven tools (`memory_ingest`, `memory_retrieve`, `memory_playbook`,
`memory_drilldown`, `memory_feedback`, `memory_assert`, `memory_topics`) built
with Dockyard (D-020): typed Go contracts, generated schemas, inspector-driven
testing; stdio + HTTP transports. **Criteria:** `dockyard validate`/`test`
gates green; tool schemas golden-tested; smoke drives each tool against a live
`stowage mcp`.

### Phase 17 — Go SDK + Harbor recipes + embedded mode
`sdk/stowage` HTTP client + in-process embedded mode (D-022); identity lift
from Harbor's quadruple; Harbor in-proc tool registration helper; bus event
adapter (`memory.*`, cost semantics per D-019); Harbor flow recipes
(consolidation, post-task-group reflection); worked recipe docs. **Criteria:**
example Harbor agent stores and retrieves through the SDK in `examples/`;
in-process mode passes the same SDK test suite as HTTP mode; embedded example
runs with the sqlite driver only (no network, CGo-free build).

### Phase 18 — Reflection & playbooks
Outcome reflection extraction mode (`strategy`/`failure_mode` candidates from
outcome-tagged records, iteratively refined, reconciled normally); multi-epoch
re-reflection sweep; `internal/playbook`: deterministic sectioned assembly,
counter-ranked, budget-packed, provenance-linked, append-biased output
(resolves **OQ-6** with cache measurements); `GET /v1/playbook`. **Criteria:**
assembly path provably LLM-free (no gateway calls in the playbook package —
lint-style test); playbook stable across runs given unchanged memories (golden);
reflection candidates respect topic gates and trust hierarchy; fleet loop
integration test: outcomes in → playbook evolves → injected playbook contains
the new strategy.

### Phase 19 — Eval harness
`stowage eval`: gain harness with a Harbor fleet as the agent loop (D-019),
retrieval benchmark (LoCoMo-style; target ≥ 0.86 — D-023), online-adaptation
scenarios (ACE-style: sequential tasks with the reflection → playbook loop
active), perf benchmarks (ingest ACK, pipeline throughput, retrieval p99 at
10k/100k/1M memories). **Criteria:** standard scenarios show positive gain;
reproducible `eval/REPORT.md` (the open-source gate artifact); negative gain on
standard scenarios blocks release (CI-runnable with mock-gateway fixtures; full
runs on demand).

### Phase 20 — Hardening, open-source readiness & v1
Security pass (HTTP hardening checklist, key handling, redaction profiles);
docs; CHANGELOG; cross-compile release matrix + checksums; public-repo audit
(D-023: license decision per OQ-5, history scrub check, predecessor-hygiene
sweep, README/docs written for an external audience). **Criteria:** the
§13/§14 checklists pass repo-wide; release artifacts build CGo-free for
darwin/linux × amd64/arm64; drift-audit forbidden-names check green over the
full git history, not just the worktree.
