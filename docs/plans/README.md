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
  launch-track hot paths even though some consuming capabilities are
  post-launch. Skipping a signal write "because nothing reads it yet" is drift.
- **The benchmark gate (from Phase 13 on):** the eval suite runs in CI; a phase
  that regresses a benchmark or the SLO does not merge. Eval is continuous, not
  terminal (D-035).
- **The knob guardrail (RFC §9.4):** every new config knob ships with a tuned
  default, a placement in every profile, and docs — in the same PR.

## Structure: launch track + post-launch tracks (D-033)

The launch track contains every differentiator and the proof. Post-launch
tracks contain capabilities whose unbackfillable signals are already captured
by the day-one schema — deferring the feature costs nothing structurally.

**Launch = v0.1 = phases 01–27 + terminal hardening (D-076).** The owner recut
the roadmap (2026-06-17): we do **not** launch at Phase 21. The capabilities
previously deferred to post-launch tracks (episodes/temporal 22–24, trust
extensions 25–26, proactive 27) are pulled **into** the v0.1 launch scope; the
hardening & launch work (former Phase 21 content) runs **last, after Phase 27**,
as the terminal v0.1 gate. Open-source gate (unchanged): the eval report
(`eval/REPORT.md`) with the public-benchmark comparison table ships the same day,
carrying the judged-QA number (`answer_quality` — Phase 20).

> **Recut sequencing (D-076).** Phase 20 (judged eval + competitor table) is
> pulled **ahead of** Phase 19 (reflection write-side): the judged headline number
> does not depend on reflection. The RFC §12 gain-fleet + online-adaptation slice
> that *does* consume the reflection→playbook loop is carved out as **Phase 20b**,
> running after Phase 19. Open item for review: whether to physically renumber the
> phase files, or keep the numbers as stable identifiers with this track reframing
> (this plan keeps the numbers stable).

## Launch track

### Wave 1 — Foundation

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 01 | Scaffold & CI | repo, Makefile, CI, hooks, drift-audit | §2 | — |
| 02 | Config, identity, telemetry, keys, profiles | `internal/config` (zero-config defaults + profiles), `internal/identity`, `internal/telemetry`, `internal/auth` | §9.4, §5.3, §11 | 01 |
| 03 | Store seam + the day-one schema | `internal/store` {postgres, sqlite}, full §8.1 schema, migrations, conformance suite | §5.0, §8 | 02 |
| 04 | Gateway seam + bifrost driver | `internal/gateway` {bifrost, mock}, batching, metering, embedding cache | §7 | 02 |

### Wave 2 — Write path

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 05 | Records, ingest API & branches | `internal/records`, ingest (outcome/branch/occurred_at), branches lifecycle, admin key endpoints | §4.1, §5.1, §5.5 | 03 |
| 06 | Buffers | branch-aware accumulation, flush triggers | §4.1 | 05 |
| 07 | Topics + extraction | `internal/topics`, extract stage, preference-fragments pack | §5.4, §5.2 | 04, 06 |
| 08 | Reconciliation + commit | `internal/reconcile`, pre-filters, trust gates, commit txn, day-one link writes | §6, §5.6 | 07 |

### Wave 3 — Read path

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 09 | Retrieval lanes + fusion | `internal/retrieval`, `internal/vindex`; four lanes; native time windows; RRF; **gateway-free degraded mode** | §4.2 | 03, 04, 08 |
| 10 | Scoring & ranking | counters, decay, trust, scope affinity, temporal-proximity boost, hub dampening, cooldown, support summary | §5.2, §4.2 | 09 |
| 11 | Injections, feedback & citations v1 | injections recording, drill-down, `/v1/feedback` (memory + response + citation level), citation handles + `/v1/citations/resolve` | §5.7, §6c | 10 |
| 12 | Rerank, hot–warm cache & the SLO | gateway rerank, query/hot-set cache, p99 ≤ 150 ms @ 1k sessions rig | §4.2 | 10, 11 |

### Wave 4 — Proof (pulled forward deliberately)

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 13 | Eval harness — the benchmark gate | `eval/`: LongMemEval, LoCoMo, ConvoMem, MemBench runners + per-question committed results; gain harness skeleton; SLO benchmark; CI gate wiring | §12 | 12 |

### Wave 5 — Lifecycle & sharing

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 14 | Sweeps | decay, dedupe/compression, rollup, re-enqueue; markers; singleflight | §6 | 08, 10 |
| 15 | Supersede chains, confirmation & rollback | chains, cycle caps, `pending_confirmation`, contradiction boost, rollback API | §6 | 14 |
| 16 | Grants & team sharing | `internal/grants`: groups, grants, zone ceilings, redaction hooks | §5.3 | 09, 11 |

