#!/usr/bin/env bash
# LongMemEval 50-question full-mode run (operator-triggered, PAID, never CI).
#
# Pipeline: real extraction (cheap model) → topic-gated capture into the broad
# LongMemEval magnet set (eval/harness/topics_seed.go) → four-lane retrieve + rerank
# → a STRONG reader (Sonnet 4.6, medium thinking) that answers ONLY from retrieved
# context and abstains otherwise → an LLM judge grading vs the gold answer.
#
# Models (override any via env before running):
#   extraction/reconcile : STOWAGE_EVAL_MODEL         (default google/gemini-2.5-flash — reliable structured extraction)
#   learner reasoning    : STOWAGE_EVAL_MODEL_EFFORT  (default ""=no reasoning param; set "low" for a reasoning-only
#                                                       learner like openai/gpt-5.4-nano that cannot disable reasoning — D-128)
#   reader + judge       : STOWAGE_EVAL_READER_MODEL  (default anthropic/claude-sonnet-4.6, reasoning effort medium)
#   embeddings           : STOWAGE_EVAL_EMBED_MODEL   (default perplexity/pplx-embed-v1-0.6b @ 1024)
#   rerank               : STOWAGE_EVAL_RERANK_MODEL  (default cohere/rerank-4-fast)
#
# Reads OPENROUTER_API_KEY from the main repo .env. Never prints the key.
#
#   bash scripts/eval/longmemeval-50.sh
#
set -uo pipefail
cd "$(dirname "$0")/../.."

ENV_FILE="${STOWAGE_ENV_FILE:-/Users/santiagobenvenuto/Repos/Stowage/.env}"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi
if [ -z "${OPENROUTER_API_KEY:-}" ]; then
  echo "FATAL: OPENROUTER_API_KEY not set (source your .env or export it)"; exit 1
fi

# ── Models + run knobs (override via env) ─────────────────────────────────────
export STOWAGE_EVAL_GATEWAY="${STOWAGE_EVAL_GATEWAY:-bifrost}"
export STOWAGE_EVAL_PROVIDER="${STOWAGE_EVAL_PROVIDER:-openrouter}"
export STOWAGE_EVAL_BASE_URL="${STOWAGE_EVAL_BASE_URL:-https://openrouter.ai/api}"
export STOWAGE_EVAL_RERANK_BASE_URL="${STOWAGE_EVAL_RERANK_BASE_URL:-https://openrouter.ai/api/v1}"
export STOWAGE_EVAL_API_KEY_REF="${STOWAGE_EVAL_API_KEY_REF:-env.OPENROUTER_API_KEY}"
export STOWAGE_EVAL_MODEL="${STOWAGE_EVAL_MODEL:-google/gemini-2.5-flash}"
export STOWAGE_EVAL_MODEL_EFFORT="${STOWAGE_EVAL_MODEL_EFFORT:-}"   # learner (extract+reconcile) reasoning effort; ""=none (D-128)
export STOWAGE_EVAL_READER_MODEL="${STOWAGE_EVAL_READER_MODEL:-anthropic/claude-sonnet-4.6}"
export STOWAGE_EVAL_READER_EFFORT="${STOWAGE_EVAL_READER_EFFORT:-medium}"
export STOWAGE_EVAL_EMBED_MODEL="${STOWAGE_EVAL_EMBED_MODEL:-perplexity/pplx-embed-v1-0.6b}"
export STOWAGE_EVAL_EMBED_DIMS="${STOWAGE_EVAL_EMBED_DIMS:-1024}"
export STOWAGE_EVAL_RERANK_MODEL="${STOWAGE_EVAL_RERANK_MODEL:-cohere/rerank-4-fast}"
export STOWAGE_EVAL_DATASET="${STOWAGE_EVAL_DATASET:-longmemeval}"   # oracle haystack; set longmemeval_s for the distractor variant
export STOWAGE_EVAL_LIMIT="${STOWAGE_EVAL_LIMIT:-50}"
export STOWAGE_EVAL_JUDGE="${STOWAGE_EVAL_JUDGE:-1}"
export STOWAGE_EVAL_SETTLE_TIMEOUT="${STOWAGE_EVAL_SETTLE_TIMEOUT:-30m}"
GO_TIMEOUT="${STOWAGE_EVAL_GO_TIMEOUT:-180m}"

echo "── LongMemEval ${STOWAGE_EVAL_LIMIT}q ──────────────────────────────────────"
echo "  dataset    : ${STOWAGE_EVAL_DATASET}"
echo "  extraction : ${STOWAGE_EVAL_MODEL} (learner reasoning=${STOWAGE_EVAL_MODEL_EFFORT:-none})"
echo "  reader+judge: ${STOWAGE_EVAL_READER_MODEL} (reasoning=${STOWAGE_EVAL_READER_EFFORT}, abstain+context-only)"
echo "  embed      : ${STOWAGE_EVAL_EMBED_MODEL}@${STOWAGE_EVAL_EMBED_DIMS}   rerank: ${STOWAGE_EVAL_RERANK_MODEL}"
echo "  settle     : ${STOWAGE_EVAL_SETTLE_TIMEOUT}   go timeout: ${GO_TIMEOUT}"
echo "──────────────────────────────────────────────────────────────────────────"

# ── Fail fast on a bad reader slug ────────────────────────────────────────────
# The in-process harness probe only validates the EXTRACTION/embed model; a wrong
# reader slug would otherwise burn ~5 questions before the run aborts. One ~1-token
# completion against the reader model catches it in seconds.
echo "probing reader model ${STOWAGE_EVAL_READER_MODEL} …"
probe=$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 \
  -X POST "${STOWAGE_EVAL_RERANK_BASE_URL%/}/chat/completions" \
  -H "Authorization: Bearer ${OPENROUTER_API_KEY}" -H "Content-Type: application/json" \
  -d "{\"model\":\"${STOWAGE_EVAL_READER_MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"ok\"}],\"max_tokens\":1}" 2>/dev/null)
if [ "$probe" != "200" ]; then
  echo "FATAL: reader model '${STOWAGE_EVAL_READER_MODEL}' probe returned HTTP ${probe:-000} —"
  echo "       confirm the OpenRouter slug (set STOWAGE_EVAL_READER_MODEL to the correct id)."
  exit 1
fi
echo "reader model OK (HTTP 200)"

# ── Fetch the dataset if absent ───────────────────────────────────────────────
DATA="eval/data/longmemeval/longmemeval.json"
if [ ! -f "$DATA" ]; then
  echo "fetching dataset ${STOWAGE_EVAL_DATASET} …"
  CGO_ENABLED=0 go build -o /tmp/stowage-eval ./cmd/stowage || { echo "build failed"; exit 1; }
  /tmp/stowage-eval eval fetch --dataset "${STOWAGE_EVAL_DATASET}" || { echo "dataset fetch failed"; exit 1; }
fi

# ── Run (results land in eval/results/longmemeval-n50-<ts>.jsonl) ─────────────
LOG="${TMPDIR:-/tmp}/longmemeval-${STOWAGE_EVAL_LIMIT}q-$(date -u +%Y%m%dT%H%M%SZ).log"
echo "logging to ${LOG}"
go test -tags=fullmode -run TestFullMode -timeout "${GO_TIMEOUT}" -v ./eval/harness/ 2>&1 | tee "$LOG"
rc=${PIPESTATUS[0]}
echo ""
echo "── result file ──"
ls -t eval/results/${STOWAGE_EVAL_DATASET}-n*.jsonl 2>/dev/null | head -1
exit "$rc"
