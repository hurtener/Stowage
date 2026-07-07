# Phase ae13 — Topics resolve tenant-wide: sub-tenant scopes stop hiding tenant topics (D-154)

- **Status:** approved
- **Owning subsystem(s):** `internal/topics` (the scope-normalization fix); `internal/pipeline` / `internal/mcpserver` / `sdk/stowage` consumed (their existing calls are repaired by the service-level fix, no caller changes)
- **RFC sections:** §5 (identity/scopes), §6 (topics as extraction magnets), §9.5 (one logic core)
- **Depends on phases:** phase 7 (topics/extraction), ae7/ae8 (the jwt/per-user scopes that exposed the bug), ae9 (topic views — the existing per-subject curation mechanism that makes per-user *topic-config* unnecessary)
- **Informing briefs:** 03 (Engram — topics are magnets configured by the operator for the tenant's extraction, not per-end-user knobs), 06 (mempalace knob-paralysis — the fix adds zero knobs and zero new surfaces)

## Goal

When this phase is done, a tenant's configured topics steer **every** run's
extraction in that tenant — including per-user (jwt) and project-scoped runs —
and every topics *read* surface (HTTP, MCP `memory_topics`, embedded SDK)
shows the tenant's topics regardless of the caller's sub-tenant scope. Today
they don't: topics are **written tenant-only** (`topics.Service.Upsert` stores
`TenantID` alone — no user, no project), but **resolved under the caller's
full scope** — and `buildScopeWhere` adds `AND user_id = ?` / `AND project_id
= ?` whenever those scope dimensions are set. A per-user buffer flush
(`pipeline.go` builds `Scope{Tenant, Project, User}`) therefore matches zero
stored topics, `Resolve` sees "no expressed intent," and extraction falls back
to the profile's default pack — the operator's configured topics silently
never reach the per-user path. The same read-side mismatch hits MCP
`memory_topics` in jwt mode and the embedded SDK's scope-carrying reads.

## Brief findings incorporated

- **03 (Engram):** topics are the tenant/operator's extraction magnets. The
  per-user dimension belongs to the *memories* extraction produces, not to the
  topic *configuration* — exactly the split this fix restores.
- **06 (knob-paralysis):** no new knob, no new surface, no schema change. One
  normalization point in the service.

## Findings I'm departing from

- **The consumer ask's Option A scope (`{Tenant, Project}`) is corrected to
  tenant-only.** Upsert stores neither user *nor project*, so keeping the
  project predicate in topic resolution would reproduce the identical
  mismatch for project-scoped runs. Read normalization must match the write
  shape exactly: `{Tenant}` (D-154).
- **The consumer ask's Option B (per-user topic config) is rejected, with a
  pointer:** per-subject curation already exists at read time as ae9 topic
  VIEWS (D-149). Extraction topic sets are tenant-level; a real per-user
  *extraction*-topic need would supersede D-154 explicitly.

## Design

### The fix (`internal/topics/topics.go`)

One unexported helper, applied at **every** `topics.Service` entry point
(`Resolve`, `ActiveTopics` via Resolve, `Upsert`, `Delete`, and any other
method taking a scope):

```go
// topicScope projects a caller scope onto the topics table's own scope shape:
// tenant-only (D-154). Topics are tenant-level extraction curation — Upsert has
// only ever stored TenantID — so reads must match writes: a sub-tenant caller
// scope (user/project/session) must not hide tenant topics behind
// buildScopeWhere's dimension predicates. Tenant stays required (P3: the store
// still fails closed on an empty tenant).
func topicScope(scope identity.Scope) identity.Scope {
    return identity.Scope{Tenant: scope.Tenant}
}
```

- **No store/driver changes.** `buildScopeWhere` and both drivers are
  untouched — the store keeps enforcing scope exactly as before; the service
  simply passes the scope shape that matches what it writes.
- **No caller changes.** The pipeline extract stage, the MCP `memory_topics`
  handler, the HTTP handlers, and the embedded SDK keep passing their natural
  scopes; the service normalizes. (The HTTP handlers already pass tenant-only
  — for them this is a no-op.)
- **Default-pack fallback unchanged:** a tenant with zero configured topics
  still resolves the profile default pack; `pack:off` semantics (D-099/D-043)
  unchanged.
- Write-path normalization (`Upsert`/`Delete`) is behaviorally a no-op today
  (they already use only `scope.Tenant`) but is applied for symmetry so the
  invariant is visible at every entry point, not implicit in field selection.

### Why the service layer (not the extract stage)

Three call sites carry sub-tenant scopes today: the extract stage
(`extract.go` — the reported bug), MCP `memory_topics` (jwt mode carries the
verified user), and the embedded SDK (`embedded.go`). Fixing the service
repairs all three at once and makes drift structurally impossible for future
callers (D-067 one-logic-core lens).

