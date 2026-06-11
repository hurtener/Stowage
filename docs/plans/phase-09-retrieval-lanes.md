# Phase 09 — Retrieval lanes + fusion

- **Status:** draft
- **Owning subsystem(s):** `internal/vindex`, `internal/retrieval`,
  `internal/store` (migration 0003 + lexical/vector methods), embed wiring
- **RFC sections:** §4.2 (read path steps 1–3, degraded mode), §8 (vindex),
  OQ-2, D-036, D-038
- **Depends on phases:** 03, 04, 08
- **Informing briefs:** 01 (hybrid BM25+vector fusion), 02 (anticipated-queries
  as a separate fused lane; enriched embedding text), 06 (gateway-free
  degraded retrieval; scoped narrowing)

## Goal

`POST /v1/retrieve` exists: four lanes run concurrently — lexical, vector,
anticipated-queries, structured — with native time-window filters, fused by
RRF. Memories get embeddings (enriched text) asynchronously after commit, with
a backfill sweep for pre-Phase-09 rows (D-038). The gateway being down
degrades retrieval to the three gateway-free lanes with a flag — never an
error (D-036). Scoring/budgeting are Phase 10/11; this phase returns the fused
candidate list (API marked v0-unstable in the response envelope).

## Brief findings incorporated

- Brief 02: embed `content + entities + keywords + anticipated_queries`
  (enriched text), not raw content; anticipated-queries get their own lexical
  lane fused separately.
- Brief 06: degraded mode is a feature, not a failure path.
- OQ-2 resolved: sqlite vector search is brute-force first (exact, correct to
  ~100k vectors/scope); pure-Go HNSW arrives behind the same seam only when
  eval scale demands (file the decision).

## Findings I'm departing from

- None.

## Design

### Migration 0003 (both dialects) + CI image

- `memory_vectors(memory_id PK→memories, tenant_id, project_id, user_id,
  session_id, model TEXT, dims INTEGER, vec <BLOB|vector>)`, scope cols
  mirrored for isolation; postgres uses `vector(dims)`? — dims vary by config:
  use `vector` untyped column with dims checked in code OR pin dims at
  migrate time? **Decision: store as `BYTEA`/BLOB float32-LE in BOTH dialects
  for v1** — uniform semantics (D-037 spirit), brute-force scan in the driver;
  pgvector-native column+HNSW is a later vindex driver upgrade behind the
  seam (record in the decision; avoids dims-pinning in DDL and keeps 0003
  extension-free). **CI stays postgres:17 — no pgvector dependency in v1.**
- Lexical: sqlite — FTS5 virtual table `memories_fts(content, context)` +
  sync triggers on memories; second FTS table `memory_queries_fts(query)` +
  triggers. postgres — generated `tsvector` columns + GIN indexes on memories
  and memory_queries.

### `internal/vindex`

```go
type Index interface {
    Upsert(ctx, scope, memoryID string, vec []float32) error
    Search(ctx, scope, vec []float32, k int, f Filter) ([]Hit, error) // Filter: window, kinds, statuses
    Delete(ctx, scope, memoryID string) error
}
```
Drivers: `sqlitebrute` and `pgbrute` (both scan BLOBs scope-filtered,
cosine in Go — shared kernel; SIMD-friendly loop). Registry + conformance
suite (isolation, dims mismatch rejection, upsert-replace, filter
correctness). Vectors written via the store (new seam methods
`Vectors().Upsert/Search/Delete` implemented per driver — vindex wraps the
store rather than owning a connection).

### Embed wiring

- Commit path: after a successful memory commit (add/update/merge/supersede
  outcomes), reconcile enqueues `(scope, memory_id, enriched_text)` on a
  bounded channel; an **embedder worker** batches via the gateway and upserts
  vectors. Failures dead-letter (stage `embed`) — retrieval still works
  lexically (degraded per-memory, not per-system).
- **Backfill sweep**: job-marker-guarded scan for active memories without
  vectors (joins memory_vectors), batches of 64 through the gateway; runs at
  serve boot and on a jittered ticker. Covers all pre-0003 memories (D-038)
  and embed dead-letters after gateway recovery.

### `internal/retrieval`

`POST /v1/retrieve` `{query, limit≤50, window?{from,to}, kinds?,
include_lanes?}` →
1. Lanes via errgroup (each scope-filtered + window-filtered):
   **lexical** (FTS5 bm25 / ts_rank over content+context),
   **queries** (FTS over anticipated queries),
   **structured** (FindNeighbors on entities/keywords extracted from the
   query — simple tokenization, stop-worded),
   **vector** (gateway Embed the query → vindex Search; SKIPPED with
   `degraded:true` + reason when the gateway errors/breaker open — D-036).
