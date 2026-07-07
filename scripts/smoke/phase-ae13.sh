#!/usr/bin/env bash
# Phase ae13 — topics resolve tenant-wide (D-154) smoke checks.
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { printf 'OK   %s\n' "$*"; }
failc(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip() { printf 'SKIP %s\n' "$*"; }

# Surface gate: SKIP cleanly until the fix lands (CLAUDE.md §4.2).
if ! grep -q "topicScope" internal/topics/topics.go 2>/dev/null; then
  skip "ae13 fix not built yet (topicScope normalization absent)"
  exit 0
fi

# Implementor fills in (plan §Smoke script, criteria 1-7):
#   - PUT two explicit topics via HTTP /v1/topics (tenant key)
#   - jwt-scoped MCP memory_topics list -> the two topics, no default-pack keys
#   - per-user ingest + flush -> extraction resolves the tenant topic set
#   - keyring/tenant-scope regression: topics still resolve (previously-working path)
#   - zero-config tenant still resolves the profile default pack
failc "ae13 smoke checks not implemented yet"

exit "$fails"
