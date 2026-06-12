#!/usr/bin/env bash
# Phase 16 smoke test: MCP server registers and exposes 7 typed tools.
#
# Starts stowage mcp --stdio with a temp SQLite store and mock gateway,
# sends JSON-RPC initialize → tools/list over the stdio pipe, and verifies:
#   - exactly 7 tools are returned
#   - all 7 expected names are present
#
# Mirrors the other phase smoke scripts: temp DB + config, CGo-free build,
# background server killed after session.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-16
TMPDIR_SMOKE=$(mktemp -d)
trap 'rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Temp environment ──────────────────────────────────────────────────────────

DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"

cat > "$CFG_PATH" <<YAML
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
mcp:
  stdio_tenant: smoke16
YAML

# ── Migrate (idempotent) ──────────────────────────────────────────────────────

"$BIN" migrate --config "$CFG_PATH" >/dev/null 2>&1 \
  && ok "migrate applied" \
  || { failc "migrate failed"; exit "$fails"; }

# ── Build JSON-RPC request sequence ──────────────────────────────────────────

INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke16","version":"0.0.1"}}}'
INITIALIZED='{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
TOOLS_LIST='{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'

# Write them to a file for piping.
printf '%s\n%s\n%s\n' "$INIT" "$INITIALIZED" "$TOOLS_LIST" \
  > "${TMPDIR_SMOKE}/requests.jsonl"

# ── Run MCP stdio in background with stdin kept open ─────────────────────────
# We pipe (requests + a 4-second sleep) into mcp so that stdin stays open
# long enough for the server to boot, read the queued messages, and respond.
# We kill after 2 s once we have the responses.

(cat "${TMPDIR_SMOKE}/requests.jsonl"; sleep 4) \
  | "$BIN" mcp --config "$CFG_PATH" \
    > "${TMPDIR_SMOKE}/responses.jsonl" \
    2>"${TMPDIR_SMOKE}/mcp.log" &
MCP_PID=$!

# Wait up to 2 s for the server to respond to tools/list.
GOT_RESPONSE=0
for i in $(seq 1 20); do
  sleep 0.1
  if grep -q '"tools"' "${TMPDIR_SMOKE}/responses.jsonl" 2>/dev/null; then
    GOT_RESPONSE=1
    break
  fi
done

# Kill the MCP process (closes stdin → clean shutdown).
kill "$MCP_PID" 2>/dev/null
wait "$MCP_PID" 2>/dev/null

ok "mcp stdio session completed"

# ── Parse responses ───────────────────────────────────────────────────────────

if [ "$GOT_RESPONSE" -eq 0 ]; then
  failc "tools/list response not received within 2 s"
  echo "--- mcp stderr ---" >&2
  cat "${TMPDIR_SMOKE}/mcp.log" >&2
  exit "$fails"
fi

RESP_FILE="${TMPDIR_SMOKE}/responses.jsonl"

# Find the tools/list response (line containing '"tools"' array from id=2).
TOOLS_LINE=$(grep '"tools"' "$RESP_FILE" | tail -1 || true)

if [ -z "$TOOLS_LINE" ]; then
  failc "tools/list response not found in MCP output"
  cat "$RESP_FILE" >&2
  exit "$fails"
fi
ok "tools/list response received"

# Count tools — use Python if jq is unavailable (jq optional on macOS CI).
if command -v jq &>/dev/null; then
  TOOL_COUNT=$(echo "$TOOLS_LINE" | jq '.result.tools | length' 2>/dev/null || echo "0")
  NAMES=$(echo "$TOOLS_LINE" | jq -r '.result.tools[].name' 2>/dev/null | sort)
else
  # Fallback: count occurrences of "name":"memory_" with grep.
  TOOL_COUNT=$(echo "$TOOLS_LINE" | grep -o '"name":"memory_' | wc -l | tr -d ' ')
  NAMES=""
fi

WANT=7
if [ "$TOOL_COUNT" -eq "$WANT" ]; then
  ok "tools/list returned exactly $WANT tools (AC-1)"
else
  failc "tools/list returned $TOOL_COUNT tools, want $WANT (AC-1)"
  echo "tools/list response: $TOOLS_LINE" >&2
fi

# Verify the 7 expected names (only when jq is available).
if command -v jq &>/dev/null && [ -n "$NAMES" ]; then
  EXPECTED="memory_assert
memory_drilldown
memory_feedback
memory_ingest
memory_playbook
memory_retrieve
memory_topics"

  if [ "$NAMES" = "$EXPECTED" ]; then
    ok "all 7 tool names match expected names (AC-1)"
  else
    failc "tool names mismatch (AC-1)"
    printf 'expected:\n%s\ngot:\n%s\n' "$EXPECTED" "$NAMES" >&2
  fi
fi

exit "$fails"
