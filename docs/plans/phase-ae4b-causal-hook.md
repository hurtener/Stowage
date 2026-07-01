# Phase ae4b — causal hook (batch links-exist) + optional positional drilldown *(deferred)*

- **Status:** deferred
- **Owning subsystem(s):** `internal/store` (+ both drivers + conformance); `internal/retrieval` (render-slot fill); `internal/mcpserver`, `internal/api`, `sdk/stowage` (parity). *(Optionally `internal/reconcile`/`internal/episodes` for the drilldown source.)*
- **RFC sections:** §5.6 (typed links), §5.7 (injections/citations), §4.2 (read-path drill-down), §8.1 (Store seam inventory)
- **Depends on phases:** **ae4a** (the episode hook + `RenderMCP` render slots); the shipped typed-links phase (`Store` link reads). **Deferred** — promote only on a confirmed host need.
- **Informing briefs:** 02 (CC-memory — reconciliation/causal-chain model the links come from), 04 (CL-Bench — the gain metric a hot-path N+1 would regress), 06 (mempalace — gateway-free retrieval, temporal-proximity; the hook is a pure store read, never a gateway call)

> **This is a thin deferred stub.** It matches the charter stub (`track-adoption-ergonomics.md`, ae4b). It is fleshed to a full `_template.md` plan only when promoted; the smoke script SKIPs until then.

## Goal

When promoted, MCP retrieve carries a per-item **"has causal edges" marker** so a reader can tell which memories participate in a typed-link/causal chain — added **without a hot-path N+1**. A single scope-required batch `Store.LinksExist(ctx, scope, ids) → map[string]bool` answers the whole result page in one round-trip; the render slot ae4a wired for it is filled behind the `retrieval.causal_hook` knob. Optionally, a positional `(response_id, handle)` drilldown lands on the injections store — the *only* new injections-store method — gated here (not ae4a) because it **is** new store code (H1).

## Findings I'm departing from

- *(To be filled on promotion.)* The charter frames both the batch marker and the positional drilldown as ae4b scope; confirm against code truth at promotion time whether the typed-links read is best expressed as a new `LinksExist` seam method or as a batch variant of an existing `ListLinks` — and record the choice in D-145, honestly, if it departs from the charter's `LinksExist` framing.

## Design (sketch — expand on promotion)

- **`Store.LinksExist(ctx, scope, ids) → map[string]bool`** — one batch round-trip over the typed-links table filtered to the caller's scope; **no per-item `ListLinks`**. New seam method ⇒ implemented on **both** drivers (sqlite + postgres) and proven by the shared conformance suite; forward-only, no new column on the 12 scope tables.
- **Render slot** — fills the causal-hook affordance slot ae4a stood up in `RenderMCP`; inert unless `retrieval.causal_hook=true`.
- **Latency budget** — a documented read-path budget for the extra batch read, bench-gated against the SLO band (D-031/D-095), kept out of the noisy per-PR matrix.
- **Fail-open (D-036)** — a `LinksExist` error omits the hook; retrieval still serves (gateway-free).
- **Optional positional drilldown** — `(response_id, handle)` on the injections store; the single new injections-store method, conformance-tested. Kept optional, gated on a confirmed host need.

## Acceptance criteria (binding — on promotion)

1. `LinksExist` is **one batch round-trip** (no per-item `ListLinks` in the hot path), **scope-required** (no unscoped variant, P3), implemented on **both drivers** and passing the shared conformance suite.
2. The causal hook stays within the **documented read-path latency budget**, measured and bench-gated against the SLO band (D-031/D-095).
3. The hook **fails open** (D-036): a batch error omits the marker, retrieval still serves gateway-free.
4. Any positional `(response_id, handle)` drilldown is the **only** new injections-store method and is conformance-tested.
5. Parity across **{SDK, HTTP, MCP}** with a parity test (MCP included).
6. Knob **`retrieval.causal_hook`** (default `false`) ships **D-034-complete** — tuned default, every-profile placement, docs, smoke check — with zero-config behaviour unchanged.

## Config keys added (on promotion)

| Key | Default | Notes |
|-----|---------|-------|
| `retrieval.causal_hook` | `false` | Enables the per-item "has causal edges" marker (one batch `LinksExist` read). D-034-complete on promotion; inert (byte-identical) when off, so zero-config start is unchanged. |

## Smoke script

`scripts/smoke/phase-ae4b.sh` — **SKIPs every check until promoted** (the surface is deferred); on promotion it asserts `Store.LinksExist` exists, the `retrieval.causal_hook` knob is registered/explainable, and the conformance + parity tests pass. `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan (on promotion)

Conformance (both drivers) for `LinksExist` and any injections drilldown method; a bench asserting the latency budget; a fail-open fault-injection unit; a §17 integration test (real drivers, ≥1 failure mode, `-race`) since the phase closes the typed-links read seam onto retrieval; a {SDK, HTTP, MCP} parity test.

## Risks & mitigations

- **Hot-path N+1** (the whole point) — a single batch `LinksExist`, bench-gated budget.
- **Scope-creep into the positional drill** — kept optional, gated on a confirmed host need; the smoke SKIPs until promoted.

## Glossary additions (on promotion)

- **Causal hook** — a per-item retrieve marker that a memory has typed-link/causal edges, sourced from a single scope-required batch `Store.LinksExist` read; fail-open, knob-gated (`retrieval.causal_hook`).

## Decisions filed (on promotion)

- **D-145** — batch `Store.LinksExist` for the causal hook (one round-trip, both drivers + conformance) + the optional positional injections-store drilldown; fail-open, bench-gated latency budget, `retrieval.causal_hook` knob.
