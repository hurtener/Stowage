# Phase ae9 — per-agent / per-key topic views (read-time curation)

- **Status:** implemented (Wave 3, feat/ae-wave-3). See "As-built deviations" below.
- **Owning subsystem(s):** `internal/retrieval` (view resolution + apply, reusing ae6's `filterByTopicOwnScope`); `internal/store` (extends ae1's `TopicViewStore` seam + both drivers with admin CRUD + conformance — **no** new table, **no** new migration; ae9 reads/writes ae1's `topic_views` junction table); `internal/identity` (subject resolution helper); the three retrieve surfaces (`internal/mcpserver`, `internal/api`, `sdk/stowage`) for apply-a-view; a new **`internal/views` core service** (`views.Service`, mirroring `grants.Service`) that owns admin CRUD + governance-event emission once, with `internal/mcpserver` + `internal/api` as thin callers for view admin; `internal/config` (the `agent_views` knob group).
- **RFC sections:** §5.3 (topic-keyed slicing, `memory_topics` reuse), §6 (retrieval, curation), §9.5 (one logic core, D-067/D-073)
- **Depends on phases:** **ae1** (the read-time optional `identity.Scope.Agent` field populated from `_meta`, the Dockyard v1.8 `server.RequestMeta(ctx)` plumbing, and the `(tenant_id, agent_id) → allow/deny topic-key` policy-binding store+table that ae9 generalizes) and **ae6** (the own-scope, fail-**open**, lane-aware `filterByTopicOwnScope` + its `topic_filter_scoring_k` candidate-window remedy — the single filter ae9 reuses). Both are prerequisites; ae9 builds no filter and no `_meta` plumbing of its own.
- **Informing briefs:** 03 (Engram — topics as extraction magnets; the `memory→topic` association ae9 slices on, D-089), 06 (mempalace — gateway-free retrieval; the view filter is a store membership read, never a gateway call, so it works in the D-036 degraded path and **fails open**), 02 (CC-memory predecessor — surface-sprawl cautionary tale → one view-resolution core, thin `{SDK,HTTP,MCP}` callers, not one lens per surface).

> **Checkpoint reconciliation (D-151).** Per **D-151**, ae9 generalizes ae1's
> **`topic_views`** junction table (`topic_key`, `effect CHECK(effect IN ('allow',
> 'deny'))` — one row per key, created by ae1 at migration **0013**) with
> named-view semantics, the key-id subject, and the admin surface. ae9 adds
> **no** new table, **no** new migration, **no** new seam, and **no** new enable
> knob — it reuses ae1's **`TopicViewStore`** seam (`Store.TopicViews()`) and
> **`retrieval.agent_views.enabled`** knob, adding only
> `retrieval.agent_views.on_policy_error` and
> `retrieval.agent_views.subject_precedence`.

## Goal

When this phase is done a host can bind **named, switchable topic views** to a
read subject — an `agent_id` (from `_meta`, via ae1) or, when no agent identity
is present, the **verified credential's key id** — and select one at read time to
narrow *which of the caller's own-scope topic-tagged memories surface*. A view is
`(tenant_id, subject_kind, subject_id, view_name) → {allow_topics, deny_topics}`;
applying it resolves the subject from the request's identity, looks up the named
view, and runs ae6's existing own-scope `filterByTopicOwnScope` with the view's
allow/deny keys. Admins create/update/delete/list views on `{HTTP, MCP}`; callers
apply a view by `view_name` on `{SDK, HTTP, MCP}`. A view can only **subtract**
from the caller's own scope — it is a curation lens, **not** an isolation
boundary (D-139); the tenant from the verified credential/JWT remains the *only*
P3 boundary. The whole path **fails open** (a views-store error returns the
caller's full own-scope results, flagged degraded) and touches **no** scope
table and **no** agent column — ae9 adds **no** new table or migration (D-151);
it reuses ae1's `topic_views` junction table.

## Brief findings incorporated

- **03 (Engram, D-089):** a view slices on the existing `memory_topics` junction
  via the scope-required `MemoriesTopics` batch reader — ae9 invents no tagging
  scheme and no new junction; a "view" is just a stored allow/deny set of the
  same topic keys the extractor already writes.
- **06 (mempalace, D-036):** retrieval must serve gateway-free; view resolution
  and the filter are pure store reads with no gateway call, so a view works in the
  degraded path and its *own* failure mode is **fail-open** (return the caller's
  own memories, mark degraded), never a hard error.
- **02 (CC-memory):** the predecessor's surface sprawl is the anti-pattern — the
  view-resolution + apply logic is one core function reused by all three retrieve
  surfaces, and the admin CRUD validation lives in one place (the store seam), so
  no surface can define its own view semantics.

## Findings I'm departing from

- **The charter frames ae9 as a clean generalization of "ae1's single binding";
  code truth is that ae1 (and ae6) are unbuilt.** `docs/plans/` contains only
  `phase-ae3` and `phase-ae6` (a draft); there is no `phase-ae1-*` plan and no
  ae1/ae6 code — `identity.Scope` has only `{Tenant,Project,User,Session}`
  (`internal/identity/identity.go:29`), `filterByTopicOwnScope` does not exist
  (only grants' fail-*closed* `filterByTopic`, `internal/retrieval/grants.go:68`),
  and no policy-binding table exists (highest migration is `0012`). This plan is
  therefore authored against ae1/ae6's **specified seams**, and ae9 cannot merge
  until both have landed. Recorded in **D-149**.
- **Superseded by D-151.** This plan originally proposed that ae9 own a new,
  generalized `topic_views` table, with ae1's narrower binding rows carried
  over into it by a one-time, one-way data-migration step at merge time, to
  avoid a cross-driver column rename/reshape on SQLite. The checkpoint
  decision **D-151** resolved this the other way: ae1 creates `topic_views`
  directly in its general **junction** shape (`subject_kind` defaulting
  `'agent'`, `subject_id`, `view_name` defaulting `'default'`, `topic_key`,
  `effect` — one row per key) at migration `0013`, so there is no narrower ae1
  table to reshape or carry rows over from. ae9 therefore adds **no** table,
  **no** migration, and **no** data-carry-over step — it adds only named-view
  semantics, the key-id subject, and the admin surface on top of ae1's table.
  The table-ownership question this bullet originally raised is closed by
  D-151, not by ae9's migration.
- **The charter's "key a view on the verified key id" is free on HTTP but
  requires new MCP plumbing it does not call out.** On HTTP the full `*auth.Key`
  (with `ID`) is on the request context (`internal/api/auth.go:48`,
  `keyFromContext` at `:58`). On MCP, `KeyringMiddleware`
  (`internal/mcpserver/server.go:229`) injects **only** `Scope{Tenant}` at `:246`
  and discards `key.ID`; `CtxScopeFn` returns only the `Scope`. So the key-id
  subject works out-of-the-box only on HTTP — **MCP needs ae9 to stash `key.ID`
  into the request context and expose it to the retrieve handler.** This concrete
  same-PR parity cost is called out here and in **D-149**.
- **Superseded by D-151.** This plan originally proposed storing `allow_topics`/
  `deny_topics` as JSON-encoded `TEXT` array columns on a single-row-per-subject
  table. **D-151** instead pins ae1's `topic_views` as a **junction** table — one
  row per `(subject, view, topic_key)` with an `effect CHECK(effect IN ('allow',
  'deny'))` column, mirroring the `memory_topics` junction idiom (D-089). ae9's
  `TopicView` domain struct still exposes `AllowTopics []string`/`DenyTopics
  []string`, but the store driver **aggregates** them from `effect='allow'`/
  `'deny'` rows at read time and **writes** them as one row per key on
  create/update — never as a JSON blob. This keeps ae9 clear of the forbidden
  "free-text JSON parse of model output" (§10) question entirely: there is no
  JSON column to reason about.

## Design

### The durable surface: `topic_views` (ae1's junction table — no new table, D-151)

ae1's sub-store on the `Store` seam (`internal/store/store.go:16`), mirroring the
`Grants()`/`Topics()` accessor pattern; ae9 extends its interface with admin CRUD
(`CreateView`/`UpdateView`/`DeleteView`/`ListViews`) but adds no new accessor and
no new table:

```go
// TopicViews returns the read-time topic-view sub-store (Phase ae1, generalized
// by ae9 — D-151). A topic view is a named, per-subject curation lens — NOT a
// scope table and it carries no memory rows. Scope-enforced (P3): every method
// requires scope.Tenant.
TopicViews() TopicViewStore
```

```go
// TopicView is a named read-time curation lens bound to a subject, resolved
// (aggregated) from ae1's topic_views junction table — one row per topic key —
// never stored as a JSON blob (D-151). It generalizes
// ae1's (tenant_id, agent_id)→allow/deny binding: ae1's row is exactly
// (SubjectKind="agent", ViewName="default") with a single-key row.
type TopicView struct {
    ID          string   // ULID (domain-level view identity; not a single junction row)
    TenantID    string   // == scope.Tenant (the only P3 boundary)
    SubjectKind string   // "agent" | "key"
    SubjectID   string   // agent_id (ae1, from _meta) OR auth.Key.ID
    ViewName    string   // "default" when unnamed (== ae1's binding)
    AllowTopics []string // aggregated from junction rows with effect='allow' (empty = no include constraint)
    DenyTopics  []string // aggregated from junction rows with effect='deny'
    CreatedAt   int64
    UpdatedAt   int64
}

type TopicViewStore interface {
    // CreateView inserts a view. ErrConflict on a duplicate
    // (tenant_id, subject_kind, subject_id, view_name). Scope-required.
    CreateView(ctx context.Context, scope identity.Scope, v TopicView) error
    // UpdateView replaces AllowTopics/DenyTopics on an existing view (by the
    // natural key). ErrNotFound when absent. Scope-required.
    UpdateView(ctx context.Context, scope identity.Scope, v TopicView) error
    // DeleteView removes a view by natural key. ErrNotFound when absent.
    DeleteView(ctx context.Context, scope identity.Scope, subjectKind, subjectID, viewName string) error
    // ListViews returns all views for the tenant, optionally narrowed to a
    // subject, ordered by created_at ASC. Scope-required.
    ListViews(ctx context.Context, scope identity.Scope, subjectKind, subjectID string) ([]TopicView, error)
    // GetView resolves one view by natural key. ErrNotFound when absent.
    // This is the read-path resolver the retriever calls.
    GetView(ctx context.Context, scope identity.Scope, subjectKind, subjectID, viewName string) (*TopicView, error)
}
```

Every method **requires `scope.Tenant`** and returns `ErrScopeRequired` when it
is empty — identical to `MemoriesTopics` (`internal/store/pgstore/memories.go:402`).
There is no unscoped variant (P3). `(TopicView).Validate()` in `store/types.go`
normalizes the natural key (`subject_kind ∈ {agent,key}`, non-empty `subject_id`,
`view_name` defaults `"default"`) and is called by both drivers' `CreateView`/
`UpdateView` — validation lives in the core so no surface can bypass it (D-067).

**No migration.** Per **D-151**, ae9 adds **no** new migration — it reads and
writes ae1's existing `topic_views` junction table, created once at migration
`0013` (both dialects). ae1's shape (reproduced here for reference; owned and
migrated by ae1, not ae9), mirroring the `memory_topics` junction idiom
(D-089):

```sql
CREATE TABLE IF NOT EXISTS topic_views (
    id           TEXT NOT NULL PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    subject_kind TEXT NOT NULL DEFAULT 'agent',   -- 'agent' | 'key'
    subject_id   TEXT NOT NULL,
    view_name    TEXT NOT NULL DEFAULT 'default', -- 'default' == ae1's binding
    topic_key    TEXT NOT NULL,
    effect       TEXT NOT NULL CHECK(effect IN ('allow','deny')),
    created_at   BIGINT NOT NULL,         -- INTEGER in sqlite
    updated_at   BIGINT NOT NULL,
    UNIQUE (tenant_id, subject_kind, subject_id, view_name, topic_key)
);
CREATE INDEX IF NOT EXISTS idx_topic_views_subject
    ON topic_views(tenant_id, subject_kind, subject_id, view_name);
```

One row per `(subject, view, topic_key)` — a view's `AllowTopics`/`DenyTopics`
are the set of `topic_key`s whose row has `effect='allow'` / `'deny'` for that
`(tenant_id, subject_kind, subject_id, view_name)`. ae9's driver methods
read/write these rows directly: `GetView`/`ListViews` aggregate rows into
`TopicView.AllowTopics`/`DenyTopics`; `CreateView`/`UpdateView` write one row
per key (inserting new keys and deleting rows for keys no longer present, so a
view's row set always matches its current allow/deny lists); `DeleteView`
deletes all rows for the natural key. No JSON-encoded column anywhere on this
table.

No FK to any memory row (a view references topic *keys*, not memories), no agent
column on any of the 12 scope tables, no dedupe-index / DSAR cascade /
buffer→flush threading — the day-one-signal disciplines that bind persisted
dimensions do **not** apply, because a view persists no memory data.

### Subject resolution (identity → view subject)

A read subject is `(kind, id)` resolved from the request's identity, honouring
the `subject_precedence` knob (default `agent,key`):

1. If `scope.Agent != ""` (ae1, from `_meta.agent_id`) → `("agent", scope.Agent)`.
2. Else if a **verified credential key id** is present → `("key", keyID)`.
3. Else **no subject** → no view → the caller's own-scope results pass through
   unfiltered (an *unbound* caller, not an error).

`subject_precedence=key,agent` flips 1↔2. Precedence only matters when *both* an
agent id and a key id are present; "agent precedence wins when both exist" is the
default (charter AC). The subject is **always identity-derived and never a wire
argument** — a caller can only ever apply *its own* agent's / key's views, so a
view can never widen scope or read another subject's lens (P3-honest).

`keyID` reaches the core through a new **internal** field on `retrieval.Request`,
populated server-side from the *verified* credential — it is **not** a JSON wire
field on any input contract (a client must never be able to spoof its key id):

```go
// retrieval.Request additions (internal/retrieval/retrieval.go):
ViewName        string // named topic view to apply (empty ⇒ "default"); wire field
CredentialKeyID string // verified key id for the "key" view subject; server-injected, NEVER a wire field
```

- **HTTP** (`internal/api/retrieve_handler.go`): the handler reads `key.ID` via
  `keyFromContext(ctx)` and sets `CredentialKeyID`. Free — the key is already on
  the context.
- **MCP** (`internal/mcpserver`): **new plumbing.** `KeyringMiddleware`
  (`server.go:229`) stashes the verified `key.ID` on the request context beside
  the scope; a `KeyIDFromContext(ctx) string` accessor exposes it; the retrieve
  handler reads it and sets `CredentialKeyID`. In stdio mode (no per-request key)
  it is empty → key views simply don't resolve (agent views still do).
- **SDK** (`sdk/stowage`): embedded/in-process mode has no HTTP key; the agent
  subject comes from `scope.Agent`, and the key subject is unused (in-process
  callers are already trusted and scope-bounded). HTTP-mode SDK rides the same
  server-side injection as any HTTP caller.

### Apply-a-view in `Retrieve` (reuses ae6's filter and seam)

View application slots into the **exact seam ae6 pins** — after RRF fusion, before
the `scoringK` trim (`retrieval.go:504`→`508`) — running over the laneK-wide fused
pool so a curated view does not underfill (the H3 remedy is inherited verbatim
from ae6; ae9 widens nothing new). New `internal/retrieval/views.go`:

```go
// resolveAndApplyView narrows the caller's OWN-scope candidate IDs through the
// named view bound to the request's subject. It reuses ae6's filterByTopicOwnScope
// (built once, in ae6) with the view's allow/deny keys. FAILS OPEN (D-139): a
// views-store error returns the input IDs unchanged with degraded=true — the
// deliberate opposite of grants' fail-closed filterByTopic.
func (r *Retriever) resolveAndApplyView(
    ctx context.Context, scope identity.Scope, req Request, ids []string,
) (kept []string, degraded bool)
```

Flow:
1. If `agent_views.enabled=false` (default) or the retriever has no `TopicViewStore`
   wired → return `(ids, false)` (no-op; zero-config off).
2. Resolve the subject (above). No subject → `(ids, false)` (unbound pass-through).
3. `view, err := r.viewSt.GetView(ctx, scope, kind, id, viewName)`:
   - `err == ErrNotFound` → **unbound** for this view name → `(ids, false)`
     (an unbound agent sees unfiltered own-scope results — charter AC).
   - other `err` → **fail-open**: log a warning, return `(ids, true)` unless
     `agent_views.on_policy_error=closed`, in which case return `(nil, true)`
     (documented operator override; see the knob note — `open` is the D-139-aligned
     default and the only value that keeps a view a curation lens).
4. `kept, deg := r.filterByTopicOwnScope(ctx, scope, ids, view.AllowTopics, view.DenyTopics)`
   → return `(kept, deg)`.

This runs **after** ae6's own request-level `include_topics/exclude_topics` filter
(if any); both only subtract, so composition order is irrelevant and the result is
their intersection. `resp.DegradedView` is set from the returned `degraded`
independently of `resp.DegradedTopicFilter`. A new `Response.DegradedView bool`
mirrors ae6's `DegradedTopicFilter` (fail-open transparency), surfaced on all
three output contracts.

The retriever gains a `viewSt store.TopicViewStore` field + `SetTopicViews(...)`
wiring method (same shape as `SetGrants`, `grants.go:26`); nil ⇒ feature off.

### View admin ({HTTP, MCP} — the team/view-admin tier)

Admin CRUD lives **once** in a new core service `internal/views` —
`views.Service` (constructed `views.New(store.TopicViews(), store.Events(), log)`),
directly mirroring `grants.Service` (`internal/grants/grants.go`). It exposes
`CreateView`/`UpdateView`/`DeleteView`/`ListView(s)`, calls `(TopicView).Validate()`
on the write path, and — inside those methods, **not** in any handler — emits the
governance audit event via the events store through its own private `emitEvent`
helper (the exact shape of `grants.Service.emitEvent`,
`internal/grants/grants.go:419`, itself called inside `CreateGrant`/`RevokeGrant`).
This is the D-067/D-073 discipline: the side effect (the audit event) lives in the
core so **no surface can omit it** and the two admin surfaces cannot diverge. The
raw `TopicViewStore` is a persistence seam the core service owns; handlers never
touch it directly and never emit events themselves. Both handlers resolve the
tenant from the verified credential (`tenantScope := identity.Scope{Tenant:
scope.Tenant}`) — an admin can only manage views in their own tenant.

- **MCP** (`internal/mcpserver`): a new action-dispatch tool `memory_views`
  mirroring `makeGrantsHandler` (`handlers.go:808`) with actions
  `create_view` / `update_view` / `delete_view` / `list_views`, each a thin call
  into `svc.ViewsSvc.*` (the same `svc.GrantsSvc.*` pattern at `handlers.go:824+`);
  contracts `ViewsInput`/`ViewsOutput` in `contracts.go`; schema goldens
  regenerated.
- **HTTP** (`internal/api`): a new `views_handler.go` mirroring
  `grants_handler.go`, whose handlers call `s.viewsSvc.*` (the `s.grantsSvc.*`
  pattern), wired via a `SetViewsService(*views.Service)` method beside
  `SetGrantsService` (`internal/api/server.go:252`). Routes
  `POST/PUT/DELETE/GET /v1/scopes/views` are registered in the same
  `mux.HandleFunc` block as the grants routes (`server.go:194-196`), guarded by
  the admin-role check and a `requireViewsSvc` 503 guard (mirroring
  `requireGrantsSvc`, `grants_handler.go:102`) when the feature is disabled.

Because both surfaces funnel through `views.Service`, a parity test asserts that
`create_view` (and each mutating op) emits the **same** governance audit event
regardless of surface — the event is produced by the core exactly once per
mutation, never by a handler.

Admin is **not** on the SDK (matches the tier: apply-a-view is
`{SDK,HTTP,MCP}`; view admin is `{HTTP,MCP}`).

### Concurrency & purity

`resolveAndApplyView` reads only `ctx`/args and the immutable `viewSt` handle — no
receiver mutation; safe under concurrent `Retrieve` (proven under `-race`, §5).
The `TopicViewStore` drivers are safe for concurrent use like every other
sub-store.

## Files added or changed

```text
internal/store/store.go                       # CHANGED — TopicViewStore interface + TopicViews() accessor
internal/store/types.go                       # CHANGED — TopicView struct + (TopicView).Validate()
internal/store/pgstore/topicviews.go          # CHANGED (ae1 NEW) — adds admin CRUD over the existing junction rows (scope-required)
internal/store/sqlitestore/topicviews.go      # CHANGED (ae1 NEW) — mirror
internal/store/conformance/conformance.go     # CHANGED — view round-trip + scope-required + cross-tenant leak guard
internal/boot/boot.go                          # CHANGED — construct views.New(Store.TopicViews(), Store.Events(), Log) → stk.ViewsSvc (beside GrantsSvc, boot.go:218)
cmd/stowage/main.go                            # CHANGED — inject stk.ViewsSvc into the MCP Services + srv.SetViewsService (beside GrantsSvc wiring)
internal/views/views.go                       # NEW — views.Service (admin CRUD + Validate + governance emitEvent), mirroring grants.Service
internal/views/views_test.go                  # NEW — core CRUD + emit-once event unit tests
internal/retrieval/views.go                   # NEW — resolveAndApplyView (fail-open), SetTopicViews, subject resolution
internal/retrieval/views_test.go              # NEW — resolve/apply/unbound/precedence/fail-open unit tests
internal/retrieval/retrieval.go               # CHANGED — Request{ViewName,CredentialKeyID}; Response{DegradedView}; wire resolveAndApplyView at the RRF→scoringK seam; viewSt field
internal/identity/identity.go                 # CHANGED — subject-resolution helper (Scope.Agent already added by ae1)
internal/config/config.go                     # CHANGED — RetrievalConfig.AgentViews{Enabled,OnPolicyError,SubjectPrecedence}; allKeys; defaults; get/set/explain; validate
internal/config/config_test.go                # CHANGED — knob default/override tests
internal/mcpserver/server.go                  # CHANGED — KeyringMiddleware stashes key.ID; KeyIDFromContext accessor; Services.ViewsSvc (*views.Service) wiring
internal/mcpserver/handlers.go                # CHANGED — retrieve handler: view_name + inject CredentialKeyID; NEW makeViewsHandler (admin, thin caller of svc.ViewsSvc.*)
internal/mcpserver/contracts.go               # CHANGED — RetrieveInput.view_name; RetrieveOutput.degraded_view; ViewsInput/ViewsOutput
internal/mcpserver/testdata/*.schema.json     # CHANGED — regen retrieve + new views schemas
internal/api/retrieve_handler.go              # CHANGED — retrieveRequest.view_name; retrieveResponse.degraded_view; inject CredentialKeyID via keyFromContext
internal/api/views_handler.go                 # NEW — HTTP view admin (create/update/delete/list) on /v1/scopes/views (thin callers of s.viewsSvc.*; requireViewsSvc 503 guard)
internal/api/server.go                        # CHANGED — register /v1/scopes/views routes in the grants mux.HandleFunc block (server.go:194-196); add viewsSvc field + SetViewsService (beside SetGrantsService)
sdk/stowage/types.go                          # CHANGED — RetrieveRequest.ViewName; RetrieveResponse.DegradedView (apply-a-view only; no admin)
sdk/stowage/embedded.go                       # CHANGED — thread ViewName (in-process call site)
sdk/stowage/http.go                           # CHANGED — retrieve rides JSON tags (view_name/degraded_view)
scripts/coverage.json                         # CHANGED — add internal/views threshold (80%, new package)
scripts/smoke/phase-ae9.sh                    # NEW
test/integration/topicviews_test.go           # NEW — real-driver round-trip + apply + fail-open + cross-scope (§17)
docs/decisions.md                             # CHANGED — D-149 (append; returned as delta, not edited here)
docs/glossary.md                              # CHANGED — topic view, view subject, subject precedence (returned as delta)
docs/plans/README.md                          # CHANGED — ae9 row status draft (returned as delta)
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `retrieval.agent_views.enabled` | `false` | Master switch. **Off by default ⇒ zero-config start is byte-identical to today** (no view is ever resolved or applied). D-034-complete: default, present in every profile's effective config, docs, `allKeys`/get/set/explain, validation, smoke. |
| `retrieval.agent_views.on_policy_error` | `open` | Fail posture on a **views-store error** (not on "view not found", which is always an unbound pass-through). `open` (default) returns the caller's full own-scope results with `DegradedView=true` — the D-139-aligned value and the *only* one that keeps a view a curation lens. `closed` drops results on error (defense-in-depth override; documented as re-tiering the view toward isolation — but the tenant boundary is unaffected either way). Enum `{open,closed}`, validated. |
| `retrieval.agent_views.subject_precedence` | `agent,key` | Order in which the subject is resolved when both an agent id and a key id are present. `agent,key` (default) ⇒ agent wins; `key,agent` flips it. Validated against the two permutations. |

All three are flat scalars under an `agent_views` sub-struct on `RetrievalConfig`
(sibling to `include_superseded`, `internal/config/config.go:63`). Inert when
`enabled=false`, so the zero-config invariant is preserved and smoke-tested.

## Acceptance criteria (binding)

1. **A bound agent view narrows own-scope retrieval.** With `enabled=true` and a
   view `(agent, <id>, "default", allow=[k1])` present, a retrieve carrying
   `scope.Agent=<id>` returns only that agent's own-scope memories tagged `k1`
   (via `MemoriesTopics`); an integration test proves it on both drivers.
2. **An unbound agent sees unfiltered own-scope results.** A retrieve with an
   `agent_id` that has no matching view (`GetView`→`ErrNotFound`) returns the
   caller's full own-scope results — no filter, no error, `DegradedView=false`.
3. **Key-id fallback + agent precedence.** When `_meta` carries no `agent_id`, a
   view bound to `(key, <keyID>, …)` applies using the **verified** key id
   (HTTP via `keyFromContext`; MCP via the new `KeyIDFromContext` plumbing). When
   both an agent id and a key id are present, `subject_precedence=agent,key`
   resolves the **agent** view (tested for both precedence values).
4. **Fail-OPEN (D-139), fault-injected.** A views-store **error** returns the
   caller's full own-scope results with `DegradedView=true` (default
   `on_policy_error=open`) — proven by fault injection — deliberately the opposite
   of grants' fail-closed `filterByTopic`. ae9 defines **no** new filter: it calls
   ae6's `filterByTopicOwnScope` (grep asserts ae9 does not reintroduce a filter
   loop and that grants' `filterByTopic` is still distinct).
5. **A view never returns a row outside the caller's scope (P3).** A parity test
   across `{SDK,HTTP,MCP}` proves a view only **subtracts** from the store-layer
   scoped result — no view ever surfaces a cross-scope or cross-tenant row; the
   subject is identity-derived and never a wire argument (a caller cannot apply
   another subject's view). The store-layer scoped query is unchanged.
6. **View admin round-trips on both drivers (conformance).** `create_view →
   apply → update_view → apply → delete_view → list_views` round-trips through the
   `TopicViewStore` conformance suite on sqlite **and** postgres; every method is
   scope-required (`ErrScopeRequired` when `scope.Tenant==""`); a cross-tenant read
   returns nothing.
7. **Admin on {HTTP,MCP}; apply on {SDK,HTTP,MCP}; parity, same PR.** The
   `view_name` apply arg and `degraded_view` marker exist on all three retrieve
   contracts with a parity test (MCP schema goldens regenerated); the
   create/update/delete/list admin capability exists on HTTP and MCP with a parity
   test; admin is absent from the SDK (tier-correct). Both admin surfaces call the
   single `views.Service` core, so each mutation emits **exactly one** governance
   audit event from the core (never from a handler); a parity test asserts the same
   event fires regardless of surface (D-067/D-073 — the side effect lives in the
   core so no surface can omit it).
8. **Knobs D-034-complete.** `retrieval.agent_views.{enabled,on_policy_error,
   subject_precedence}` ship with tuned defaults, placement in every profile's
   effective config, docs, `allKeys`/get/set/explain, validation, and smoke
   checks; `enabled=false` (zero-config) leaves retrieval byte-identical.
9. **No scope-table change.** No agent column on any of the 12 scope tables; ae9
   adds **no** new durable surface (D-151) — it extends ae1's existing
   `topic_views` junction table with admin CRUD, proven by the same
   conformance suite on both drivers; `source_agent` stays a records-only
   label.
10. **Gateway-free (D-036).** View resolution and application perform no gateway
    call; the feature serves in the degraded retrieval path.

## Smoke script

`scripts/smoke/phase-ae9.sh` — SKIPs gracefully until built (ae1+ae6 land first);
then:
- `internal/retrieval/views.go` defines `resolveAndApplyView`, and ae6's
  `filterByTopicOwnScope` is still the single filter it calls (grep).
- `grants.filterByTopic` (fail-closed) is still distinct (D-139 not collapsed).
- `internal/views/views.go` defines `views.Service` with `emitEvent`, and neither
  `internal/api/views_handler.go` nor the MCP `makeViewsHandler` emits an event
  directly (grep: no `Events().Emit` in the view handlers — the audit event lives
  only in the core, D-067/D-073).
- `internal/store/store.go` declares `TopicViewStore` + `TopicViews()`, with
  ae9's admin methods (`CreateView`/`UpdateView`/`DeleteView`/`ListViews`)
  present; ae1's `topic_views` migration (`0013`) exists in **both** dialect
  dirs and is the **only** `topic_views`-creating migration in either dir
  (grep guard: ae9 adds no migration of its own); no agent column appears in
  the 12 scope-table migrations (grep guard).
- `view_name` present on all three retrieve contracts; `degraded_view` on the
  three outputs; the MCP retrieve + views schema goldens carry them.
- `retrieval.agent_views.enabled` / `.on_policy_error` / `.subject_precedence`
  are registered + explainable (`stowage config explain`/`get`); default
  `enabled=false`.
- `go test ./internal/retrieval/ -run View` and the conformance + parity tests
  pass; `go test ./test/integration/ -run TopicViews` passes (or SKIP until real
  drivers wired).
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Unit (`views_test.go`):** subject resolution (agent-only, key-only,
  both-with-precedence in both orders, none→unbound); apply with allow-only /
  deny-only / both / empty; `GetView`→`ErrNotFound`→pass-through; **fail-open**
  (injected `GetView` error ⇒ input returned, `degraded=true`) plus the
  `on_policy_error=closed` variant (returns nil, degraded); an assertion that ae9
  calls ae6's `filterByTopicOwnScope` and defines no second filter.
- **Conformance (`conformance.go`):** `TopicViewStore` round-trip
  (create/update/delete/list/get), scope-required on every method, UNIQUE
  conflict, and a cross-tenant leak guard — run against **both** drivers.
- **Integration (`test/integration/topicviews_test.go`, real drivers, §17 — ae9
  consumes ae1's identity seam + ae6's filter + the topics seam D-089):** the
  full `create → apply → update → apply → delete` lifecycle proving a bound agent
  narrows and an unbound agent does not, on sqlite **and** postgres; the
  **fail-open** path with a forced views-store error; identity/scope propagation
  (a view never returns another scope's row; key-subject path exercised through
  the MCP `KeyIDFromContext` plumbing); `-race`.
- **Parity test:** `view_name` + `degraded_view` on retrieve across
  `{SDK,HTTP,MCP}`; create/update/delete/list across `{HTTP,MCP}`, including an
  assertion that both admin surfaces route through `views.Service` and emit the
  **same** governance audit event once per mutation (the core owns the emission; a
  handler never emits — D-067/D-073).
- **Core (`internal/views/views_test.go`):** `views.Service` CRUD calls
  `(TopicView).Validate()` on writes and emits exactly one governance event per
  mutating op via the events store (mirroring `grants.Service`), with a nil-events
  no-op path.
- **Concurrency (§5):** `resolveAndApplyView` invoked from N goroutines on shared
  input under `-race`.
- **Regression:** `enabled=false` retrieval byte-identical (a no-view request is a
  pass-through); `TestEvalCI` unmoved.
- **No new fuzz target:** ae9 adds no new parse-of-untrusted-input surface — the
  allow/deny JSON is operator-written config, decoded with stdlib `encoding/json`
  under the existing store decode paths.

## Risks & mitigations

- **"Harmonizing" fail-open with grants' fail-closed into a bug.** The whole D-139
  hazard. Mitigated: ae9 **reuses** ae6's distinct fail-open `filterByTopicOwnScope`
  and never touches grants' `filterByTopic`; a grep + unit test assert both remain
  distinct; the glossary + D-149 record the intentional opposite semantics.
- **Curation mistaken for isolation.** A view can only **subtract** from the
  store-layer scoped result; the subject is identity-derived (never a wire arg);
  the tenant from the verified credential/JWT stays the sole P3 boundary. AC-5
  pins it across surfaces. The `on_policy_error=closed` knob is documented as a
  posture override, **not** a new boundary.
- **ae1/ae6 unbuilt ⇒ ae9 blocks on their seams.** ae9 cannot merge until
  `Scope.Agent`, the `_meta` `agent_id` plumbing, and `filterByTopicOwnScope`
  exist. The smoke SKIPs until the files appear; D-149 records the dependency
  (its table-ownership framing is superseded by **D-151** — see the checkpoint
  note at the top of this plan).
- **MCP key-id plumbing gap.** `KeyringMiddleware` discards `key.ID` today; ae9
  adds the stash + `KeyIDFromContext` accessor in the same PR (AC-3) so the key
  subject reaches parity on MCP, not only HTTP.
- **Table generalization / data-carry-over risk — resolved by D-151.** This
  plan originally carried a risk (and mitigation) around ae9 owning a new
  generalized table and carrying ae1's narrower rows over into it, to avoid an
  in-place cross-driver column rename. The checkpoint decision **D-151** closed
  this: ae1 creates `topic_views` directly in its general junction shape at
  migration `0013`, so there is no narrower table to reshape or copy from, and
  ae9 adds neither a table nor a migration.
- **Knob sprawl / accidental default-on.** `enabled=false` default keeps
  zero-config byte-identical; three scalars only, all D-034-complete and
  smoke-checked.

## As-built deviations (implementation notes)

Recorded per CLAUDE.md §4.3 — reasonable deviations discovered during
implementation, none of which change the design's intent.

1. **The auth surface changed underneath this plan (ae7 landed after this plan
   was authored).** The plan's "On HTTP the full `*auth.Key` (with `ID`) is on
   the request context" premise is **stale**: by the time ae9 was implemented,
   ae7's unified `auth.Authenticator`/`AuthMiddleware`/`authMiddleware` had
   replaced the old per-surface verify paths, and **both** HTTP and MCP had
   quietly lost the verified key's `ID` (HTTP synthesized
   `&auth.Key{TenantID, Role}` with no `ID`; MCP never carried it either). ae9
   fixes this at the **one shared root** instead of patching each surface:
   `auth.Authenticator.Authenticate` gains a fourth return, `keyID string`
   (`""` in `ModeJWT` — a verified JWT is not a stored `*auth.Key`, so the
   "key" view subject simply never resolves for a JWT-mode caller). Both
   `internal/api/auth.go`'s `authMiddleware` and
   `internal/mcpserver/server.go`'s `AuthMiddleware` (which `KeyringMiddleware`
   wraps) now thread it through — `keyFromContext(ctx).ID` on HTTP, the new
   `mcpserver.KeyIDFromContext(ctx)` on MCP. This is a **correctness fix**, not
   scope creep: without it, the "key" topic-view subject could never resolve
   on either surface, not just MCP as the plan anticipated.
2. **The `Authenticate` signature change touches every call site** (both
   middlewares + `internal/auth/authenticator_test.go`'s ~11 call sites) —
   documented here since it is a wider blast radius than a typical same-package
   change, but confined to `internal/auth` + the two thin call sites.
3. **The key-id stash lives in `AuthMiddleware`, not `KeyringMiddleware`
   specifically** (the plan's naming) — because ae7 unified both modes behind
   one `AuthMiddleware`, and `KeyringMiddleware` is now a thin wrapper around it
   (`AuthMiddleware(auth.NewKeyringAuthenticator(kr), next)`). The stash applies
   uniformly to both auth modes; `ModeJWT` simply always yields `keyID=""`.
4. **Result-cache bypass for view-eligible requests** (`hasViewApply`,
   `internal/retrieval/views.go` + the two `!r.hasViewApply(scope, req)` guards
   in `retrieval.go`'s Get/Put cache blocks) — **not spelled out in the
   original design**, added for correctness. `ViewName` is a per-request wire
   field and the "key" subject's `CredentialKeyID` is not part of the cache key
   at all (unlike `scope.Agent`, ae1's agent filter's own cache-key dimension,
   D-135); caching a view-eligible response could serve one `view_name`'s (or
   one key-subject's) result to a different `view_name`/key. Mirrors ae6's own
   choice for `IncludeTopics`/`ExcludeTopics` — skip caching rather than widen
   the key. Inert (no bypass) when `agent_views.enabled=false`, preserving the
   zero-config byte-identical invariant (AC-8).
5. **`(TopicView).Validate()` also rejects an empty view** (`ErrEmptyPolicy` —
   reusing ae1's `PutAgentPolicy` sentinel) when both `AllowTopics` and
   `DenyTopics` are empty. Not explicitly stated in the design's Validate()
   description, but required by the junction-table representation D-151 pins:
   a view with zero keys has **no durable row** at all, so it would be
   indistinguishable from "no view exists" (unfindable via `GetView`, absent
   from `ListViews`, and a duplicate `CreateView` would not even be caught as
   `ErrConflict`). Symmetric with `PutAgentPolicy`'s existing guard, since
   ae1's binding *is* one of these views.
6. **`CreateView`'s conflict check is an explicit natural-key existence probe**
   inside the same transaction as the insert, not a bare reliance on the
   per-key `UNIQUE(tenant_id, subject_kind, subject_id, view_name, topic_key)`
   index. Two `CreateView` calls for the *same* natural key but *disjoint*
   topic-key sets would not collide on that index alone; the pre-check makes
   "a view already exists for this subject/view_name" the correct, tested
   `ErrConflict` semantics (AC-6).
7. **`UpdateView` is an atomic delete-then-insert** (not a diff/merge of
   individual keys) — behaviourally equivalent to the design's "insert new
   keys, delete removed keys" framing (the *end row set* always matches the
   new `AllowTopics`/`DenyTopics` exactly either way) and consistent with
   `PutAgentPolicy`'s existing atomic-replace precedent. One side effect
   inherited from that precedent: `UpdatedAt`/`CreatedAt` are both refreshed
   to "now" on every `UpdateView` (ae1's `PutAgentPolicy` has the identical
   characteristic — the junction table has no single row that owns a
   view-level `created_at` independent of its per-key rows).
8. **`TopicView.ID` is a synthesized, informational string**
   (`subject_kind + "/" + subject_id + "/" + view_name`), never a stored
   column — the junction table has no single row that owns a view's identity
   (a view is a *set* of per-key rows, each with its own real primary key).
   Every CRUD method still addresses a view by its natural key, never by this
   string; `memory_views`' `delete_view`/HTTP's `DELETE
   .../{subject_kind}/{subject_id}/{view_name}` use the same natural-key path
   segments.

## Glossary additions

- **Topic view (agent view)** — a named, per-subject read-time curation lens
  `(tenant_id, subject_kind, subject_id, view_name) → {allow_topics, deny_topics}`
  stored in the `topic_views` table (not a scope table, no memory rows). Applied at
  read time it narrows the caller's **own-scope** topic-tagged results via ae6's
  fail-open `filterByTopicOwnScope`. A curation lens, **not** a P3 isolation
  boundary (D-139); it can only subtract. Generalizes ae1's single binding
  (ae1's row == the `("agent", …, "default")` view).
- **View subject** — the `(subject_kind, subject_id)` a view is bound to: an
  `agent_id` (`"agent"`, from `_meta` via ae1) or the **verified credential's key
  id** (`"key"`). Always identity-derived, never a wire argument — a caller can
  apply only its own subject's views.
- **Subject precedence** — the `retrieval.agent_views.subject_precedence` order
  (default `agent,key`) that decides which view subject resolves when both an agent
  id and a key id are present.

## Decisions filed

- **D-149** — Named per-agent/per-key topic **views** generalizing ae1's single
  agent→topic binding: a read-time **curation lens, not isolation** over the
  shared `topic_views` table, keyed by
  `(tenant_id, subject_kind, subject_id, view_name)` (ae1's row == the
  `agent`/`default` view), that only subtracts from own-scope and **fails
  open** (reusing ae6's `filterByTopicOwnScope`, deliberately opposite grants'
  fail-closed `filterByTopic`); the subject is identity-derived (agent from
  `_meta`, else the verified key id — which required new MCP `key.ID` context
  plumbing); apply-a-view on `{SDK,HTTP,MCP}`, view admin on `{HTTP,MCP}`.
  Implements D-139. **Superseded in part by D-151** (checkpoint decision,
  2026-06-30): D-149's original table-ownership framing — ae9 owning a new
  generalized table and carrying ae1's narrower rows over into it — is
  replaced by ae1 creating `topic_views` directly in its general junction
  shape at migration `0013`; ae9 adds no table, seam, migration, or enable
  knob. (Charter D-139.)
