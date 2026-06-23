<p align="center">
  <img src="docs/assets/stowage-logo.svg" alt="Stowage" width="380">
</p>

<p align="center">
  <strong>Memory infrastructure for agentic systems.</strong>
</p>

<p align="center">
  Remembers, reconciles, retrieves — and forgets. One CGo-free Go binary.
</p>

<p align="center">
  <a href="https://github.com/hurtener/Stowage/actions/workflows/ci.yml"><img src="https://github.com/hurtener/Stowage/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/go-1.26-00ADD8.svg" alt="Go 1.26">
  <img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="Apache-2.0">
  <img src="https://img.shields.io/badge/CGo-free-0E2A47.svg" alt="CGo-free">
  <img src="https://img.shields.io/badge/surfaces-HTTP%20%C2%B7%20MCP%20%C2%B7%20SDK%20%C2%B7%20CLI-34D3C0.svg" alt="surfaces">
</p>

---

Most memory layers recall conversation history. **Stowage manages a memory's whole life** — it
captures raw interactions verbatim, distills durable memories from them, *reconciles* new
information against what it already knows (update, supersede, merge), serves hybrid retrieval
that always drills back to the source, and **forgets** on purpose through decay, dedupe, and
supersede gates. It runs as one static binary over Postgres, or embeds in your Go process over
pure-Go SQLite — same code, same guarantees.

Stowage is the memory tier of a four-part Go-native agent ecosystem:

```text
Portico  — the MCP gateway        connects and governs tools
Harbor   — the agent framework    builds and runs agents
Dockyard — the MCP Apps framework builds the MCP servers users touch
Stowage  — memory infrastructure  remembers, reconciles, retrieves, forgets   ← you are here
```

