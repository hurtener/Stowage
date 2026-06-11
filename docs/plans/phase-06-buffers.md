# Phase 06 — Buffers

- **Status:** draft
- **Owning subsystem(s):** `internal/pipeline` (buffer stage)
- **RFC sections:** §4.1 (write path), §2.1 P2
- **Depends on phases:** 05
- **Informing briefs:** 03 (Engram buffers: collect across runs/agents, flush
  on triggers — the multi-agent write surface), 02 (write-contention lessons)

## Goal

The first real pipeline stage: ingested records accumulate per
`(scope, branch, buffer key)` and flush downstream when a trigger fires —
count, token estimate, max age, session end, or explicit flush. Many agents
feed one buffer without blocking each other; flush is exactly-once; crash
recovery is structural (durable items + due-scan), not bolted on. Downstream
is a typed channel a no-op consumer drains until Phase 07 replaces it with
extraction.

## Brief findings incorporated

- Brief 03: buffers are how a fleet learns continuously without agents
  blocking each other — flush triggers are the core contract.
- Brief 02: contention discipline — buffer writes ride the Phase 05 ingest
  path (already durable); the stage only *consumes*, never re-writes records.

## Findings I'm departing from

- None.

## Design

### Flow

Phase 05's enqueue channel delivers record IDs. The buffer stage:

1. Resolves the buffer key: explicit `buffer_key` ingest hint, else derived
   `(session_id, branch_id)`.
2. Appends a `buffer_items` row (durable; store BufferStore from Phase 03).
3. Evaluates count + token triggers against the buffer's unflushed items.
4. A jittered ticker (5 s) scans `ListDue` for age-triggered and
   crash-recovered buffers (items older than max age with no flush).
5. `Flush` (store-atomic, exactly-once — proven in Phase 03 conformance)
   marks items and emits `FlushedBuffer{scope, key, branch, record_ids,
   token_estimate, trigger}` on the downstream channel + a `buffer.flushed`
   event with the trigger reason.
6. Session-end and explicit flush arrive via `POST /v1/buffers/{key}/flush`
   (added to the API this phase) and via branch discard (Phase 05 hook: a
   discarded branch's buffers flush with trigger `branch_discard` and a flag
   the extraction stage will use to skip promotion).

### Trigger defaults (profile values, not top-level knobs)

| Trigger | assistant | coding-agent | fleet |
|---|---|---|---|
| count | 12 | 20 | 30 |
| tokens | 1500 | 2500 | 4000 |
| max age | 90 s | 180 s | 120 s |

Resolves **OQ-3** with these starting values; the eval harness re-tunes later.

### Concurrency posture

The stage is a small supervised goroutine set: N stage workers consuming the
ingest channel (per-buffer serialization via a keyed mutex so trigger
evaluation doesn't race), one ticker goroutine. Graceful drain on shutdown:
stop intake, flush nothing implicitly (items are durable; due-scan recovers
next boot). `-race` proven with many writers across many buffers.

## Files added or changed

```text
internal/pipeline/{pipeline.go, buffer.go, triggers.go, buffer_test.go}
internal/api/buffers_handler.go    (flush endpoint)
cmd/stowage/main.go                (serve wires the stage)
scripts/coverage.json              (pipeline 80)
scripts/smoke/phase-06.sh
```

## Config keys added

None top-level (trigger values are profile-internal; knob guardrail).

## Acceptance criteria (binding)

1. Trigger matrix table-tested: count, tokens, age, explicit, session-end,
   branch-discard — each produces exactly one flush with the right reason.
2. Exactly-once under concurrency: many concurrent appenders + racing
   triggers ⇒ every item consumed exactly once (extends the Phase 03
   conformance proof to the stage level, `-race`).
3. Branch isolation: items from branch X never flush into branch Y's or the
   parent's `FlushedBuffer`.
4. Crash recovery: items appended then process restarted ⇒ due-scan flushes
   them within one ticker period (test with a fresh stage instance).
5. Ingest ACK latency unchanged (Phase 05 bench still passes — P2).
6. `buffer.flushed` events carry trigger reasons; coverage ≥ 80, `-race`.

## Smoke script

phase-06.sh: live server; ingest N small records → age/count flush observed
via events endpoint? (events SSE lands later — assert via sqlite query);
explicit flush endpoint 202.

## Test plan

Table-driven trigger matrix; stage-level race test; restart-recovery test;
golden on FlushedBuffer payload.

## Risks & mitigations

- Keyed-mutex map growth → periodic sweep of idle keys.
- Ticker scan cost at many buffers → indexed ListDue (Phase 03 indexes);
  measure in bench.

## Glossary additions

None (buffer already defined).

## Decisions filed

- OQ-3 resolved with the trigger-default table above (filed as D-042, superseding the provisional D-041 reference).
