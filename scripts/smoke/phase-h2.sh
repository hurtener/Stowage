#!/usr/bin/env bash
# Phase h2 smoke test: Wave A correctness + honesty bundle (D-067 Wave A, D-069).
#
# Acceptance criteria verified (activate when h2 implementation lands):
#   AC-1  NewEmbedded rejects invalid / literal-api_key config (fail-loud, D-030).
#   AC-3  sqlite lexical query with FTS operators/special chars does not crash.
#   AC-5  MCP memory_ingest with target_scope set fails loud (no silent mis-scope).
#
# SKELETON: this plan PR ships the skeleton; checks SKIP until h2 is implemented
# (CLAUDE.md §4.2 — SKIP gracefully where the surface isn't built yet).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Implementation gate ───────────────────────────────────────────────────────
# h2 makes NewEmbedded run cfg.Validate(). Until the embedded validation call is
# present, SKIP so preflight stays green on the plan PR.
if ! grep -rq 'Validate()' sdk/stowage/embedded.go 2>/dev/null; then
  skip "AC-1: embedded fail-loud config validation not yet implemented (plan skeleton)"
  skip "AC-3: sqlite FTS special-char robustness (pending h2)"
  skip "AC-5: MCP contribute-mode fail-loud (pending h2)"
  exit "$fails"
fi

# ── Real checks (implemented by the h2 PR) ────────────────────────────────────
skip "AC-1: NewEmbedded invalid/literal-key rejection — wired by the h2 PR"
skip "AC-3: FTS special-char query no-crash — wired by the h2 PR"
skip "AC-5: MCP contribute-mode rejection — wired by the h2 PR"

exit "$fails"
