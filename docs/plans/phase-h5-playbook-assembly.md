# Phase h5 — Deterministic playbook assembly (finish the stubbed primitive, all surfaces)

- **Status:** draft | approved | in-progress | shipped
- **Owning subsystem(s):** `internal/playbook` (new, LLM-free), `internal/store` (a scoped list-by-kinds seam method), `internal/api`, `internal/mcpserver`, `sdk/stowage`
- **RFC sections:** §6a.3 (deterministic playbook assembly — no LLM), §9.1/§9.2/§9.3 (surfaces), §4.2 (scoring reuse)
- **Depends on phases:** 08 reconcile (strategy/failure_mode kinds), 10 scoring (pure utility/decay fns), 16/17 surfaces — all shipped
- **Informing briefs:** 05 (ACE: itemized playbook, delta-only evolution, context-collapse defense, append-bias for KV-cache), 02 (utility-counter ranking)
- **Program:** D-067 Wave C (finish half-shipped primitives — owner posture: finish, make worthy, accommodate {SDK,MCP,HTTP} from the get-go). Pre-reserved decision: **D-072**.

## Goal

When this phase is done, `GET /v1/playbook`, the SDK `Client.Playbook`, and the MCP
`memory_playbook` tool all return a **real** deterministic, sectioned,
utility-ranked, budget-packed playbook with provenance — identical on all three
surfaces — instead of the `Stub:true` placeholder shipped in Phase 17. The
assembly is **LLM-free** (RFC §6a.3, CLAUDE.md §6 — `internal/playbook` never
imports the gateway; the existing lint test stays green). This finishes the one
genuinely half-shipped launch primitive and removes the last stub from the public
surface (`ErrPlaybookStub` is deleted).

## Brief findings incorporated

