#!/usr/bin/env bash
# Phase 23 smoke: episodic retrieval (RFC §6b). D-080.
#
#   AC-1  memory_episodes reachable on HTTP (route) + MCP (tool + golden) + SDK (Client).
#   AC-2  internal/episodes view core is gateway-free (no provider SDK; P5).
#   AC-3  cross-surface parity test + view unit tests pass.
#   AC    eval-ci unaffected.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if [ ! -f internal/episodes/view.go ]; then
  skip "AC-1..3: episodic retrieval not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-1: surfaces wired ─────────────────────────────────────────────────────────
if grep -q 'GET /v1/episodes' internal/api/server.go 2>/dev/null; then
  ok "AC-1: GET /v1/episodes route registered"
else
  failc "AC-1: GET /v1/episodes route missing"
fi
if grep -q 'memory_episodes' internal/mcpserver/server.go 2>/dev/null \
   && [ -f internal/mcpserver/testdata/memory_episodes.output.schema.json ]; then
  ok "AC-1: memory_episodes MCP tool registered + schema golden present"
else
  failc "AC-1: memory_episodes tool or schema golden missing"
fi
if grep -q 'Episodes(ctx context.Context, req EpisodesRequest)' sdk/stowage/client.go 2>/dev/null; then
  ok "AC-1: SDK Client.Episodes present"
else
  failc "AC-1: SDK Client.Episodes missing"
fi

# ── AC-2: view core gateway-free (P5) ────────────────────────────────────────────
if grep -q 'internal/gateway' internal/episodes/view.go 2>/dev/null; then
  failc "AC-2: episodes view core imports the gateway (must be deterministic/LLM-free)"
else
  ok "AC-2: episodes view core is gateway-free"
fi

# ── AC-3: view unit tests + parity ───────────────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=180s -run 'TestList_Get_Window' ./internal/episodes/ >/tmp/p23-view.log 2>&1 \
   && CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestEpisodesParity_AllSurfaces' ./test/integration/ >/tmp/p23-parity.log 2>&1; then
  ok "AC-3: episodes view unit + all-surfaces parity tests pass"
else
  failc "AC-3: episodes view/parity tests failed"; tail -25 /tmp/p23-view.log /tmp/p23-parity.log >&2
fi

# ── AC: MCP schema goldens stable + CI eval gate unaffected ──────────────────────
if CGO_ENABLED=1 go test -count=1 -run 'TestSchemaGoldens' ./internal/mcpserver/ >/tmp/p23-golden.log 2>&1; then
  ok "AC: MCP schema goldens stable (incl. memory_episodes)"
else
  failc "AC: MCP schema goldens drifted"; cat /tmp/p23-golden.log >&2
fi
if make eval-ci >/tmp/p23-evalci.log 2>&1; then
  ok "AC: make eval-ci green (deterministic CI unaffected)"
else
  failc "AC: make eval-ci failed"; cat /tmp/p23-evalci.log >&2
fi

exit "$fails"
