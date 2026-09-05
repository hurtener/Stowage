# Changelog

All notable changes to Stowage are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- Ordinary agents now use a five-tool memory catalog with task-oriented guidance,
  constrained schemas, and evidence/warning-preserving read projections. Connect
  planners to `/mcp/agent` (shared HTTP) or `/agent` (dedicated MCP); the existing
  full endpoint remains for runtime/curator integrations. Stdio defaults to the
  agent catalog; `--catalog full` preserves the integration catalog. Harbor's
  `Tools` also defaults to five tools; `LegacyTools` retains its old catalog.
  See `docs/agent-memory.md` for required connection/source-binding migration.
- Go Client implementations now include `Remember` and `Correct`. Standard HTTP
  and embedded clients implement both; custom implementations must add them.
- Adversarial scope review (D-033–D-036): plan restructured into a 21-phase
  launch track (every differentiator + proof) and post-launch tracks v1.1–v1.3
  (episodic, trust extensions, proactive); eval pulled forward as its own wave
  with a CI benchmark gate (LongMemEval/LoCoMo/ConvoMem/MemBench + gain + SLO)
  and a launch-day competitor comparison report; configuration redesigned
  around the five-minute rule (zero-config start, profiles, runtime knobs,
  `config explain`, knob guardrail); gateway-free degraded retrieval and
  temporal-proximity boosting adopted from the mempalace review (brief 06).

### Added

- Source-backed remember/correct commands across MCP, HTTP and SDK. Exact owned
  user quotations carry byte-span provenance; corrections require inspected
  revisions and retain reversible history. SQLite/Postgres transactions atomically
  commit effects and durable idempotency receipts. Replay reports current status
  without reviving deleted/superseded memories; rank/indexing readiness is not
  promised. Direct assertion is not the implementation shortcut.
- Pinned pre-change MCP/service baseline, agent-use scenario protocol, actual
  catalog/schema goldens, cross-surface command tests, restart/concurrency tests,
  and `scripts/smoke/phase-ae13.sh`. Live-model selection rates are not measured.
- Explicit forgetting/retention documentation. No incomplete selective-forgetting
  tool is exposed, and derived-item deletion is not represented as erasure.
- Roadmap integration: day-one signal-capture schema (injections, links,
  episodes, branches, suggestions, runtime API keys — RFC §5.0/§8.1), episodic
  & temporal memory (§6b), trust layer — citations, verification, reasoning
  traces, review queue (§6c), proactive memory with governance (§6d), branches,
  hot–warm cache + read-path SLO, zero-config agent wiring + Python client;
  master plan expanded to 28 phases across 9 waves (D-024–D-032).
- Design RFC (`RFC-001-Stowage.md`), master phase plan (20 phases, 5 waves),
  research briefs 01–05, decisions log D-001–D-023, glossary, binding
  contributor norms (`CLAUDE.md`/`AGENTS.md`), and the build/preflight/
  drift-audit scaffolding.
- RFC amendments: team sharing via grants (§5.3), reversible reconciliation +
  rollback (§6), ACE built-ins — outcomes, reflection, deterministic playbooks
  (§6a), Postgres as principal store with sqlite as the embedded driver
  (§8.1), Harbor protocol-not-runtime integration (§10), Dockyard-built MCP
  surface + post-v1 console App (§9.2), SOTA-gated open-source strategy (§12).
