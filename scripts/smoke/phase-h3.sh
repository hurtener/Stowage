#!/usr/bin/env bash
# Phase h3 smoke: reconciliation reversibility parity (rollback/confirm/get across
# SDK + MCP + HTTP) — D-067 Wave B, D-070.
#
# AC verified when implemented:
#   AC-3  rollback reachable on the SDK (embedded) and as an MCP tool
#   AC-4  rollback restores prior state identically on embedded + server
#
# SKELETON: SKIP until the reconcile.Rollback core + SDK/MCP surfaces exist
# (CLAUDE.md §4.2). The h3 PR flips these to OK/FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if ! grep -rq 'func Rollback' internal/reconcile/ 2>/dev/null; then
  skip "AC-1: reconcile.Rollback core not yet implemented (plan skeleton)"
  skip "AC-3: rollback on SDK + MCP (pending h3)"
  skip "AC-4: rollback parity embedded vs server (pending h3)"
  exit "$fails"
fi

skip "AC-3: rollback SDK+MCP reachability — wired by the h3 implementation PR"
skip "AC-4: rollback parity embedded vs server — wired by the h3 implementation PR"
exit "$fails"
