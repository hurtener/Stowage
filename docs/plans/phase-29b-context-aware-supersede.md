# Phase 29b — Context-aware reconciliation (raw turns in the supersede decision)

- **Status:** draft
- **Owning subsystem(s):** `internal/reconcile`
- **RFC sections:** §6 (Reconciliation — the forget machinery)
- **Depends on phases:** 08 (reconcile), 29 (consolidation hardening — D-104/D-106/D-107)
- **Informing briefs:** reconcile/forgetting briefs (Pearce-Hall supersede); the LongMemEval
  miss analysis ([[longmemeval-miss-analysis]]).

## Goal

The reconcile supersede/merge decision is an LLM call that today sees **only the memories** —
the candidate plus its structural/semantic neighbor memories (`BuildUserPrompt(c, neighbors)`),
with no conversational context. So when two memories state different values, the model cannot
tell a *correction of one fact* from *two distinct facts*, and over-supersedes (the commute
"45 min each way" vs "30 min" false-merge: they are arguably the audiobook-listening time vs the
work-commute time, but the model had no turns to disambiguate). When this phase ships, the
decision prompt also carries the **original conversation turns** behind the candidate and behind
each neighbor, so the model distinguishes correction-vs-distinct-fact and over-supersede drops.

## Brief findings incorporated

- The miss analysis showed the residual reconcile errors are over-supersede / sequential-state,
  not winner-selection (fixed by D-106). Detection (H4) would *increase* false-supersedes; the
  right lever is giving the existing decision more **context**, not more aggression.

## Findings I'm departing from

- Not adding H4 semantic-detection here (double-edged on this evidence). Not adding a blunt
  "don't merge distinct facts" heuristic — the LLM with raw turns is the principled judge.

## Design

`ReconcileStage` gains a `recs store.RecordStore` handle (optional; nil ⇒ current behaviour,
no context block — degrade-safe). In `processCandidate`, after neighbors are resolved and
before `BuildUserPrompt`, assemble a bounded **conversation-context** block:

1. **Candidate turns:** `recs.GetMany(scope, ids)` for the candidate's provenance record IDs
   (the raw turns the candidate was extracted from).
2. **Neighbor turns:** for each neighbor passed to the prompt (already bounded by the neighbor
   limit, ≤ ~8), `mem.GetJunctions(neighbor.ID).Provenance[].RecordID` → `recs.GetMany`. This
   shows the model the *original wording* of the existing value it might retire.
3. **Bounding (P2/cost):** cap total context records (`maxContextRecords`, e.g. 12) and per-record
   content length; dedupe record IDs across candidate+neighbors. A fetch error degrades to
   "no context block" (never fails the decision). Reconcile is already async (off the ingest ACK).

`BuildUserPrompt` gains a third argument (the context records, keyed by which memory they back)
and renders an "## Original conversation context" section: the candidate's source turns and, per
neighbor, its source turns, each labelled with the memory it supports. The system prompt gains a
rule: "Use the original conversation context to decide whether the candidate CORRECTS the
neighbor's fact (→ supersede/update) or states a DIFFERENT fact that merely shares words (→ add).
When in doubt that they are the same fact, prefer add."

No schema change, no new store method (GetMany/GetJunctions/ListBySession already exist). The
optional preceding-turn window (records just before the candidate in the same session via
`ListBySession`) is a follow-up refinement, not in this phase.

## Files added or changed

```text
internal/reconcile/reconcile.go    # recs handle; context assembly in processCandidate
internal/reconcile/prompt.go       # BuildUserPrompt context section + system-prompt rule
internal/boot/boot.go              # wire RecordStore into ReconcileStage
eval/harness/server.go             # wire RecordStore into the harness reconcile stage
docs/decisions.md docs/glossary.md
scripts/smoke/phase-29b.sh
```

## Config keys added

None. (Context enrichment is on whenever a RecordStore is wired; bounded by internal caps.)

## Acceptance criteria (binding)

1. `BuildUserPrompt` renders an "Original conversation context" section with the candidate's
   source turns and each neighbor's source turns when records are supplied; renders unchanged
   (no section) when nil — golden-tested.
2. The reconcile decision system prompt instructs correction-vs-distinct-fact disambiguation
   from the conversation context.
3. Context assembly is bounded (≤ `maxContextRecords`) and degrade-safe (a record-fetch error
   logs and proceeds with no context block — the decision still runs).
4. Reconcile remains reversible (D-070): supersede/merge driven with context still round-trips.
5. Re-learn + re-test: the commute false-supersede no longer fires (45-each-way retained), with
   no net regression on the K=15/30 judged score vs the Phase-29 result.
6. `make preflight` + drift-audit + mirror green; reconcile coverage target met.

## Smoke script

`scripts/smoke/phase-29b.sh` — BuildUserPrompt with/without context records (section present/
absent); system prompt carries the disambiguation rule; reconcile package tests pass.

## Test plan

Unit/golden: `BuildUserPrompt` context section (with records / nil); system-prompt rule present.
Unit: context assembly bounding + dedupe + degrade-on-fetch-error. Integration (§17, real
drivers, `-race`): a candidate that corrects a neighbor supersedes WITH context and round-trips
rollback; a candidate that shares words but is a distinct fact is NOT superseded. Eval: re-learn,
re-test K=15/30, audit the commute pair.

## Risks & mitigations

- **Prompt bloat / cost** → hard cap on records + content length; async path.
- **Context still ambiguous** → falls back to the model's best judgement; no worse than today.
- **N+1 GetJunctions for neighbors** → neighbors already bounded (≤ ~8); acceptable async.

## Glossary additions

- **Conversation context (reconcile)** — the raw provenance turns of the candidate and its
  neighbors, supplied to the supersede/merge decision so the model distinguishes a correction
  from a distinct fact (Phase 29b).

## Decisions filed

- D-108: The reconcile supersede/merge decision is context-aware — it sees the candidate's and
  neighbors' original conversation turns, not just the derived memories.
