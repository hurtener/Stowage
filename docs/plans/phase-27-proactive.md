# Phase 27 — Proactive trigger engine (RFC §6d, implements D-028)

- **Status:** approved
- **Owning subsystem(s):** `internal/proactive` (new: engine, rules, governance, tuning),
  `internal/store` (two new seams: `SuggestionStore`, `ScopeSettingsStore` — both
  day-one tables), `internal/lifecycle` (suggestion-expiry sweep), `internal/config`
  (profile governance defaults), `internal/api` / `internal/mcpserver` / `sdk/stowage`
  (the `memory_suggestions` + proactive-governance surfaces), `test/integration`
- **RFC sections:** §6d (proactive memory: trigger engine, threshold scoring, governance,
  feedback tuning, stretch pattern-mining), §4.2 scoring (reused), §9.1 (`GET /v1/suggestions`),
  §8.1 (`suggestions` + `scope_settings` are day-one), §9.5/D-067, §7/§10/P5, D-028, D-034
- **Depends on phases:** 22/23b (episodes + `episodes.Similar`), 09/10 (retrieval scoring
  + `SimilarNarratives`), 14 (lifecycle sweeps), 11 (feedback counters), 03 (the day-one
  `suggestions`/`scope_settings` tables)
- **Informing briefs:** 06 (mempalace — temporal/proactive positioning), 05 (online
  adaptation — accept/dismiss tuning), 04 (CL-Bench), 02 (ccmem — scoring/decay)

## Goal

When this phase is done, **Stowage proactively offers context** — the holder of the
information decides it might be useful, not the agent. `GET /v1/suggestions?session_id=&query=`
runs the **trigger engine**: three rules (a new session in a scope with a **recent
episode**; a query **resembling a past episode**; a memory **approaching `valid_until`**)
produce candidates, each scored by the **same `scoring.Score` machinery as retrieval**
and surfaced only **above a governance threshold within a strict per-request budget**
("silence over spam"). Offers persist as `pending` `suggestions` rows; **accept/dismiss**
(`POST /v1/suggestions/{id}`) tune **per-trigger-class confidence** so annoying triggers
decay and helpful ones gain. Everything is **governed per scope** via `scope_settings`
(enabled, threshold, budget, trigger classes, opt-out) — admin-set, runtime-changeable,
a tenant can turn it **off entirely**. Ships on {SDK, HTTP, MCP} (D-067). **No new
schema** — both tables are day-one (§8.1); one index-only migration. Temporal
pattern-mining is the explicit **deferred stretch** (D-087).

## Brief findings incorporated

- **06 (mempalace):** the holder-offers-context thesis + temporal positioning — the
  recent-episode and similar-episode triggers are the §6b episodic layer turned outward.
- **05 (online adaptation):** accept/dismiss is the online signal; per-trigger-class
  confidence tuning is the bounded, deterministic adaptation (no opaque ML in v1).
- **02 (ccmem):** reuse the proven scoring/decay machinery for candidate ranking — a
  proactive offer is just a retrieval candidate gated harder (higher threshold, budget).

## Findings I'm departing from / decided (D-087)

- **"Six-counter machinery on a `suggestion` record" (RFC §6d) is realized as per-
  trigger-class accept/dismiss tuning, not six counters per suggestion.** The day-one
  `suggestions` table carries `accept_count`/`dismiss_count` + `status`, not the six
  memory counters. The faithful reading: a suggestion's *feedback* (accept/dismiss)
  rolls up **per `(scope, trigger_kind)`** into a confidence multiplier that biases that
  class's future candidate scores — "triggers that annoy decay; triggers that help gain
  stability." This is the spirit of §6d (feedback-tuned, nothing static) without
  shoehorning six columns the schema doesn't have. Documented in D-087.
- **Pull model, not push.** Stowage does not push on session-start (it owns no session
  lifecycle — Harbor does); the agent calls `GET /v1/suggestions` at session start /
  before a turn. RFC §6d "on session start" is satisfied by the agent's call. The
  endpoint both *evaluates* (creating pending rows) and *returns* the surviving offers.
- **Governance is scope-settings JSON, profile-defaulted.** A single `proactive`
  scope-settings key holds `{enabled, threshold, budget, classes[]}`; the effective
  config = the profile default (`config.ProactiveConfigForProfile`, profile-internal —
  NOT a top-level knob, D-034) overlaid by the scope's override (scope wins; opt-out =
  `enabled:false`). Resolution reads the most-specific scope row present (user, then
  tenant). Admin role writes governance; the agent role reads suggestions.
