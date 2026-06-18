# Phase 19 — Reflection write-side (ACE §6a.1-2)

- **Status:** draft
- **Owning subsystem(s):** new `internal/reflect` (reflection extraction: prompt,
  schema, trajectory assembler, candidate constructor); `internal/lifecycle`
  (re-reflection sweep); `internal/store` (+ `ListByOutcome` + index migration);
  `internal/boot` (reconcile fan-in wiring); `internal/config` (reflection knobs).
- **RFC sections:** §6a.1 (outcome-tagged ingestion), §6a.2 (reflection extraction
  mode + multi-epoch re-reflection sweep), §5.4 (kind enum + reflection kinds),
  §5.6 (links), §6 (reconciliation), §7/§10 (gateway seam, schema-constrained).
- **Depends on phases:** 05 (records/outcomes/branches), 07 (extraction), 08
  (reconciliation + commit), 10 (scoring), 14 (lifecycle sweeps), h5/D-072
  (deterministic playbook assembly — the READ side, already shipped).
- **Informing briefs:** 05 (ACE — the reflection→playbook loop, execution-feedback
  reflection, context-collapse defense), 04 (CL-Bench — gain from compounding
  experience), 06 (mempalace — playbook positioning).

## Goal

When this phase is done, a Stowage scope's accumulated **outcome-tagged records**
(success/failure trajectories from a Harbor fleet) are distilled by an outcome-aware
**reflection extraction pass** into `strategy` and `failure_mode` candidate
memories ("what worked, what to avoid, why"), reconciled like any other candidate
(dedupe / update / supersede under the same trust gates), and surfaced by the
already-shipped `GET /v1/playbook`. Reflection runs as a supervised **lifecycle
sweep** (P2 — no caller-facing trigger; fed by already-ingested outcomes), with a
**multi-epoch re-reflection** pass that revisits older outcomes as the playbook
matures. The write side closes the ACE loop: agents run → outcomes ingest
(fire-and-forget) → reflection + reconciliation evolve the team playbook → every
agent's next session starts from the playbook. This is the **LLM write-side**;
the playbook *assembly* remains LLM-free (§6a.3, D-072).

## Brief findings incorporated

- **Brief 05 (ACE):** reflection works from **execution feedback alone** (no gold
  labels) — outcomes (`success`/`failure` + detail) are the reflection signal; no
  human annotation. Reflection produces *itemized* `strategy`/`failure_mode`
  memories (not a monolithic rewrite) so evolution happens through **delta
  reconciliation**, defending against ACE's "context collapse." Multi-epoch
  re-reflection (revisiting old trajectories as the playbook grows) is an explicit
  ACE mechanism → our lifecycle sweep.
- **Brief 05:** the reflection→playbook loop is a *team* capability — reflection
  memories are shared via grants (§5.3); one agent's `failure_mode` becomes every
  teammate's avoided mistake. (Grant sharing already exists, Phase 16; Phase 19
  only needs to write the kinds it already shares.)
- **Brief 04 (CL-Bench):** the value is *compounding* gain across sequential tasks
  — measured by the Phase 20b gain-fleet harness, which consumes this loop.

## Findings I'm departing from

- **RFC §6a.2 says reflection runs "alongside topic extraction."** Departure:
  reflection is implemented as a **sweep-only** stage, NOT a per-buffer-flush mode
  beside topic extraction. Reason (from the seam map): outcome is not carried on
  the in-flight `pipeline.Item`/`FlushedBuffer`, and a reflection trajectory spans
  *multiple* flushes/sessions — so a per-flush hook is the wrong granularity. The
  RFC itself says multi-epoch reflection "runs as a lifecycle sweep" (§6a.2); we
  make the *whole* reflection pass sweep-driven, reading outcome-tagged records
  directly from the store. Filed as **D-077**.
- **RFC §338 says "kinds carry default scoring weights."** No per-kind scoring
  table exists today (`scoring.Score` is kind-agnostic; `candidateToMemory`
  hardcodes trust/stability). Rather than retrofit kind-aware scoring across the
  engine, Phase 19 applies reflection **seed weights** in the reflection candidate
  constructor (seed importance/stability/trust per reflection kind); generic
  scoring then evolves them via counters. Documented in D-077.

