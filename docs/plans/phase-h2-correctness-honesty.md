# Phase h2 — Wave A correctness + honesty bundle

- **Status:** draft | approved | in-progress | shipped
- **Owning subsystem(s):** `sdk/stowage`, `internal/config`, `internal/store` (FTS query build), `internal/retrieval` (drill-down shaping), `internal/mcpserver`, plus doc surfaces
- **RFC sections:** §9.4 (config: zero-config, fail-loud, knob guardrail), §9.1 (D-030 keys never in config files), §4.2 (lexical lane / FTS), P1 (provenance drill-down fidelity)
- **Depends on phases:** 02 (config/auth), 09 (retrieval lanes), 17 (MCP), 18 (SDKs) — all shipped
- **Informing briefs:** 06 (mempalace: config & adoption, gateway-free retrieval, verbatim drill-down), 02 (CC-memory: knob-paralysis cautionary tale), 01 (Python predecessor: gateway pain points)
- **Program:** D-067 Wave A (correctness + honesty punch-list). Pre-reserved decision: **D-069**.

## Goal

When this phase is done, the embedded path enforces the same fail-loud config
guarantees and produces the same lane-complete, rune-safe results as the server;
the sqlite FTS lane no longer hard-errors on operator/special-char queries; and no
surface silently accepts a field it ignores. These are the Wave A "fix now, no
design" items from `docs/notes/parity-lens-findings.md` — the outright bugs
(BUG-2..BUG-5) and the doc-honesty corrections — delivered as one bundle (the
flagship structural fix is the separate phase-h1).

## Brief findings incorporated

- **Brief 06 (mempalace):** zero-config + gateway-free retrieval are load-bearing
  for embedded/desktop (D-022, D-036); the embedded path must apply the same
  defaults and degrade the same way as the server, not silently lose a lane.
- **Brief 02 (CC-memory):** knob paralysis — this bundle adds **no** knobs; it
  makes existing config validation fire on the embedded path.
- **Brief 01 (Python predecessor):** gateway/config divergence between deployment
  modes was a recurring defect class; align the modes.

## Findings I'm departing from

- None. (The FTS result-set *semantics* across drivers are aligned only as far as
  the two engines allow; exact equivalence of FTS5 `MATCH` and Postgres
  `plainto_tsquery` is not achievable and not claimed — see AC-3 / Risks.)

## Design

Six independent fixes; each lands with its own check. They share no files except
the smoke script, so they can be implemented together in one PR safely.

1. **Embedded config validation + D-030 guard (BUG-3).** `NewEmbedded` runs the
   same `cfg.Validate()` the server runs before `boot.Open`, including the D-030
   secret-indirection guard (a literal `api_key` in config — not `env.VAR` — fails
   closed). An invalid embedded config returns an error from `NewEmbedded`, never a
   half-built stack. *Fail-loud, security-adjacent.*
2. **Embedded gateway defaults (Pattern P3).** The embedded path applies
   `config.Defaults` for gateway model / embedding dims / rerank model so the
   vector and rerank lanes are populated identically to the server under the
   documented-minimal config. Today embedded leaves dims unset → the vector lane is
   a silent no-op while the server populates it (e.g. 512 dims). Route embedded
   construction through the same defaults+profile-merge layer `config.Load` applies,
   or apply `config.Defaults` explicitly in `NewEmbedded` before `boot.Open`.
3. **sqlite FTS query sanitization (BUG-4).** The lexical/queries lane builds the
   FTS5 `MATCH` argument from user text. Sanitize/quote it so operator and
   special-character input yields results (or a clean empty), never a hard error
   that silently drops both lexical lanes. Behavior is documented and brought as
   close to the Postgres `plainto_tsquery` robustness profile as the engine allows.
