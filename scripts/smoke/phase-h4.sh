#!/usr/bin/env bash
# Phase h4 smoke: tiered control-verb surface parity (topics/flush/branches/assert
# on {SDK,MCP,HTTP}; grants/contribute on {HTTP,MCP} not SDK) — D-067 Wave B, D-071.
#
# AC verified when implemented:
#   AC-1  Tier-A single-user verbs reachable on the SDK + MCP
#   AC-2  Tier-B multi-user verbs ABSENT from the SDK (single-user boundary)
#   AC-3  contribute-mode honored on MCP with a valid grant (h2 fail-loud replaced)
#
# SKELETON: SKIP until the Tier-A SDK methods exist (CLAUDE.md §4.2). The h4 PR
# flips these to OK/FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Tier-A lands a buffer Flush method on the SDK Client; gate on that symbol.
if ! grep -rq 'Flush(ctx context.Context' sdk/stowage/client.go 2>/dev/null; then
  skip "AC-1: Tier-A SDK control verbs not yet implemented (plan skeleton)"
  skip "AC-2: Tier-B SDK-absence boundary (pending h4)"
  skip "AC-3: MCP contribute-mode honoring (pending h4)"
  exit "$fails"
fi

skip "AC-1: Tier-A verbs on SDK+MCP — wired by the h4 implementation PR"
skip "AC-2: Tier-B absent from SDK — wired by the h4 implementation PR"
skip "AC-3: contribute honored on MCP — wired by the h4 implementation PR"
exit "$fails"