- **Pattern-mining deferred (stretch).** The recurring-routine miner is out of Phase 27
  (it needs time-series frequency analysis + an automation surface); recorded as a
  deferred stretch in D-087, gated behind the same governance + feedback loop when pulled.

## Design

### Store seams (no new schema; one index)

`SuggestionStore` (both drivers + conformance):
```go
Create(ctx, scope, []Suggestion) error          // pending offers (idempotent on id)
ListBySession(ctx, scope, sessionID, status string, limit int) ([]Suggestion, error)
Get(ctx, scope, id string) (*Suggestion, error)
Resolve(ctx, scope, id, action string, now int64) (*Suggestion, error) // accept|dismiss → status + counter (CAS on status='pending')
CountByTrigger(ctx, scope, triggerKind string) (accepted, dismissed int, err error) // for tuning
ListPendingBefore(ctx, scope identity.Scope, before int64, limit int) ([]Suggestion, error) // expiry sweep
ExpirePending(ctx, scope, ids []string, now int64) error
```
`ScopeSettingsStore` (both drivers + conformance):
```go
Get(ctx, scope, key string) (value string, found bool, err error)  // most-specific scope row
Set(ctx, scope, key, value string, now int64) error                 // upsert (UNIQUE scope+key)
List(ctx, scope) (map[string]string, error)
Delete(ctx, scope, key string) error
```
New migration `0010_suggestions_index.sql` (both drivers, index-only): `idx_suggestions_pending ON suggestions(tenant_id, user_id, session_id, status)` for `ListBySession`/expiry scans. `Resolve` uses a CAS (`UPDATE … WHERE id=? AND status='pending'`) — 0 rows ⇒ `ErrNotPending` (the checkpoint D-085 lesson, applied fresh here).

### Governance — `internal/proactive/governance.go`

```go
type Config struct {
    Enabled   bool
    Threshold float64        // min final score to surface (default conservative)
    Budget    int            // max offers per request ("strict per-turn budget")
    Classes   map[string]bool // trigger classes enabled: recent_episode | similar_episode | expiring
}
// Resolve merges the profile default with the scope's "proactive" scope-settings JSON
// (scope override wins; absent ⇒ profile default). Opt-out ⇒ Enabled=false.
func Resolve(ctx, ss store.ScopeSettingsStore, scope, profileDefault Config) (Config, error)
```
`config.ProactiveConfigForProfile(profile)` (profile-internal, D-034): assistant/fleet
enabled with a high threshold + small budget (silence over spam); coding-agent off by
default. The eval re-tunes; a tenant overrides at runtime via scope settings.

### Trigger engine — `internal/proactive/engine.go` + `rules.go`

```go
type Candidate struct {
    TriggerKind string  // recent_episode | similar_episode | expiring
    MemoryID    string  // the offered memory (narrative for episode triggers)
    EpisodeID   string  // set for episode triggers
    Relevance   float64 // pre-utility relevance (similarity, recency, or urgency in [0,1])
    Title       string  // human-facing offer label ("the Q2 plan from last quarter")
}

// Evaluate runs the enabled trigger rules, scores candidates with scoring.Score,
// applies the per-trigger-class confidence multiplier (feedback tuning), filters by
// threshold, dedupes against the session's existing suggestions, ranks, and trims to
// budget. Persists survivors as pending rows. Degraded-safe: a gateway-dependent rule
// (similar_episode) that can't run is skipped (D-036), the others still fire.
func Evaluate(ctx, st store.Store, searcher NarrativeSearcher, scope, sessionID, query string, cfg Config, now int64) ([]Offer, bool, error)
```
Rules (each gated by `cfg.Classes`):
- **recent_episode** (gateway-free): `episodes.List` most-recent in scope; offer the
  narrative if the episode ended within a recency window and the session is new (no
  prior suggestion of it). `Relevance` from recency.
- **similar_episode** (gateway, degraded-safe): when `query != ""`, `episodes.Similar`
  (via `SimilarNarratives`) → offer the top matching past episode's narrative;
  `Relevance` = similarity. Gateway down ⇒ class skipped, `degraded=true`.
- **expiring** (gateway-free): memories with `0 < valid_until ≤ now+window` (a new store
  query `ListExpiring`, or reuse the decay scan) — offer to reaffirm before they lapse;
  `Relevance` from urgency (closeness to expiry).

Each candidate's offered memory facts → `scoring.Score` (utility/decay/trust/importance
reused) × the trigger-class confidence multiplier; keep `final ≥ cfg.Threshold`; sort
desc; cap at `cfg.Budget`. Dedupe: skip a memory/episode already offered to this session
(`ListBySession` any-status).

### Feedback tuning — `internal/proactive/tuning.go`

