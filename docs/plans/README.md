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
- **The day-one schema rule (RFC §5.0):** signal-bearing columns and tables
  (occurred_at, branch_id, outcome, injections, links, episodes) are written by
  W1–W3 hot paths even though the capabilities that consume them land in
  W6–W8. A later phase discovering it needs an unbackfillable signal that
  wasn't captured is a planning defect — raise it against this file.

## Milestones

- **M1 — Core memory substrate** (W1–W5): every Harbor agent reads/writes
  memory out of the box; hybrid retrieval under SLO; grants; MCP surface.
- **M2 — Narrative & trust** (W6–W7): episodes, causal links, citations,
  verification, audit traces.
- **M3 — Self-improving & proactive** (W8): playbooks, trigger engine.
- **Release gate** (W9): SOTA eval report (D-023) + hardening → open-source.

## Phase index

### Wave 1 — Foundation

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 01 | Scaffold & CI | repo, Makefile, CI, hooks, drift-audit | §2 | — |
| 02 | Config, identity, telemetry, API keys | `internal/config`, `internal/identity`, `internal/telemetry`, `internal/auth` (runtime key store) | §5.3, §9.1, §11 | 01 |
| 03 | Store seam + the day-one schema | `internal/store` {postgres, sqlite}, full §8.1 schema, migrations, conformance suite | §5.0, §8 | 02 |
| 04 | Gateway seam + bifrost driver | `internal/gateway` {bifrost, mock}, batching, metering, embedding cache | §7 | 02 |

### Wave 2 — Write path

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 05 | Records, ingest API & branches | `internal/records`, ingest (outcome/branch/occurred_at), `POST /v1/branches` lifecycle | §4.1, §5.1, §5.5 | 03 |
| 06 | Buffers | `internal/pipeline` (buffer stage), branch-aware keys, flush triggers | §4.1 | 05 |
| 07 | Topics + extraction | `internal/topics`, extract stage, candidate schema, preference-fragments pack | §5.4, §5.2 | 04, 06 |
| 08 | Reconciliation + commit | `internal/reconcile`, dedup pre-filters, trust gates, commit txn, day-one link writes, events | §6, §5.6 | 07 |

### Wave 3 — Read path

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 09 | Retrieval lanes + fusion | `internal/retrieval`, `internal/vindex`; lexical/vector/anticipated/structured lanes; native time-window filters; RRF | §4.2 | 03, 04, 08 |
| 10 | Scoring & ranking | utility counters, decay, trust weights, hub dampening, cooldown, support summary | §5.2, §4.2.5 | 09 |
| 11 | Injections, drill-down & feedback | injections recording, provenance expansion, `/v1/feedback` (incl. like/dislike per response), citation handles in responses | §5.7, §4.2 | 10 |
| 12 | Rerank, hot–warm cache & the SLO | gateway rerank, query/hot-set cache + invalidation, p99 ≤ 150 ms @ 1k sessions | §4.2 | 10, 11 |

### Wave 4 — Lifecycle & sharing

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 13 | Sweeps | `internal/lifecycle`: decay, dedupe/compression, rollup, re-enqueue; job markers; singleflight | §6 | 08, 10 |
| 14 | Supersede chains, confirmation & rollback | chain walking, cycle caps, `pending_confirmation`, contradiction boost, rollback API | §6 | 13 |
| 15 | Grants & team sharing | `internal/grants`: groups, read/contribute grants, zone ceilings, redaction hooks, admin governance | §5.3 | 09, 11 |

### Wave 5 — Surfaces

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 16 | MCP server (Dockyard) | `internal/mcpserver`, 7-tool surface built with Dockyard | §9.2 | 11 |
| 17 | SDKs + zero-config agent wiring | `sdk/stowage` (HTTP + embedded), Harbor assemble option, bus/cost adapters, flow recipes, thin Python client | §9.3, §10, §2 | 11 |