**[Quickstart](#five-minute-quickstart) · [How it works](#how-stowage-thinks) · [Surfaces](#four-surfaces-one-core) · [Benchmarks](#benchmarks) · [Deploy](#deploy) · [Docs](#documentation)**

---

## Why Stowage

Five properties are binding — every line of code upholds them, and the design rejects changes
that weaken any one:

- **Fidelity first.** Every raw interaction is appended verbatim and is durable; every derived
  memory carries provenance back to the exact source span. Nothing is silently lost, and you can
  always drill down to *why* a memory exists.
- **Fire-and-forget writes.** Ingest acknowledges the moment the verbatim record is durable.
  Extraction, embedding, and reconciliation are asynchronous, supervised, in-process stages — no
  external workers, no queue to stand up.
- **Scopes enforced at write *and* read.** Identity (`tenant / project / user / session`) is
  enforced in the store layer. There is no unscoped query API to misuse.
- **Memory must forget.** Reconciliation, decay, supersede gates, and quarantine are first-class
  subsystems — not a TODO. A correction supersedes the stale fact (reversibly); noise decays out.
- **No local models; one intelligence seam.** Every embedding and LLM call goes through a single
  provider-agnostic gateway. The shipped binary is CGo-free and model-free.

## Five-minute quickstart

One binary, one secret, a round-trip.

```bash
# 1. Build the CGo-free binary
make build                      # → bin/stowage

# 2. Point it at any OpenAI-compatible / Bifrost provider — one env var
export STOWAGE_GATEWAY_API_KEY=sk-...      # the only required secret

# 3. Serve (HTTP + a co-mounted MCP listener). SQLite by default — zero config.
bin/stowage serve
```

```bash
# 4. Mint a key, teach it what to remember, ingest, retrieve.
KEY=$(curl -s localhost:7160/v1/admin/keys -d '{"tenant_id":"acme","role":"admin"}' | jq -r .plaintext)

curl -s localhost:7160/v1/topics -X PUT -H "Authorization: Bearer $KEY" \
  -d '[{"key":"preferences","description":"the user'\''s tools, formats, and working style"}]'

curl -s localhost:7160/v1/records -X POST -H "Authorization: Bearer $KEY" \
  -d '{"records":[{"role":"user","content":"I always format Python with Black, line length 100.","session_id":"s1","buffer_key":"s1"}]}'
curl -s localhost:7160/v1/buffers/s1/flush -X POST -H "Authorization: Bearer $KEY" -d '{"trigger":"explicit"}'

# … the pipeline extracts + reconciles asynchronously, then:
curl -s localhost:7160/v1/retrieve -X POST -H "Authorization: Bearer $KEY" \
  -d '{"query":"how should I format Python?","limit":5}'
# → a `preference` memory, with a citation that drills down to the verbatim line.
```

`stowage config explain` prints the effective config and where every value came from
(default · profile · scope · env). Full walkthrough: **[docs/getting-started.md](docs/getting-started.md)**.

## How Stowage thinks

```text
  ingest ──▶ records ──▶ buffer ──▶ extract ──▶ reconcile ──▶ commit ──▶ retrieve
 (verbatim,  (durable,   (per       (LLM, gated  (add/update/  (memory +   (lexical +
  ACK fast)   immutable)  scope)     by topics)   supersede/    provenance  vector +
                                                  merge/park)   + events)   structured, fused)
                                       │                                          │
                                  topics & packs                          drill-down to verbatim
                                 (extraction magnets)                      + support / citations
```

- **Topics & packs** are *extraction magnets*: natural-language descriptions of what's worth
  remembering. A candidate that matches no active topic is never created (noise never enters).
  Compiled-in **packs** (`preferences`, `agent-learnings`, `project`, `incidents`, …) **compose** —
  enable several per scope, or layer your own topics on top.
- **Reconciliation** is a constrained decision against the memories a candidate overlaps:
  *add* new knowledge, *update* a refinement, *supersede* a contradiction, *merge* fragments,
  *park* the uncertain for review, or *discard* noise — and every destructive op is reversible
  from its event.
- **Retrieval** fuses lexical, vector, anticipated-query, and structured lanes with utility-driven
  ranking (use/save/fail/noise feedback, decay, hub-dampening), and returns citations that
  **drill down to the verbatim source**. It degrades gracefully when the provider is unreachable.
- **Self-improving fleets (optional):** task outcomes become reusable `strategy` / `failure_mode`
  lessons via reflection, assembled deterministically into an evolving **playbook** (ACE), and
  shareable across a team through **grants**.

## Four surfaces, one core

Every capability is implemented once in the core and exposed through thin, parity-tested surfaces:

| Surface | What it's for |
|---|---|
| **HTTP API** (`stowage serve`) | the service: ingest, retrieve, drill-down, feedback, topics, grants, episodes, admin |
| **MCP** (co-mounted, or `stowage mcp`) | the same memory tools for any MCP host — built with Dockyard |
| **Go SDK** (`sdk/stowage`) | in-process embedding in Harbor, or an HTTP client — same API |
| **CLI** (`stowage …`) | `serve`, `mcp`, `migrate`, `config explain`, `eval` |

## Benchmarks

Stowage is **benchmark-led and reproducible** — the numbers are something you run, not something
we ask you to trust. The eval harness drives the real pipeline (extraction → retrieval → an LLM
reader that answers *only* from retrieved context and abstains otherwise → an LLM judge) against
public memory-QA datasets through the same gateway seam you deploy with.

```bash
stowage eval fetch --dataset longmemeval        # LongMemEval | longmemeval_s | locomo
scripts/eval/longmemeval-50.sh                   # operator-run, judged; results → eval/results/
```

Methodology, datasets, and the latest operator-run numbers live in
**[`eval/REPORT.md`](eval/REPORT.md)**. The eval suite also runs deterministically in CI (no paid
calls) so retrieval quality can't silently regress.

## Deploy

- **Embedded (SQLite, pure-Go, CGo-free).** `import "github.com/hurtener/stowage/sdk/stowage"` and
  run the whole pipeline in your process — ideal for single-user agents and local dev.
- **Standalone (Postgres).** `stowage serve` against Postgres (pgx) for multi-tenant, team-shared
  deployments. The same conformance suite proves both backends behave identically.
- **MCP co-mount.** `serve` exposes the HTTP API and the MCP tool surface from one process and one
  port policy — no second service to operate.

Migrations are forward-only (`stowage migrate`). The event stream (`events/v1`) is a versioned,
consumable audit trail: every memory mutation and lifecycle decision emits an event with its reason.

## Architecture

Pluggable seams — interface + factory + drivers — keep the core honest and the binary portable:

| Seam | Drivers |
|---|---|
| **Store** | `sqlite` (modernc, pure-Go) · `postgres` (pgx) |
| **Vector index** | `pgvector` · `gohnsw` · `brute` |
| **Gateway** (the one intelligence seam) | `bifrost` · `mock` |
| **Events** | typed stream · SSE · Harbor-bus adapter |

Design source of truth: **[`RFC-001-Stowage.md`](RFC-001-Stowage.md)**.

## Documentation

- **[Getting started](docs/getting-started.md)** — the full five-minute onboarding + first real memory
- **[RFC-001](RFC-001-Stowage.md)** — the design, end to end
- **[Decisions log](docs/decisions.md)** · **[Glossary](docs/glossary.md)** — how and why it's built
- **[Contributing](CONTRIBUTING.md)** — the binding contributor norms (`CLAUDE.md` / `AGENTS.md`)
- **[Changelog](CHANGELOG.md)**

## Build & test

```bash
make build       # CGo-free static binary at bin/stowage
make test        # go test -race ./...
make preflight   # build + per-phase smoke checks + drift-audit (the merge gate)
```

## License

[Apache-2.0](LICENSE). Permissive, with an explicit patent grant — built to be adopted and
extended across the Portico / Harbor / Dockyard / Stowage ecosystem.