`classMultiplier(accepted, dismissed int) float64` — a bounded, deterministic function
in `[min,1]`: high dismiss-rate suppresses the class (multiplier → min so its candidates
fall below threshold); accepts restore it. Computed per `(scope, trigger_kind)` from
`CountByTrigger`. Nothing static; no opaque ML. A class a tenant finds annoying decays
itself out without being turned off.

### Surfaces (D-067)

- **`memory_suggestions`** (single-user tier {SDK, HTTP, MCP}): `GET /v1/suggestions?session_id=&query=`
  → `{offers:[{id, trigger_kind, memory_id, episode_id, title, content, score}], degraded}`;
  `POST /v1/suggestions/{id}` `{action: accept|dismiss}` → `{id, status}`. MCP
  `memory_suggestions` `{action: list|accept|dismiss, session_id?, query?, id?}`; SDK
  `Suggestions` + `ResolveSuggestion`.
- **Proactive governance** (admin tier {HTTP, MCP}, D-067 team/admin): `GET /v1/admin/proactive`
  + `PUT /v1/admin/proactive` read/write the scope's `proactive` governance JSON via
  `ScopeSettingsStore`; MCP `memory_proactive_config`. Agent role cannot change
  governance; admin can (and can opt the tenant out).

Tool count 18 → 20 (`memory_suggestions`, `memory_proactive_config`); schema goldens
added. The evaluate path needs the gateway+retriever (for similar_episode) on the
surfaces (they already hold the retriever/SimilarNarratives via the stack).

### Lifecycle (P4) — suggestion expiry sweep

A gateway-free `runExpireSuggestions` sweep (sibling to the lifecycle sweeps,
advisory-locked, jittered, profile-gated by proactive-enabled): `pending` suggestions
older than a TTL → `expired` (`ExpirePending`). Bounded batch. Reversible-by-nature
(status transition, audited via an event). Accept/dismiss are the live path; expiry GCs
stale offers so the queue doesn't grow unbounded.

### Day-one signals / no new schema

`suggestions` + `scope_settings` are day-one (§8.1); Phase 27 wires the seams + one
index-only migration. Suggestion status transitions emit `suggestion.offered` /
`suggestion.accepted` / `suggestion.dismissed` / `suggestion.expired` events (audit
trail, §8).

## Config keys added

**None top-level.** Proactive governance lives in `scope_settings` (runtime, per-scope);
profile defaults are profile-internal (`config.ProactiveConfigForProfile`), re-tuned by
eval (D-034 ceremony N/A — not an operator env knob). Zero-config start unaffected.

## Acceptance criteria (binding)

1. **Trigger engine + thresholds (scoring reuse):** `proactive.Evaluate` runs the
   enabled rules, scores candidates via `scoring.Score`, applies the per-class tuning
   multiplier, surfaces only `≥ threshold` within `budget`, dedupes per session, and
   persists pending offers. similar_episode degrades (skipped, `degraded=true`) when the
   gateway is down (D-036); recent_episode + expiring still fire. Gateway-free except the
   similar_episode embed.
2. **Governance (scope-settings, runtime, opt-out):** the effective config = profile
   default overlaid by the scope's `proactive` setting; `enabled:false` ⇒ zero offers;
   threshold/budget/classes are honored. Admin writes governance; agent cannot.
3. **Feedback tuning (P4, nothing static):** `accept`/`dismiss` resolve the suggestion
   (CAS on `pending`, counter + status + event); a trigger class with a high dismiss rate
   is suppressed below threshold via the per-class multiplier; accepts restore it.
   Resolving a non-pending suggestion ⇒ `ErrNotPending`.
4. **Tiered parity (D-067):** `memory_suggestions` (deterministic legs) + the governance
   read/write are byte-identical across their tiers; missing/absent ⇒ empty, no error.
5. **Scope (P3):** suggestions, scope-settings, and evaluation are scope-enforced in the
   store layer; cross-tenant/user offers never surface (tests). Conformance on both drivers.
6. **Lifecycle:** the expiry sweep moves stale `pending` → `expired` (bounded, advisory-
   locked, gateway-free), proven idempotent.
7. **Schema/tool discipline:** no new table/column (one index-only migration); profile-
   internal governance (no top-level knob); two new MCP tools ⇒ goldens + tool-count +
   same-PR smoke (§4.2).
8. **Gates:** build, `go test -race ./...`, golangci-lint, gofmt, coverage (incl. the new
   package + raised store/conformance), preflight, drift-audit, mirror green.

## Smoke script