### Wave 6 — Surfaces & launch

| # | Phase | Owns | RFC | Deps |
|---|-------|------|-----|------|
| 17 | MCP server (Dockyard) | `internal/mcpserver`, 7 tools | §9.2 | 11 |
| 18 | SDKs + zero-config agent wiring | `sdk/stowage` (HTTP + embedded), Harbor assemble option, bus/cost adapters, flow recipes, Python client | §9.3, §10, §2 | 11 |
| 19 | Reflection & playbooks | outcome reflection mode, re-reflection sweep, `internal/playbook`, `GET /v1/playbook` | §6a | 08, 11, 14 |
| 20 | Eval finalization + competitor report | full gain harness on a Harbor fleet, online-adaptation scenarios, comparison table vs published competitor numbers, `eval/REPORT.md` | §12 | 13, 18, 19 |
| 21 | Hardening & launch | security pass, docs, release matrix, public-repo audit, license (OQ-5), five-minute-rule smoke | §13, §9.4 | all |

### Numbering reconciliation (executed vs planned)

The executed track diverged from the tables above when eval was pulled
forward and Wave 5/6 phases landed in dependency order. Mapping (table № →
executed plan file): 13 → `phase-13-eval.md`, 14 → `phase-14-sweeps.md`,
16 → `phase-15-grants.md`, 17 → `phase-16-mcp.md`, 18 → `phase-17-sdks.md`.
Table slot **15 (supersede chains, confirmation & rollback) was skipped in
that shuffle** — discovered at Phase 17 gate review; it executes as
`phase-18-rollback-confirmation.md` (rollback API per D-017, OQ-4
confirmation resolution, depth-capped chain exposure on
`GET /v1/memories/{id}`). Two slot-15 line items are deferred with intent:
*contradiction boost* is §6c-adjacent retrieval tuning → v1.2 trust
extensions; *chain cycle caps* reduce to the depth cap above because
rollback unwinds newest-first one step at a time. Executed phases 19–21
match table slots 19–21.

## Pulled into v0.1 (formerly post-launch tracks — D-076)

> These three tracks (episodic/temporal, trust extensions, proactive) were the
> D-033 post-launch deferral. The D-076 recut pulls them **into** the v0.1 launch
> scope: phases 22–27 ship before the terminal hardening gate. The `v1.x` labels
> below are retained only as the original capability grouping.

### v1.1 — Episodic & temporal

| # | Phase | Owns | RFC |
|---|-------|------|-----|
| 22 | Episodes & narratives | boundary-detection sweep, narrative construction, episode wiring | §6b |
| 23 | Episodic retrieval | `memory_episodes` (list/get/window) across {SDK,HTTP,MCP}; deterministic | §6b |
| 23b | Similar-episode contrast | `memory_episodes` `similar_to` (vector-over-narratives nearest-episode contrast) across {SDK,HTTP,MCP}, degraded-safe — D-082. **LLM window-synthesis deferred** (deterministic window list already serves the §6b structured summary; pulled on an eval signal) | §6b |
| 24 | Causal links | inferred `led_to` edges (schema-constrained, confidence-gated, inferred once at narration) + the gateway-free `memory_causal` "why" traversal across {SDK,HTTP,MCP} — D-083 | §5.6, §6b |
| 24b | Episode threading | gateway-free sweep groups session-episodes into cross-session **arcs** via `relates_to` edges between narratives (content bigram-Jaccard ∧ temporal ∧ project/user); read via `memory_episodes` `arc_of` across {SDK,HTTP,MCP}. **Mechanism shipped OFF by default**; enablement eval-gated — **D-081 (ratified)** | §6b |

### v1.2 — Trust extensions

| # | Phase | Owns | RFC |
|---|-------|------|-----|
| 25 | Verification & review queue | `memory_verify` (`POST /v1/verify`) schema-constrained gateway entailment, degraded-safe + `memory_review` scope-level queue (assert `review`→`pending_review`; approve→active / reject→quarantined, reversible) across {SDK,HTTP,MCP} — D-084. Auto uncited-detection + trace export deferred | §6c |
| 26 | Reasoning traces + audit export | `memory_trace` (`GET /v1/traces/{response_id}`) across {SDK,HTTP,MCP}: per-response chain reconstructed on demand from day-one tables (query+verdicts captured to events), ed25519-signed bundle. Retention = source rows; OQ-10 settled — D-086 | §6c |

