#!/usr/bin/env bash
# Phase a3 smoke: quickstart honesty + MCP opt-in clarity (D-133). The README and
# getting-started must match shipped defaults (MCP opt-in, one real secret); `serve`
# logs a hint when MCP is off. MCP stays opt-in (D-074 reaffirmed).
#
# Verifies:
#   AC-1  README quickstart names STOWAGE_GATEWAY_API_KEY and marks MCP opt-in.
#   AC-2  `serve` with server.mcp_listen empty logs the MCP-disabled hint.
#   AC-3  the h6 single-surface default still holds (no regression).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── AC-1: README quickstart is honest ──────────────────────────────────────────
if grep -q 'STOWAGE_GATEWAY_API_KEY' README.md; then
  ok "AC-1: README quickstart names STOWAGE_GATEWAY_API_KEY"
else
  failc "AC-1: README quickstart does not name STOWAGE_GATEWAY_API_KEY"
fi
# Must NOT claim MCP is co-mounted by default; must mark it opt-in.
if grep -Eq 'MCP is opt-in|opt-in.*server\.mcp_listen|server\.mcp_listen to co-mount' README.md; then
  ok "AC-1: README marks the MCP surface opt-in"
else
  failc "AC-1: README does not mark MCP opt-in"
fi
if grep -Eq 'co-mounted MCP listener\)\. SQLite' README.md; then
  failc "AC-1: README still implies MCP is co-mounted by default"
else
  ok "AC-1: README no longer implies auto co-mount"
fi
# getting-started must also be honest: no present-tense "serve co-mounts" claim.
if grep -Eq 'stowage serve. co-mounts an MCP' docs/getting-started.md; then
  failc "AC-1: getting-started still implies serve co-mounts MCP by default"
else
  ok "AC-1: getting-started no longer implies auto co-mount"
fi
if grep -Eq 'MCP tool surface is .?.?opt-in|opt-in via .server\.mcp_listen' docs/getting-started.md; then
  ok "AC-1: getting-started marks the MCP surface opt-in"
else
  failc "AC-1: getting-started does not mark MCP opt-in"
fi

# ── AC-2: serve logs the MCP-disabled hint when the knob is empty ───────────────
BIN=/tmp/stowage-smoke-a3
TMPDIR_SMOKE=$(mktemp -d)
H6_LOG="${TMPDIR_SMOKE}/h6.log"
trap 'kill "${SRV_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT
if CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null; then
  ok "CGo-free binary build"
else
  failc "CGo-free binary build"; exit "$fails"
fi
env -u STOWAGE_GATEWAY_API_KEY STOWAGE_GATEWAY_DRIVER=mock \
  STOWAGE_STORE_DSN="${TMPDIR_SMOKE}/a3.db" STOWAGE_SERVER_LISTEN=":17193" \
  STOWAGE_TELEMETRY_METRICS_LISTEN=":17194" \
  "$BIN" serve >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SRV_PID=$!
sleep 2
kill "$SRV_PID" 2>/dev/null
wait "$SRV_PID" 2>/dev/null || true
SRV_PID=""
if grep -qi 'MCP surface disabled' "${TMPDIR_SMOKE}/serve.log"; then
  ok "AC-2: serve logs the MCP-disabled hint when mcp_listen is empty"
else
  failc "AC-2: serve did not log the MCP-disabled hint"
  cat "${TMPDIR_SMOKE}/serve.log" >&2
fi

# ── AC-3: h6 single-surface default unchanged ──────────────────────────────────
if bash scripts/smoke/phase-h6.sh >"$H6_LOG" 2>&1; then
  ok "AC-3: phase-h6 (single-surface default, co-mount opt-in) still passes"
else
  failc "AC-3: phase-h6 regressed"
  tail -20 "$H6_LOG" >&2
fi

exit "$fails"
