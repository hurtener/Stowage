# Phase h7 — bifrost custom-provider rerank (full OpenRouter stack) + benchmark rebase

- **Status:** shipped
- **Owning subsystem(s):** `internal/gateway/bifrost` (account + driver), `internal/config` (validation/docs), `eval/harness` (bench rebase)
- **RFC sections:** §7 (gateway seam, one intelligence seam P5), §12 (eval/benchmark)
- **Depends on phases:** 04/09c gateway (D-040, D-049) — shipped
- **Informing briefs:** 01 (gateway pain points), 06 (benchmark-led positioning)
- **Program:** post-D-067 follow-up (operator-driven). New decision: **D-075**.

## Goal

When this phase is done, the **bifrost** driver can rerank over OpenRouter — so a
single bifrost gateway runs the *whole* stack (embed + complete + rerank) on
OpenRouter with one key — and the full-mode benchmark is rebased onto it with the
cheaper operator-chosen models. This closes the gap proven during investigation:
bifrost's built-in `openrouter` provider doesn't implement rerank, but a Bifrost
**custom provider** (`BaseProviderType: Cohere`, `RequestPathOverrides{rerank:
"/rerank"}`) reranks against OpenRouter's `/api/v1/rerank` successfully (verified
live: `cohere/rerank-4-fast` returned real scores). Today Stowage's driver can't
express that; this phase wires it.

## Brief findings incorporated

- **Empirical (2026-06-17):** bifrost+OpenRouter embed works (`perplexity/pplx-embed-v1-0.6b`,
  1024-dim); rerank via the `openrouter` provider returns "not supported"; rerank
  via a custom Cohere-based provider at `https://openrouter.ai/api/v1` + path
  `/rerank` **works** (Index 0, Score 0.7143 for a Go query). One Bifrost `Account`
  can expose multiple providers, so embed/complete→`openrouter`, rerank→custom.

## Findings I'm departing from

- The earlier full-mode REPORT used `openaicompat`. We switch the bench to
  **bifrost** (operator preference, Harbor-parity D-049) now that bifrost reranks.
  `openaicompat` remains a valid driver (and its rerank live test stays).

## Design

### 1. Driver: auto-wire a Cohere-shape rerank provider (targeted)
Native-rerank Bifrost providers are `{cohere, vllm, bedrock, vertex}`. In
`internal/gateway/bifrost/account.go`:
- If `cfg.RerankModel != ""` **and** the primary `cfg.Provider` is **not** a
  native-rerank provider, the `Account` ALSO exposes a synthetic custom provider
  `stowage-rerank`:
  - `GetConfiguredProviders` → `[primary, "stowage-rerank"]`.
  - `GetConfigForProvider("stowage-rerank")` → `ProviderConfig{ NetworkConfig{BaseURL: rerankBaseURL}, CustomProviderConfig{ BaseProviderType: Cohere, AllowedRequests{Rerank:true}, RequestPathOverrides{RerankRequest: "/rerank"} } }`.
  - `GetKeysForProvider("stowage-rerank")` → the same key, `Models: {"*"}`.
- `rerankBaseURL` defaults to `cfg.BaseURL` (OpenRouter case: same host, `/rerank`).
- If the primary provider **is** native-rerank (e.g. `cohere`), no custom provider
  is added — rerank routes to the primary as today.

In `driver.go`, `Rerank` sets `bfReq.Provider` to the rerank provider (the custom
`stowage-rerank` when wired, else `d.provider`). A `rerankProvider` field on the
Driver records which to use. Embed/Complete are unchanged (primary provider).

**Graceful degradation preserved (D-036):** if the backend doesn't actually expose
a Cohere-shape `/rerank` (e.g. a future non-OpenRouter, non-native provider), the
call errors → existing `DegradedRerank` path. A boot log notes when the custom
rerank provider is auto-wired, so it's never silent.

### 2. Config (minimal — D-034)
No new **required** knob. Reuse `gateway.rerank_model` (already exists) + `gateway.base_url`.
Add ONE optional knob `gateway.rerank_base_url` (default empty → use `base_url`)
for the rare case rerank lives on a different host; documented + in `config explain`
+ example. The `/rerank` path is a constant for the auto-wired Cohere-shape provider.

### 3. Benchmark rebase
Point the full-mode eval (`eval/harness`, `fullmode_test.go`) at bifrost + the
cheaper models:
- `STOWAGE_EVAL_GATEWAY=bifrost`, `STOWAGE_EVAL_PROVIDER=openrouter`,
  `STOWAGE_EVAL_BASE_URL=https://openrouter.ai/api/v1`,
  `STOWAGE_EVAL_MODEL=inception/mercury-2` (memory formation / "learner"),
  `STOWAGE_EVAL_EMBED_MODEL=perplexity/pplx-embed-v1-0.6b`, `STOWAGE_EVAL_EMBED_DIMS=1024`,
  `STOWAGE_EVAL_RERANK_MODEL=cohere/rerank-4-fast`.
