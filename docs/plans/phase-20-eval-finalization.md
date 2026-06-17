# Phase 20 — Eval finalization: judged QA + competitor report

- **Status:** draft
- **Owning subsystem(s):** `eval/harness`, `eval/datasets`, `eval/REPORT.md` (no
  shipped-binary surface change beyond an optional `stowage eval fetch --variant`)
- **RFC sections:** §12 (Evaluation — the public benchmark suite, the comparison
  table, the launch artifact), §10 (gateway: schema-constrained Complete), §7
  (gateway seam — reader/judge calls go through it)
- **Depends on phases:** 13 (eval harness + CI gate + full mode), 12 (rerank/SLO),
  18 (SDK/embedded — gain-fleet loop, deferred slice), h7/D-075 (bifrost full
  OpenRouter stack). **Not** dependent on Phase 19 reflection (see scope split).
- **Informing briefs:** 06 (mempalace — benchmark-led positioning, the
  comparison-table-as-launch-artifact model, verbatim vindication), 04 (CL-Bench —
  failure modes + the gain metric), 02 (LoCoMo methodology), 05 (online adaptation
  — informs the *deferred* gain-fleet slice only).

## Goal

When this phase is done, the eval harness can produce the **honest, like-for-like
LongMemEval number** competitors publish — a reader LLM answers each question from
Stowage's retrieved context and an LLM judge grades the answer against the gold
answer *semantically* — emitting a new `answer_quality` metric alongside the
existing retrieval-only `answer_context_hit`. The judged path is **opt-in and
full-mode only**; the CI mock gate (`make eval-ci`) stays deterministic, LLM-free,
and string-match-only. The harness also gains a free deterministic
normalization pass (number-word and either-direction matching) that lifts
`answer_context_hit` off its worst artifacts, and the ability to run against the
distractor-laden `longmemeval_s` haystack competitors report on. The operator runs
the full suite; the result is `eval/REPORT.md` carrying real judged accuracy and a
**comparison table vs published competitor figures** (Mem0, Zep, Letta, mempalace,
Engram) — the launch artifact (D-023/D-035).

## Why now (sequencing) and the scope split

The owner pulled Phase 20 ahead of Phase 19 (2026-06-17): the judged headline
number is the most valuable deliverable and **does not depend on reflection**. The
last n=10 full run scored `answer_context_hit=0/10` while retrieval was excellent
(right memory at rank 1 nearly every time) — every miss is a metric artifact
(paraphrase "over a year" vs gold "more than a year"; number form "five" vs "5";
or an answer needing a reasoning/aggregation step the retriever can't do but a
reader can: 17+8 postcards=25, $60/5 mugs=$12). The reader+judge is exactly what
credits these. See `eval/REPORT.md` "Re-baseline follow-up" and the retraction
section.

RFC §12 lists two things under "eval finalization" that **do** consume the
reflection→playbook loop: the **Harbor-fleet gain harness** and the
**online-adaptation (ACE) scenarios**. Because we run Phase 20 before Phase 19,
this plan **carves those out** as a deferred slice — *Phase 20b: gain-fleet +
online-adaptation* — that rides immediately after Phase 19's reflection write-side
lands. Phase 20 proper is the reflection-independent core: judged QA + the public
suite + the competitor table. This split is recorded in **D-076**.

## Brief findings incorporated

- **Brief 06 (mempalace):** benchmark-led positioning — the comparison table *is*
  the launch pitch; a memory server without published, reproducible numbers "loses
  by default." Reference points to beat/match in the table: mempalace 98.4% R@5
  LongMemEval (hybrid), 88.9% R@10 LoCoMo. We report end-to-end judged QA accuracy
  (the comparable axis) *and* retrieval R@k where competitors publish it.
- **Brief 06 (verbatim vindication):** when a judged answer is wrong but the fact
  was retrieved-but-abstracted, the P1 drill-down to the verbatim record is the
  recovery path — the reader is given retrieved memory content; a follow-up
  (deferred) feeds drill-down spans when the memory abstracted the detail away.
