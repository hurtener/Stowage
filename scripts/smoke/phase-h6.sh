#!/usr/bin/env bash
# Phase h6 smoke: co-mount MCP-over-HTTP onto `stowage serve` (one process, both
# surfaces, one stack) — D-073 follow-up, D-074.
#
# AC verified when implemented:
#   AC-1  one `serve` process answers both REST and MCP
#   AC-2  an HTTP-ingested memory is visible to an MCP retrieve (one cache)
#
# SKELETON: SKIP until server.mcp_listen + the co-mount wiring exist
# (CLAUDE.md §4.2). The h6 PR flips these to OK/FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if ! grep -rq 'MCPListen' internal/config/ 2>/dev/null; then
  skip "AC-1: server.mcp_listen co-mount not yet implemented (plan skeleton)"
  skip "AC-2: HTTP write visible via MCP retrieve, one cache (pending h6)"
  exit "$fails"
fi

skip "AC-1: one serve process answers REST + MCP — wired by the h6 PR"
skip "AC-2: cache-coherence across surfaces — wired by the h6 PR"
exit "$fails"
