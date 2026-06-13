# Parity-lens findings — Stowage productionization audit

> Read-only seam audit run per `docs/notes/productionization-playbook.md` (§2 / §7).
> Lens confirmed by the owner at GATE 1. Method: 11 parallel investigators (one
> per §7.2 seam) + an adversarial skeptic on every blocker/major, pipelined per
> seam. Calibration: **40 agents, 35 findings (9 blocker / 20 major / 6 minor),
> 28 skeptic-survived, 1 refuted, several severity-recalibrated.** This document
> is the program's citable source of truth; the wave program below is ratified as
> decision **D-067**.

---

## The lens (verbatim, owner-confirmed)

> *"Same code, same seams" must be literally true — every capability, lifecycle,
> and semantic must be reachable and behave identically on the embedded-sqlite
> path AND the server-over-Postgres path; any behavior/lifecycle/capability that
> exists, works, runs, or is observable on only one of the two — in EITHER
> direction — is a parity finding.*

Two axes, checked together: the **path axis** (embedded `sdk/stowage` vs server
`cmd/stowage` → `internal/api` + `internal/mcpserver`) and the **backend axis**
(pure-Go sqlite vs Postgres, `internal/store`). Neither side is privileged;
divergence in either direction is the bug.

---

## Capability tiers (owner clarification, 2026-06-13 — refines the lens)

The owner resolved both GATE-2 questions, which refine the flat lens into a
**tiered** one. Governing principle, verbatim intent: ***logic should be one but
access is through different surfaces (to avoid drift).*** One core; thin surfaces.

The surfaces and their tiers:

- **Embedded SDK (`sdk/stowage`, in-process) — inherently single-user.** It
  exposes only single-user memory capabilities. **Team sharing (grants/groups
  management, contribute-mode) and tenant admin (API-key management) are *not*
  embedded capabilities — by design, not omission.**
- **Server = HTTP (`internal/api`) + MCP (`internal/mcpserver`) — co-equal access
  points over one running stack.** MCP is *not* an embeddable; it is a server
  access point so consumers can manage memory through agents without being forced
  into a proprietary UI. HTTP and MCP must expose the *same* capabilities.

Parity is therefore **tiered**:

| Capability tier | Must be reachable + identical on | Explicitly NOT on |
|---|---|---|
| **Single-user** (ingest, retrieve, drilldown, feedback, citations, topics R/W, rollback, confirm/reject, buffer flush, branches, playbook; the pipeline + lifecycle) | embedded SDK **+** HTTP **+** MCP | — |
| **Multi-user / admin** (grants & group management, contribute-mode, runtime API-key admin) | HTTP **+** MCP | embedded SDK (single-user) |
| **Backend** (every Store capability) | sqlite **+** Postgres | — |

This reclassifies two findings below (grants-management and contribute-mode are
**not** embedded-parity violations) and *sharpens* the rest: the flagship MCP bug
stands (a server surface must run the stack), and the privileged-HTTP-handler
stratum is now a drift between **co-equal surfaces** of one logic core, exactly
the thing the governing principle forbids.

---

## One-paragraph verdict

**The store/backend axis is essentially clean; the path axis is not.** The audit
sought the flagship memory-server risk first — does the async
ingest→extract→reconcile→forget pipeline progress identically on both paths — and
found that on the **embedded SDK** path it *does*: `sdk/stowage` starts the full
buffer/extract/reconcile pipeline and all five lifecycle sweeps, identically to
`stowage serve`, on both backends. The divergence hides in the **other**
direction. `stowage mcp` — a documented standalone server surface (D-020) —
boots the stack and accepts `memory_ingest` but **starts no pipeline and no
sweeps at all**, so MCP-ingested records are durably appended and then never
become memories and never decay (flagship blocker). Around that sit two
structural patterns: (1) a **privileged HTTP-handler stratum** — rollback,
grants/team-sharing management, topic writes, buffer flush, branch ops,
contribute-mode — wired only to `internal/api`. Under the tiered model this
splits cleanly: **single-user** verbs (rollback/D-017, topic write, flush,
branches) absent from the embedded SDK *and* from MCP are genuine parity
violations; **multi-user** verbs (grants/D-016, contribute) are correctly absent
from embedded but are still a drift between the **co-equal server surfaces**
(HTTP has them, MCP does not). (2) **path-divergent post-boot wiring** that is
hand-duplicated across
`runServe`/`runMCP`/`NewEmbedded` and has drifted. The **Store seam itself is a
parity success story** (shared conformance suite exercises both drivers, every
method on both, identical migrations) — the single substantive backend
divergence is FTS query handling (sqlite raw FTS5 `MATCH` vs Postgres
`plainto_tsquery`). Observability *emission* and gateway *metering* hold parity
on both axes; only the scrape/read surfaces are server-only (all minor).

