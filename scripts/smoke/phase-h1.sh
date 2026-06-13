#!/usr/bin/env bash
# Phase h1 smoke test: boot.StartPipeline — pipeline + lifecycle parity across
# all entrypoints (D-067 Wave A, D-068).
#
# Acceptance criteria verified (activate when h1 implementation lands):
#   AC-1  boot.StartPipeline is the only live-serving stage constructor.
#   AC-3  A record ingested via `stowage mcp` becomes a retrievable memory.
#   AC-5  BackfillSweep runs on the mcp/embedded paths (not serve-only).
#
# SKELETON: this plan PR ships the skeleton; checks SKIP until h1 is implemented
# (CLAUDE.md §4.2 — SKIP gracefully where the surface isn't built yet).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Implementation gate ───────────────────────────────────────────────────────
# h1 introduces boot.StartPipeline. Until that symbol exists, SKIP every check so
# preflight stays green on the plan PR. The implementing PR flips these to OK/FAIL.
if ! grep -rq 'func StartPipeline' internal/boot/ 2>/dev/null; then
  skip "AC-1: boot.StartPipeline not yet implemented (plan skeleton)"
  skip "AC-3: MCP ingest -> retrievable memory (pending h1)"
  skip "AC-5: BackfillSweep on mcp/embedded (pending h1)"
  exit "$fails"
fi

# ── Real checks (implemented by the h1 PR) ────────────────────────────────────
# AC-1: no direct stage construction remains in cmd/ or sdk/ outside the helper.
if grep -rqE 'pipeline\.New|NewExtractStage|reconcile\.New|lifecycle\.New|BackfillSweep' \
     cmd/stowage sdk/stowage 2>/dev/null; then
  failc "AC-1: stray live-stage construction outside boot.StartPipeline"
else
  ok "AC-1: boot.StartPipeline is the sole live-stage constructor"
fi

# AC-3: ingest via `stowage mcp --http` then retrieve must return the memory.
#   (Implemented by the h1 PR: boot mcp, call memory_ingest, flush, memory_retrieve,
#    assert count >= 1 — FAIL on zero, which is today's broken behavior.)
skip "AC-3: MCP ingest->memory E2E — wired by the h1 implementation PR"
skip "AC-5: BackfillSweep on mcp/embedded — wired by the h1 implementation PR"

exit "$fails"
