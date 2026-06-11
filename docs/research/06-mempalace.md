# Brief 06 — mempalace (github.com/mempalace/mempalace)

> Public source, reviewed 2026-06-11. An open-source, local-first Python memory
> system organized on the method-of-loci metaphor (wings/rooms/drawers). Most
> valuable to Stowage as (a) independent vindication of fidelity-first, (b) the
> model for benchmark-led positioning, (c) two concrete techniques.

## What it is

Python (ChromaDB default; pluggable sqlite/qdrant/pgvector backends), local
embeddings (~300 MB), verbatim "drawers" with **no summarization** —
"preserves original content integrity without lossy extraction." Hierarchical
scoping (wings = people/projects, rooms = topics) narrows search before it
runs. Temporal entity-relationship graph with validity windows. 29 MCP tools.
Env-driven config with pip extras.

## The headline lesson: benchmarks ARE the marketing

mempalace's front page is its numbers, with committed per-question result
files and reproduction commands in `benchmarks/BENCHMARKS.md`:

- **LongMemEval (500 q):** 96.6 % R@5 raw semantic — *zero API calls*;
  98.4 % hybrid (held-out 450 q); ≥99 % with LLM rerank.
- **LoCoMo:** 60.3 % R@10 session mode; 88.9 % hybrid.
- **ConvoMem:** 92.9 % average recall (250 items).
- **MemBench (ACL 2025):** 80.3 % R@5 (8,500 items).

Implication for Stowage (D-035): our eval suite must cover the same public
benchmarks (LongMemEval, LoCoMo, ConvoMem, MemBench) plus our gain harness and
SLO, ship **at launch** with committed per-question results and reproduction
commands, and publish a comparison table against competitors' published
numbers. A memory server without numbers on its front page loses by default.

## Vindications

- **Verbatim-first works.** 96.6 % R@5 with *raw retrieval over verbatim
  content and no LLM anywhere* is strong evidence for P1 and for CL-Bench's
  fidelity finding — much of retrieval quality comes from not destroying the
  signal at write time.
- **Scoped narrowing beats corpus-wide search.** Their wings/rooms hierarchy is
  our scopes + topics + structured lanes; same instinct, confirms the design.
- **Temporal validity windows** on graph edges — we have validity windows on
  memories and links already.

## Techniques to adopt

1. **Temporal-proximity boosting** (their hybrid v4/v5 ingredient): score
   candidates higher when their `occurred_at` is near the query's implied or
   explicit time window — finer than recency. → a Phase 10 scoring input.
2. **Zero-LLM degraded retrieval.** Their raw mode needs no API at all. Stowage
   bans local models (P5), but the lexical, anticipated-queries, and structured
   lanes need no gateway either: retrieval must **degrade gracefully to a
   gateway-free mode** (embedded/desktop offline, gateway outage) instead of
   failing. → read-path requirement (D-036).

## Counter-examples (what not to copy)

- **29 MCP tools** — the surface-sprawl pattern both predecessors fell into;
  Stowage stays at seven (D-015/D-018).
- **Local embedding models** — their privacy-first tradeoff, our deployment
  liability (P5, brief 01). Our embedded mode + offline lexical degradation
  recovers most of the local-first story without shipping models.
- **Python + per-install extras** — Stowage's single static binary is the
  differentiator here; keep it absolute.