---

## Outright bugs (promoted to the top — these justify the program regardless of the lens)

These are not "parity wishlist" items; they are defects a non-SDK stakeholder
should care about.

### BUG-1 (flagship) — `stowage mcp` runs no pipeline or lifecycle; `memory_ingest` is a silent dead-end
`runMCP` boots the stack, creates `pipelineCh := make(chan pipeline.Item, 4096)`,
hands it to `svc.PipelineIn`, and **never constructs or starts a single stage** —
no `pipeline.New`, `NewExtractStage`, `reconcile.New`, `lifecycle.New`, or
`BackfillSweep`. All of those live only in `runServe`. So on a standalone MCP
server every `memory_ingest` durably appends the verbatim record, enqueues into a
channel **with no consumer**, fills the 4096-slot buffer, then silently drops —
while the tool reports success. `memory_retrieve` returns nothing; P4 forgetting
never runs. Confirmed independently by three seams.
*Evidence:* `cmd/stowage/main.go:425` (channel), `:435-442` (wired to `svc.PipelineIn`, no consumer), `:444-487` (no stage construction), `:603-637` (serve starts all four), `sdk/stowage/embedded.go:118-144` (embedded starts the same four), `internal/boot/boot.go:18` (godoc: stages started by "serve + sdk embedded" — not mcp), `internal/mcpserver/handlers.go:88-106`.
*Severity:* **blocker**, path axis, side=embedded-lacks-nothing/server-mcp-broken. Behavior-changing fix ⇒ its own `fix` PR + decision + E2E.

### BUG-2 — Contribute-mode silently mis-scopes on MCP (accept-and-ignore of a grant-gated field)
The MCP `memory_ingest` contract **declares** `target_scope` /
`contributor_user_id` (D-059), but `makeIngestHandler` never references them and
performs no grant check. A caller who sets `target_scope` believes they
contributed to a pooled owner scope; the record silently lands in their own scope.
This is *worse than absence* — a silent semantic divergence on a grant-gated
write. The SDK omits the capability entirely; only the raw HTTP handler honors it.
*Evidence:* `internal/mcpserver/contracts.go:28-40` (declares fields), `internal/mcpserver/handlers.go:20-118` (handler ignores them), `internal/api/records_handler.go:109-134` (correct HTTP impl + `CheckContributeGrant`), `sdk/stowage/types.go:29-32` (SDK omits fields), `internal/grants/grants.go:235` (one caller only).
*Severity:* **blocker** (security/correctness-shaped), path axis.

### BUG-3 — Embedded construction bypasses config validation, including the D-030 secret-indirection invariant
The embedded constructor does not run the `internal/config` fail-loud validation
that `cmd/stowage` runs before `boot.Open`, so an embedded host can stand up a
stack the server would reject at boot — including the D-030 "API keys never live
in config files" / literal-`api_key` bypass guard (a security invariant) and
silent unknown-profile degradation.
*Severity:* **major** (security-adjacent), path axis, config-boot seam.

### BUG-4 — sqlite FTS hard-errors on operator/special-char queries and silently drops the lexical lane
The lexical/queries lane uses raw FTS5 `MATCH` on sqlite vs `plainto_tsquery` on
Postgres. The same user query yields different hit sets, and on sqlite a query
containing FTS operators or special characters **hard-errors**, silently dropping
both lexical lanes — a sqlite-only robustness cliff on otherwise-valid input.
*Evidence:* retrieval-ranking + store seams (sqlite `MATCH` vs pg `plainto_tsquery`).
*Severity:* **major**, backend axis, side=sqlite.

