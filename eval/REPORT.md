# Stowage — Benchmark Report

> The launch artifact (D-023/D-035): reproducible numbers on the public memory
> benchmark suite, with committed per-question results. Comparison table vs
> published competitor figures lands with the launch run (Phase 20).

## How to reproduce

- CI subset (deterministic, mock gateway): `make eval-ci`
- Full mode (real models): see the header of `eval/harness/fullmode_test.go`
- Datasets: `./bin/stowage eval fetch --dataset longmemeval|longmemeval_s|locomo`

### The public-benchmark runner (D-096)

Every public benchmark flows through one path — `harness.RunDataset` — selected by
name through the dataset **registry** (`eval/datasets`): the dataset is a parameter,
not a forked runner. Adding a benchmark is a new `eval/datasets/<name>/` package
(a `Fetch` + a `Normalize`) plus one `datasets.Register` call in its `init()`; the
runner, the CLI `eval fetch`, and the full-mode entry pick it up automatically.

Registered today: **longmemeval** (oracle), **longmemeval_s** (distractor haystack —
the headline variant), **locomo**. The dataset→runner wiring is proven in CI with the
mock gateway over a scripted extraction (`TestRunDataset_Wiring`, `TestDatasetRegistry`);
the benchmark **numbers** are operator-run (a live gateway, never CI).

Operator runs (select with `STOWAGE_EVAL_DATASET`):

```bash
# Fetch once, then run the full reader+judge pipeline (see fullmode_test.go header for
# the STOWAGE_EVAL_* gateway env block):
./bin/stowage eval fetch --dataset longmemeval_s
STOWAGE_EVAL_DATASET=longmemeval_s STOWAGE_EVAL_JUDGE=1 STOWAGE_EVAL_LIMIT=50 … \
  go test -tags=fullmode -run TestFullMode -timeout 90m ./eval/harness/

./bin/stowage eval fetch --dataset locomo
STOWAGE_EVAL_DATASET=locomo STOWAGE_EVAL_JUDGE=1 … \
  go test -tags=fullmode -run TestFullMode -timeout 90m ./eval/harness/

# Gain (memory-ON vs memory-OFF) over a dataset's questions:
STOWAGE_EVAL_DATASET=longmemeval_s STOWAGE_EVAL_GAIN=1 STOWAGE_EVAL_LIMIT=20 … \
  go test -tags=fullmode -run TestGainDatasetMode -timeout 90m ./eval/harness/
```

Results land in `eval/results/<dataset>-n<N>-<ts>.jsonl` (and
`gain-<dataset>-n<N>-<ts>.jsonl`). Record headline numbers in the sections below.

## Metric definition

Two metrics, by design (Phase 20, D-076):

`answer_context_hit` — the **deterministic, LLM-free CI metric**: the gold answer
appears in the content of the retrieved memories (case-insensitive). Short answers
(< 4 runes) match on token boundaries with joining-punctuation handling, so "2"
cannot match inside "f/2.8". Phase 20 added deterministic normalization:
number-word equivalence both directions ("five"↔"5", boundary-matched so "8" never
matches inside "weight") and either-direction stopword-tolerant phrase match
("under my bed"↔"under the bed"). This is a RETRIEVAL-ONLY proxy: no reader, no
judge. **It is NOT comparable to published LongMemEval accuracy figures** — several
question classes (abstention, preference, temporal composites) have full-sentence
gold answers that can never substring-match on ANY system.

`answer_quality` — the **judged end-to-end QA metric**, comparable to competitors'
published accuracy. A reader LLM answers from the retrieved context; an LLM judge
grades the answer against the gold answer semantically (correct = 1, partial = ½,
incorrect = 0); `answer_quality = (correct + ½·partial) / N_judged`. The judge call
is JSON-schema-constrained through the gateway seam (RFC §10). Opt-in
(`STOWAGE_EVAL_JUDGE=1`), full-mode only, operator-run — **never in CI**.

## Judged-QA result (2026-06-17, D-076) — the headline number

First judged run with the Phase 20 reader+judge path. Full bifrost/OpenRouter
stack (D-075): memory formation + reader + judge on `inception/mercury-2`, embed
`perplexity/pplx-embed-v1-0.6b` @ 1024d, rerank `cohere/rerank-4-fast`
(precise profile). LongMemEval **oracle** (cleaned), n=10.

