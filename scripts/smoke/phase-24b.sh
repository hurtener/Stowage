#!/usr/bin/env bash
# Phase 24b smoke: episode threading / cross-session arcs (RFC §6b). D-081.
#
#   AC-1  the threading sweep is gateway-free (no internal/gateway import in threading.go).
#   AC-2  threading is OFF BY DEFAULT (ThreadingEnabled false in every profile).
#   AC-3  episodes.Arc is gateway-free; internal/episodes imports no gateway.
#   AC-4  memory_episodes exposes arc_of on HTTP + MCP + SDK.
#   AC    threading + arc unit tests + parity pass; MCP golden stable; eval-ci green.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if [ ! -f internal/lifecycle/threading.go ]; then
  skip "AC-1..4: episode threading not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-1: threading sweep gateway-free ───────────────────────────────────────────
if grep -qE '"github.com/hurtener/stowage/internal/gateway"' internal/lifecycle/threading.go 2>/dev/null; then
  failc "AC-1: threading.go imports the gateway (the clustering must stay LLM-free)"
else
  ok "AC-1: threading sweep is gateway-free"
fi
if grep -q 'wordJaccard' internal/lifecycle/threading.go 2>/dev/null \
   && grep -q 'relates_to' internal/lifecycle/threading.go 2>/dev/null; then
  ok "AC-1: threading clusters by word-set overlap → relates_to edges"
else
  failc "AC-1: threading clustering/edge wiring missing"
fi

# ── AC-2: off by default ─────────────────────────────────────────────────────────
if grep -q 'ThreadingEnabled: false' internal/config/profiles.go 2>/dev/null; then
  ok "AC-2: threading ships OFF by default (eval-gated, D-081)"
else
  failc "AC-2: threading default-off not set in the profile config"
fi

# ── AC-3: Arc read gateway-free ──────────────────────────────────────────────────
if grep -q 'func Arc(' internal/episodes/view.go 2>/dev/null; then
  ok "AC-3: episodes.Arc present"
else
  failc "AC-3: episodes.Arc missing"
fi
if grep -qE '"github.com/hurtener/stowage/internal/gateway"' internal/episodes/view.go 2>/dev/null; then
  failc "AC-3: episodes view core imports the gateway (Arc must stay LLM-free)"
else
  ok "AC-3: episodes.Arc is gateway-free"
fi

# ── AC-4: arc_of on all three single-user surfaces (D-067) ────────────────────────
if grep -q 'arc_of' internal/api/episodes_handler.go 2>/dev/null \
   && grep -q 'ArcOf' internal/mcpserver/contracts.go 2>/dev/null \
   && grep -q 'episodes.Arc' internal/mcpserver/handlers.go 2>/dev/null \
   && grep -q 'ArcOf' sdk/stowage/types.go 2>/dev/null \
   && grep -q 'arc_of' sdk/stowage/http.go 2>/dev/null \
   && grep -q 'episodes.Arc' sdk/stowage/embedded.go 2>/dev/null; then
  ok "AC-4: memory_episodes exposes arc_of on HTTP + MCP + SDK"
else
  failc "AC-4: arc_of wiring missing on a surface"
fi

# ── AC: unit + parity tests ──────────────────────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestThreadingSweep' ./internal/lifecycle/ >/tmp/p24b-thread.log 2>&1 \
   && CGO_ENABLED=1 go test -count=1 -timeout=180s -run 'TestArc' ./internal/episodes/ >/tmp/p24b-arc.log 2>&1; then
  ok "AC-1/3: threading sweep + episodes.Arc unit tests pass"
else
  failc "AC-1/3: threading/arc unit tests failed"; tail -25 /tmp/p24b-thread.log /tmp/p24b-arc.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestEpisodesParity_Arc' ./test/integration/ >/tmp/p24b-parity.log 2>&1; then
  ok "AC-4: arc_of all-surfaces parity passes"
else
  failc "AC-4: arc_of parity failed"; tail -25 /tmp/p24b-parity.log >&2
fi

# ── AC: MCP schema golden stable + CI eval gate unaffected ───────────────────────
if CGO_ENABLED=1 go test -count=1 -run 'TestSchemaGoldens' ./internal/mcpserver/ >/tmp/p24b-golden.log 2>&1; then
  ok "AC: MCP schema goldens stable (memory_episodes arc_of)"
else
  failc "AC: MCP schema goldens drifted"; cat /tmp/p24b-golden.log >&2
fi
if make eval-ci >/tmp/p24b-evalci.log 2>&1; then
  ok "AC: make eval-ci green (deterministic CI unaffected)"
else
  failc "AC: make eval-ci failed"; cat /tmp/p24b-evalci.log >&2
fi

exit "$fails"
