# Phase 11 — Injections, drill-down, feedback & citations v1

- **Status:** draft
- **Owning subsystem(s):** `internal/retrieval` (injection recording),
  `internal/api` (drilldown/feedback/citations endpoints, envelope v1),
  `internal/store` (injection/feedback methods)
- **RFC sections:** §5.7 (injections — the attribution backbone), §4.2 steps
  5–8, §6c (citations v1), D-025
- **Depends on phases:** 10
- **Informing briefs:** 02 (six-count feedback discipline; rank rises only
  with use/save), 04 (drill-down as the fidelity recovery path)

## Goal

Every retrieval is recorded as **injections** (async, zero added latency);
the envelope graduates to **v1** with citation handles; `/v1/drilldown`
expands provenance to verbatim record spans; `/v1/feedback` wires the six
counters at memory, response (via injections), and citation levels;
`/v1/citations/resolve` makes any handle verifiable. After this phase the
attribution loop is closed: retrieve → inject → cite → feedback → rank shift.

## Design

### Store additions (seam + both drivers + conformance, scope-enforced)

- `Injections()` sub-store: `Append(ctx, scope, []Injection)` (batch, async
  caller), `ListByResponse(ctx, scope, responseID)`,
  `Get(ctx, scope, injectionID)`.
- `Memories().ApplyFeedback(ctx, scope, memoryID, signal)` — atomic counter
  increment by signal (`use|save|fail|noise`) + `last_accessed_at` touch.
- `Feedback()`: `Append` (audit rows; injection_id/response_id optional refs).

### Injection recording (retrieval)

After ranking/limit: build rows {id (ULID = the **citation handle**),
response_id (caller-supplied; generated when absent and echoed), memory_id,
rank, score, lanes (CSV)} → buffered channel → writer goroutine batches
Append (drop-with-metric on full; never blocks the response). Envelope per
item gains `citation` (injection id) + top-level `response_id`; `api: "v1"`.

### Endpoints

- `POST /v1/drilldown` `{memory_id}` or `{citation}` → provenance spans
  hydrated to verbatim record excerpts `{record_id, span, excerpt,
  occurred_at, role}` (batch GetMany on records; spans clamped).
- `POST /v1/feedback` `{response_id, signal}` (response level → resolve via
  injections to all its memories) | `{memory_id, signal}` | `{citation,
  signal: wrong_citation}` (→ injection.feedback marked + memory noise++ +
  fail++ per D-027 groundwork). Signals validated; events emitted.
- `POST /v1/citations/resolve` `{citations: [id]}` → per handle: memory
  (id/kind/content/context/importance/confidence/created_at), provenance
  refs, rank/score/lanes from the injection.
- **Retrieval profiles**: request field `profile: precise|balanced|broad`
  mapping to (k per lane, rerank slice size placeholder, limit defaults) —
  named presets, profile-internal constants.

## Acceptance criteria (binding)

1. Injection rows exist for every retrieval (integration test); writing is
   provably non-blocking (stalled store writer ⇒ retrieve still returns —
   test with fault hook).
2. Response-level `use` feedback increments use_count on exactly the
   response's injected memories (cross-response isolation test).
3. `wrong_citation` marks the injection AND bumps noise+fail on the memory;
   next identical retrieve ranks it measurably lower (end-to-end test).
4. Drill-down returns exact verbatim spans (golden on a fixture);
   citation-based drill-down equals memory-based for the same target.
5. Resolve returns 404-style per-handle misses without failing the batch;
   cross-tenant handles invisible (P3 test on both drivers).
6. Envelope v1 golden (citation + response_id present; debug unchanged);
   smoke drives retrieve→feedback→re-retrieve rank drop.
7. Conformance for all new store methods incl. cross-tenant AND cross-user
   isolation + empty-scope rejection sweep additions.
8. Coverage ≥ 85 new code paths (retrieval band holds, api ≥ 80); `-race`
   ×3 on retrieval; smokes 01–11.

## Files added or changed

```text
internal/retrieval/{injections.go, profiles.go} (+ wiring)
internal/api/{drilldown_handler.go, feedback_handler.go, citations_handler.go}
internal/store/{store.go, types.go, both drivers, conformance}
scripts/coverage.json, scripts/smoke/phase-11.sh
```

## Decisions filed

- D-051: response_id is caller-supplied-or-generated and echoed; citation
  handle == injection ULID (no separate token namespace).

## Risks & mitigations

- Injection volume growth → it's the gain/eval/cache substrate (D-024/25);
  retention is a Phase 14 sweep concern, noted there.
- Feedback abuse (unbounded counters) → int64 + monotonic; rate concerns are
  auth-layer territory, noted for Phase 21.
