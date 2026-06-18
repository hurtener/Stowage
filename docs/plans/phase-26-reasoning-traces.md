# Phase 26 — Reasoning traces + audit export (RFC §6c, OQ-10)

- **Status:** approved
- **Owning subsystem(s):** `internal/traces` (new: reconstruction + signing core),
  `internal/retrieval` (response-query capture), `internal/trust` (verdict capture),
  `internal/api` / `internal/mcpserver` / `sdk/stowage` (the `memory_trace` surface),
  `internal/config` (signing key), `test/integration`
- **RFC sections:** §6c (reasoning traces: "the full memory-into-conclusion chain per
  response — query, injections, drill-downs, verification verdicts — is reconstructable
  from the day-one tables (injections + events) and exported as a signed bundle per
  `response_id` … traces carry their own retention class"), §9.1 (`GET /v1/traces/{response_id}`),
  §8.1 (schema budget), §9.5/D-067, §7/D-030 (secret indirection)
- **Depends on phases:** 11 (injections + `response_id`), 25 (verify verdicts), 09/10
  (retrieve → `response_id`, support), 24 (links for the per-memory chain)
- **Informing briefs:** 04 (CL-Bench — auditability), 02 (ccmem — provenance discipline)

## Goal

When this phase is done, any caller can export the **reasoning trace** for a
`response_id` — the memory-into-conclusion chain: the query, the injected memories
(rank/score/lane/cited/feedback), each memory's drill-down provenance spans + typed
links + status, and the verification verdicts run against it — assembled
**read-only** from the day-one tables and returned as a **signed bundle**
(`GET /v1/traces/{response_id}` + `memory_trace` MCP + SDK `Trace`, single-user tier,
D-067). Two unbackfillable signals the §6c trace names — the **query text** and
**verify verdicts** — are now captured into the `events` table keyed by `response_id`
(D-024: the named later phase reads them), schema-neutrally. **OQ-10 is settled**
(D-086): traces are **reconstructed on demand, never stored**, so their retention
class is exactly the retention of their source rows (no separate trace store or
retention column); the bundle is signed with **ed25519** using an operator-provided,
env-indirected key (unsigned, `signed:false`, when no key is configured). No new
table or column.

## Brief findings incorporated

- **02 (ccmem):** provenance is the audit backbone — the trace chains injection →
  memory → provenance spans (the same drill-down machinery), never a summary.
- **04 (CL-Bench):** auditability of "why did the system answer this" is the trust
  capstone; the signed bundle is the regulator/third-party-tooling artifact.

## Findings I'm departing from / decided (D-086)

- **Reconstruct-on-demand, not stored.** The RFC says traces are *reconstructable*
  from day-one tables — so Phase 26 stores **no** trace rows. This makes OQ-10's
  "retention class" fall out for free: a trace lives exactly as long as its source
  injections/events/records, so the retention policy is the existing one — no new
  retention table, sweep, or column. (A future phase may cache/pre-sign bundles if
  audit volume demands; not now.)
- **Capture the two missing signals via `events`, keyed by `response_id`.** The §6c
  chain lists *query* and *verification verdicts*; neither is in the day-one tables
  today (injections have `response_id` but not the query; verify verdicts were
  ephemeral, D-084). Both are unbackfillable, so per D-024 we capture them now — as
  events with `SubjectID = response_id` and the payload in JSON (no schema change;
  events already carry a JSON payload). `retrieve.query` is emitted on the **async**
  injection-writer path (zero added retrieve latency, P2-respecting); `verify.verdict`
  is emitted by the verify core (verify already does a gateway round-trip).
- **Signing: ed25519 detached signature, operator key, env-indirected (D-030).** A
  config knob `trace.signing_key` (an `env.VAR` reference to a base64 ed25519 seed);
  when set, the bundle carries a detached signature over its canonical JSON + the
  public key; when unset, the bundle is returned `signed:false`. CGo-free (stdlib
  `crypto/ed25519`). No new key table — symmetric with the API-key secret indirection.

## Design

### Capture (day-one signals → events, schema-neutral)

- **`retrieve.query`** — the async `InjectionWriter` (already per-response, D-025)
  gains an `EventStore` + emits one event per response: `SubjectID = responseID`,
  `Payload = {"query": <text>, "support": <strength>, "degraded": <bool>, "limit": n}`.
  Off the request path (async), so retrieve latency is unchanged. The query text is
  the one privacy-sensitive field; it lives under the trace retention class (= events
  retention) and is redaction-eligible like any gateway-bound payload.
- **`verify.verdict`** — `trust.Verify`'s caller (the verify handlers/core) emits
  `SubjectID = responseID` (derived from the citations' injection rows; the citations
  are injection IDs that carry `response_id`), `Payload = {"claim", "verdict",
  "confidence", "degraded"}`.

### Reconstruction core — `internal/traces/reconstruct.go` (deterministic, gateway-free)

```go
type Trace struct {
    ResponseID string
    Query      string        // from the retrieve.query event ("" if not captured)
    Support    string
    Degraded   bool
    Items      []TraceItem   // one per injected memory, rank-ordered
    Verdicts   []TraceVerdict
    GeneratedAt int64
}
type TraceItem struct {
    MemoryID string; Kind string; Content string; Status string
    Rank int; Score float64; Lane string; WasCited bool; Feedback string
    Provenance []TraceSpan   // drill-down: record_id + span + excerpt
    Links      []TraceLink   // typed edges from this memory
}

func Reconstruct(ctx, st, scope, responseID string) (Trace, error)
```

Assembly: `Injections().ListByResponse(scope, responseID)` → per memory:
`Memories().Get` (kind/content/status), `GetJunctions` (provenance spans → excerpts
via `retrieval.ClampExcerpt`), `ListLinks(from=memID)` (typed edges). The query +
verdicts come from `Events().ListBySubject(scope, responseID, …)` filtered to
`retrieve.query` / `verify.verdict`. Scope-enforced throughout (P3). Empty/unknown
`response_id` ⇒ an empty trace (no error), parity with the other reads. Gateway-free.

### Signing — `internal/traces/sign.go`

```go
// SignedBundle wraps a Trace with an optional detached ed25519 signature over the
// canonical JSON of the trace. signer is nil when no key is configured.
type Bundle struct { Trace Trace `json:"trace"`; Signed bool `json:"signed"`;
    Algorithm string `json:"algorithm,omitempty"`; PublicKey string `json:"public_key,omitempty"`; Signature string `json:"signature,omitempty"` }
func Sign(t Trace, seed ed25519.PrivateKey) Bundle  // canonical-JSON → ed25519 detached sig
```

Canonical JSON = `json.Marshal` of the Trace with map-free, stable-ordered fields
(structs marshal deterministically). The signature covers exactly the `trace` object
bytes so a verifier recomputes it independently.

### Surfaces — `memory_trace` (single-user tier, D-067)

`GET /v1/traces/{response_id}` (HTTP), `memory_trace` (MCP, `{response_id}`), SDK
`Trace(TraceRequest{ResponseID})`. All call `traces.Reconstruct` then `traces.Sign`
(with the configured key, held by the surface like the gateway/retriever). Scope owner
exports their own response's trace; a `response_id` outside scope ⇒ empty trace.
Byte-identical parity across the three (deterministic — no gateway; signature is
deterministic for a fixed key, or absent when unkeyed → set the same test key on all
three).

### Config (D-034)

| Key | Default | Notes |
|-----|---------|-------|
| `trace.signing_key` | `""` (unsigned) | `env.VAR` reference (D-030) to a base64-encoded 32-byte ed25519 seed. Empty ⇒ bundles returned unsigned (`signed:false`). Validated at boot (decodes to 32 bytes) — fail-loud. Documented in the example config + every profile (empty). |

### Lifecycle / retention (OQ-10 → D-086)

Traces are not stored, so they have no independent lifecycle: a trace is exactly as
durable as its source injections/events/records and is removed by the same
retention/DSAR cascade (the day-one signals' retention IS the trace retention class).
The `retrieve.query` event is the one new always-on write — bounded (one per
retrieve, async) and subject to the same event retention.

## Files added or changed

```text
internal/traces/{reconstruct,sign}.go (+ tests)
internal/retrieval/injections.go        # InjectionWriter emits retrieve.query (async)
internal/retrieval/retrieval.go          # pass query/support to the writer
internal/trust/verify.go OR the verify surfaces  # emit verify.verdict (response-keyed)
internal/config/*                        # trace.signing_key knob + validation + profiles + explain
internal/api/traces_handler.go ; server.go route ; SetTraceSigner
internal/mcpserver/{contracts,handlers,server}.go  # memory_trace tool + golden ; Services signer ; count 17→18
sdk/stowage/{client,types,http,embedded}.go  # Trace method
adapters/harbor/harbor_test.go           # fakeClient.Trace
test/integration/traces_parity_test.go
scripts/smoke/phase-26.sh
docs/plans/phase-26-reasoning-traces.md ; docs/decisions.md (D-086) ; docs/glossary.md
```

## Acceptance criteria (binding)

1. **Capture (D-024):** a retrieve emits one async `retrieve.query` event keyed by
   `response_id` (no added retrieve latency); `trust.Verify` emits a `verify.verdict`
   event keyed by `response_id`. No new table/column (events JSON payload).
2. **Reconstruction (deterministic, gateway-free, P3):** `traces.Reconstruct` assembles
   the full chain (query, injected memories with rank/score/lane/cited/feedback, per-
   memory provenance spans + links + status, verdicts) from day-one tables, scope-
   enforced; unknown `response_id` ⇒ empty trace, no error; `internal/traces` imports
   no gateway.
3. **Signing (D-030):** with `trace.signing_key` set, the bundle carries a verifiable
   ed25519 detached signature over the canonical trace JSON + the public key; unset ⇒
   `signed:false`, bundle still returned. A unit test verifies a signed bundle with the
   public key and rejects a tampered one.
4. **Tiered parity (D-067):** `memory_trace` byte-identical across {SDK, HTTP, MCP}
   for a seeded response (same signing key on all three).
5. **Schema/knob discipline:** no new table/column; one config knob with default +
   profiles + `config explain` + boot validation; new MCP tool ⇒ golden + tool-count
   update + same-PR smoke.
6. **OQ-10 settled** in D-086 (reconstruct-on-demand → retention = source rows;
   ed25519 operator key).
7. **Gates:** build, `go test -race ./...`, golangci-lint, gofmt, coverage, preflight,
   drift-audit, mirror green.

## Smoke script

`scripts/smoke/phase-26.sh`: capture wired (retrieve.query + verify.verdict greps);
`internal/traces` gateway-free; reconstruction + signing unit tests; `memory_trace` on
HTTP route + MCP tool (+ golden, count 18) + SDK; signing-key knob in profiles +
explain + boot validation; parity passes; goldens stable; `make eval-ci` green.

## Test plan

- **Unit — reconstruct:** seeded injections + events + provenance + links → assert the
  full chain (query, items ordered by rank, spans, links, verdicts); unknown response;
  scope isolation; non-active memory still shown (it was injected) but flagged by status.
- **Unit — sign:** sign → verify with public key passes; tamper a byte → verify fails;
  unkeyed ⇒ `signed:false`.
- **Capture:** retrieve emits one retrieve.query event (async, drained); verify emits
  verify.verdict keyed by the citations' response_id.
- **Parity (§17):** `memory_trace` byte-identical across surfaces (fixed test key).
- **Fuzz:** the signature-verify decode path (`FuzzTraceVerify`) — arbitrary bundle
  bytes never panic; verify is always bool/err.

## Risks & mitigations

- **Query text is PII in events.** → it is the one new always-on capture, under the
  trace retention class (= event retention), redaction-eligible; documented in D-086.
  (A future opt-out knob is possible but not shipped — the §6c trace requires the query.)
- **Retrieve write amplification.** → the query event rides the existing async
  injection-writer batch (one event per response, off the request path).
- **Signing key management.** → operator-provided, env-indirected (D-030), validated
  fail-loud at boot; unsigned is a valid mode (dev/zero-config).

## Glossary additions

- **Reasoning trace** — the read-only, per-`response_id` memory-into-conclusion chain
  (query, injected memories, drill-down spans, typed links, verification verdicts)
  reconstructed from the day-one tables and exported as a signed bundle (Phase 26, §6c).
- **Trace bundle** — the `memory_trace` export: a `Trace` plus an optional ed25519
  detached signature + public key for third-party audit verification (Phase 26, D-086).

## Decisions filed

- **D-086** — Reasoning traces are **reconstructed on demand** (never stored) from the
  day-one tables, so their retention class is exactly that of their source
  injections/events/records (settles OQ-10 — no separate trace store/retention). The
  two §6c signals not yet captured — the retrieve **query** and **verify verdicts** —
  are now written to `events` keyed by `response_id` (schema-neutral, D-024). Export is
  a signed bundle (`memory_trace`, single-user tier {SDK,HTTP,MCP}): **ed25519** detached
  signature over the canonical trace JSON with an operator-provided, env-indirected key
  (`trace.signing_key`, D-030); unsigned when unkeyed. No new schema.
