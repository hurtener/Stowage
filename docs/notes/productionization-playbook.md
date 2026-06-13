# The productionization playbook — audit-driven hardening of a framework

> A reusable process guide, written from the Harbor SDK re-homing program
> (2026-06, Harbor D-193, PRs #276–#285 + the 111 band). The steps are
> framework-agnostic; Harbor is the worked example throughout §0–§6. Use this to
> run the same program against another framework: the inputs are a codebase, a
> claimed-but-underexercised consumer, and an owner who approves PRs as CI goes
> green.
>
> **Homed into Stowage (2026-06).** §7 maps every generic step onto Stowage's
> actual structure — lens candidates, seam decomposition, doc/CI artifacts,
> decision numbering, and the memory-server-specific failure classes. Read §0–§6
> for the method; read §7 to execute it here. The Harbor references in §0–§6 are
> the worked example, not Stowage facts — translate them via §7.

## 0. The premise

A framework accumulates silent drift between **what it claims** and **what is
true on main** whenever its daily dev loop exercises only one of its claimed
consumption paths. Harbor's claim was "ships as a Go module" (an embeddable
SDK); its daily loop was the dev binary + its own Protocol/Console client. The
result, invisible until audited: production semantics stranded in the binary,
primitives whose only consumers were tests, config knobs that validated and did
nothing, and three outright product bugs — including the flagship pause/resume
primitive deadlocking on its canonical use case.

The playbook has five phases: **pick the lens → audit the seams → triage into
waves by change-type → execute with plans + staged parallel agents +
checkpoint audits → leave the external/decision-shaped work as an explicit
RFC**. The whole program ran in ~10 PRs over a few days with one human in the
loop approving merges.

## 1. Pick the lens (one sentence, adversarial)

Write the audit lens as a single sentence naming **the consumer your
architecture claims to serve but your tooling never exercises**, and the
constraint that makes it sharp:

> *Harbor must hold as an embeddable Go SDK — a consumer who constructs the
> runtime in Go and never runs the dev binary, never serves the Protocol,
> never opens the Console — even though the Protocol path is the house-favored
> client.*

Good lenses share three properties: (a) the consumer is *legitimate* per your
own docs/RFC, so findings are violations, not feature requests; (b) the
consumer's path is *mechanically checkable* ("can this be reached without X?"
is a grep/compile question, not a taste question); (c) the favored path is
named explicitly, because that's where the drift hides. Other lenses that work
the same way: "must hold without the ORM," "must hold on the second platform,"
"must hold for the on-prem deployment that can't phone home."

## 2. The seam audit (the analysis workflow)

Run a **parallel multi-agent fan-out with adversarial verification**. Do not
audit serially with one context — the value is independent eyes per seam plus
a skeptic pass that kills plausible-but-wrong findings before a human reads
them.

### 2.1 Decompose into seams

~10 scopes, one investigator each. Decompose by *subsystem boundary*, not by
folder: ours were run-loop/tasks, planner/context-assembly, LLM stack,
tools/dispatch, pause/steering, memory/skills, persistence/eventing,
config-duality (a cross-cutting one), the external-module surface (another
cross-cutting one), and governance/telemetry/import-direction. Include at
least one *empirical* scope (ours compiled a throwaway external module to
prove the import boundary) — probes beat reading.

### 2.2 The investigator prompt (the load-bearing part)

Every investigator gets the same preamble:

1. **The lens**, verbatim.
2. **A severity taxonomy with definitions** — ours: *blocker* = capability
   unreachable on the audited path at all; *major* = reachable but requires
   replicating privileged-path code, or a config-only knob, or mandatory
   ceremony; *minor* = missing recipe/docs/ergonomics. Without definitions,
   severities are noise.
3. **What counts as friction, as named failure classes** — e.g. "the only
   wiring or only consumer of a primitive is the privileged binary," "a
   validated config knob with zero consumers," "a lifecycle (GC, cleanup,
   expiry) that only runs on the favored path," "logic duplicated between the
   binary and the test kit." Name the classes; investigators pattern-match.
4. **A known-findings exclusion list** — everything already settled, so agents
   dig instead of re-reporting the headline.
5. **Citation discipline**: every claim carries `file:line`; before reporting
   something as privileged-path-only, grep for an exported alternative.
