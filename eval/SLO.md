# Stowage Retrieval SLO — Reference Results

**Binding target (D-031):** `p99 ≤ 150 ms` at 1 000 concurrent sessions on
the postgres driver on reference hardware (cache hit path ≤ 20 ms).

---

## SLO Rig Description

The rig lives in `internal/bench/slo/` (build tag `slo`). Run it with:

```bash
STOWAGE_TEST_PG_DSN=postgres://... make slo
# or:
go test -tags=slo -v -run TestSLO ./internal/bench/slo/ \
  -slo.dsn "$STOWAGE_TEST_PG_DSN"
```

**What the rig does:**

1. Seeds `--slo.seeds` (default 10 000) memories into a postgres store.
2. Spins up an in-process `httptest.Server` with a single `POST /retrieve` handler
   backed by the full retrieval pipeline (all four lanes, RRF fusion, utility
   scoring, result cache, hot set).
3. Fires `--slo.sessions` (default 1 000) concurrent goroutines, each making
   `--slo.queries` (default 5) retrieve calls with `limit=10` and
   `profile=balanced`.
4. Collects round-trip latencies and cache hit counts.
5. Reports p50/p95/p99 and cache hit rate.

**Rig parameters (tunable via flags):**

| Flag            | Default  | Description                        |
|-----------------|----------|------------------------------------|
| `-slo.dsn`      | env DSN  | Postgres connection string         |
| `-slo.seeds`    | 10 000   | Memories seeded before the run     |
| `-slo.sessions` | 1 000    | Concurrent goroutine count         |
| `-slo.queries`  | 5        | Retrieve calls per goroutine       |
| `-slo.limit`    | 10       | `limit` parameter per retrieve     |

---

## Reference Machine

> **TODO (gate reviewer):** Fill in the machine specs and recorded numbers after
> running `make slo` on the reference hardware.

**Hardware:**

- CPU: _____________
- RAM: _____________
- Storage: _____________
- OS: _____________
- Go version: _____________
- Postgres version: _____________

---

## Recorded Numbers

> **TODO (gate reviewer):** Paste the output of `make slo` here.

```
=== SLO RESULTS ===
requests : <total> (<sessions> sessions × <queries> queries)
p50      : __ ms
p95      : __ ms
p99      : __ ms  (target ≤ 150 ms)
cache    : __/__ hits (__%)
```

---

## Notes

- The rig uses the `brute` vindex driver (exact recall) for reproducibility.
  Production deployments use `hnsw` which is faster at scale.
- The mock gateway is used (no real embedding API calls). The vector lane is
  active; embeddings are deterministic unit vectors seeded from SHA-256.
- The result cache is active (`STOWAGE_CACHE_OFF` is not set). Cache hits should
  be substantial given the repeated query set across sessions (10 distinct query
  templates mod session+query index).
- Phase 13 gates CI on this benchmark — a regression against the numbers here
  blocks merge (D-035).
