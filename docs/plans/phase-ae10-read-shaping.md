# Phase ae10 — `layer`/`intent` read-shaping argument *(deferred — own-or-drop)*

- **Status:** deferred
- **Owning subsystem(s):** `internal/retrieval` (the ae3 parameterized renderer/lane layer); the three retrieve surfaces (`internal/mcpserver`, `internal/api`, `sdk/stowage`) — **or** the track charter's identity principle (if dropped).
- **RFC sections:** §6 (retrieval / read-path shaping), §9.5 (one logic core, D-067/D-073)
- **Depends on phases:** **ae2** (the additive `_meta` read) and **ae3** (the parameterized render core, `RenderMode`). Wave 4.
- **Informing briefs:** 06 (mempalace — lean, benchmark-led reader shape; gateway-free retrieval, so any shaping is a pure renderer/lane concern, never a gateway call), 05 (ACE — context shape drives context collapse; shaping is a renderer decision, not an identity one).

## Goal (when promoted)

Resolve the **M2 unowned promise**. Either **own** `layer`/`intent` as an additive,
read-time, scoped retrieval-**output-shaping** argument — threaded through ae3's
parameterized renderer (and, if the shape demands it, the lane candidate query) with
no schema change and all-tier parity — **or formally drop it**: amend the charter's
identity principle (which already flagged the drop, `track-adoption-ergonomics.md`
lines 118–123) and delete this stub in the same PR, so no dangling promise remains.

## Why this stays deferred

`layer`/`intent` describes **how to shape the answer**, not **who is asking** — the
exact reason M2 left the identity principle (conflating shaping with identity is the
named risk). It has **no owner and no confirmed host need** today. Keep the stub thin
until an owner commits to own-or-drop; do not pre-build a shaping arg on speculation
(that would add a D-034 knob/contract surface no caller asked for).

## Design sketch (only if OWNED)

`layer`/`intent` is a read-**output-shaping** input, not an identity dimension and not
a scope: it never widens scope (P3 unchanged — it rides on top of the store's scoped
query), never calls the gateway (D-036 — pure renderer/lane concern), and adds **no
schema**. Threaded as an additive `omitempty` arg on `retrieval.Request` and mirrored
on all three input contracts (the `include_topics` precedent, ae6), it selects a
render/lane shape through ae3's `RenderMode` seam rather than forking a renderer.

## Acceptance criteria (binding — resolve at promotion)

1. **Own-or-drop, no dangling promise.** Exactly one of: (a) an additive read-shaping
   arg lands on all three retrieve surfaces with a parity test and a smoke check, threaded
   through ae3's parameterized renderer with **no schema change**; **or** (b) the charter
   identity principle is amended to record the drop and this stub file is deleted — both
   in the **same PR**.
2. **If OWNED — shaping, not identity/scope (P3).** The arg only re-shapes the caller's
   already-scoped results; it never widens scope, adds no store predicate, opens no
   unscoped path, and makes no gateway call (serves gateway-free, D-036).
3. **If OWNED — knob discipline (D-034).** Any config knob ships same-PR with a tuned
   default, placement in every profile, docs, and a smoke check; zero-config start
   unchanged.

## Smoke script

`scripts/smoke/phase-ae10.sh` — **SKIPs** every check until an owner promotes or drops
this phase (deferred). One SKIP line, exit 0.

## Risks & mitigations

- **Conflating identity with shaping** (the M2 root risk) — the stub stays thin and, if
  owned, `layer`/`intent` lands strictly as a renderer/lane shaping arg, never on the
  identity/scope path.
- **Dangling promise** — AC-1 forces a same-PR resolution: own it with parity, or drop
  it and amend the principle; the charter must not keep an unownable promise.

## Glossary additions

- *(none until promoted — a thin deferred stub adds no vocabulary)*

## Decisions filed

- *(none — M2 own-or-drop; the settling decision id is allocated by the owner at
  promotion, per the charter's per-phase D-141+ sequence.)*
