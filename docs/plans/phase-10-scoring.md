# Phase 10 — Scoring & ranking

- **Status:** draft
- **Owning subsystem(s):** `internal/scoring` (new, pure), `internal/retrieval`
  (wiring), `internal/store` (counter/feedback inputs already exist)
- **RFC sections:** §5.2 (counters, decay, trust), §4.2 step 4 (scoring
  inputs), §4.2.5 (support summary)
- **Depends on phases:** 09 (09b/09c landed; no coupling)
- **Informing briefs:** 02 (THE source: six-counter model, turn-based decay
  critique, precision factor, exploration bonus, hub dampening, cooldown),
  06 (temporal-proximity boost)

## Goal

Fused candidates get ranked by the full utility model: a **pure, table-tested
scoring function** (no I/O, no clock reads — `now` and activity inputs are
parameters) combining utility counters, decay, trust, scope affinity,
temporal proximity, hub dampening, and write-echo cooldown; plus a per-response
**support summary** (evidence strength + agreement/conflict) and a
`debug=true` score breakdown in the retrieve response.

## Brief findings incorporated (brief 02, adapted per RFC §5.2)

- Six counters with distinct roles: `useBoost = 1 + log2(1 + use + 2·save)`;
  `noisePenalty = 1/(1 + 0.15·noise)` floored at 0.4; **precision factor**
  ramp 0.5–1.5 from the use/inject ratio (zombie memories sink);
  **exploration bonus** 1.3× when `inject < 3` (new memories get a chance).
- **Decay**: `exp(-Δ/effectiveStability)` where Δ blends scope-activity turns
  and wall-clock (`Δ = α·turnsNorm + (1−α)·daysNorm`, α=0.6) — fixes the
  predecessor's dormant-project blind spot; `effectiveStability = stability ·
  (1 + log2(1 + use + 2·save))`. Floors: 0.10 default, 0.50 for
  `user_stated`.
- **Trust multipliers**: user_stated 1.25, agreed_upon 1.15, agent_suggested
  1.0, llm_extracted 0.95 (scoring weights — distinct from the Phase 08
  supersede-gate multipliers; document the difference).
- **Scope affinity**: same-session 1.3×, same-project 1.15×, tenant-only 1.0.
- **Temporal proximity** (brief 06): when the query carries a window, boost
  candidates whose `occurred_at`-derived provenance or created_at falls inside
  proportionally to closeness (max 1.2×).
- **Hub dampening**: memories returned by ≥4 distinct query-token clusters in
  the recent window get 0.8× (generic-content penalty); tracked via a small
  in-memory LRU of (memory_id → distinct query signatures), not persisted.
- **Write-echo cooldown**: memories created < 30 min ago are suppressed
  (score ×0.1) for retrievals in the SAME session they were extracted from
  (anti-echo) — other sessions see them normally.
- **Importance**: multiplicative `0.8 + importance/10` (1→0.9, 5→1.3).

## Design

### `internal/scoring` (pure)

```go
type Inputs struct {            // everything the function may consult
    Memory       MemoryFacts    // counters, importance, confidence, trust, stability, created/last-accessed, session of origin
    FusedScore   float64        // RRF output
    Now          int64
    ActivityTurns int64         // scope turns since last access (from records count delta — supplied by retrieval)
    QueryWindow  *Window
    SameSession  bool
    HubSignals   int            // distinct recent query clusters containing this memory
}
func Score(in Inputs) (float64, Breakdown)
```
`Breakdown` carries every factor (named) for debug mode and goldens. No
package-level state; hub LRU and activity lookups live in retrieval and feed
Inputs.

### Support summary

After ranking: `support: {strength: weak|moderate|strong, top_score,
conflicts: [{a, b}]}` — strength from top-3 score mass; conflicts from
existing `contradicts` links among returned memories (store ListLinks batch —
one query). Attached to every retrieve response (RFC §6c groundwork).

### Wiring

retrieval: after RRF → batch-load memory facts (GetMany already returns what's
needed; add counters to the projection if missing) → Score each → sort →
limit → support summary → optional `debug` flag echoes Breakdowns.
ActivityTurns: cheap approximation = records count in scope since memory's
last_accessed_at (one COUNT per retrieve, scope-indexed) — documented.

## Files added or changed

```text
internal/scoring/{scoring.go, scoring_test.go, golden_test.go}
internal/retrieval/{retrieval.go (wire), hub.go (LRU), support.go}
internal/api/retrieve_handler.go (debug flag + support in envelope)
internal/store (GetMany projection if counters missing; ListLinks batch if needed)
scripts/coverage.json (scoring 90)
scripts/smoke/phase-10.sh
```

## Config keys added

None (all constants profile-internal; knob guardrail).

## Acceptance criteria (binding)

1. `Score` is pure (no time/rand/io imports — lint-style test) and
   table-tested across every factor with hand-computed expectations.
2. Golden breakdown test (fixed Inputs → exact Breakdown JSON).
3. Property tests: more use ⇒ never lower score; more noise ⇒ never higher;
   decay floor respected (user_stated ≥ 0.5 factor; default ≥ 0.1).
4. Zombie vs fresh: high-inject/zero-use ranks below low-inject/high-use with
   otherwise identical facts (the brief-02 signature test).
5. Cooldown: a memory extracted in session S is suppressed for S but not for
   another session (integration test through retrieve).
6. Hub dampening: a memory hit by 4 distinct synthetic query clusters drops
   below an otherwise-equal memory (test drives retrieval LRU).
7. Support summary: planted `contradicts` pair among results is reported;
   strength buckets table-tested.
8. `debug=true` returns per-item breakdowns; absent by default (envelope
   golden updated; api stays "v0").
9. Coverage ≥ 90 scoring; retrieval bands hold; all `-race`; smokes 01–10.

## Risks & mitigations

- Constant soup → every constant named, documented, and golden-pinned;
  Phase 13 eval re-tunes with data.
- ActivityTurns COUNT cost → scope-indexed; measured in bench; cacheable later.

## Decisions filed

- D-050: scoring trust multipliers are distinct from supersede-gate
  multipliers (different jobs: rank vs protection); both documented in one
  place (scoring.go doc comment cross-referencing trust.go).
