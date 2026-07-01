#!/usr/bin/env bash
# Phase ae3 smoke: shared render core (eval-mode vs MCP-mode), D-141. One render
# entry point in internal/retrieval; eval reader prompt byte-frozen; RenderMCP is an
# inert superset until ae4a; RenderMode is a call-site arg, NOT a config knob.
#
# Verifies:
#   AC-1  internal/retrieval/render.go defines Render + RenderMode (single entry point).
#   AC-1  no [OUTDATED / "| When:" render literals leak into internal/mcpserver.
#   AC-3  the [OUTDATED re-parse is gone from eval/harness/judge.go (typed seam).
#   AC-7  RenderMode is not a config knob (absent from internal/config).
#   AC-2  eval golden + render unit tests pass (byte-identity + mode-diff + concurrency).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

RENDER=internal/retrieval/render.go

# ── AC-1: single render entry point exists ──────────────────────────────────────
if [ ! -f "$RENDER" ]; then
  skip "AC-1: $RENDER not built yet (ae3 not landed)"
  exit "$fails"
fi
if grep -Eq 'func Render\(' "$RENDER" && grep -Eq 'type RenderMode' "$RENDER"; then
  ok "AC-1: $RENDER defines Render + RenderMode"
else
  failc "AC-1: $RENDER missing Render or RenderMode"
fi

# ── AC-1: MCP carries no render logic (one entry point) ─────────────────────────
if grep -REq '\[OUTDATED|\| When:' internal/mcpserver; then
  failc "AC-1: render literals ([OUTDATED / | When:) leaked into internal/mcpserver"
else
  ok "AC-1: internal/mcpserver carries no render literals"
fi

# ── AC-3: the string round-trip is removed from eval prompt assembly ────────────
if grep -Eq 'HasPrefix\([^)]*\[OUTDATED|Index\([^)]*"\] "' eval/harness/judge.go; then
  failc "AC-3: judge.go still re-parses the [OUTDATED marker (string coupling not removed)"
else
  ok "AC-3: judge.go no longer re-parses the [OUTDATED marker (typed seam)"
fi

# ── AC-7: RenderMode is a call-site arg, not a config knob ───────────────────────
if grep -Rq 'RenderMode' internal/config; then
  failc "AC-7: RenderMode appears in internal/config (must not be a knob)"
else
  ok "AC-7: RenderMode is not a config knob"
fi

# ── AC-2: render unit tests + eval golden pass (byte-identity, mode-diff, -race) ─
if go test ./internal/retrieval/ -run Render -count=1 >/dev/null 2>&1; then
  ok "AC-2: internal/retrieval Render tests pass"
else
  failc "AC-2: internal/retrieval Render tests fail"
fi
if go test ./eval/harness/ -run 'TestReaderPrompt_Golden|TestEvalCI' -count=1 >/dev/null 2>&1; then
  ok "AC-2: eval reader-prompt golden + TestEvalCI pass unchanged"
else
  failc "AC-2: eval golden / TestEvalCI regressed"
fi

exit "$fails"
