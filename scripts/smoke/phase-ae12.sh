#!/usr/bin/env bash
# Phase ae12 — memory_ingest_run, the Harbor run-completion sink (D-153).
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { printf 'OK   %s\n' "$*"; }
failc(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip() { printf 'SKIP %s\n' "$*"; }

# Surface gate: SKIP cleanly until the tool lands (CLAUDE.md §4.2).
if ! grep -q "memory_ingest_run" internal/mcpserver/server.go 2>/dev/null; then
  skip "ae12 surface not built yet (memory_ingest_run absent)"
  exit 0
fi

# Implementor fills in (plan §Smoke script, criteria 1-9). Boot jwt-mode
# MCP-over-HTTP (static JWKS + sqlite; reuse the ae11 smoke's test-only minter):
#   - bearer-less tools/list          -> catalog contains memory_ingest_run (24 tools)
#   - bearer-less memory_ingest_run   -> 401 (a tool like any other, D-152)
#   - JWT call, full 13-key format_version-1 payload (entries carry kind/step/at)
#                                     -> 200, ids count == entry count
#   - sqlite: records stamped with the JWT tenant/user + outcome + shared session
#   - JWT call, format_version: 2     -> tool error naming version 1
#   - JWT call, mismatched tenant_id  -> rejected (fail closed)
#   - memory_ingest regression        -> a plain records call still succeeds
failc "ae12 smoke checks not implemented yet"

exit "$fails"