### Wave 6 — Episodic & temporal

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 18 | Episodes & narratives | `internal/episodes`: boundary detection sweep, narrative construction, episode↔memory wiring | §6b | 13 |
| 19 | Episodic retrieval | similar-episode contrast (outcome-aware), cross-episode window aggregation, `GET /v1/episodes` | §6b | 18, 12 |
| 20 | Causal links | inferred `caused_by`/`led_to` detection over narratives, "why" graph traversal API | §5.6, §6b | 18 |

### Wave 7 — Trust: citations & audit

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 21 | Citations v1 + citation feedback | `/v1/citations/resolve`, citation handles end-to-end, `wrong_citation` downweighting | §6c, §5.7 | 11 |
| 22 | Verification & review queue | `POST /v1/verify` entailment safeguard, uncited-claim `pending_review` queue + admin endpoints | §6c | 21 |
| 23 | Reasoning traces + audit export | trace reconstruction per response_id, signed export bundles, third-party audit API, retention class | §6c | 21 |

### Wave 8 — Self-improvement & proactive

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 24 | Reflection & playbooks | outcome reflection mode, re-reflection sweep, `internal/playbook`, `GET /v1/playbook` | §6a | 08, 11, 13 |
| 25 | Proactive trigger engine | `internal/proactive`: triggers, threshold scoring, per-tenant governance/opt-outs, accept/dismiss tuning | §6d | 19, 24 |
| 26 | Temporal pattern mining (stretch) | routine detection over episode timing, suggestions via 25's machinery | §6d | 19, 25 |

### Wave 9 — Proof & release

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 27 | Eval harness | `eval/`: gain harness on a Harbor fleet, LoCoMo-style benchmark, online-adaptation scenarios, SLO benchmarks | §12 | 17, 24 |
| 28 | Hardening, open-source readiness & v1 | security pass, docs, CHANGELOG, release matrix, public-repo audit, license (OQ-5) | §13 | all |

## Phase detail blocks

### Phase 01 — Scaffold & CI
Repo skeleton matching CLAUDE.md §3; Makefile targets (all no-op gracefully);
golangci config; CI (build, test -race, lint, mirror, drift-audit); pre-commit
hook; smoke template + phase-01 smoke. **Criteria:** `make preflight` green on
a fresh clone; CI green; mirror enforced.

### Phase 02 — Config, identity, telemetry, API keys
Typed config with `env.VAR` indirection, fail-loud `Validate()`; identity scope
type + ctx helpers; slog setup (JSON/text), secret redaction; Prometheus
registry; `internal/auth`: API keys live in the store and are managed at
runtime (create/list/rotate/revoke/bulk-revoke — admin endpoints land with the
HTTP surface in Phase 05, the key model and constant-time verification land
here). Onboarding an agent or retiring a compromised key never requires a
restart. **Criteria:** invalid config fails boot with file:line precision;
identity round-trips ctx; key verify is constant-time; no secret in logs (test).

### Phase 03 — Store seam + the day-one schema
`Store` interface + **the full §8.1 schema in the first migration set**:
records (occurred_at, branch_id, outcome, response_id), memories (episode_id,
validity window, all statuses), junctions, provenance, injections, links,
episodes, branches, topics, buffers, grants/groups, feedback, suggestions,
scope_settings, api_keys, events, dead_letters, job_markers. Postgres driver
(pgx — principal, D-021) + sqlite driver (modernc, WAL, dedicated writer
goroutine); forward-only migrations; shared conformance suite; scope-
parameterized query builders only. Indexes designed up front: (scope,
occurred_at), (scope, status), injection (response_id), link (from, type).
**Risks:** over-modeling — mitigated by the §5.0 rule: every column is written
by a W1–W3 hot path; anything else is rejected. **Criteria:** conformance
suite green on both drivers under `-race`; cross-scope queries return nothing
(both drivers); concurrent-writer test shows no lock storms on sqlite;
EXPLAIN-verified index use on the temporal and injection queries (postgres).