`scripts/smoke/phase-27.sh`: SuggestionStore + ScopeSettingsStore on both drivers +
conformance; `proactive.Evaluate` present + scoring-reuse + degraded-safe; governance
resolve (profile ⊕ scope, opt-out); per-class tuning suppresses a dismissed class;
`memory_suggestions` + `memory_proactive_config` on all surfaces (+ goldens, count 20);
expiry sweep present + gateway-free; unit + parity + conformance tests; eval-ci green.

## Test plan

- **Unit — engine/rules:** each rule fires on its input + not otherwise; threshold +
  budget filter; dedupe per session; degraded similar_episode; scoring reuse.
- **Unit — governance:** profile default ⊕ scope override precedence; opt-out ⇒ silence;
  admin-only write.
- **Unit — tuning:** dismissed class suppressed below threshold; accepted class restored;
  bounded multiplier.
- **Conformance:** SuggestionStore (create/list/get/resolve-CAS/count/expire, scope
  isolation, not-pending) + ScopeSettingsStore (get most-specific/set-upsert/list/delete,
  scope isolation) on sqlite + pgx.
- **Lifecycle integration:** real store — offer → expiry sweep → expired; idempotent.
- **Parity (§17):** `memory_suggestions` list + resolve + governance read/write byte-
  identical across surfaces (deterministic: recent_episode + expiring, no gateway).
- **Fuzz:** the governance-JSON decode (`FuzzProactiveConfig`) — arbitrary bytes never
  panic, always yield a valid clamped Config or a clean error.

## Risks & mitigations

- **Spam / annoyance (the core UX risk).** → high default threshold + small budget +
  "silence over spam" + per-class feedback suppression + tenant opt-out; offers are
  pull-only (agent decides when to ask). Defaults are conservative; eval tunes them.
- **Re-offering the same context every turn.** → per-session dedupe against existing
  suggestions (any status); expiry GCs the backlog.
- **Gateway dependence of similar_episode.** → degraded-safe; the two gateway-free rules
  carry the feature when the provider is down (D-036).
- **Governance write is powerful.** → admin-role-gated; scope-scoped; audited via events.
- **Unbounded suggestion growth.** → expiry sweep + bounded per-request budget.

## Glossary additions

- **Proactive suggestion (offer)** — a context offer the trigger engine surfaces for a
  session (a recent/similar episode or an expiring memory), scored + threshold-gated,
  accept/dismiss-tuned; a `suggestions` row (Phase 27, §6d, D-087).
- **Trigger class** — a proactive rule category (`recent_episode`, `similar_episode`,
  `expiring`) whose confidence is tuned per scope by accept/dismiss feedback (Phase 27).
- **Proactive governance** — the per-scope `scope_settings` config (enabled, threshold,
  budget, classes) controlling proactivity; admin-set, runtime, opt-out-able (D-087).

## Deviations during implementation (kept in sync with the code, §4.3)

1. **Resolve event emission lives in a `proactive.ResolveOffer` core, not the store.**
   The plan listed `suggestion.accepted`/`suggestion.dismissed` as emitted events; to
   keep them un-omittable across surfaces (D-067), the accept/dismiss CAS + its event
   emission are wrapped in `proactive.ResolveOffer(ctx, st, scope, id, action, now)`,
   which all three surfaces (HTTP/MCP/SDK) call instead of `Store.Suggestions().Resolve`
   directly. `suggestion.offered` is emitted by `Evaluate` (the engine core);
   `suggestion.expired` by the lifecycle sweep. All four §6d lifecycle events ship.
2. **Expiry sweep does one bounded page per run** (no inner pagination loop): if more
   than the batch is stale, the next 15-minute sweep drains the rest — the D-057
   "bounded per sweep" pattern, fewer branches.
3. **Expiry sweep is registered whenever `SuggestExpireInterval > 0`** (not gated on
   proactive being enabled): it is gateway-free and cheap, and a tenant that *disabled*
   proactive may still hold stale `pending` offers from before — GC must not depend on
   the feature staying on. Tenant discovery is `memories ∪ records` (a suggestion always
   references a memory in its tenant).

## Decisions filed

- **D-087** — Phase 27 implements D-028. Proactive offers are produced by a trigger
  engine (recent_episode + similar_episode + expiring) reusing `scoring.Score` under a
  per-scope governance threshold + budget (scope_settings JSON, profile-defaulted,
  runtime, opt-out-able); accept/dismiss tune **per-(scope,trigger_class)** confidence
  (the §6d "feedback-tuned, nothing static" realized via the day-one
  `accept_count`/`dismiss_count`, not six per-suggestion counters). Pull model
  (`GET /v1/suggestions`, agent-initiated). `memory_suggestions` is single-user tier;
  governance is admin tier. An expiry sweep GCs stale offers. **No new schema** (one
  index). **Temporal pattern-mining is a deferred stretch.**
