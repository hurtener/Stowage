# Phase 03 — Store seam + the day-one schema

- **Status:** draft
- **Owning subsystem(s):** `internal/store` (+ drivers), migrations
- **RFC sections:** §5.0–§5.7 (data model), §8 (storage), §2.1 P1/P3
- **Depends on phases:** 02
- **Informing briefs:** 01 (schema sprawl anti-goal; pgvector/FTS hybrid),
  02 (sqlite single-writer contention → dedicated writer goroutine), 04
  (fidelity layer), 06 (temporal validity)

## Goal

All durable state behind one `Store` interface with two conformance-equal
drivers (postgres principal, sqlite embedded) and the **full day-one schema**
(RFC §8.1) in the first migration set — so every later phase adds behavior,
not migrations-under-pressure. After this phase, `stowage migrate` works
against both drivers and the conformance suite proves scope isolation at the
query layer.

## Brief findings incorporated

- Brief 02: sqlite multi-second lock waits under concurrent writers → the
  sqlite driver serializes ALL writes through one writer goroutine (bounded
  channel, batched transactions); readers run WAL-concurrent.
- Brief 01: 48-table sprawl → exactly the §8.1 inventory; nothing else.
- Brief 04/D-024: unbackfillable signals (occurred_at, branch_id, outcome,
  injections, links) present from migration 0001.

## Findings I'm departing from

- `memory_vectors` (embedding storage) is **deferred to Phase 09**: embeddings
  are recomputable from content — they are caches, not signals — so deferring
  is not a D-024 violation, and it keeps the pgvector extension out of Phase
  03's CI. Recorded as the one deliberate §8.1 deviation; RFC table footnote
  to be added in the Phase 09 PR.

## Design

### The seam

```go
type Store interface {
    Migrate(ctx context.Context) error
    Records() RecordStore
    Memories() MemoryStore
    Topics() TopicStore
    Buffers() BufferStore
    Keys() auth.Keyring        // store-backed keyring (Phase 02 interface)
    Events() EventStore
    Ops() OpsStore             // dead letters + job markers
    Close(ctx context.Context) error
}
```

Sub-interfaces are deliberately sized to Wave-2 needs; later phases add
methods to the seam and every driver implements them (CLAUDE.md §9). Every
read/list method takes `identity.Scope` as its first data argument; **no
unscoped variant exists**. Scope matching: hard equality on tenant; project/
user/session filter when set (P3).

Construction: `store.Open(ctx, cfg.Store, deps)` → driver registry via
blank-import `init()` (CLAUDE.md §4.4): `internal/store/sqlitestore`,
`internal/store/pgstore`.

### Initial method set (W2-sized)

- `RecordStore`: `Append(ctx, scope, []Record) error` (idempotent on id),
  `Get(ctx, scope, id)`, `ListBySession(ctx, scope, sessionID, branchID,
  limit, cursor)`, `ListUnprocessed(ctx, scope?, olderThan, limit)`,
  `MarkProcessed(ctx, ids)`.
- `MemoryStore`: `Insert`, `Get` (with junctions + provenance), `Update`
  (full-row, optimistic via updated_at), `SetStatus`, `ListByStatus`,
  `InsertLinks`, `ListLinks(from|to)`, `AddProvenance`. (Search lands Phase 09;
  counters/feedback methods land Phase 10/11.)
- `TopicStore`, `BufferStore` (append item, list due by trigger inputs, flush
  = consume atomically), `EventStore` (`Emit` within the caller's txn where
  the driver supports it, `List` scoped + cursor), `OpsStore` (dead-letter
  put/list/resolve; job marker check-and-set).
- `Keys()`: implements the Phase 02 `auth.Keyring` against `api_keys`.

### The day-one schema (migration 0001, both dialects)

Portable type policy: ids TEXT (ULID); timestamps INTEGER unix-millis in both
dialects (uniform conformance semantics; humans use views/tooling); enums
TEXT with CHECK; flexible payloads TEXT JSON; the six counters are INTEGER
columns. Scope columns on every row: `tenant_id TEXT NOT NULL`, `project_id/
user_id/session_id TEXT NULL`.

Tables (21): `records` (occurred_at, branch_id, response_id, outcome,
outcome_detail, role, content, source_agent, token_estimate, processed_at),
`memories` (kind, content, context, status, importance, confidence,
trust_source, match_count, inject_count, use_count, save_count, fail_count,
noise_count, stability, last_accessed_at, valid_from, valid_until,
episode_id, supersedes_id, superseded_by_id, privacy_zone),
`memory_entities`, `memory_keywords`, `memory_queries` (anticipated queries),
`provenance` (memory_id, record_id, span_start, span_end),
`injections` (response_id, memory_id, rank, score, lane, was_cited, feedback),
`links` (from_memory, to_memory, type, source, confidence),
`episodes` (title, status, started_at, ended_at, narrative_memory_id, outcome),
`branches` (session_id, parent_branch_id, status),
`topics` (key, description, status, pack),
`buffer_items` (buffer_key, branch_id, record_id, token_estimate, flushed_at),
`groups` (name), `group_members` (group_id, user_id),
`grants` (group_id, owner-scope cols, access, topic_filter, kind_filter,
zone_ceiling, redaction_profile, revoked_at),
`feedback` (memory_id, injection_id, response_id, signal, note),
`suggestions` (trigger_kind, memory_id, episode_id, status + counters),
`scope_settings` (key, value JSON), `api_keys` (per Phase 02 model),
`events` (type, subject_id, reason, payload), `dead_letters` (stage, item_id,
error, attempts, resolved_at), `job_markers` (job, marker, ran_at).