## Design

### Architecture: a sweep that feeds the existing reconcile flow

Reflection reuses everything downstream of extraction. The new path is:

```
[lifecycle re-reflection sweep]                      (internal/lifecycle/reflect.go)
   → per scope: ListByOutcome(scope, {success,failure}, since-watermark)
   → assemble trajectories  (internal/reflect: group by (session_id, branch_id))
   → gateway.Complete(reflectionSchema)  → strategy/failure_mode candidates
   → emit pipeline.CandidateBatch  ──►  [reconcile stage]  (UNCHANGED)
                                          → dedupe / trust gate / supersede / commit
                                          → memories(kind=strategy|failure_mode)
   ↳ GET /v1/playbook (D-072, already shipped) renders them
```

No new caller surface (the trigger is outcome ingestion, which already exists on
all tiers). A forced run is available for tests/ops via the existing
`STOWAGE_SWEEP_FORCE` / `Manager.RunForce` path — not a new public verb.

### 1. Store: query outcome-tagged records by scope (the missing primitive)

`internal/store`: add to `RecordStore`

```go
// ListByOutcome returns scope's records whose outcome ∈ outcomes and
// occurred_at > since, ordered by (session_id, branch_id, occurred_at), capped at
// limit. Scope-parameterized (P3). Used by the reflection sweep.
ListByOutcome(ctx, scope, outcomes []string, since int64, limit int) ([]Record, error)
```

Implemented on **both** drivers (sqlite + pgx), proven by the shared conformance
suite. New **forward-only migration** adds index `idx_records_tenant_outcome_occurred
(tenant_id, outcome, occurred_at) WHERE outcome <> ''` (partial; only ~tagged
records). This is an **index addition, not a schema-inventory change** (the
`outcome`/`occurred_at` columns exist since the day-one schema — §8.1/D-024), so
no RFC amendment is required.

### 2. `internal/reflect`: the reflection extraction (LLM, schema-constrained)

A new package (kept out of `internal/pipeline/extract.go`, which is
topic-extraction-specific). It owns:

- **`reflectionSchema`** (`json.RawMessage`, draft-07): a response object of
  `reflections: [{kind ∈ {strategy,failure_mode}, content, context, entities,
  keywords, anticipated_queries, importance, confidence, provenance[]}]` — the same
  candidate shape as topic extraction but a **reflection-only kind enum** (does NOT
  widen the topic `ValidKinds`, so topic extraction can never emit reflection kinds
  and vice-versa).
- **Trajectory assembler:** groups outcome-tagged records by `(session_id,
  branch_id)`, orders by `occurred_at`, and pairs/contrasts terminal outcomes
  ("what worked" from `success`, "what to avoid" from `failure"`) — ACE's
  success/failure contrast. A trajectory with no terminal outcome is skipped.
- **Reflection prompt** (golden-tested, §11): outcome-aware system prompt distinct
  from `internal/pipeline/prompt.go`; instructs the model to distill *transferable*
  strategies/failure-modes with provenance spans, not restate facts.
- **Candidate constructor:** builds `pipeline.Candidate`s with reflection **seed
  weights** (per-kind seed importance/stability; `TrustSource:"llm_reflected"` —
  a new trust-source string, distinct from `llm_extracted`, so provenance shows the
  reflection origin) and stamps scope/branch server-side (P3).
- All gateway calls go through `gateway.Gateway` (P5); schema-constrained (§10);
  metered/evented like every gateway call. Reasoning-headroom `MaxTokens` (the
  thinking-model lesson — REPORT.md item 4).

### 3. `internal/lifecycle`: the re-reflection sweep

Following the **rollup** sweep precedent (`rollup.go`): a new `runReflect`
(`lifecycle/reflect.go`) registered in `Manager.Start`/`RunForce`, on a jittered
ticker, Postgres advisory lock `0x1406`. Per scope (`st.Tenants`):

