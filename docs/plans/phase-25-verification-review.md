# Phase 25 — Verification & review queue (RFC §6c)

- **Status:** approved
- **Owning subsystem(s):** `internal/trust` (new: `Verify` entailment + review-queue
  core), `internal/reconcile` (assert can park to `pending_review`), `internal/api` /
  `internal/mcpserver` / `sdk/stowage` (the `memory_verify` + `memory_review`
  surfaces), `test/integration`
- **RFC sections:** §6c (claim verification — "a safeguard pass (gateway,
  schema-constrained) checks that a drafted claim is actually entailed by its cited
  memories … exposed as `POST /v1/verify`"; uncited-claim review — "parks as
  `pending_review` in an admin queue"), §5.7 (citations/injections), §9.5/D-067
  (one core, thin tiered surfaces), §7/§10/P5 (gateway seam), §8.1 (schema budget)
- **Depends on phases:** 11 (citations/injections + `/v1/citations/resolve`), 04
  (gateway `Complete`, schema-constrained), 08/18 (reconcile assert + the
  pending/Resolve reversibility pattern, D-065/D-017)
- **Informing briefs:** 04 (CL-Bench — hallucination/entailment failure modes), 02
  (ccmem — trust/lifecycle discipline)

## Goal

When this phase is done, Stowage can **check that a claim is entailed by its cited
memories** and **hold uncited agent assertions for review** instead of silently
trusting them. (1) `POST /v1/verify` (+ `memory_verify` MCP + SDK `Verify`) takes a
claim + citation handles, resolves the cited memories (reusing the Phase-11 injection
resolution), and runs a **schema-constrained gateway entailment check** (P5/D-040),
returning a verdict (`entailed`/`not_entailed`/`unclear`) + confidence + explanation;
gateway-unreachable degrades to `unclear`+`degraded` (never blocks, D-036). (2) A
**review queue**: `memory_assert` gains a `review` flag that parks the asserted memory
as `pending_review` (instead of `active`) with a `memory.pending_review` event, and a
new `memory_review` capability lists a scope's `pending_review` memories and
**approves** (→ active) or **rejects** (→ quarantined) them — reversibly, via events
(D-017). No new table/column: `pending_review` + `quarantined` are day-one statuses
(§8.1) — **no RFC amendment**. Reasoning-trace export is **deferred to Phase 26**
(D-027/D-076).

## Brief findings incorporated

- **04 (CL-Bench):** "looks-good-but-isn't-real" claims are a top failure mode; the
  verify safeguard + the review queue are the two §6c defenses against reinforcing
  hallucinations into the substrate.
- **02 (ccmem):** the review-resolve reuses the Phase-18 confirm/reject reversibility
  discipline (prior-state events, D-065/D-017) — destructive ops stay invertible.

## Findings I'm departing from

Settled as **D-084**:
- **Review queue is a scope-level, single-user-tier capability**, not a
  credential-admin function. The pending_review memories are scope-owned (P3); the
  scope owner reviews them via `memory_review` on {SDK, HTTP, MCP} (D-067 single-user
  tier) at `/v1/review` — **not** `/v1/admin/*` (which is operator/credential tier).
  RFC's "admin queue" = a review/moderation surface, satisfied by a scope-level queue.
- **Reject → `quarantined`, not deleted** (P4 reversible; a rejected claim is held,
  not destroyed; re-approvable). Approve → `active`.
- **The producer is the explicit `review` flag on `memory_assert`**, not automatic
  uncited-detection. Automatically routing "agent-generated extraction without
  citations" to review needs a citation-on-ingest signal we don't capture yet and an
  eval to tune false positives; Phase 25 ships the **mechanism** (park + queue +
  resolve + verify) and the explicit producer. Automatic detection is a future
  eval-gated enhancement (noted in D-084).

## Design

### Verify core — `internal/trust/verify.go` (gateway, schema-constrained)

```go
type Verdict struct {
    Verdict     string  // "entailed" | "not_entailed" | "unclear"
    Confidence  float64 // 0–1
    Explanation string
    Degraded    bool    // gateway unreachable ⇒ unclear+degraded, no error (D-036)
}
type CitedMemory struct { ID, Content string }

// Verify runs a schema-constrained entailment check of claim against the cited
// memories (P5/D-040). Gateway error ⇒ {Verdict:"unclear", Degraded:true}, NO error.
func Verify(ctx context.Context, gw gateway.Gateway, claim string, cited []CitedMemory) (Verdict, error)
```

