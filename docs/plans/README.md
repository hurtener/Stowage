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

### Wave 4 — Lifecycle

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 13 | Sweeps | `internal/lifecycle`: decay, dedupe, rollup, re-enqueue; job markers; singleflight | §6 | 08, 10 |
| 14 | Supersede chains & confirmation | chain walking, cycle caps, `pending_confirmation` resolution, contradiction boost | §6 | 13 |

### Wave 5 — Surfaces & proof

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 15 | MCP server | `internal/mcpserver`, 6-tool surface | §9.2 | 11 |
| 16 | Go SDK + Harbor recipes | `sdk/stowage`, in-process mode, Harbor tool/bus adapters | §9.3, §10 | 11 |
| 17 | Eval harness | `eval/`: gain harness, retrieval benchmark, perf benchmarks | §12 | 11, 13 |
| 18 | Hardening & v1 | security pass, docs, CHANGELOG, release build matrix | §13 | all |

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
events, job markers, dead letters); sqlite driver (modernc, WAL, dedicated
writer goroutine) + postgres driver (pgx); embedded forward-only migrations;
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
Verbatim record append (ULID, scope stamp, token estimate); `POST /v1/records`
(single/batch) with ACK after durable write; lexical indexing of records;
pipeline enqueue (bounded channel); retention/DSAR cascade stubs. **Criteria:**
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
⇒ no candidate; golden prompt tests. **Criteria:** candidates carry provenance
spans; prompt goldens; extraction failure → dead letter + event, never data loss.

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

### Phase 14 — Supersede chains & confirmation
Chain walk with cycle detection + hop cap; `pending_confirmation` resolution
(confirm on repeated independent extraction or explicit feedback; TTL
auto-resolve — resolves **OQ-4**); contradiction boost. **Criteria:** chain
property tests (no cycles, cap honored); confirmation matrix table-tested.

### Phase 15 — MCP server
Six tools (`memory_ingest`, `memory_retrieve`, `memory_drilldown`,
`memory_feedback`, `memory_assert`, `memory_topics`) over
`modelcontextprotocol/go-sdk`; stdio + HTTP transports. **Criteria:** tool
schemas golden-tested; smoke drives each tool against a live `stowage mcp`.

### Phase 16 — Go SDK + Harbor recipes
`sdk/stowage` HTTP client + in-process mode; identity lift from Harbor's
quadruple; Harbor in-proc tool registration helper; bus event adapter; worked
recipe docs. **Criteria:** example Harbor agent stores and retrieves through the
SDK in `examples/`; in-process mode passes the same SDK test suite as HTTP mode.

### Phase 17 — Eval harness
`stowage eval`: gain harness (memory on/off delta over scripted multi-session
scenarios), retrieval benchmark (recall@k + answer accuracy over long
conversations), perf benchmarks (ingest ACK, pipeline throughput, retrieval p99
at 10k/100k/1M memories). **Criteria:** standard scenarios show positive gain;
results land in `eval/REPORT.md`; negative gain on standard scenarios blocks
release (CI-runnable with mock-gateway fixtures; full runs on demand).

### Phase 18 — Hardening & v1
Security pass (HTTP hardening checklist, key handling, redaction profiles);
docs; CHANGELOG; cross-compile release matrix + checksums. **Criteria:** the
§13/§14 checklists pass repo-wide; release artifacts build CGo-free for
darwin/linux × amd64/arm64.
