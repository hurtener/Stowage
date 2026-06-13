# Stowage — Status Report (2026-06-12)

Snapshot for resuming work. Main is green at PR #23; 23 PRs merged total;
all merges went through `scripts/merge-pr.sh` (CI-gated). The launch track
is paused after Phase 18 pending a direction decision (see "Decision
needed" below).

## What is built (launch track, executed order)

| Executed phase | Shipped | PR |
|---|---|---|
| 01–06 foundations | store seam (sqlite + postgres drivers, conformance suite), identity/scopes (P3 fail-closed), records + verbatim provenance (P1), buffers & fire-and-forget ingest (P2), gateway seam, events/audit, day-one signal schema (migrations 0001–0006) | #1–#6 |
| 07 topics + extraction | topic registry, extraction stage, candidate validation | #7 |
| 08 reconciliation | add/update/merge/supersede with trust gates, prior-state snapshots on every destructive op (D-017), atomic Commit | #8–#9 |
| 09/09b/09c retrieval + vectors + gateway remediation | hybrid lanes + RRF; HNSW vindex default (per-tenant graphs, brute oracle); gateway "bifrost" driver imports the real maximhq/bifrost core SDK, HTTP client renamed `openaicompat` (D-049) | #10–#13 |
| 10 scoring | pure scoring fn (utility counters, decay, trust) | #13 |
| 11 attribution | injections recording, citations v1, `/v1/feedback`, envelope v1 | #14–#15 |
| 12 rerank + cache + SLO | cohere rerank via OpenRouter (live-validated), scope-generation result cache (D-053), SLO rig | #16 |
| 13 eval harness | **CI benchmark gate live** (`make eval-ci`, quality-only); fullmode build tag for real-model runs | #17 |
| 14 sweeps | decay / dedupe / rollup / re-enqueue, idempotent + advisory-locked | #18 |
| 15 grants | store-enforced team sharing, zone ceilings, contribute mode (D-059/D-060) | #19 |
| 16 MCP server | 7 typed tools over the Dockyard runtime library, store-keyring auth | #20 |
| 17 SDKs | `sdk/stowage` NewHTTP + NewEmbedded proven by ONE shared suite; `internal/boot`; `adapters/harbor` separate module (D-062/D-063); Python stdlib client; CGo-free embedded example | #21 |
| 18 rollback & confirmation | the master plan's skipped slot 15: `POST /v1/memories/{id}/rollback` (D-064, consumes D-017 snapshots, all-or-nothing merge unwind), confirm sweep + PATCH confirm/reject (D-065, OQ-4 resolved), parked-duplicate counter | #22 |
| eval sanity check | valid baselines + pipeline convergence fixes (see below) | #23 |

Numbering note: executed phases diverged from the master-plan tables at
Wave 5; the mapping lives in `docs/plans/README.md` ("Numbering
reconciliation").

## Benchmark state (the 2026-06-12 sanity check)

- The earlier LongMemEval **0.10 baseline was invalid and is retracted**
  in `eval/REPORT.md` (scored a ~2%-ingested store; its one hit was a
  substring artifact).
- **Valid baselines** (oracle dataset, retrieval-only `answer_context_hit`,
  fully-quiescent store, per-question results committed in
  `eval/results/`): n=10 → **0.30**; n=50 → **0.20 headline, 0.32 on the
  31 substring-scoreable questions**. 19/40 misses are sentence-gold
  metric artifacts; dominant real gap is extraction granularity (numeric
  details abstracted away — the P1 drill-down scorer recovers these);
  only 2/50 retrievals off-topic.
- **Not comparable to published ~92/93% figures**: those are end-to-end
  LLM-judged QA on `longmemeval_s` haystacks; ours is retrieval-only on
  the oracle variant. Judged-QA estimate over our retrieved context:
  7–8/10.

Two production bugs found by the sanity check, both fixed:

1. `MarkProcessed` had no production caller → unbounded re-extraction
   loop (re-enqueue sweep re-offered all history forever; gateway cost
   burn). Extract now stamps records on delivered/skipped flushes.
2. Thinking models could not run memory formation: reasoning tokens count
   against `max_tokens`; extraction (4096) and reconcile decisions (512)
   truncated and dead-lettered everything → now 16384/8192.

Also fixed the same day: coder/hnsw upstream Delete leaves dangling
inbound edges (SIGSEGV in production Upsert path) → vindex graph is now
append-only with invalidate-and-lazy-rebuild (D-066).

## Pending

**Decision needed first:** resume Phase 19 as planned, or pull Phase 20
eval prerequisites forward for an earlier like-for-like competitor number.

- **Phase 19 — reflection & playbooks** (RFC §6a, D-018, OQ-6): outcome
  reflection extraction mode (`strategy`/`failure_mode` memories),
  re-reflection sweep, `internal/playbook` + `GET /v1/playbook`
  (deterministic, no LLM in assembly, append-biased for prompt caching),
  `memory_playbook` MCP tool becomes real. Plan not yet authored.
- **Phase 20 — eval finalization + competitor report**: fetch + run
  `longmemeval_s` (fetcher currently pulls oracle only); LLM-judged QA
  mode (the honest comparison metric); drill-down-aware scorer; gain
  harness on a Harbor fleet; comparison table in `eval/REPORT.md`.
  Carry-ins from the sanity check: live budget-headroom check for
  thinking models (the CI mock cannot catch truncation), dead-letter
  replay (reconcile-side batches are lost once records are marked — or
  defer marking to reconcile commit).
- **Phase 21 — hardening & launch**: security pass, docs, release
  matrix, public-repo audit, license decision (OQ-5), five-minute-rule
  smoke.
- Smaller deferred items: contradiction boost (v1.2 trust extensions),
  vindex/hnsw coverage override restore (D-056 checkpoint audit),
  pgstore override 81 (D-039).

## Process reminders (binding)

- Merges ONLY via `scripts/merge-pr.sh <pr>` (verifies every check, then
  squash-merges; now PR-state-verifying after a false-refusal quirk).
- `scripts/drift-audit.sh` + `scripts/preflight.sh` before/after merges;
  CI ≈ 14 min.
- Doc order: RFC-001 > docs/plans/ > CLAUDE.md (= AGENTS.md) > briefs.
  Decisions through **D-066** in `docs/decisions.md`.
- Full-mode eval runs: see `eval/harness/fullmode_test.go` header
  (OpenRouter models: extract `google/gemini-3.5-flash`, embed
  `google/gemini-embedding-2` 3072d, rerank `cohere/rerank-4-fast`).
- Worktree pattern for implementation: `../Stowage-wt-phaseNN`, branch
  `feat/phase-NN-*`; commit fixes BEFORE mutation spot-checks; run
  `golangci-lint cache clean` before lint; verify coverage WITH `-race`
  (it differs); verify multi-module changes with `GOWORK=off`.
