# Changelog

All notable changes to Stowage are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed

- Adversarial scope review (D-033–D-036): plan restructured into a 21-phase
  launch track (every differentiator + proof) and post-launch tracks v1.1–v1.3
  (episodic, trust extensions, proactive); eval pulled forward as its own wave
  with a CI benchmark gate (LongMemEval/LoCoMo/ConvoMem/MemBench + gain + SLO)
  and a launch-day competitor comparison report; configuration redesigned
  around the five-minute rule (zero-config start, profiles, runtime knobs,
  `config explain`, knob guardrail); gateway-free degraded retrieval and
  temporal-proximity boosting adopted from the mempalace review (brief 06).

### Added

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