2. **RRF fusion** (k=60) over lane rankings; per-item lane provenance kept
   (`lanes: ["lexical","vector"]` — Phase 11 injections will persist it).
3. Response: `{items:[{memory(id,kind,content,context,importance,confidence,
   created_at), score, lanes}], degraded, api:"v0"}` — explicitly unstable
   until Phase 11 finalizes the envelope (citation handles etc.).
- `status='active'` only; `match_count` incremented per returned memory
  (existing IncrementCounter; async, non-blocking).

### Live validation

`-tags=live` test: real embeddings via OpenRouter `google/gemini-embedding-2`
(STOWAGE_TEST_OPENROUTER_KEY; dims 3072 — config sets embed_dims accordingly
in the test): embed two related + one unrelated text, assert related pair
ranks above unrelated in vindex search. Not CI; not preflight.

## Files added or changed

```text
internal/vindex/{vindex.go, registry.go, brute.go, conformance/, drivers...}
internal/retrieval/{retrieval.go, lanes.go, fuse.go, tokenize.go, retrieval_test.go}
internal/reconcile/  (embed enqueue hook)
internal/pipeline or internal/retrieval (embedder worker — put with vindex)
internal/store/{store.go, types.go} (Vectors() seam + lexical search methods)
internal/store/migrations/{sqlite,postgres}/0003_vectors_fts.sql
internal/store/{sqlitestore,pgstore}/(vectors.go, lexical.go)
internal/api/retrieve_handler.go
cmd/stowage/main.go (embedder + backfill wiring)
scripts/coverage.json (vindex 85, retrieval 85)
scripts/smoke/phase-09.sh
```

## Config keys added

None top-level (embedder batch/interval, backfill batch, RRF k are profile
constants — knob guardrail).

## Acceptance criteria (binding)

1. Migration 0003 applies on both dialects; FTS sync proven (insert/update/
   delete memory ⇒ FTS reflects it; conformance on both drivers).
2. vindex conformance: scope isolation (cross-tenant AND cross-user), dims
   mismatch rejected, upsert-replace, window/kind filters.
3. Lane correctness fixtures: a term-match memory tops lexical; a paraphrase
   tops vector (mock embeddings are sha-seeded — craft fixtures via identical
   strings); an anticipated-query phrasing tops the queries lane; an
   entity-overlap memory tops structured.
4. RRF: an item ranked mid in two lanes outranks an item top in one lane only
   (golden fusion test).
5. Time-window filters exclude out-of-window memories on EVERY lane (table
   test).
6. Degraded mode: gateway breaker open ⇒ 200 with `degraded:true`, vector
   lane absent, other lanes intact (test); ingest unaffected.
7. Commit→embed→searchable: integration test (mock gateway) — committed
   memory becomes vector-searchable; backfill embeds a pre-existing
   vector-less memory (test seeds one directly).
8. Embed failures dead-letter (stage `embed`) and the backfill recovers them
   after the gateway heals (test scripts mock failure then success).
9. `match_count` bumps on returned memories (test).
10. Coverage ≥ 85 vindex + retrieval; all `-race`; smokes 01–09 green.

## Smoke script

phase-09.sh: serve (mock gateway, lazy script); seed two memories via the
Phase-08 e2e path; retrieve by exact term (lexical hit), by anticipated-query
phrasing, with a time window excluding one; assert degraded:false; flip to a
breaker-open scenario is unit-level (not smoked).

## Test plan

Conformance (store lexical/vector methods + vindex); lane fixtures; fusion
goldens; degraded-mode tests; integration commit→search; fuzz on the retrieve
request decoder; benchmarks: BenchmarkVectorScan (10k vectors), BenchmarkRRF.

## Risks & mitigations

- FTS5 trigger drift vs memories writes → triggers in-migration + conformance
  sync test on both dialects.
- Brute-force scan cost at scale → benchmark now; HNSW driver behind the seam
  when eval says so (the seam is the point).
- 3072-dim default from gemini-embedding-2 is heavy (12 KiB/vector) → config
  already pins embed_dims; live test uses 3072; mock tests use small dims.

## Glossary additions

- **Enriched text** — the embed input: content + entities + keywords +
  anticipated queries (brief 02).
- **Backfill sweep** — the job-marker-guarded embedder pass for vector-less
  active memories.

## Decisions filed

- D-046: vectors stored as float32-LE BLOB in both dialects; brute-force
  cosine in Go for v1 (OQ-2 resolved); pgvector-native/HNSW are future vindex
  drivers behind the seam; CI needs no pgvector image.
- D-047: enriched-text embedding; embeds are async post-commit with
  dead-letter + backfill recovery (never block commit).
