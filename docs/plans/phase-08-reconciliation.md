# Phase 08 — Reconciliation + commit

- **Status:** draft
- **Owning subsystem(s):** `internal/reconcile`, `internal/store` (commit
  method + neighbor search + migration 0002), `internal/pipeline` (wiring)
- **RFC sections:** §6 (reconciliation, reversibility), §5.2 (trust, status),
  §5.6 (links), §2.1 P4
- **Depends on phases:** 07
- **Informing briefs:** 03 (active reconciliation: dedupe/rewrite/keep/delete
  — memory must forget), 02 (pre-filters eliminated ~40 % of reconciliation
  LLM calls; trust-gated supersede; contradiction boost), 01 (multi-signal
  dedupe scoring)

## Goal

Candidates become **memories**, safely: cheap pre-filters kill duplicates
before any LLM cost; a schema-constrained gateway decision reconciles each
survivor against its in-scope neighbors (`add | update | merge | supersede |
discard`); trust gates park risky supersedes as `pending_confirmation`;
every destructive operation is invertible from its event (D-017 contract —
rollback *API* lands Phase 15, but the events written here must already
carry the prior state); commits are transactional; `supports`/`contradicts`
links are written from decisions. After this phase the write path is complete
end-to-end: ingest → buffer → extract → reconcile → committed memory.

## Brief findings incorporated

- Brief 02: SHA-256 exact + bigram-Jaccard near-dup pre-filters before LLM;
  trust score `f(use, save, source multiplier, importance)` gating supersede;
  contradiction boost (correction inherits `importance ≥ 4`, elevated
  stability).
- Brief 03: the decision set is exactly Engram's reconciliation moves,
  constrained tool-call, with the model's stated reason persisted.
- Brief 01: every lifecycle action is an audit event.

## Findings I'm departing from

- **No embeddings at commit.** `memory_vectors` doesn't exist until Phase 09
  (D-038); memories commit vector-less and Phase 09 backfills (embeddings are
  recomputable). Neighbor retrieval this phase is structural, not semantic.

## Design

### Store additions (seam + both drivers + conformance)

1. **Migration 0002**: `ALTER TABLE memories ADD COLUMN content_hash TEXT`
   (SHA-256 hex of normalized content) + index `(tenant_id, content_hash)`.
   Backfill not needed (no production data). Forward-only discipline intact.
2. `Memories().GetByContentHash(ctx, scope, hash)` — exact-dup lookup.
3. `Memories().FindNeighbors(ctx, scope, NeighborQuery{Entities, Keywords,
   Kinds, Limit})` — junction-overlap search: memories sharing entities/
   keywords, ranked by overlap count then recency, `status='active'` only.
   This is the **interim neighbor lookup** until Phase 09's lanes (documented
   here; it survives later as the structured lane's core query).
4. `Memories().IncrementCounter(ctx, scope, id, counter)` — for match-count
   bumps on dedup (atomic, both drivers).
5. `Memories().Commit(ctx, scope, CommitSet) error` — **one transactional
   unit** per reconciliation outcome: memory insert/update, junction rows,
   provenance rows, link rows, status transitions on targets, and the event
   row — atomic per driver (sqlite: one writer-goroutine closure; pg: one tx).
   Events written inside the commit carry `prior` snapshots for D-017.

### `internal/reconcile`

Worker pool consuming `CandidateBatch` (replaces the Phase 07 no-op consumer):

Per candidate:
1. **Exact pre-filter:** `GetByContentHash` hit (active memory) → drop
   candidate, `IncrementCounter(match)`, event `reconcile.dedup_exact`.
   No LLM call.
2. **Neighbor retrieval:** `FindNeighbors` (limit 8).
3. **Near-dup pre-filter:** bigram-Jaccard ≥ 0.85 between candidate content
   and any neighbor → treat as the same fact: drop + `match`++ + event
   `reconcile.dedup_near` (threshold is a profile constant — D-044). No LLM.
4. **Fast-add path:** zero neighbors → commit as `add` directly. No LLM call
   (D-044) — the common case for a fresh scope costs nothing.
