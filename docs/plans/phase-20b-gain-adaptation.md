# Phase 20b — Gain harness + online-adaptation (RFC §12)

- **Status:** implemented (see "As-built deviations")
- **Owning subsystem(s):** `eval/gain`, `eval/harness`, `eval/REPORT.md` (no
  shipped-binary surface change)
- **RFC sections:** §12 (the gain harness — memory-on-vs-off delta; the
  online-adaptation scenarios via the reflection→playbook loop), §10 (gateway
  schema-constrained), §6a (reflection→playbook loop), §7/P5 (gateway seam)
- **Depends on phases:** 13 (eval harness + gain scenario skeleton), 18 (SDK), 19
  (reflection write-side — the online-adaptation loop), 20 (reader+judge — the
  scoring substrate)
- **Informing briefs:** 04 (CL-Bench — the gain metric + failure-mode taxonomy),
  05 (ACE — online adaptation, compounding improvement), 06 (mempalace — positioning)

## Goal

When this phase is done, the eval harness can measure **gain** — does memory improve
task completion? — by running each scripted scenario twice through the Phase 20
reader+judge: once with retrieved memory context (**on**) and once with none
(**off**), reporting the per-scenario and aggregate quality delta. It can also run
**online-adaptation** scenarios: sequential tasks where the Phase 19
reflection→playbook loop accumulates strategies between tasks, measuring whether
later tasks improve as the playbook matures (ACE's compounding result). Both are
opt-in, full-mode-only, operator-run; the CI mock run stays deterministic and
LLM-free. The aggregate gain becomes a **release gate** (RFC §12: negative gain on
the standard scenarios fails release) — operator-run, never CI.

## Brief findings incorporated

- **Brief 04 (CL-Bench):** the gain metric is the memory-on vs memory-off delta on
  scripted multi-session scenarios; negative gain is a release-blocking signal.
- **Brief 05 (ACE):** online adaptation — contexts evolve *during* evaluation via
  the reflection→playbook loop; the measure is *compounding* improvement across a
  task sequence, not single-shot accuracy. We drive this with the Phase 19
  reflection sweep + the deterministic playbook injected into the next task.

## Findings I'm departing from

- **RFC §12 says the gain harness uses "a Harbor fleet as the agent loop (§10)."**
  Harbor is a **separate codebase** (the agent framework in the ecosystem) and is
  not a dependency of this repo, so Stowage cannot import it. Departure: the gain
  harness uses the **Phase 20 eval reader as the stand-in agent loop** — the reader
  answers the scenario's eval question with vs without memory context, and the
  judge scores both. This measures exactly the RFC's quantity (does memory improve
  the agent's answer?) without coupling the eval to Harbor's wire protocol. When
  Harbor lands, a Harbor-driven gain runner can replace the reader stand-in behind
  the same `GainResult` contract. Filed as **D-078**.

## Design

### Gain harness (`eval/harness/gain.go`)

Per `gain.Scenario` (the Phase-13 format: turns + eval_question + expected_answer):

1. Ingest the scenario's turns as records under a fresh session scope; flush;
   `WaitForQuiescence` (real extraction → reconcile → memories).
2. Retrieve for `eval_question` (reuse `Runner.scoreQuestion` → retrieved item
   contents = the **on** context).
3. **off:** `JudgeQuestion(gw, eval_question, expected, nil)` → `VerdictOff`.
   **on:** `JudgeQuestion(gw, eval_question, expected, items)` → `VerdictOn`.
4. `gain = quality(VerdictOn) − quality(VerdictOff)`, where
   `quality(correct)=1, quality(partial)=0.5, else 0`.

Aggregate: mean gain across scenarios + the count of non-negative scenarios. The
release gate asserts mean gain ≥ 0 (operator-run). Both `JudgeQuestion` calls are
schema-constrained through the gateway seam (§10/P5) — reused verbatim from Phase 20.

### Online-adaptation harness (`eval/harness/adapt.go`)

An `AdaptScenario` is an ordered list of `AdaptTask`s (each: turns, a task outcome
`success`/`failure`, an eval_question + expected_answer). The runner, per task in
order:

1. Ingest the task's turns (outcome-tagged) under the scope; flush; settle.
2. Run the **reflection sweep** (Phase 19) over the accumulated outcomes
   (`lifecycle.Manager` with `SetReflection`, forced via `RunForce`); settle the
   reconcile of any reflection candidates.
3. Assemble the **playbook** (`internal/playbook.Assemble`) for the scope and inject
   it as additional reader context.
4. `JudgeQuestion(gw, eval_question, expected, playbook+retrieved)` → quality_t.

Report the quality trajectory across tasks; compounding = later tasks ≥ earlier as
the playbook grows. This is the exploratory ACE measurement; it is reported, not
release-gated (the gain delta is the gate).

### CI vs operator-run

- **CI (deterministic, no LLM):** `gain_test.go` tests the pure pieces
  (`quality`, `AggregateGain`, `scenarioToFixture`) and a fakeGateway-driven
  on-vs-off delta (no live model). The committed gain CI metric is **not** a paid
  call; `make eval-ci` is unchanged.
- **Operator-run (full mode, paid):** `gainmode_test.go` /
  `adaptmode_test.go` (build tag `fullmode`, gated on `STOWAGE_EVAL_GAIN`) run the
  full loop and write results; the negative-gain release gate lives here.

## Files added or changed

```text
eval/gain/scenario.go            # + AdaptScenario / AdaptTask types
eval/gain/scenarios/*.json       # (existing 3 gain seeds reused)
eval/gain/adapt/*.json           # new: online-adaptation seed scenario(s)
eval/harness/gain.go             # GainResult, quality, AggregateGain, scenarioToFixture, RunGainScenario
eval/harness/gain_test.go        # CI: pure + fakeGateway on/off delta
eval/harness/gainmode_test.go    # fullmode: gain over scenarios + release gate (STOWAGE_EVAL_GAIN)
eval/harness/adapt.go            # RunAdaptScenario (reflection→playbook compounding)
eval/harness/adaptmode_test.go   # fullmode: online-adaptation run
eval/REPORT.md                   # gain + online-adaptation sections (operator-run)
scripts/smoke/phase-20b.sh       # new
scripts/coverage.json            # (eval/harness already at 0 threshold — eval tooling)
docs/plans/phase-20b-gain-adaptation.md
docs/decisions.md                # D-078
docs/glossary.md                 # gain, online adaptation, memory-on/off
```

## Config keys added

None — driven by `STOWAGE_EVAL_*` test/harness env vars (the established full-mode
convention). New: `STOWAGE_EVAL_GAIN` (opt-in, full mode only) enables the gain +
online-adaptation runs. No `internal/config` profile knob (D-034 not applicable).

## Acceptance criteria (binding)

1. The gain runner computes `QualityOff`, `QualityOn`, and `Gain` per scenario via
   the Phase-20 reader+judge; both judge calls are schema-constrained (§10) and
   route through `gateway.Gateway` (P5); no provider SDK under `eval/`.
2. CI stays deterministic + LLM-free: `make eval-ci` unchanged; the gain CI tests
   use pure logic + a fakeGateway (no live model); the gain/adaptation full runs are
   opt-in (`STOWAGE_EVAL_GAIN`) and never run in CI.
3. `quality`/`AggregateGain` are pure + table-tested; a fakeGateway test proves a
   positive gain when on=correct & off=incorrect, and zero when both agree.
4. The online-adaptation runner wires the Phase-19 reflection sweep between
   sequential tasks and injects the assembled playbook into the next task's reader
   context; a deterministic wiring test (fakeGateway / mock) proves the loop runs.
5. Operator full-mode run produces gain + adaptation numbers committed to
   `eval/REPORT.md` with one-command reproduction; the negative-gain release gate
   (mean gain ≥ 0) is asserted in the operator path.
6. Gates: `make build`, `go test -race ./...`, `golangci-lint`, `gofmt -l .` empty,
   `make coverage`, `make preflight`, `make drift-audit`, `make check-mirror` green;
   `go vet -tags=fullmode ./eval/harness/` compiles.

## Smoke script

`scripts/smoke/phase-20b.sh` (SKIP-graceful until built):
- `OK` gain runner present + schema-constrained judge reused (grep).
- `OK` no provider SDK under `eval/` (P5).
- `OK` gain pure-logic + fakeGateway tests pass.
- `OK` online-adaptation runner wires reflection + playbook (grep + test).
- `OK` full-mode build compiles (`go vet -tags=fullmode`).
- `OK` `make eval-ci` still green (deterministic CI unaffected).

## Test plan

- **Unit/table:** `quality(verdict)`, `AggregateGain`, `scenarioToFixture`.
- **fakeGateway:** on-vs-off gain delta (correct-on/incorrect-off ⇒ gain=1;
  agree ⇒ 0); adaptation loop runs and injects playbook (deterministic).
- **Compile:** `go vet -tags=fullmode ./eval/harness/`.
- **Operator (not CI):** the `STOWAGE_EVAL_GAIN=1` full run produces REPORT numbers.

## Risks & mitigations

- **Spend / key** → opt-in + operator-run only; CI never sets `STOWAGE_EVAL_GATEWAY`.
- **Noisy single-scenario gain** (one judge call each) → report per-scenario +
  aggregate; the gate is on the mean; a 2-of-3 judge vote is a noted future
  hardening.
- **Online-adaptation is operator-only + exploratory** → it is reported, not
  release-gated; the deterministic wiring test guards the plumbing, the real
  compounding signal is an operator observation.
- **Reader memory-off leakage** (the off run must truly see no context) →
  `JudgeQuestion(..., nil)` passes an empty context block; the reader prompt renders
  "(no memories retrieved)".

## Glossary additions

- **Gain** — the memory-on-vs-off quality delta on a scripted scenario, scored by
  the reader+judge; the RFC §12 release-gate metric (negative gain fails release).
- **Online adaptation** — measuring compounding improvement across a sequential task
  run as the reflection→playbook loop accumulates strategies (ACE, §6a/§12).
- **Memory-on / memory-off** — the two conditions of a gain run: the reader answers
  with retrieved memory context vs with none.

## Decisions filed

- **D-078** — The gain harness uses the Phase-20 eval reader as the agent-loop
  stand-in (Harbor is a separate codebase, not a dependency); the gain metric
  (memory-on-vs-off reader+judge delta) is the RFC §12 release gate, asserted
  operator-run (never CI — no paid LLM in CI); online-adaptation is driven by the
  Phase-19 reflection→playbook loop and is reported, not gated.
