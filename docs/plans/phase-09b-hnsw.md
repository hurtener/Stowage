# Phase 09b — HNSW vindex driver (OQ-2 superseded by owner directive)

- **Status:** draft
- **Owning subsystem(s):** `internal/vindex` (new driver), config
- **RFC sections:** §4.2, §8, OQ-2
- **Depends on phases:** 09
- **Informing briefs:** 02 (brute-force-at-scale pain: no ANN index hurt at
  100k+), 06 (benchmark-led positioning — vector-search latency is currency)

## Goal

Owner directive (2026-06-11): the system scales fast — 100k vectors/scope is
too low a ceiling, so **pure-Go HNSW is the default vector search**, not
brute-force. This phase adds an `hnsw` vindex driver (github.com/coder/hnsw
v0.6.1, pure Go, CGo-free) as the default, keeping the BLOB store as the
source of truth (D-046's storage decision stands) and the brute driver as the
**exact-recall oracle** in conformance.

## Brief findings incorporated

- Brief 02: the predecessor's brute-force BLOB scan was its documented
  scalability wall — this phase removes ours before it exists in production.

## Findings I'm departing from

- Supersedes D-046's "brute-force for v1" *algorithm* choice (storage format
  and no-pgvector stance unchanged) — file D-048.

## Design

- **Graphs per tenant** (the hard isolation boundary): `map[tenant]*graph`
  behind an RWMutex per entry (coder/hnsw graphs are not concurrency-safe).
  Node key = memory_id; vectors float32.
- **Build**: lazy on first search/upsert for a tenant — bulk-load from
  `Vectors().Scan` (full tenant), then incremental `Add` on Upsert and
  `Delete` on Delete. No persistence of the graph itself in this phase:
  rebuild-from-BLOBs at boot is O(n·log n) inserts and acceptable; snapshot
  persistence is a later optimization if boot latency ever matters.
- **Filtered search**: HNSW returns nearest by vector only → over-fetch
  `min(k×4+16, 512)` candidates, then filter by sub-scope (project/user/
  session), kinds, and window using a metadata sidecar map maintained on
  upsert (scope cols + kind + created_at per memory_id — no store round-trip
  on the search path), truncate to k. Under-fill after filtering triggers ONE
  refetch at 4× the previous over-fetch before returning what exists.
- **Recall conformance**: seeded corpus (1k vectors, mixed scopes), assert
  recall@10 ≥ 0.95 vs the brute driver on 50 random queries (fixed seed) —
  the brute driver stays for exactly this.
- **Selection**: `vindex.driver` config key (`hnsw` default | `brute`),
  registry pattern like store/gateway. The retrieval stage is
  driver-oblivious.
- **Tuning**: M=16, EfSearch=64 as profile-internal constants (knob
  guardrail); revisit with Phase 13 benchmarks.

## Files added or changed

```text
internal/vindex/hnsw/{driver.go, meta.go, driver_test.go}
internal/vindex/{registry wiring}
internal/vindex/conformance/ (recall oracle case + runs against BOTH drivers)
internal/config (vindex.driver key + validation + explain)
cmd/stowage/main.go (driver selection)
scripts/coverage.json (vindex/hnsw 85)
scripts/smoke/phase-09b.sh (serve boots with hnsw default; retrieve works)
go.mod (+ github.com/coder/hnsw v0.6.1)
docs/decisions.md (D-048), RFC OQ-2 footnote update
```

## Acceptance criteria (binding)

1. Recall@10 ≥ 0.95 vs brute oracle on the seeded corpus (both drivers run
   full vindex conformance; isolation cases pass on hnsw via per-tenant
   graphs + sidecar filtering — cross-tenant AND cross-user).
2. Upsert-replace and Delete reflected in subsequent searches (no stale
   hits; tombstone correctness).
3. Lazy build: first search after boot returns identical results to
   post-incremental state (rebuild test).
4. Filtered search under-fill triggers refetch (test with a kind filter
   matching 1-of-100).
5. Concurrent searches + upserts race-clean (`-race`, ≥8 goroutines).
6. Benchmark: 100k×768d corpus — hnsw Search ≥ 20× faster than brute at
   recall ≥ 0.95 (recorded in the bench output, not a CI gate).
7. `vindex.driver` validated (unknown → boot error with key path); explain
   shows it; default hnsw.
8. Coverage ≥ 85 on the new package; smokes 01–09b green.

## Risks & mitigations

- coder/hnsw API drift → pinned v0.6.1; driver isolates it (P5-style seam
  discipline).
- Memory footprint of per-tenant graphs + sidecar → bench memory at 100k;
  documented; eviction of idle tenant graphs is a later phase if needed.
- Deletion support in HNSW is tombstone-based → verify library semantics;
  if hard-delete unsupported, maintain a deleted-set filter + periodic
  rebuild threshold (document choice in code).

## Decisions filed

- D-048: HNSW (coder/hnsw, pure Go) is the default vector search per owner
  directive; brute-force remains as the conformance oracle and debug driver;
  BLOB storage + no-pgvector (D-046) unchanged. Supersedes D-046's
  algorithm-default only.