- **Brief 05 (ACE):** the playbook is a *deterministic view over itemized
  memories*, not a monolithic LLM rewrite — monolithic rewrites cause context
  collapse; evolution happens only via delta reconciliation (which already runs).
  Output must be **stable + append-biased** so host-side prompt caching stays warm
  (ACE's 91.8% KV-hit property). → assembly orders deterministically and appends.
- **Brief 02:** rank within a section by the six utility counters + decay (pure
  `internal/scoring` functions), the same signals retrieval already trusts.

## Findings I'm departing from

- None. **Scope boundary (not a departure — explicit):** §6a pieces 1–2
  (outcome-aware *reflection extraction* + the multi-epoch re-reflection sweep —
  the LLM **write** side that *produces* `strategy`/`failure_mode` memories) are
  roadmap **Phase 19**, OUT of scope here. The assembly is useful without them: it
  views whatever `strategy`/`failure_mode` (and other building-block) memories
  already exist — they arrive today via topic extraction kinds and `memory_assert`.
  This phase builds only the **read/assembly** path.

## Design

### Core: `internal/playbook` (LLM-free)
```go
// Assemble renders a deterministic, sectioned, budget-packed playbook for a scope.
// NO gateway call (CLAUDE.md §6). Pure function of stored memories + scoring.
func Assemble(ctx context.Context, st store.Store, scope identity.Scope, opts Options) (*Playbook, error)

type Options struct {
    SessionID  string // optional filter/affinity
    TopicKeys  []string // optional restriction
    TokenBudget int     // default from profile (see Config)
}
type Playbook struct {
    Sections    []Section // ordered: strategy, failure_mode, then other building blocks
    Budget      BudgetInfo
}
type Section struct{ Title, Kind string; Items []Item }
type Item struct{ MemoryID, Content string; Score float64; Provenance []store.ProvenanceRef }
```
Algorithm (deterministic): (1) list active memories in scope of the playbook kinds
(`strategy`,`failure_mode`, then `decision`,`gotcha`,`pattern` — the §6a building
blocks) via the new store seam method below; (2) group into sections by kind, then
by topic; (3) rank within each section by `scoring.Score`/`DecayFactor` (pure, no
gateway), with a stable tiebreak on memory ULID so ordering is reproducible;
(4) budget-pack to `TokenBudget` (greedy by score, estimate via the existing token
estimator); (5) attach provenance refs (P1 drill-down). Output ordering is
**append-biased**: higher-stability items first, new items appended — so a host
re-fetching the playbook gets a prefix-stable string (KV-cache warm).

### Store seam (the one new persistence concern)
The assembly ranks by utility counters, not query relevance, so it is a **store
view**, not a retrieval query. Add ONE scoped method to the `Store`/`Memories`
seam — `ListByKinds(ctx, scope, kinds []string, opts) ([]Memory, error)` (active
only, scope-enforced — P3) — implemented by **both** drivers and proven by the
shared **conformance suite** (CLAUDE.md §9). No unscoped variant.

### Surfaces (single-user read tier → {SDK, MCP, HTTP}, from the get-go)
- **HTTP:** `GET /v1/playbook` (new route + handler) → `playbook.Assemble`.
- **SDK:** `Client.Playbook` real impl — embedded → `playbook.Assemble(stack…)`;
  http → `GET /v1/playbook`. Delete `ErrPlaybookStub`. Replace the stub
  `PlaybookResponse{Entries []any; Stub bool}` with the typed sectioned response
  (breaking change to a type that only ever returned `Stub:true` — noted).
- **MCP:** `memory_playbook` real handler + typed contract + schema golden
  (replaces the "not implemented" stub).

### Config (D-034 knob guardrail)
Playbook token budget is **profile-internal** (`assistant`/`coding-agent`/`fleet`
constants in `internal/config/profiles.go`, like buffer triggers D-042) — NOT a new
top-level config knob. Default chosen per profile; documented in the plan + profile
docs. Zero new operator knobs.

## Files added or changed

```text
internal/playbook/playbook.go + assemble.go    # NEW — LLM-free assembly (no gateway import)
internal/playbook/playbook_test.go             # golden + budget + append-bias tests
internal/store/store.go + pgstore + sqlitestore + conformance  # ListByKinds on the seam, both drivers + conformance
internal/config/profiles.go                    # playbook budget per profile
internal/api/server.go + playbook_handler.go   # GET /v1/playbook
internal/mcpserver/server.go + handlers.go + contracts.go + testdata  # real memory_playbook + golden
sdk/stowage/client.go (doc) + embedded.go + http.go + types.go  # real Playbook; delete ErrPlaybookStub; typed response
sdk/stowage/suite_test.go                       # Playbook parity on both impls
scripts/smoke/phase-h5.sh                       # NEW
test/integration/playbook_parity_test.go        # NEW — identical playbook across {SDK,MCP,HTTP}
docs/plans/README.md ; docs/glossary.md
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| (none top-level) | — | Playbook budget is profile-internal (D-042 pattern); no new operator knob (D-034). |

## Acceptance criteria (binding)

1. `internal/playbook` assembles a deterministic sectioned/ranked/budget-packed
   playbook with provenance; it imports NO gateway (the existing §6 LLM-free lint
   test passes; add an assertion if one isn't already covering `internal/playbook`).
2. Same scope + same memories ⇒ **byte-identical** playbook output (golden test);
   ordering is append-biased/prefix-stable when a new lower-ranked item is added.
3. `ListByKinds` is on the `Store` seam, scope-enforced (no unscoped variant),
   implemented by both drivers and covered by the shared conformance suite.
4. `GET /v1/playbook`, `Client.Playbook` (embedded + http), and MCP
   `memory_playbook` return the playbook; the `Stub` field and `ErrPlaybookStub`
   are gone. New endpoint + MCP tool + SDK method each have a smoke check + (MCP)
   schema golden in this PR (§4.2/§13).
4b. Budget packing never exceeds `TokenBudget`; an empty scope returns an empty
   (non-error) playbook.
5. **All-surfaces-identical bar:** `test/integration/playbook_parity_test.go`
   asserts the assembled playbook is identical across {SDK embedded, HTTP, MCP} for
   the same scope/memories, on real sqlite, `-race`.

## Smoke script

`scripts/smoke/phase-h5.sh` — build; seed a few `strategy`/`failure_mode` memories
(via `memory_assert`); fetch the playbook via `GET /v1/playbook`, the embedded SDK,
and `stowage mcp` (stdio); assert each returns the same non-empty sectioned
playbook with provenance and `Stub` absent. SKIP-graceful pre-build.

## Test plan

- **Golden:** assembly output for a fixed memory set (determinism + sections +
  budget + append-bias).
- **Conformance:** `ListByKinds` on both drivers (scope isolation, active-only).
- **Integration (§17):** playbook parity across all three surfaces, real sqlite, `-race`.
- **Unit:** budget packer (never exceeds, greedy-by-score, stable tiebreak); empty scope.
- **Lint:** §6 no-gateway-import assertion covers `internal/playbook`.

## Risks & mitigations

- *Risk:* assembly couples to retrieval/scoring internals. *Mitigation:* depend only
  on pure `internal/scoring` functions + the store seam; no gateway, no retriever.
- *Risk:* breaking the stub `PlaybookResponse` shape breaks a consumer. *Mitigation:*
  it only ever returned `Stub:true`; no real consumer exists; noted in D-072.
- *Risk:* token-budget knob sprawl. *Mitigation:* profile-internal constants, no new
  operator knob (D-034).

## Glossary additions

- **Playbook assembly** — the deterministic, LLM-free, sectioned + utility-ranked +
  budget-packed view over a scope's `strategy`/`failure_mode`/building-block
  memories, append-biased for host prompt-cache warmth (RFC §6a.3).

## Decisions filed

- **D-072** — the deterministic playbook assembly (`internal/playbook`, LLM-free) is
  finished and reachable identically on {SDK, MCP, HTTP}; `GET /v1/playbook` + a new
  `Store.ListByKinds` seam method (both drivers + conformance); playbook budget is
  profile-internal; the `Stub`/`ErrPlaybookStub` placeholders are removed. Reflection
  (§6a.1-2, the LLM write-side) remains roadmap Phase 19.

## Scope boundaries (explicit — what this phase does NOT do)

- **Reflection extraction + re-reflection sweep** (§6a.1-2): roadmap Phase 19.
- **DSAR cascade** (`handleDSARStub`, 501): a deliberately reserved surface for
  Phase 21 retention — left as-is.
- **Grants `RedactionProfile` application**: a stored-but-unapplied field for a later
  phase — left as-is.
- **Runtime API-key management — RESOLVED (owner, 2026-06-16): HTTP-only by
  design.** Keys are fully shipped on HTTP (D-030); they are deliberately NOT
  exposed on MCP. This is a **tier exception**, recorded so it is a conscious
  choice, not drift: runtime key/credential management is an HTTP-admin-only
  concern (operators provision keys through the HTTP admin surface), distinct from
  *grants/team-sharing* management which remains {HTTP, MCP} (D-071). So the D-067
  tiered model now reads: single-user → {SDK, MCP, HTTP}; team/grants admin →
  {HTTP, MCP}; **key/credential admin → {HTTP} only**. No h6; Wave C is h5 alone.
  (Recorded in D-072.)