1. Read the per-scope reflection **watermark** (last reflected `occurred_at`) via
   `OpsStore` job markers (the `job_markers` table already exists — no new schema).
2. `ListByOutcome(scope, {success,failure}, since=watermark)`; assemble
   trajectories via `internal/reflect`.
3. Reflection gateway call → candidates → emit `CandidateBatch` into the reconcile
   stage (see §4 wiring).
4. Advance the watermark to the max `occurred_at` reflected.

**Multi-epoch re-reflection:** in addition to the incremental forward pass (new
outcomes since the watermark), every Nth sweep (the *epoch* cadence) re-reflects a
**wider trailing window** so older trajectories are re-examined as the playbook
matures (ACE's compounding refinement). Re-reflected candidates dedupe/supersede
through reconcile — they do not duplicate. The epoch counter is a job marker;
idempotency comes from reconcile's content-hash + near-dup pre-filters, so a
re-run over the same records produces no new memories.

**Lifecycle of reflection memories (P4, §11 requirement):** `strategy`/
`failure_mode` memories decay (activity + wall-clock), supersede (a refined
strategy supersedes its predecessor via reconcile — reversible per D-017), and are
quarantine-eligible exactly like other derived memories. They are **not** verbatim
records (P1 untouched).

### 4. `internal/boot`: reconcile fan-in

Reconcile currently consumes a single channel (`extract.Downstream()`). Phase 19
adds a small **fan-in merge** so both the extract stage and the reflection sweep
emit `CandidateBatch` into the same reconcile workers (one reconcile core, two
producers — keeps trust gates/dedupe identical for both paths). `StartPipeline`
exposes the reflection→reconcile channel to the `lifecycle.Manager`. The eval
harness `server.go` reference wiring is updated to match, and the
`stageparity_test.go` substring list is extended (so the harness still mirrors
`StartPipeline`).

### Design decisions (resolved in this plan; filed as D-077)

| # | Decision | Resolution |
|---|----------|-----------|
| 1 | Trigger model | **Sweep-only**, reading outcomes from the store (not per-flush). |
| 2 | Trajectory unit | Records grouped by `(session_id, branch_id)` with a terminal outcome, ordered by `occurred_at`; success/failure contrast. |
| 3 | Reflection prompt/schema | **Dedicated** reflection prompt + schema + reflection-only kind enum; topic `ValidKinds` unchanged. |
| 4 | Kind seed weights | Applied in the reflection candidate constructor (seed importance/stability per kind; `TrustSource:"llm_reflected"`). |
| 5 | Cross-kind supersede | Reflection reconciliation restricts neighbors to `Kinds:["strategy","failure_mode"]` so strategies cannot supersede facts. |
| 6 | Re-reflection idempotency | Per-scope watermark + epoch counter via `job_markers`; reconcile pre-filters guarantee re-runs add nothing. |
| 7 | Reconcile fan-in | One reconcile core, two producers (extract + reflection) via a fan-in merge in `StartPipeline`. |
| 8 | Link source | Reuse the existing `reconciler` link source (no link-schema change); reflection origin is visible via `TrustSource:"llm_reflected"`. |

## Files added or changed

```text
internal/reflect/reflect.go         # new: trajectory assembler + candidate constructor
internal/reflect/schema.go          # new: reflectionSchema (draft-07, reflection kinds)
internal/reflect/prompt.go          # new: outcome-aware reflection prompt (golden)
internal/reflect/*_test.go          # new: schema/prompt goldens, assembler tables
internal/lifecycle/reflect.go       # new: runReflect sweep + watermark/epoch markers
internal/lifecycle/manager.go       # register sweep; Profile reflect knobs + defaults
internal/store/store.go             # + RecordStore.ListByOutcome
internal/store/sqlitestore/*.go     # ListByOutcome impl
internal/store/pgstore/*.go         # ListByOutcome impl
internal/store/migrations/*         # forward-only: idx_records_tenant_outcome_occurred
internal/store/conformance/*        # ListByOutcome conformance case
internal/boot/pipeline.go           # reconcile fan-in; wire reflection→reconcile
internal/config/*                   # reflection knobs + profile placement (D-034)
eval/harness/server.go              # mirror StartPipeline wiring (parity)
eval/harness/stageparity_test.go    # extend the mirrored-constructor list
test/integration/reflection_loop_test.go  # fleet-loop integration test
scripts/smoke/phase-19.sh           # new
docs/plans/phase-19-reflection.md   # this file
docs/decisions.md                   # D-077
docs/glossary.md                    # reflection, trajectory, re-reflection, llm_reflected
```

## Config keys added (D-034 — each ships with a tuned default + profile placement + docs + smoke)

| Key | Default | Profiles | Notes |
|-----|---------|----------|-------|
| `lifecycle.reflect_enabled` | profile-dependent | fleet: **on**; assistant/coding-agent: off | the fleet-learning loop is fleet-first |
| `lifecycle.reflect_interval` | `30m` (fleet) | longer/off elsewhere | jittered ticker base |
| `lifecycle.reflect_batch_size` | `200` | all | max outcome-tagged records per scope per sweep |
| `lifecycle.reflect_epoch_every` | `8` | all | every Nth sweep re-reflects the wider trailing window |

Zero-config start (D-034) is preserved: reflection is **off** unless the profile or
an explicit knob enables it; `stowage serve` with one secret env var is unaffected.

## Acceptance criteria (binding)

1. `ListByOutcome` exists on both store drivers, scope-parameterized (no unscoped
   variant — P3), passes the shared conformance suite under `-race`; the new index
   is used (EXPLAIN-verified on postgres).
2. The reflection sweep, given outcome-tagged success+failure trajectories,
   produces `strategy` and `failure_mode` candidate memories that commit through
   the **unchanged** reconcile flow (dedupe/trust/supersede); every mutation emits
   an event with a reason.
3. Reflection is **LLM-side** but `internal/playbook` stays gateway-free (the
   D-072 transitive no-gateway lint still passes); the reflection schema call is
   schema-constrained (§10) and routes through `gateway.Gateway` (P5) — proven by a
   golden wire/prompt test.
4. Re-reflection is **idempotent**: running the sweep twice over the same records
   commits no second copy (content-hash + near-dup pre-filters); the watermark
   advances; the epoch re-reflection pass supersedes rather than duplicates.
5. Reflection memories carry their lifecycle (decay/supersede/quarantine) and
   `TrustSource:"llm_reflected"` provenance; a refined strategy superseding its
   predecessor is rollback-reversible (D-017 round-trip).
6. Reflection candidates cannot supersede non-reflection kinds (neighbor query
   restricted to reflection kinds — AC-tested).
7. The eval-harness `server.go` mirrors the new `StartPipeline` wiring;
   `stageparity_test.go` passes.
8. **Fleet-loop integration test** (`test/integration`, real sqlite + mock
   gateway): ingest outcome-tagged trajectories → force a reflection sweep →
   `GET /v1/playbook` returns the produced strategies/failure-modes; identity/scope
   isolation holds; ≥1 failure mode covered (e.g. gateway error → dead-letter, no
   partial commit).
9. New config knobs ship with tuned defaults + profile placement + docs + smoke
   (D-034); zero-config start still smoke-green.
10. Gates: `make build`, `go test -race ./...`, `golangci-lint`, `gofmt -l .`
    empty, `make coverage`, `make preflight`, `make drift-audit`,
    `make check-mirror` — all green.

## Smoke script

`scripts/smoke/phase-19.sh` (SKIP-graceful until built):
- `OK` reflection sweep registered in the lifecycle manager (grep `runReflect`/`0x1406`).
- `OK` reflection kinds NOT in topic `ValidKinds`; ARE in the reflection schema.
- `OK` `internal/reflect` routes through `gateway.Gateway` (no provider SDK; P5 grep).
- `OK` reflection schema call is schema-constrained (§10 — Schema set).
- `OK` `ListByOutcome` present on both drivers (grep) + conformance test runs.
- `OK` reflection config knobs surfaced in `config explain`, default off for non-fleet.
- `OK` reflection unit tests (assembler/prompt/schema goldens) + integration test pass.
- `OK` `internal/playbook` transitive no-gateway lint still passes (D-072 unbroken).

## Test plan

- **Unit/golden:** reflection prompt assembly + schema (fixed trajectory → fixed
  prompt/schema); trajectory assembler tables (grouping, terminal-outcome pairing,
  no-outcome skip); seed-weight constructor.
- **Conformance:** `ListByOutcome` on both drivers (scope isolation, ordering,
  `since` filter, limit) under `-race`.
- **Idempotency:** sweep-twice → no duplicate memories; watermark advances; epoch
  re-reflection supersedes.
- **Integration (§17):** the fleet-loop test (real store + mock gateway, ≥1 failure
  mode) + a recorded-fixture test against the real reflection wire format (gateway
  `mock` is the sanctioned test driver; pair with one recorded real-shape fixture).
- **Reuse safety:** the reflection sweep + extract both feeding reconcile proven
  race-clean (`-race`) — two producers, one core.

## Risks & mitigations

- **Cross-kind supersede** (a strategy clobbering a fact) → neighbor query
  restricted to reflection kinds (decision 5) + AC-6 test.
- **Re-reflection runaway** (re-reflecting everything every tick) → watermark +
  epoch cadence + reconcile pre-filters (decision 6); `reflect_batch_size` caps
  per-sweep cost; a `log()` of records dropped past the cap (no silent truncation).
- **Reconcile fan-in race** → single reconcile core, channel fan-in, `-race` proof;
  drain order (lifecycle stopped before reconcile) preserved in `Drain`.
- **Cost** (reflection is a gateway call per trajectory batch) → off by default
  except the fleet profile; batch size + interval bound spend; metered/evented.
- **Prompt quality** (vague/restated-fact strategies) → golden prompt + the Phase
  20b gain-fleet harness measures whether reflection actually compounds; a negative
  gain is a tuning signal, not a silent ship.

## Out of scope / tracked follow-ups

- **Eval reader/judge abstention prompt tuning** (owner note, 2026-06-17): in the
  Phase 20 judged run the reader answered instead of abstaining on the abstention
  variant (`031748ae_abs`), which the judge correctly marked incorrect. This is
  **eval-harness prompt engineering** (the reader/judge prompts in
  `eval/harness/judge.go`), belongs to **Phase 20b / eval-tuning**, and is tracked
  there — not Phase 19.
- Per-kind scoring weights as a first-class engine feature (Phase 19 uses seed
  weights in the constructor; a general kind→weight table is a later refinement).
- A `reflector` link source (reflection rides the existing `reconciler` source;
  revisit only if a consumer needs to distinguish reflection-origin links).

## Glossary additions

- **Reflection (write-side)** — the outcome-aware gateway pass that distills
  `strategy`/`failure_mode` candidates from outcome-tagged trajectories (§6a.2);
  distinct from the LLM-free playbook *assembly* (§6a.3).
- **Trajectory (reflection)** — outcome-tagged records grouped by `(session_id,
  branch_id)` with a terminal outcome, the unit a reflection pass reflects over.
- **Re-reflection** — the multi-epoch lifecycle sweep that revisits older
  trajectories as the playbook matures; idempotent via reconcile pre-filters.
- **`llm_reflected`** — the trust source stamped on reflection-produced memories,
  distinguishing them from `llm_extracted` (topic extraction).

## Decisions filed

- **D-077** — Reflection write-side is a sweep-driven stage feeding the existing
  reconcile core (the 8 resolutions above): sweep-only trigger, `(session,branch)`
  trajectories, dedicated reflection prompt/schema/kinds, constructor seed weights +
  `llm_reflected` trust source, reflection-kind-restricted neighbors, watermark +
  epoch idempotency, reconcile fan-in, reused `reconciler` link source.