Schema `{verdict: enum, confidence: number, explanation: string}` (`additionalProperties:false`).
Empty `cited` ⇒ `unclear` (nothing to entail against), no gateway call. The surfaces
resolve citation handles → `CitedMemory` (the Phase-11 `Injections().Get` +
`Memories().GetMany` path, factored into a shared `trust.ResolveCited`).

### Review core — `internal/trust/review.go` (deterministic, gateway-free)

```go
// ListPending returns the scope's pending_review memories (most-recent-first),
// wrapping store.ListByStatus("pending_review"). Scope-enforced (P3).
func ListPending(ctx, st, scope, limit int, cursor string) (items []store.Memory, next string, err error)

// Resolve approves (→active) or rejects (→quarantined) a pending_review memory,
// reversibly (a memory.review_approved / memory.review_rejected event carries the
// prior state; mirrors reconcile.Resolve for pending_confirmation). ErrNotPending when
// the memory is not pending_review.
func Resolve(ctx, st, scope, id string, action ReviewAction, inv ...reconcile.ScopeInvalidator) (*ReviewResult, error)
```

Gateway-free. Approve/reject go through a `store.CommitSet` (status change + event in
one tx) so the resolution is atomic + auditable; cache-invalidated on approve (content
becomes retrievable).

### Producer — `reconcile.Assert` parks to pending_review

`AssertParams` gains `Review bool`. When set, the asserted memory commits with status
`pending_review` (instead of `active`) + a `memory.pending_review` event. The
`memory_assert` input (SDK + MCP — assert is Tier-A {SDK, MCP}, D-071) gains `review`.
A pending_review memory is **not retrievable** (retrieval is active-only) until approved.

### Surfaces (D-067)

- **`memory_verify`** (single-user tier {SDK, HTTP, MCP}): input
  `{claim, citations:[handle]}` → `{verdict, confidence, explanation, degraded}`.
  HTTP `POST /v1/verify`; MCP `memory_verify`; SDK `Verify`. The api.Server +
  mcpserver.Services gain a `Gateway` handle (set at boot, like the retriever);
  embedded uses `c.stack.Gateway`. Parity is testable with the **mock gateway**
  (scripted deterministic verdict ⇒ byte-identical across surfaces).
- **`memory_review`** (single-user tier {SDK, HTTP, MCP}): list + resolve. HTTP
  `GET /v1/review?limit=&cursor=` (list) + `POST /v1/review/{id}` `{action:approve|reject}`;
  MCP `memory_review` `{action:list|approve|reject, id?, limit?, cursor?}`; SDK `Review`.
  Deterministic ⇒ byte-identical parity.
- New MCP tools: count 15 → 17 (`memory_verify`, `memory_review`); schema goldens added.

### Lifecycle (P4)

`pending_review` memories are inert (not retrieved, not decayed as active). Approve →
active (enters normal lifecycle). Reject → quarantined (reversible; a future sweep MAY
expire long-quarantined review rejects, out of scope here). Verify is read-only (no
state change). All transitions emit events (audit trail, §8).

## Files added or changed

```text
internal/trust/verify.go ; internal/trust/review.go ; internal/trust/resolve_cited.go (+ tests)
internal/reconcile/assert.go            # AssertParams.Review → pending_review + event
internal/api/{verify_handler,review_handler}.go ; server.go routes ; SetGateway
internal/mcpserver/{contracts,handlers,server}.go  # memory_verify + memory_review ; Services.Gateway ; goldens ; count 15→17
sdk/stowage/{client,types,http,embedded}.go  # Verify + Review (+ assert review flag)
adapters/harbor/harbor_test.go          # fakeClient gains Verify + Review
test/integration/{verify_parity,review_parity}_test.go
scripts/smoke/phase-25.sh
docs/plans/phase-25-verification-review.md ; docs/decisions.md (D-084) ; docs/glossary.md
```

## Config keys added