### BUG-5 — Embedded drill-down byte-slices multi-byte runes (invalid UTF-8)
Drill-down excerpt shaping is duplicated and diverged: the server (HTTP+MCP) path
is UTF-8 rune-safe; the SDK embedded path byte-slices the provenance span and can
split a multi-byte rune, returning invalid UTF-8.
*Severity:* minor (recalibrated from major by the skeptic — only manifests when a
span offset lands mid-rune), path axis, side=embedded. A real correctness defect;
trivial fix; bundle with Wave A.

---

## Cross-cutting patterns

### Pattern P1 — The privileged HTTP-handler stratum (path divergence; the dominant pattern)
A whole class of write/control/management verbs is wired only to `internal/api`
HTTP handlers, absent from the 8-method SDK `Client` interface
(`Ingest/Retrieve/Drilldown/Feedback/ResolveCitations/Topics(list-only)/Playbook(stub)`)
and mostly from the MCP tool set. The service-layer logic largely exists on
`boot.Stack`; it is simply not surfaced uniformly. Read against the **tiered
model**: single-user verbs must reach {SDK, HTTP, MCP}; multi-user verbs must
reach {HTTP, MCP}.

| Capability | Tier | Reachable on | Missing where it should be | Severity | Evidence |
|---|---|---|---|---|---|
| **Reconciliation rollback** (D-064/D-017, **binding §6**) + confirm/reject + memory Get/Patch | single-user | HTTP | SDK, MCP | blocker | `internal/api/memories_handler.go:198,449`, `sdk/stowage/client.go:10`, `internal/mcpserver/server.go:55-102` |
| **Topic upsert/delete + `pack:off`** (D-043) | single-user | HTTP, MCP | SDK (list-only) | blocker | `internal/api/topics_handler.go:69,127`, `internal/mcpserver/handlers.go:493`, `sdk/stowage/client.go:34` |
| **Buffer flush** (explicit / session_end) | single-user | HTTP | SDK, MCP | major | `internal/api/buffers_handler.go:63`, `internal/pipeline/pipeline.go:183`, `sdk/stowage/client.go:10` |
| **Branch fork/merge/discard** (+ discard skip-promotion, D-029) | single-user | HTTP | SDK, MCP | major | `internal/api/branches_handler.go:99-101`, `internal/pipeline/pipeline.go:385`, `internal/pipeline/extract.go:125` |
| **`memory_assert`** (direct write, bypasses pipeline) — **reverse direction** | single-user | MCP | SDK, HTTP | major | `internal/mcpserver` (assert tool) |
| **Grants/groups/membership management** (D-016, RFC §5.3) | multi-user | HTTP | **MCP** (correctly absent on SDK) | major | `internal/api/grants_handler.go:113-349`, `internal/mcpserver/contracts.go` (no grant tool) |
| **Contribute-mode ingest** (D-059) | multi-user | HTTP | **MCP** (correctly absent on SDK) | major (+ BUG-2 silent mis-scope on MCP) | `internal/api/records_handler.go:109-134`, `internal/mcpserver/handlers.go:20-118` |
| **Runtime API-key management** (D-030) | multi-user/admin | HTTP | MCP (correctly absent on SDK) | minor | `internal/api` admin routes |

> **Reclassified by the owner's tiered model:** grants management, contribute-mode,
> and API-key admin are **multi-user/admin** — their absence on the embedded SDK
> is *correct by design* (embedded is single-user), not a parity violation. They
> remain a drift between the **co-equal server surfaces**: present on HTTP, missing
> on MCP, which the governing principle (one core, parity across surfaces)
> requires closing. Grants management is thus a **major server-surface (HTTP↔MCP)
> finding**, not an embedded blocker.

