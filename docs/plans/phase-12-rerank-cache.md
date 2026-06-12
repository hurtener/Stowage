# Phase 12 — Rerank, hot–warm cache & the SLO rig

- **Status:** draft
- **Owning subsystem(s):** `internal/gateway` (Rerank seam method + drivers),
  `internal/retrieval` (rerank pass + cache), bench rig
- **RFC sections:** §4.2 steps 2/4/SLO, D-031, OQ-9
- **Depends on phases:** 11
- **Informing briefs:** 06 (rerank as the top-tier quality step;
  mempalace ≥99% with rerank), 01 (rerank as optional refinement)

## Pre-verified wire (2026-06-11, gate)

- OpenRouter `POST /v1/rerank` with `cohere/rerank-4-fast` works (Cohere
  shape: `results: [{index, relevance_score}]`, `usage.search_units`).
- The Bifrost SDK v1.5.15 ships `schemas/rerank.go` — native support.

## Design

### Gateway: `Rerank` seam method

`Rerank(ctx, RerankRequest{Query string; Documents []string; TopN int}) →
(RerankResponse{Results []RerankResult{Index int; Score float64}; Usage}, error)`
— drivers: **mock** (deterministic: score = token-overlap ratio), **openaicompat**
(`POST {base}/rerank`, Cohere wire, golden tests; usage.search_units → cost
metering), **bifrost** (SDK rerank request type; client-seam fake tests).
Breaker/retry/metering apply via the existing seam plumbing. New config key:
`gateway.rerank_model` (default `cohere/rerank-4-fast`; only consulted when a
profile enables rerank).

### Retrieval: rerank pass

Profile-gated (`precise` enables; others off): after scoring/sort, take the
top `rerankSlice` (24) candidates, send (query, contents) to Rerank, blend:
`final = 0.6·rerankNorm + 0.4·scoreNorm` (named constants), re-sort, limit.
Failures degrade gracefully (typed log + `degraded_rerank:true` in envelope;
ranking falls back to Phase 10 scores). Cost-aware: skipped when the breaker
is open or the response would exceed a per-call document budget (32).

### Hot–warm cache (OQ-9 simple-first)

- **Hot (result cache):** LRU keyed `(scope.String(), querySignature,
  profile, window)` → response items + support, TTL 60 s, cap 8192 entries.
  Serves before lanes (RFC §4.2 step 2); `cache:"hit"` in envelope debug.
- **Warm (hot set):** per-scope LRU of memory IDs by injection frequency
  (fed by the Phase 11 injection writer); used to pre-warm GetMany batches —
  v1 scope: maintain + expose metrics only; retrieval fast-path consumption
  is measured by the rig before wiring deeper (avoid premature complexity —
  documented).
- **Invalidation:** any committed memory mutation (reconcile Commit, feedback
  counter writes excluded — they don't change content) bumps a per-scope
  generation counter checked on cache read (O(1), no scans). Write paths
  call `cache.InvalidateScope(scope)` via a small interface to avoid
  retrieval→reconcile coupling (event-driven: subscribe to memory.* events
  in-process).
- Live-flag: `STOWAGE_CACHE_OFF=1` env escape hatch for debugging (documented,
  not a config key).

### SLO rig

`make slo` → `internal/bench/slo`: standalone harness (build tag `slo`)
seeding N memories (10k default) into postgres (DSN env), firing 1k
concurrent sessions × M retrieves through the full HTTP stack (httptest or
live serve), reporting p50/p95/p99 + cache hit rate. Binding target recorded
in `eval/SLO.md` with the reference rig description. Bench, not CI gate
(Phase 13 wires gating).

## Acceptance criteria (binding)

1. Rerank golden wire test (openaicompat, Cohere shape) + SDK fake test +
   mock determinism test; usage metered (search_units → cost).
2. Live validation (gate runs it): `cohere/rerank-4-fast` on OpenRouter
   reorders a planted relevance fixture sensibly (tags=live).
3. Profile gating: precise reranks, balanced/broad don't (counting mock);
   failure → `degraded_rerank:true` + Phase 10 order preserved.
4. Cache: identical retrieve twice → second is a hit (zero lane work,
   counting fakes); a reconcile commit in scope invalidates (generation
   test); different scope/profile/window → miss; TTL expiry test (injected
   clock).
5. Mutation-resistant: cache key includes full scope string (cross-tenant/user
   hit impossible — test plants identical queries across scopes).
6. Hot-set metrics exposed; injection writer feeds it (test).
7. SLO rig runs against local postgres (skip without DSN) and emits the
   report; numbers recorded in eval/SLO.md for the reference machine.
8. Coverage ≥ 80 new code; race ×3 retrieval+gateway; smokes 01–12.

## Files added or changed

```text
internal/gateway/{gateway.go (Rerank), types, mock/openaicompat/bifrost drivers}
internal/retrieval/{rerank.go, cache.go, hotset.go, wiring}
internal/config (rerank_model key)
internal/bench/slo/ (tagged)
eval/SLO.md, scripts/smoke/phase-12.sh, scripts/coverage.json
```

## Decisions filed

- D-052: rerank blend constants + slice size; degradation contract.
- D-053: cache invalidation via per-scope generation counters driven by
  memory.* events (OQ-9 resolved simple-first); hot-set v1 is
  metrics-only.
