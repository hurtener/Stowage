# Phase 29d — Pipeline & consolidation correctness wave

- **Status:** in-progress
- **Owning subsystem(s):** `internal/lifecycle`, `internal/reconcile`, `internal/retrieval`, `internal/api`, `internal/mcpserver`, `internal/trust`, `internal/store`
- **RFC sections:** §4.2 (retrieval), §5.x (scoring/lifecycle), §6 (reconciliation/forgetting), §6c (trust/review)
- **Depends on phases:** 29/29b/29c (D-103–D-109, on this branch)
- **Informing briefs:** [02](../research/02-predecessor-ccmem.md) (scoring & lifecycle model — decay,
  sweeps, supersede) and [03](../research/03-engram.md) (reconciliation), per `docs/research/INDEX.md`;
  retrieval items track briefs [01](../research/01-predecessor-python.md)/[04](../research/04-cl-bench.md).
- **Informing audit:** the read-only multi-agent miswiring audit (22 findings vs `main`; 4 already
  fixed on this branch — see below), plus two independent §17 adversarial reviews of the wave diff
  (B1/S1–S6/N1–N4 resolution — D-119/120/121).

## Goal

Close the production-correctness bugs surfaced by the audit so "all systems are up and correct"
before we re-baseline the eval. The audit ran against `main`; this plan tracks only what is
**genuinely open on this branch**, plus Idea-1 (richer superseded rendering for MCP clients).

## Already fixed on this branch (do NOT re-fix — audit ran vs main)

- #3 reconcile numeral-correction guard — **D-104** (`reconcile.go:388` `NumeralsDiverge`).
- #7 within-flush candidate ordering by record-ULID — **D-106** (`candidateAssertionKey`).
- #11 reconcile sees raw conversation turns — **D-108** (`buildReconcileContext`).
- #13 `candidateToMemory` sets `ValidFrom` — **D-109** (`reconcile.go:961`).

## Open work items (grouped; acceptance = test + no regression)

