#!/usr/bin/env bash
# LongMemEval LEARN phase (operator-run, PAID): ingest + extract with a CHEAP learner
# model, judging OFF. Produces (a) a persistent store and (b) a results JSONL that
# freezes each question's retrieved context — the reusable artifact for the reader/
# judge sweeps (scripts/eval/sweep.sh).
#
# Why split learn from read: extraction ("learning") is the dominant cost of the
# benchmark. Doing it once with a cheap model and reusing the frozen retrieval lets us
# sweep readers/judges for a cost/quality curve without re-paying for learning.
#
#   STOWAGE_EVAL_MODEL   = xiaomi/mimo-v2.5     (the learner — ~1/10 the Gemini cost)
#   judging              = OFF                  (answer_context_hit measures LEARNING)
#   store                = persistent           (live JSON-validity monitoring + reuse)
#
#   bash scripts/eval/learn.sh
#
set -uo pipefail
cd "$(dirname "$0")/../.."

ENV_FILE="${STOWAGE_ENV_FILE:-/Users/santiagobenvenuto/Repos/Stowage/.env}"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi
[ -n "${OPENROUTER_API_KEY:-}" ] || { echo "FATAL: OPENROUTER_API_KEY not set"; exit 1; }

export STOWAGE_EVAL_GATEWAY="${STOWAGE_EVAL_GATEWAY:-bifrost}"
export STOWAGE_EVAL_PROVIDER="${STOWAGE_EVAL_PROVIDER:-openrouter}"
export STOWAGE_EVAL_BASE_URL="${STOWAGE_EVAL_BASE_URL:-https://openrouter.ai/api}"
export STOWAGE_EVAL_RERANK_BASE_URL="${STOWAGE_EVAL_RERANK_BASE_URL:-https://openrouter.ai/api/v1}"
export STOWAGE_EVAL_API_KEY_REF="${STOWAGE_EVAL_API_KEY_REF:-env.OPENROUTER_API_KEY}"
export STOWAGE_EVAL_MODEL="${STOWAGE_EVAL_MODEL:-openai/gpt-5.4-nano}"       # the cheap learner — probed: ~1.5s, valid schema JSON, ~$0.00012/call
export STOWAGE_EVAL_EMBED_MODEL="${STOWAGE_EVAL_EMBED_MODEL:-perplexity/pplx-embed-v1-0.6b}"
export STOWAGE_EVAL_EMBED_DIMS="${STOWAGE_EVAL_EMBED_DIMS:-1024}"
export STOWAGE_EVAL_RERANK_MODEL="${STOWAGE_EVAL_RERANK_MODEL:-cohere/rerank-4-fast}"
export STOWAGE_EVAL_DATASET="${STOWAGE_EVAL_DATASET:-longmemeval}"
export STOWAGE_EVAL_LIMIT="${STOWAGE_EVAL_LIMIT:-50}"
export STOWAGE_EVAL_JUDGE=""                                                # learn phase: no reader/judge
export STOWAGE_EVAL_SETTLE_TIMEOUT="${STOWAGE_EVAL_SETTLE_TIMEOUT:-30m}"
export STOWAGE_EVAL_DB_PATH="${STOWAGE_EVAL_DB_PATH:-$PWD/eval/data/learned/longmemeval-${STOWAGE_EVAL_LIMIT}.db}"
GO_TIMEOUT="${STOWAGE_EVAL_GO_TIMEOUT:-180m}"

rm -f "$STOWAGE_EVAL_DB_PATH" "$STOWAGE_EVAL_DB_PATH"-* 2>/dev/null   # fresh learn
mkdir -p "$(dirname "$STOWAGE_EVAL_DB_PATH")"

echo "── LearnPhase ${STOWAGE_EVAL_LIMIT}q ─────────────────────────────────────"
echo "  learner (extraction): ${STOWAGE_EVAL_MODEL}   (judging OFF)"
echo "  embed/rerank        : ${STOWAGE_EVAL_EMBED_MODEL}@${STOWAGE_EVAL_EMBED_DIMS} / ${STOWAGE_EVAL_RERANK_MODEL}"
echo "  persistent store    : ${STOWAGE_EVAL_DB_PATH}"
echo "  monitor JSON health : bash scripts/eval/learn-monitor.sh \"${STOWAGE_EVAL_DB_PATH}\""
echo "──────────────────────────────────────────────────────────────────────────"

# The harness Gateway().Probe validates the CONFIGURED (extraction = learner) model at
# startup — a bad xiaomi/mimo-v2.5 slug fails here in seconds, not after a long run.

LOG="${TMPDIR:-/tmp}/learn-${STOWAGE_EVAL_LIMIT}q-$(date -u +%Y%m%dT%H%M%SZ).log"
echo "logging to ${LOG}"
go test -tags=fullmode -run TestFullMode -timeout "${GO_TIMEOUT}" -v ./eval/harness/ 2>&1 | tee "$LOG"
rc=${PIPESTATUS[0]}
echo ""; echo "── learn result file ──"
ls -t eval/results/${STOWAGE_EVAL_DATASET}-n*.jsonl 2>/dev/null | head -1
echo "(reuse it for sweeps:  STOWAGE_EVAL_SWEEP_INPUT=<that file> bash scripts/eval/sweep.sh)"
exit "$rc"