4. **Embedded rune-safe drill-down (BUG-5).** The embedded drill-down excerpt
   shaper byte-slices the provenance span and can split a multi-byte rune. Replace
   with the same UTF-8-boundary-safe shaping the server uses (ideally by sharing
   the server's shaping function so the two cannot diverge again).
5. **Contribute-mode fail-loud (BUG-2 / honesty).** MCP `memory_ingest` declares
   `target_scope` / `contributor_user_id` but ignores them — a silent mis-scope. In
   Wave A, **reject** these fields with a clear error on MCP (and the SDK omits
   them) so no caller believes they contributed to a pool when they did not. *Full
   cross-server-surface honoring (HTTP↔MCP) is Wave B, per the tiered model — these
   are multi-user verbs, never on the single-user embedded SDK.*
6. **Doc honesty (A7).** Correct: `internal/boot/boot.go` godoc framing (now points
   at `StartPipeline`, h1), the MCP `memory_ingest` tool description (no longer
   claims it "mirrors POST /v1/records" if any divergence remains after h1), and
   any README/godoc that claims a capability is reachable on a surface where it is
   not.

## Files added or changed

```text
sdk/stowage/embedded.go          # cfg.Validate(); apply config.Defaults; rune-safe drilldown
internal/config/*.go             # (if needed) export the validate/defaults path NewEmbedded reuses
internal/store/.../*.go          # FTS5 MATCH argument sanitization (sqlite driver)
internal/retrieval/*.go          # share/align drill-down excerpt shaping (rune-safe)
internal/mcpserver/handlers.go   # contribute-mode fields -> fail-loud
docs/, godoc                     # honesty corrections
scripts/smoke/phase-h2.sh        # NEW
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| (none) | — | No new knobs. This bundle makes **existing** validation/defaults fire on the embedded path; D-034 guardrail not engaged. |

## Acceptance criteria (binding)

1. `NewEmbedded` rejects an invalid config (including a literal `api_key` per
   D-030) with an error — identical fail-loud behavior to the server boot; no
   half-built embedded stack.
2. The embedded path applies gateway defaults (model/dims/rerank) so the vector
   lane is non-empty for the documented-minimal config, matching the server.
3. A sqlite lexical query containing FTS5 operators/special characters returns a
   result set (or clean empty) and **never** hard-errors or silently drops the
   lexical lanes; covered by a fuzz/seed test on the FTS argument builder.
4. Embedded drill-down returns valid UTF-8 for a provenance span whose byte offset
   lands mid-rune; identical output to the server path (golden).
5. MCP `memory_ingest` with `target_scope`/`contributor_user_id` set returns a
   clear error rather than silently ingesting into the caller scope; the SDK does
   not expose the fields.
6. No godoc/README/tool-description claims a capability reachable where it is not
   (boot.go, MCP ingest description, any audited doc); `make drift-audit` green.

## Smoke script

`scripts/smoke/phase-h2.sh` — build; assert `NewEmbedded` (via a tiny harness or
`stowage` subcommand) rejects an invalid/literal-key config; issue a special-char
lexical query and assert no crash; (rune-safety + contribute fail-loud asserted in
unit/integration tests, referenced here). SKIP gracefully where a surface isn't
built.

## Test plan

- **Fuzz (§11):** `FuzzFTSQueryArg` on the sqlite FTS argument builder — invariant:
  never returns an error that aborts the lane; corpus seeds the operator/special
  cases. Runs as an ordinary CI test.
- **Golden:** embedded vs server drill-down excerpt for a mid-rune span → identical.
- **Unit:** `NewEmbedded` validation table (valid / invalid / literal-key);
  embedded defaults populate dims; MCP contribute-field rejection.
- **Integration:** embedded retrieve over a degraded gateway still serves the
  lexical lane (D-036) without the FTS crash.

## Risks & mitigations

- *Risk:* FTS sanitization changes hit-sets for legitimate queries. *Mitigation:*
  sanitize only operator/special tokens that today crash; cover with the fuzz
  corpus + existing lexical-lane tests; document the chosen escaping.
- *Risk:* applying `config.Defaults` on embedded changes existing embedded
  consumers' effective config. *Mitigation:* defaults match the server's; document
  in the PR; existing embedded tests must pass (behavior-preservation for valid
  configs).
- *Risk:* contribute fail-loud breaks a caller relying on the silent-ignore.
  *Mitigation:* that path was already broken (silent mis-scope); a clear error is
  strictly safer; named in the PR body.

## Glossary additions

- (none new; reuses existing config/FTS/drill-down vocabulary)

## Decisions filed

- **D-069** — Wave A embedded correctness + honesty: `NewEmbedded` enforces the
  server's fail-loud config validation (incl. D-030) and gateway defaults; sqlite
  FTS arguments are sanitized; embedded drill-down is rune-safe; MCP contribute-mode
  fields fail loud rather than silently mis-scope. No new knobs.
