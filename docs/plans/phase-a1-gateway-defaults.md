# Phase a1 — Gateway defaults: the real Bifrost/OpenRouter stack

- **Status:** shipped
- **Owning subsystem(s):** `internal/config`, `internal/boot`, `internal/gateway/bifrost`
- **RFC sections:** §9.4 (five-minute rule), §10 (gateway seam)
- **Depends on phases:** 04 (gateway seam), 09c (bifrost SDK driver, D-049), h7 (custom rerank, D-075)
- **Informing briefs:** `docs/research/01-predecessor-python.md` (no local models — the gateway seam is the single biggest deployment lever); `docs/research/06-mempalace.md` (single static binary + zero/env-driven config as the adoption differentiator)

## Goal

After this phase, `STOWAGE_GATEWAY_API_KEY=… stowage serve` is a working **real** server on
embedded SQLite — one secret, the full lane stack (completion + embedding + rerank) over
OpenRouter via the Bifrost SDK driver. The shipped default driver is no longer `mock`; a real
driver with no key fails loud at boot naming the missing secret, and `mock` remains the keyless
escape hatch for hermetic/offline runs. This closes the gap between RFC §9.4 / the
getting-started doc (which already promised Bifrost-by-default) and the code (which still
defaulted to `mock`).

## Brief findings incorporated

- **No local models; one gateway seam (brief 01).** The default points at a real provider
  through the gateway, never a local model — the binary stays CGo-free and model-free.
- **Zero/env-driven config as the adoption differentiator (brief 06).** One secret, tuned
  defaults for every other knob; `config explain` shows the full effective stack with provenance.

## Findings I'm departing from

- **Per-concern provider/key split, planned in a1 (brief instinct: "don't force one provider").**
  The motivating constraint — OpenRouter not serving embeddings — is false (`perplexity/
  pplx-embed-v1-0.6b` is eval-proven on OpenRouter). So per-concern keys are *optionality*, not
  required for the one-key start; deferred to follow-up **a1b** so a1 ships clean. Filed under
  D-131's deviation note.

## Design

`internal/config/config.go` `Defaults()` gateway block flips to `driver=bifrost`,
`provider=openrouter`, the live-validated embed/rerank ids from
`internal/gateway/bifrost/live_test.go` (`embed_model=perplexity/pplx-embed-v1-0.6b`,
`embed_dims=1024`, `rerank_model=cohere/rerank-4-fast`), and the owner-chosen baseline learner
`model=openai/gpt-5.4-nano`. `base_url` and `rerank_base_url` ship **empty**: OpenRouter's
provider appends `/v1/…` (embed+complete need `…/api`) and the auto-wired Cohere-shape rerank
provider (D-075) needs `…/api/v1/rerank`, but those URLs are supplied by the bifrost driver
(`applyProviderBaseDefaults` in `account.go`, gated on `provider==openrouter`) — NOT baked into
config — so empty keeps its "native endpoint / reuse base_url" meaning for every other provider
and a non-OpenRouter bifrost config is never silently misrouted to openrouter.ai (P5: the driver
owns wire details).

`FillZeroDefaults` (the embedded-SDK defaults layer) fills `provider` (so the all-defaults
embedded bifrost stack validates D-049) but deliberately NOT `base_url`/`rerank_base_url` (empty
must keep its native-endpoint meaning; the driver supplies OpenRouter's).

`internal/boot/boot.go`: gateway `Open` is already fatal; when the driver is not `mock`, the
boot error appends the five-minute-minimum hint (`set STOWAGE_GATEWAY_API_KEY`) and the `mock`
escape hatch. The driver layer (`bifrost/account.go`) already names the exact env var and fails
closed — unchanged. `Probe` failure stays a degraded warning (D-036), never a boot error.

**Migration.** Flipping the default means a real gateway boots by default; every serve-booting
smoke already writes `gateway: driver: mock` (or sets `STOWAGE_GATEWAY_DRIVER=mock`), and all
unit tests construct gateway config explicitly — so the blast radius is the explain golden and
the rerank-base-url default test, both updated.

## Files added or changed

```text
internal/config/config.go                     # Defaults() gateway block; FillZeroDefaults gateway fills
internal/config/config_test.go                # TestRerankBaseURLDefault (was …DefaultEmpty)
internal/config/testdata/explain_default.golden  # regenerated
internal/boot/boot.go                         # five-minute-minimum hint on gateway open failure
scripts/smoke/phase-a1.sh                     # new smoke (5 ACs)
docs/plans/phase-a1-gateway-defaults.md       # this plan
docs/plans/README.md                          # Adoption & ergonomics track section
docs/decisions.md                             # D-131
docs/glossary.md                              # five-minute minimum; mock escape hatch
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| _(none)_ | — | a1 changes **default values** of existing gateway keys only; no new keys. New keys (per-concern + per-stage) arrive in a1b / a2. |

## Acceptance criteria (binding)

1. `Defaults()` returns `driver=bifrost`, `provider=openrouter`; `config explain` shows them with origin `default`.
2. `config explain` shows the full-stack ids (`openai/gpt-5.4-nano`, `perplexity/pplx-embed-v1-0.6b`, `embed_dims=1024`) and `base_url`/`rerank_base_url` default empty (driver-supplied for OpenRouter).
3. `stowage serve` with `STOWAGE_GATEWAY_API_KEY` unset and a real driver exits non-zero with a message naming the missing var.
4. `STOWAGE_GATEWAY_DRIVER=mock` boots `serve` with no key (escape hatch).
5. The config package validates the new defaults (explain golden + rerank default + bifrost-provider rule); all prior smokes still pass.

## Smoke script

`scripts/smoke/phase-a1.sh` — build; AC-1 driver/provider defaults; AC-2 full-stack ids; AC-3
keyless real driver fails loud; AC-4 mock keyless escape hatch; AC-5 config default tests.

## Test plan

Unit/golden: `internal/config` (regenerated explain golden, updated rerank-default test, existing
bifrost-provider validation). Manual/live: the bifrost `live_test.go` already exercises the exact
default config against real OpenRouter (operator-gated, not CI). Full `go test ./...` and the
serve-booting smokes (05, 09, 11, 16, h6) verified green under the flipped default.

## Risks & mitigations

- **Default flip breaks a keyless boot somewhere** → audited: all serve smokes set `mock`; unit
  tests set gateway config explicitly; `mock` remains first-class. Verified green.
- **Silent misroute if base_url were a baked default** → avoided: `base_url`/`rerank_base_url`
  default empty and the driver supplies OpenRouter's only when `provider==openrouter`, so a
  non-OpenRouter bifrost operator keeps native-endpoint semantics and is never misrouted.

## Glossary additions

- **Five-minute minimum** — the single secret (`STOWAGE_GATEWAY_API_KEY`) that a real-driver
  `stowage serve` needs to boot; missing it fails loud (D-131, RFC §9.4).
- **`mock` escape hatch** — `STOWAGE_GATEWAY_DRIVER=mock` boots a keyless, no-provider gateway for
  hermetic tests/offline runs (D-131).

## Decisions filed

- D-131: Adoption & ergonomics track; gateway defaults to the real Bifrost/OpenRouter stack
  (per-concern keys deferred to a1b).
