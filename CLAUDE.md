# Stowage — Contributor & Agent Normatives

> This file is **binding** for anyone — human or AI — modifying this repository.
> It is mirrored **verbatim** in `AGENTS.md` so all agent tooling picks it up
> automatically. If the two files diverge, the most recent commit timestamp wins;
> flag the drift in your PR.

If a rule below conflicts with the RFC or a phase plan, the **RFC wins**, then the
**phase plan**, then this file. Update whichever artifact is wrong; never silently
ignore the conflict.

---

## Starting a new session — orientation (READ THIS FIRST)

Stowage is a multi-phase, doc-driven build. The design surface is large on purpose:
hygiene up front is cheaper than retrofitting it. Before substantive work, skim, in
order:

1. **§1 — What Stowage is.** The product and its five binding properties.
2. **§2 — Authoritative sources.** The priority chain: RFC > phase plans > this file
   > research briefs > code comments.
3. **§16 — Authoring a phase plan.** The binding workflow for any contributor
   touching a phase. Skipping it is the single largest source of design drift.

**Drift-hygiene artifacts (live references):**

- `RFC-001-Stowage.md` — the design source of truth.
- `docs/decisions.md` — append-only log of settled decisions (`D-NNN`). When tempted
  to re-litigate something, grep here first.
- `docs/glossary.md` — Stowage vocabulary. New terms land here in the same PR.
- `docs/research/INDEX.md` — subsystem → research-brief reverse index.
- `docs/plans/_template.md` — phase plan template; new phases start as a copy.
- `scripts/drift-audit.sh` — mechanical drift checks (`make drift-audit`).

If asked to do something that doesn't fit a phase (a one-off fix, a question, a small
doc edit), proceed without the full §16 ritual — but mention any drift risk you spot.

**Predecessor hygiene.** Stowage is a clean-room redesign. Code or files from the
predecessor systems (the internal Python memory server and the internal CC-memory
Go system) are **never** copied, vendored, or committed here, and the Python
predecessor's project name must not appear anywhere in this repository — refer to
it as "the Python predecessor". Ideas are inherited via `docs/research/` briefs
only.

---

## 1. What Stowage is

Stowage is a Go-native memory server for agentic systems. It is the fourth product
in a four-part ecosystem:

```text
Portico  — the MCP gateway        (connects and governs tools)
Harbor   — the agent framework    (builds and runs agents; owns the MCP client)
Dockyard — the MCP Apps framework (builds the MCP servers and apps users touch)
Stowage  — memory infrastructure  (remembers, reconciles, retrieves, forgets)
```

Stowage ships **one CGo-free static binary** — `stowage` — that serves an HTTP
API, an MCP tool surface, and an operations CLI, plus a Go SDK (`sdk/`) for
in-process embedding in Harbor.

**Five binding properties.** A change that weakens any of them is wrong — reach
for the RFC, not the keyboard.

1. **P1 — Fidelity first.** Verbatim records are durable and never silently
   discarded; every derived memory carries provenance; retrieval can always
   drill down to the verbatim source.
2. **P2 — Fire-and-forget writes.** Ingest ACKs after the durable verbatim
   append; everything else is asynchronous, supervised, in-process goroutine
   stages. No external workers, no polling loops.
3. **P3 — Scopes enforced at write AND read.** Identity scoping happens in the
   store layer; no unscoped query API exists.
4. **P4 — Memory must forget.** Reconciliation, decay, supersede gates, and
   quarantine are first-class subsystems.
5. **P5 — No local models; one intelligence seam.** Every embedding/LLM call
   goes through the `gateway` seam (Bifrost driver first). The shipped binary is
   CGo-free and model-free.

---

## 2. Authoritative sources (in priority order)

1. `RFC-001-Stowage.md` — product intent and design decisions.
2. `docs/plans/phase-NN-*.md` — implementation specifications. Acceptance criteria
   are binding.
3. `docs/plans/README.md` — the master phase plan: cross-cutting conventions and the
   phase index.
4. This file (`CLAUDE.md` / `AGENTS.md`) — operational rules.
5. `docs/research/*.md` — phase-planning research briefs. Authoritative for
   *context*, not for design — the RFC and phase plans are where decisions land.