Indexes (0001): `(tenant_id, occurred_at)` on records; `(tenant_id, session_id,
branch_id)` on records; `(tenant_id, status)` + `(tenant_id, kind)` on
memories; `(response_id)` on injections; `(from_memory, type)` + `(to_memory)`
on links; `(memory_id)` on provenance + junctions; uniques: api_keys(id),
job_markers(job, marker), topics(scope, key).

`group_members`/`group` push the count to 21+2 ops tables; the RFC §8.1 table
counts groups as one line — acceptable; the inventory note stays binding.

### Migrations

`internal/store/migrations/{sqlite,postgres}/0001_init.sql`, embedded
(`embed.FS`), applied in order inside a transaction where the dialect allows,
recorded in `schema_migrations(version, applied_at)`. Forward-only (CLAUDE.md
§9). `stowage migrate [--config]` CLI wires `Store.Migrate`; `migrate
--status` prints applied/pending.

### Drivers

- **sqlite** (`modernc.org/sqlite`): DSN file path; PRAGMAs journal_mode=WAL,
  busy_timeout=5000, foreign_keys=ON, synchronous=NORMAL. One writer
  goroutine owns a dedicated write connection; write ops are closures sent
  over a bounded channel and executed in micro-batched transactions; reads use
  a pool. Shutdown drains the channel.
- **postgres** (`jackc/pgx/v5` pool): plain SQL, no ORM; advisory-lock helper
  exposed on `OpsStore` for later singleflight (Phase 14 consumes).

### Conformance suite

`internal/store/conformance` exports `Run(t, factory)` covering: CRUD per
sub-store; **scope isolation** (cross-tenant and cross-user reads return
nothing, on every list/get); record immutability (no update API; Append
idempotency); unprocessed→processed flow; buffer flush atomicity (concurrent
appenders + one flusher, exactly-once consumption); keyring conformance
(Phase 02 suite reused); event emit/list ordering; dead-letter and job-marker
semantics; migration idempotency (Migrate twice = no-op). sqlite runs it
in-repo; postgres runs it when `STOWAGE_TEST_PG_DSN` is set — CI gets a
postgres:17 service container so **both drivers run in CI**.

## Files added or changed

```text
internal/store/{store.go, types.go, errors.go, registry.go}
internal/store/conformance/conformance.go
internal/store/sqlitestore/...
internal/store/pgstore/...
internal/store/migrations/{sqlite,postgres}/0001_init.sql
cmd/stowage/main.go            (migrate subcommand)
scripts/smoke/phase-03.sh
scripts/coverage.json          (store packages: 85 — CLAUDE.md §11 band)
.github/workflows/ci.yml       (postgres service; STOWAGE_TEST_PG_DSN)
go.mod                         (+ modernc.org/sqlite, jackc/pgx/v5, oklog/ulid/v2)
```

## Config keys added

None beyond Phase 02's `store.driver`/`store.dsn` (+ `store.migrate`:
`auto`|`manual`, default `auto`).

## Acceptance criteria (binding)

1. Conformance suite green on **both** drivers under `-race` (postgres in CI
   via service container).
2. Cross-scope isolation proven by conformance on every read path.
3. sqlite concurrent-writer test (≥8 goroutines × ≥200 writes + concurrent
   readers) completes with zero SQLITE_BUSY surfacing to callers and no
   multi-second stalls.
4. `Migrate` is idempotent; `migrate --status` reports correctly; editing an
   applied migration is detected (checksum) and fails loudly.
5. EXPLAIN-verified index use on postgres for: temporal window list, status
   list, injections-by-response (asserted in a driver test).
6. Append is idempotent on record id (duplicate batch ⇒ no dup rows).
7. Coverage ≥ 85 on both driver packages and the seam package.

## Smoke script

phase-03.sh: `stowage migrate --status` against a temp sqlite DB; migrate;
re-run idempotent; insert/read round-trip via a tiny test helper binary or
`go test -run` invocation.

## Test plan

Conformance (shared); driver-specific: sqlite writer-goroutine race test,
postgres EXPLAIN assertions; fuzz: ULID/scope parsing if any custom parsing
exists; benchmarks: `BenchmarkAppendRecords`, `BenchmarkListByStatus` (baseline
only).

## Risks & mitigations

- Dialect drift between the two 0001 files → conformance suite is the
  arbiter; keep DDL minimal and portable (INTEGER millis policy).
- pgx/modernc version churn → pin via go.mod; no SDK-default behaviors relied
  on.
- Writer-goroutine deadlock on shutdown → drain with context deadline; test.

## Glossary additions

None.

## Decisions filed

- D-037: timestamps stored as INTEGER unix-millis in both dialects; ids are
  ULIDs in TEXT; the six counters are dedicated INTEGER columns — uniform
  cross-driver semantics over dialect-native types.
- D-038: `memory_vectors` deferred to Phase 09 (embeddings are recomputable
  caches, not signals; keeps pgvector out of the foundation CI).

---

**Implementation note (Phase 03 delivery):** `memory_vectors` is intentionally
absent from migration 0001. See D-038. The RFC §8.1 table inventory is complete
for all other tables (21 tables shipped). pgvector and embedding storage land in
Phase 09 as a separate migration against the same driver seam.
