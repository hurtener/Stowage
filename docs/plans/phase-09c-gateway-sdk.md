# Phase 09c — Gateway remediation: real Bifrost SDK driver (owner directive)

- **Status:** implemented
- **Owning subsystem(s):** `internal/gateway` (driver rename + new SDK driver),
  config
- **RFC sections:** §7 (gateway seam, P5), D-005, D-040
- **Depends on phases:** 04 (09b ships first; no functional dependency)
- **Informing briefs:** Harbor's `internal/llm/drivers/bifrost` (the proven
  integration pattern: Account interface, client seam for tests, fail-closed
  keys)

## Goal

Owner directive (2026-06-11): the Phase 04 "bifrost" driver was a
misunderstanding — it's a from-scratch OpenAI-compatible HTTP client, not the
intended **direct import of the Bifrost Go SDK** (`maximhq/bifrost/core`,
Harbor's choice), which provides every provider (OpenAI, Anthropic, Gemini,
Mistral, …) natively in-process. Remediation, least-impactful shape:

1. **Rename** the existing HTTP driver `bifrost` → `openaicompat` (it stays —
   it is live-validated and remains the right tool for OpenRouter and any
   OpenAI-compatible endpoint; D-040 amended, not revoked).
2. **Add** a real `bifrost` driver backed by the SDK, implementing the same
   `Gateway` interface (Embed + Complete + Probe + Close). Verified before
   planning: the SDK ships `BifrostEmbeddingRequest/Response` (embeddings are
   first-class) and the `bf.Init` + `ChatCompletionRequest` pattern Harbor
   uses in production.

Because of P5, **zero call sites change** — reconcile, extraction, retrieval,
and the embedder are driver-oblivious. This is the cheapest possible point in
the project's life to make the swap.

## Design

### Rename (mechanical)

`internal/gateway/bifrost` → `internal/gateway/openaicompat`; registry name
`"openaicompat"`; config validation message for the old intent; live tests
(OpenRouter) move with it and keep working unchanged. Goldens unchanged
(same wire). Smokes/config examples updated.

### The SDK driver (`internal/gateway/bifrost`, the name now meaning what it says)

- Dependency: `github.com/maximhq/bifrost/core` pinned to the latest v1.5.x
  (Harbor uses v1.5.15; CGo-free — Harbor ships a static binary with it).
- **Account** implementation (Harbor's pattern): provider from new config key
  `gateway.provider` (`openai` | `anthropic` | `gemini` | …), API key via the
  existing `env.` indirection, optional `base_url` passthrough. Fail-closed at
  Open.
- **Complete**: `ChatCompletionRequest` with structured-output params where
  the provider supports them; the seam's schema-validate-and-retry-once
  (Phase 04) remains the safety net for providers that don't.
- **Embed**: `EmbeddingRequest` path; batching/cache/breaker/metering are
  seam-level (Phase 04) and apply unchanged.
- **Test seam**: wrap the SDK behind a tiny `bifrostClient` interface (exactly
  Harbor's move) so unit tests inject fakes; table tests for translate
  functions; error classification mirrors the SDK's error type.
- **Probe**: one canary embedding via the SDK; dims pinning unchanged.

### Config

`gateway.driver`: `mock` (default, dev) | `bifrost` (SDK — recommended
production; all providers) | `openaicompat` (any OpenAI-compatible HTTP
endpoint; used for OpenRouter live validation). New key `gateway.provider`
(required when driver=bifrost; validated against the SDK's provider set).
`config explain` + profiles + example updated.

## Files added or changed

```text
internal/gateway/openaicompat/   (moved from internal/gateway/bifrost)
internal/gateway/bifrost/        (new: sdk driver — account.go, driver.go, translate.go, *_test.go)
internal/config/                 (provider key + driver enum + validation + explain golden)
cmd/stowage/main.go              (registry imports)
scripts/smoke/phase-09c.sh       (boot matrix: mock default OK; bifrost without key fails closed with key-path error; openaicompat unchanged)
go.mod                           (+ github.com/maximhq/bifrost/core)
docs/decisions.md                (D-049), docs/plans update; D-040 amendment note
```

## Acceptance criteria (binding)

1. Rename is behavior-preserving: all Phase 04 tests (goldens, retry, breaker,
   envelope) green under `openaicompat`; OpenRouter live tests pass unchanged
   (run them).
2. SDK driver implements the full Gateway contract behind the client seam;
   unit tests cover translate (chat + embed), error classification, and
   fail-closed key/provider validation.
3. `gateway.provider` validated (unknown provider → boot error with key path);
   explain shows it; required iff driver=bifrost.
4. Seam invariants hold driver-agnostically: schema validation + single
   retry, batching, cache, breaker, metering — proven by running the seam
   test suite against the SDK driver with a fake client.
5. CGo-free build (`make build`) still green with the SDK dependency.
6. No call-site changes outside internal/gateway + config + cmd wiring (diff
   proves it).
7. Coverage ≥ 80 both driver packages; smokes 01–09c green.
8. Optional (not gating): live SDK round-trip when STOWAGE_TEST_OPENAI_KEY or
   STOWAGE_TEST_ANTHROPIC_KEY is present (tags=live).

## Risks & mitigations

- SDK transitive deps weight → review `go mod graph` delta in the PR body;
  Harbor already carries it CGo-free.
- SDK API drift vs Harbor's version → pin exact version; client seam isolates.
- Name confusion in history → D-049 records the rename rationale; grep-clean.

## Decisions filed

- D-049: the `bifrost` driver name now denotes the real SDK integration
  (maximhq/bifrost/core — every provider in-process, Harbor parity); the
  Phase 04 HTTP client lives on as `openaicompat` (D-040 amended: its
  base_url-agnostic wire client remains the OpenRouter/live-validation path).
