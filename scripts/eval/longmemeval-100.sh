#!/usr/bin/env bash
# LongMemEval 100-question full-mode re-baseline (operator-triggered, PAID, never CI).
#
# The 29d exit-gate run: 100 questions (so one wrong question moves ~1%), the consolidation
# sweeps ON (production-faithful — set STOWAGE_EVAL_NO_CONSOLIDATE=1 to measure the delta),
# and a per-category breakout in the log. Target: ≥75% answer_quality at the current
# compression rate. Thin wrapper over longmemeval-50.sh (same models/probe/fetch); only the
# default question count differs. All model knobs are overridable via env.
#
#   bash scripts/eval/longmemeval-100.sh
set -uo pipefail
export STOWAGE_EVAL_LIMIT="${STOWAGE_EVAL_LIMIT:-100}"
exec bash "$(dirname "$0")/longmemeval-50.sh"