5. **Decision call:** `gateway.Complete` with the decision schema (versioned
   constant + prompt goldens):
   `{action: add|update|merge|supersede|discard, target_ids[], content?,
   links: [{target_id, type: supports|contradicts}], reason}` — candidate +
   neighbors (id, kind, content, trust summary) in the prompt. Server-side
   validation: target_ids ⊆ neighbor set (else the decision degrades to
   `add` + metric — the model never touches a memory it wasn't shown);
   content required for update/merge.
6. **Trust gate (supersede/update on a high-trust target):**
   `trust = (0.5 + log1p(use + 2·save)) · source_multiplier · (importance/3)`
   (brief 02). `trust < 1.0` → apply; `1.0–3.0` → apply + `reconcile.warned`
   event; `≥ 3.0` → the new memory commits as `pending_confirmation` with
   `supersedes_id` set, target stays `active`; resolution machinery is
   Phase 15.
7. **Apply + commit** (one `Commit` call):
   - `add`: insert active memory (+junctions, provenance, links).
   - `update`: target content rewritten; event carries prior content/context
     (D-017); candidate's provenance appended; `updated_at` bumped.
   - `merge`: insert merged memory; all sources → `superseded` +
     `superseded_by`; provenance union; event carries source snapshot ids.
   - `supersede`: target → `superseded`; new memory active with
     **contradiction boost** — `importance = max(candidate, 4)`, stability
     elevated (constant) — corrections outrank what they correct immediately.
   - `discard`: nothing persisted; event with reason.
   - Trust sources: extraction candidates are `llm_extracted`; the decision
     never raises trust_source.
8. Terminal gateway failure → dead-letter (stage `reconcile`, candidate
   payload) + event; candidates are reproducible from records (P1), so loss
   is recoverable.

Events: every outcome emits `memory.committed | memory.updated |
memory.merged | memory.superseded | reconcile.discarded | reconcile.parked`
with the model's reason and prior-state snapshots where destructive.

### Wiring

serve: reconcile pool consumes the extract stage's channel; shutdown order
api → buffers → extract → reconcile → gateway/store. The full write path is
live; the Phase 08 smoke proves ingest→memory end-to-end with the mock
gateway.

## Files added or changed

```text
internal/reconcile/{reconcile.go, prefilter.go, decision.go, trust.go,
                    prompt.go, reconcile_test.go}
internal/store/{store.go, types.go}            (methods above)
internal/store/migrations/{sqlite,postgres}/0002_content_hash.sql
internal/store/sqlitestore/*, internal/store/pgstore/*   (implementations)
internal/store/conformance/conformance.go      (new cases)
cmd/stowage/main.go                            (wiring)
scripts/coverage.json                          (reconcile 85)
scripts/smoke/phase-08.sh
```

## Config keys added

None top-level. Profile internals: near-dup threshold (0.85), neighbor limit
(8), reconcile worker count, trust-gate thresholds (1.0 / 3.0) — constants
with docs (knob guardrail).

## Acceptance criteria (binding)

1. Pre-filters: replaying an identical conversation produces zero gateway
   reconcile calls (exact-dup test) and a near-dup fixture (≥ 0.85) also
   short-circuits; both bump `match_count` (test reads counters).
2. Fast-add: first candidate in an empty scope commits with no reconcile
   gateway call (counting mock).
3. Decision validation: a scripted decision targeting a non-neighbor id
   degrades to `add` (never mutates the unseen memory) — test.
4. Trust gate matrix (table test): low → applied; medium → applied + warned
   event; high → parked `pending_confirmation`, target still active,
   `supersedes_id` set.
5. Reversibility contract: update/merge/supersede events carry prior-state
   snapshots sufficient to restore (asserted by reconstructing the prior
   memory from the event payload in a test — the Phase 15 rollback will
   consume exactly this).
6. Contradiction boost: a supersede commit has `importance ≥ 4` and elevated
   stability (test).
7. Links: a scripted decision with `links` writes `supports`/`contradicts`
   rows with `source='reconciler'` (test + conformance for the new store
   methods on both drivers).
8. Commit atomicity: a forced mid-commit failure (driver fault injection on
   the sqlite closure / pg tx) leaves no partial rows (test).
9. End-to-end write path: ingest → flush → extract → reconcile → active
   memory with junctions + provenance, via live serve + mock gateway
   (integration test + smoke).
10. Coverage ≥ 85 reconcile; conformance green both drivers; all `-race`.

## Smoke script

phase-08.sh: serve (mock gateway scripted add); ingest fixture conversation;
explicit flush; poll for `memory.committed` event; assert memory row +
junctions + provenance in sqlite; replay same conversation; assert
`reconcile.dedup_exact` event and no second memory.

## Test plan

Prompt goldens (decision template); trust matrix; pre-filter tables; fault
injection on Commit; full-path integration under `-race`; fuzz target on
decision JSON validation; conformance for all new seam methods.

## Risks & mitigations

- Near-dup threshold too aggressive (drops genuine updates) → only drops on
  ≥ 0.85 against *retrieved neighbors* (not corpus-wide); the gray zone goes
  to the LLM; threshold is a documented constant revisited with eval data.
- Commit method becomes a god-function → CommitSet is a closed struct per
  outcome type; review gate.

## Glossary additions

- **Fast-add** — committing a no-neighbor candidate without an LLM decision.
- **Parked** — a memory in `pending_confirmation` awaiting Phase 15
  resolution.

## Decisions filed

- D-044: pre-filter thresholds + fast-add path (no-neighbor candidates skip
  the decision call entirely).
- D-045: `Memories().Commit` is the single transactional unit for
  reconciliation outcomes; events inside the commit carry prior-state
  snapshots (the D-017 reversibility contract starts here).