6. **Read-only, structured output** (JSON schema: seam, reachability verdict,
   findings[{title, severity, evidence[], why, recommendation}]). The
   recommendation field forces each finding to arrive with its smallest
   viable fix.

### 2.3 Adversarial verification

Every blocker/major finding gets an independent skeptic agent whose prompt is
*to refute it*: re-read the cited code, hunt for the exported alternative the
investigator missed, check whether the claim mis-attributes. Refuted findings
die; survivors get a severity-calibration opinion. Pipeline it (verify each
seam's findings as that seam returns — no global barrier). Harbor's run: 53
agents, 62 findings, **0 refuted**, 3 severity downgrades — and several
verifiers *strengthened* findings with extra evidence. The skeptic pass is
what makes the findings doc trustworthy enough to drive a multi-week program
without re-litigating.

### 2.4 Synthesize by pattern, not by seam

The coordinator (not an agent) writes the findings doc: a one-paragraph
verdict, the **cross-cutting patterns** (ours: the privileged-binary stratum +
its diverged test-kit mirror; zero-consumer primitives; wire-layer auth leaked
into in-process control; the external boundary; config duality), any **outright
bugs found incidentally** (promote these to the top — they justify the program
to stakeholders who don't care about the lens), and a **prioritized wave
program**. File the doc in-repo; it becomes the program's citable source of
truth.

## 3. Triage into waves by change-type (not by subsystem)

This is the key structural decision. Findings cluster naturally by *what kind
of change fixes them*, and each kind has a different safe delivery process:

- **Wave A — correctness + honesty (fix now, no design needed).** Product
  bugs, dead config knobs made fail-loud, lying godocs/docs corrected,
  test-kit parity drifts. Delivery: the *checkpoint-audit punch-list pattern* —
  the coordinator fixes (or dispatches 1–2 agents), unified into one `chore`
  PR. **Exception**: any fix that changes behavior semantics (our run-loop
  dispatch change) gets its own `fix` PR with its own decision-log entry and a
  real E2E — never buried in hygiene.
- **Wave B — mechanical re-homing.** Code that is correct but lives in the
  wrong place (the privileged binary, a hand-maintained mirror). Promotions
  with a **byte-for-byte behavior-preservation bar**: golden tests pin the old
  output, existing tests pass unmodified, the old call sites become thin
  callers *in the same PR* (the consumer-in-same-phase rule), and every
  promotion deletes a mirror copy. Stage by **file-collision analysis, not
  logical grouping**: we ran 2+2 sequential stages because all four phases
  touched the same two files; a wide fan-out would have been a merge
  bloodbath.
- **Wave C — finish or formally defer the half-shipped primitives.** Every
  primitive whose only consumer is a test gets either its first production
  consumer or a recorded deferral + honest godoc. These phases are mutually
  independent *and they all wire into the assembly* — so hold them until Wave
  B settles the assembly (we almost dispatched them in parallel; don't).
- **Wave D — the decision-shaped remainder.** Anything requiring an
  architecture-level call (for Harbor: the external facade — what gets
  promoted out of `internal/`). This is an RFC, not a phase plan; it's also
  why Wave B precedes it: *you cannot facade what lives in a binary*.

## 4. Execution machinery (what made 10 PRs in days safe)

### 4.1 Plans as binding specs, authored before implementation

Waves B and C got full phase plans (your repo's plan template + research-brief
ritual) authored by agents from coordinator directives, *reviewed as a docs PR
before any implementation*. Two practices that paid off:

- **Open questions are explicitly marked for owner decision** with the plan's
  recommendation + the alternatives. Our example: should a dormant
  capability-filtered Directory replace the live-but-hygiene-bypassing search
  path? The plan recommended, the owner picked, the resolution was recorded in
  the plan (a small docs PR) *before* the implementing agent dispatched — and
  the implementing prompt said "this is resolved; cleared to build."
- **Cross-phase consequences are written into both plans** (the Directory
  decision obsoleted a helper another phase was about to promote; that phase's
  plan gained a "promote with a scheduled-for-deletion notice" clause).

### 4.2 Decision-log discipline

One **program entry** (D-193) records the lens, the wave structure, the
principles (re-homing over mirroring; primitive-with-consumer re-read against
the current tree; the import-direction rule), and **pre-reserves a decision
number per phase** — parallel agents then append their own entries without
numbering collisions. The log is append-only: corrections land as *dated
notes inside the entry* (we did this when an audit found a recorded
justification incomplete), never as rewrites.

### 4.3 The coordinator/agent contract

- Agents implement in **isolated git worktrees**, one phase each, branch from
  `origin/main`, **commit but never push** — the coordinator reviews, rebases,
  pushes, opens the PR; the owner approves on green CI.
- Every dispatch prompt carries: the binding plan path, the pre-assigned
  decision number, the mandatory reading list, the full validation gate, the
  workspace discipline block (`pwd` first; stay in the worktree), the
  sibling-awareness note (who else is running, which files are shared, keep
  edits banded), and the known tooling gotchas.
- **Review is real**: diff scope vs plan, base freshness, decision entry
  present, then targeted gates — and when two parallel branches' *code* first
  combines at a rebase, the full gate (entire race suite + preflight) runs on
  the combination before push. Twice that combination check was the only
  thing standing between a green-looking merge and main.

### 4.4 The same-PR repair rules (anti-deferral)

- **Fix what the work surfaces, wherever it lives**: smoke scripts pinning
  deleted file paths, fixtures that only pass because they're weaker than
  production, latent nil-derefs in rewritten blocks — fixed in the same
  commit, named in the PR body. Three different phases each caught a
  prior-phase regression this way; deferring any of them would have shipped a
  silently degraded gate.
- **Docs/skills that document a surface change in the same PR** — including
  deleting documentation of features that don't exist (we found operator docs
  for a CLI verb that was never built; excised in Wave A, restored in Wave C
  when the verb shipped).

### 4.5 Checkpoint audits gate the next wave

At each wave boundary, a read-only audit fan-out (same workflow shape as §2,
smaller: one auditor per shipped phase + **one cross-phase integration
auditor** + skeptics on FAILs) produces a FAIL/WARN/NIT punch list; the
coordinator lands it as one `chore(checkpoint)` PR, and **the next wave does
not dispatch until it merges**. What ours caught that nothing else would have:
a *pending* next-wave plan still spec'd against a symbol the just-merged wave
deleted; glossary entries naming dead code as the live implementation; a
"byte-for-byte parity" claim whose key field no test pinned. The integration
auditor's brief should explicitly include "hunt casualties of the
coordinator's merge resolutions."

## 5. Recurring failure modes (pre-empt them in dispatch prompts)

1. **Stale agent bases** — an agent fetches, then a PR merges; its branch
   "reverts" the merge in diffs. Always rebase/merge onto fresh main at
   review; never trust the agent's base.
2. **Append-collisions in shared logs** (decision log, master plan rows) —
   expected, not avoided: resolve keep-both in canonical order; the second
   PR to merge gets the rebase.
3. **Both-sides-deleted merges** — when two phases each promote away code the
   other still carries, the conflict region is *entirely* dead: delete both
   sides and let the compiler adjudicate.
4. **Shared lint-cache poisoning** across parallel worktrees of one module —
   phantom findings in deleted sibling paths; `cache clean` first, exclude
   the worktree dir from lint walks.
5. **Smoke/test assertions pinned to file paths or helper names** — every
   promotion wave trips these; the SKIP-that-should-be-OK variant is silent.
   Make "grep prior smokes for the names you delete" a standing dispatch
   instruction.
6. **Plan line-number drift** — plans authored before earlier phases land cite
   shifted lines. Dispatch wording: *intent is binding; line refs are
   orientation; read the current code.*
7. **Stale background watchers** re-notifying after completion — harmless;
   clean up merged-work worktrees to quiet them.

## 6. What it produced (the calibration data)

For sizing expectations on the next framework: the audit was 53 agents / 62
findings / 0 refuted; the program landed as ~10 PRs (1 amendment-docs, 1 fix,
1 audit-chore, 1 plans, 4 re-homing, 1 checkpoint-chore, + the
finish-the-primitives band); the Wave B checkpoint came back **0 FAIL / 10
WARN / 13 NIT** — i.e., after the process above, a hostile audit of the
wave's own output found no binding violation, only hygiene. The bugs that
justified everything to a non-SDK stakeholder: a deadlock in the flagship
control primitive, a GC that could kill live sessions, and three
silently-dropped-config classes — all found by asking each seam one question:
*who actually calls this?*

---

## 7. Applying this to Stowage (the concrete mapping)

Stowage shares Harbor's doc-driven structure, so the machinery transfers
almost verbatim. This section makes every generic step actionable here. Treat
§0–§6 as the method and this section as the binding parameters.

### 7.1 Pick the lens — Stowage is a PARITY lens (both seams co-equal)

Stowage's README claims: *"Runs as a standalone binary (HTTP + MCP + CLI) over
Postgres, or **fully embedded in a host process over pure-Go sqlite — same
code, same seams**."* Unlike Harbor — where one path (the Protocol/Console
client) was house-favored and the other (the embedded SDK) was the neglected
claim — **in Stowage both seams are first-class and equally important**: the
embedded-over-sqlite host AND the standalone server over Postgres. So the §1
single-favored-path framing does not apply verbatim. When both consumers are
co-equal, the drift does not hide in "the path tooling never exercises" — it
hides in **divergence between the two paths** (and between the two backends).
The lens is therefore a *parity* lens, symmetric and bidirectional:

> *"Same code, same seams" must be literally true: every capability, lifecycle,
> and semantic must be **reachable and behave identically** on the
> embedded-sqlite path AND the standalone-server-over-Postgres path. Any
> behavior, lifecycle, or capability that exists / works / runs / is observable
> on **only one** of the two — in **either** direction — is a finding (a parity
> violation), regardless of which side it favors.*

This keeps the §1 properties (legitimate consumer per the RFC; mechanically
checkable — "does X hold on path A and path B?" is a compile/grep/diff
question) while replacing "the favored path is named" with "neither path is
privileged; divergence is the bug."

Two parity axes the audit must check together, not separately:

- **Path axis** — embedded (`sdk/stowage`, in-process) vs. server
  (`cmd/stowage` → `internal/api` + `internal/mcpserver`).
- **Backend axis** — pure-Go sqlite vs. Postgres (`internal/store`); a feature
  that "only works on Postgres" is the same class of violation as one that
  "only runs in the server."

The **load-bearing memory-server case**: the async pipeline (fire-and-forget
ingest → extraction → reconciliation → forget). A standalone server may run it
on a background goroutine/worker started by the server boot; the embedded host
must get the *same* progression with no out-of-process worker — and the server
must expose the *same* observability the embedded host has. Divergence here
(writes that become memories on one path but not the other; reversibility
drivable from one side only) is the flagship finding to probe first.

The owner confirms this parity framing (or substitutes one) before §2
dispatches. Write the chosen lens verbatim into the findings doc and every
investigator prompt.

### 7.2 Seam decomposition for Stowage (§2.1 — ~10 scopes)

Map by subsystem boundary, using `internal/` + the consumer surfaces. A
suggested 11-scope cut (one investigator each), each carrying the chosen lens:

1. **Ingest + buffers** — `internal/records`, the ingest path in
   `internal/pipeline` (fire-and-forget API; is the write durable + the async
   hand-off reachable embedded?).
2. **Extraction + topics** — `internal/topics`, `internal/pipeline`
   (topic-guided extraction; does it run without a server/worker?).
3. **Reconciliation + lifecycle** — `internal/reconcile`, `internal/lifecycle`
   (update / supersede / merge / forget, *reversibly*; rollback — Phase 18).
4. **Retrieval + ranking** — `internal/retrieval`, `internal/scoring`,
   `internal/vindex` (hybrid lexical + vector + structured fusion; provenance
   drill-down).
5. **Gateway seam** — `internal/gateway` (embeddings + LLM via Bifrost; the
   one provider-agnostic seam — is it reachable + swappable embedded?).
6. **Store / persistence** — `internal/store` (Postgres vs pure-Go sqlite
   parity; the day-one schema rule, RFC §5.0).
7. **Identity + auth + grants** — `internal/identity`, `internal/auth`,
   `internal/grants` (isolation boundary; cross-team sharing; does auth leak
   from the HTTP/wire layer into in-process control? — the Harbor analogue
   was wire-auth leaked into in-process steering).
8. **Server surfaces** — `internal/api`, `internal/mcpserver`, `cmd/stowage`
   (HTTP + MCP + CLI; what lives here that the embedded path can't reach — and
   what does the embedded path do that the server doesn't expose?).
9. **Parity probe (empirical scope)** — `sdk/stowage`, `adapters/harbor`,
   `clients/python` + `cmd/stowage`. Run the **same scenario both ways and
   diff**: (a) compile a throwaway module OUTSIDE the repo that imports
   `sdk/stowage`, opens embedded sqlite, ingests, and reads back a reconciled
   memory; (b) run the identical scenario against `cmd/stowage` over Postgres
   (HTTP/MCP). Diff the observable results — same memory, same reconciliation
   outcome, same provenance, same events. Any divergence is the headline
   finding. Probe, don't read.
10. **Config + boot duality** — `internal/config`, `internal/boot` (every knob:
    does it have a consumer? is it settable programmatically AND via config? is
    zero-config embedded start real?).
11. **Telemetry + observability + import-direction** — `internal/telemetry`,
    `internal/version`, `internal/bench` (can the embedded host see pipeline
    progress/events? any forbidden import from a low layer up into the server?).

Include scope 9 as the load-bearing empirical probe; it is the cheapest way to
turn "reachable?" from a reading question into a compile question.

### 7.3 Stowage-specific failure classes (extend §2.2.3)

These are **bidirectional** — a violation counts in EITHER direction (server-only
*or* embedded-only). Name them in every investigator prompt, on top of the
generic four:

- **Path-divergent lifecycle.** Any stage of ingest → extract → reconcile →
  forget whose scheduler/worker/goroutine is started on only one path (e.g.
  only by the server boot, so an embedded host's writes never progress — or
  only on the embedded in-process tick, so the server relies on something the
  SDK can't drive). The flagship memory-server risk; check both directions.
- **Backend-divergent feature.** A retrieval / index / transaction / FTS /
  vector feature that works on Postgres but silently degrades or no-ops on
  sqlite, *or* vice-versa — a `Store`-seam parity violation (CLAUDE.md §9). The
  conformance suite both drivers must pass is the mechanical check.
- **One-path-only capability.** Forget / supersede / rollback / grants / any
  verb reachable from only one of {SDK, HTTP, MCP, CLI} when the model says it
  should hold for both consumers.
- **Divergent observability.** The embedded host can see pipeline
  progress/events the server doesn't expose (or the reverse) — same internal
  events, different surfaces.
- **Provenance / drill-down stranded at one layer** — the verbatim-source link
  (or any result-shaping) computed in `internal/api` rather than the retrieval
  core, so the embedded path returns a different answer than the server.

### 7.4 Doc, decision, and gate artifacts (wire the machinery to Stowage)

- **Findings doc:** file at `docs/notes/sdk-lens-findings.md` (or
  `<lens>-findings.md`) — the §2.4 synthesis; it is the program's source of
  truth.
- **Decision log:** Stowage `docs/decisions.md` is at **D-066**; the program
  entry is **D-067** (records the lens, wave structure, principles, and
  pre-reserves D-068.. one per phase). Append-only; corrections as dated notes
  inside the entry (CLAUDE.md decision rules).
- **Phase plans:** author Wave B/C plans from `docs/plans/_template.md` per the
  §16 ritual (read the cited RFC sections + research briefs first), reviewed as
  a docs PR before any implementation. Update `docs/plans/README.md` rows.
- **Gates:** every PR through `make preflight` (build + per-phase
  `scripts/smoke/phase-NN.sh` + `make drift-audit`); a new CLI/endpoint/MCP
  tool/config key needs a smoke check in the same PR (CLAUDE.md §4.2/§13).
- **Worktree discipline:** never modify `main` directly — agents work in
  isolated worktrees, branch from `origin/main`, commit-but-never-push; the
  coordinator reviews/rebases/pushes (CLAUDE.md §12 + §4.3 of this playbook).
- **Checkpoint audit (§4.5):** at each wave boundary, a read-only fan-out +
  one cross-phase integration auditor → one `chore(checkpoint)` PR; the next
  wave does not dispatch until it merges (CLAUDE.md §17).

### 7.5 The one question, Stowage-flavored (bidirectional)

Every seam, ask it both ways: ***does this hold IDENTICALLY on the
embedded-sqlite path and the server-over-Postgres path — same reachability,
same lifecycle progression, same semantics, same observability — and if not,
which side has it that the other lacks?*** A capability that exists, runs, or
behaves differently on only one path/backend — in either direction — is a
parity finding. The flagship instance for a memory server: does a write become
an extracted, reconciled, retrievable, reversibly-forgettable memory the *same
way* whether driven through `sdk/stowage` in-process or through `cmd/stowage`
over the wire?
