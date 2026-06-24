#!/usr/bin/env bash
# LongMemEval K-SWEEP (operator-run, cheap): re-score an already-learned persistent
# store at several retrieve limits (K = how many compressed memories reach the reader)
# WITHOUT re-paying for extraction. Tests the compression-dividend hypothesis: memories
# are ~36 tokens each, so a larger K raises recall at near-zero context cost.
#
# Reuses scripts/eval/learn.sh's persistent DB (STOWAGE_EVAL_DB_PATH). Skip-ingest
# bypasses the whole ingest→extract→settle path and scores directly against the existing
# memories. The brute vindex is mandatory: the in-memory hnsw index comes up EMPTY on
# reopen (it never persisted its graph), so only brute's store-scan retrieval is faithful.
#
# Judging is OFF by default (answer_context_hit + token cost only — free signal, no
# reader/judge calls). Set STOWAGE_EVAL_JUDGE=1 to also judge each K (paid).
#
#   STOWAGE_EVAL_KS="5 10 15 25" bash scripts/eval/ksweep.sh
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
export STOWAGE_EVAL_MODEL="${STOWAGE_EVAL_MODEL:-openai/gpt-5.4-nano}"  # probed by Gateway().Probe; not re-invoked under skip-ingest
export STOWAGE_EVAL_EMBED_MODEL="${STOWAGE_EVAL_EMBED_MODEL:-perplexity/pplx-embed-v1-0.6b}"
export STOWAGE_EVAL_EMBED_DIMS="${STOWAGE_EVAL_EMBED_DIMS:-1024}"
# Rerank on by default (precise profile). STOWAGE_EVAL_NO_RERANK=1 disables it (balanced
# profile) — needed because `:-` would otherwise treat an empty rerank model as unset.
if [ -n "${STOWAGE_EVAL_NO_RERANK:-}" ]; then
  export STOWAGE_EVAL_RERANK_MODEL=""
else
  export STOWAGE_EVAL_RERANK_MODEL="${STOWAGE_EVAL_RERANK_MODEL:-cohere/rerank-4-fast}"
fi
export STOWAGE_EVAL_DATASET="${STOWAGE_EVAL_DATASET:-longmemeval}"
export STOWAGE_EVAL_LIMIT="${STOWAGE_EVAL_LIMIT:-50}"
export STOWAGE_EVAL_JUDGE="${STOWAGE_EVAL_JUDGE:-}"          # OFF → context-hit + tokens only
export STOWAGE_EVAL_READER_MODEL="${STOWAGE_EVAL_READER_MODEL:-}"
export STOWAGE_EVAL_SETTLE_TIMEOUT="${STOWAGE_EVAL_SETTLE_TIMEOUT:-1m}"  # skip-ingest: no settle needed

# Skip-ingest + brute vindex against the frozen learn store.
export STOWAGE_EVAL_SKIP_INGEST=1
export STOWAGE_EVAL_VINDEX="${STOWAGE_EVAL_VINDEX:-brute}"
export STOWAGE_EVAL_DB_PATH="${STOWAGE_EVAL_DB_PATH:-$PWD/eval/data/learned/longmemeval-${STOWAGE_EVAL_LIMIT}.db}"
[ -f "$STOWAGE_EVAL_DB_PATH" ] || { echo "FATAL: learn DB not found: $STOWAGE_EVAL_DB_PATH (run scripts/eval/learn.sh first)"; exit 1; }

KS="${STOWAGE_EVAL_KS:-5 10 15}"
GO_TIMEOUT="${STOWAGE_EVAL_GO_TIMEOUT:-30m}"

echo "── K-sweep over ${STOWAGE_EVAL_DB_PATH} ───────────────────────────────────"
echo "  Ks: ${KS}   vindex=${STOWAGE_EVAL_VINDEX}   judge=${STOWAGE_EVAL_JUDGE:-off}   reader=${STOWAGE_EVAL_READER_MODEL:-none}"
echo "──────────────────────────────────────────────────────────────────────────"

for K in $KS; do
  export STOWAGE_EVAL_RETRIEVE_LIMIT="$K"
  LOG="${TMPDIR:-/tmp}/ksweep-K${K}-$(date -u +%Y%m%dT%H%M%SZ).log"
  echo ""; echo "### K=${K}  (log ${LOG})"
  go test -tags=fullmode -run TestFullMode -timeout "${GO_TIMEOUT}" -v ./eval/harness/ 2>&1 | tee "$LOG" | grep -E "FULL-MODE|answer_context_hit|judge_errors" || true
done

echo ""; echo "── per-K result files ──"
ls -t eval/results/${STOWAGE_EVAL_DATASET}-n*.jsonl 2>/dev/null | head -n "$(echo $KS | wc -w)"
