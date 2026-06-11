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
negative gain on the standard scenarios blocks release (RFC §12, Phase 17).

## D-015 — Small MCP surface (6 tools)

2026-06-10. Both predecessors grew sprawling surfaces (50+ endpoints / 70+
tools). Stowage's MCP surface is deliberately six tools; additions require an
RFC amendment.