- **Brief 04 (CL-Bench):** the gain metric (memory-on vs memory-off delta) and its
  failure-mode taxonomy inform the deferred gain-fleet slice (20b).
- **Brief 02 (LoCoMo):** like-for-like requires the *distractor* haystack, not the
  oracle slice — Phase 20 runs `longmemeval_s` (~40–50 sessions/question) for the
  comparison row, keeping the oracle slice as a fast sanity variant.

## Findings I'm departing from

- **RFC §12 bundles the gain-fleet + online-adaptation into "eval finalization."**
  Departure: split them out as Phase 20b (post-Phase-19), because they consume the
  reflection→playbook loop that Phase 19 ships. Filed as **D-076**. The RFC §12
  text is updated in this PR to name the judged-QA mode and the split.
- **No new server config knob.** The judged path is driven by `STOWAGE_EVAL_*`
  *test/harness* env vars (the existing full-mode convention), not by
  `internal/config` profile knobs — so D-034 (profile-knob guardrail) does not
  apply. New env vars are documented in the `fullmode_test.go` header + `REPORT.md`
  and SKIP-guarded in the smoke, matching the existing `STOWAGE_EVAL_*` precedent.

## Design

### 1. Deterministic normalization of `answer_context_hit` (free, CI-safe)

`eval/harness/scores.go`. Extend `AnswerContextHit` with a normalization pass that
runs **before** the substring/token-boundary check and adds **no** model call:

- **Case-fold** (already present), **whitespace-collapse**, strip surrounding
  punctuation.
- **Number-word equivalence:** fold the small cardinals/ordinals both directions
  (`"five"↔"5"`, `"two"↔"2"`, `"first"↔"1st"`) over a fixed lookup table (0–20,
  tens, "a dozen"→12 explicitly excluded — keep the table small and exact). When
  the gold answer is a bare number or number-word, a hit counts if *either* form
  appears on a token boundary in the content.
- **Either-direction containment** for short gold answers: gold-in-content OR
  content-token-in-gold, so "under my bed" matches retrieved "under the bed" only
  when the non-stopword tokens all align — implemented conservatively as: after
  dropping a fixed tiny stopword set (`the/a/an/my/your/their/of`), gold tokens ⊆
  content tokens contiguously. (Anchored so "5" still cannot match inside "f/2.8".)

This is a deterministic proxy, not the judged metric — it stays the CI metric.
**Baseline impact:** if normalization flips any `eval/ci-fixtures` question, the
committed `eval/baselines/ci.json` is re-derived **in this PR** and the change is
called out (D-035 gate: a metric shift ships with its re-derived baseline);
`TestEvalCIGateBites` must stay biting. The normalization carries its own
table-driven unit tests + an extension to `scores_test.go`.

### 2. Reader + judge judged-QA mode (opt-in, full-mode only)

New file `eval/harness/judge.go` (build tag `fullmode`), wired into
`fullmode_test.go`. The harness already holds a live `gateway.Gateway` (`srv.gw`);
both calls go **through the gateway seam** (P5 — no provider SDK in `eval/`).

**Flow, per question, after retrieval:**

