# Stowage — Benchmark Report

> The launch artifact (D-023/D-035): reproducible numbers on the public memory
> benchmark suite, with committed per-question results. Comparison table vs
> published competitor figures lands with the launch run (Phase 20).

## How to reproduce

- CI subset (deterministic, mock gateway): `make eval-ci`
- Full mode (real models): see the header of `eval/harness/fullmode_test.go`
- Datasets: `./bin/stowage eval fetch --dataset longmemeval|locomo`

## First real-model baseline (2026-06-12, gate-recorded)

| Dataset | n | answer_context_hit | p50 | p95 | Gateway |
|---|---|---|---|---|---|
| LongMemEval (cleaned) | 10 | **0.10** (1/10) | 525 ms | 1174 ms | openaicompat → OpenRouter (extract gemini-3.5-flash, embed gemini-embedding-2 3072d) |

Caveats, recorded deliberately: n=10 (cost-bounded overnight run; the
≥50-question run is one command — see fullmode_test.go header); the metric is
strict case-insensitive answer-substring over the MEMORY abstraction layer
only — the scorer does not yet drill down to verbatim records (follow-up: a
drill-down-aware scorer is fair and matches the P1 architecture); retrievals
were verified non-empty and on-topic for all 10 questions — the pipeline works
end-to-end with real models; this score is the starting line the benchmark
gate now defends and every later phase must improve.

Results: `eval/results/longmemeval-n10-20260612T054427Z.jsonl`