### v1.3 — Proactive

| # | Phase | Owns | RFC |
|---|-------|------|-----|
| 27 | Proactive trigger engine | `internal/proactive`: three trigger rules (recent/similar episode, expiring) scored by `scoring.Score`; per-scope governance (threshold+budget+classes) in `scope_settings`, profile-defaulted + opt-out; accept/dismiss confidence tuning; `memory_suggestions` {SDK,HTTP,MCP} + `memory_proactive_config` admin-tier {HTTP,MCP}; gateway-free expiry sweep; no new schema — D-087 | §6d |

### Backlog (no phase until pulled)

Temporal pattern mining (§6d stretch); Stowage Console as a Dockyard MCP App
(§9.2); managed-cloud control plane (§14).

## Phase detail blocks

### Phase 01 — Scaffold & CI
Repo skeleton matching CLAUDE.md §3; Makefile targets; golangci config; CI
(build, test -race, lint, mirror, drift-audit); pre-commit hook; smoke
template + phase-01 smoke. **Criteria:** `make preflight` green on a fresh
clone; CI green; mirror enforced.

### Phase 02 — Config, identity, telemetry, keys, profiles
Typed config with `env.VAR` indirection, fail-loud `Validate()`; **zero-config
defaults + the three profiles (assistant / coding-agent / fleet)**;
`stowage config explain` (value + provenance); identity scope type + ctx
helpers; slog setup, secret redaction; Prometheus registry; `internal/auth`
runtime key store (constant-time verify; admin endpoints land with Phase 05).
**Criteria:** server boots with exactly one secret env var; invalid config
fails with file:line precision; `config explain` shows provenance for every
effective value; profile switch changes documented knobs (table test); no
secret in logs.

### Phase 03 — Store seam + the day-one schema
Full §8.1 schema in the first migration set (records, memories, junctions,
provenance, injections, links, episodes, branches, topics, buffers,
grants/groups, feedback, suggestions, scope_settings, api_keys, events,
dead_letters, job_markers). Postgres driver (pgx — principal, D-021) + sqlite
driver (modernc, WAL, dedicated writer goroutine); forward-only migrations;
shared conformance suite; scope-parameterized query builders only; indexes
designed up front ((scope, occurred_at), (scope, status), injections
(response_id), links (from, type)). **Criteria:** conformance green on both
drivers under `-race`; cross-scope queries return nothing; no sqlite lock
storms under concurrent writers; EXPLAIN-verified index use (postgres).

### Phase 04 — Gateway seam + bifrost driver
`Gateway` (Embed batched, Complete schema-constrained); bifrost + mock
drivers; batching, retry/backoff, circuit breaker; embed cache; cost metering
events; model+dims pinning. Resolves **OQ-1**. **Validation note (updated
2026-06-11):** OpenRouter (dev key local via `.env`, never committed) serves
current chat models for `Complete`-path validation, and embeddings via
`google/gemini-embedding-2` (verified live, 3072 dims default) for the
`Embed` path. Rerank validation (Phase 12): `cohere/rerank-4-fast`. This
also unblocks the OQ-1 bake-off. **Criteria:** golden wire tests; cache hit
path proven; cost events emitted; boot fails on dims mismatch.

### Phase 05 — Records, ingest API & branches
Verbatim append (ULID, scope, occurred_at vs created_at, outcome, branch_id,
response_id, token estimate); `POST /v1/records` ACK after durable write;
lexical indexing; pipeline enqueue; `POST /v1/branches` fork/merge/discard;
admin key endpoints; retention/DSAR cascade stubs. **Criteria:** ingest p99
< 15 ms (sqlite local bench); ACK never waits on the pipeline; records
immutable; branch discard leaves no active branch memories but all records;
key rotate/revoke without restart (smoke).

### Phase 06 — Buffers
Per (scope, branch, key) accumulation; count/token/age/session-end/explicit
triggers; `-race`-proven many-writers; re-enqueue recovery contract. Resolves
**OQ-3**. **Criteria:** trigger matrix table-tested; exactly-once flush under
concurrency; branch isolation.

### Phase 07 — Topics + extraction
Topic CRUD + default packs incl. **preference fragments**; schema-constrained
candidates with provenance spans; no-topic-match ⇒ no candidate. (Reflection
mode: Phase 19.) **Criteria:** prompt goldens; preference fragments from a
fixture conversation; extraction failure → dead letter + event.

