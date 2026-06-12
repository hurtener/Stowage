# Phase 14 — Sweeps (lifecycle maintenance)

- **Status:** implemented
- **Owning subsystem(s):** `internal/lifecycle` (new), `internal/store`
  (sweep-support methods), serve wiring
- **RFC sections:** §6 (scheduled lifecycle), §4.1.4 (re-enqueue promise), P4
- **Depends on phases:** 13 (benchmark gate defends quality through these
  changes)
- **Informing briefs:** 01 (sleep-cycle consolidation + idempotency markers),
  02 (decay floors; near-dup discipline)

## Goal

The in-process maintenance loop: four sweeps on jittered tickers (Phase 06
pattern), each **idempotent** (run-twice-same-state), singleflight across
replicas (pg advisory locks from Phase 03; sqlite is single-process),
crash-recoverable, and fully evented. Closes the Phase 05 crash-recovery
promise (re-enqueue) and the "DB does not grow linearly with traffic"
promise (dedupe/compression).

## Design

`internal/lifecycle.Manager` — one goroutine per sweep, jittered base
intervals (profile constants): decay 10 m, dedupe 30 m, rollup 60 m,
re-enqueue 2 m. Each sweep: acquire advisory lock (pg) or proceed (sqlite) →
batched work (limit per pass) → events → release. Job markers record
last-completed watermark where a sweep is incremental.

1. **Decay sweep**: batched scan of active memories per scope; compute the
   Phase 10 decay factor (pure scoring fn reused — export the decay
   component); memories whose decayed effective weight < floor for
   `decayExpireGrace` consecutive sweeps (tracked via `valid_until` set on
   first below-floor observation) → status `expired` + event with prior state
   (D-017 contract). user_stated floor 0.5 means user_stated never expires by
   decay (assert in test).
2. **Dedupe/compression sweep**: per scope, group active memories by
   `content_hash` (exact dups that raced past commit-time unique index can't
   exist — instead this sweep targets **near-dups**: for each recent memory,
   FindNeighbors + bigram-Jaccard ≥ 0.85 → merge via the existing
   reconcile-style Commit(merge) carrying prior snapshots; provenance union;
   counters summed; events). Budgeted per pass (e.g. 200 comparisons).
3. **Rollup sweep**: session-scoped working memories (scope.Session != "")
   older than `rollupAge` (profile: 7 d) whose session is inactive → merged
   into ONE session digest memory (kind `narrative`, provenance union,
   counters summed) promoted to the parent scope (session column cleared,
   privacy zone respected — `personal`+ stays unpromoted, just expires);
   sources → superseded. Records untouched (P1).
4. **Re-enqueue sweep**: `Records().ListUnprocessed(olderThan=processing
   deadline 10 m, limit)` → re-offer to the pipeline channel (non-blocking;
   leftover next pass) + event. Closes the ingest drop-on-full and
   crash-loss windows.

Store additions (both drivers + conformance): decay-scan iterator
(`Memories().ListActiveForDecay(scope-less batched? — sweeps are global
operators: iterate tenants via a new `Tenants(ctx)` listing, then per-scope
queries — keeps P3's no-unscoped-reads intact by enumerating tenants
explicitly as an operator concern; document)`, `SetValidUntil`, expired
transition in SetStatus (exists). Advisory-lock helper exists (Phase 03).

## Acceptance criteria (binding)

1. Each sweep idempotent: run twice on a fixed fixture ⇒ identical DB state
   (golden row-dump comparison per sweep).
2. Decay: below-floor memory expires after grace; user_stated never expires
   via decay; expiry event carries prior state.
3. Dedupe: planted near-dup pair (≥0.85) merges with provenance union +
   summed counters; unrelated pair untouched; merge is reversible (event
   snapshot test reusing Phase 08 AC-5 machinery).
4. Rollup: inactive session's working memories → one promoted digest;
   `personal` zone working memories expire unpromoted; records intact.
5. Re-enqueue: stalled-pipeline records re-enter and complete extraction
   end-to-end (integration test with mock gateway).
6. Singleflight: two concurrent sweep managers (pg) ⇒ exactly one executes
   per sweep tick (conformance-level test, CI postgres).
7. Crash-mid-sweep: kill after partial batch ⇒ next run completes without
   duplication (idempotency via watermarks/markers, test).
8. Benchmark gate stays green (make eval-ci unchanged scores); coverage ≥85
   lifecycle; race ×3; smokes 01–14 (phase-14.sh: seed → force-run sweeps via
   a test hook env → assert expirations/merges/rollups/re-enqueues evented).

## Files added or changed

```text
internal/lifecycle/{manager.go, decay.go, dedupe.go, rollup.go, reenqueue.go, *_test.go}
internal/scoring (export decay component)
internal/store (Tenants listing + decay-scan + SetValidUntil; both drivers + conformance)
cmd/stowage/main.go (manager wiring + STOWAGE_SWEEP_FORCE test hook env)
scripts/{coverage.json, smoke/phase-14.sh}
```

## Decisions filed

- D-057: tenant enumeration (`Tenants()`) is an explicit operator-level store
  method — sweeps iterate tenants then operate per-scope; the no-unscoped-
  query rule holds for data reads.
- D-058: decay expiry uses observe-then-grace via valid_until (no immediate
  hard expiry); grace = 2 sweeps.

## Risks & mitigations

- Sweep storms on big tenants → per-pass budgets + watermarks; metrics per
  sweep.
- Merge cascades in dedupe → only pairwise per pass, budgeted; supersede
  chains keep history.
