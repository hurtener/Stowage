# Phase 05 — Records, ingest API & branches

- **Status:** draft
- **Owning subsystem(s):** `internal/records`, `internal/api`, `cmd/stowage`
  (`serve`)
- **RFC sections:** §4.1 (write path), §5.1 (records), §5.5 (branches), §9.1
  (API surface), §2.1 P1/P2/P3
- **Depends on phases:** 02, 03
- **Informing briefs:** 04 (fidelity layer — verbatim, immutable), 03
  (fire-and-forget ergonomics), 01 (ingest ACK coupled to request-time work —
  the anti-pattern)

## Goal

`stowage serve` exists: authenticated, scope-enforced HTTP ingest that ACKs
after the durable verbatim append and nothing else (P2); branch lifecycle;
runtime admin key endpoints; health/metrics. After this phase an agent can
store interaction history durably at p99 < 15 ms.

## Brief findings incorporated

- Brief 01: the predecessor coupled classification work into the ingest
  request → here the handler does validate → stamp → append → enqueue → ACK,
  nothing heavier (P2 rule, CLAUDE.md §6).
- Brief 04: records immutable, never summarized at write time.

## Findings I'm departing from

- Branch `merge` in this phase only transitions lifecycle state and re-tags
  scope visibility of branch records; reconciliation of branch *memories*
  into the parent arrives with Phase 08 (memories don't exist yet). Noted so
  the API contract is stable from day one.

## Design

### `internal/records`

`Record` domain type mirroring the store row; `New(scope, input)` stamps
ULID, `created_at`, validates roles/content, computes `token_estimate`
(len/4 heuristic — replaced by a real estimator only if eval shows it
matters); `outcome` optional enum `success|failure|neutral` + free-form
detail.

### `internal/api`

- Go 1.22+ `http.ServeMux` patterns; **no router dependency**.
- Middleware chain: recovery (no panic across the boundary — 500 + slog),
  request logging (identity-stamped), body limit (1 MiB default), timeouts
  from config, auth, metrics.
- **Auth middleware:** `Authorization: Bearer sk_...` → `auth.Verify` against
  the store keyring; the key's tenant becomes the request tenant — a payload
  scope with a different tenant is a 403 (scope forgery is structurally
  impossible; P3). Admin endpoints require `RoleAdmin`.
- Endpoints (this phase):
  - `POST /v1/records` — single or batch; per-item scope (tenant from key);
    durable append; non-blocking enqueue to the pipeline channel; `202
    {ids, enqueued}`. Oversized batch → 413; malformed item → 400 with index.
  - `POST /v1/branches` — `{action: fork|merge|discard, session_id,
    branch_id?, parent_branch_id?}` per §5.5.
  - `POST/GET/DELETE /v1/admin/keys` (+ `POST /v1/admin/keys/{id}/revoke`,
    bulk revoke by tenant) — runtime key management (D-030); create returns
    plaintext once.
  - `GET /healthz` (process), `GET /readyz` (store ping + migrations
    current), `GET /metrics` (Prometheus).
- **Pipeline stub:** a bounded channel (`cap` from profile) + a drainer
  goroutine that currently no-ops (Phase 06 replaces it with the buffer
  stage). Enqueue is non-blocking: if full, drop the enqueue (NOT the record
  — it is already durable; the future re-enqueue sweep recovers) and count a
  metric.
- HTTP hardening explicit per CLAUDE.md §7: Read/Write/Idle timeouts,
  MaxHeaderBytes, Content-Type enforcement on POSTs.

### `cmd/stowage serve`

config.Load → telemetry.New → store.Open (+ auto-migrate per config) →
api.New → graceful shutdown (SIGTERM: stop accepting, drain pipeline channel,
store.Close). `--config` flag mirrors `migrate`.

### DSAR stubs

`DELETE /v1/admin/users/{user}` returns 501 with a documented contract
comment; the store cascade lands with the retention work (Phase 21 security
pass). Listed here so the surface is reserved.

## Files added or changed

```text
internal/records/{records.go, records_test.go}
internal/api/{server.go, middleware.go, auth.go, records_handler.go,
              branches_handler.go, keys_handler.go, health.go, api_test.go}
cmd/stowage/main.go            (serve subcommand)
scripts/coverage.json          (records 85, api 80)
scripts/smoke/phase-05.sh
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `server.read_timeout` / `write_timeout` / `idle_timeout` | 10s/20s/60s | explicit, never SDK defaults |
| `server.max_body_bytes` | 1 MiB | |
| (profile-internal) pipeline channel cap | 4096 | not a top-level knob |

## Acceptance criteria (binding)

1. Ingest ACK p99 < 15 ms on sqlite (benchmark, local reference).
2. ACK independence: with the pipeline channel full/stalled, ingest still
   ACKs 202 (test) — P2.
3. No update/delete route for records exists; immutability proven in store
   conformance (Phase 03) and reasserted by an API 405 test.
4. Cross-tenant forgery rejected: key of tenant A + payload scope tenant B →
   403 (test).
5. Branch fork/discard/merge lifecycle: discard leaves records readable,
   branch state `discarded`; merge transitions state and re-tags visibility.
6. Key lifecycle via API: create (plaintext once), rotate, revoke, bulk
   revoke — effective on the next request, no restart (test drives a live
   server).
7. `readyz` reflects store reachability; recovery middleware converts a
   handler panic into 500 without process exit (test).
8. Coverage ≥ 85 records / ≥ 80 api; all `-race`.

## Smoke script

phase-05.sh: start `stowage serve` on a random port with temp sqlite; curl:
healthz, admin key create, ingest single + batch, branch fork/discard,
forged-tenant 403; clean shutdown on SIGTERM.

## Test plan

httptest-based handler tests; live-server smoke; `BenchmarkIngestACK`;
fuzz target on the records payload decoder; race tests on concurrent ingest
+ key revocation.

## Risks & mitigations

- Token-estimate heuristic too crude → isolated in one function; revisit with
  eval data only.
- Pipeline-full drops derivation silently → metric + the Phase 14 re-enqueue
  sweep is the designed recovery; documented in code.

## Glossary additions

None.

## Decisions filed

- D-041: stdlib `http.ServeMux` only — no router dependency; the API surface
  is small by design (D-015) and stdlib patterns suffice. (Original provisional
  number D-040 was already taken by the bifrost wire-format decision in Phase 04;
  re-filed as D-041 in the same PR.)