### Phase 08 — Reconciliation + commit
SHA-256 + bigram-Jaccard pre-filters; neighbor retrieval; constrained
tool-call decisions; trust gates; transactional commit; `supports`/
`contradicts` link writes; mutation events with reasons. **Criteria:**
pre-filters kill duplicate-replay LLM calls; trust gate blocks low-trust
supersede; every mutation evented; write-path integration test (mock gateway +
real store).

### Phase 09 — Retrieval lanes + fusion
`vindex` seam (pgvector principal; sqlite brute-force → pure-Go HNSW —
resolves **OQ-2**); concurrent lanes; native time-window filters; RRF;
structured filters; **gateway-free degraded mode** (lexical + anticipated +
structured lanes serve with a degraded flag when the gateway is down — D-036).
**Criteria:** lane timings in metrics; fusion goldens; scope isolation at
retrieval (both drivers); time-window correctness; gateway-down test still
returns lexical results flagged degraded.

### Phase 10 — Scoring & ranking
Six counters; decay (activity+wall-clock, stability growth, floors);
trust multipliers; scope affinity; **temporal-proximity boost** (brief 06);
hub dampening; cooldown; support summary. **Criteria:** pure table-tested
scoring; debug score breakdown; support summary flags planted contradiction;
temporal-proximity fixture ranks near-window memory above stale equivalent.

### Phase 11 — Injections, feedback & citations v1
Async injections for every retrieval; citation handles in responses;
`/v1/citations/resolve` (memory + provenance + metadata); `/v1/drilldown`;
`/v1/feedback` at memory, response (like/dislike via injections), and citation
(`wrong_citation` downweight) levels; retrieval profiles. **Criteria:**
injection rows for every retrieval; response-level dislike decrements the
right memories; wrong-citation feedback lowers next-retrieval score; drill-down
returns exact spans; read-path integration test.

### Phase 12 — Rerank, hot–warm cache & the SLO
Gateway rerank (profile-gated, cost-aware); (query-signature, scope) cache +
injection-frequency hot set, scope-invalidated (**OQ-9**: per-scope first);
load rig. **Criteria:** cache hit skips vector lookup; write invalidates;
rerank improves fixture metric; **p99 ≤ 150 ms (hit ≤ 20 ms) @ 1k concurrent
sessions on postgres reference rig** — joins `make bench` and the release gate.

### Phase 13 — Eval harness: the benchmark gate
`stowage eval` with runners for **LongMemEval, LoCoMo, ConvoMem, MemBench**;
per-question result files committed; one-command reproduction; gain-harness
skeleton (full fleet version in Phase 20); SLO benchmark integration; **CI
gate wiring — from this phase on, a benchmark or SLO regression blocks
merge.** **Criteria:** all four public benchmarks run end-to-end against a
live server with committed results; CI fails on planted regression; first
baseline numbers recorded in `eval/BASELINE.md`.

### Phase 14 — Sweeps
Jittered tickers; singleflight (advisory locks on postgres); idempotency
markers; decay, dedupe/compression ("sleep cycle"), rollup, re-enqueue.
**Criteria:** idempotent; crash-recoverable; re-enqueue catches lost
derivations; compression reduces near-duplicate fixtures without provenance
loss; benchmark gate stays green.

### Phase 15 — Supersede chains, confirmation & rollback
Chain walk + cycle caps; `pending_confirmation` (TTL auto-resolve — **OQ-4**);
contradiction boost; D-017 reversibility + rollback endpoint. **Criteria:**
chain property tests; confirmation matrix; rollback round-trip per op type.

### Phase 16 — Grants & team sharing
Groups; read/contribute grants (topic/kind filterable); zone ceilings;
redaction hooks; store-layer enforcement; grant/revoke events. Resolves
**OQ-7**. **Criteria:** `personal` never crosses a grant (both drivers);
revocation effective next query; contribute respects pool-owner trust gates;
cross-tenant grants impossible by construction.

### Phase 17 — MCP server (Dockyard)
Seven tools built with Dockyard (D-020); stdio + HTTP. **Criteria:** Dockyard
validate/test green; schema goldens; smoke drives each tool live.

### Phase 18 — SDKs + zero-config agent wiring
`sdk/stowage` HTTP + embedded (D-022); **Harbor assemble option** wiring
ingest/retrieve/feedback automatically (D-032); identity lift; bus/cost
adapters (D-019); flow recipes; **thin Python client**. **Criteria:** example
Harbor agent uses memory with zero memory-specific code; embedded example
sqlite-only CGo-free (works offline in degraded retrieval mode); Python client
smoke; in-process mode passes the HTTP SDK suite.

