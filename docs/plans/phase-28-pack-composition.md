# Phase 28 — Topic-pack composition + curated packs

- **Status:** approved
- **Owning subsystem(s):** `internal/topics` (resolution core), `internal/pipeline`
  (extract stage consumes it + emits the cap event); read-only ripple to
  `internal/api`, `sdk/stowage`, `internal/mcpserver` (the `source` value).
- **RFC sections:** §5.4 (Topics / extraction magnets — composition paragraph).
- **Depends on phases:** 07 (topics & extraction — the subsystem this extends).
- **Informing briefs:** 03 (Weaviate Engram — topics-as-magnets; the magnet model is
  what composition multiplies), 02 (CC-mem — the 50-knob "config paralysis" tale; why
  composition is driven by scope-state sentinels, not new YAML knobs).

## Goal

When this phase is done, topic packs **compose**: a scope's effective extraction
topics are the deduped union of the packs it has enabled and its explicit topics
(explicit wins on key collision), bounded by a `maxActiveTopics` cap. A scope enables
an extra compiled-in pack with a `pack:on:<name>` sentinel topic (mirroring `pack:off`),
so e.g. a project scope can run personalization + a project pack + a few bespoke topics
at once. `profile` selects an *ordered list* of default packs that apply only when the
scope expressed no intent (zero-config path preserved). Six curated packs ship beyond
the existing two: `pack:project`, `pack:incidents`, `pack:product`, `pack:people`,
`pack:compliance`, `pack:research`. This realizes D-099 (which amended D-043).

## Brief findings incorporated

- **03 (Engram): topics are natural-language magnets; more well-aimed magnets = more
  captured signal.** Composition lets a scope stack several magnet sets without losing
  the topic gate's precision (each pack stays tight and non-overlapping).
- **02 (CC-mem): config paralysis kills adoption.** Composition adds **zero YAML knobs** —
  packs are enabled per-scope through the existing topics API/SDK/MCP via a `pack:on:`
  sentinel (the `pack:off` precedent). The only profile lever is unchanged.

## Findings I'm departing from

- **None of substance.** This is the implementation of the already-ratified D-099.
- **One implementation refinement (documented, not a decision change):** D-099 says the
  `source` field "becomes `pack:<name>`". The `TopicView` wire type already carries a
  separate `pack` field; to honor D-099 literally `source` is set to the pack name for
  pack-sourced entries (`explicit` otherwise), and the pre-existing `pack` field is
  **retained** (equal to `source` for pack entries) so GET/SDK/MCP consumers are not
  wire-broken. No behavior change beyond the `source` string.

## Design

### Resolution core (`internal/topics`)

`packs.go`:
- New pack-name constants: `PackProject`, `PackIncidents`, `PackProduct`, `PackPeople`,
  `PackCompliance`, `PackResearch` (alongside `PackPreferences`, `PackAgentLearnings`,
  `PackOff`). New sentinel prefix const `packOnPrefix = "pack:on:"`.
- A compiled-in `packRegistry map[string][]packEntry` keyed by pack name; each new pack
  gets a tight, non-overlapping entry slice (keys + descriptions; see below).
- `defaultPacksForProfile(profile string) []string` replaces `defaultPackForProfile`:
  returns an **ordered list** (`assistant → [pack:preferences]`,
  `coding-agent`/`fleet → [pack:agent-learnings]`).
- `packNameFromOnSentinel(key string) (name string, ok bool)`: maps `pack:on:<short>` →
  `pack:<short>` iff `<short>` is non-empty and resolves to a registered pack; unknown
  packs return `ok=false` (caller logs+ignores).
- `const MaxActiveTopics = 32` — the composition cap (exported so the extract event can
  report it). An internal recall/cost guardrail like the D-090 cosine floor — **not a knob**.