| Metric | Value | Notes |
|---|---|---|
| **`answer_quality` (judged)** | **0.556 (5/9)** | reader+judge; the competitor-comparable axis |
| `answer_context_hit` (normalized, retrieval-only) | 0.20 (2/10) | deterministic proxy; understates quality |
| judged_count | 9/10 | 1 question dropped on a transient empty reader response (see below) |
| p50 / p95 retrieve latency | 536 ms / 2293 ms | local dev box, not the SLO rig |

Run validity: pipeline fully quiescent before scoring (87 active memories from 10
conversations); Probe passed (fail-fast guard armed). Results:
`eval/results/longmemeval-n10-20260617T230629Z.jsonl`.

**The judged metric more than doubles the retrieval-only number (0.556 vs 0.20),
and the judge is discriminating — not rubber-stamping.** Per-question:

| Question | gold | reader answer | verdict | why the substring metric missed it |
|---|---|---|---|---|
| 001be529 | over a year | more than a year | ✅ correct | paraphrase |
| 0100672e | $12 | $12 per coffee mug | ✅ correct | gold not verbatim in a memory |
| 01493427 | 25 | 25 new postcards | ✅ correct | (also a substring hit) |
| 06db6396 | 5 | five | ✅ correct | number form — now also a normalized hit |
| 06f04340 | (preference: homegrown-produce dinners) | dishes using your cherry tomatoes, basil, mint… | ✅ correct | full-sentence gold; synthesis answer |
| 00ca467f | 2 | One | ❌ incorrect | genuine reasoning error (miscount) |
| 031748ae | (temporal: led 4, now 5) | partial/unspecified | ❌ incorrect | genuine — composite not fully answered |
| 031748ae_abs | (abstention) | "You lead four engineers." | ❌ incorrect | genuine — failed to abstain |
| 07741c44 | under my bed | In a shoe rack. | ❌ incorrect | genuine — wrong location |
| 06878be2 | (preference: Sony accessories) | (empty) | — not judged | transient empty reader response |

The 4 ❌ are real failures (a miscount, a missed abstention, a temporal composite,
a wrong retrieval/read) — the judge correctly rejects them. The 1 unjudged question
is a transient `nil content in response message` from the reader model (1
consecutive; the 5-error fail-fast did not trip); a re-run would judge it.

### Comparability + competitor reference points (NOT yet apples-to-apples)