The **branch-discard** case has a real P4 consequence: discard flushes with
`SkipPromotion=true`, so speculative branch content is *suppressed* from durable
memory — but only on the HTTP path. On embedded/MCP the same buffered records
flush via the age trigger and **leak into durable memory**.

### Pattern P2 — Path-divergent post-boot wiring (S1/G3)
`boot.Open` is a genuinely shared, driver-agnostic core. But the post-boot step
that turns a static stack into a *live* system — starting the pipeline stages and
the lifecycle/backfill sweeps — is hand-duplicated outside `boot` across
`runServe`, `runMCP`, and `NewEmbedded`, and has drifted:
- **BUG-1** (MCP starts nothing) is the severe instance.
- **`BackfillSweep` (embed recovery) runs only in `serve`** — absent from embedded
  SDK and MCP, so on the embedded path a degraded/saturated gateway (D-036
  scenario) permanently strands dropped embeddings → silent vector-lane
  incompleteness. *Evidence:* `cmd/stowage/main.go:613`, `internal/boot/boot.go:21,29` ("serve-only optimisation"), absent from `embedded.go`.

**Root cause + fix shape:** there is no single shared "start the live system"
helper. The structural remedy (proposed as a program principle): a
`boot.StartPipeline(ctx, stk, cfg) → drainer` consumed by all three entrypoints,
so they cannot drift again.

### Pattern P3 — Config/boot honesty gap (G2; embedded bypasses `config.Load`)
Embedded construction skips the `config.Load` validation+defaults+profile-merge
layer that the server runs. Consequences: BUG-3 (validation/secret-indirection
bypass); **gateway model/dims/rerank defaults (`config.Defaults`) not applied on
embedded** → vector lane is a *complete silent no-op* at the documented-minimal
config while the server populates it at 512 dims (major); profile override map and
`STOWAGE_SWEEP_FORCE` honored only on serve (minor).

### Pattern P4 — Backend axis is clean except FTS (the Store seam is a parity success)
The shared conformance suite exercises **both** drivers (CI sets
`STOWAGE_TEST_PG_DSN` against `postgres:17`), every Store/EventStore/VectorStore/Ops
method is implemented on both, migrations 0001-0006 are identical, and the only
no-op (`AdvisoryLock` on sqlite) is the documented D-057 exclusion. The single
substantive backend divergence is **BUG-4** (FTS5 `MATCH` vs `plainto_tsquery`).
Worth stating explicitly so the program does not over-invest on this axis.

### Pattern P5 — Observability: emission holds parity, exposure is server-only (all minor)
Event emission through `Store.Events()` and gateway metering run identically on
both paths and both backends (parity holds — the counters increment on embedded
too). Only the **scrape/read surfaces** are server-only: the Prometheus registry
and the event-read/audit-trail endpoint are unreachable in-process. Exposure-only;
all recalibrated to minor.

### Refuted (1)
- *"`resolve_citations` missing from MCP"* — **refuted**. The capability is
  reachable on both the embedded path (SDK) and the server path (HTTP); MCP is one
  transport of the server path, not a separate path. Not a parity violation under
  the defined axes.

---

## The wave program (triage by change-type, per §3)

> Decision numbers are pre-reserved in D-067 so parallel agents append without
> collision. Each wave's plans are authored from `docs/plans/_template.md` per the
> §16 ritual and reviewed as a docs PR before implementation. Re-homing / parity
> fixes carry a **both-paths-identical behavior bar**: the same conformance
> scenario passes embedded AND server in the same PR.

### Wave A — correctness + honesty (fix now, no design)
- **A1 (own `fix` PR + D-068 + E2E):** introduce `boot.StartPipeline` and wire it
  into `runMCP` so MCP drives the identical pipeline + sweeps (BUG-1). Refactor
  `runServe` and `NewEmbedded` onto the same helper so the three paths cannot
  drift again. Behavior-changing ⇒ not buried in hygiene.
- **A2:** stop the contribute-mode silent mis-scope (BUG-2) — minimally
  **fail-loud** on MCP/SDK when `target_scope` is set but unhonored (full honoring
  is Wave B). Honesty/security.
