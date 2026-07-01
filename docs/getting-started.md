<p align="center">
  <img src="assets/stowage-mark.svg" alt="Stowage" width="84">
</p>

# Getting started with Stowage

This is the full version of the five-minute quickstart: from a fresh checkout to your first
real, retrievable memory — with the *why* behind each step, the configuration surface, and the
two ways to run Stowage (embedded or standalone). If you just want the three commands, the
[README quickstart](../README.md#five-minute-quickstart) has them.

- [Prerequisites](#prerequisites)
- [1 · Build](#1--build)
- [2 · The one secret](#2--the-one-secret)
- [3 · Serve](#3--serve)
- [4 · Teach it what to remember (topics)](#4--teach-it-what-to-remember-topics)
- [5 · Ingest → extract → retrieve](#5--ingest--extract--retrieve)
- [6 · Drill down, give feedback, correct](#6--drill-down-give-feedback-correct)
- [Configuration](#configuration)
- [Choosing a backend](#choosing-a-backend-sqlite-or-postgres)
- [Using it from MCP](#using-it-from-mcp)
- [Embedding it in Go (the SDK)](#embedding-it-in-go-the-sdk)
- [Troubleshooting](#troubleshooting)
- [Where to go next](#where-to-go-next)

## Prerequisites

- **Go 1.26+** (the build is CGo-free; no system libraries needed).
- **An LLM/embedding provider key.** Stowage never runs models locally — every embedding and
  completion goes through one gateway seam. Any OpenAI-compatible provider works; the default
  driver is **Bifrost** (which fronts OpenAI, Anthropic, OpenRouter, and more on one key).
- `curl` + `jq` for the walkthrough below (optional).

## 1 · Build

```bash
git clone https://github.com/hurtener/Stowage && cd Stowage
make build        # → bin/stowage  (one CGo-free static binary)
bin/stowage version
```

## 2 · The one secret

Stowage's zero-config start needs exactly one secret: your provider key. The default config
resolves the gateway key from `env.STOWAGE_GATEWAY_API_KEY`, so just export it:

```bash
export STOWAGE_GATEWAY_API_KEY=sk-...
```

Secrets are always referenced indirectly (`env.VAR`) and validated at boot — Stowage fails loud
on a missing or malformed secret rather than starting half-configured. Keys are never logged.

## 3 · Serve

```bash
bin/stowage serve
```

That's it — no config file. You get:

- the **HTTP API** on `:7160`,
- a **SQLite** store in a local file (pure-Go, CGo-free) — swap in Postgres later with one config
  block, no code change.

The **MCP tool surface is opt-in**: `serve` binds exactly one port by default. Set
`server.mcp_listen` (e.g. `:7161`) to co-mount MCP-over-HTTP on a second listener over the same
process and key policy, or run `stowage mcp` standalone. `serve` logs a one-line hint when MCP is
off so you know the knob exists.

Confirm what you're running:

```bash
bin/stowage config explain     # effective config + where each value came from
curl -s localhost:7160/healthz
```

## 4 · Teach it what to remember (topics)

Extraction is **topic-gated**: Stowage only distills memories that match an active *topic* (a
natural-language description of what's worth keeping). This is the noise filter — a candidate that
matches no topic is never created.

You get sensible defaults out of the box (the assistant profile ships a `preferences` pack), but
for anything real you'll define topics per scope. First mint a key, then set topics:

```bash
# Bootstrap an admin key (the first key needs no auth; subsequent ones do).
ADMIN=$(curl -s localhost:7160/v1/admin/keys \
  -d '{"tenant_id":"acme","role":"admin"}' | jq -r .plaintext)

# An agent key for day-to-day ingest/retrieve.
KEY=$(curl -s localhost:7160/v1/admin/keys -H "Authorization: Bearer $ADMIN" \
  -d '{"tenant_id":"acme","role":"agent"}' | jq -r .plaintext)

# Topics = extraction magnets. You can also enable compiled-in PACKS with the
# `pack:on:<name>` sentinel — e.g. {"key":"pack:on:project"} — and they compose
# with your explicit topics.
curl -s localhost:7160/v1/topics -X PUT -H "Authorization: Bearer $KEY" -d '[
  {"key":"preferences","description":"tools, formats, frameworks, and working style the user prefers"},
  {"key":"decisions","description":"technical decisions made and the reason why"},
  {"key":"gotchas","description":"pitfalls and lessons learned worth avoiding next time"}
]'
```

## 5 · Ingest → extract → retrieve

Ingest is **fire-and-forget**: the API acknowledges as soon as the raw record is durable;
extraction, embedding, and reconciliation happen asynchronously in supervised in-process stages.

```bash
# Ingest raw turns into a per-session buffer.
curl -s localhost:7160/v1/records -X POST -H "Authorization: Bearer $KEY" -d '{
  "records":[
    {"role":"user","content":"We chose PostgreSQL over DynamoDB for the orders service because we need multi-row ACID transactions.","session_id":"s1","buffer_key":"s1"}
  ]}'

# Flush the buffer to trigger extraction now (otherwise it flushes on size/age).
curl -s localhost:7160/v1/buffers/s1/flush -X POST -H "Authorization: Bearer $KEY" -d '{"trigger":"explicit"}'
```

Give the pipeline a moment (real extraction is an LLM call), then retrieve:

```bash
curl -s localhost:7160/v1/retrieve -X POST -H "Authorization: Bearer $KEY" -d '{
  "query":"why did we pick our orders database?","limit":5
}' | jq
```

You get back a `decision`-kind memory — a self-contained statement ("The team chose PostgreSQL
over DynamoDB for the orders service because multi-row ACID transactions were required."), with a
relevance score, a **citation handle**, and anticipated queries. Retrieval fuses lexical, vector,
anticipated-query, and structured lanes; it returns a `support` summary and degrades gracefully
(lexical + structured) if the provider is briefly unreachable.

## 6 · Drill down, give feedback, correct

- **Drill down to the source.** Every memory cites the verbatim record span it came from:

  ```bash
  curl -s localhost:7160/v1/drilldown -X POST -H "Authorization: Bearer $KEY" \
    -d '{"citation":"<citation-handle-from-retrieve>"}' | jq
  ```

- **Give feedback.** `use` / `save` / `fail` / `noise` / `wrong_citation` tune ranking over time:

  ```bash
  curl -s localhost:7160/v1/feedback -X POST -H "Authorization: Bearer $KEY" \
    -d '{"citation":"<handle>","signal":"use"}'
  ```

- **Correct it, and watch it forget.** Ingest a contradiction and Stowage *reconciles*: the new
  decision **supersedes** the old one (reversibly — the chain and the event survive).

  ```bash
  curl -s localhost:7160/v1/records -X POST -H "Authorization: Bearer $KEY" -d '{
    "records":[{"role":"user","content":"Correction: we are migrating the orders service off PostgreSQL onto DynamoDB; the transaction requirement was dropped.","session_id":"s2","buffer_key":"s2"}]}'
  curl -s localhost:7160/v1/buffers/s2/flush -X POST -H "Authorization: Bearer $KEY" -d '{"trigger":"explicit"}'
  # A later retrieve now returns the DynamoDB decision; the Postgres one is superseded, not deleted.
  ```

## Configuration

The whole point of the five-minute rule is that you rarely need config. When you do, it's a small,
documented surface — there is no 50-knob maze.

- **Profiles** select sensible defaults for a deployment shape: `assistant` (single user),
  `coding-agent`, `fleet`. The profile picks default topic packs, extraction budgets, and scoring
  posture. Set it with `profile: <name>` in the config file.
- **`stowage config explain`** prints the effective config *and the provenance of every value*
  (default · profile · scope · env) — your first stop when something behaves unexpectedly.
- **Config file** (YAML) is optional; pass it with `--config`. Example:

  ```yaml
  server:  { listen: ":7160", mcp_listen: ":7161" }
  store:   { driver: sqlite, dsn: "stowage.db" }
  gateway:
    driver: bifrost
    provider: openrouter
    base_url: "https://openrouter.ai/api"
    api_key: env.STOWAGE_GATEWAY_API_KEY
    model: "google/gemini-2.5-flash"
    embed_model: "perplexity/pplx-embed-v1-0.6b"
    embed_dims: 1024
  profile: assistant
  ```

- **Retrieval profile tuning** (`retrieval:`) is optional and rarely needed — the three named
  retrieval profiles (`precise`, `balanced`, `broad`) ship tuned windows. When you *do* want more
  retrieved memories per query, raise a profile's windows. Memories are compressed (~30–40 tokens
  each), so a larger result set is cheap context — on LongMemEval, lifting the rerank (`precise`)
  profile from 10 to 30–50 scored candidates roughly doubled retrieval recall at a few hundred extra
  tokens. Each field is optional and inherits the built-in preset when omitted:

  ```yaml
  retrieval:
    precise:  { lane_k: 60, scoring_k: 30, default_limit: 10 }   # rerank a deeper window
    # balanced / broad inherit their presets unless set
  ```

  - **`lane_k`** — candidates fetched per lane before fusion.
  - **`scoring_k`** — fused candidates scored/reranked. This is the cap on how many memories can
    reach the reader; the per-request `limit` is floored up into this window, so asking for
    `limit: 25` always scores at least 25 (never silently clamped below the request).
  - **`default_limit`** — result count when a `/v1/retrieve` call omits `limit` (hard cap: 50).

  `retrieval.include_superseded` (default `true`) controls **dual-visibility**: when a fact is
  corrected, the retired value is kept retrievable but flagged `stale` with a `superseded_by` link to
  the current value, so an agent can reason about the change ("you said X, then Y") instead of silently
  losing the history. Set it `false` for active-only retrieval.

Every config key ships with a tuned default and is documented; a new knob without all three never
ships (the "knob guardrail").

## Choosing a backend (SQLite or Postgres)

| | SQLite (default) | Postgres |
|---|---|---|
| **For** | single-user agents, local dev, embedded use | multi-tenant, team-shared, production |
| **Driver** | `modernc.org/sqlite` (pure-Go, CGo-free) | `pgx` |
| **Config** | `store: { driver: sqlite, dsn: "stowage.db" }` | `store: { driver: postgres, dsn: "postgres://…" }` |

Both pass the *same* conformance suite — neither is "the real one"; the seam is the contract.
Migrations are forward-only: `stowage migrate --config <file>`.

## Using it from MCP

`stowage serve` can co-mount an MCP tool surface (opt-in via `server.mcp_listen`), or run
`stowage mcp` standalone. Point any MCP host (Claude, Cursor, a Harbor agent) at it and the memory
tools — ingest, retrieve, drill-down, feedback, topics, episodes, playbook, verify — appear as
first-class tools, with the same behavior and scope enforcement as the HTTP API (one core,
parity-tested).

## Embedding it in Go (the SDK)

For single-user agents you can skip the server entirely and run the whole pipeline in-process:

```go
import stowage "github.com/hurtener/stowage/sdk/stowage"

client, _ := stowage.NewEmbedded(ctx, cfg)   // pure-Go SQLite, same seams
client.Ingest(ctx, records)
res, _ := client.Retrieve(ctx, stowage.Query{Text: "…", Limit: 5})
```

The SDK exposes the same capabilities as the HTTP and MCP surfaces; switch to the HTTP client mode
later without changing your call sites.

## Troubleshooting

- **`gateway probe failed` / 401 at boot or first call** → check `STOWAGE_GATEWAY_API_KEY` and the
  `provider` / `base_url` / `model` triple. `stowage config explain` shows what resolved.
- **Retrieve returns nothing after ingest** → extraction is async; flush the buffer and allow a
  moment. If still empty, confirm a **topic matches** the content (`GET /v1/topics`) — topic-gated
  extraction drops anything that matches no active topic by design.
- **`degraded: true` in a retrieve response** → the provider was unreachable; Stowage served the
  lexical + structured lanes. It's a soft failure, not an error.
- **Everything 401s** → the first key is a bootstrap (no auth); every subsequent admin call needs
  an admin Bearer token.

## Where to go next

- **[RFC-001](../RFC-001-Stowage.md)** — the complete design (pipeline, reconciliation,
  retrieval, forgetting, grants, episodes, trust, proactive).
- **[Benchmarks](../README.md#benchmarks)** + **[`eval/REPORT.md`](../eval/REPORT.md)** — reproduce
  the numbers yourself.
- **[Glossary](glossary.md)** — every Stowage term, defined.
- **[Contributing](../CONTRIBUTING.md)** — the binding contributor norms.