### Tier 0 — data corruption (straight fixes, no RFC)
- **W0 (#5) decay unit bug** — `int64(DecayInterval)` ns-as-ms → ~38yr grace, P4 dead. Fix to
  `.Milliseconds()`; regression test bounds grace tightly. **(DONE — D-110.)**
- **W1 (#1) dedupe runs tenant-only** — `dedupeTenant` at `{Tenant}` → `FindNeighbors` matches
  across users, survivor written NULL-scope. Add `Memories().DistinctScopes` (both drivers +
  conformance); iterate full `(tenant,project,user[,session])` like `episodes`/`threading`.
  P3 + P1.
- **W2 (#2) dedupe survivor + numeral guard** — sweep only merges, keeps `target` arbitrarily,
  no numeral guard. Shared `reconcile.NumeralsDiverge` (exists) + new `reconcile.SelectSurvivor`
  (later `ValidFrom` → trust tier → importance → `CreatedAt` → ULID). Numeral-divergent near-dup ⇒
  **supersede-by-date** (not merge); same-numeral dup ⇒ merge keeping survivor. Reversible (D-070).
- **W3 (#4) ingest scope threading** — per-record `project_id/user_id/session_id` (+ D-059
  contribute target) dropped; pipeline tenant-only. Make ingest scope-authoritative end-to-end.
  P3.

### Tier 1 — correctness (straight fixes)
- **W4 (#6) rollup digest cap** — supersedes all promotable but digests only first 10 → newest
  correction lost. Cap `Targets` to digested set (or digest all).
- **W5 (#8) retrieval cache key** — add `Kinds` + `IncludeLanes` to `cacheKey`; key unit test.
- **W6 (#9) rerank-before-trim** — rerank the scored pool, then trim to `limit` (currently trims
  then reranks the already-cut top-K). On the eval path.
- **W7 (#10) review rollback dead** — add `review_approved/rejected` to `isRestorable` +
  `commitSimpleRollback`; round-trip test. P4/D-084.
- **W8 (#15) lifecycle cache invalidation** — give the Manager a `ScopeInvalidator`; call after
  status-mutating sweeps.
- **W9 (#16) expire atomicity** — `memory.expired` event + `SetStatus` in one txn (mirror confirm).

### Tier 2 — consume captured signals / latent (some decision-gated)
- **W10 (#18) resolve `contradicts` by date** — cheapest consolidation pass: for every
  `contradicts` pair with both endpoints active, supersede the older by `ValidFrom`. Reversible.
- **W11 (#12) temporal scoring/windowing** — rank/window/cooldown key on
  `COALESCE(valid_from, created_at)` not `created_at`. Needs D-109 ratified (it is, on branch).
- **W12 (#19) consume `updates-and-corrections`** — as an explicit `candidate.IsCorrection`
  signal that biases reconcile toward supersede / seeds sweep clusters (not free-form topic reuse).
- **W13 (#17) `memory.expired` payload schema** — unify the two incompatible shapes (events/v1).
- **W14 (#20) `was_cited`** — set it on the citation path so traces aren't all false.
- **W15 (#14) cross-flush recall** — pull not-yet-embedded same-scope candidates into
  `augmentWithVectorNeighbors`; widen neighbor recall for contradiction detection.

### Idea 1 — richer superseded rendering (MCP-faithful)
- **W16** — the dual-visibility item carries, inline and self-contained: `superseded_by` **value +
  date + details** (not a bare ID), configurable history depth (sliding window). Clients that
  can't use a reader-prompt section still get "this was superseded by «X» on «date»". Plus a
  history-scan tool. (Accuracy-modest; product/MCP correctness + clarity.)

## Refuted by the audit — do NOT chase

- `match_count`/`use/save` biasing scoring (dead inputs / feedback-only) → no read-time near-dup
  collapse (P1 drill-down hazard).
- `memory.expired` non-reversibility is intentional (decay is deterministic lifecycle, RFC §490).
- rollup "cross-user leak" is orphaning, not a leak — fix scope-stamping (covered by W1-style scope).

## Acceptance criteria (binding)

1. Each W-item: a unit/golden/integration test that would have caught the bug; `-race` green.
2. Destructive ops (W1/W2/W10) emit reversible prior-state and round-trip via rollback (D-070).
3. Every store query touched is scope-parameterized (P3); new `DistinctScopes` on both drivers +
   conformance.
4. `make preflight` + drift-audit + mirror green; coverage on touched packages met.
5. **§17 checkpoint:** an adversarial-review workflow over the full wave diff lands its punch list;
   blocking findings fixed before merge.
6. Smoke: `scripts/smoke/phase-29d.sh` asserts the key invariants (decay grace magnitude,
   dedupe scope, numeral-guard in lifecycle, cache key, rerank order, review-rollback).

## Exit gate (post-merge, separate)

Fix the eval harness to be **production-faithful** (un-park the consolidation/decay sweeps; run a
real consolidation pass), then re-baseline on **100 questions, broken out by category**, target
**≥75% at the current compression rate**.

## Decisions filed

- D-110: decay grace computed in milliseconds (unit-bug fix). **Filed below.**
- D-111: lifecycle consolidation sweep does merge AND supersede, shared survivor-selection +
  numeral guard with reconcile (D-067), full-scope. *(W1/W2)*
- D-112: ingest is scope-authoritative end-to-end (per-record scope honored). *(W3)*
- D-113: retrieval ranking/windowing uses `COALESCE(valid_from, created_at)`. *(W11)*
- D-114: superseded items are rendered self-contained for non-prompt clients (Idea 1). *(W16)*

## §17 adversarial review — punch list resolution (D-119/120/121)

Two independent multi-agent reviews of the wave diff (`0f5eeab..HEAD`) ran in parallel; both flagged
the same BLOCKING finding. Resolution (all landed in this wave, each with a regression guard; the
three highest-risk guards are mutation-verified to fail on the buggy code):

- **B1 (BLOCKING) — dedupe still merged across users.** `ListActiveForDecay` is tenant-only, so the
  per-user candidate seed was the whole tenant; and `buildScopeWhere` wildcards an empty leaf.
  Fixed with exact-leaf scope (`ListActiveInScope` + `buildExactScopeWhere` + `NeighborQuery.ExactScope`).
  **D-119.** Guards: conformance `MemoryListActiveInScope` (incl. NULL bucket + round-trip);
  `TestDedupeSweepNeverMergesAcrossUsers` (mutation-verified).
- **S1 — expire/rollup reversibility (P4).** decay `memory.expired` made restorable with a flat
  snapshot (active, valid_until cleared); rollup emits `memory.merged` so all siblings restore.
  **D-120.** Guards: `TestDecayExpireIsReversible`, `TestRollupSweepIsReversible` (mutation-verified).
- **S3/S5/N3 — cache.** Cache is tenant-keyed: dedupe now invalidates at tenant scope; rollup
  personal-zone + confirm/promoteParked paths gained the missing invalidation; the cache key now
  includes the effective `limit`. **D-121.** Guards: `TestResultCache_LimitInKey`,
  `TestDedupeSweep/RollupSweepInvalidatesCacheAtTenant`.
- **S4/S8 — survivor & numeral-correction.** `TestDedupeSweepKeepsSurvivorContent`,
  `TestDedupeSweepNumeralCorrectionDropsLoserSurface`.
- **S6 — rerank before trim.** `TestRerankPromotesBelowLimitCandidate` (mutation-verified).
- **N1/N2 — dedupe audit events** name the real survivor/loser; subject is the merged row; payload
  carries survivor_id/loser_id/merged_id.
- **N4 — approve-rollback** symmetry: `TestReview_ApproveRollback`.
- Deferred (noted, not blocking): rollup digest size cap (nit; bounded in practice); per-scope
  dedupe budget multiplies sweep work (N1 — mitigated since exact-leaf removes the tenant-wide rescan).

- D-119: dedupe sweep isolates partitions with exact-leaf scope. *(B1)*
- D-120: decay expire reversible; rollup is a reversible many-to-one merge. *(S1)*
- D-121: cache invalidation matches the tenant-keyed cache; cache key includes effective limit. *(S3/S5/N3)*