### Phase 04 — Gateway seam + bifrost driver
`Gateway` interface (Embed batched, Complete schema-constrained); bifrost
driver; mock driver; batching, retry/backoff, circuit breaker; (model,
content-hash) embed cache; token/cost metering events; model+dims pinning.
Resolves **OQ-1**. **Criteria:** golden wire tests; cache hit path proven;
cost events emitted; boot fails on dims mismatch.

### Phase 05 — Records, ingest API & branches
Verbatim record append (ULID, scope stamp, occurred_at vs created_at, optional
outcome + branch_id + response_id, token estimate); `POST /v1/records`
(single/batch) ACK after durable write; lexical indexing; pipeline enqueue;
`POST /v1/branches` fork/merge/discard lifecycle (merge reconciles branch
working memories into parent; discard expires them; records always remain);
admin key endpoints from Phase 02's model; retention/DSAR cascade stubs.
**Criteria:** ingest p99 < 15 ms (sqlite local bench); ACK never waits on the
pipeline; records immutable; branch discard leaves no active branch memories
but all records; key rotate/revoke effective without restart (smoke).

### Phase 06 — Buffers
Per (scope, branch, key) accumulation; triggers: count, token estimate, max
age, session end, explicit flush; many-writers-one-buffer under `-race`; crash
recovery via re-enqueue contract. Resolves **OQ-3**. **Criteria:** trigger
matrix table-tested; flush exactly-once under concurrent writers; branch
isolation (branch buffer never flushes into parent extraction).

### Phase 07 — Topics + extraction
Topic CRUD + default packs — including the **preference-fragments pack**
("how this user wants to be answered/addressed/informed") so personalization
works from the first extraction; extract stage with JSON-schema-constrained
candidates (kind, content, entities, keywords, anticipated queries,
importance, confidence, provenance spans); no-topic-match ⇒ no candidate.
(Reflection mode deferred to Phase 24.) **Criteria:** prompt goldens;
preference fragments extracted from a fixture conversation; extraction failure
→ dead letter + event, never data loss.

### Phase 08 — Reconciliation + commit
SHA-256 + bigram-Jaccard pre-filters; neighbor retrieval; constrained
tool-call decision (add/update/merge/supersede/discard); trust gates;
transactional commit; **`supports`/`contradicts` link rows written from every
applicable decision** (day-one graph, §5.6); embedding upserts; mutation
events with reasons. **Criteria:** pre-filters kill duplicate-replay LLM
calls; trust gate blocks low-trust supersede; every mutation has an event and
applicable decisions write links; full write-path integration test with mock
gateway + real store.

### Phase 09 — Retrieval lanes + fusion
`vindex` seam (pgvector principal; sqlite brute-force → pure-Go HNSW behind
the seam — resolves **OQ-2**); concurrent lanes via errgroup; **native
time-window filters on every lane** (occurred_at index from Phase 03); RRF
fusion; structured filters (entity/keyword/kind/zone/episode). **Criteria:**
lane timings in metrics; fusion goldens; scope isolation at the retrieval
layer (both drivers); time-window query correctness tests.

### Phase 10 — Scoring & ranking
Six-counter utility model; decay (activity+wall-clock blend, stability growth,
floors); trust/source multipliers; scope affinity; hub dampening; write-echo
cooldown; **support summary** (strength + agreement/conflict across returned
set) computed per response. **Criteria:** scoring is a pure table-tested
function; debug mode returns the score breakdown; support summary flags a
planted contradiction fixture.

### Phase 11 — Injections, drill-down & feedback
**Injections recorded for every retrieval** (async append — zero added
response latency), citation handles in retrieve responses; `/v1/drilldown`;
`/v1/feedback`: per-memory signals AND per-response like/dislike resolved
through injections to the memories behind the response; retrieval profiles.
**Criteria:** injection rows exist for every retrieval (test); a response-level
dislike decrements the right memories' counters via injections; drill-down
returns exact verbatim spans; end-to-end read-path integration test.

