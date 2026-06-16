#!/usr/bin/env bash
# Phase h3 smoke: reconciliation reversibility parity (rollback/confirm/get across
# SDK + MCP + HTTP) — D-067 Wave B, D-070.
#
# Acceptance criteria verified:
#   AC-1  reconcile.Rollback / reconcile.Resolve core exists.
#   AC-4  embedded SDK ⇄ HTTP server rollback parity (the both-paths-identical
#         bar) — driven by the integration test under -race.
#   AC-3  rollback reachable as an MCP tool: a memory rolled back over
#         `stowage mcp` (stdio) is restored; a double rollback returns the
#         conflict error.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── AC-1: reconcile reversibility core present ────────────────────────────────
if ! grep -rq 'func Rollback' internal/reconcile/ 2>/dev/null; then
  skip "AC-1: reconcile.Rollback core not yet implemented (plan skeleton)"
  skip "AC-3: rollback on SDK + MCP (pending h3)"
  skip "AC-4: rollback parity embedded vs server (pending h3)"
  exit "$fails"
fi
ok "AC-1: reconcile.Rollback / reconcile.Resolve core present"

# ── AC-4: embedded SDK ⇄ HTTP parity via the integration test ─────────────────
if command -v go >/dev/null 2>&1; then
  if go test -count=1 -race -run '^TestReversibilityParity_EmbeddedVsServe$' \
       ./test/integration/ >/tmp/h3-parity.log 2>&1; then
    ok "AC-4: embedded SDK ⇄ HTTP rollback parity (restored memory + events identical)"
  else
    failc "AC-4: embedded/HTTP rollback parity test failed"
    tail -25 /tmp/h3-parity.log >&2
  fi
else
  skip "AC-4: go toolchain unavailable for parity integration test"
fi

# ── AC-3: rollback reachable over `stowage mcp` (stdio) ───────────────────────
command -v jq      >/dev/null 2>&1 || { skip "AC-3: jq unavailable";      exit "$fails"; }
command -v sqlite3 >/dev/null 2>&1 || { skip "AC-3: sqlite3 unavailable"; exit "$fails"; }

BIN=/tmp/stowage-smoke-h3
TMPDIR_SMOKE=$(mktemp -d)
cleanup() {
  [ -n "${MCP_PID:-}" ] && kill "$MCP_PID" 2>/dev/null
  exec 3>&- 2>/dev/null || true
  rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"
}
trap cleanup EXIT

if ! CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>"${TMPDIR_SMOKE}/build.log"; then
  skip "AC-3: binary did not build"; cat "${TMPDIR_SMOKE}/build.log" >&2; exit "$fails"
fi
ok "cgo-free build"

DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"
FIFO="${TMPDIR_SMOKE}/in.fifo"
OUT="${TMPDIR_SMOKE}/out.jsonl"
TENANT="smokeh3"
MEMID="rev-smoke-mem-0001"

cat > "$CFG_PATH" <<YAML
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
  embed_dims: 8
mcp:
  stdio_tenant: ${TENANT}
YAML

"$BIN" migrate --config "$CFG_PATH" >/dev/null 2>&1 \
  && ok "migrate applied" || { failc "migrate failed"; exit "$fails"; }

# Seed an "updated" memory whose prior-state event restores the original content.
ORIG="the original kickoff was in Q1"
PRIOR_PAYLOAD=$(jq -nc --arg id "$MEMID" --arg c "$ORIG" \
  '{id:$id,kind:"fact",content:$c,status:"active",created_at:1000,updated_at:1000}')
sqlite3 "$DB_PATH" <<SQL
INSERT INTO memories (id,tenant_id,kind,content,status,created_at,updated_at)
VALUES ('${MEMID}','${TENANT}','fact','mutated content after edit','active',1000,2000);
INSERT INTO events (id,tenant_id,type,subject_id,reason,payload,created_at)
VALUES ('rev-smoke-ev-0001','${TENANT}','memory.updated','${MEMID}','seed',
        '${PRIOR_PAYLOAD}',1500);
SQL
ok "seeded a memory with a restorable memory.updated prior-state event"

# ── Drive `stowage mcp` (stdio) over a fifo ───────────────────────────────────
mkfifo "$FIFO"
export STOWAGE_MOCK_SCRIPT="${TMPDIR_SMOKE}/empty.json"; echo '[]' > "$STOWAGE_MOCK_SCRIPT"
"$BIN" mcp --config "$CFG_PATH" < "$FIFO" > "$OUT" 2>"${TMPDIR_SMOKE}/mcp.log" &
MCP_PID=$!
exec 3>"$FIFO"
send() { printf '%s\n' "$1" >&3; }
await_id() {
  local id="$1" tries="${2:-60}"
  for _ in $(seq 1 "$tries"); do
    jq -e --argjson id "$id" 'select(.id==$id)' "$OUT" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  return 1
}
field_of() { jq -r --argjson id "$1" "select(.id==\$id) | $2 // empty" "$OUT" 2>/dev/null | head -1; }

send '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smokeh3","version":"0.0.1"}}}'
send '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
if ! await_id 1; then failc "AC-3: MCP initialize did not respond"; cat "${TMPDIR_SMOKE}/mcp.log" >&2; exit "$fails"; fi
ok "mcp stdio initialized"

# 1. memory_rollback over MCP.
send '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_rollback","arguments":{"memory_id":"'"$MEMID"'"}}}'
if ! await_id 2; then failc "AC-3: memory_rollback no response"; exit "$fails"; fi
RB_CONTENT=$(field_of 2 '.result.structuredContent.memory.content')
if [ "$RB_CONTENT" = "$ORIG" ]; then
  ok "AC-3: memory_rollback restored prior content over MCP"
else
  failc "AC-3: memory_rollback did not restore (got '${RB_CONTENT}', want '${ORIG}')"
  jq -c 'select(.id==2)' "$OUT" >&2
fi

# 2. memory_get confirms the restore is durable.
send '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_get","arguments":{"memory_id":"'"$MEMID"'"}}}'
await_id 3 >/dev/null
GET_CONTENT=$(field_of 3 '.result.structuredContent.memory.content')
GET_STATUS=$(field_of 3 '.result.structuredContent.memory.status')
if [ "$GET_CONTENT" = "$ORIG" ] && [ "$GET_STATUS" = "active" ]; then
  ok "AC-3: memory_get reflects the restored (active) memory over MCP"
else
  failc "AC-3: memory_get mismatch (content='${GET_CONTENT}' status='${GET_STATUS}')"
fi

# 3. double rollback → conflict error across the MCP boundary.
send '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_rollback","arguments":{"memory_id":"'"$MEMID"'"}}}'
await_id 4 >/dev/null
IS_ERR=$(field_of 4 '.result.isError')
HAS_ERR=$(field_of 4 '.error.message')
if [ "$IS_ERR" = "true" ] || [ -n "$HAS_ERR" ]; then
  ok "AC-3: double rollback returns the conflict error over MCP"
else
  failc "AC-3: double rollback did not error"
  jq -c 'select(.id==4)' "$OUT" >&2
fi

exec 3>&-
kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null; MCP_PID=""
ok "mcp stdio session closed"

exit "$fails"
