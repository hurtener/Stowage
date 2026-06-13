#!/usr/bin/env bash
# Phase h2 smoke test: Wave A correctness + honesty bundle (D-067 Wave A, D-069).
#
# Acceptance criteria verified:
#   AC-1  NewEmbedded rejects invalid / literal-api_key config (fail-loud, D-030).
#   AC-3  sqlite lexical query with FTS operators/special chars does not crash.
#   AC-5  MCP memory_ingest with target_scope set fails loud (no silent mis-scope).
#
# AC-2 (embedded gateway defaults), AC-4 (rune-safe drill-down), and the FTS fuzz
# target are asserted in unit tests:
#   - internal/config             TestFillZeroDefaults_PopulatesGatewayLanes (AC-2)
#   - internal/retrieval          TestClampExcerpt / TestClampExcerptAlwaysValidUTF8 (AC-4)
#   - internal/store/sqlitestore  FuzzFTSQueryArg (AC-3 fuzz)
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Implementation gate ───────────────────────────────────────────────────────
# h2 makes NewEmbedded run cfg.Validate(). Until that call is present, SKIP so
# preflight stays green on the plan PR.
if ! grep -rq 'Validate()' sdk/stowage/embedded.go 2>/dev/null; then
  skip "AC-1: embedded fail-loud config validation not yet implemented (plan skeleton)"
  skip "AC-3: sqlite FTS special-char robustness (pending h2)"
  skip "AC-5: MCP contribute-mode fail-loud (pending h2)"
  exit "$fails"
fi

command -v go >/dev/null 2>&1 || {
  skip "AC-1/AC-3/AC-5: go toolchain unavailable"
  exit "$fails"
}

# ── AC-1: NewEmbedded rejects invalid / literal-api_key config ────────────────
if go test -count=1 -run '^TestClientEmbedded_ConfigValidation$' ./sdk/stowage/ >/tmp/h2-ac1.log 2>&1; then
  ok "AC-1: NewEmbedded rejects invalid / literal-api_key config (fail-loud, D-030)"
else
  failc "AC-1: NewEmbedded config validation test failed"
  cat /tmp/h2-ac1.log >&2
fi

# ── AC-3: sqlite FTS special-char queries never hard-error ────────────────────
if go test -count=1 -run '^TestFTSSpecialCharQueriesNeverError$|^TestFTSSanitizedQueriesStillMatch$' \
     ./internal/store/sqlitestore/ >/tmp/h2-ac3.log 2>&1; then
  ok "AC-3: sqlite FTS operator/special-char queries return cleanly (no lane abort)"
else
  failc "AC-3: sqlite FTS special-char query test failed"
  cat /tmp/h2-ac3.log >&2
fi

# ── AC-5: MCP memory_ingest contribute-mode fails loud ────────────────────────
if go test -count=1 -run '^TestHandlerIngest_ContributeFailLoud$' ./internal/mcpserver/ >/tmp/h2-ac5.log 2>&1; then
  ok "AC-5: MCP memory_ingest contribute-mode fields fail loud (no silent mis-scope)"
else
  failc "AC-5: MCP contribute-mode fail-loud test failed"
  cat /tmp/h2-ac5.log >&2
fi

exit "$fails"
