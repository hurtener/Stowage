#!/usr/bin/env bash
# Phase h6 smoke: co-mount MCP-over-HTTP onto `stowage serve` (one process, both
# surfaces, one stack) — D-073 follow-up, D-074.
#
# Verifies:
#   AC-1  ONE `serve` process answers BOTH REST and MCP-over-HTTP (second port).
#   AC-2  an HTTP-ingested+flushed memory is returned by an MCP memory_retrieve
#         on the second port — one shared stk.Retriever cache.
#   AC-3  dual-listener shutdown stops both BEFORE p.Drain (no panic).
#   AC-4  the `server.mcp_listen` knob is surfaced by `config explain`, validates,
#         and defaults empty (opt-in — `serve` then binds exactly one port).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Pre-build SKIP-graceful guard (CLAUDE.md §4.2).
if ! grep -rq 'MCPListen' internal/config/ 2>/dev/null; then
  skip "AC-1: server.mcp_listen co-mount not yet implemented (plan skeleton)"
  skip "AC-2: HTTP write visible via MCP retrieve, one cache (pending h6)"
  skip "AC-3: dual-listener shutdown before drain (pending h6)"
  skip "AC-4: server.mcp_listen knob surfaced (pending h6)"
  exit "$fails"
fi

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-h6
TMPDIR_SMOKE=$(mktemp -d)
trap 'kill "${SRV_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "CGo-free binary build" \
  || { failc "CGo-free binary build"; exit "$fails"; }

# ── AC-4: the knob is surfaced + validates + defaults empty ────────────────────

EXPLAIN=$("$BIN" config explain 2>/dev/null || true)
if printf '%s' "$EXPLAIN" | grep -q 'server.mcp_listen'; then
  ok "AC-4: config explain surfaces server.mcp_listen"
else
  failc "AC-4: config explain does not surface server.mcp_listen"
fi
# Default must be empty (opt-in): the value column after '=' is blank.
if printf '%s' "$EXPLAIN" | grep -Eq 'server.mcp_listen[[:space:]]*=[[:space:]]*\['; then
  ok "AC-4: server.mcp_listen defaults empty (opt-in)"
else
  failc "AC-4: server.mcp_listen default is not empty"
fi
# Validation rejects a bad address.
if CGO_ENABLED=1 go test -count=1 -timeout=120s -run 'TestMCPListen' ./internal/config/ >/tmp/h6-config.log 2>&1; then
  ok "AC-4: server.mcp_listen validation unit tests pass (empty/valid/bad)"
else
  failc "AC-4: server.mcp_listen validation unit tests failed"
  cat /tmp/h6-config.log >&2
fi

# ── AC-1 + AC-2 + AC-3: co-mount integration (real MCP-over-HTTP) ──────────────

if CGO_ENABLED=1 go test -count=1 -race -timeout=180s -run 'TestComount' ./test/integration/ >/tmp/h6-comount.log 2>&1; then
  ok "AC-1/AC-2: REST ingest visible via MCP retrieve over a second HTTP port (one cache)"
  ok "AC-3: dual-listener shutdown stops both before drain (no panic, -race)"
else
  failc "AC-1/AC-2/AC-3: co-mount integration test failed"
  cat /tmp/h6-comount.log >&2
fi

# ── AC-1 (live binary): the actual `serve` process binds the second MCP port ───

API_PORT=17160
MCP_PORT=17161
DB_PATH="${TMPDIR_SMOKE}/smokeh6.db"
CFG_PATH="${TMPDIR_SMOKE}/stowageh6.yaml"
cat > "$CFG_PATH" <<YAML
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
server:
  listen: ":${API_PORT}"
  mcp_listen: ":${MCP_PORT}"
YAML

"$BIN" migrate --config "$CFG_PATH" >/dev/null 2>&1 \
  && ok "AC-1: migrate applied" \
  || { failc "AC-1: migrate failed"; exit "$fails"; }

"$BIN" serve --config "$CFG_PATH" >"${TMPDIR_SMOKE}/serveh6.log" 2>&1 &
SRV_PID=$!

API_URL="http://127.0.0.1:${API_PORT}"
MCP_URL="http://127.0.0.1:${MCP_PORT}"
READY=0
for _ in $(seq 1 50); do
  sleep 0.1
  if curl -sf "${API_URL}/healthz" >/dev/null 2>&1; then READY=1; break; fi
done

if [ "$READY" -eq 0 ]; then
  failc "AC-1: server did not become ready within 5 s"
  cat "${TMPDIR_SMOKE}/serveh6.log" >&2
else
  # Bootstrap an admin key on the API port; it is valid on the co-mounted MCP
  # port too (same store, one keyring).
  KEY_RESP=$(curl -sf -X POST "${API_URL}/v1/admin/keys" \
    -H "Content-Type: application/json" \
    -d '{"tenant_id":"smokeh6-tenant","role":"admin"}' 2>/dev/null || true)
  API_KEY=$(printf '%s' "$KEY_RESP" | sed -n 's/.*"plaintext"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

  if [ -z "$API_KEY" ]; then
    failc "AC-1: could not bootstrap API key (response: ${KEY_RESP})"
  else
    # REST surface answers.
    REC=$(curl -sf -X POST "${API_URL}/v1/records" \
      -H "Content-Type: application/json" -H "Authorization: Bearer ${API_KEY}" \
      -d '{"records":[{"role":"user","content":"co-mount smoke record","session_id":"h6"}]}' 2>/dev/null || true)
    if printf '%s' "$REC" | grep -q '"ids"'; then
      ok "AC-1: REST POST /v1/records answers on the API port"
    else
      failc "AC-1: REST ingest failed (response: ${REC})"
    fi

    # The co-mounted MCP port answers an MCP `initialize` over HTTP, with auth.
    INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}'
    MCP_RESP=$(curl -s -X POST "${MCP_URL}" \
      -H "Authorization: Bearer ${API_KEY}" \
      -H "Content-Type: application/json" \
      -H "Accept: application/json, text/event-stream" \
      -d "${INIT}" 2>/dev/null || true)
    if printf '%s' "$MCP_RESP" | grep -q 'protocolVersion'; then
      ok "AC-1: co-mounted MCP port answers MCP initialize over HTTP"
    else
      failc "AC-1: MCP port did not answer initialize (response: ${MCP_RESP})"
    fi

    # KeyringMiddleware guards the MCP port: no Bearer ⇒ 401.
    CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${MCP_URL}" \
      -H "Content-Type: application/json" \
      -H "Accept: application/json, text/event-stream" \
      -d "${INIT}" 2>/dev/null || true)
    if [ "$CODE" = "401" ]; then
      ok "AC-1: MCP port enforces auth (401 without Bearer)"
    else
      failc "AC-1: MCP port did not require auth (got HTTP ${CODE})"
    fi
  fi
  kill "$SRV_PID" 2>/dev/null
  wait "$SRV_PID" 2>/dev/null
  SRV_PID=""
fi

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
if [ "$fails" -eq 0 ]; then
  echo "phase-h6 smoke: ALL CHECKS PASSED"
else
  echo "phase-h6 smoke: $fails check(s) FAILED" >&2
fi
exit "$fails"