### Phase 12 — Rerank, hot–warm cache & the SLO
Gateway rerank (profile-gated, cost-aware); **hot–warm cache**: (query
signature, scope) result cache + scope hot-set from injection frequency,
write-invalidated per scope (resolves **OQ-9** starting simple); load
benchmark rig. **Criteria:** cache hit serves without vector lookup (test);
write invalidates (test); rerank improves fixture retrieval metric; **p99 ≤
150 ms (hit ≤ 20 ms) at 1k concurrent sessions on postgres reference rig** —
the SLO benchmark joins `make bench` and the release gate.

### Phase 13 — Sweeps
Jittered tickers; singleflight (advisory locks on postgres); idempotency
markers; decay sweep, dedupe/compression sweep (the nightly "sleep cycle" —
near-duplicate fragments merge so the DB does not grow linearly with traffic),
rollup, re-enqueue sweep. **Criteria:** sweeps idempotent; crash mid-sweep
recovers; re-enqueue picks up lost derivations; compression provably reduces
near-duplicate fixtures without losing provenance.

### Phase 14 — Supersede chains, confirmation & rollback
Chain walk with cycle detection + hop cap; `pending_confirmation` resolution
(TTL auto-resolve — resolves **OQ-4**); contradiction boost; reversible
reconciliation contract (D-017) + `POST /v1/memories/{id}/rollback`.
**Criteria:** chain property tests; confirmation matrix; rollback round-trip
per op type.

### Phase 15 — Grants & team sharing
`internal/grants` (D-016): groups; read/contribute grants (topic/kind
filterable); zone ceilings; redaction hooks; store-layer enforcement; admin
governance endpoints; grant/revoke events. Resolves **OQ-7**. **Criteria:**
`personal` never crosses a grant (both drivers); revocation effective next
query; contribute-mode respects pool-owner trust gates; cross-tenant grants
impossible by construction.

### Phase 16 — MCP server (Dockyard)
Seven tools (`memory_ingest`, `memory_retrieve`, `memory_playbook`,
`memory_drilldown`, `memory_feedback`, `memory_assert`, `memory_topics`) built
with Dockyard (D-020); stdio + HTTP. **Criteria:** Dockyard validate/test
gates green; schema goldens; smoke drives each tool live.

### Phase 17 — SDKs + zero-config agent wiring
`sdk/stowage` HTTP + embedded modes (D-022); **Harbor assemble option that
wires ingest-on-turn / retrieve-on-context / feedback-on-outcome
automatically** — a new Harbor agent gets memory with zero plumbing; identity
lift; bus/cost adapters (D-019); flow recipes; **thin Python client**
(ingest/retrieve/feedback/playbook) for the Python agent framework.
**Criteria:** example Harbor agent uses memory with no memory-specific code in
`examples/`; embedded example runs sqlite-only CGo-free; Python client smoke
against a live server; in-process mode passes the HTTP SDK test suite.

### Phase 18 — Episodes & narratives
Boundary-detection sweep (heuristic-first per **OQ-8**: temporal gaps + topic
shift, gateway refinement optional); episode rows over record ranges;
narrative construction (gateway, schema-constrained, full provenance);
`episode_id` wiring on memories extracted within an episode. **Criteria:**
fixture conversations segment into expected episodes; narratives carry
provenance to every cited record; re-running detection is idempotent.