**None.** Verify reuses the configured gateway; the review queue is read/resolve over
existing status. No new knob (D-034 N/A).

## Acceptance criteria (binding)

1. **Verify (P5/D-040, D-036):** `POST /v1/verify` resolves citation handles to their
   memories and runs a schema-constrained gateway entailment check returning
   `{verdict∈{entailed,not_entailed,unclear}, confidence, explanation}`; a gateway
   failure ⇒ `unclear`+`degraded`, HTTP 200, **no error**. Empty citations ⇒ `unclear`,
   no gateway call.
2. **Review producer:** `memory_assert` with `review:true` parks the memory as
   `pending_review` (not active, not retrievable) + a `memory.pending_review` event.
3. **Review queue:** `memory_review` lists the scope's `pending_review` memories and
   approves (→active, retrievable, cache-invalidated) or rejects (→quarantined),
   each emitting a reversible `memory.review_approved`/`memory.review_rejected` event;
   resolving a non-pending memory ⇒ `ErrNotPending`.
4. **Tiered parity (D-067):** `memory_verify` (mock gateway) and `memory_review` are
   byte-identical across {SDK, HTTP, MCP}.
5. **Scope (P3):** verify, list, and resolve are scope-enforced; cross-tenant memories
   never resolve/list/approve.
6. **Schema/tool discipline:** no new table/column (day-one statuses); two new MCP
   tools ⇒ goldens + tool-count update + same-PR smoke (§4.2).
7. **Gates:** build, `go test -race ./...`, golangci-lint, gofmt, coverage, preflight,
   drift-audit, mirror green.

## Smoke script

`scripts/smoke/phase-25.sh`: `trust.Verify` schema-constrained + gateway-seam;
`trust.ListPending`/`Resolve` present + review.go gateway-free; assert `review` parks
pending_review; `POST /v1/verify` + `/v1/review` routes; `memory_verify` +
`memory_review` MCP tools (+ goldens, count 17); SDK `Verify`/`Review`; unit + parity
tests pass; goldens stable; `make eval-ci` green.

## Test plan

- **Unit — verify:** mock gateway scripted verdicts (entailed/not_entailed/unclear);
  empty citations ⇒ unclear no-call; gateway error ⇒ degraded+unclear.
- **Unit — review:** assert→pending_review; ListPending scope isolation + ordering;
  Resolve approve→active (+ retrievable), reject→quarantined; ErrNotPending on a
  non-pending memory; reversibility event payloads.
- **Parity (§17):** `memory_verify` (mock) + `memory_review` byte-identical across
  surfaces; cross-scope isolation.
- **Fuzz:** the verify JSON-verdict unmarshal surface (seed corpus, CI test).

## Risks & mitigations

- **Verify is non-deterministic / can be wrong.** → it is an advisory safeguard
  callers gate on; degraded-safe; schema-constrained (no free-text parsing). Parity is
  proven with the mock; real-driver behavior is a recorded-fixture test (§17).
- **Review queue could strand memories.** → pending_review is inert but listed; reject
  is reversible (quarantined, re-approvable). A long-quarantine expiry sweep is future.
- **Producer scope (explicit flag, not auto-detect).** → documented in D-084;
  automatic uncited-detection is eval-gated future work.

## Glossary additions

- **Claim verification** — the schema-constrained gateway entailment check that a
  claim is supported by its cited memories; the `memory_verify` capability
  (`POST /v1/verify`), degraded-safe (Phase 25, §6c).
- **Review queue** — the scope-level hold for `pending_review` memories (uncited agent
  assertions): listed and approved (→active) or rejected (→quarantined) via
  `memory_review` (Phase 25, D-084).

## Decisions filed

- **D-084** — Phase 25 ships claim verification (`POST /v1/verify` / `memory_verify`,
  gateway entailment, degraded-safe) and a **scope-level** review queue
  (`memory_review`: list + approve→active / reject→quarantined, reversible) on the
  single-user tier {SDK, HTTP, MCP} — not a credential-admin function. The producer is
  the explicit `review` flag on `memory_assert` (parks `pending_review`); **automatic
  uncited-claim detection is deferred** (needs a citation-on-ingest signal + eval).
  Reasoning-trace export remains Phase 26. No new schema (day-one statuses).
