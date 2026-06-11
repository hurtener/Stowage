# Brief 01 — The Python predecessor

> Context-only. Clean-room: no files from the predecessor are reproduced here;
> this brief records architecture and lessons from a code review of it
> (2026-06-10). Its project name is not used in this repository (D-001).

## Shape

FastAPI service, ~76k lines / ~194 files, SQLAlchemy 2.0 async over
Postgres (pgvector) or sqlite, FAISS in-memory per-tenant indexes as the
alternative vector path, local SentenceTransformers embeddings (~300 MB
resident) plus a local CrossEncoder reranker, LiteLLM + DSPy for extraction/
classification, a custom async DAG-orchestration framework for flows, and
polling-based background workers (embedding queue polled at 0.25 s; nightly
"sleep cycle" consolidation scheduler with jittered intervals and semaphores).
48+ tables, 88 migrations, 50+ REST endpoints.

## What works well (keep the ideas)

- **Hybrid retrieval**: BM25 + vector with weighted fusion, optional rerank,
  privacy-zone and confidence filters, token-budget packing.
- **Privacy zones** (`public/work/personal/intimate`) orthogonal to scope.
- **Confidence as a composed value** (user feedback + LLM assessment + semantic
  overlap), snapshot history.
- **Knowledge kinds** (fact / preference / assertion) with truthiness
  (`verified | asserted | derived | inferred`).
- **Consolidation jobs**: temporal rollup, dedupe with multi-signal scoring
  (truthiness, reinforcement, recency, confidence, similarity), decay, digest;
  idempotency via job markers.
- **Importance reinforcement**: EWMA boost on positive feedback.
- **Audit events** for every lifecycle action (ingested, reinforced, shared,
  revoked, decayed…).
- **Explicit assertion API**: users can override extraction; a validation
  dispatcher catches contradictions.

## What hurts (fix in Stowage)

- **Runtime fights the design**: GIL workarounds (thread-pool executors around
  FAISS, per-tenant lock dicts), `asyncio.to_thread` around blocking model
  loads, defensive wrappers around driver cancellation quirks.
- **Polling workers**: the embedding queue polls; Go channels make this free.
- **Local models**: heavy cold start, memory residency, GPU/CPU pressure,
  version skew — the single biggest deployment liability. → gateway seam (D-005).
- **Schema sprawl**: 48+ tables, many for features with thin usage (federation
  graphs, triage queues). → ~12-table budget (D-009).
- **Custom orchestration framework** for what are, in Go, plain goroutine
  stages with supervision.
- **Manual idempotency tracking** in app code where DB constraints suffice.
- **Surface sprawl**: 50+ endpoints; high doc and test burden. → small surface
  (D-015).

## Performance notes observed

- Ingest ACK is coupled to request-time classification work; Stowage moves all
  of it post-ACK (P2).
- Per-tenant FAISS index rebuilds and lock contention dominate tail latency at
  multi-tenant load.
- DSPy/JSON re-parsing of LLM output is a recurring failure source → schema-
  constrained calls only (CLAUDE.md §10).
