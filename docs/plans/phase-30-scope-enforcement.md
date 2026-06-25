# Phase 30 — Read/write scope enforcement (multi-user-per-tenant isolation)

- **Status:** in-progress
- **Owning subsystem(s):** `internal/store` (records write path), `internal/api`, `internal/mcpserver`, `sdk/stowage`, `internal/retrieval` (read scope), `internal/identity`
- **RFC sections:** §1 P3 (line 102 — "Scopes enforced at write AND read"), line 364 ("Hard isolation at tenant AND user boundaries — enforced in the store layer"), §5.3 (grants/scopes), §8.1 (schema inventory — no new tables), identity model.
- **Depends on phases:** 15 (supersede/scopes), 16 (grants/EffectiveScopes), 18 (SDK), 17 (MCP).
- **Informing briefs:** [01](../research/01-predecessor-python.md), [02](../research/02-predecessor-ccmem.md) (scope/identity model inherited from the predecessors; Harbor's identity shape — tenant/project/user/session), per `docs/research/INDEX.md`.
- **Informing finding:** the 2026-06-25 read-side review + the episode-non-fire root-cause workflow, which together exposed BOTH halves of a P3 gap.

## Goal

Make Stowage actually enforce the RFC's **hard isolation at tenant AND user (and project/session) boundaries on BOTH write and read** for the multi-user-per-tenant deployment the RFC mandates. Two confirmed defects:

- **W (write):** `Records().Append` (both drivers) binds `project_id/user_id/session_id` from the batch `scope` argument, never from each `store.Record`. The `/v1/records` handler reads per-record fields but calls `Append` with a tenant-only scope, so every per-record project/user/session is silently dropped to NULL. Confirmed: all 2056 eval records have NULL session/project/user. This breaks every session-keyed feature (episodes never fire) and means the write path cannot carry per-user data at all.
- **R (read):** every retrieve surface (HTTP `retrieve_handler.go:34`, SDK `embedded.go`, MCP `ScopeFn`) builds a TENANT-ONLY scope; the retrieve request/SDK carry no project/user, so the lexical+vector lanes filter `tenant_id = ?` only and return ALL users' memories within a tenant.

The store layer is already capable (`buildScopeWhere` adds `user_id = ?`/`project_id = ?` when set) — the gap is purely that neither path supplies the scope.

## Goal (criteria summary)

P3 hard isolation real on both paths; additive + backward-compatible; no schema change; dual adversarial reviews.

## Brief findings incorporated

- Scope shape (tenant/project/user/session) matches Harbor's identity (brief 01/02 + D-of-record). Identity scoping is a store-layer concern (P3) — the fix stays in the store + thin surfaces, never handler-only filtering.

## Findings I'm departing from

- None — this phase implements what the RFC/identity decision already mandated but the code missed.

## Design

**Security model (decision):** the **tenant is the auth/trust boundary** (the API key carries `TenantID`). **project/user/session are caller-supplied per-request sub-scopes** — exactly as ingest already accepts per-record `project_id`/`user_id`. Stowage hard-isolates every store query to the supplied scope; the calling app (Harbor) supplies the correct end-user identity, the same trust model as ingest. This satisfies "hard isolation at tenant AND user" without per-user keys (a heavier key-issuance model, out of scope for v1).

- **W fix (scope-authoritative, record-fills-gaps):** in both `Append` impls bind `nullStr(firstNonEmpty(scope.X, rec.X))` for project/user/session. **Scope WINS when set** (a record can NEVER override a declared non-empty scope dimension → a write can't escape its authorized scope, P3); the record only fills a dimension the scope left empty. `scope.Tenant` stays authoritative + the fail-closed `scope.Tenant == ""` guard stays.
- **R fix (scope from request):** the retrieve request (HTTP/MCP) gains optional `project_id`/`user_id`/`session_id`; the handler builds `scope = {Tenant: authKey.TenantID, Project: req.ProjectID, User: req.UserID, Session: req.SessionID}`. The SDK gains a per-call scope on `RetrieveRequest`. `Retriever.Retrieve` already threads `scope` to the lanes via `buildScopeWhere` — minimal new logic, mostly handler/SDK plumbing + request fields.
- **Grants unchanged:** `resolveEffectiveScopes`/`EffectiveScopes` (Phase 16) still expands the caller scope to granted scopes for shared reads — now starting from the caller's real (project/user) scope instead of tenant-wide.
- **No schema change** (§8.1): the columns exist; we stop dropping them.

## Files added or changed

- `internal/store/sqlitestore/records.go`, `internal/store/pgstore/records.go` — Append scope-fill (+ a `firstNonEmpty` helper).
- `internal/store/conformance/conformance.go` — write-scope conformance: tenant-only batch with per-record session/project/user → scoped reads / `DistinctSessions` see them; a record CANNOT override a set scope dimension.
- `internal/api/retrieve_handler.go` (+ records_handler audit), `internal/mcpserver/handlers.go`+`contracts.go`, `sdk/stowage/types.go`+`embedded.go`+`http.go` — read-scope request fields + scope construction (parity across SDK/HTTP/MCP, D-067).
- `internal/identity` — a scope-merge/`firstNonEmpty` helper if shared.
- `scripts/smoke/phase-30.sh`.

## Config keys added

None (no new knob — D-034). Request/SDK fields only.

## Acceptance criteria (binding)

1. **Write:** a tenant-only-scoped `Append` of records carrying per-record `session_id`/`user_id`/`project_id` persists them (not NULL); a record CANNOT override a non-empty scope dimension (scope wins) — conformance, both drivers.
2. **Read isolation:** two users under one tenant; a retrieve scoped to user A returns ONLY A's memories (lexical AND vector lanes); A's query never surfaces B's memory. Integration test with real drivers (§17).
3. **Parity (D-067):** the read scope works identically across SDK/HTTP/MCP, proven by a parity test (MCP included).
4. **Episodes fire:** with the write fix, the eval (or a probe) builds episodes from per-record sessions.
5. **Grants intact:** an existing grant still widens reads to the granted scope (no regression in Phase-16 tests).
6. **Backward-compatible:** a caller that passes no project/user still works (tenant-wide read) — additive.
7. `make preflight` + `make coverage` + `drift-audit` + mirror green; `-race` clean.
8. **§17 — DUAL adversarial reviews** (two independent multi-agent reviews, as in the 29d wave) over the full diff, with extra check-ups; blocking findings fixed before merge.

## Smoke script

`scripts/smoke/phase-30.sh` asserts: Append binds per-record scope (grep the fill); retrieve handler/SDK/MCP read project/user/session; the conformance + read-isolation tests exist; build green.

## Test plan

- Store conformance: write-scope fill + scope-wins (both drivers).
- Read-isolation integration (real sqlite + pgstore): two-user no-leak across lexical+vector.
- SDK/HTTP/MCP parity test for the read scope.
- Regression: grants EffectiveScopes still widens; existing retrieve tests (tenant-only) unchanged.

## Risks & mitigations

- **Scope escape (record overrides scope):** mitigated by scope-wins-when-set; conformance asserts it.
- **Silent behavior change for existing tenant-only callers:** additive — empty project/user keeps tenant-wide reads; documented.
- **Grant interaction:** EffectiveScopes starts from the real scope now; covered by Phase-16 regression.
- **Cross-surface drift (D-067):** parity test gates it.

## Glossary additions

- "scope-authoritative write" — Append: a declared scope dimension wins; the record only fills empty dimensions.

## Decisions filed

- **D-124** — scope-authoritative record write (Append fills empty scope dims from the record; scope wins when set).
- **D-125** — read scope is caller-supplied per request (tenant = auth boundary; project/user/session via request/SDK), enforced in the store layer.