This is an **n=10 oracle** run; published competitor LongMemEval accuracy is on the
**`longmemeval_s` distractor haystack (~500 questions, ~40–50 sessions each)**. The
two are not directly comparable — a larger `_s` run is the remaining operator
follow-up before claiming a head-to-head. Reference figures (end-to-end QA accuracy,
from each project's published materials; verify before quoting in launch copy):

| System | LongMemEval (published) | Notes |
|---|---|---|
| Stowage (this run) | **0.556 judged** | n=10 **oracle**, mercury-2 reader+judge — preliminary |
| mempalace | ~98.4% R@5 (retrieval) | retrieval recall, not end-to-end QA |
| Mem0 / Zep / Letta / Engram | (varies; cite per source) | end-to-end QA on `_s`; pull exact figures with the `_s` run |

**Honest read:** 0.556 on a cheap reader (mercury-2) over oracle context is an
encouraging first judged signal — and the gap to `answer_context_hit` confirms the
0/10–0.20 retrieval-only numbers were a metric artifact, exactly the Phase 20
thesis. It is **not** a launch claim yet: the like-for-like `_s` run (and likely a
stronger judge/reader) is required for the competitor table.

## Valid baseline (2026-06-12, run #6)

> ⚠️ **Computed pre-BUG-4 (D-069); lexical/queries lanes inactive — pending
> re-baseline.** This run (2026-06-12) predates the BUG-4 FTS fix (2026-06-13),
> so the sqlite lexical + queries lanes hard-errored on the "?"-terminated
> LongMemEval questions and silently dropped out — these numbers reflect
> vector + structured retrieval only. A full real-gateway re-run is Phase-20
> scope (needs an API key); see "Re-baseline follow-up" below.

| Dataset | n | answer_context_hit | p50 | p95 | Gateway |
|---|---|---|---|---|---|
| LongMemEval oracle (cleaned) | 10 | **0.30** (3/10) ⚠️ pre-BUG-4 | 573 ms | 733 ms | openaicompat → OpenRouter (extract gemini-3.5-flash, embed gemini-embedding-2 3072d) |

Run validity, verified: pipeline fully quiescent before scoring (66 active
memories from 10 conversations, zero unprocessed records, zero dead
letters); retrieved items query-discriminating (40 distinct memories across
the 50 retrieval slots); spot-read topicality on every question.

Miss breakdown (committed per-question results):

- 3–4 of 7 misses are metric artifacts: abstention/preference questions
  whose gold answers are full sentences, and one temporal composite
  ("led 4 engineers, now 5") that no single retrieved memory can contain.
- 1 is point-of-view normalization: retrieved "stores their old sneakers
  under their bed" vs gold "under my bed" — semantically correct.
- 2 are real extraction-granularity gaps: numeric details ("2 appointments
  in March", "$12 per mug") were abstracted away during extraction while
  the surrounding fact was captured. Both are recoverable via P1 drill-down
  to verbatim records — the drill-down-aware scorer is the follow-up.

A reader+judge over this run's retrieved context would plausibly score
7–8/10; that mode (Phase 20) is the honest competitor-comparison number.

Results: `eval/results/longmemeval-n10-20260612T180501Z.jsonl`

## n=50 confirmation (2026-06-12, run #7)

> ⚠️ **Computed pre-BUG-4 (D-069); lexical/queries lanes inactive — pending
> re-baseline.** Same caveat as run #6: the lexical + queries lanes were dead
> for the "?"-terminated questions in this run.

| Dataset | n | answer_context_hit | p50 | p95 | Gateway |
|---|---|---|---|---|---|
| LongMemEval oracle (cleaned) | 50 | **0.20** (10/50) ⚠️ pre-BUG-4 | 517 ms | 1025 ms | same as run #6 |

Validity: 299 active memories from 50 conversations, zero unprocessed
records at scoring, 160 distinct retrieved memories across 250 slots.
15 dead letters, all transient upstream 504s (OpenRouter "operation was
aborted") — extraction-side ones were retried by the re-enqueue sweep;
reconcile-side ones lost their candidate batches because dead-lettered
batches are not auto-replayed once records are marked processed
(follow-up for Phase 20/21: replay dead letters, or defer marking to
reconcile commit).

Miss classes (40 misses): 19 are sentence-gold metric artifacts
(preference/abstention/temporal classes — gold answers are full sentences
that cannot substring-match on any system), 15 are on-topic
extraction-granularity gaps (the right memories retrieved, the numeric or
fine-grained detail abstracted away — the P1 drill-down scorer's case),
4 weak-topical, 2 off-topic. **On the 31 substring-scoreable questions the
hit rate is 0.32**, consistent with run #6's 0.30 — the headline 0.20 is
diluted by question classes the metric cannot score.

Results: `eval/results/longmemeval-n50-20260612T184318Z.jsonl`

Caveats: the fetched dataset is the cleaned ORACLE variant (each question's
haystack contains only its evidence sessions, ~3 per question). Published
competitor numbers use the `longmemeval_s` haystack (~40–50 sessions of
distractors); Phase 20 must fetch and run `_s` for the comparison table.

## Retraction: the 2026-06-12 "0.10 baseline" was invalid

The previously recorded n=10 figure (0.10, file
`longmemeval-n10-20260612T054427Z.jsonl`) is retracted in full, including
this report's earlier claim that retrievals were "on-topic for all 10
questions" (7/10 had zero content-word overlap with any retrieved item).
The run was invalid for compounding reasons, each found and fixed during
the 2026-06-12 sanity check:

1. **Scored a ~2%-ingested store.** The harness waited for only
   `len(conversations)` memories, then warned-and-continued; the run took
   44.7 s wall, while real extraction takes minutes. The same ~10 memories
   (one photography/painting user) filled all 50 retrieval slots. Fixed:
   `WaitForQuiescence` hard barrier (zero unprocessed records + stable
   memory count, `t.Fatal` on timeout).
2. **The single "hit" was a scorer artifact**: gold answer "2" substring-
   matched "Sony 24-70mm f/2.8 lens". Fixed: token-boundary matching for
   short answers.
3. **`MarkProcessed` had no production caller** — every record stayed
   unprocessed forever, so the re-enqueue sweep re-offered the entire
   record history every cycle (unbounded re-extraction in any long-running
   deployment, masked by duplicate-content discards). Fixed: the extract
   stage stamps records when the candidate batch is delivered downstream
   or the flush is deliberately skipped; failure paths still leave records
   for retry.
4. **Thinking models could not run the memory-formation path at all.**
   Both production gateway call sites truncated at `max_tokens` because
   reasoning tokens count against the output budget: extraction at 4096
   and reconcile decisions at 512 dead-lettered every call on real
   LongMemEval conversations. Fixed: 16384 / 8192. The CI mock cannot
   catch this class — Phase 20 should add a live budget-headroom check.
5. **Full-mode runs starved on CI-sized flush triggers**: items dropped
   from the bounded ingest channel were recovered by the re-enqueue sweep
   into buffers that waited out 10-minute ticks (~one extraction per
   tick). Fixed: production-like triggers in full mode (no mock script to
   protect).

This score is the starting line the benchmark gate defends; every later
phase must improve it, and the Phase 20 judged-QA mode supersedes it for
external comparison.

## Re-baseline follow-up (2026-06-13, D-067 Wave-A checkpoint)

The two real-gateway headline runs above (run #6 n=10=0.30, run #7 n=50=0.20)
were computed on 2026-06-12, BEFORE the BUG-4 FTS fix landed on 2026-06-13
(D-069). Every LongMemEval question ends in "?", and the pre-fix sqlite
lexical/queries lanes passed the raw "?"-terminated text straight to FTS5
`MATCH`, which hard-errored and silently dropped both lanes — so those headline
numbers reflect vector + structured retrieval only and almost certainly
understate post-fix retrieval.

A corrected re-baseline requires a full real-gateway re-run, which is **Phase-20
scope** (it needs an API key and the `longmemeval_s` haystack — not attempted in
this checkpoint). Until then, treat the ⚠️-flagged rows as a pre-BUG-4 lower
bound, not the current baseline. The CI gate (`make eval-ci`, mock gateway) is
unaffected — its fixtures and baseline (eval/baselines/ci.json) were re-derived
post-fix and the lexical + queries lanes are load-bearing there
(TestEvalCIGateBites).

**Action:** Phase 20 re-runs n=10/n=50 with the real gateway post-BUG-4 and
replaces the ⚠️-flagged rows with corrected figures.

## Full-mode config rebase to bifrost (2026-06-17, D-075)

The full-mode benchmark is rebased off `openaicompat` onto the **bifrost** driver,
which now runs the *whole* OpenRouter stack — embed + complete + **rerank** — on
one key. bifrost's built-in `openrouter` provider does not implement rerank, so
the driver auto-wires a Cohere-shape custom provider (`BaseProviderType=Cohere`,
path `/rerank`, same key/base) for the rerank pass (verified live:
`cohere/rerank-4-fast` returns real sorted scores over `…/api/v1/rerank`).

Documented full-mode config (run via the `fullmode_test.go` header; operator-run,
needs `OPENROUTER_API_KEY` — not CI):

| Knob | Value |
|---|---|
| `STOWAGE_EVAL_GATEWAY` | `bifrost` |
| `STOWAGE_EVAL_PROVIDER` | `openrouter` |
| `STOWAGE_EVAL_BASE_URL` (embed/complete) | `https://openrouter.ai/api` |
| `STOWAGE_EVAL_RERANK_BASE_URL` (custom rerank provider) | `https://openrouter.ai/api/v1` |
| `STOWAGE_EVAL_MODEL` (memory formation) | `inception/mercury-2` |
| `STOWAGE_EVAL_EMBED_MODEL` / `STOWAGE_EVAL_EMBED_DIMS` | `perplexity/pplx-embed-v1-0.6b` / `1024` |
| `STOWAGE_EVAL_RERANK_MODEL` | `cohere/rerank-4-fast` |

Rerank is **ENABLED** in full mode: the harness retriever is wired with
`WithRerankModel` and the runner issues `precise`-profile retrieves so the
cross-encoder actually runs (`DegradedRerank` surfaced if the provider is
unreachable). The CI mock gate (`make eval-ci`) leaves rerank OFF and is
unaffected. A fresh full-mode run on this config is recorded by the operator
(needs the key); model deltas vs. the prior gemini-based runs are noted with that
run.

## Gain harness + online adaptation (Phase 20b, D-078)

The gain harness measures whether memory improves task completion. Each scenario
(`eval/gain/scenarios/*.json`) is answered by the Phase-20 reader+judge twice — once
with the retrieved memory context (**memory-ON**) and once with none
(**memory-OFF**) — and `gain = quality(on) − quality(off)` (`quality(correct)=1,
partial=½, incorrect=0`). The reader is the stand-in agent loop (Harbor is a separate
codebase, not a dependency — D-078). **Mean aggregate gain ≥ 0 on the standard
scenarios is a release gate** (RFC §12: negative gain fails release), asserted in the
operator-run path — never CI.

The online-adaptation harness (`eval/gain/adapt/*.json`) runs sequential tasks
through the Phase-19 reflection→playbook loop: between tasks the reflection sweep
distills strategies and the assembled playbook is injected into the next task's
reader context; the per-task quality trajectory (delta = last − first) is the
compounding signal (ACE). Reported, not gated.

### Reproduce (operator-run, needs `OPENROUTER_API_KEY`; never CI)

```
set -a; source .env; set +a
STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_PROVIDER=openrouter \
  STOWAGE_EVAL_BASE_URL=https://openrouter.ai/api \
  STOWAGE_EVAL_RERANK_BASE_URL=https://openrouter.ai/api/v1 \
  STOWAGE_EVAL_API_KEY_REF=env.OPENROUTER_API_KEY \
  STOWAGE_EVAL_MODEL=inception/mercury-2 \
  STOWAGE_EVAL_EMBED_MODEL=perplexity/pplx-embed-v1-0.6b STOWAGE_EVAL_EMBED_DIMS=1024 \
  STOWAGE_EVAL_RERANK_MODEL=cohere/rerank-4-fast \
  STOWAGE_EVAL_GAIN=1 \
  go test -tags=fullmode -run 'TestGainMode|TestAdaptMode' -v -timeout 60m ./eval/harness/
```

Results (per-scenario gain + the aggregate summary; adaptation trajectories) are
written to `eval/results/gain-n*.jsonl` and `eval/results/adapt-n*.jsonl`. The
deterministic CI tests (`make eval-ci` + the harness unit tests) cover the scoring,
aggregation, and loop wiring with no model; the headline gain number is **pending an
operator run** (one command above).

---

## Public-benchmark headline numbers (pending operator runs — D-096)

The runner wiring for these is committed and CI-proven (`TestRunDataset_Wiring`,
`TestDatasetRegistry`, `TestGainMode` scoring); the numbers below are filled by the
operator after running the commands in **The public-benchmark runner** section above.
Each is a release-report line, not a per-PR CI gate (the latency SLO and the eval
benchmarks gate per their own decisions — D-095/D-035).

### longmemeval_s (distractor haystack — the headline LongMemEval variant)

> **TODO (operator):** `answer_quality` (judged) + `answer_context_hit`, n, wall time,
> from `eval/results/longmemeval_s-n*.jsonl`. Compare to published LongMemEval figures.

```
dataset            : longmemeval_s
n                  : __
answer_context_hit : __
answer_quality     : __ (judged)
p50 / p95 latency  : __ / __ ms
```

### LoCoMo

> **TODO (operator):** results from `eval/results/locomo-n*.jsonl`. Compare to
> published LoCoMo figures.

```
dataset            : locomo
n                  : __
answer_context_hit : __
answer_quality     : __ (judged)
p50 / p95 latency  : __ / __ ms
```

### Gain over a public dataset (memory-ON vs memory-OFF)

> **TODO (operator):** mean gain from `eval/results/gain-<dataset>-n*.jsonl`
> (`TestGainDatasetMode`). Mean gain ≥ 0 is the RFC §12 release gate.

```
dataset      : __ (longmemeval_s | locomo)
n            : __
mean_gain    : __   (mean_quality_on __ − mean_quality_off __)
non_negative : __/__
```