`topics.go`:
- New `Resolution{ Topics []TopicView; DroppedKeys []string }`.
- New `Resolve(ctx, scope) (Resolution, error)` holds the full algorithm:
  1. `List` active topics; partition into: `pack:off` present?, enabled pack names (from
     `pack:on:` sentinels, in store order, deduped, unknown logged+skipped), explicit
     topics (every other active, non-sentinel topic).
  2. `pack:off` present → return empty `Resolution` (opt out; caller short-circuits).
  3. If no explicit topics **and** no enabled packs → `enabled = defaultPacksForProfile`.
  4. Build the union: explicit topics first (`source="explicit"`), then each enabled
     pack's entries in order, **skipping keys already seen** (explicit wins; among packs
     first-enabled wins); pack entries get `source = pack name`, `pack = pack name`.
  5. Cap: if `len > MaxActiveTopics`, keep the first `MaxActiveTopics` (explicit are first,
     so they're never dropped; packs drop by enable order) and record the dropped keys in
     `DroppedKeys`; log a WARN. Never silent.
- `ActiveTopics(ctx, scope) ([]TopicView, error)` becomes a thin wrapper: `Resolve(...).Topics`
  — every existing caller (GET /v1/topics, SDK, MCP) is unchanged and emits no event.

### Extract stage (`internal/pipeline/extract.go`)

- Call `Resolve` instead of `ActiveTopics`; use `.Topics` for the prompt as today.
- When `len(.DroppedKeys) > 0`, emit `extraction.topics_capped` via the existing
  `emitEvent` helper: `reason="max_active_topics"`, payload `{buffer_key, cap:
  MaxActiveTopics, dropped_count, dropped_keys}`. Read paths never emit (the cap event is
  pipeline-only). Empty topic set (pack:off or genuinely none) keeps the existing
  `extraction.skipped`/short-circuit behavior.

### The six new packs (compiled-in entries; descriptions are extraction-prompt text)

- **pack:project** — project-glossary, ownership-and-contacts, environments-and-endpoints,
  runbooks-and-procedures, project-conventions, design-rationale.
- **pack:incidents** — incidents-and-outages, root-causes, postmortem-lessons,
  oncall-footguns, mitigations-and-workarounds.
- **pack:product** — product-decisions, requirement-rationale, user-research-findings,
  roadmap-rationale, success-metrics.
- **pack:people** — team-members-and-roles, stakeholders, working-relationships,
  expertise-and-contacts.
- **pack:compliance** — hard-rules-and-prohibitions, approval-requirements,
  data-handling-and-redaction, regulatory-obligations.
- **pack:research** — sources-and-references, claims-and-findings, open-questions,
  assumptions.

### Concurrency / fidelity posture

`topics.Service` stays immutable after construction; `Resolve` is read-only over the
store + compiled-in constants (safe under concurrent use). No schema change, no new
config, no gateway calls in `topics`. Lifecycle of pack/composed memories is unchanged
from Phase 07 (topics gate extraction; deleting an explicit topic still expires its
memories; packs are virtual and never persisted).

## Files added or changed

```text
internal/topics/packs.go                  # 6 new packs + registry + defaultPacksForProfile(list) + pack:on + MaxActiveTopics
internal/topics/topics.go                 # Resolution + Resolve(); ActiveTopics → wrapper; source=pack:<name>
internal/topics/topics_test.go            # composition unit tests (union/dedup/pack:on/multi-default/pack:off/cap)
internal/topics/packs_test.go             # registry + defaultPacksForProfile + packNameFromOnSentinel tests
internal/pipeline/extract.go              # Resolve(); emit extraction.topics_capped
internal/pipeline/extract_test.go         # composed-prompt golden + topics_capped event test
internal/pipeline/testdata/extract_prompt_compose.golden  # explicit + pack union prompt
internal/api/topics_handler_test.go       # source=pack:<name> assertion update (if asserted)
scripts/smoke/phase-28.sh                 # pack:on composition smoke
docs/plans/phase-28-pack-composition.md   # this plan
docs/glossary.md                          # already has the D-099 terms (no change expected)
```

## Config keys added

None. (D-034 / D-099: composition is scope-state via the `pack:on:` sentinel; the cap is
a package constant, not a knob.)

| Key | Default | Notes |
|-----|---------|-------|
| — | — | none |

## Acceptance criteria (binding)

1. **Composition union.** A scope with `pack:on:project` + two explicit topics resolves to
   the deduped union (explicit first, then the project pack's entries), with explicit
   winning any key collision. Proven by unit test.
2. **`pack:on:<name>` enables a pack** for the scope; an unknown `pack:on:bogus` is logged
   and ignored (not treated as an explicit topic, not an error).
3. **`profile` → ordered default-pack list**, applied **only** when the scope has no
   explicit topics and no enabled packs; once either is present the default packs are not
   auto-added (operator-in-control, the D-043 spirit). `pack:off` still dominates and
   short-circuits with no gateway call.
4. **Cap, never silent.** When the union exceeds `MaxActiveTopics`, explicit topics are
   retained, pack entries drop by enable order, `DroppedKeys` is populated, a WARN is
   logged, and the extract stage emits `extraction.topics_capped`.
5. **`source` reports origin** (`explicit` | `pack:<name>`) on GET /v1/topics, SDK, MCP —
   the `pack` field is retained.
6. **All six new packs are registered and resolvable** via `pack:on:<name>`; their entries
   render into the extraction prompt (golden).
7. **One-core (D-067):** SDK/HTTP/MCP all reflect composition through
   `topics.Service`; no surface re-implements resolution. `make preflight`,
   `go test -race ./...`, `golangci-lint`, and `make coverage` (touched packages) green.

## Smoke script

`scripts/smoke/phase-28.sh`:
- `pack:on:project` upsert + `GET /v1/topics` lists the project pack entries with
  `source: pack:project` alongside any explicit topics (composition visible).
- `pack:off` upsert → `GET /v1/topics` returns empty (opt-out still dominates).
- assert the new pack names are known (a `pack:on:<name>` for each resolves to entries).

## Test plan

- **Unit (`internal/topics`):** Resolve union + explicit-wins dedup; `pack:on` single +
  multi-pack; default-packs-only-when-empty; `pack:off` dominance over `pack:on`; the cap
  (enable all packs → exceed `MaxActiveTopics` → deterministic drop of pack entries,
  `DroppedKeys` populated, explicit retained); unknown-pack `pack:on:bogus` ignored;
  registry completeness (every pack name resolves; entries non-empty, keys unique within a
  pack). `source`/`pack` values asserted.
- **Golden (`internal/pipeline`):** existing pack/explicit goldens unchanged (prompt render
  is Key+Description, unaffected by `source`); new `extract_prompt_compose.golden` locks an
  explicit-plus-pack composed prompt; `extraction.topics_capped` event test.
- **Parity:** existing API/SDK/MCP topics tests still pass with the `source` value change;
  one assertion updated to `pack:<name>`.
- **No fuzz/bench** (no new parse/hot-reuse surface). Coverage: `internal/topics` keeps its
  band; `internal/pipeline` keeps its band.

## Risks & mitigations

- **Leaky gate from too many vague topics.** Mitigation: each new pack is tight and
  non-overlapping; the `MaxActiveTopics` cap bounds the worst case; descriptions are
  prompt-reviewed and golden-locked.
- **Cost/latency from large composed sets.** Mitigation: the cap + the existing per-profile
  prompt token clamp; the cap drops packs before explicit topics and is evented.
- **Back-compat of the `source` change.** Mitigation: `pack` field retained; only the
  `source` string widens from `pack` to `pack:<name>` — a superset consumers can switch on
  by prefix; tests updated in the same PR.

## Glossary additions

- None new — the D-099 PR already added **Pack**, **Pack composition**, **`pack:on:<name>`**,
  **`pack:off`**. (This phase adds the `extraction.topics_capped` event type, noted in the
  event contract, not the glossary.)

## Decisions filed

- None new — implements the already-filed **D-099** (which amended **D-043**). Any
  implementation refinement (the `source`/`pack` field handling above) is documented here
  and in the PR, not a new decision.
