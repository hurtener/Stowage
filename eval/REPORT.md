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

| Dataset | n | answer_context_hit | p50 | p95 | Gateway |
|---|---|---|---|---|---|
| LongMemEval oracle (cleaned) | 10 | **0.30** (3/10) | 573 ms | 733 ms | openaicompat → OpenRouter (extract gemini-3.5-flash, embed gemini-embedding-2 3072d) |

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
