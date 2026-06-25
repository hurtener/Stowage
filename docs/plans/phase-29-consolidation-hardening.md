# Phase 29 — Consolidation hardening (stale-value forgetting + extraction context)

- **Status:** in-progress — write-time core (H1, H2, H3) + read-time H5 (dual-visibility) shipped; H0/H4 deferred; 2nd re-learn + re-test (K=10/15/30/40) in flight
- **Owning subsystem(s):** `internal/pipeline` (extract), `internal/reconcile` (prompt + prefilter), `internal/retrieval` + `internal/scoring` (read-time staleness surfacing), `internal/config`
- **RFC sections:** §6 (Reconciliation — the forget machinery), §6c (Trust / calibrated uncertainty), §4.2 (retrieval), §5.2 (scoring)
- **Depends on phases:** 08 (reconcile), 09–12 (retrieval/scoring/rerank), 25 (verification), 26 (reasoning traces — reused for H0 instrumentation)
- **Informing briefs:** reconcile/forgetting briefs per `docs/research/INDEX.md` (A4 semantic-neighbor recall; Pearce-Hall supersede); retrieval scoring briefs.

## Goal

When this phase ships, a corrected fact retires its stale predecessor instead of co-existing
with it: numeric/quantity corrections ("120→125 stars", "6→9 months", "30 min → 45 min each
way") drive a **supersede**, not a silent near-dup discard or an `add`; extracted memories
retain their disambiguating qualifiers/units/context so the same fact is detected as the same
fact; and when a superseded value is still retrieved it is **surfaced to the reader flagged as
stale with a link to the current value** (both stay visible — §6c calibrated uncertainty — but
the agent can tell which is current). Diagnosed against the LongMemEval miss class where stale
values were never forgotten and the older/vaguer copy outranked the newer/specific one.

## Brief findings incorporated

- **A4 semantic-neighbor recall** already wired (`augmentWithVectorNeighbors`) — H4 is therefore
  *deferred*, not net-new; the gap for the cited misses is the **decision**, not recall.
- **Pearce-Hall / P4 forgetting:** a contradiction must retire the stale value; the lexical
  near-dup auto-discard must not swallow a correction.

## Findings I'm departing from

- The original analysis assumed read-time scoring was the primary lever. **Adversarial review
  corrected this:** the read path already filters `status='active'`, so this is overwhelmingly a
  **write-time** failure (supersede never fires). Read-time work (H5) is a *mitigation*, not the
  primary fix, and per the human decision is reframed from "collapse/drop the stale value" to
  "**retain and flag** it" (§6c dual-visibility).
- Freshness/recency *booster* and the contradiction/lingering *diminishers* from the first draft
  are **dropped**: query-independent recency buries valid stable facts, and co-present pairs carry
  no contradiction signal until supersede fires. Only a small confidence booster + staleness
  surfacing survive.

## Design

Five changes (H0–H3, H5; H4/H6/H7 deferred). **Human decisions baked in:** scope = {H0,H1,H2,H3,H5};
recency basis = `Record.occurred_at` (when said, not when extracted); contradictions = **both
values stay retrievable, the superseded one flagged stale with a successor link**.

### H0 — Instrument reconcile decisions (diagnostic; ship first, read-only)
Extend the memory_trace export (D-086) so a reconcile decision records, per candidate:
the neighbor set (structural ∪ semantic), which lane surfaced each, the `BigramJaccard` vs each
neighbor, the LLM action chosen (add/update/merge/supersede/discard/park), and the target trust
tier. Add an eval-harness pass that, over a learned corpus, reports per known-contradiction-pair
whether the failure was *recall* (never co-shown), *decision* (LLM chose add), or *near-dup
discard*. This re-prioritizes H1–H4 and gates H4. No gateway change; no schema change beyond the
existing event payload.

### H1 — Extraction quality + canonical fact-key (pipeline)
1. **Prompt** (`internal/pipeline/prompt.go`): instruct that `content` MUST retain quantitative
   qualifiers/units/scope ("each way", "gross vs net", "per week") and `context` MUST carry the
   disambiguating conversational frame. Add a worked example (the commute "each way" case). Bump
   `PromptTemplateVersion`; regenerate goldens.
2. **Canonical quantity key:** when a candidate asserts a measurement, emit a canonical
   subject/unit junction key (e.g. `commute_duration`) into the existing keyword/entity index so
   the *existing structural* `FindNeighbors` catches numeric contradictions with **zero embedding
   cost**. (Schema: reuse the keyword junction — no new table; if a dedicated column is needed it
   requires an RFC §8.1 amendment first, D-024.)
3. **Coarser buffer window** (assistant profile): raise the eval-tuned flush constants (D-042;
   ~count 12→18, ~maxAge 90s→180s) so memories are fewer and better-contextualized. Config-tunable.

### H2 — Reconcile decision prompt (reconcile/prompt.go)
Replace rule 1 ("Prefer 'add' when no neighbor shares the same subject or claim") with explicit
"**same subject + different value ⇒ supersede the older**" guidance, and feed **assertion recency
(`occurred_at`)** of each neighbor into `BuildUserPrompt` so the model knows which to retire. Add
quantity-normalization guidance (compare the numeric value, not the surface string). Golden-test
the prompt; pair with the false-supersede metric (below) so stronger guidance can't silently erase
correct memories. Decision still routes through the reversible `Commit` (D-070).

### H3 — Near-dup numeric-correction guard (reconcile/reconcile.go, prefilter)
Before the `BigramJaccard ≥ nearDupThreshold` auto-discard (`reconcile.go:287`), check whether the
candidate and the near-dup neighbor carry **divergent numerals for the same unit** (a cheap numeric
token diff). If so, **do not discard and do not bump the neighbor's `match_count`** — fall through
to the LLM decision (supersede path). Deterministic; no cosine floor; no embedding cost. This
closes the documented blind spot (the existing comment wrongly assumes a contradiction is never
lexically near-identical — a one-digit numeric correction is).

### H5 — Read-time staleness surfacing (retrieval + scoring + reader)
Per the human decision (**retain-and-flag, not collapse**):
1. **Supersede retains-and-flags:** when reconcile supersedes, the old memory keeps a retrievable
   lifecycle state (`status='superseded'` + `superseded_by_id`), reversible as today.
2. **Retrieval surfaces lifecycle state:** retrieved items carry a `stale`/`current` annotation and
   the `superseded_by` handle; a recently-superseded memory is *included but demoted* when its
   successor is also retrieved (so the agent sees the pair), instead of being hard-filtered. The
   store query stays **scoped** (P3). (Open: always-include vs only-when-successor-co-retrieved —
   resolve in implementation behind a config flag, default conservative.)
3. **Small confidence booster** at scoring (no freshness booster), `occurred_at` reached via a
   scoped provenance→records join (not a new Memory column).
4. **Reader prompt:** when two values of the same fact appear and one is flagged stale/superseded,
   prefer the current value and may note the history. Golden-test the prompt.

## Files added or changed

```text
internal/pipeline/prompt.go            # H1 extraction prompt + version bump
internal/pipeline/candidates.go        # H1 canonical fact-key emission
internal/pipeline/triggers.go|config   # H1 coarser buffer window (config-tunable)
internal/reconcile/prompt.go           # H2 decision prompt (supersede bias + occurred_at)
internal/reconcile/reconcile.go        # H3 numeric-correction guard before near-dup discard
internal/reconcile/trace*.go           # H0 decision-trace fields (reuse memory_trace/D-086)
internal/retrieval/retrieval.go        # H5 lifecycle-state surfacing + scoped include
internal/scoring/scoring.go            # H5 confidence booster (occurred_at via provenance join)
eval/harness/*                         # H0 contradiction-pair report; reader prompt (H5)
internal/config/config.go              # H1 buffer window knobs; H5 stale-include flag
docs/decisions.md docs/glossary.md     # D-entries + vocab
scripts/smoke/phase-29.sh              # new
```

## Config keys added

| Key | Default | Notes |
|-----|---------|-------|
| `pipeline.buffer.count` (assistant profile) | 18 | H1 coarser window (was ~12); D-042 |
| `pipeline.buffer.max_age_sec` (assistant) | 180 | H1 (was ~90); D-042 |
| `retrieval.include_superseded` | true | H5 dual-visibility; demoted + flagged |

(Each ships with a tuned default in every profile + docs + smoke — D-034.)

## Acceptance criteria (binding)

1. A numeric correction (candidate "...125 stars" vs neighbor "...120 stars") is NOT auto-discarded
   and does NOT bump the neighbor's match_count; it reaches the LLM decision (H3).
2. The reconcile decision prompt golden contains the "same subject + different value ⇒ supersede"
   rule and surfaces neighbor `occurred_at` (H2).
3. Extraction prompt golden requires qualifier/unit/scope retention; `PromptTemplateVersion` bumped;
   a unit-bearing candidate emits its canonical fact-key (H1).
4. After a supersede, the superseded memory is still retrievable and its retrieved item is flagged
   `stale=true` with a `superseded_by` handle; the current value is flagged `current` (H5).
5. Reconcile decisions remain reversible: a supersede driven by H2/H3 round-trips via rollback
   (D-070) — binding integration test, real drivers, `-race`.
6. New CI-tracked metrics exist: contradiction-co-presence rate, stale-survival rate, false-supersede
   rate (wired to the D-035 gate).
7. `make preflight` + drift-audit + mirror green; coverage on touched packages met.

## Smoke script

`scripts/smoke/phase-29.sh` — config explain shows the new knobs + invalid rejection; reconcile
prompt golden present; extraction prompt version bumped; numeric-guard unit asserts no-discard;
retrieved superseded item carries the stale flag.

## Test plan

Unit: numeric-divergence detector; canonical-key emission; staleness annotation. Golden: reconcile
decision prompt, extraction prompt, reader prompt. Integration (§17, real drivers, `-race`):
supersede→rollback round-trip; scoped retrieval surfaces stale flag with scope propagation. Eval:
H0 contradiction-pair report; re-learn once; re-test K=5/10/20/50 + the three new metrics.

## Risks & mitigations

- **Over-eager supersede erases a correct memory** → false-supersede CI metric + reversible commit
  + rollback test; never same-tier auto-apply outside the LLM decision.
- **Coarser window merges two distinct facts** → over-broad-merge eval metric; middle-out token
  clamp as follow-up.
- **Including superseded inflates slots/noise** → demote + flag + config flag; only co-retrieve with
  successor by default.
- **gpt-5.4-nano confidence uncalibrated** → confidence booster weight kept small; calibration check
  before it gets decisional weight.

## Glossary additions

- **Stale flag / lifecycle state** — a retrieved item's `current`/`superseded` annotation + successor
  handle (H5; §6c dual-visibility).
- **Canonical fact-key** — a subject/unit junction key that lets structural neighbor search catch
  numeric contradictions without embeddings (H1).

## Decisions filed

- D-104: Numeric corrections bypass the lexical near-dup auto-discard (route to supersede). **Filed.**
- D-107: Assistant extraction buffer window coarsened for context retention. **Filed.**
- D-105: Superseded memories are retained-and-flagged in retrieval (dual-visibility, §6c), not hidden. **Filed.**
- D-106: Reconcile winner-selection is deterministic by assertion order. **Filed** — within-flush candidates are sorted by latest source-record ULID (turn order) so the newer value supersedes the older. `occurred_at` proved session-granular (ties); record-ULID is finer.