1. **Reader.** `gw.Complete` with a fixed golden prompt: the question + the
   retrieved memory contents (top-k, the same items already scored) as context,
   instructed to answer concisely or abstain ("the information provided is not
   enough"). Reader model = `STOWAGE_EVAL_READER_MODEL` (default: falls back to
   `STOWAGE_EVAL_MODEL`, the cheap learner `inception/mercury-2`). Free-text answer
   is fine here (it's the thing being graded, not parsed for control flow).
2. **Judge.** `gw.Complete` **schema-constrained** (RFC §10 — JSON-schema
   Complete; free-text JSON parsing of model output is forbidden). Schema:
   `{verdict: "correct"|"incorrect"|"partial", justification: string}`. Inputs: the
   question, the gold `expected` answer, and the reader's answer. Judge model =
   `STOWAGE_EVAL_JUDGE_MODEL` (default: falls back to `STOWAGE_EVAL_MODEL`; the
   operator may set a stronger model). The judge is prompted on *semantic*
   equivalence and on honoring abstention gold answers.
3. **Score.** `answer_quality` = (#correct + 0.5·#partial) / N, reported alongside
   `answer_context_hit`. Per-question result rows gain `reader_answer`,
   `judge_verdict`, `judge_justification`. The judged path is entered only when
   `STOWAGE_EVAL_JUDGE=1`; otherwise full mode behaves exactly as today.

**Golden stability (§11).** The reader/judge *prompt assembly* is covered by a
golden test (fixed question + fixed context + fixed gold → fixed prompt string and
fixed judge JSON-schema), so prompt drift is caught without a live call. The live
verdict itself is non-deterministic and is *not* asserted in CI.

`Scores` gains `AnswerQuality float64` + `JudgedCount int` (omitempty / pointer so
the CI JSON is byte-unchanged when judging is off — protects the committed CI
baseline shape).

### 3. The `longmemeval_s` distractor haystack

The fetcher already honors `STOWAGE_EVAL_LONGMEMEVAL_URL`
(`eval/datasets/longmemeval/fetcher.go`), so the `_s` haystack needs **no code
change** — the operator points it at `longmemeval_s.json`. For ergonomics + a
recorded checksum per variant, add an **optional** `stowage eval fetch --variant
oracle|s` flag (default `oracle`, preserving current behavior) that selects the
URL and writes a variant-suffixed filename. *Because this touches the CLI surface,
it ships with a same-PR smoke check* (§4.2). If the flag is judged not worth the
surface at implementation time, it is dropped and the env override documented
instead — a recorded reasonable deviation (§4.3).

### 4. The competitor comparison table + `eval/REPORT.md`

The operator runs the full suite (needs `OPENROUTER_API_KEY`; never CI). The
report (PR #3 below) replaces the ⚠️ pre-BUG-4 rows with corrected figures and adds:

- LongMemEval judged QA accuracy (`answer_quality`) on `longmemeval_s`, n recorded,
  with the reader+judge models named.
- The retrieval-only `answer_context_hit` (normalized) as the deterministic proxy.
- LoCoMo (judged where the harness supports it; retrieval R@k otherwise).
- A **comparison table** vs published Mem0 / Zep / Letta / mempalace / Engram
  numbers, each cited to its source, with an explicit note on metric comparability
  (oracle vs `_s`, retrieval vs end-to-end QA).
- Per-question result files committed under `eval/results/`.

### Delivery as three PRs (this plan gates all three)

- **PR #1 — this docs PR** (plan + roadmap recut + D-076 + glossary + RFC §12/§15).
  Owner reviews and merges before implementation. ← we stop here.
- **PR #2 — harness implementation** (normalization, judge.go, golden tests, smoke,
  optional fetch flag, re-derived ci.json if needed). Gated by full CI + smoke.
  Carries no live numbers.
- **PR #3 — report population** (operator full-mode run → `eval/REPORT.md` rows +
  committed `eval/results/*.jsonl` + the competitor table). Operator-run numbers.

## Files added or changed

```text
eval/harness/judge.go            # new (//go:build fullmode): reader + schema-constrained judge
eval/harness/judge_test.go       # new: golden prompt-assembly + judge JSON-schema test
eval/harness/scores.go           # normalization pass; AnswerQuality/JudgedCount on Scores
eval/harness/scores_test.go      # number-word + either-direction normalization tables
eval/harness/fullmode_test.go    # wire the judged path behind STOWAGE_EVAL_JUDGE
eval/datasets/longmemeval/fetcher.go  # (optional) --variant oracle|s support
cmd/stowage/main.go              # (optional) eval fetch --variant flag + usage
eval/baselines/ci.json           # re-derived ONLY if normalization flips a CI fixture
eval/REPORT.md                   # PR #3: judged rows + competitor table
scripts/smoke/phase-20.sh        # new
docs/plans/README.md             # roadmap recut (launch = 01–27, v0.1 after hardening)
docs/plans/phase-20-eval-finalization.md  # this file
RFC-001-Stowage.md               # §12 judged-QA metric + split; §15 launch = v0.1
docs/decisions.md                # D-076
docs/glossary.md                 # reader, LLM judge, answer_quality, judged-QA, longmemeval_s
```

## Config keys added

None — the judged path is driven by `STOWAGE_EVAL_*` test/harness env vars, not
`internal/config` profile knobs (so D-034 does not apply). New env vars:

| Env var | Default | Notes |
|---------|---------|-------|
| `STOWAGE_EVAL_JUDGE` | unset (off) | `=1` enables the reader+judge path (full mode only) |
| `STOWAGE_EVAL_READER_MODEL` | → `STOWAGE_EVAL_MODEL` | reader that answers from retrieved context |
| `STOWAGE_EVAL_JUDGE_MODEL` | → `STOWAGE_EVAL_MODEL` | judge that grades vs gold (schema-constrained) |
| `STOWAGE_EVAL_LONGMEMEVAL_URL` | oracle URL (existing) | point at `longmemeval_s.json` for the distractor haystack |

## Acceptance criteria (binding)

**Implementation (PR #2, gated by CI + smoke):**

1. `make eval-ci` is byte-for-byte unchanged in behavior: deterministic, mock
   gateway, no LLM call, string-match metric; `TestEvalCIGateBites` still bites.
2. Normalization is pure/deterministic and table-tested: `"five"`↔`"5"` and
   `"under my bed"`↔`"under the bed"` count as hits; `"2"` still does **not** match
   inside `"f/2.8"`. If any CI fixture flips, `eval/baselines/ci.json` is re-derived
   in the same PR and the delta is noted.
3. The judged path is **opt-in**: with `STOWAGE_EVAL_JUDGE` unset, full mode is
   behaviorally identical to today (no reader/judge calls; CI JSON shape unchanged).
4. The judge call is **schema-constrained** via `gw.Complete` (no free-text JSON
   parsing of model output — §10); the reader/judge prompt assembly + judge schema
   are covered by a **golden test** with no live call.
5. No provider SDK import or hand-built provider request anywhere under `eval/`
   (P5): reader/judge route through `gateway.Gateway`.
6. If the `eval fetch --variant` flag ships: `stowage eval fetch --variant s`
   selects the `_s` URL and writes a distinct checksummed file; `--variant oracle`
   (default) reproduces today's behavior — both covered by the smoke.
7. `make build` (CGo-free), `go test -race ./...`, `golangci-lint run`,
   `gofmt -l .` empty, `make coverage`, `make preflight`, `make drift-audit`,
   `make check-mirror` all green. `go vet -tags=fullmode ./eval/harness/` compiles.

**Report (PR #3, operator-run):**

8. `eval/REPORT.md` carries a real `answer_quality` judged figure on
   `longmemeval_s` (n + reader/judge models named), the normalized
   `answer_context_hit`, committed per-question results, and a competitor
   comparison table with cited sources and an explicit comparability note.
9. Every number has a one-command reproduction documented in the report.

## Smoke script

`scripts/smoke/phase-20.sh` — SKIP-graceful before PR #2 lands; once present:

- `OK` judged path is opt-in: `fullmode_test.go` gates the reader/judge on
  `STOWAGE_EVAL_JUDGE` (grep).
- `OK` no provider SDK imported under `eval/` (grep deny-list — P5).
- `OK` judge uses schema-constrained Complete (grep for the schema wiring; assert
  no `json.Unmarshal` over raw model text in `judge.go`).
- `OK` normalization unit tests pass (`go test -run TestNormalize|TestAnswerContextHit ./eval/harness/`).
- `OK` judge prompt-assembly golden test passes.
- `OK` full-mode build compiles (`go vet -tags=fullmode ./eval/harness/`).
- `OK` (if flag shipped) `stowage eval fetch --variant` accepted; bad variant rejected.
- `OK` `make eval-ci` still green (deterministic CI metric unaffected).

## Test plan

- **Unit / table:** normalization (number-word both directions, either-direction
  containment, the `f/2.8` non-match guard), `scores_test.go` extension.
- **Golden:** reader prompt assembly, judge prompt assembly + judge JSON schema
  (fixed input → fixed output) — `judge_test.go`. No live call in CI.
- **Determinism:** assert CI run + `ci.json` gate (existing `TestEvalCIGate*`),
  plus a test that `Scores` JSON is unchanged when judging is off.
- **Compile:** `go vet -tags=fullmode ./eval/harness/` in smoke (the judged path
  only compiles under the tag).
- **Live (operator, not CI):** the full `STOWAGE_EVAL_JUDGE=1` run on
  `longmemeval_s` produces the report numbers (PR #3).
- **Integration (§17):** the judged path consumes the retrieval surface end-to-end
  through the live server in `fullmode_test.go` (real store + real gateway) — the
  existing full-mode test *is* the cross-subsystem integration boundary.

## Risks & mitigations

- **CI determinism regression** from normalization flipping fixtures → re-derive
  `ci.json` in PR #2, keep `TestEvalCIGateBites` biting, assert off-path JSON shape.
- **Judge leniency/strictness skews `answer_quality`** → golden-stable prompt; the
  judge schema forces a verdict enum + justification; the operator spot-audits a
  sample of verdicts and the report commits per-question justifications for
  inspection. (A 2-of-3 judge panel is a possible PR-#2 hardening, noted not
  required.)
- **Spend / key exposure** → judged mode is opt-in and operator-run only; the key
  lives in a gitignored `.env`, never printed/committed; CI never sets
  `STOWAGE_EVAL_GATEWAY`.
- **Fail-slow on a bad key** (known wart: every call 401s, settle loop waits 5m) →
  carry forward the "fail-fast on repeated gateway errors" guard as a small PR-#2
  add (abort the run after N consecutive gateway errors) so a misconfigured run
  dies in seconds, not ~10 min.
- **Known full-run validity gaps** (REPORT.md): reconcile-side dead-letters aren't
  replayed; thinking-model token-budget truncation. These bit prior runs — PR #2
  adds a live budget-headroom log assertion; dead-letter replay is tracked as a
  hardening follow-up (Phase 21/h-series) unless it blocks a clean `_s` run.
- **`longmemeval_s` is ~10× the oracle data** → longer settle; the full-mode
  `WaitForQuiescence` hard barrier + production-like triggers already handle this;
  bump `STOWAGE_EVAL_SETTLE_TIMEOUT` for the big run.

## Glossary additions

- **Reader (eval)** — the LLM that answers an eval question from Stowage's
  retrieved context, in judged-QA mode.
- **LLM judge** — the schema-constrained LLM that grades a reader answer against the
  gold answer semantically, emitting a `correct/incorrect/partial` verdict.
- **answer_quality** — the judged end-to-end QA metric: (correct + 0.5·partial) / N
  — the figure comparable to competitors' published LongMemEval accuracy.
- **Judged-QA mode** — the opt-in, full-mode-only reader+judge eval path; distinct
  from the deterministic retrieval-only `answer_context_hit`.
- **longmemeval_s** — the distractor-laden LongMemEval haystack (~40–50
  sessions/question) competitors report on; the like-for-like comparison variant.

## Decisions filed

- **D-076** — Roadmap recut: v0.1 launch after the hardening of phases 22–27 (no
  launch at 21); Phase 20 (judged QA + competitor table) pulled ahead of Phase 19;
  the RFC §12 gain-fleet + online-adaptation slice that consumes the
  reflection→playbook loop deferred to **Phase 20b**, post-Phase-19;
  `answer_quality` (reader+judge) defined as the competitor-comparable launch
  metric.
