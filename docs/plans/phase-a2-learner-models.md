# Phase a2 — Per-learner-stage model selection

- **Status:** shipped
- **Owning subsystem(s):** `internal/config`, `internal/pipeline`, `internal/reconcile`, `internal/reflect`, `internal/lifecycle`, `internal/boot`
- **RFC sections:** §9.4 (knob guardrail), §10 (gateway seam)
- **Depends on phases:** a1 (gateway defaults), 08 (reconcile), 19 (reflect), D-100 (per-call model), D-128 (per-stage effort)
- **Informing briefs:** `docs/research/01-predecessor-python.md` (knowledge kinds / distinct learner roles); `docs/research/05-ace.md` (Generator/Reflector/Curator are distinct roles that may warrant distinct models)

## Goal

After this phase, each learner LLM stage — extract, reconcile, reflect — can run a
different completion model, set via `gateway.extract_model` / `gateway.reconcile_model` /
`gateway.reflect_model`, each falling back to `gateway.model` when unset. A cheap, fast
extractor can run alongside a stronger reconciler/reflector through a single gateway, with
zero new behavior when the keys are left empty (the default).

## Brief findings incorporated

- **Distinct learner roles (brief 05 ACE).** Generator/Reflector/Curator are separate roles;
  exposing a per-stage model lets each run at the right capability/cost point.
- **Knowledge kinds & cost discipline (brief 01).** The predecessor mixed cheap and expensive
  LLM work on one model; a per-stage model is the cost lever without a second gateway.

## Findings I'm departing from

- None. This extends the existing per-call model seam (D-100) and per-stage effort pattern
  (D-128) rather than introducing a new mechanism.

## Design

`CompleteRequest.Model` (D-100) already overrides the configured model per call and is honored
by the bifrost and openaicompat drivers. a2 plumbs a per-stage model to that field:

- **Extract** (`internal/pipeline/extract.go`) and **reconcile** (`internal/reconcile/reconcile.go`)
  are stage structs — each gains a `model` field + `SetModel(string)` setter next to
  `SetReasoningEffort`, and sets `CompleteRequest.Model = stage.model` (empty → gateway.model).
- **Reflect** (`internal/reflect/reflect.go`) is a free function — it gains a trailing `model`
  argument; the lifecycle `Manager` holds `reflectModel` (`SetReflectModel`) and passes it from
  `runReflect`. The eval harness and unit callers pass `""`.
- **Production wiring** (`internal/boot/pipeline.go`): `extract.SetModel(cfg.Gateway.ExtractModel)`,
  `rec.SetModel(cfg.Gateway.ReconcileModel)`, `lc.SetReflectModel(cfg.Gateway.ReflectModel)` — so
  the production pipeline applies the per-stage models, not just the eval harness.
- **Config** (`internal/config/config.go`): three `yaml` fields on `GatewayConfig`, registered in
  `allKeys`, `envKeys`, `getByPath`, `setByPath`. Default empty; not secret; no `Validate` rule
  (a free-text model string, empty allowed). `FillZeroDefaults` needs no entry — empty is the
  intended default and the fallback is honored downstream.

## Files added or changed

```text
internal/config/config.go                       # 3 GatewayConfig fields + registry sites
internal/config/testdata/explain_default.golden # regenerated (3 keys, empty)
internal/pipeline/extract.go                    # model field + SetModel + request.Model
internal/pipeline/extract_model_test.go         # wiring test
internal/reconcile/reconcile.go                 # model field + SetModel + request.Model
internal/reconcile/reconcile_model_test.go      # wiring test
internal/reflect/reflect.go                     # model param + request.Model
internal/reflect/reflect_test.go                # wiring test + caller updates
internal/lifecycle/{manager.go,reflect.go}      # reflectModel + SetReflectModel + pass-through
internal/boot/pipeline.go                       # wire per-stage models from config
eval/harness/adapt.go                           # Reflect("") caller update
scripts/smoke/phase-a2.sh                        # new smoke
docs/plans/phase-a2-learner-models.md            # this plan
docs/decisions.md                                # D-132
docs/glossary.md                                 # per-learner-stage model
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `gateway.extract_model` | `""` | Override completion model for the extract stage; empty → `gateway.model`. Env: `STOWAGE_GATEWAY_EXTRACT_MODEL`. |
| `gateway.reconcile_model` | `""` | Override for the reconcile decision; empty → `gateway.model`. Env: `STOWAGE_GATEWAY_RECONCILE_MODEL`. |
| `gateway.reflect_model` | `""` | Override for the reflection sweep; empty → `gateway.model`. Env: `STOWAGE_GATEWAY_REFLECT_MODEL`. |

## Acceptance criteria (binding)

1. Unset → extract/reconcile/reflect call with `CompleteRequest.Model == ""` (gateway uses its configured model) — proven by the three `*_ModelWiring` tests.
2. A set per-stage model (field/env) appears on that stage's `CompleteRequest.Model`; other stages are unaffected.
3. `config explain` surfaces all three keys with provenance; an env override resolves to origin `env`.
4. The production boot path applies the per-stage models (visible `SetModel`/`SetReflectModel` wiring in `boot.StartPipeline`); the stage tests prove the model then reaches `Complete`. (A full boot integration assertion is intentionally omitted — the wiring is three trivial calls and the stage tests cover the behavior without flush-timing flakiness.)

## Smoke script

`scripts/smoke/phase-a2.sh` — AC-1 keys surface empty; AC-2 env override; AC-3 the three
model-wiring unit tests pass.

## Test plan

Unit: `TestExtract_ModelWiring`, `TestStageModelWiring`, `TestReflect_ModelWiring` (set vs
empty). Golden: regenerated `explain_default.golden`. Existing extract/reconcile/reflect/
lifecycle/boot/eval suites pass unchanged under the new signature.

## Risks & mitigations

- **Knob proliferation (3 keys)** vs D-034 → all default-empty (inherit), so zero-config is
  unchanged; justified by a real operator need (a model choice a profile can't absorb).
- **Reflect signature churn** (5 call sites) → updated in the same change; `""` preserves prior
  behavior for the eval harness and unit callers.

## Glossary additions

- **Per-learner-stage model** — a per-stage completion model override falling back to
  `gateway.model` (D-132).

## Decisions filed

- D-132: Per-learner-stage completion model (extract / reconcile / reflect).