6. Code comments and godoc — last and least authoritative.

When a phase plan and the RFC drift, the RFC wins. File a follow-up to fix the plan.

---

## 3. Repository layout

```text
.
├── RFC-001-Stowage.md           # design RFC — source of truth
├── README.md
├── CHANGELOG.md                 # release notes (Keep a Changelog)
├── CLAUDE.md / AGENTS.md        # this file (verbatim copies)
├── Makefile                     # canonical build / test / lint commands
├── go.mod / go.sum
├── .github/                     # CI, PR template
├── .golangci.yml / .editorconfig / .gitignore
├── cmd/
│   └── stowage/                 # the `stowage` binary entrypoint (serve, mcp, CLI)
├── internal/
│   ├── api/                     # HTTP surface: routing, validation, auth
│   ├── mcpserver/               # MCP tool surface over go-sdk
│   ├── identity/                # scope types + enforcement helpers
│   ├── config/                  # typed config, env indirection, fail-loud validation
│   ├── records/                 # verbatim record layer (P1)
│   ├── pipeline/                # buffer → extract → reconcile → commit stages (P2)
│   ├── topics/                  # topic (extraction magnet) management
│   ├── reconcile/               # reconciliation decisions, trust gates, chains, rollback (P4)
│   ├── retrieval/               # lanes, fusion, scoring, budgeting, drill-down
│   ├── grants/                  # team sharing: groups, grants, zone ceilings (RFC §5.3)
│   ├── playbook/                # deterministic playbook assembly (RFC §6a — no LLM calls)
│   ├── lifecycle/               # decay / dedupe / rollup / re-enqueue / re-reflection sweeps
│   ├── gateway/                 # the intelligence seam + drivers {bifrost, mock} (P5)
│   ├── store/                   # the Store seam + drivers {sqlite, postgres}
│   ├── vindex/                  # vector index seam + drivers {pgvector, gohnsw, brute}
│   ├── events/                  # typed event stream + SSE + Harbor-bus adapter
│   └── telemetry/               # slog setup, metrics, optional OTel
├── sdk/
│   └── stowage/                 # public Go client (HTTP + in-process modes)
├── eval/                        # gain harness + retrieval benchmarks (`stowage eval`)
├── examples/
├── test/integration/
├── scripts/
│   ├── preflight.sh             # the preflight gate
│   ├── drift-audit.sh           # design-coherence checks
│   ├── smoke/                   # per-phase smoke scripts
│   ├── hooks/pre-commit
│   └── install-hooks.sh
└── docs/
    ├── plans/                   # master plan (README.md) + phase plans + _template.md
    ├── research/                # research briefs + INDEX.md
    ├── decisions.md             # append-only D-NNN log
    └── glossary.md
```

Directories are created as the phases that own them land. Anything that doesn't have
a home above is wrong — if you need a new top-level directory, propose it in the RFC
first; `§3` is the binding layout.

---

## 4. Build, test, lint, run

All targets are canonical and run by CI. Targets no-op gracefully before the code
they act on exists.

```bash
make build         # build the stowage binary (CGo-free static)
make test          # go test -race ./...
make coverage      # per-package coverage profile + the mechanical band gate
make bench         # run the Go benchmarks (on demand — not a CI gate)
make vet           # go vet ./...
make lint          # golangci-lint run
make drift-audit   # design-coherence checks (RFC/plans/briefs/mirror/forbidden names)
make check-mirror  # verify AGENTS.md == CLAUDE.md
make preflight     # build + smoke checks + drift-audit
make install-hooks # install the pre-commit hook (one-time, per clone)
```

### 4.1 Preflight gate — non-negotiable

`make preflight` is the same gate the pre-commit hook and CI enforce: it builds,
runs every per-phase smoke script (which SKIP gracefully where the surface isn't
built yet), and runs `drift-audit`. Do not bypass the pre-commit hook with
`--no-verify` outside a documented emergency.

### 4.2 Phase implementor contract

A phase is **done** only when: (a) every acceptance criterion in its plan passes;
(b) coverage targets for touched packages are met; (c) `scripts/smoke/phase-NN.sh`
reports `OK ≥ count(criteria)` and `FAIL = 0`; (d) prior phases' smoke scripts still
pass. A new CLI command, API endpoint, MCP tool, or public API ⇒ a smoke check in
the **same** PR. A new config key ⇒ documented in the plan, the example config, and
a smoke check.

