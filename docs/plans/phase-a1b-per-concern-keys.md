# Phase a1b — Per-concern provider / key / base_url (embed + rerank)

- **Status:** shipped
- **Owning subsystem(s):** `internal/config`, `internal/gateway/bifrost`
- **RFC sections:** §10 (gateway seam), §9.4 (knob guardrail)
- **Depends on phases:** a1 (gateway defaults + driver base-URL defaulting), 09c (bifrost SDK driver, D-049), h7 (custom rerank, D-075)
- **Informing briefs:** `docs/research/01-predecessor-python.md` (no local models — the gateway seam is the provider boundary); `docs/research/02-predecessor-ccmem.md` (don't force one provider; small but honest config)

## Goal

After this phase, an operator can point the **embedding** and **rerank** lanes at a different
provider/key/base_url than the primary completion provider — "three providers for three
things" — instead of sharing one `gateway.api_key`. Each per-concern setting is optional and
**inherits the primary** when empty, so the one-key OpenRouter default (a1) is unchanged. This
delivers the optionality a1 deferred once OpenRouter proved one key covers all three lanes
(D-131 deviation note).

## Brief findings incorporated

- **Don't force one provider (brief 02).** The seam must let embed/rerank use a different
  provider+credential than completion; hardcoding one provider is the anti-pattern.
- **Gateway is the provider boundary (brief 01).** All provider/key routing stays inside
  `internal/gateway/bifrost` (P5); config only carries references.

## Findings I'm departing from

- **"Same provider name, different key" is out of scope.** Bifrost's `Account` is keyed by
  provider *name* (`GetKeysForProvider(provider)`), and both Stowage keys are unrestricted
  (`Models: ["*"]`), so two keys for the same provider name can't be disambiguated by model.
  The supported (and requested) shape is a **distinct provider name** per concern. Documented in
  D-134; an operator wanting a different key for the same provider uses the primary key.

## Design

### Config (`internal/config/config.go`)

Add five optional `GatewayConfig` fields, all default empty (inherit primary):

| Field | yaml | Env | Secret | Inherit-on-empty |
|---|---|---|---|---|
| `EmbedProvider` | `embed_provider` | `STOWAGE_GATEWAY_EMBED_PROVIDER` | no | → `provider` |
| `EmbedAPIKey` | `embed_api_key` | (none — `env.` ref) | **yes** | → `api_key` |
| `EmbedBaseURL` | `embed_base_url` | `STOWAGE_GATEWAY_EMBED_BASE_URL` | no | → driver default for the embed provider |
| `RerankProvider` | `rerank_provider` | `STOWAGE_GATEWAY_RERANK_PROVIDER` | no | → primary (auto-wired custom rerank, D-075) |
| `RerankAPIKey` | `rerank_api_key` | (none — `env.` ref) | **yes** | → `api_key` |

(`rerank_base_url` already exists.) Register each in `allKeys`, `envKeys` (the two non-secret
providers + embed_base_url; the two `*_api_key` are `env.`-ref secrets set via config file like
`gateway.api_key`, not an env override), `getByPath`, `setByPath`. Add the two `*_api_key` to
`secretKeyPaths`, and to `Validate` (must use `env.` indirection like `gateway.api_key`).
`FillZeroDefaults` does NOT fill them (empty = inherit). No profile placement (gateway config is
not profile-varying, same precedent as `rerank_base_url`).

### Driver routing (`internal/gateway/bifrost/`)

The `Account` becomes multi-provider. A small resolved view computed in `newAccount`:

- **primary** = `provider` / resolved `api_key` / `base_url` (with the a1 OpenRouter base default).
  Serves Complete always; Embed and Rerank when not overridden.
- **embed provider** (only when `embed_provider != "" && embed_provider != provider`): a separate
  entry keyed by `embed_provider`, key = `embed_api_key` (fallback primary key), base =
  `embed_base_url` (fallback the driver's provider default, e.g. OpenRouter `…/api`). The
  `Driver` gains an `embedProvider` field; `Embed` routes `translateEmbedRequest(d.embedProvider,
  cfg.EmbedModel, …)` instead of `d.provider`.
- **rerank provider**: extend the existing D-075 wiring. The rerank provider's KEY becomes
  `rerank_api_key` (fallback primary key) — so the auto-wired Cohere-shape custom rerank, or an
  explicit `rerank_provider`, can carry its own credential. `rerank_provider` set to a native
  provider routes natively; empty keeps today's behavior exactly.

`Account.GetConfiguredProviders / GetKeysForProvider / GetConfigForProvider` return the deduped
set of {primary, embed?, rerank?} with each entry's own key + ProviderConfig. Dedup by provider
name so a per-concern provider equal to the primary collapses to the primary entry (no behavior
change, no double worker pool).

### Fallback invariants

- All-empty → byte-identical wiring to a1 (one provider, one key). Proven by a test asserting
  `GetConfiguredProviders` returns exactly `[primary]` (+ auto-wired rerank when applicable).
- `embed_api_key`/`rerank_api_key` empty → the concern uses the primary key.

## Files added or changed

```text
internal/config/config.go                       # 5 fields + registry + secretKeyPaths + Validate
internal/config/testdata/explain_default.golden # regenerated (5 keys, empty; secrets redacted)
internal/gateway/bifrost/account.go             # multi-provider resolution (embed + rerank key)
internal/gateway/bifrost/driver.go              # embedProvider field + Embed routing
internal/gateway/bifrost/account_test.go        # fallback + distinct-provider routing tests
internal/gateway/bifrost/live_test.go           # (optional) split-provider live test, key-gated
scripts/smoke/phase-a1b.sh                       # new smoke
docs/plans/phase-a1b-per-concern-keys.md         # this plan
docs/decisions.md                                # D-134
docs/glossary.md                                 # per-concern gateway key
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `gateway.embed_provider` | `""` | Distinct embedding provider; empty → `gateway.provider`. |
| `gateway.embed_api_key` | `""` | `env.VAR` ref (secret); empty → `gateway.api_key`. |
| `gateway.embed_base_url` | `""` | Empty → driver default for the embed provider. |
| `gateway.rerank_provider` | `""` | Distinct rerank provider; empty → primary (auto-wired custom rerank, D-075). |
| `gateway.rerank_api_key` | `""` | `env.VAR` ref (secret); empty → `gateway.api_key`. |

## Acceptance criteria (binding)

1. All five keys surface in `config explain`; the two `*_api_key` are redacted; defaults empty.
2. `*_api_key` set to a non-`env.` literal fails `Validate` (same rule as `gateway.api_key`).
3. All-empty per-concern config produces the identical bifrost wiring as a1 (single provider/key)
   — proven by an `Account` test (`GetConfiguredProviders == [primary]`).
4. `embed_provider` set to a distinct provider routes Embed to that provider with
   `embed_api_key` (fallback primary key) and `embed_base_url`; Complete still uses the primary —
   proven against the bifrost test seam.
5. `rerank_api_key` set makes the rerank provider use that key (not the primary) — proven via the
   Account's `GetKeysForProvider(rerankProvider)`.
6. The a1 default (one OpenRouter key) still live-validates: embed+complete+rerank
   (`TestLiveBifrost_DefaultConfig` unchanged).

## Smoke script

`scripts/smoke/phase-a1b.sh` — keys surface (empty, secrets redacted); non-`env.` `*_api_key`
fails Validate; the Account fallback + distinct-provider unit tests pass.

## Test plan

Unit (bifrost `account_test.go`): all-empty fallback == a1 wiring; distinct `embed_provider`
routes embed + carries embed key/base; `rerank_api_key` carried on the rerank provider; primary
unaffected. Golden: regenerated explain. Live (key-gated, operator-run): the a1 default stack
still passes; optionally a split-provider path when a second key is supplied.

## Risks & mitigations

- **Destabilizing the D-075 rerank path** → the rerank change is additive (key override only);
  empty `rerank_*` keeps today's exact behavior, covered by the existing rerank tests + AC-3.
- **Knob count (+5)** vs D-034 → all default-empty (inherit); zero-config unchanged; justified by
  a real operator need (a credential/provider a profile can't absorb), the same standard as
  `gateway.api_key`/`rerank_base_url`.
- **Same-provider-different-key confusion** → explicitly out of scope + documented (D-134); the
  Validate/docs steer operators to distinct provider names.

## Glossary additions

- **Per-concern gateway key** — an optional provider/key/base_url for the embedding or rerank lane
  distinct from the primary completion provider, inheriting the primary when empty (D-134).

## Decisions filed

- D-134: Per-concern provider/key/base_url for embed + rerank (distinct provider names; inherit
  on empty; same-provider-different-key out of scope).
