#!/usr/bin/env bash
# Reader/judge cost-quality SWEEP over a frozen learn-phase result JSONL (operator-run,
# PAID). Reuses the learning + retrieval already captured by scripts/eval/learn.sh —
# no re-extraction — and measures real answer_quality across reader × judge models.
#
#   STOWAGE_EVAL_SWEEP_INPUT=eval/results/longmemeval-n50-<ts>.jsonl \
#     STOWAGE_EVAL_SWEEP_READERS="openai/gpt-5.4-nano,openai/gpt-4o-mini,anthropic/claude-sonnet-4.6" \
#     STOWAGE_EVAL_SWEEP_JUDGES="anthropic/claude-sonnet-4.6" \
#     bash scripts/eval/sweep.sh
#
set -uo pipefail
cd "$(dirname "$0")/../.."
ENV_FILE="${STOWAGE_ENV_FILE:-/Users/santiagobenvenuto/Repos/Stowage/.env}"
[ -f "$ENV_FILE" ] && { set -a; . "$ENV_FILE"; set +a; }
[ -n "${OPENROUTER_API_KEY:-}" ] || { echo "FATAL: OPENROUTER_API_KEY not set"; exit 1; }

# Default the sweep input to the most recent learn result if unset.
if [ -z "${STOWAGE_EVAL_SWEEP_INPUT:-}" ]; then
  STOWAGE_EVAL_SWEEP_INPUT=$(ls -t eval/results/longmemeval-n*.jsonl 2>/dev/null | head -1)
fi
[ -n "${STOWAGE_EVAL_SWEEP_INPUT:-}" ] && [ -f "$STOWAGE_EVAL_SWEEP_INPUT" ] || { echo "FATAL: no sweep input (set STOWAGE_EVAL_SWEEP_INPUT)"; exit 1; }
export STOWAGE_EVAL_SWEEP_INPUT

export STOWAGE_EVAL_GATEWAY="${STOWAGE_EVAL_GATEWAY:-bifrost}"
export STOWAGE_EVAL_PROVIDER="${STOWAGE_EVAL_PROVIDER:-openrouter}"
export STOWAGE_EVAL_BASE_URL="${STOWAGE_EVAL_BASE_URL:-https://openrouter.ai/api}"
export STOWAGE_EVAL_RERANK_BASE_URL="${STOWAGE_EVAL_RERANK_BASE_URL:-https://openrouter.ai/api/v1}"
export STOWAGE_EVAL_API_KEY_REF="${STOWAGE_EVAL_API_KEY_REF:-env.OPENROUTER_API_KEY}"
export STOWAGE_EVAL_EMBED_MODEL="${STOWAGE_EVAL_EMBED_MODEL:-perplexity/pplx-embed-v1-0.6b}"
export STOWAGE_EVAL_EMBED_DIMS="${STOWAGE_EVAL_EMBED_DIMS:-1024}"
export STOWAGE_EVAL_SWEEP_READERS="${STOWAGE_EVAL_SWEEP_READERS:-openai/gpt-5.4-nano,openai/gpt-4o-mini,anthropic/claude-sonnet-4.6}"
export STOWAGE_EVAL_SWEEP_JUDGES="${STOWAGE_EVAL_SWEEP_JUDGES:-anthropic/claude-sonnet-4.6}"
export STOWAGE_EVAL_READER_EFFORT="${STOWAGE_EVAL_READER_EFFORT:-medium}"
GO_TIMEOUT="${STOWAGE_EVAL_GO_TIMEOUT:-120m}"

echo "── Reader/Judge sweep ────────────────────────────────────────────────────"
echo "  input  : ${STOWAGE_EVAL_SWEEP_INPUT}"
echo "  readers: ${STOWAGE_EVAL_SWEEP_READERS}"
echo "  judges : ${STOWAGE_EVAL_SWEEP_JUDGES}   reader_effort=${STOWAGE_EVAL_READER_EFFORT}"
echo "──────────────────────────────────────────────────────────────────────────"

LOG="${TMPDIR:-/tmp}/sweep-$(date -u +%Y%m%dT%H%M%SZ).log"
go test -tags=fullmode -run TestReaderJudgeSweep -timeout "${GO_TIMEOUT}" -v ./eval/harness/ 2>&1 | tee "$LOG"
echo ""; echo "── sweep result file ──"; ls -t eval/results/sweep-*.jsonl 2>/dev/null | head -1