### Phase 19 — Episodic retrieval
`GET /v1/episodes`: list/inspect; **similar-episode contrast** (vector over
narratives + structured overlap + outcome comparison — "what worked then vs
what differs now"); **cross-episode aggregation** over time windows
(deterministic assembly first, optional cited gateway synthesis) — "what was I
working on in Q1?" returns structure, not a fragment dump. **Criteria:**
contrast fixture returns the planted analogous episode with outcome diff;
aggregation over a window returns episode-structured summary with citations.

### Phase 20 — Causal links
Inference pass proposing `caused_by`/`led_to` edges between decision memories
connected through episode narratives (gateway, schema-constrained, confidence
on every edge, reconciler-style review threshold); "why" traversal in
`GET /v1/memories/{id}` (links expansion) and graph queries. **Criteria:**
planted causal fixture chain is detected; traversal answers "why did X lead to
Y" with provenance at each hop; low-confidence edges are not asserted.

### Phase 21 — Citations v1 + citation feedback
Citation handles end-to-end (retrieve → agent → `/v1/citations/resolve`);
`wrong_citation` feedback downweights via injections; citation metadata in
resolve responses. **Criteria:** resolve returns memory + provenance +
metadata for any handle; wrong-citation feedback measurably lowers the
memory's next retrieval score (test); handles survive drill-down.

### Phase 22 — Verification & review queue
`POST /v1/verify`: claim-vs-cited-memories entailment check (gateway,
schema-constrained verdict + reasons); uncited-claim detection on
knowledge-bearing ingest → `pending_review` + `GET /v1/admin/review`
approve/reject. **Criteria:** planted unsupported claim is rejected by verify;
uncited knowledge candidate parks instead of committing; approve commits with
`agreed_upon` trust; reject leaves no active memory.

### Phase 23 — Reasoning traces + audit export
Trace reconstruction per response_id (query, injections, drill-downs, verify
verdicts, feedback) from day-one tables; signed export bundle
(`GET /v1/traces/{response_id}`); third-party audit API surface; trace
retention class (resolves **OQ-10**). **Criteria:** trace for a fixture
response reconstructs the full memory-into-conclusion chain; export verifies
against its signature; retention sweep honors the trace class.

### Phase 24 — Reflection & playbooks
Outcome reflection mode (`strategy`/`failure_mode` candidates, iteratively
refined, normally reconciled); multi-epoch re-reflection sweep;
`internal/playbook` deterministic assembly (counter-ranked, budget-packed,
provenance-linked, append-biased — resolves **OQ-6**); `GET /v1/playbook`.
**Criteria:** playbook package provably gateway-free (lint test); playbook
golden-stable given unchanged memories; fleet-loop integration test (outcome
in → strategy in next playbook).

### Phase 25 — Proactive trigger engine
`internal/proactive`: trigger rules (session start, episode similarity,
expiring validity); candidates scored through Phase 10 machinery,
threshold-gated, strict per-turn budget; per-tenant/profile governance in
scope_settings (limits, trigger classes, opt-outs); accept/dismiss feedback
adjusts per-trigger confidence via suggestion counters. **Criteria:**
below-threshold candidates never surface; per-turn budget enforced; tenant
opt-out suppresses everything; repeated dismissals demote a trigger (test);
governance changes effective without restart.

### Phase 26 — Temporal pattern mining (stretch)
Routine detection over episode/record timing ("Monday 9 am → campaign
email"); suggestions via Phase 25's machinery and governance. Explicitly
stretch: deferred if the release gate (Phase 27) is in reach. **Criteria:**
planted weekly pattern is detected and suggested once, then respects feedback.

### Phase 27 — Eval harness
`stowage eval`: gain harness on a Harbor fleet (D-019); LoCoMo-style benchmark
(target ≥ 0.86 — D-023); online-adaptation scenarios (ACE-style); episodic
recall scenarios; perf benchmarks incl. the §4.2 latency SLO. **Criteria:**
positive gain on standard scenarios; reproducible `eval/REPORT.md` (the
open-source gate artifact); negative gain or SLO miss blocks release.

### Phase 28 — Hardening, open-source readiness & v1
Security pass; docs; CHANGELOG; cross-compile matrix + checksums; public-repo
audit (license per OQ-5, full-history forbidden-names sweep, external-audience
docs); key-management incident runbook. **Criteria:** §13/§14 checklists pass
repo-wide; CGo-free artifacts for darwin/linux × amd64/arm64; drift-audit
green over full git history.