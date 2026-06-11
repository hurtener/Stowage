# Phase 07 — Topics + extraction

- **Status:** done
- **Owning subsystem(s):** `internal/topics`, `internal/pipeline` (extract
  stage), `internal/api` (topics endpoints)
- **RFC sections:** §5.4 (topics), §5.2 (kinds, preference fragments), §4.1
  step 3 (extract), §2.1 P1/P5
- **Depends on phases:** 04, 06
- **Informing briefs:** 03 (topics as magnets — no topic match, no memory),
  01 (extraction quality lessons; structured-output discipline), 02
  (anticipated queries + enriched candidate metadata)

## Goal

Flushed buffers become **candidates**: the extract stage builds a
topic-gated, schema-constrained gateway call over the buffer's verbatim
records and emits validated candidate memories (with provenance spans) on a
typed channel for Phase 08. Topics are scope-configurable via the API, with
profile default packs — including the **preference-fragments pack** — so
personalization extraction works with zero configuration.

## Brief findings incorporated

- Brief 03: extraction is gated by topics; a candidate matching no topic is
  never produced. Topics live in config/scope state, not prompt hacks.
- Brief 02: candidates carry entities, keywords, and 3–5 anticipated queries
  at extraction time (the later retrieval lanes depend on them existing now).
- Brief 01: schema-constrained calls only (Phase 04 seam enforces); candidate
  validation is server-side, per-candidate, never trust-the-model.

## Findings I'm departing from

- None.

## Design

### `internal/topics`

