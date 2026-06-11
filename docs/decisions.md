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

## D-040 — Bifrost driver speaks OpenAI-compatible wire format (base_url-agnostic)

2026-06-11. The bifrost gateway driver uses the OpenAI-compatible wire format
(`POST {base_url}/chat/completions` with `response_format: json_schema strict`,
`POST {base_url}/embeddings`) and is base_url-agnostic: it works against
Bifrost, OpenRouter, or any OpenAI-compatible endpoint. Provider-specific
drivers are added only when a wire format actually diverges from this baseline
(P5, RFC §7). All wire structs live exclusively in `internal/gateway/bifrost`;
no other package may import them (CLAUDE.md §13). This supersedes the
placeholder in D-039's plan entry and is the definitive wire-format decision
for Phase 04.
