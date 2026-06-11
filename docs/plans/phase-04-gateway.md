# Phase 04 — Gateway seam + bifrost driver

- **Status:** draft
- **Owning subsystem(s):** `internal/gateway` (+ drivers)
- **RFC sections:** §7 (the gateway seam), §2.1 P5
- **Depends on phases:** 02
- **Informing briefs:** 01 (local-model liability → API-only; DSPy JSON
  re-parsing failures → schema-constrained calls), 06 (gateway-free
  degradation context — consumers must tolerate gateway absence)

## Goal

The single intelligence seam every later phase calls: batched embeddings and
JSON-schema-constrained completions behind one interface, with a deterministic
`mock` driver for all hermetic tests and a `bifrost` driver speaking the
OpenAI-compatible wire format (works against Bifrost, OpenRouter, or any
compatible endpoint). Retries, circuit breaking, cost metering, and
model+dims pinning live here — callers never see provider concerns (P5).

## Brief findings incorporated

- Brief 01: free-text JSON parsing of model output was a recurring failure →
  `Complete` takes a JSON schema; the driver uses constrained decoding and the
  seam validates the returned JSON against the schema (retry once on invalid).
- Brief 01: embedding hosts were the deployment liability → batch + cache +
  breaker here, no local fallback.

## Findings I'm departing from

- None.

## Design

### The seam

```go
type Gateway interface {
    Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
    Complete(ctx context.Context, req CompleteRequest) (CompleteResponse, error)
    Probe(ctx context.Context) error   // boot validation: model reachable, dims match
    Close(ctx context.Context) error
}

type EmbedRequest struct{ Inputs []string }            // model/dims come from config (pinned)
type EmbedResponse struct{ Vectors [][]float32; Usage Usage }

type CompleteRequest struct {
    System   string
    Messages []Message            // role + content
    Schema   json.RawMessage      // REQUIRED: JSON schema for the output (P5/§10 rule)
    MaxTokens int
    Temperature float32
}
type CompleteResponse struct{ JSON json.RawMessage; Usage Usage }

type Usage struct{ InputTokens, OutputTokens int; CostUSD float64 }
```

- `Schema` is mandatory — there is no free-text completion in Stowage
  (CLAUDE.md §10). The seam validates `JSON` against `Schema`
  (santhosh-tekuri/jsonschema/v6, Harbor's choice) and retries once with the
  validation error appended before failing.
- Metering: the gateway takes a `Meter` interface
  (`Record(ctx, op, model, usage)`) at construction; Phase 05 wires it to the
  event store; this phase ships a Prometheus + slog implementation.
- Registry + blank-import drivers, mirroring the store seam.

### Batching (Embed)

A coalescing batcher: concurrent `Embed` calls are merged into provider
batches (max size 64, flush interval 25 ms, both configurable via profile —
not new top-level knobs); callers get their slice back transparently. Errors
propagate per-batch. Cache lookup happens **before** batching: an in-memory
LRU keyed `(model, sha256(input))`, default 50k entries, hit returns without
provider traffic.

### Resilience

Per-request retry: jittered exponential backoff, max 3 attempts, retry on
429/5xx/timeouts, never on 4xx. Circuit breaker around the provider: 5
consecutive failures → open 30 s → half-open single probe. Breaker-open
errors are typed (`ErrGatewayUnavailable`) so Phase 09 can flag degraded
retrieval (D-036).

### The `bifrost` driver

OpenAI-compatible wire format: `POST {base_url}/chat/completions` with
`response_format: {type: "json_schema", json_schema: {strict: true, ...}}`,
and `POST {base_url}/embeddings`. API key via `config.ResolveEnvRef`
(fail-closed at Open). `Probe()` embeds one canary string and checks
`len(vec) == cfg.EmbedDims` — mismatch fails boot (an index is bound to
model+dims; RFC §7). Wire structs live only in this package (P5).

### The `mock` driver

Deterministic: embeddings are unit vectors derived from sha256(input) at the
configured dims; `Complete` returns the schema's instance skeleton filled
from a per-test script table (tests register expectations). Hermetic tests
for every later phase build on this.

### Validation (manual, not CI)

A `-tags=live` test gated on `STOWAGE_TEST_OPENROUTER_KEY` +
`STOWAGE_TEST_OPENROUTER_MODEL` exercises `Complete` against OpenRouter
(schema-constrained round-trip). **Update 2026-06-11 (post-merge):** OpenRouter
now serves embeddings — `google/gemini-embedding-2` verified live (3072 dims
default); use it for `Embed`-path live validation going forward. For the
optional rerank lane (Phase 12), `cohere/rerank-4-fast` is the designated
validation model.

## Files added or changed

```text
internal/gateway/{gateway.go, types.go, errors.go, registry.go, meter.go,
                  batcher.go, cache.go, breaker.go, validate.go}
internal/gateway/bifrost/{driver.go, wire.go, driver_test.go (httptest goldens), live_test.go}
internal/gateway/mock/{driver.go, driver_test.go}
scripts/coverage.json       (gateway packages at 80)
scripts/smoke/phase-04.sh
go.mod                      (+ santhosh-tekuri/jsonschema/v6)
```

## Config keys added

None new (Phase 02 already declared gateway.driver/base_url/api_key/model/
embed_model/embed_dims). Batcher size/interval and cache size are
profile-internal values, not top-level keys (knob guardrail).

## Acceptance criteria (binding)

1. Golden wire tests: bifrost driver requests match goldens byte-for-byte
   (chat + embeddings); canned responses decode correctly.
2. Schema validation: an invalid model response triggers exactly one retry
   with the validation error appended; second failure returns a typed error.
3. Batcher: N concurrent Embed calls produce ≤ ceil(N/64) provider requests
   (test with a counting fake); per-caller results correct under `-race`.
4. Cache: repeat input produces zero provider traffic (counting fake).
5. Breaker: 5 consecutive 500s open the circuit; calls during open fail fast
   with `ErrGatewayUnavailable`; half-open probe recovers.
6. Retry policy: 429/503 retried with backoff; 400 not retried (test).
7. `Probe` fails when response dims ≠ configured dims.
8. Meter records op/model/usage for every provider call (Prometheus counters
   asserted).
9. Coverage ≥ 80 on gateway packages; everything `-race` clean.

## Smoke script

phase-04.sh: hermetic — runs the mock-driver path via a tiny `go test -run`
hook; asserts `stowage version` still fine; live checks SKIP without the env
key.

## Test plan

Golden wire tests (httptest), table-driven retry/breaker matrices, `-race`
batcher tests, fuzz target on the schema-validation path input (model JSON),
benchmark `BenchmarkEmbedCacheHit`.

## Risks & mitigations

- Provider `json_schema` support varies by model → seam-level validation +
  retry is the safety net regardless; live test proves one real model.
- Batcher deadlocks under cancellation → ctx propagation tests; flush ticker
  always drains.

## Glossary additions

None.

## Decisions filed

- D-040: the bifrost driver speaks the OpenAI-compatible wire format
  generically (base_url-agnostic: Bifrost, OpenRouter, any compatible
  endpoint); provider-specific drivers only when a wire format actually
  diverges. (Note: D-039 was taken by a coverage override; this decision
  is D-040.)