Domain over the Phase 03 `TopicStore`: `Topic{Key, Description, Status,
Pack}`. **Default packs** (compiled-in, versioned constants):
`pack:preferences` (assistant profile — "how this user wants to be answered,
addressed, and informed; durable personal facts"), `pack:agent-learnings`
(coding-agent/fleet — gotchas, decisions, patterns, failure modes). Rule:
when a scope has **no active topics**, the profile's default pack applies at
prompt-build time (virtual, not persisted — D-043); any explicit topic
disables the virtual pack.

API (auth'd, tenant from key; project/user/session as query/body params):
- `GET /v1/topics` — list active (explicit or the applicable virtual pack,
  flagged `source: pack|explicit`).
- `PUT /v1/topics` — upsert batch `[{key, description, status}]`.
- `DELETE /v1/topics/{key}`.

### Extract stage (`internal/pipeline/extract.go`)

Consumes the Phase 06 `FlushedBuffer` channel:

1. **SkipPromotion** (branch-discard flushes): no extraction — emit
   `extraction.skipped` event; records remain (P1), working memories were
   never created.
2. Hydrate the flush's records via `Records().Get` (≤ trigger count, loop is
   fine; no seam change).
3. Build the prompt: system template (versioned constant) + active topics
   (key: description lines) + transcript blocks tagged `[record <id>]` with
   role/content. Safety clamp: transcript capped at a per-profile token
   budget (8k assistant / 12k others), oldest-first truncation with a
   truncated-flag in the event.
4. `gateway.Complete` with the **candidate-list JSON schema**:
   `{candidates: [{kind, content, context, entities[], keywords[],
   anticipated_queries[] (3–5), importance (1–5), confidence (0–1),
   provenance: [{record_id, span_start, span_end}]}]}` — kinds restricted to
   the RFC §5.2 enum minus reflection kinds (those arrive Phase 19).
5. **Server-side validation, per candidate** (never reject the batch):
   provenance record_ids ⊆ the flush set (else drop candidate + metric);
   spans clamped to record content length; kind/importance/confidence range
   checks; empty content dropped. Valid candidates get scope + branch stamped
   from the flush (P3 — the model never chooses scope).
6. Emit `CandidateBatch{Scope, BufferKey, Branch, Candidates}` on the typed
   downstream channel (no-op consumer until Phase 08) + an
   `extraction.completed` event (counts: produced, dropped, truncated).
7. Terminal gateway failure (after the seam's retries): dead-letter
   (stage `extract`, the flush descriptor as payload) + event — never data
   loss; records and buffer-flush markers are durable.

Concurrency: N extract workers (per-profile constant) consuming the channel;
no shared mutable state beyond metrics; `-race` proven. Gateway breaker open
(`ErrGatewayUnavailable`) → dead-letter with a distinct reason (the future
re-enqueue sweep retries; D-036 posture).

### serve wiring

`cmd/stowage serve`: gateway.Open now happens at boot (mock driver default
until configured); extract stage sits between the buffer stage's downstream
and the Phase 08 placeholder consumer. Graceful order: api → buffers →
extract → store/gateway close. `readyz` unchanged (gateway probe failure logs
+ degraded, not fatal — D-036).

## Files added or changed

```text
internal/topics/{topics.go, packs.go, topics_test.go}
internal/pipeline/{extract.go, prompt.go, candidates.go, extract_test.go}
internal/api/topics_handler.go
cmd/stowage/main.go              (gateway boot + stage wiring)
scripts/coverage.json            (topics 85)
scripts/smoke/phase-07.sh
```

## Config keys added

None top-level. Per-profile internals: extract worker count, transcript token
budget (knob guardrail: profile constants with docs).

## Acceptance criteria (binding)

1. Prompt golden tests: fixed topics + records ⇒ byte-exact prompt (one per
   pack and one explicit-topics case).
2. Topic gating: a scope whose topics cannot match the fixture conversation
   yields zero candidates (mock gateway scripted to return an off-topic
   candidate ⇒ dropped by validation? No — gating is prompt-side; the test
   asserts the prompt contains only active topics AND that a pack-less,
   explicit-empty-topic scope short-circuits without a gateway call).
3. Preference-fragments fixture: assistant-profile scope with no explicit
   topics extracts `preference` candidates from a fixture conversation
   (mock-scripted), carrying anticipated queries and provenance spans.
4. Per-candidate validation: foreign record_id dropped; spans clamped; batch
   survives one bad candidate (table test).
5. SkipPromotion flushes produce no gateway call and an `extraction.skipped`
   event.
6. Terminal gateway failure dead-letters with the flush descriptor and emits
   an event; nothing is lost (test: dead letter row exists, records intact).
7. Scope stamping: candidates carry the flush's scope/branch regardless of
   model output (P3 test).
8. Coverage ≥ 85 topics / ≥ 80 pipeline; all `-race`; `extraction.completed`
   events carry counts.

## Smoke script

phase-07.sh: serve with mock gateway + temp sqlite; PUT explicit topic; ingest
a small conversation; explicit flush; poll sqlite for `extraction.completed`
event with produced ≥ 1; GET /v1/topics shows explicit topic; DELETE returns
the scope to the virtual pack.

## Test plan

Prompt goldens; candidate-validation table tests; mock-gateway scripted
end-to-end (flush → candidates) under `-race`; fuzz target on the candidate
JSON validation path; pack constants covered by goldens.

## Deviations from plan

- **`pack:off` sentinel**: The spec implied opt-out via topic deletion. The
  implementation adds a `pack:off` sentinel key: a stored topic with
  `key="pack:off"` and `status="active"` opts the scope out of virtual packs
  entirely and short-circuits extraction without a gateway call. This is more
  explicit (the intent is visible in `GET /v1/topics`) and avoids the ambiguity
  of "no topics = virtual pack vs. no topics = user deleted all topics".

- **`Delete` store method returns `ErrNotFound`**: The original `sqlitestore`
  `topics.Delete` did not check `RowsAffected`, so deleting a non-existent key
  silently returned 200. The implementation now checks and returns
  `store.ErrNotFound` (0 rows affected), matching the handler's documented
  404 contract.

## Risks & mitigations

- Prompt drift breaking goldens on every tweak → the template is one
  versioned constant; goldens regenerate via UPDATE_GOLDEN=1 like Phase 02.
- Candidate schema churn (Phase 08 needs more fields) → schema constant lives
  in `candidates.go` with its own version string; additive evolution.

## Glossary additions

- **Pack** (default topic pack) — already implied by §5.4; add explicit
  glossary entry in the PR.

## Decisions filed

- D-043 (next free; verify against docs/decisions.md): default topic packs
  are virtual — applied at prompt-build when a scope has no explicit topics;
  any explicit topic disables the pack. Zero-config extraction without
  hidden persisted state.