- **A3:** embedded config validation + D-030 secret-indirection guard (BUG-3).
- **A4:** sqlite FTS query sanitization to match Postgres robustness (BUG-4).
- **A5:** embedded drill-down rune-safety (BUG-5).
- **A6:** apply `config.Defaults` (gateway model/dims/rerank) on the embedded path
  (Pattern P3 silent vector/rerank degradation).
- **A7 (chore punch-list):** doc honesty — correct `boot.go` godoc framing, the MCP
  `memory_ingest` "mirrors POST /v1/records" description, and any README/godoc
  claiming a capability is reachable where it isn't.

Delivery: A2–A7 unify into one `chore`/`fix` punch-list where non-behavioral; A1
(and any other behavior-changer) gets its own `fix` PR + decision + E2E.

### Wave B — mechanical re-homing / surface-parity
Lift handler-resident orchestration into the shared service layer (where not
already there) and expose the management/control verbs uniformly across {SDK
`Client`, MCP tools, HTTP}, with the both-paths-identical bar.
Apply the **tiered** target: single-user verbs reach {SDK, HTTP, MCP};
multi-user verbs reach {HTTP, MCP} only (never the SDK).
- **B1 (D-070) — single-user reversibility:** lift rollback orchestration out of
  `memories_handler.go` into an exported `reconcile.Rollback`; expose Rollback +
  confirm/reject + memory Get/Patch on **SDK + MCP**.
- **B2 (D-071) — single-user control verbs:** topic write, buffer flush, branch
  ops, `memory_assert` on **SDK + MCP**.
- **B3 (D-071, server-surface only):** grants/group management and contribute-mode
  (full honoring) on **MCP** to match HTTP — **not** added to the SDK (multi-user;
  embedded is single-user). Closes the HTTP↔MCP drift the governing principle
  forbids.

**Staging is by file-collision, not logical grouping:** `sdk/stowage/client.go`,
`http.go`, `embedded.go`, and the shared SDK suite test are touched by every
single-user item → run B1/B2 as sequential stages, not a wide fan-out (the Harbor
2+2 lesson — a wide fan-out is a merge bloodbath). B3 touches the MCP surface +
shared core only (SDK untouched), so it can run alongside B1/B2 without colliding.

### Wave C — finish or formally defer half-shipped primitives
- **Playbook** (`Client.Playbook` returns `Stub:true`) — finish (launch-scope per
  D-018/D-033) or record a deferral + honest godoc.
- **Runtime API-key management** — confirm it reaches both server surfaces (HTTP +
  MCP); record the by-design embedded exclusion.
- Any verb Wave B deferred rather than re-homed.

### Wave D — decision-shaped RFC remainder
**Both original GATE-2 questions are now resolved by the owner (2026-06-13) and
recorded in the tiered model above:**
- *Is team-sharing management an embedded capability?* — **No.** Embedded is
  inherently single-user; grants/contribute/admin are server-surface tiers.
- *Is `stowage mcp` standalone or a proxy?* — **A server access point**, co-equal
  with HTTP over the running stack (MCP adds management-via-agents so consumers
  aren't forced through a proprietary UI). It runs the stack (Wave A fix stands).

The remaining decision-shaped work for the RFC amendment:
- **Server deployment shape:** does one `stowage serve` process expose *both* HTTP
  and MCP over a single stack/pipeline (preferred — strictly one logic core), or
  does `stowage mcp` remain a separate process that also runs a full stack? Either
  satisfies "MCP runs the pipeline"; the RFC picks the canonical shape.
- **Codify the governing principle** — *one logic core, thin surfaces* — with
  `boot.StartPipeline` as the canonical anti-drift post-boot seam and the tiered
  capability matrix as the surface contract.

---

## Checkpoint discipline (§4.5 / CLAUDE.md §17)
At each wave boundary: a read-only audit fan-out + **one cross-phase integration
auditor whose brief includes "hunt path/backend divergences the merge resolutions
introduced"** → one `chore(checkpoint)` PR; the next wave does not dispatch until
it merges.
