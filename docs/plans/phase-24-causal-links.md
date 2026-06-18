# Phase 24 — Causal links (inferred `caused_by`/`led_to`, "why" traversal)

- **Status:** approved
- **Owning subsystem(s):** `internal/causal` (new: inference + traversal cores),
  `internal/lifecycle` (narration sweep hooks inference), `internal/store`
  (one reverse-provenance query method), `internal/api` / `internal/mcpserver` /
  `sdk/stowage` (the `memory_causal` traversal surface), `test/integration`
- **RFC sections:** §5.6 (typed links / causal graph), §6b (causal links: "an
  inference pass proposes `caused_by`/`led_to` edges between decisions connected
  through episode narratives; 'why did X lead to Y' is a graph traversal with
  provenance at every hop"), §9.5/D-067 (one core, thin tiered surfaces),
  §7/§10/P5 (gateway seam), §8.1 (schema budget)
- **Depends on phases:** 22 (episodes + narratives + the narration sweep), 08
  (reconcile link-writing pattern + `links` table), 04 (gateway `Complete`,
  schema-constrained), 09 (retrieval/scope helpers)
- **Informing briefs:** 04 (CL-Bench — causal/temporal reasoning failure modes the
  edges target), 06 (mempalace — narrative positioning), 02 (ccmem — lifecycle/sweep
  discipline)

## Goal

When this phase is done, Stowage **infers causal structure** over episodes and lets
callers **traverse it**. (1) An inference step, run **once per episode at narration
time** (gated by the same `narrative_memory_id` check that makes narration
idempotent), reads the episode's narrative + its decision-class memories and proposes
`led_to` edges between them via a schema-constrained gateway call (P5/D-040),
confidence-gated, written into the existing `links` table with `source="inferred"`
(RFC §5.6). (2) A deterministic, **gateway-free** traversal core walks the
`caused_by`/`led_to` graph from a memory — "why did this happen" (backward to causes)
or "what did this lead to" (forward to effects) — returning the chain with provenance
at every hop, and ships as a new single-user capability `memory_causal` on all three
surfaces (D-067) with a byte-identical parity test. No new table or column: the
`links` table and its `caused_by`/`led_to`/`inferred` enum values are day-one schema
(§8.1, D-024) — **no RFC amendment required**.

## Brief findings incorporated

- **04 (CL-Bench):** current memory systems fail at *temporal/causal* reasoning —
  "what happened and why," not just "what is true." The inferred causal graph +
  why-traversal target exactly that gap; the edges are confidence-scored so weak
  inferences don't pollute retrieval.
- **06 (mempalace):** narrative positioning is the substrate for causality. Inference
  reads the Phase-22 narrative (the "concrete path of decisions"), not raw fragments.
- **02 (ccmem):** sweeps are bounded, supervised, idempotent. Inference rides the
  existing narration sweep's once-per-episode gate rather than introducing a new
  unbounded scan or a new processed-marker column.

## Findings I'm departing from

- **None contradicting a brief.** One design choice settled as **D-083**: causal
  inference is a **sub-step of narration** (atomic with the narrative commit via the
  shared `CommitSet.Links`), not a standalone sweep — this reuses narration's
  once-per-episode idempotency (no new `episodes` column, no RFC §8.1 amendment), at
  the cost of "best-effort, inferred once at narration." Re-inference is a future
  explicit reindex op, never a silent re-run (consistent with the embedding-reindex
  discipline, §10).

## Design

### Candidate gathering — one reverse-provenance store method (P3)

The narration sweep already loads an episode's records to build the narrative. The
episode's **decision-class memories** are those whose provenance points at those
records. One new `MemoryStore` method (scoped, both drivers, conformance-tested):

```go
// ListMemoriesByRecords returns active memories whose provenance references any of
// recordIDs, optionally filtered to kinds (empty = any). Distinct, scope-enforced
// (P3). Reverse of GetJunctions; reusable beyond causal.
ListMemoriesByRecords(ctx context.Context, scope identity.Scope, recordIDs []string, kinds []string) ([]Memory, error)
```

SQL: `provenance JOIN memories` where `provenance.record_id IN (...)`,
`memories.status='active'`, kind filter, the scope predicate (`buildScopeWhere`),
`DISTINCT` on memory id. No unscoped variant.

`decisionKinds = {decision, task, gotcha, pattern, strategy, failure_mode}` — the
causal-actor kinds (facts/preferences/narratives excluded; they are state, not acts).
A named constant in `internal/causal`.

### Inference core — `internal/causal/infer.go` (gateway-seam, schema-constrained)

```go
type Candidate struct { ID, Kind, Content string } // a decision memory, numbered for the prompt

type ProposedLink struct {
    FromIdx, ToIdx int     // indices into the candidate slice (cause → effect)
    Confidence     float64 // 0–1
    Reason         string  // short rationale (not persisted; for the event payload)
}

// Infer proposes led_to (cause→effect) edges among the episode's decisions given its
// narrative. Schema-constrained Complete (P5/D-040; no free-text JSON). Returns the
// raw proposals; the caller confidence-gates + maps indices→memory IDs. Gateway error
// ⇒ error (caller logs + narrates without links — best-effort, D-036 spirit).
func Infer(ctx context.Context, gw gateway.Gateway, narrative string, cands []Candidate) ([]ProposedLink, error)
```

JSON schema: `{links:[{from_idx:int, to_idx:int, confidence:number, reason:string}]}`,
`additionalProperties:false`, all required (mirrors `episodes/narrate.go`). The prompt
gives the narrative + the numbered decisions and asks only for edges *grounded in the
narrative*. Self-edges (`from==to`) and out-of-range indices are dropped by the caller.

### Wiring into narration — `internal/lifecycle/episodes.go`

After the narrate `Complete` produces the narrative text, **before** committing it:

1. `recs` (already loaded) → `recordIDs`.
2. `cands := ListMemoriesByRecords(scope, recordIDs, decisionKinds)`. If `len < 2`,
   skip inference (no edges possible).
3. `proposals := causal.Infer(ctx, gw, narrativeText, cands)`. On error: log, proceed
   with **no** links (narration still succeeds).
4. Gate: keep proposals with `confidence >= cfg.Lifecycle.CausalMinConfidence`, valid
   distinct indices, `from != to`. Map to `store.Link{Type:"led_to", Source:"inferred",
   Confidence:…, FromMemory:cands[from].ID, ToMemory:cands[to].ID}` with fresh ULIDs.
   Dedupe against existing edges for the pair (cheap in-memory set; the pair is new per
   episode in practice).
5. Add the links to the **same `CommitSet`** that commits the narrative memory
   (`CommitSet.Links`) → narrative + inferred edges land atomically, or neither does
   (narration retries next sweep). Emit one `causal.inferred` event per link with its
   reason (audit trail, §8).

Runs exactly once per episode (the `narrative_memory_id`-absent gate). No new marker,
no schema change. Concurrency: the sweep is already advisory-locked + jittered
(Phase-22 posture); inference adds no shared state.

### Traversal core — `internal/causal/traverse.go` (deterministic, gateway-free)

```go
type Direction string // "backward" (causes), "forward" (effects), "both"

type Node struct { MemoryID, Kind, Content, Context string; EpisodeID string; Provenance []ProvRef }
type Edge struct { From, To, Type string; Confidence float64 } // canonical from=cause, to=effect
type Graph struct { Root string; Nodes []Node; Edges []Edge; Truncated bool }

// Traverse walks the causal graph from startID up to depth hops (capped at maxDepth),
// following led_to/caused_by edges, normalizing both types to cause→effect direction.
// Only ACTIVE memories are included (superseded/quarantined/deleted nodes are not
// traversed); provenance is attached per node (P1 drill-down at every hop). Gateway-free.
func Traverse(ctx context.Context, st store.Store, scope identity.Scope, startID string, dir Direction, depth int) (Graph, error)
```

Direction normalization (both link types may exist — reconciler/explicit can write
either): a memory's **causes** (backward) = `{from : led_to(from→X)} ∪ {to : caused_by(X→to)}`;
its **effects** (forward) = `{to : led_to(X→to)} ∪ {from : caused_by(from→X)}`. BFS with
a visited set (cycle-safe), `depth` capped at `maxDepth=10` (constant), `Truncated`
set when the cap or a per-traversal node budget (200) is hit (no silent truncation —
§11). Active-only filter via `Memories().GetMany` status check; non-active nodes are
dropped and their edges not followed.

### Surfaces — `memory_causal` (D-067, one core, three thin callers)

A new single-user read capability, mirroring `memory_episodes`:

- **HTTP:** `GET /v1/causal?memory_id=&direction=&depth=` → `{root, nodes, edges, truncated}`.
- **MCP:** tool `memory_causal`, `CausalInput{MemoryID, Direction, Depth}` →
  `CausalOutput{Root, Nodes, Edges, Truncated}`; schema golden regenerated (D-061).
  Tool count 14 → 15 (update `server_test.go` WANT + `phase-16.sh`).
- **SDK:** `Client.Causal(ctx, CausalRequest{MemoryID, Direction, Depth}) (CausalResponse, error)`
  on both the HTTP and embedded clients (the embedded one calls `causal.Traverse`
  directly with `c.stack.Store`).

All three call `causal.Traverse`. `direction` defaults to `"backward"` (the "why"
question), `depth` defaults to 3. A missing/absent root memory ⇒ empty graph, 200/no
error (parity with `memory_episodes` get-missing). Byte-identical parity test across
the three surfaces over a seeded link graph (deterministic — no gateway in the read).

### Lifecycle of the produced edges (P4)

Inferred links are **derived, advisory, and re-derivable**. Decay/supersede: edges are
not decayed directly; traversal filters to active endpoints, so an edge to a superseded
memory simply stops being traversed (the edge row remains for audit, harmless).
Quarantine: a quarantined endpoint is non-active → not traversed. Hard delete
(retention/DSAR cascade) removes the memory and is responsible for its links (existing
cascade concern; out of scope here — the FK is `REFERENCES memories(id)`). Re-inference
is an explicit future reindex, never silent (D-083).

## Files added or changed

```text
internal/causal/infer.go            # Infer (gateway, schema-constrained) + decisionKinds (+ test)
internal/causal/traverse.go         # Traverse (gateway-free BFS) + types (+ test)
internal/store/store.go             # + ListMemoriesByRecords on MemoryStore
internal/store/sqlitestore/memories.go ; internal/store/pgstore/memories.go  # impl
internal/store/conformance/conformance.go  # ListMemoriesByRecords conformance
internal/lifecycle/episodes.go      # narration sweep: gather decisions → Infer → gate → CommitSet.Links (+ test)
internal/config/*                   # + Lifecycle.CausalMinConfidence (default + profiles + explain)
internal/api/causal_handler.go      # GET /v1/causal (+ test) ; server.go route
internal/mcpserver/{contracts,handlers,server}.go  # memory_causal tool + golden ; tool count 14→15
sdk/stowage/{client,types,http,embedded}.go  # Causal method + types
test/integration/causal_parity_test.go  # all-surfaces traversal parity ; inference integration (mock gw)
scripts/smoke/phase-24.sh
docs/plans/phase-24-causal-links.md ; docs/decisions.md (D-083) ; docs/glossary.md
```

## Config keys added

**None top-level.** `CausalMinConfidence` (default 0.6) is **profile-internal**
(`config.EpisodeConfig`, alongside the episode-sweep intervals), re-tuned by eval
(D-035) — NOT an operator-facing top-level knob, so the D-034 ceremony
(profiles/`explain`/zero-config) does not apply. This is a deliberate deviation from
the original plan, which proposed a top-level `lifecycle.causal_min_confidence`;
keeping it profile-internal is consistent with how episode tuning and the playbook
budget are handled (recorded in D-083). Traversal `depth` is a request parameter
(default 3) with a hard `maxDepth=10` constant — not a knob.

## Acceptance criteria (binding)

1. **Inference (P5/D-040):** narration, after producing a narrative for an episode
   with ≥2 decision-class memories, calls `causal.Infer` via the gateway seam with a
   JSON-schema-constrained `Complete`; proposals below `causal_min_confidence` or with
   invalid/self indices are dropped; survivors are committed as `links` rows
   (`type=led_to`, `source=inferred`) **atomically with the narrative** (one
   `CommitSet`). A gateway failure ⇒ narration still commits (no links), logged.
2. **Once-per-episode:** inference runs only when the episode is first narrated
   (gated by `narrative_memory_id`); a re-run of the sweep does not re-infer or
   duplicate edges. Proven by a test that runs the sweep twice.
3. **Traversal (deterministic, gateway-free):** `causal.Traverse` walks
   `led_to`/`caused_by` from a memory in `backward`/`forward`/`both`, normalizes both
   types to cause→effect, includes only active memories, attaches provenance per node,
   is cycle-safe, and caps depth (`maxDepth`) + node budget with `Truncated` flagged.
   `internal/causal` imports no gateway symbol in `traverse.go`.
4. **Tiered parity (D-067):** `memory_causal` ships on {SDK, HTTP, MCP}; a seeded
   link-graph traversal is byte-identical across the three; missing root ⇒ empty graph,
   no error, on all three.
5. **Scope (P3):** `ListMemoriesByRecords` and traversal are scope-enforced in the
   store layer; cross-tenant records/links never surface (test).
6. **Schema/knob discipline:** no new table/column (links is day-one; one
   index-only migration `idx_provenance_record`); `CausalMinConfidence` is
   profile-internal with a tuned default (not a top-level knob — D-083/D-034). New
   MCP tool ⇒ schema golden + tool-count update + smoke in this PR (§4.2).
7. **Gates:** build, `go test -race ./...`, golangci-lint, gofmt, coverage (incl. the
   new package + raised store/conformance), preflight, drift-audit, mirror green.

## Smoke script

`scripts/smoke/phase-24.sh`: `causal.Infer` present + gateway-schema-constrained;
`traverse.go` gateway-free (no `internal/gateway` import); `ListMemoriesByRecords` on
both drivers; narration wires inference (grep); `memory_causal` on HTTP route + MCP
tool (+ golden) + SDK client; config knob in profiles + `explain`; inference +
traversal unit tests + parity pass; MCP schema goldens stable; tool count = 15;
`make eval-ci` green.

## Test plan

- **Unit — infer:** mock gateway returns scripted proposals; assert confidence gate,
  index validation, self-edge drop, index→ID mapping. Gateway-error path.
- **Unit — traverse:** seeded link graph (chains, a cycle, a branch, a superseded
  node, cross-tenant noise); assert direction normalization, active-only, provenance
  attachment, depth/budget truncation, cycle safety, scope isolation.
- **Conformance:** `ListMemoriesByRecords` on sqlite + pgx (kind filter, distinct,
  scope isolation, empty).
- **Lifecycle integration:** real sqlite + mock gateway — ingest a session, run
  detect+narrate twice, assert inferred `led_to` edges exist once (idempotent), gated
  by confidence, atomic with the narrative.
- **Parity (§17):** `memory_causal` byte-identical across embedded/HTTP/MCP over a
  seeded graph; missing-root parity.
- **Fuzz:** the infer JSON-unmarshal/index-mapping surface (`FuzzCausalProposals`)
  with the seed corpus run as a CI test (§11).

## Risks & mitigations

- **Spurious edges polluting retrieval / "why".** → confidence gate
  (`causal_min_confidence`), edges grounded in the narrative only, advisory (traversal
  is opt-in; edges don't change scoring in this phase).
- **Best-effort once-at-narration (no retry on inference failure).** → atomic
  narrative+links commit covers the crash case; an inference *gateway* failure leaves
  the episode edge-less but narrated (acceptable for an advisory layer); re-inference
  is a documented future reindex (D-083).
- **Traversal blow-up on dense graphs / cycles.** → BFS visited-set, `maxDepth=10`,
  node budget 200, `Truncated` surfaced (no silent cap, §11).
- **Decision-memory gathering misses cross-session causes.** → Phase 24 scopes
  causality within an episode (the narrative's frame); cross-episode arcs are Phase
  24b (D-081). Documented boundary.

## Glossary additions

- **Causal inference pass** — the once-per-episode, schema-constrained gateway step
  (at narration) that proposes confidence-scored `led_to` edges between an episode's
  decision memories, written `source="inferred"` (Phase 24, D-083).
- **Why-traversal** — the deterministic, gateway-free walk of the `caused_by`/`led_to`
  graph from a memory (backward to causes, forward to effects) with provenance at
  every hop; the `memory_causal` capability (Phase 24, RFC §5.6/§6b).

## Decisions filed

- **D-083** — Causal inference ships as a sub-step of the Phase-22 narration sweep
  (atomic with the narrative commit via `CommitSet.Links`, gated once-per-episode by
  `narrative_memory_id`), emitting confidence-gated `led_to` edges (`source=inferred`)
  into the day-one `links` table — **no new schema, no RFC amendment**. Why-traversal
  is a deterministic gateway-free core (`memory_causal`) across {SDK, HTTP, MCP}.
  Re-inference is an explicit future reindex, never a silent re-run. Cross-episode
  causality is deferred to Phase 24b (D-081).
