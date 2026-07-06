#!/usr/bin/env bash
# Phase ae11 — method-aware MCP handshake auth (D-152) smoke checks.
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails+1)); }
skip() { echo "SKIP $*"; }

# --- checks ---------------------------------------------------------------
# Surface gate: skip everything until the classifier lands.
if ! grep -q "MethodAwareAuthMiddleware" internal/mcpserver/*.go 2>/dev/null; then
  skip "ae11 surface not built yet (MethodAwareAuthMiddleware absent)"
  exit 0
fi

# Implementor fills in (plan §Smoke script, criteria 1-9):
#  jwt mode boot (static JWKS file, sqlite):
#   - bearer-less initialize            -> 200 + Mcp-Session-Id
#   - bearer-less notifications/initialized -> accepted
#   - bearer-less tools/list            -> catalog contains memory_retrieve
#   - bearer-less tools/call            -> 401 "authorization required"
#   - test-signed JWT tools/call        -> 200
#   - batch-array POST without bearer   -> 401
#  keyring mode boot:
#   - bearer-less initialize            -> 401 (regression: strict gate intact)
#  zero-config `stowage serve` start still OK (existing invariant)
failc "ae11 smoke checks not implemented yet"

exit "$fails"
