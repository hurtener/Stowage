# Phase 18 — Rollback & pending-confirmation resolution

- **Status:** draft
- **Owning subsystem(s):** `internal/api` (memories handler), `internal/store`
  (rollback commit + subject-indexed events), `internal/lifecycle` (confirm
  sweep), `internal/reconcile` (parked-duplicate counter)
- **RFC sections:** §6 (rollback), §10 surface, D-017, OQ-4
- **Depends on phases:** 08 (prior-state snapshots), 14 (lifecycle manager)
- **Informing briefs:** 02 (lifecycle trust model), 04 (fidelity — nothing is
  unrecoverable), 05 (LLM-gets-to-be-wrong recoverably)

> Launch-track note: this executes the master plan's "Supersede chains,
> confirmation & rollback" slot (see the numbering-reconciliation note added
> to docs/plans/README.md in this PR). D-017 promised the rollback API "lands
> with Phase 15"; the executed track renumbered around it and the slot was
> never built. `MarshalPriorState`'s own doc comment says "rollback will
> consume this payload verbatim" — this phase is that consumer.

## Goal

Close the P4 reversibility loop: every destructive reconciliation op
(update / merge / supersede) becomes invertible via
`POST /v1/memories/{id}/rollback`, consuming the D-017 prior-state event
payloads (scalars + entity/keyword/query junctions + provenance). And resolve
OQ-4: `pending_confirmation` memories stop being a roach motel — they
auto-resolve by TTL, promote early on repeated independent extraction, or
resolve explicitly via PATCH confirm/reject.

## Design

### Store: subject-indexed events + rollback commit

- `EventStore.ListBySubject(ctx, scope, subjectID string, limit int)
  ([]Event, error)` — newest-first, both drivers + conformance. Migration
  0006 (both dialects): `idx_events_subject ON events(tenant_id, subject_id,
  created_at)`.
- New commit action `ActionRollback` on the existing atomic
  `Memories().Commit` path: restores one or more FULL memory rows (all
  scalars incl. status/`superseded_by_id`) + junction replacement (ActionUpdate
  semantics: entities/keywords/queries/provenance replaced from the snapshot),
  optional tombstone of a result row (status → `deleted`), events written in
  the same tx (Phase 08 atomicity contract). Both drivers + conformance.

### `POST /v1/memories/{id}/rollback`

