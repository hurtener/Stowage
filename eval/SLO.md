# Stowage Retrieval SLO — Reference Results

**Binding target (D-031):** `p99 ≤ 150 ms` at 1 000 concurrent sessions on
the postgres driver on reference hardware (cache hit path ≤ 20 ms).

**The gate bites (D-095).** The rig **fails the build** (`t.Fatalf`) when the measured
p99 exceeds the budget (`-slo.maxp99`, default = the 150 ms binding target). It is **not**
part of the default `go test ./...` / CI matrix: it is behind the `slo` build tag and
skips without a postgres DSN, so it runs as a dedicated **reference-hardware release gate**
via `make slo` (the binding 150 ms is measured on reference hardware, not on noisy shared
CI runners — D-031). A slower-than-reference environment may raise the budget deliberately
with `-slo.maxp99`; the binding number recorded below is always taken at the 150 ms target
on reference hardware. The eval benchmark suite (`make eval-ci`) is the gate that runs on
every CI build (D-035); the SLO is its reference-hardware counterpart.

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
- The rig **fails on a p99 regression** (`t.Fatalf` when p99 > `-slo.maxp99`), so a
  change that regresses the SLO blocks the release gate when `make slo` is run (D-095).
  It is a **reference-hardware** gate (D-031), deliberately kept out of the noisy
  per-PR CI matrix; the eval suite (`make eval-ci`) is the benchmark gate that runs on
  every CI build (D-035). Re-run `make slo` on reference hardware and update the
  **Recorded Numbers** above whenever the read path changes materially.
