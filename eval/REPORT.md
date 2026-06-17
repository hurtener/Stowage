# Stowage — Benchmark Report

> The launch artifact (D-023/D-035): reproducible numbers on the public memory
> benchmark suite, with committed per-question results. Comparison table vs
> published competitor figures lands with the launch run (Phase 20).

## How to reproduce

- CI subset (deterministic, mock gateway): `make eval-ci`
- Full mode (real models): see the header of `eval/harness/fullmode_test.go`
- Datasets: `./bin/stowage eval fetch --dataset longmemeval|locomo`

## Metric definition

`answer_context_hit`: the gold answer string appears (case-insensitive) in
the content of the retrieved memories. Short answers (< 4 runes) must match
on token boundaries with joining-punctuation handling, so "2" cannot match
inside "f/2.8". This is a RETRIEVAL-ONLY metric over the memory abstraction
layer: no reader model, no LLM judge, no drill-down to verbatim records.

**It is NOT comparable to published LongMemEval accuracy figures** (the
90%+ range reported by competitors), which measure end-to-end QA accuracy:
a reader LLM answers from retrieved context and an LLM judge scores the
answer. Several LongMemEval question classes (abstention, preference,
temporal composites) have gold answers that are full sentences which can
never substring-match retrieved context on ANY system — the like-for-like
comparison requires the Phase 20 judged-QA mode.

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
| `STOWAGE_EVAL_BASE_URL` | `https://openrouter.ai/api/v1` |
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
