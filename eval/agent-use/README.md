# Agent-use evaluation

## Captured baseline

`ab7d3a2c0eb3a4c7230d508e5a45dd6996005b38` is the immutable pre-change baseline.
The first successful capture is GitHub Actions run `33973153758`. The baseline
workflow records the actual MCP catalog, race-enabled MCP/retrieval/reconciliation
service tests, capture timestamp, toolchain, and an explicit limitations file.
Later feature commits do not change the source checked out by this job.

**Live agent-selection baseline: not run.** No live model/host credentials were
available. Schema goldens and scripted handlers are not spontaneous tool-use
measurements. Do not derive an improvement percentage from them.

## Behavioral protocol

`cases.jsonl` contains implicit-memory positives and self-contained/current-context
negatives. `prior_user_records` are seeded before a run; `current_context` and
`request` are given to the agent. The `recall_expected` label and grading criteria
must NEVER be included in the agent prompt. Empty results, historical values,
changed preferences, and stored instruction attacks are covered.

Run each case against isolated copies of the baseline and candidate using the
same deployed host/model/version, competing tools, prompts, budgets and seed
records. Use several independent trials. Reset the store and host context before
each trial so feedback, suggestion dedupe, or new writes cannot leak between arms.
Record the model-visible catalog and exact response projection, not just tools/list.

Recommended arms: baseline; descriptions only; reduced/schema-guided catalog;
full candidate. A forced-retrieval arm diagnoses retrieval usefulness but is not
a production-policy score. Host-injected memories must be logged separately from
model-selected calls; low voluntary calls are not a failure when useful context
was already supplied.

Keep per-case tool traces, final answers, latency, tokens, evidence applied,
identity scope, and any errors. Report appropriate-recall rate on positive cases,
unnecessary-recall rate on negatives, task-quality grading, source/freshness errors,
and latency/token cost. Count missing/failed trials explicitly, rather than dropping
them. Human review or a separately pinned judge grades the written success criteria.

The agent endpoint also has deterministic live-wire tests for the five-tool
catalog, unknown runtime/admin calls, invalid enums/limits, missing source errors,
source-bound commands, and warning retention. Those contract tests run without
provider calls and complement, not replace, this behavioral comparison.
