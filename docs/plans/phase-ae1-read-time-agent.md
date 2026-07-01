# Phase ae1 — read-time agent identity dimension (+ Dockyard bump)

- **Status:** draft
- **Owning subsystem(s):** `internal/identity` (optional read-only `Scope.Agent`); `internal/store` (a new `AgentPolicyStore` seam + `agent_topic_policies` table on **both** drivers + conformance — **not** a scope table); `internal/retrieval` (the agent→topic-keys resolver that feeds ae6's `filterByTopicOwnScope`); `internal/mcpserver` (the `dockyard v1.8.0` bump + the `server.RequestMeta(ctx)["agent_id"]` read; a `memory_agent_policy` admin tool); `internal/api` (agent-filter intake + policy-admin routes); `sdk/stowage` (agent-filter intake); `internal/config` (one enable knob)
- **RFC sections:** §5 (identity & scopes), §5.3 (`memory_topics`/topic-keyed slicing reuse), §9.5 (one logic core, D-067/D-073)
- **Depends on phases:** **ae6** (reuses its own-scope fail-open `filterByTopicOwnScope` + the H3 lane/`scoringK` remedy — the generic filter is built **once**, in ae6). **Does *not* depend on ae7** — `agent_id` arrives via `_meta` (MCP) or an explicit field (HTTP/SDK), never the JWT. This phase performs the mechanical Dockyard bump the rest of the track builds on (W1).
- **Informing briefs:** 03 (Engram — topics as extraction magnets; the memory→topic association the agent binding curates over), 02 (CC-memory predecessor — surface-sprawl cautionary tale → the agent filter is *one* core reused across surfaces, not a per-surface fork), 06 (mempalace — gateway-free retrieval, D-036; the agent filter is a store read, never a gateway call).

> **Checkpoint reconciliation (D-151).** This plan as drafted names the binding table
> `agent_topic_policies`, the seam `AgentPolicyStore`, and the enable knob
> `retrieval.agent_filter_enabled`. Per **D-151** the track converges on **one** durable
> surface: ae1 creates the general **`topic_views`** table (`subject_kind` default
> `'agent'`, `subject_id`, `view_name` default `'default'`, `topic_key`, `effect`) behind
> the **`TopicViewStore`** seam (`Store.TopicViews()`) at migration **0013**, writing its
> rows as `(subject_kind='agent', view_name='default')`, gated by
> **`retrieval.agent_views.enabled`**. ae9 then adds named views + the key-id subject +
> admin on this same table/seam/knob (no new table, migration, or enable knob). Read the
> `agent_topic_policies` / `AgentPolicyStore` / `agent_filter_enabled` names below as the
> D-151 names.

## Goal

When this phase is done a host can narrow a tenant's **own** retrieval results by
the calling agent, using only (a) an agent identity carried outside the model's
arguments — `_meta.agent_id` on MCP (via the newly-bumped `dockyard v1.8.0`
`server.RequestMeta`), an explicit `agent_id` field on HTTP/SDK — (b) a small
`(tenant_id, agent_id) → {allow_topics, deny_topics}` policy binding in a new,
**non-scope** `agent_topic_policies` table, and (c) ae6's existing own-scope
`filterByTopicOwnScope`. The agent filter **subtracts** from the caller's own-scope
candidates and never widens scope; it **fails open** (D-139/D-036). There is **zero
schema change to the 12 denormalized scope tables, zero agent column anywhere, and
zero write-path change** — `identity.Scope` gains an optional `Agent` field that is
provably inert on every write and in both drivers' scope-`WHERE` builders. Agent
policy admin (create/get/list/delete a binding) ships on `{HTTP, MCP}`; the agent
read-filter ships on `{SDK, HTTP, MCP}` with a parity test.

## Brief findings incorporated

- **03 (Engram):** the agent binding curates over the same `memory_topics`
  extraction-magnet junction ae6 filters on (D-089) — the agent policy resolves to
  topic keys, and ae6's filter does the slicing. No new tagging scheme; agent is a
  read-time lens over existing associations.
- **02 (CC-memory):** surface sprawl is the named predecessor failure → the agent
  filter is implemented **once** in `internal/retrieval` (reusing ae6's single
  `filterByTopicOwnScope`) and each surface is a thin caller that only differs in
  *how it sources the agent identity* — a sanctioned per-surface intake divergence
  (D-140), not a forked capability.
- **06 (mempalace):** retrieval must serve gateway-free (D-036); the agent policy
  resolution + filter are pure store reads with no gateway call, so agent narrowing
  works in the degraded path.

## Findings I'm departing from

- **There was no `phase-ae1` plan and there is no placeholder to "fix" (M5, corrected).**
  The charter AC sketch says *"the placeholder `MetaFromContext` is wrong — M5"*,
  which reads as *correct an existing wrong symbol*. **Code truth:** a repo-wide
  search for `MetaFromContext` / `RequestMeta` / `agent_id` / `AgentID` /
  `Scope.Agent` over `internal`, `sdk`, `cmd` returns **zero** Go hits. `dockyard
  v1.7.3` exposes only the *outbound* tool-definition `Meta`
  (`runtime/server/tool.go`), and the handler type carries no meta argument
  (`runtime/tool/builder.go`: `type Handler[In, Out any] func(ctx, in In)
  (Result[Out], error)`). So ae1 **adds** the inbound-meta call site from scratch
  after bumping to `v1.8.0`; there is nothing to correct. The bump is therefore
  **load-bearing**, not cosmetic. Recorded in D-135.
- **ae6 is my hard dependency and is currently plan-only.** `filterByTopicOwnScope`,
  `internal/retrieval/topicfilter.go`, and `DegradedTopicFilter` do **not** exist in
  code yet (grep hits `docs/` + `scripts/smoke/phase-ae6.sh`, which SKIPs). ae1's
  "reuse ae6's filter" story is real **only after ae6 lands**; ae1's smoke and the
  reuse-dependent integration test SKIP gracefully until `topicfilter.go` exists.
  Sequencing stands: ae6 → ae1 (charter Wave map).
- **How agent reaches HTTP/SDK before ae7/ae2 (a deliberate D-140 split).** `_meta`
  is an MCP-only seam and the JWT verifier (ae7) is not a dependency, so on HTTP and
  SDK the agent identity is sourced from an **explicit `agent_id` request field**,
  while MCP sources it from `_meta.agent_id`. This is exactly the sanctioned
  MCP-vs-HTTP intake divergence D-140 blesses (precedent: `assert`'s HTTP omission):
  the **core** agent filter (keyed on `Scope.Agent`) is byte-identical across all
  three surfaces; only the *intake wire* differs. The parity test asserts the core,
  not the intake. This is a design choice ae1 pins, not a parity violation.
- **The two drivers' scope-`WHERE` builders have different arities.** `pgstore`'s
  `buildScopeWhere`/`buildExactScopeWhere` thread a `startIdx`/return `nextIdx`
  (`$N` placeholders); `sqlitestore`'s take no index (`?` placeholders). The C1
  inertness assertion (no `Agent` reference) must be checked in **both** signatures.

## Design

### 1. `identity.Scope.Agent` — the optional read-only field (C1)

Add one field to `identity.Scope` (`internal/identity/identity.go:29-34`):

```go
type Scope struct {
    Tenant  string
    Project string
    User    string
    Session string
    // Agent is the calling agent identity, set ONLY on the read path (from
    // _meta.agent_id on MCP, or an explicit agent_id field on HTTP/SDK). It is a
    // READ-TIME identity/filter dimension (D-135): it is NEVER persisted, NEVER a
    // column on any of the 12 scope tables, and NEVER referenced by a scope-WHERE
    // builder or an INSERT. It drives only the read-time agent→topic filter
    // (internal/retrieval), which can only SUBTRACT from the caller's own-scope
    // results (P3 preserved, fails open per D-139).
    Agent string
}
```

- `Validate()` is **unchanged** — only `Tenant` is required; `Agent` is optional and
  never validated (an empty agent = no agent filtering).
- `String()` is **unchanged** — `Agent` is not part of the canonical slash-path,
  which reinforces that it is not a *persisted* scope dimension. **This does NOT
  extend to the read-result cache key:** because the agent filter subtracts from the
  *final* cached result set, `Scope.Agent` **must** be part of the retrieval cache
  key even though it is absent from `String()` and from every scope-`WHERE` builder
  (see §6). The "not persisted / not in `String()`" reasoning applies to durable
  scope predicates only, never to the in-memory read cache.
- **Provable inertness (C1).** Neither driver's `buildScopeWhere` nor
  `buildExactScopeWhere` (`internal/store/pgstore/scope.go`,
  `internal/store/sqlitestore/scope.go`) is touched — they build their `WHERE`
  field-by-field for non-empty `Tenant/Project/User/Session` only, so a new `Agent`
  field can never enter a scope predicate. Every write `INSERT`
  (`records.go`/`memories.go` on both drivers) enumerates its columns explicitly and
  binds `Tenant/Project/User/Session` by name/position — none bind `Agent`, and there
  is no `agent` column to bind. A grep-gated test asserts `Agent`/`agent_id` appears
  in **neither** `scope.go` nor any `INSERT` column list on either driver.

### 2. `AgentPolicyStore` + `agent_topic_policies` — the non-scope policy binding (D-146, C2)

A new sub-store on the `Store` seam (`internal/store/store.go`) and a new table
(migration `0013`), **both drivers**, proven by the shared conformance suite. This is
**not** one of the 12 scope tables: it carries **no memory rows** and **no** agent
column ever lands on a scope table. It is tenant-scoped config keyed by
`(tenant_id, agent_id)`.

New type (`internal/store/types.go`):

```go
// AgentPolicy is a read-time (tenant_id, agent_id) → {allow, deny} topic-key
// binding (Phase ae1, D-146). It curates (never isolates — D-139) the caller's
// own-scope retrieval by the calling agent. NOT a scope table: no memory rows, no
// user_id, no agent column on any scope table.
type AgentPolicy struct {
    TenantID    string
    AgentID     string
    AllowTopics []string // keep only own-scope memories tagged with ≥1 of these (empty = no include constraint)
    DenyTopics  []string // drop any own-scope memory tagged with one of these
    CreatedAt   int64    // unix millis
    UpdatedAt   int64    // unix millis
}
```

New seam (`internal/store/store.go`) + `AgentPolicies() AgentPolicyStore` on the
top-level `Store`:

```go
// AgentPolicyStore manages read-time agent→topic policy bindings (Phase ae1,
// D-146). NOT a scope table — carries no memory rows. All methods are tenant-scoped
// (P3, scope.Tenant required — ErrScopeRequired on empty); there is NO unscoped
// variant. Generalized to named views by ae9 (add subject_kind + view_name).
type AgentPolicyStore interface {
    // PutAgentPolicy upserts the binding for (scope.Tenant, agentID). Replaces any
    // existing allow/deny sets for that agent atomically. Rejects an empty agentID.
    PutAgentPolicy(ctx context.Context, scope identity.Scope, p AgentPolicy) error
    // GetAgentPolicy returns the binding for (scope.Tenant, agentID). Returns
    // ErrNotFound when no binding exists (an UNBOUND agent — the filter then leaves
    // the caller's own-scope results unfiltered, NOT degraded).
    GetAgentPolicy(ctx context.Context, scope identity.Scope, agentID string) (*AgentPolicy, error)
    // ListAgentPolicies returns all bindings for the tenant, ordered by agent_id asc.
    ListAgentPolicies(ctx context.Context, scope identity.Scope) ([]AgentPolicy, error)
    // DeleteAgentPolicy removes the binding for (scope.Tenant, agentID).
    // Returns ErrNotFound when absent.
    DeleteAgentPolicy(ctx context.Context, scope identity.Scope, agentID string) error
}
```

Table (migration `internal/store/migrations/{sqlite,postgres}/0013_agent_topic_policies.sql`,
forward-only). Allow/deny are stored as a small junction (one row per topic key +
effect) rather than CSV, matching the `memory_topics` junction idiom and avoiding
comma-in-key fragility:

```sql
CREATE TABLE IF NOT EXISTS agent_topic_policies (
    id         TEXT    NOT NULL PRIMARY KEY,
    tenant_id  TEXT    NOT NULL,
    agent_id   TEXT    NOT NULL,
    topic_key  TEXT    NOT NULL,
    effect     TEXT    NOT NULL CHECK(effect IN ('allow','deny')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_topic_policies_agent
    ON agent_topic_policies(tenant_id, agent_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_topic_policies
    ON agent_topic_policies(tenant_id, agent_id, topic_key, effect);
```

- `PutAgentPolicy` = delete-then-insert the `(tenant, agent)` rows in one tx (atomic
  replace). `GetAgentPolicy` aggregates rows into `AllowTopics`/`DenyTopics`.
- **P3:** every method requires `scope.Tenant` and filters on `tenant_id` — no
  unscoped variant. Cross-tenant reads are unconstructible (the `WHERE tenant_id`
  is mandatory; conformance proves isolation).
- **C2 / DSAR:** this table carries **no memory rows and no `user_id`** — it is
  per-agent *tenant config*, not user PII — so the DSAR cascade (`DSARCounts`,
  `OpsStore.DeleteUserData`) is **not** extended to it, and `source_agent` stays a
  **records-only** label. No dedupe index, no `UNIQUE` on a scope table, no
  buffer→flush threading changes.

### 3. The agent→topic resolver that feeds ae6's filter (`internal/retrieval`)

The Retriever gains an optional agent-policy handle and enable flag (builder-wired,
matching the `grantsSt`/`WithProfiles` pattern — set after `New`, before serving,
not concurrent with `Retrieve`):

```go
// on *Retriever
agentPolSt        store.AgentPolicyStore // nil ⇒ agent filter disabled
agentFilterOn     bool                   // from retrieval.agent_filter_enabled

func (r *Retriever) WithAgentPolicy(st store.AgentPolicyStore, enabled bool) *Retriever
```

New file `internal/retrieval/agentfilter.go`:

```go
// resolveAgentTopics returns the (allow, deny) topic keys bound to scope.Agent, or
// (nil, nil, false) when agent filtering is inactive (disabled, no agent, no store,
// or an UNBOUND agent). degraded is true ONLY when the policy STORE errored
// (fail-open, D-139) — an unbound agent (ErrNotFound) is a legitimate "no filter",
// not a degradation.
func (r *Retriever) resolveAgentTopics(
    ctx context.Context, scope identity.Scope,
) (allow, deny []string, active bool, degraded bool)
```

Behaviour:
- `!r.agentFilterOn || r.agentPolSt == nil || scope.Agent == ""` ⇒ `(nil,nil,false,false)`
  (inert; zero-config byte-identical).
- `GetAgentPolicy` **error** (not `ErrNotFound`) ⇒ log a warning, return
  `(nil,nil,false,true)` — **fail open** (D-139/D-036), the caller sees unfiltered
  own-scope results and `DegradedAgentFilter=true`.
- `ErrNotFound` (unbound agent) ⇒ `(nil,nil,false,false)` — unfiltered, not degraded.
- Otherwise ⇒ `(policy.AllowTopics, policy.DenyTopics, true, false)`.

**Placement — reuses ae6's filter, one composed extra pass.** In `Retrieve`, at the
**same** post-RRF-fusion / pre-`scoringK`-trim point ae6 introduced (see ae6's
`internal/retrieval/retrieval.go` wiring), after ae6's request-topic pass runs,
resolve the agent policy and — when `active` — call ae6's **existing**
`filterByTopicOwnScope(ctx, scope, ids, allow, deny)` a second time over the
surviving fused IDs. Each pass only subtracts and each fails open independently;
composition is well-defined (request filter ∩ agent filter). Set
`resp.DegradedAgentFilter` from either the resolver's `degraded` or the filter pass's
`degraded`. Because the agent pass runs over the same laneK-wide pool ae6 widened,
it inherits ae6's no-underfill property (H3) — ae1 adds **no** new lane/`scoringK`
logic. When no topic filter *and* no active agent filter is present, the candidate
window is unchanged (zero-config unaffected).

New response marker `DegradedAgentFilter bool` on `retrieval.Response` (sibling to
ae6's `DegradedTopicFilter`), mirrored on the three output types (fail-open
transparency, D-036).

**Because this agent pass runs *inside* the cached result path** (the fused, filtered
items are what `ResultCache` stores and a cache HIT returns verbatim, bypassing this
stage), it forces the two cache-coherence requirements in §6 — the read-result cache
key must include `Scope.Agent`, and a policy mutation must invalidate affected reads.

### 4. Surface intake — the sanctioned D-140 split, parity on the core

- **Dockyard bump (M5).** `go get github.com/hurtener/dockyard@v1.8.0 && go mod tidy`
  (`go.mod:8` `v1.7.3` → `v1.8.0`).
- **MCP** (`internal/mcpserver/handlers.go` `makeRetrieveHandler`, at the scope build
  `:177`): read the host-injected identity and stamp it on the **read** scope only:

  ```go
  scope = identity.Scope{Tenant: scope.Tenant, Project: in.ProjectID, User: in.UserID}
  if m := server.RequestMeta(ctx); m != nil {
      if a, ok := m["agent_id"].(string); ok {
          scope.Agent = a // read-time only; never persisted (D-135)
      }
  }
  ```

  `RequestMeta` returns stdlib `map[string]any` (nil when no `_meta` was sent);
  the `agent_id` value is type-asserted to `string` (nil-safe). **No new MCP arg** —
  MCP sources agent from `_meta` exclusively.
- **HTTP** (`internal/api/retrieve_handler.go`): add `agent_id` to `retrieveRequest`;
  set `scope.Agent = req.AgentID` at the scope build (`:135`). (No `_meta`/JWT on HTTP
  until ae7; the explicit field is the D-140-sanctioned HTTP intake.)
- **SDK** (`sdk/stowage/types.go` + `embedded.go`): add `AgentID` to `RetrieveRequest`;
  set `scope.Agent = req.AgentID` at the in-process scope build (`:273`);
  `sdk/stowage/http.go` rides the JSON tag.
- **Parity test** asserts: same agent identity + same policy binding ⇒ identical
  filtered result across `{SDK, HTTP, MCP}`. The test exercises the **core**
  (`Scope.Agent`-keyed filter), not the intake wire (whose per-surface divergence is
  D-140-sanctioned).

### 5. Agent policy admin — `{HTTP, MCP}` (policy-admin tier)

CRUD is thin: the **core** is the `AgentPolicyStore` seam itself (validation —
`agent_id` required, `effect` constrained — lives in the drivers, proven identically
by conformance, so no surface can diverge). Matches the grants-admin tier
(`{HTTP, MCP}`, not SDK).

- **MCP:** a new tool `memory_agent_policy` (`internal/mcpserver`), op-dispatched
  (`create`|`get`|`list`|`delete`), mirroring `memory_grants`
  (`handlers.go:makeGrantsHandler`, registered in `server.go`). Regenerate the schema
  goldens.
- **HTTP:** routes mirroring `internal/api/grants_handler.go`, e.g.
  `PUT /v1/scopes/agent-policies` (upsert), `GET /v1/scopes/agent-policies` (list),
  `GET /v1/scopes/agent-policies/{agent_id}` (get), `DELETE …/{agent_id}` (delete).

### 6. Cache coherence — key on `Scope.Agent`, invalidate on policy change (B2-class)

The read-result cache (`internal/retrieval/cache.go`) is the one place the agent
filter can silently break, because the agent pass (§3) subtracts from the *final*
result set that is cached (`retrieval.go` caches the post-fusion, post-filter items)
and a cache **HIT** returns those items directly, bypassing the post-fusion
agent-filter stage entirely. Two fixes, both required:

- **Key the cache on `Scope.Agent` (blocking #1).** Today `scopeCacheKey` encodes
  `Tenant\x00Project\x00User\x00Session` (`cache.go:61-63`) and `cacheKey`
  (`cache.go:88-98`) folds that in — **Agent is absent**. With
  `agent_filter_enabled=true`, two callers differing *only* in `Scope.Agent` (same
  tenant/project/user/session/query/profile/window/kinds/lanes/limit) collide on one
  key: agent B receives agent A's filtered items, and a prior unbound/no-agent read
  caches *unfiltered* items that a later bound agent then receives with its filter
  silently skipped. This is exactly the Phase-30 **B2** class the `cache.go:57-60`
  comment documents fixing for the *user* dimension. **Fix:** extend `scopeCacheKey`
  (and therefore `cacheKey` and the `scopeGen` map) to include `Scope.Agent` as a
  fifth NUL-delimited dimension, so a bound-agent read never collides with another
  agent's — or an unbound/no-agent — cached set. `Agent` stays absent from
  `String()` and every scope-`WHERE` builder (§1); this touches the in-memory cache
  key **only**. *(Mirror concern: ae6's request-topic filter uses the same
  inside-cache placement and also does not key the cache on its filter dimension —
  cache-keying-by-filter-dimension is a shared ae6/ae1/ae9 concern the wave
  checkpoint tracks; ae1 keys on `Agent`, ae6 must key on its request-topic
  selector.)*
- **Invalidate the cache on a policy mutation (blocking #2).** `PutAgentPolicy` /
  `DeleteAgentPolicy` change which items a bound agent's filtered read returns, but
  `agent` is **not** part of the `scopeGen` tenant→project→user→session hierarchy, so
  no existing `InvalidateScope` path (`cache.go:219-237`, called by reconcile /
  lifecycle / memories_handler / review) fires on a policy edit — the agent's cached
  agent-filtered reads stay stale until TTL. D-067/D-073 require a capability's side
  effects (cache invalidation) to live in the **core** so no surface can omit them.
  **Fix:** the policy-admin **core** path (not the HTTP/MCP handlers) invalidates
  affected agent-filtered reads on every `Put`/`Delete` — `InvalidateScope` at the
  binding's `{Tenant}` (a per-agent generation bump is a valid finer-grained
  alternative, since the cache key now carries `Agent`). This is wired where the
  mutation happens so both surfaces inherit it.

## Files added or changed

```text
go.mod                                                           # CHANGED — dockyard v1.7.3 → v1.8.0
internal/identity/identity.go                                    # CHANGED — Scope.Agent (read-only, inert)
internal/store/types.go                                          # CHANGED — AgentPolicy type
internal/store/store.go                                          # CHANGED — AgentPolicyStore seam + Store.AgentPolicies()
internal/store/migrations/sqlite/0013_agent_topic_policies.sql   # NEW — non-scope policy table
internal/store/migrations/postgres/0013_agent_topic_policies.sql # NEW — same, pg dialect
internal/store/sqlitestore/agentpolicy.go                        # NEW — driver impl (delete+insert replace)
internal/store/pgstore/agentpolicy.go                            # NEW — driver impl
internal/store/conformance/phase-ae1.go                          # NEW — RunAgentPolicy (CRUD, scope-isolation, scope-required, not-found)
internal/retrieval/agentfilter.go                               # NEW — resolveAgentTopics (fail-open); feeds ae6's filterByTopicOwnScope
internal/retrieval/agentfilter_test.go                          # NEW — unit: disabled/no-agent/unbound/bound/fail-open
internal/retrieval/cache.go                                     # CHANGED — key scopeCacheKey/cacheKey/scopeGen on Scope.Agent (§6, blocking #1); policy-mutation invalidation hook
internal/retrieval/cache_test.go                                # CHANGED — two distinct bound agents don't share a cached result; bound read never returns an unbound/other-agent cached set
internal/retrieval/retrieval.go                                 # CHANGED — WithAgentPolicy; Response.DegradedAgentFilter; composed agent pass
internal/config/config.go                                       # CHANGED — retrieval.agent_filter_enabled (field, allKeys, get/set, validate)
internal/mcpserver/server.go                                    # CHANGED — register memory_agent_policy
internal/mcpserver/handlers.go                                  # CHANGED — RequestMeta(ctx)["agent_id"] → Scope.Agent; makeAgentPolicyHandler; DegradedAgentFilter
internal/mcpserver/contracts.go                                 # CHANGED — RetrieveOutput.DegradedAgentFilter; AgentPolicy{Input,Output}
internal/mcpserver/testdata/*.schema.json                       # CHANGED — regen (new tool + field)
internal/api/retrieve_handler.go                                # CHANGED — retrieveRequest.AgentID; response DegradedAgentFilter
internal/api/agentpolicy_handler.go                             # NEW — HTTP policy-admin routes
internal/api/router.go (or wherever routes register)            # CHANGED — mount agent-policy routes
sdk/stowage/types.go                                            # CHANGED — RetrieveRequest.AgentID; RetrieveResponse.DegradedAgentFilter
sdk/stowage/embedded.go                                         # CHANGED — thread Scope.Agent
scripts/smoke/phase-ae1.sh                                      # NEW
test/integration/agentfilter_test.go                           # NEW — real-driver agent-narrow + fail-open + cross-tenant (§17)
docs/plans/README.md                                           # CHANGED — register the ae* track / update Plans line
docs/decisions.md                                              # CHANGED — D-146 (D-135 is filed by the first-landing W0 phase, not ae1)
docs/glossary.md                                               # CHANGED — read-time agent identity, agent→topic policy binding, _meta seam
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `retrieval.agent_filter_enabled` | `false` | Master switch for the read-time agent→topic filter. Default **off** ⇒ zero-config start is byte-identical (even a host that injects `_meta.agent_id` gets no filtering until an operator enables it). Flat bool on `RetrievalConfig` (sibling to `include_superseded`). **D-034-complete:** tuned default, present in every profile's effective config (added to `Defaults()`; no per-profile override needed — off everywhere), docs, `allKeys`/get/set/explain, validation (bool parse), and a smoke check. Fail-open behaviour on a policy-store error is **hardwired** per D-139 (not a knob); ae9 introduces the configurable `retrieval.agent_views.*` group when it generalizes bindings to named views. |

## Acceptance criteria (binding)

1. **`Scope.Agent` inert on writes and scope predicates (C1).** `identity.Scope`
   gains an `Agent` field defaulting `""`; `Validate()`/`String()` are unchanged. A
   grep-gated test asserts `Agent`/`agent_id` appears in **neither**
   `buildScopeWhere`/`buildExactScopeWhere` **nor** any `INSERT` column list on
   **both** drivers, so no write or scope query can reference it.
2. **No schema change to the 12 scope tables; no agent column (C2).** No migration
   touches a scope table; no `agent` column is added anywhere; `source_agent` stays a
   records-only label; no dedupe-index / `UNIQUE` / DSAR-cascade / buffer→flush
   change. `agent_topic_policies` carries no memory rows and no `user_id` (asserted by
   inspecting the migration + a store test that a policy row is never returned by any
   memory read).
3. **Dockyard bump + real symbol (M5).** `go.mod` pins `github.com/hurtener/dockyard
   v1.8.0`; `makeRetrieveHandler` reads `server.RequestMeta(ctx)["agent_id"]`
   (type-asserted to string, nil-safe) onto the **read** scope only. There is **no**
   `MetaFromContext` symbol (it never existed); the build compiles against the real
   `v1.8.0` `RequestMeta`.
4. **Policy store on both drivers, conformance, scope-required (D-146, P3).**
   `AgentPolicyStore` (Put/Get/List/Delete) is implemented by sqlite **and** postgres
   and passes a shared conformance suite covering CRUD round-trip, `ErrScopeRequired`
   on empty tenant (no unscoped variant), cross-tenant isolation (a policy in tenant A
   is invisible to tenant B), and `ErrNotFound`.
5. **Agent narrowing works, own-scope only (P3).** With `agent_filter_enabled=true`,
   a bound agent's retrieval returns only the caller's own-scope memories whose
   `memory_topics` membership satisfies the binding's allow/deny (via ae6's
   `filterByTopicOwnScope`); no cross-scope row ever appears (the store scope query is
   unchanged — the agent pass only subtracts).
6. **Fail-OPEN (D-139).** A policy-**store** error returns the caller's own
   *unfiltered* results with `DegradedAgentFilter=true` (proven by fault injection) —
   the deliberate **opposite** of grants' fail-closed `filterByTopic`. An **unbound**
   agent (`ErrNotFound`) returns unfiltered results **without** the degraded marker.
7. **Reuses ae6's single filter (no redundancy).** ae1 adds **no** new topic-filter or
   lane/`scoringK` logic; it calls ae6's `filterByTopicOwnScope`. A grep asserts ae1
   defines a *resolver* (`resolveAgentTopics`) but **not** a second topic-filter
   function.
8. **Read-filter parity `{SDK, HTTP, MCP}` (D-067/D-140).** The core `Scope.Agent`
   filter yields identical results across all three surfaces (parity test); the
   per-surface **intake** divergence (MCP `_meta.agent_id`; HTTP/SDK explicit
   `agent_id`) is the D-140-sanctioned split, documented and not treated as a parity
   break.
9. **Policy admin parity `{HTTP, MCP}`.** create/get/list/delete a binding is available
   on HTTP and MCP (not SDK), each a thin caller of the `AgentPolicyStore` core, with
   validation living in the drivers (identical via conformance).
10. **Knob D-034-complete.** `retrieval.agent_filter_enabled` ships with default
    `false`, every-profile placement, docs, `allKeys`/get/set/explain, validation, and
    a smoke check; zero-config (flag off) behaviour is byte-identical to today.
11. **Gateway-free (D-036).** The resolver and filter perform no gateway call; agent
    narrowing serves in the degraded retrieval path.
12. **Read-result cache is keyed on `Scope.Agent` (§6, blocking #1).** With
    `agent_filter_enabled=true`, `scopeCacheKey`/`cacheKey`/`scopeGen` include
    `Scope.Agent`. A test asserts two distinct **bound** agents with identical
    tenant/project/user/session/query/profile/window/kinds/lanes/limit do **not**
    share a cached result, and that a bound-agent read never returns an earlier
    unbound/other-agent cached set (the post-fusion agent filter is never silently
    skipped on a cache HIT). `Agent` remains absent from `String()` and every
    scope-`WHERE` builder (AC-1 unaffected).
13. **Policy mutation invalidates affected reads (§6, blocking #2, D-067/D-073).**
    `PutAgentPolicy`/`DeleteAgentPolicy` invalidate the affected agent-filtered reads
    in the **core** (via `InvalidateScope` at the binding's `{Tenant}`, or a per-agent
    generation bump) so no surface can omit it. A test asserts a bound agent's read
    returns updated results immediately after a policy edit (no stale-until-TTL
    window), and that the invalidation is triggered by the core path, not the handler.

## Smoke script

`scripts/smoke/phase-ae1.sh` — SKIPs gracefully until each surface exists; then:
- `go.mod` pins `dockyard v1.8.0` (not `v1.7.3`).
- `internal/mcpserver/handlers.go` calls `server.RequestMeta(` and stamps `Scope.Agent`;
  `MetaFromContext` is **absent** from the tree.
- `identity.Scope` has an `Agent` field; `Agent`/`agent_id` is **absent** from both
  drivers' `scope.go` and no scope-table migration adds an `agent` column.
- migrations `0013_agent_topic_policies.sql` exist for both drivers; `AgentPolicyStore`
  + `Store.AgentPolicies()` are declared.
- `internal/retrieval/agentfilter.go` defines `resolveAgentTopics` and does **not**
  redefine a topic-filter function (ae6 reuse).
- `retrieval.agent_filter_enabled` is a registered, explainable knob defaulting `false`.
- `agent_id` present on the HTTP + SDK retrieve contracts; `memory_agent_policy` tool +
  HTTP agent-policy routes registered.
- `internal/retrieval/cache.go` `scopeCacheKey` includes `Scope.Agent` (§6, blocking #1);
  `PutAgentPolicy`/`DeleteAgentPolicy` wire cache invalidation in the core (§6, blocking #2).
- `go test ./internal/retrieval/ -run 'AgentFilter|Cache'`, the conformance run, and the
  parity test pass.
- `OK ≥ count(criteria)`, `FAIL = 0`.

## Test plan

- **Unit (`agentfilter_test.go`):** disabled (flag off) → inert; no agent → inert;
  store nil → inert; unbound agent (`ErrNotFound`) → unfiltered, `degraded=false`;
  bound allow-only / deny-only / both → correct subtraction; policy-store error →
  unfiltered, `degraded=true` (fail-open). Assert ae1 calls ae6's
  `filterByTopicOwnScope` (not a private copy).
- **Conformance (`internal/store/conformance/phase-ae1.go`, both drivers):**
  Put/Get/List/Delete round-trip; atomic replace on re-Put; `ErrScopeRequired` on
  empty tenant; cross-tenant isolation; `ErrNotFound`.
- **Integration (`test/integration/agentfilter_test.go`, real drivers, §17 — ae1
  consumes ae6's filter seam + `memory_topics`/D-089 and adds the `AgentPolicyStore`
  public interface):** on **both** sqlite + postgres — bind agent→allow topics,
  retrieve with `Scope.Agent` set ⇒ only allowed-topic own-scope memories; unbound
  agent ⇒ unfiltered; **fail-open** with a forced policy-store error ⇒ unfiltered +
  `DegradedAgentFilter=true`; cross-tenant policy never applies; scope/identity
  propagation (an agent filter never returns another scope's row); `-race`. SKIPs until
  ae6's `filterByTopicOwnScope` exists.
- **Cache coherence (`cache_test.go`, §6):** (a) **keying (blocking #1)** — two
  distinct bound agents with identical tenant/project/user/session/query/profile/
  window/kinds/lanes/limit get **separate** cache entries (no collision); a bound
  agent's read after a prior unbound/other-agent read for the same key does **not**
  return the cached unfiltered/other set (the agent filter is never skipped on a
  HIT). (b) **invalidation (blocking #2)** — after `PutAgentPolicy`/`DeleteAgentPolicy`
  the affected agent's next read reflects the new binding immediately (no
  stale-until-TTL), and the invalidation fires from the core mutation path, not the
  handler.
- **Parity test:** same agent + binding ⇒ identical filtered result across
  `{SDK, HTTP, MCP}`; MCP schema goldens regenerated for the new tool + field.
- **Regression:** flag-off retrieval byte-identical to today; `TestEvalCI` unmoved.
- **No new fuzz target** — ae1 adds no parse surface (agent policies are typed store
  rows; `agent_id` from `map[string]any` is a plain type-assertion).

## Risks & mitigations

- **Redundancy with ae6.** Mitigated by AC-7 — ae1 defines only a *resolver* and calls
  ae6's single `filterByTopicOwnScope`; a grep gate stops a second topic filter.
- **Fail-OPEN vs grants' fail-CLOSED confusion.** The opposite error semantics are
  deliberate (D-139): the agent filter is *curation, not isolation*, so a policy-store
  error must not blind the caller to their own memories. Pinned by D-139 + the glossary
  + AC-6; ae1 reuses ae6's already-fail-open filter, so it inherits the correct
  direction by construction.
- **`v1.8.0` may not export `RequestMeta` as assumed.** PREREQ-1 states it ships
  `func RequestMeta(ctx) map[string]any` + `WithRequestMeta` in `runtime/server`.
  Mitigation: the bump-and-compile is the first concrete step; the smoke greps for the
  real symbol; if the export differs, reconcile the call site before proceeding (the
  RFC/real API wins over the charter's name).
- **ae6 not yet landed.** ae1's reuse-dependent smoke and integration test SKIP until
  `internal/retrieval/topicfilter.go` exists; ae1 is sequenced strictly after ae6
  (charter Wave map).
- **Cache serves cross-agent / stale results (B2-class).** The agent filter runs
  *inside* the cached result path, so an un-keyed cache collides two agents and a
  policy edit leaves reads stale until TTL. Mitigated by §6 + AC-12/AC-13: the cache
  key includes `Scope.Agent`, and `Put`/`Delete` invalidate in the core. This is a
  shared ae6/ae1/ae9 concern (ae6's request-topic filter has the same placement) the
  wave checkpoint tracks as cache-keying-by-filter-dimension.
- **Scope creep into a persisted agent partition.** Prevented by AC-1/AC-2 + D-135:
  `Scope.Agent` is read-only and inert; the policy table is not a scope table.

## Glossary additions

- **Read-time agent identity** — `identity.Scope.Agent`: the calling-agent dimension
  set **only on the read path** (from `_meta.agent_id` on MCP, an explicit `agent_id`
  field on HTTP/SDK). Never persisted, never a column on any of the 12 scope tables,
  never in a scope-`WHERE` builder or an `INSERT`; it drives only the read-time
  agent→topic filter, which can only subtract from own-scope results (D-135).
- **Agent→topic policy binding** — a `(tenant_id, agent_id) → {allow_topics,
  deny_topics}` row set in the non-scope `agent_topic_policies` table
  (`store.AgentPolicy`), resolved at read time and fed to ae6's `filterByTopicOwnScope`
  to curate (not isolate — D-139) an agent's own-scope retrieval. Generalized to named
  views by ae9. **Fails open** on a policy-store error (D-139/D-036).
- **`_meta` seam** — Dockyard `v1.8.0`'s inbound per-call host metadata, read verbatim
  via `server.RequestMeta(ctx) map[string]any` (nil when unsent); the key contract
  (e.g. `agent_id`) is owned by Stowage + Harbor, not Dockyard. ae1 is its first
  consumer (agent identity); ae2 generalizes intake to `user`/`session`.

## Decisions filed

- **D-135 (filed by ae1).** The parent `ae*` track decision: agent is a **read-time
  identity/filter only** with no schema migration on and no agent column on the 12
  scope tables; identity arrives from Harbor JWT (HTTP, ae7) and host `_meta` (MCP);
  Stowage is a verify-never-mint verifier. Filed here as ae1's decision-log entry
  (the audit confirmed no earlier W0 plan claims it), plus its M5 correction (no
  `MetaFromContext` placeholder existed — the real symbol is `server.RequestMeta`) and
  the C1/C2 inertness guarantees ae1 implements.
- **D-146 (filed by ae1, superseded on shape by D-151).** The `(tenant_id, agent_id) →
  {allow, deny}` topic-key policy binding: **not** a scope table, carries no memory
  rows and no `user_id`; on both drivers with a scope-required (no unscoped) seam,
  forward-only migration, conformance-proven; excluded from the DSAR cascade;
  generalized to named views by ae9. **Per the checkpoint decision D-151** the table is
  **`topic_views`** (general shape), the seam is **`TopicViewStore`** (`Store.TopicViews()`),
  and the enable knob is **`retrieval.agent_views.enabled`** — read the
  `agent_topic_policies`/`AgentPolicyStore`/`agent_filter_enabled` names in the Design
  below as those D-151 names.