### 4.3 Reasonable plan deviations

Plans are specifications, not straitjackets. A reasonable deviation discovered
during implementation is fine — document it in the PR description and update the
plan file **in the same PR**. Silent divergence from a plan or the RFC is drift.

### 4.4 Extensibility seams (project-wide policy)

Any subsystem with a plausible alternate backend lives behind an **interface +
factory + driver** pattern. V1 mandates this for: the `Store` (sqlite + postgres),
the vector index (`vindex`), the `gateway` (bifrost + mock), and the `events`
emitter. Drivers register via `init()` blank-import.

---

## 5. Code conventions (Go)

- **Toolchain.** Go 1.26, pinned in every `go.mod`. **No CGo in the shipped
  artifact** — `make build` pins `CGO_ENABLED=0`; a runtime dependency that needs
  CGo is rejected. Test binaries are the one exception: `make test` runs with
  `CGO_ENABLED=1` because the `-race` detector requires CGo — tests are not
  shipped, so this does not weaken the CGo-free guarantee.
- **Style.** `gofmt -s`; `go vet` and `golangci-lint run` clean. Generated code is
  marked with a `// Code generated … DO NOT EDIT.` header and stays boring and
  readable.
- **Errors.** `errors.Is`/`errors.As`, `%w` wrapping, sentinel errors,
  `errors.Join`. Wrap with context. **Never `panic` for control flow** and never
  panic across the API or MCP boundary.
- **Context.** `context.Context` is the first parameter of any call that does I/O,
  blocks, or can be cancelled. Honour cancellation.
- **Logging.** `log/slog` only — no `log.Printf`, no `logrus`/`zap`. JSON handler in
  production, text in dev. No unredacted secrets in logs.
