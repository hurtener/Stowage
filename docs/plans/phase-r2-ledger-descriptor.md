# Phase r2 — ledger descriptor self-description seam

- **Status:** shipped
- **Owning subsystem(s):** `internal/api`
- **RFC sections:** §9.5 (thin tiered surfaces), §8 (memory record schema)
- **Depends on phases:** 18 (memory management API — the list/get/mutate endpoints the descriptor names)
- **Informing briefs:** none — this is a cross-product integration endpoint. The contract is defined by the Pengui console's `internal/ledgerdescriptor` package (schema version "1"); see `docs/deploy-render.md`.

## Goal

Stowage advertises its own memory-ledger descriptor at
`GET /.well-known/pengui-memory-ledger`, so the Pengui console's memory-ledger
admin UI renders and mutates records from Stowage's **authoritative** shape
(source `well-known`) instead of a descriptor the console hardcodes for Stowage
(source `builtin:stowage`), which can drift from the real API.

## Brief findings incorporated

- None (no brief). The contract is the console's `ledgerdescriptor.Descriptor`
  (version "1") + its `Validate` rules, which the served descriptor must satisfy
  — an invalid descriptor makes the console report the capability unresolved (no
  fallback), which is worse than a 404.

## Findings I'm departing from

- The console's built-in `StowageDescriptor()` is a copy that omitted several real
  `memoryJSON` fields. Stowage's descriptor is authoritative and reflects the real
  record shape (16 fields), so the two may differ — by design (D-157).

## Design

A public (no-auth, like `/healthz`) HTTP handler serving a static JSON descriptor
built from the real memory API:

- **list** → `GET /v1/memories`, `items_field: "memories"`, `next_cursor_field: "next_cursor"`
- **get** → `GET /v1/memories/{id}`
- **fields** → `memoryJSON`'s keys (id, kind, content, context, status, importance,
  confidence, trust_source, stability, valid_from, valid_until, episode_id,
  supersedes_id, superseded_by_id, created_at, updated_at)
- **mutate_ops** → `confirm`/`reject` (`PATCH /v1/memories/{id}` with `{"action":…}`),
  `rollback` (`POST /v1/memories/{id}/rollback`). No delete (P1).

Public because the console probes it authenticated AND (on a mint failure)
unauthenticated; a public endpoint guarantees the real descriptor always resolves.
HTTP-only (a discovery document, not a tiered capability — D-067 applies to
capabilities).

## Files added or changed

```text
internal/api/ledger_descriptor.go                       # types, builder, handler
internal/api/ledger_descriptor_test.go                  # golden + self-validation + handler test
internal/api/testdata/ledger_descriptor.golden.json     # pinned bytes
internal/api/server.go                                  # register GET /.well-known/pengui-memory-ledger (public)
scripts/smoke/phase-r2.sh                               # descriptor smoke
docs/decisions.md                                       # D-157
docs/deploy-render.md, docs/glossary.md                 # notes
```

## Config keys added

None (always on — a static, harmless, non-sensitive document; D-034).

## Acceptance criteria (binding)

1. `GET /.well-known/pengui-memory-ledger` → 200, **public** (served without a bearer even in jwt mode). *(phase-r2 AC-1)*
2. The descriptor is well-formed and passes the console's `Validate` (version "1", id_field, list, ≥1 field). *(phase-r2 AC-2; verified live against the console's real `Validate()`)*
3. Declared mutate ops are exactly the real ones (`confirm`, `reject`, `rollback`); declared paths are routes the API serves. *(phase-r2 AC-3/4)*
4. Golden test pins the exact bytes; a self-validation test mirrors the console's rules so Stowage's CI catches contract drift.

## Smoke script

`scripts/smoke/phase-r2.sh` — boots serve in jwt mode, curls the descriptor
bearer-less (proving public), asserts shape + mutate ops + real routes.

## Test plan

- **Golden:** `TestLedgerDescriptorGolden` pins the JSON.
- **Contract self-guard:** `TestLedgerDescriptorValidates` mirrors the console's rules.
- **Handler:** `TestLedgerDescriptorHandler` — 200 + application/json + decodable.
- **Cross-repo (one-off, not committed):** decoded the live descriptor into the
  console's `ledgerdescriptor.Descriptor` and ran its real `Validate()` — passes.

## Risks & mitigations

- **Invalid descriptor → console reports capability unresolved (no fallback)** →
  golden + self-validation + live check against the console's real `Validate`.
- **Descriptor drifts from the real API** → the self-validation test asserts the
  declared paths are real routes; the golden forces a conscious update.

## Glossary additions

- **Ledger descriptor** — the `/.well-known/pengui-memory-ledger` self-description document.

## Decisions filed

- **D-157** — Advertise a ledger descriptor at `/.well-known/pengui-memory-ledger` (public self-description seam).