## Files added or changed

```text
internal/topics/topics.go            # topicScope normalization at every entry
internal/topics/topics_test.go       # regression: sub-tenant scope resolves tenant topics
internal/pipeline/extract_test.go    # (or in-package equivalent) per-user flush uses tenant topics, not default pack
internal/mcpserver/*_test.go         # memory_topics with a user-carrying scope lists tenant topics
scripts/smoke/phase-ae13.sh          # smoke checks
docs/decisions.md                    # D-154
docs/glossary.md                     # topics-are-tenant-level note (amend existing entries if present)
docs/plans/ae-implementation-roadmap.md  # follow-up entry
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| *(none)* | — | D-034: nothing to tune; the fix restores intended semantics. |

## Acceptance criteria (binding)

1. **`Resolve` with a user-carrying scope returns the tenant's explicit
   topics** (regression unit test: upsert two topics tenant-only, resolve
   under `Scope{Tenant, User}` and `Scope{Tenant, Project, User}` → both
   return the two topics, NOT the default pack).
2. **A per-user buffer flush extracts under the tenant's topics**: pipeline-
   level test — records ingested with a user dimension, flush, the extract
   stage's resolved active set is the tenant's configured topics (assert via
   the existing extract-stage test seams; the tagged output contains the
   tenant topic key and no default-pack key).
3. **MCP `memory_topics` (list) under a jwt/user-carrying scope returns the
   tenant's topics** — handler test.
4. **Zero-config behavior unchanged**: a tenant with no stored topics still
   resolves the profile default pack (existing tests keep passing untouched);
   `pack:off` still suppresses packs.
5. **Tenant isolation unchanged**: topics of tenant A never resolve for
   tenant B (regression test with two tenants); empty-tenant scope still
   fails closed (P3).
6. **No store/driver/schema changes**: `buildScopeWhere`, both drivers, and
   the conformance suite are untouched by the diff.
7. `scripts/smoke/phase-ae13.sh` reports `OK ≥ 5`, `FAIL = 0`; prior phases'
   smoke scripts still pass.

## Smoke script

`scripts/smoke/phase-ae13.sh` — boots jwt-mode MCP-over-HTTP + the HTTP API
(reuse the ae11/ae12 smoke's minter pattern; note `stowage serve` co-mounts
both surfaces, or boot them separately):

- PUT two explicit topics via HTTP `/v1/topics` (tenant admin key)
- jwt-scoped MCP `memory_topics` list → shows the two topics (not
  `pack:preferences` keys)
- ingest a preference-bearing record per-user via jwt `memory_ingest_run` (or
  `memory_ingest`) + flush → with the mock gateway, assert via sqlite that the
  buffer flushed under the tenant topic set (as observable — e.g. the
  extraction event/log or the memories' topic tags if the mock emits them;
  pick the strongest hermetic assertion available)
- keyring/tenant path regression: tenant-scope resolve still returns the
  topics (the previously-working path stays working)
- zero-config tenant still resolves the default pack

## Test plan

- **Unit (topics):** the criterion-1 regression matrix (user / project+user /
  session-carrying scopes), two-tenant isolation, empty-tenant fail-closed,
  default-pack fallback + `pack:off` untouched (existing tests).
- **Pipeline:** criterion-2 flush test following the existing extract-stage
  test patterns (mock gateway).
- **MCP:** criterion-3 handler test.
- **No new fuzz/bench** (no new parse surface, no hot path change).
- **Coverage:** `internal/topics` stays ≥ its band.

## Risks & mitigations

- **Hidden reliance on sub-tenant topic narrowing:** enumerated every
  `Resolve`/`ActiveTopics`/`Upsert`/`Delete` caller (extract stage, HTTP
  handlers, MCP handler, embedded SDK, service-internal) — none writes or
  expects user/project-scoped topics; the store schema's unused sub-tenant
  columns stay unused. A future per-user-topics feature supersedes D-154
  explicitly (and changes write+read together).
- **Behavioral surprise for keyring/tenant callers:** none — their scope was
  already tenant-only; the fix is a no-op for them (criterion 5's regression
  proves it).

## Glossary additions

- Amend the topics/pack entries (if present) with one line: topic
  configuration is **tenant-level** — resolution normalizes any caller scope
  to tenant-only so sub-tenant scopes cannot hide tenant topics (D-154);
  per-subject read-time curation is ae9 topic views, not topic config.

## Decisions filed

- D-154: topics are tenant-level curation — resolution and admin normalize to
  tenant-only scope; per-user/per-project extraction-topic curation requires
  an explicit superseding decision.