- **Concurrency.** Race detector mandatory on tests. A reusable artifact (a server,
  a store, a pipeline stage, a gateway driver) must be safe under concurrent use;
  prove it. Per-request state lives in `ctx` and parameters, never receiver fields;
  shared instances are immutable after construction (Harbor's D-025 discipline).
- **Tests.** Table-driven where it fits; golden tests for prompt/contract output;
  `-race` always.
- **JSON.** Stdlib `encoding/json` (v1).

---

## 6. The non-negotiable product rules

These enforce P1–P5 (§1). They are binding on every phase.

- **Fidelity first (P1).** No code path deletes or mutates a verbatim record except
  the explicit retention/DSAR cascade. Every derived memory write includes
  provenance refs; a memory without provenance fails validation.
- **Fire-and-forget (P2).** The ingest handler does exactly: validate, stamp,
  durable append, enqueue, ACK. Anything heavier belongs in a pipeline stage.
  Pipeline stages are bounded, supervised, and drain on shutdown.
- **Scopes (P3).** Store-layer query builders require a scope parameter — there is
  no unscoped variant. A new query method without scope enforcement is rejected in
  review.
- **Forgetting (P4).** A new memory-producing feature must state its lifecycle
  (decay class, supersede behavior, quarantine eligibility) in its phase plan.
- **One intelligence seam (P5).** No package outside `internal/gateway` imports an
  LLM/embedding provider SDK or constructs provider HTTP requests. New providers
  are new gateway drivers.
- **Playbook assembly is LLM-free.** `internal/playbook` never calls the
  gateway — evolution happens only through delta reconciliation (RFC §6a,
  ACE's context-collapse defense). A gateway import there fails review and the
  Phase 18 lint test.
- **Reconciliation is reversible.** Destructive ops (update/merge/supersede)
  must be invertible from their events (D-017); a reconciliation change that
  breaks rollback round-trips is wrong.
- **No-CGo, single binary.** Every artifact compiles CGo-free and cross-compiles.

---

## 7. Security — non-negotiable rules

- No hardcoded secrets, anywhere — including tests. Config secrets use `env.VAR`
  indirection and fail closed at boot.
- Scope isolation is enforced in the store layer; handler-layer filtering is not a
  substitute.
- HTTP transport: timeouts, body limits, Origin/Content-Type and cross-origin
  protections are set **explicitly** — never inherited from an SDK default.
- API keys are compared in constant time; keys are never logged.
- Gateway payloads are the only data that leaves the process; redaction profiles
  apply before any gateway call once they land.

---

## 8. Observability — the event-stream rules

- The event stream is a **versioned, consumable** contract (`events/v1`). A change
  to the event shape is a versioned change, documented, never silent.
- The runtime emits; it never blocks on a slow consumer. Emit paths are
  non-blocking (ring buffer, bounded fan-out).
- Every memory mutation and lifecycle decision emits an event with its reason —
  the audit trail *is* the event log.
- OTel export is an adapter behind the telemetry seam, off by default.

---

## 9. Persistence — the `Store` seam rules

- All durable state goes through the `Store` interface. V1 drivers: `postgres`
  (pgx — the principal production store, D-021) and `modernc.org/sqlite`
  (pure-Go, CGo-free — the embedded/portable driver; it must stay CGo-free
  forever, D-022). Both pass the same conformance suite; neither is "the real
  one" in code — the seam is the contract.
- A new persistence concern adds a method to the seam and is implemented by **every**
  driver, proven by the shared conformance suite — not bolted onto one driver.
- Migrations are forward-only; never edit a migration after it merges.

---

## 10. The gateway seam rules

- `internal/gateway` is the only package that knows provider wire formats. Drivers
  are versioned against their provider API and covered by golden request/response
  tests with a `mock` driver for everything else.
- Embedding model + dimensions are pinned per vector index and validated at boot;
  a model change is an explicit reindex operation, never silent.
- Every gateway call is metered (tokens, cost) and emitted as an event.
- Structured outputs use JSON-schema-constrained calls; free-text JSON parsing of
  model output is forbidden.

---

## 11. Testing rules

- `-race` on every test run. CI fails on a race.
- Prompt assembly and API contracts are covered by **golden tests** (fixed input →
  fixed output).
- A phase that consumes another subsystem's surface, or closes a cross-subsystem
  seam, ships an **integration test** with real drivers — see §17.
- Coverage defaults (override per phase): 80% new packages; 85% the `Store`/`vindex`
  drivers and the conformance-tested subsystems; 70% CLI / tooling.
- **The coverage bands are a mechanical gate, not an aspiration.** `make coverage`
  runs the per-package coverage checker against configured thresholds; CI runs it
  and a coverage regression — or a new package with no configured threshold — fails
  the build. A band genuinely unreachable hermetically gets a documented override
  (class + reason) and a decision entry — never a silent lowering.
- Prime parse/decode surfaces carry Go `FuzzXxx` **fuzz targets** with a seed
  corpus and an asserted invariant; the corpus runs as an ordinary CI test. Hot
  reusable artifacts carry `BenchmarkXxx` **benchmarks** (run on demand via
  `make bench` — a baseline, not a CI gate).

---

## 12. Commit and PR conventions

- **Commits:** imperative mood, scoped (`feat(pipeline): …`, `fix(retrieval): …`,
  `chore: …`, `docs: …`). Small and coherent. Commits are **unsigned** in this
  repository (`commit.gpgsign=false` is set locally; do not enable signing).
- **Branches:** never commit feature work directly to `main`; use `feat/phase-NN-*`
  (or `chore/*`, `docs/*`). Once the project is past scaffolding, do not modify
  `main` directly — use a worktree or branch.
- **PRs:** reference the RFC section(s) and the phase. State any plan deviation and
  update the plan in the same PR. The pre-merge checklist (§14) gates the PR.
- **Merge:** squash unless history is meaningful. CI green is mandatory.

---

## 13. Forbidden practices

- Hardcoded secrets, including in tests.
- `panic` for control flow; panicking across the API or MCP boundary.
- A CGo runtime dependency, or building the shipped artifact with `CGO_ENABLED=1`
  (`-race` test runs use `CGO_ENABLED=1` and are exempt — §5).
- Copying or vendoring code/files from the predecessor systems; naming the Python
  predecessor anywhere in this repository.
- An unscoped store query API (violates P3).
- Importing a provider SDK or building provider requests outside
  `internal/gateway` (violates P5).
- Deleting/mutating verbatim records outside the retention/DSAR cascade
  (violates P1).
- Blocking the ingest ACK on extraction, embedding, or reconciliation work
  (violates P2).
- Free-text JSON parsing of model output (§10).
- Adding a CLI command, endpoint, MCP tool, or config key without a smoke check in
  the same PR.
- Editing a migration after merge.
- Bypassing the pre-commit hook with `--no-verify` outside a documented emergency.

---

## 14. Pre-merge checklist

- [ ] `make drift-audit` passes.
- [ ] `make check-mirror` passes (`AGENTS.md` == `CLAUDE.md`).
- [ ] `make preflight` passes.
- [ ] `go test -race ./...` and `golangci-lint run` are clean.
- [ ] All cross-references (`RFC §X.Y`, `brief NN`) resolve.
- [ ] Coverage on touched packages ≥ the phase's stated target — `make coverage`
      passes (a new package is added to the coverage config in the same PR).
- [ ] A new CLI command / endpoint / MCP tool / config key has a smoke check in
      this PR.
- [ ] If a reusable artifact changed: a concurrent-reuse test passes under `-race`.
- [ ] If a cross-subsystem seam was opened or consumed: an integration test exists
      (§17).
- [ ] New vocabulary added to `docs/glossary.md` in this PR.
- [ ] A new architectural decision (or a departure from a brief) is filed in
      `docs/decisions.md`.

---

## 15. When in doubt

The RFC wins. If the RFC is silent, the phase plan decides; if both are silent,
raise it — do not invent a decision and bury it in code. A new settled decision is
an entry in `docs/decisions.md`; a change to a settled decision is an RFC PR plus a
superseding decision entry, never a silent edit.

---

## 16. Authoring a phase plan (workflow)

The canonical workflow for any contributor starting a phase. The drift-audit gate
enforces what it can; this workflow covers what it can't.

1. **Read the master plan entry.** Open `docs/plans/README.md`, find the Phase N
   detail block. Note owning subsystem, RFC sections, dependencies, risks.
2. **Read the cited RFC sections** in `RFC-001-Stowage.md`.
3. **Read the relevant briefs** per `docs/research/INDEX.md`. A phase plan that
   cites no informing brief is a drift signal.
4. **Read the glossary** for any term you're unsure about; pre-write the entry for
   any new term you introduce.
5. **Read the decisions log** (`docs/decisions.md`) for entries touching this
   subsystem. Settled decisions are not re-litigated silently.
6. **Copy the template:** `cp docs/plans/_template.md docs/plans/phase-NN-slug.md`.
   Fill every section. "Brief findings incorporated" and "Findings I'm departing
   from" are forcing functions — they make brief inheritance visible.
7. **Author the smoke skeleton:**
   `cp scripts/smoke/_template.sh scripts/smoke/phase-NN.sh`.
8. **Run `make drift-audit` and `make preflight`** before committing.
9. **Commit only when both pass.** The PR references the RFC section and any
   superseded decision.

---

## 17. End-to-end + integration testing

Per-package unit tests miss two classes of bug: **cross-package wiring gaps** (two
phases each ship their half of a seam, neither connects them) and **cross-subsystem
concurrency interactions**.

A phase ships an integration test whenever its `Deps` name a different subsystem's
shipped phase, or it closes a seam another phase opened, or it introduces a public
interface other phases will build on. Integration tests use **real drivers** on the
seam (no mocks at the boundary; the gateway `mock` driver is the one sanctioned
exception for tests that must not call a paid API — pair it with at least one
recorded-fixture test against the real wire format), prove identity/scope
propagation, cover ≥1 failure mode, and run under `-race`. They live in-package
when the package *is* the wiring boundary, otherwise in `test/integration/`.

At wave boundaries a read-only **checkpoint audit** reviews every shipped phase for
wiring gaps, RFC drift, weak tests, and hygiene regressions, and lands its punch
list as one `chore(checkpoint)` PR. When an integration test surfaces a bug, fix it
in the same PR — even when the root cause is in an earlier phase.

---

## 18. Mirroring

`AGENTS.md` and `CLAUDE.md` are kept **verbatim identical**. After any edit:

```bash
diff -q AGENTS.md CLAUDE.md   # expected: no output
```

CI enforces this; the `mirror` job fails the build if they differ.
