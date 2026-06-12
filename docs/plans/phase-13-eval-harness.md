# Phase 13 — Eval harness: the benchmark gate

- **Status:** draft
- **Owning subsystem(s):** `eval/` (harness library + datasets + results),
  `cmd/stowage` (`eval` subcommand), CI gate wiring
- **RFC sections:** §12 (evaluation at launch and continuous), D-035, D-023
- **Depends on phases:** 12
- **Informing briefs:** 06 (benchmark-led positioning; committed per-question
  results + one-command reproduction), 04 (gain metric), 02 (LoCoMo
  methodology)

## Goal

`stowage eval` exists and **CI gains the benchmark gate**: a fast,
deterministic, mock-gateway subset that fails merge on retrieval-quality
regression. Runners for the public suite ingest each dataset's conversations
through the real write path (serve + store + pipeline) and measure retrieval
(recall@k) and answer-context quality. Full runs (real models) are manual.
This phase ships the harness, runners, CI subset, and baseline numbers — SOTA
tuning is the standing job of every later phase, measured from here on.

## Dataset verification (done at planning, 2026-06-11)

- **LongMemEval** — public, HuggingFace `xiaowu0162/longmemeval-cleaned`
  (+ `-v2`, 451 questions); JSON. PRIMARY.
- **LoCoMo** — public, github.com/snap-research/locomo
  (`data/locomo10.json`); 10 conversations, QA + event annotations. PRIMARY.
- **ConvoMem / MemBench** — availability NOT confirmed; runners ship
  behind a dataset-presence check (`SKIPPED: dataset not present` — never a
  silent pass); confirming/licensing them is a recorded follow-up, not a
  blocker (deviation from the master plan's four-primary framing — recorded).

## Design

- `eval/datasets/`: fetchers (`stowage eval fetch <name>`) downloading into
  `eval/data/` (**gitignored**); checksum-pinned; license notes per dataset
  in `eval/datasets/README.md`. Normalizers: each dataset → the common
  `Conversation{Sessions[]{Turns[]{role, content, ts}}}` +
  `Question{id, text, expected{answer | evidence_ids}, category}` shape.
- `eval/harness/`: boots an in-process serve (random port, temp sqlite or
  PG via env) with configurable gateway driver; ingests conversations
  through `/v1/records` (real write path: buffers → extraction →
  reconciliation run with the chosen gateway); retrieves per question;
  scores: **recall@k** (evidence-id match via provenance/citations),
  answer-context-hit (expected answer substring in retrieved content — the
  retrieval-centric metric mempalace publishes), latency stats. Topics: the
  assistant pack (default) — the harness must NOT hand-tune per dataset
  (that's the honesty constraint; tuning happens in product code).
- **Two modes:** `--mode=ci` (mock gateway + a COMMITTED deterministic
  fixture subset — 40 curated questions whose extraction candidates are
  pre-scripted via the lazy mock-script mechanism; runs < 90 s; produces
  exact-match scores against `eval/baselines/ci.json`) and `--mode=full`
  (real gateway driver + models via env; writes
  `eval/results/<dataset>-<date>.jsonl` per-question + summary — committed
  by the operator per D-035).
- **CI gate:** new job step `make eval-ci` — fails when any ci-mode metric
  drops below `eval/baselines/ci.json` (exact thresholds in-file; raising
  them = committing a new baseline in the same PR that improves quality).
  Wire into build-test job + the merge-pr.sh contract documents it.
- **Gain skeleton:** `eval/gain/` scenario format (multi-session tasks with
  memory on/off) + 3 seed scenarios runnable in ci mode; the full
  Harbor-fleet loop remains Phase 20 per the master plan.
- `eval/SLO.md` from Phase 12 referenced; `stowage eval slo` shells to the
  rig.
- `eval/REPORT.md` skeleton with the comparison-table structure (competitor
  published numbers cited from brief 06).

## Acceptance criteria (binding)

1. `stowage eval fetch longmemeval|locomo` downloads + verifies checksums;
   normalizers golden-tested on committed mini-fixtures (5 questions each,
   hand-built, no licensed data committed).
2. ci mode: deterministic across 3 consecutive runs (byte-identical
   results.jsonl); < 90 s on the dev machine; scores match
   eval/baselines/ci.json exactly.
3. **The gate bites**: a planted retrieval regression (test temporarily
   disabling a lane via env) drops a ci metric and `make eval-ci` exits
   non-zero (proven in a test, not by breaking main).
4. full mode runs end-to-end against LongMemEval-cleaned subset (≥50
   questions) with real embeddings (gate runs once with OpenRouter key;
   numbers recorded in eval/results/ + REPORT.md baseline section).
5. Per-question results format stable (golden); summary includes recall@k,
   answer-context-hit, p50/p95 latency, token+cost from gateway metering.
6. ConvoMem/MemBench runners SKIP loudly without data; follow-up filed.
7. Harness honesty: no per-dataset topic/prompt overrides (lint-style check:
   eval/ does not import or construct extraction prompts).
8. Coverage ≥ 80 eval packages (fetchers excluded hermetically — documented);
   race-clean; smokes 01–13 (phase-13.sh: eval ci on the committed fixture
   subset passes + gate-bite check).

## Files added or changed

```text
eval/{datasets/, harness/, gain/, baselines/ci.json, results/.gitkeep, REPORT.md}
cmd/stowage (eval subcommand: fetch|run|slo)
Makefile (eval-ci target), .github/workflows/ci.yml (gate step)
.gitignore (eval/data/), scripts/smoke/phase-13.sh, scripts/coverage.json
```

## Decisions filed

- D-054: the CI benchmark gate = deterministic mock-gateway subset with
  committed baselines; full benchmark runs are operator-triggered and their
  per-question results are committed (D-035 mechanics).
- D-055: ConvoMem/MemBench deferred to availability confirmation; the
  binding public suite at launch is LongMemEval + LoCoMo until then
  (amends the master plan's Phase 13 row; recorded).

## Risks & mitigations

- Dataset drift/licensing → checksums + license README; data never committed.
- ci-fixture overfitting → fixtures rotate only via deliberate baseline PRs;
  full-mode numbers are the truth, ci mode is the tripwire.