### Phase 19 — Reflection & playbooks
Outcome reflection mode; re-reflection sweep; `internal/playbook`
deterministic assembly (**OQ-6**); `GET /v1/playbook`. **Criteria:** playbook
package provably gateway-free (lint test); golden-stable output; fleet-loop
integration test.

### Phase 20 — Eval finalization: judged QA + competitor report
**Pulled ahead of Phase 19 (D-076).** Adds the **judged-QA mode** — an opt-in,
full-mode-only reader LLM answers from retrieved context and an LLM judge grades it
against the gold answer semantically, emitting `answer_quality` (the
competitor-comparable metric) alongside the retrieval-only `answer_context_hit`;
plus a deterministic normalization pass (number-word + either-direction) on
`answer_context_hit`, the `longmemeval_s` distractor haystack, final public-suite
runs, and the **comparison table vs published competitor numbers** (Mem0, Zep,
Letta, mempalace, Engram where public) in `eval/REPORT.md` — the launch artifact.
The CI mock gate stays deterministic, LLM-free, string-match-only. Plan:
`phase-20-eval-finalization.md`. **Criteria:** the binding list in that plan
(judged `answer_quality` on `longmemeval_s`; CI gate unaffected; schema-constrained
judge; reproducible report). SOTA/top-tier or a documented hold (D-023).

### Phase 20b — Gain-fleet + online-adaptation (post-Phase-19)
The RFC §12 slice that consumes the reflection→playbook loop, carved out of Phase
20 by D-076 and run after Phase 19 ships reflection: the full gain harness on a
Harbor fleet (memory-on vs memory-off delta) and the ACE online-adaptation
scenarios. **Criteria:** positive gain on the standard scenarios; compounding
improvement across sequential tasks.

### Hardening & launch (terminal v0.1 gate — runs after Phase 27; former Phase 21)
Security pass; docs for an external audience; CHANGELOG; cross-compile matrix
+ checksums; public-repo audit (license per OQ-5, full-history forbidden-names
sweep); **five-minute-rule smoke** (fresh machine → one env var → first memory
stored and retrieved in < 5 min, scripted). **Criteria:** §13/§14 checklists
repo-wide; CGo-free artifacts darwin/linux × amd64/arm64; history-wide
drift-audit green; five-minute smoke green.

### Phases 22–27 (v0.1 launch scope — D-076) — summaries
Detail plans are authored when each track is pulled. 22 episodes & narratives
(heuristic-first boundaries — **OQ-8**; provenance-complete narratives;
idempotent re-detection). 23 episodic retrieval (shipped deterministic
`memory_episodes` list/get/window across {SDK,HTTP,MCP} — D-080; similar-episode
contrast + gateway-synthesized window summary deferred to 23b). 24 causal links
(schema-constrained inference, confidence-gated assertion, "why" traversal). 24b
episode threading (PROPOSED, D-081 — cross-session arc grouping; gated on an
episodic-eval win).
25 verification & review queue (entailment verdicts; uncited knowledge parks,
approve commits as `agreed_upon`). 26 reasoning traces + audit export (signed
bundles; retention class — **OQ-10**). 27 proactive engine (threshold +
per-turn budget; per-tenant governance in scope_settings; dismissals demote —
all consuming signals captured since day one).

## Productionization hardening track (D-067)

An orthogonal, post-launch program enforcing the parity lens — *"same code, same
seams"* across the embedded-sqlite and server-over-Postgres paths (findings:
`docs/notes/parity-lens-findings.md`; method:
`docs/notes/productionization-playbook.md`). Numbered as a `phase-h*` sub-series
so it does not collide with the reserved launch (19–21) / post-launch (22–27)
roadmap slots; smoke scripts still match the `scripts/smoke/phase-*.sh` gate.
Governing principle (D-067): **one logic core, thin tiered surfaces** — single-user
capabilities reach {SDK, HTTP, MCP}; multi-user/admin capabilities reach {HTTP,
MCP}; backend parity (sqlite↔Postgres) throughout.

### Wave A — correctness + honesty

| # | Phase | Owns | RFC | Deps | Decision |
|---|-------|------|-----|------|----------|
| h1 | `boot.StartPipeline` — pipeline + lifecycle parity across entrypoints (flagship: `stowage mcp` runs no pipeline today) | `internal/boot`, `cmd/stowage`, `sdk/stowage` | §4.1, §9.2, §9.3, §10 | 06–08, 14, 17, 18 | D-068 |
| h2 | Wave A correctness + honesty bundle (embedded config validation + D-030, gateway defaults, sqlite FTS sanitization, rune-safe drill-down, MCP contribute-mode fail-loud, doc honesty) | `sdk/stowage`, `internal/config`, `internal/store`, `internal/retrieval`, `internal/mcpserver` | §9.4, §9.1, §4.2, P1 | 02, 09, 17, 18 | D-069 |

