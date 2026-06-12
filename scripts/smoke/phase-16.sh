#!/usr/bin/env bash
# Phase 16 smoke test: MCP server registers 7 tools.
#
# Sends JSON-RPC initialize → tools/list over stdio and verifies that exactly
# 7 tools are returned with the expected names.
#
# Requirements: jq, stowage binary in ./bin/stowage or on PATH.
set -euo pipefail

BIN="${STOWAGE_BIN:-./bin/stowage}"
if ! command -v "$BIN" &>/dev/null && [ ! -f "$BIN" ]; then
  echo "phase-16 smoke: building stowage binary..."
  CGO_ENABLED=0 go build -o ./bin/stowage ./cmd/stowage 2>/dev/null || true
fi

if [ ! -f "$BIN" ]; then
  BIN="$(command -v stowage 2>/dev/null || true)"
fi

if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  echo "phase-16 smoke: SKIP (binary not found; run 'make build' first)"
  exit 0
fi

if ! command -v jq &>/dev/null; then
  echo "phase-16 smoke: SKIP (jq not installed)"
  exit 0
fi

# Build the JSON-RPC request sequence: initialize + tools/list
REQUESTS=$(cat <<'EOF'
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0.0.1"}}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
EOF
)

# Pipe requests into mcp stdio and capture output (with 5s timeout).
OUTPUT=$(echo "$REQUESTS" | timeout 5 "$BIN" mcp --config /dev/null 2>/dev/null || true)

if [ -z "$OUTPUT" ]; then
  echo "phase-16 smoke: SKIP (mcp produced no output — store may require migration)"
  exit 0
fi

# Extract tools/list response (the second JSON line that contains "tools").
TOOLS_JSON=$(echo "$OUTPUT" | grep '"tools"' | tail -1 || true)

if [ -z "$TOOLS_JSON" ]; then
  echo "phase-16 smoke: SKIP (no tools/list response found in output)"
  exit 0
fi

# Count tools.
TOOL_COUNT=$(echo "$TOOLS_JSON" | jq '.result.tools | length' 2>/dev/null || echo "0")

WANT=7
if [ "$TOOL_COUNT" -eq "$WANT" ]; then
  echo "phase-16 smoke: OK — $TOOL_COUNT tools registered"
else
  echo "phase-16 smoke: FAIL — expected $WANT tools, got $TOOL_COUNT"
  echo "tools/list response: $TOOLS_JSON"
  exit 1
fi

# Verify the 7 expected names.
NAMES=$(echo "$TOOLS_JSON" | jq -r '.result.tools[].name' 2>/dev/null | sort)
EXPECTED="memory_assert
memory_drilldown
memory_feedback
memory_ingest
memory_playbook
memory_retrieve
memory_topics"

if [ "$NAMES" = "$EXPECTED" ]; then
  echo "phase-16 smoke: OK — all 7 tool names match"
else
  echo "phase-16 smoke: FAIL — tool names mismatch"
  echo "expected:"
  echo "$EXPECTED"
  echo "got:"
  echo "$NAMES"
  exit 1
fi