- **Wire rerank into the harness** (it currently never does): the harness retriever
  must use the **precise** profile (`EnableRerank`) and `WithRerankModel(rerankModel)`
  so the bench actually measures the cross-encoder, with `DegradedRerank` surfaced.
  Add a `STOWAGE_EVAL_PROFILE`/rerank toggle to `RunConfig`.
- Update `eval/harness/server.go` to read these envs (it already has the override
  hook) + plumb the rerank model; update `eval/REPORT.md` + `fullmode_test.go`
  header docs to the new bifrost config and models.

## Files added or changed

```text
internal/gateway/bifrost/account.go      # multi-provider account; auto-wire stowage-rerank custom provider
internal/gateway/bifrost/driver.go       # route Rerank to the rerank provider; record rerankProvider
internal/gateway/bifrost/account_test.go # fake-client: custom rerank provider wired iff non-native + rerank_model
internal/gateway/bifrost/live_test.go    # embed PASS + rerank PASS (auto-wired custom provider over OpenRouter)
internal/config/config.go                # optional gateway.rerank_base_url + validation + explain + example
eval/harness/server.go + runner.go       # bifrost config + rerank wiring (precise + WithRerankModel)
eval/harness/fullmode_test.go            # header docs → bifrost + cheap models
eval/REPORT.md                           # documented bench config updated
scripts/smoke/phase-h7.sh                # NEW
docs/plans/README.md ; docs/glossary.md
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `gateway.rerank_base_url` | `""` (→ use `base_url`) | Optional: host for the auto-wired Cohere-shape rerank provider when it differs from `base_url`. Documented + `config explain` + example (D-034). |

## Acceptance criteria (binding)

1. With `driver=bifrost`, `provider=openrouter`, `rerank_model` set, the driver
   reranks successfully over OpenRouter (live test: real scores, sorted).
2. Embed + Complete still route to the primary provider unchanged; a native-rerank
   primary (e.g. `cohere`) does NOT get a custom provider (rerank → primary).
3. The auto-wire is logged at boot (never silent); on a backend without Cohere-shape
   rerank the call degrades (`DegradedRerank`), not panics.
4. `internal/gateway` is the only package touched for provider wiring (P5); metering
   + circuit breaker still apply to the custom rerank provider.
5. **Bench rebased:** full-mode eval runs on bifrost + the three cheap models with
   rerank ENABLED (precise profile + rerank model wired); `eval/REPORT.md` +
   `fullmode_test.go` docs updated; a fresh full-mode run is recorded (operator-run,
   needs the key — not CI).
6. New config key has default + docs + `config explain` + same-PR smoke (D-034/§13).

## Smoke script

`scripts/smoke/phase-h7.sh` — build; assert config validation accepts/【rejects】
`gateway.rerank_base_url`; assert (with the mock gateway) the bifrost driver
constructs with a rerank provider wired when provider is non-native + rerank_model
set (unit-level; the live rerank is the `live` test, not the smoke). SKIP-graceful.

## Test plan

- **Live (`-tags=live`, env-gated on `OPENROUTER_API_KEY`):** `TestLiveBifrost_Embed`
  (1024-dim) + `TestLiveBifrost_Rerank` (auto-wired custom provider → real scores).
- **Unit (fake client):** account wires `stowage-rerank` iff non-native primary +
  rerank_model; native primary does not; driver routes Rerank to the right provider.
- **Bench:** a recorded full-mode run on the new config (operator-run).

## Risks & mitigations

- *Risk:* auto-wiring for a provider with no Cohere-shape rerank silently degrades.
  *Mitigation:* boot log on auto-wire; `DegradedRerank` surfaced; documented as
  OpenRouter-targeted.
- *Risk:* knob sprawl. *Mitigation:* one optional knob; everything else reuses
  existing config.
- *Risk:* bench numbers shift (new cheaper models). *Mitigation:* this is the point
  (re-baseline); record the run + note model deltas in REPORT.

## Glossary additions

- **Auto-wired rerank provider** — a synthetic Bifrost custom provider
  (`BaseProviderType=Cohere`) Stowage adds so a non-native-rerank primary (e.g.
  OpenRouter) can serve the cross-encoder rerank over its Cohere-shape `/rerank`.

## Decisions filed

- **D-075** — the bifrost driver auto-wires a Cohere-shape custom rerank provider
  (`BaseProviderType=Cohere`, path `/rerank`, same key/base) when the primary
  provider is not native-rerank and a `rerank_model` is configured, enabling a
  full OpenRouter stack on bifrost; the benchmark is rebased onto bifrost with the
  operator's cheaper models (inception/mercury-2, perplexity/pplx-embed-v1-0.6b@1024,
  cohere/rerank-4-fast) and rerank enabled in the harness.