Wave A shipped: h1 (D-068, #28), h2 (D-069, #29), checkpoint (#30).
Wave B shipped: h3 (D-070, #32), h4 (D-071, #33), checkpoint (#34).

### Wave B — mechanical re-homing / tiered surface-parity

| # | Phase | Owns | RFC | Deps | Decision |
|---|-------|------|-----|------|----------|
| h3 | Reconciliation reversibility parity — lift rollback/confirm/reject/get into an exported `reconcile` core; reachable on {SDK, MCP, HTTP} | `internal/reconcile`, `internal/api`, `internal/mcpserver`, `sdk/stowage` | §6, §9.1-3 | 15/18, h1, h2 | D-070 |
| h4 | Tiered control-verb surface parity — single-user (topics/flush/branches/assert) on {SDK, MCP, HTTP}; multi-user (grants mgmt, contribute honoring) on {HTTP, MCP} only | `sdk/stowage`, `internal/mcpserver`, `internal/grants`, `internal/api` | §5.3-5, §4.1, §9.1-3 | **h3**, 16 | D-071 |

h4 shares the SDK `Client`/`http`/`embedded` trio + `mcpserver/server.go` with h3,
so **h4 lands after h3** (sequential, file-collision per playbook §3). A Wave-B
checkpoint audit (§17) gates Wave C.

### Wave C — finish the half-shipped primitives

| # | Phase | Owns | RFC | Deps | Decision |
|---|-------|------|-----|------|----------|
| h5 | Deterministic playbook assembly (LLM-free) — finish the stubbed `memory_playbook`/`Client.Playbook`/`GET /v1/playbook` across {SDK, MCP, HTTP} | `internal/playbook`, `internal/store`, `internal/api`, `internal/mcpserver`, `sdk/stowage` | §6a.3, §9.1-3 | 08, 10, 16, 17 | D-072 |

Owner posture: **finish** (no deferrals); consumers on {SDK, MCP, HTTP} accommodated
from the get-go. Reflection (§6a.1-2, the LLM write-side) stays roadmap Phase 19.
Wave C is **h5 alone** — runtime API-key management is HTTP-only by design (owner,
2026-06-16; a recorded tier exception: key/credential admin → {HTTP} only, distinct
from grants admin → {HTTP, MCP}). A Wave-C checkpoint gates Wave D.

Wave C shipped: h5 (D-072, #36), checkpoint (#37).

### Wave D — decision-shaped RFC remainder (closes the program)

Wave D is an **RFC amendment, not an implementation phase** (D-073): it ratifies
the **server deployment shape** (one process exposes both HTTP + MCP over one
`boot.Stack` + `boot.StartPipeline`; stdio MCP a separate lightweight mode — owner,
2026-06-17) and codifies the **one logic core, thin tiered surfaces** invariant +
the three-tier capability matrix the program proved (RFC §9.2/§9.5, CLAUDE.md §6).
Named follow-up: a small phase to co-mount the MCP-HTTP handler onto `stowage
serve` (h6 below). Deferred (recorded): reflection §6a.1-2 → Phase 19; playbook
topic-grouping → schema amendment; DSAR → Phase 21; grants RedactionProfile →
later.

### Wave D follow-up — co-mount (D-073 Decision 1 implementation)

| # | Phase | Owns | RFC | Deps | Decision |
|---|-------|------|-----|------|----------|
| h6 | Co-mount MCP-over-HTTP onto `stowage serve` — one process, both surfaces, one `boot.Stack`+`StartPipeline` (two listeners, shared stack; new `server.mcp_listen` knob) | `cmd/stowage`, `internal/config` | §9.2, §9.5 | h1, h3-h5 | D-074 |

Two listeners (not a single path-prefixed port) because MCP streams and must not
inherit the REST `WriteTimeout`/middleware; the shared stack+pipeline is what
delivers the D-073 cache-coherence win. Default `server.mcp_listen` empty = serve
unchanged (open question in the h6 plan: opt-in vs on-by-default).

**Program complete (D-067).**

### Post-program follow-ups

| # | Phase | Owns | Decision |
|---|-------|------|----------|
| h6 | Co-mount MCP-over-HTTP onto `stowage serve` (one process, both surfaces) | `cmd/stowage`, `internal/config` | D-074 |
| h7 | bifrost custom-provider rerank (full OpenRouter stack) + benchmark rebase to cheaper models | `internal/gateway/bifrost`, `eval/harness` | D-075 |
 Waves A (h1/h2), B (h3/h4), C (h5) + three
checkpoint audits shipped; the "same code, same seams" parity gap is closed.

## Performance & resource hardening track (D-126)

An orthogonal, post-launch track — same posture as the D-067 productionization program
— that gives Stowage a **measured** picture of its runtime resource behaviour (CPU,
heap, goroutines, block/mutex contention) instead of an asserted one. The latency SLO
(`make slo`, D-031/D-095) is a p99 stopwatch on the read path; it says nothing about
idle CPU, heap growth, goroutine leaks, or drain-on-shutdown — the P2 contract — which
this track measures and regression-gates. Numbered `phase-pN-*` so it does not collide
with the launch (01–27), post-launch (22–27), or productionization (`h*`) slots; smoke
scripts still match the `scripts/smoke/phase-*.sh` gate. **Harness-first** (D-126): the
lead phase builds the instrumentation + baselines; each leak/inefficiency it surfaces
lands as a scoped `pN` follow-up gated by the baseline (the eval continuous model,
D-035), not one open-ended mega-phase.

| # | Phase | Owns | RFC | Deps | Decision |
|---|-------|------|-----|------|----------|
| p1 | Profiling & leak-detection harness + baselines — auth-gated off-by-default pprof listener (`server.pprof_listen`), `MemStats`/`NumGoroutine` telemetry sampler, `goleak` in the goroutine-heavy packages (advisory), the `internal/bench/profile/` load+profile rig (goroutine-stability + idle gates), committed `eval/PROFILE.md` baselines | `internal/bench`, `internal/telemetry`, `cmd/stowage`, `internal/config`, `internal/api` | §2.1 (P2), §8.2, §11, §13/§14 | 03–14, h1 | D-126 |

Plan: `phase-p1-profiling-harness.md`. Posture: **build + baseline, don't fix** — fixes
are scoped `pN` follow-ups. Gating is **advisory-then-promote**; scope covers in-process
Go concurrency **and** backends under load (pgx pool, sqlite writer goroutine).
## Adoption & ergonomics track (D-131)

An orthogonal, post-launch track that sharpens Stowage's first-five-minutes story and
loosens the gateway's single-model / single-key assumptions. Three gaps between what the
README promises and what the binary does: the default gateway driver was `mock` (so one
secret wired a *synthetic* gateway); one shared completion model drove every learner stage;
and the quickstart implied MCP was co-mounted by default and that one env var pointed at a
real provider. Numbered `phase-aN-*` so it does not collide with the launch (01–27),
post-launch (22–27), productionization (`h*`), or performance (`p*`) slots; smoke scripts
still match the `scripts/smoke/phase-*.sh` gate.

| # | Phase | Owns | RFC | Deps | Decision |
|---|-------|------|-----|------|----------|
| a1 | Gateway defaults → the real Bifrost/OpenRouter stack (one-secret five-minute start, fail-loud minimums, `mock` escape hatch) | `internal/config`, `internal/boot`, `internal/gateway/bifrost` | §9.4, §10 | 04, 09c, h7 | D-131 |
| a2 | Per-learner-stage model selection (`extract`/`reconcile`/`reflect` models, fallback to `gateway.model`) | `internal/config`, `internal/pipeline`, `internal/reconcile`, `internal/reflect`, `internal/boot`, `internal/lifecycle` | §9.4, §10 | a1, 19 | D-132 |
| a3 | Quickstart honesty & MCP opt-in clarity (README/getting-started/glossary + serve startup hint; MCP stays opt-in) | `README.md`, `docs/`, `cmd/stowage` | §9.2, §9.4, §9.5 | a1 | D-133 |
| a1b | Per-concern provider/key/base_url overrides (embed, rerank) with inherit-on-empty | `internal/config`, `internal/gateway/bifrost` | §10 | a1 | D-131 |

Plans: `phase-a1-gateway-defaults.md` (shipped), `phase-a2-learner-models.md` (shipped),
`phase-a3-quickstart-honesty.md` (shipped). Per-concern keys (a1b) were folded out of a1 once OpenRouter
proved it serves all three lanes on one key (D-131 deviation note).

## Agent-identity & read-time scoping track (D-135)

An orthogonal, post-launch track that gives Stowage read-time agent identity
and per-agent curation without persisting agent on any of the 12 scope tables.
Numbered `phase-aeN-*` so it does not collide with the launch (01–27),
post-launch (22–27), productionization (`h*`), performance (`p*`), or adoption
(`a*`) slots; smoke scripts still match the `scripts/smoke/phase-*.sh` gate.

Charter (wave map + dependency graph): `docs/plans/track-adoption-ergonomics.md`.
Posture is **additive-first** — read identity from `_meta` *alongside* the existing
arguments until the JWT verifier (ae7) lands; tenant stays the credential-pinned P3
boundary throughout. Wave-0 posture decisions are settled (D-135–D-140); the
multiplexing-vs-strict default is **STRICT** with two orthogonal opt-in knobs (D-137).

| # | Phase | Owns | RFC | Deps | Decision |
|---|-------|------|-----|------|----------|
| ae3 | Shared render core (eval-mode vs MCP-mode) | `internal/retrieval` (render), `eval/harness`, `internal/mcpserver` | §4.2, §9.2, §9.5 | — | D-141 |
| ae4a | Lean MCP read — `Text` markdown + episode hook + drill by citation ULID | `internal/mcpserver`, `internal/retrieval`, `sdk/stowage`, `internal/api` | §4.2, §5.7, §6b, §9.2, §9.5 | ae3 | D-142 |
| ae5 | List / browse (most-recent-first, superseded filter) | `internal/store` (+ both drivers + conformance), `internal/retrieval`, surfaces | §5.2, §5.3, §8.1, §9.1-9.5 | — | D-143 |
| ae6 | Request-level topic filter (own-scope, fail-open, lane-aware) | `internal/retrieval`, surfaces | §4.2, §5.3, §5.4, §9.5 | — | D-144 |
| ae1 | Read-time agent identity dimension (+ Dockyard v1.8 bump) | `internal/identity`, `internal/store`, `internal/retrieval`, surfaces | §5, §5.3, §9.5 | ae6 | D-135, D-139 |
| ae2 | Additive `_meta` identity intake | `internal/mcpserver`, `internal/identity` | §5, §9.5, D-125 | ae1 | D-137, D-138 |
| ae7 | Harbor-aligned JWT verifier (second mode) | `internal/auth`, `internal/api`, `internal/mcpserver`, `internal/config` | §5.5, §9.5 | — | D-136 |
| ae8 | Effective-scope resolution + read-side enforcement | `internal/identity`, `internal/store`, `internal/retrieval`, `internal/config` | P3/§6, §5, §9.5 | ae2, ae7 | D-137 |
| ae9 | Per-agent / per-key topic views (read-time curation) | `internal/retrieval`, `internal/identity`, `internal/store`, surfaces | §5.3, §6, §9.5 | ae1, ae6 | D-139 |
| ae4b | *(deferred)* Causal hook (batch links-exist) + positional drilldown | `internal/store` (+ drivers + conformance), `internal/reconcile`/`internal/episodes`, `internal/retrieval`, `internal/mcpserver` | §5.6, §5.7, §4.2, §8.1 | ae4a | D-145 (on promotion) |
| ae2b | Breaking removal of `project_id`/`user_id` from MCP contracts | `internal/mcpserver`, `sdk/stowage`, `docs/` | D-125, §9.5 | ae7, ae8 | D-140 |
| ae10 | *(deferred)* `layer`/`intent` read-shaping argument | `internal/retrieval`, surfaces | §6 | ae2, ae3 | — |

Plans (all **draft** unless noted): `phase-ae3-shared-render-core.md`,
`phase-ae4a-lean-mcp-render.md`, `phase-ae5-browse.md`, `phase-ae6-topic-filter.md`,
`phase-ae1-read-time-agent.md`, `phase-ae2-meta-intake.md`, `phase-ae7-jwt-verifier.md`,
`phase-ae8-effective-scope.md`, `phase-ae9-topic-views.md`,
`phase-ae2b-contract-removal.md`; `phase-ae4b-causal-hook.md`,
`phase-ae10-read-shaping.md` (**deferred**). Wave-authoring checkpoint reconciliations
are recorded as D-150 (session never filters and never ranks a read — cross-session
recall preserved) and D-151 (ae1↔ae9 converge on one `topic_views` table /
`TopicViewStore` seam / `retrieval.agent_views.enabled` knob).

**Implementation tracking:** `phase-ae*` is built autonomously, one PR per wave,
under the protocol and live wave-board in `docs/plans/ae-implementation-roadmap.md`
(workers = Sonnet; dual adversarial review; orchestrator fixes same-wave; mandatory
live 3-surface SDK/HTTP/MCP validation; merge on web-CI green; roadmap marked per
wave). The orchestrator keeps that file's checkboxes current.
