#!/usr/bin/env bash
# scripts/smoke/phase-13.sh — smoke checks for Phase 13 (eval harness)
#
# Tests:
#   1. stowage eval (no args) exits non-zero and prints usage
#   2. stowage eval ci prints instructions and exits 0
#   3. stowage eval fetch --help exits 0
#   4. stowage eval fetch --dataset=unknown exits non-zero
#   5. go vet ./eval/... passes

set -uo pipefail

BIN="${BIN:-bin/stowage}"
PASS=0
FAIL=0

ok()   { echo "  OK  $*"; PASS=$((PASS+1)); }
fail() { echo "FAIL  $*"; FAIL=$((FAIL+1)); }

# 1. stowage eval with no args → exits non-zero and prints usage
eval_output=$("$BIN" eval 2>&1 || true)
eval_exit=0
"$BIN" eval >/dev/null 2>&1 || eval_exit=$?
if [ "$eval_exit" -ne 0 ] && echo "$eval_output" | grep -q "stowage eval"; then
  ok "eval no-args exits non-zero and prints usage"
else
  fail "eval no-args should exit non-zero and print usage (exit=$eval_exit)"
fi

# 2. stowage eval ci → exits 0 and mentions make eval-ci
ci_output=$("$BIN" eval ci 2>&1)
ci_exit=0
"$BIN" eval ci >/dev/null 2>&1 || ci_exit=$?
if [ "$ci_exit" -eq 0 ] && echo "$ci_output" | grep -q "make eval-ci"; then
  ok "eval ci exits 0 and prints make eval-ci"
else
  fail "eval ci should exit 0 and mention make eval-ci (exit=$ci_exit)"
fi

# 3. stowage eval fetch --help → exits 0
help_exit=0
"$BIN" eval fetch --help >/dev/null 2>&1 || help_exit=$?
if [ "$help_exit" -eq 0 ]; then
  ok "eval fetch --help exits 0"
else
  fail "eval fetch --help should exit 0 (got $help_exit)"
fi

# 4. stowage eval fetch with unknown dataset → exits non-zero
unknown_exit=0
"$BIN" eval fetch --dataset=unknown >/dev/null 2>&1 || unknown_exit=$?
if [ "$unknown_exit" -ne 0 ]; then
  ok "eval fetch --dataset=unknown exits non-zero"
else
  fail "eval fetch with unknown dataset should fail (got exit 0)"
fi

# 5. go vet ./eval/... passes
if go vet ./eval/... 2>&1; then
  ok "go vet ./eval/... clean"
else
  fail "go vet ./eval/... failed"
fi

echo ""
echo "phase-13 smoke: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