`{id}` is the memory that carries the prior-state event (the op's TARGET).

1. `Events().ListBySubject` → newest event of type `memory.updated` /
   `memory.merged` / `memory.superseded` for `{id}`. None → 404. Only the
   NEWEST reconciliation event is invertible — if a later one exists for the
   row, the older ones are unreachable by design (chains unwind one step at a
   time).
2. Conflict guards (409 with machine-readable detail): target already rolled
   back (a newer `memory.rolled_back` exists); result row itself superseded /
   merged downstream (chain must unwind newest-first); snapshot payload
   missing/unparseable.
3. Inverse per op, ONE Commit:
   - **updated** — restore `{id}` from snapshot (content, counters, junctions,
     provenance — replace semantics).
   - **superseded** — restore `{id}` to its prior state (active,
     `superseded_by_id` cleared by the snapshot); result row =
     `{id}.superseded_by_id` → status `deleted` (D-017 "tombstone").
   - **merged** — locate ALL siblings (`superseded_by_id = resultID`), restore
     each from ITS `memory.merged` snapshot (every sibling must have one or
     409), tombstone the merged digest. Rollback from any one source undoes
     the whole merge — document in the handler godoc and recipe.
4. Every rollback emits `memory.rolled_back` per restored row with payload =
   the PRE-rollback state (`MarshalPriorState` reused) — the audit trail stays
   complete and a rollback is itself diagnosable.
5. Vector lane: superseded rows are masked by status sidecar filtering, so
   restoring status restores vector retrievability IF the vector row survived.
   Verify; if any path hard-deletes vectors, re-embed on rollback via the
   gateway when available, else log + event a degraded restore (gateway-free
   rollback must still succeed — config rule).

### `GET /v1/memories/{id}` (RFC §10, needed for rollback UX + smoke)

Inspect: memory + junctions + provenance + chain (`supersedes_id`,
`superseded_by_id`, and parked-pending pointer), with the chain walk
depth-capped (constant, e.g. 10 — the master plan's "cycle caps" line item
reduces to this). Read-only, scope-enforced like every read.

### Pending-confirmation resolution (OQ-4 → resolved)

- **Parked-duplicate counter** (`internal/reconcile`): the active-only unique
  index never fires for parked rows, so today a re-extracted parked memory
  silently creates a SECOND parked row. Fix: in the park paths, pre-commit
  lookup of an existing `pending_confirmation` row with the same
  `content_hash` (new store method `GetByContentHashStatus(ctx, scope, hash,
  status)`); hit → increment its `match` counter + `memory.reconfirmed` event,
  discard the incoming candidate (commitExactDupDiscard pattern).
  `GetByContentHash` itself stays active-only (commit-time uniqueness
  contract untouched).
- **Confirm sweep** joins the lifecycle Manager (fifth sweep; jittered 10 m,
  advisory-locked, idempotent, budgeted, evented — Phase 14 pattern). A
  parked memory promotes when EITHER:
  - age > `confirmTTL` (profile knob, default 72 h) — OQ-4 lean-yes: the
    NEWER memory wins after the review window lapses; or
  - `match_count ≥ confirmRepeats` (profile knob, default 2) — repeated
    independent extraction is confirmation.
  Promotion = the SUPERSEDE path against the parked row's `supersedes_id`
  target (full prior-state event on the target) — so every auto-resolution is
  itself reversible by this phase's rollback API. Trust gates are NOT
  re-applied: TTL/threshold/human action IS the gate's resolution (document).
  Target already gone (expired/superseded meanwhile) → promote as plain
  activate, evented.
- **Explicit resolution**: `PATCH /v1/memories/{id}` with
  `{"action": "confirm"}` (immediate promotion, same supersede path) or
  `{"action": "reject"}` (status → `expired`, evented). Parked rows only —
  409 otherwise. The RFC's assert/correct/quarantine PATCH actions stay in
  v1.2 trust extensions; the route ships with just these two actions and says
  so in its godoc.

## Acceptance criteria (binding)

1. Round-trip per op type (update / supersede / merge): seed → destructive op
   → rollback → golden row-dump equals pre-op state including junctions AND
   provenance rows (reuse the Phase 08 AC-5 snapshot-completeness machinery);
   both drivers.
2. Multi-target merge rollback restores ALL sources and tombstones the
   digest; rollback initiated from any one source.
3. Conflict guards tested: double rollback → 409; rollback when the result
   row was superseded downstream → 409; missing snapshot → 409.
4. Restored memory is retrievable again via structured AND vector lanes
   (integration test).
5. Parked TTL auto-resolve: parked row older than `confirmTTL` promotes via
   supersede; the promotion is then rolled back successfully (the
   reversibility composition test).
6. Repeated extraction: identical content parked twice increments the counter
   (NO second parked row exists); at `confirmRepeats` the sweep promotes
   before TTL.
7. PATCH confirm promotes immediately; reject expires; non-parked target →
   409.
8. Confirm sweep idempotent (run-twice golden) + pg singleflight (Phase 14
   AC-6 pattern).
9. `ListBySubject` + `GetByContentHashStatus` + `ActionRollback` conformance
   green on both drivers; cross-scope rollback unconstructible (P3 test:
   caller in scope A cannot roll back scope B's memory).
10. eval-ci scores unchanged; coverage ≥85 on new/changed packages; race ×3;
    smokes 01–18 (phase-18.sh: ingest → park → reconfirm → sweep-promote →
    rollback → drilldown shows restored state, end-to-end).

## Files added or changed

```text
internal/api/{memories_handler.go, memories_handler_test.go, server.go routes}
internal/store/{store.go, types.go (ActionRollback), both drivers, conformance,
  migrations/{sqlite,postgres}/0006_events_subject_index.sql}
internal/reconcile/{reconcile.go park paths, reconcile_test.go}
internal/lifecycle/{confirm.go, confirm_test.go, manager.go wiring}
internal/config (confirmTTL/confirmRepeats profile knobs)
docs/plans/README.md (numbering-reconciliation note)
scripts/{coverage.json, smoke/phase-18.sh}
```

## Implementation notes

### Vector restore behavior (AC-4 finding)

The HNSW driver does NOT perform status-based filtering; it loads all tenant
vectors into the in-memory graph regardless of memory status. The sidecar
retrieval filter only excludes by project/user/session scope, not by status.
Vectors are NOT hard-deleted on supersede: the `StoredVector` row is retained
with the same `memory_id`. Consequently, after an ActionRollback that restores
a previously superseded memory to `status='active'`, the vector entry already
exists and no re-embedding is required. Lexical/structured/query lane
retrievability is restored immediately by the status change back to `active`.
The existing `ScopeInvalidator.InvalidateScope` call after every rollback/confirm
commit ensures the result cache does not serve stale superseded results.

### Parked-duplicate dedup (Step 2b)

The reconcile pipeline now performs a "Step 2b" check after the exact-dedup
check (Step 2): `GetByContentHashStatus(ctx, scope, hash, "pending_confirmation")`.
If a `pending_confirmation` row with the same content hash already exists, the
incoming candidate is discarded without creating a second parked row. Instead,
the existing row's `match_count` is bumped and a `memory.reconfirmed` event is
emitted. This prevents duplicate park accumulation when the same unconfirmed
content is submitted multiple times.

### ActionConfirm for reject

The `PATCH /v1/memories/{id}` reject path uses `ActionConfirm` with
`Memory.Status='deleted'` rather than introducing a new action. This is valid
because `confirmMemoryStatusSQLite/PG` performs a plain status+superseded_by_id
UPDATE, and `status='deleted'` is a valid terminal state. The driver's
`superseded_by_id` column is `NOT NULL DEFAULT ''`, so the raw string value
(empty string, not SQL NULL) must be passed; `nullStr()` is intentionally not
used there.

### superseded_by_id NOT NULL constraint

Both the SQLite and Postgres schemas define `superseded_by_id TEXT NOT NULL DEFAULT ''`.
The `confirmMemoryStatusSQLite/PG` helpers were initially written using `nullStr()`
which sends SQL NULL, violating the NOT NULL constraint. Fixed to pass the raw
string value directly (empty string is the valid sentinel).

## Decisions filed

- D-064: rollback contract — newest-event-only inversion, atomic
  ActionRollback commit, tombstone = status `deleted`, `memory.rolled_back`
  carries the pre-rollback snapshot; merge rollback is all-or-nothing.
- D-065: OQ-4 RESOLVED — pending_confirmation auto-resolves in favor of the
  newer memory after `confirmTTL` (72 h default) or at `confirmRepeats` (2)
  independent re-extractions; promotion rides the supersede path so it is
  always reversible; explicit confirm/reject via PATCH; assert/correct PATCH
  actions deferred to v1.2.

## Risks & mitigations

- Restoring rows the retrieval cache still considers superseded → rely on the
  existing per-scope generation invalidation (rollback commits bump the scope
  generation like any write; assert in test).
- Snapshot drift (fields added to Memory after Phase 08) → AC-1's golden
  round-trip catches it; add a compile-time field-count guard test mirroring
  the Phase 08 snapshot-completeness test.
- HNSW deleted-vector edge → AC-4 forces the question in implementation
  rather than leaving it as an assumption.
