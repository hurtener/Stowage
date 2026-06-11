# Stowage

**Memory infrastructure for agentic systems.** A single CGo-free Go binary that
remembers, reconciles, retrieves — and forgets.

```text
Portico  — the MCP gateway        (connects and governs tools)
Harbor   — the agent framework    (builds and runs agents)
Dockyard — the MCP Apps framework (builds the MCP servers users touch)
Stowage  — memory infrastructure  (this project)
```

Stowage ingests raw interactions with a fire-and-forget API, asynchronously
extracts structured memories guided by configurable **topics**, actively
**reconciles** new information against what it already knows (update,
supersede, merge, forget — reversibly), and serves **hybrid retrieval**
(lexical + vector + structured, fused) with utility-driven ranking and
**provenance drill-down** to the verbatim source. Fleets self-improve through
built-in **reflection and playbooks** (ACE): task outcomes become shared
strategies, assembled deterministically into an evolving context, shareable
across a team via **grants**. Embeddings and LLM calls go through one
provider-agnostic gateway seam (Bifrost first) — no local models, no workers,
no CGo.

Runs as a standalone binary (HTTP + MCP + CLI) over Postgres, or fully
embedded in a host process over pure-Go sqlite — same code, same seams.

## Status

Pre-implementation. The design RFC and the master phase plan are complete; code
lands phase by phase.

- **Design:** [`RFC-001-Stowage.md`](RFC-001-Stowage.md)
- **Plan:** [`docs/plans/README.md`](docs/plans/README.md) (28 phases, 9 waves)
- **Working norms:** [`CLAUDE.md`](CLAUDE.md) / `AGENTS.md` (binding,
  mirrored)
- **Research briefs:** [`docs/research/INDEX.md`](docs/research/INDEX.md)
- **Decisions:** [`docs/decisions.md`](docs/decisions.md)

## Build

```bash
make build      # CGo-free static binary at bin/stowage
make test       # go test -race ./...
make preflight  # build + smoke checks + drift-audit (the merge gate)
```

## License

Private repository; license to be decided before any publication (D-013).
