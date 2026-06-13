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
contribute-mode — wired only to `internal/api` and absent from the 8-method SDK
`Client` interface (and mostly from MCP), so several *binding* capabilities
(D-017 reversibility, D-016 team sharing) are unreachable on the embedded path;
and (2) **path-divergent post-boot wiring** that is hand-duplicated across
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
`boot.Stack`; it is simply not surfaced uniformly. Two of these are *binding*
capabilities unreachable on the embedded path:

| Capability | Reachable on | Missing on | Severity | Evidence |
|---|---|---|---|---|
| **Reconciliation rollback** (D-064/D-017, **binding §6**) + confirm/reject + memory Get/Patch | HTTP | SDK, MCP | blocker | `internal/api/memories_handler.go:198,449`, `sdk/stowage/client.go:10`, `internal/mcpserver/server.go:55-102` |
| **Grants/groups/membership management** (D-016, RFC §5.3) | HTTP | SDK, MCP, CLI | blocker | `internal/api/grants_handler.go:113-349`, `sdk/stowage/client.go:10-41`, `internal/boot/boot.go:159` (svc built, never surfaced) |
| **Topic upsert/delete + `pack:off`** (D-043) | HTTP, MCP | SDK (list-only) | blocker | `internal/api/topics_handler.go:69,127`, `internal/mcpserver/handlers.go:493`, `sdk/stowage/client.go:34` |
| **Buffer flush** (explicit / session_end) | HTTP | SDK, MCP | major | `internal/api/buffers_handler.go:63`, `internal/pipeline/pipeline.go:183`, `sdk/stowage/client.go:10` |
| **Branch fork/merge/discard** (+ discard skip-promotion, D-029) | HTTP | SDK, MCP | major | `internal/api/branches_handler.go:99-101`, `internal/pipeline/pipeline.go:385`, `internal/pipeline/extract.go:125` |
| **`memory_assert`** (direct write, bypasses pipeline) — **reverse direction** | MCP | SDK, HTTP | major | `internal/mcpserver` (assert tool) |
| **Runtime API-key management** (D-030) | HTTP | SDK, MCP | minor | `internal/api` admin routes |

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
- **B1 (D-070):** lift rollback orchestration out of `memories_handler.go` into an
  exported `reconcile.Rollback`; expose Rollback + confirm/reject + memory
  Get/Patch on SDK + MCP.
- **B2 (D-071):** surface-parity for the remaining stratum — topic write,
  buffer flush, branch ops, contribute-mode (full honoring), and (pending the
  Wave-D facade call) grants management — on SDK + MCP.

**Staging is by file-collision, not logical grouping:** `sdk/stowage/client.go`,
`http.go`, `embedded.go`, and the shared SDK suite test are touched by *every*
item here → run as sequential stages, not a wide fan-out (the Harbor 2+2 lesson —
a wide fan-out is a merge bloodbath).

### Wave C — finish or formally defer half-shipped primitives
- **Playbook** (`Client.Playbook` returns `Stub:true`) — finish (launch-scope per
  D-018/D-033) or record a deferral + honest godoc.
- **Runtime API-key management** on the embedded path — first in-process consumer
  or recorded deferral.
- **`memory_assert`** — grant SDK/HTTP parity or record an MCP-only deferral.
- Any verb Wave B deferred rather than re-homed.

### Wave D — decision-shaped RFC remainder
Architecture-level calls the parity findings surface (RFC amendments, not phase
plans):
- **Is `stowage mcp` a standalone server or a thin proxy to a remote stowage?**
  Wave A makes it behave like `serve`; D ratifies the model in the RFC.
- **What is the SDK `Client` contract / the embedded facade?** Which verbs are
  in-scope for embedding — is team-sharing *management* an embedded capability or
  inherently a multi-tenant-server concern? This defines the embedded surface,
  analogous to Harbor's Wave D facade question.
- Codify `boot.StartPipeline` as the canonical post-boot seam (anti-drift).

---

## Checkpoint discipline (§4.5 / CLAUDE.md §17)
At each wave boundary: a read-only audit fan-out + **one cross-phase integration
auditor whose brief includes "hunt path/backend divergences the merge resolutions
introduced"** → one `chore(checkpoint)` PR; the next wave does not dispatch until
it merges.
