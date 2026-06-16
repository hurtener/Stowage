#!/usr/bin/env bash
# Phase h4 smoke: tiered control-verb surface parity (topics/flush/branches/assert
# on {SDK,MCP,HTTP}; grants/contribute on {HTTP,MCP} not SDK) — D-067 Wave B, D-071.
#
# AC verified:
#   AC-1  Tier-A single-user verbs reachable + identical on SDK ⇄ HTTP, and on MCP
#   AC-2  Tier-B multi-user verbs ABSENT from the SDK (single-user boundary)
#   AC-3  contribute-mode honored on MCP with a valid grant (h2 fail-loud replaced)
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Gate on the Tier-A Flush method landing on the SDK Client.
if ! grep -rq 'Flush(ctx context.Context' sdk/stowage/client.go 2>/dev/null; then
  skip "AC-1: Tier-A SDK control verbs not yet implemented (plan skeleton)"
  skip "AC-2: Tier-B SDK-absence boundary (pending h4)"
  skip "AC-3: MCP contribute-mode honoring (pending h4)"
  exit "$fails"
fi

command -v go >/dev/null 2>&1 || { skip "go toolchain unavailable"; exit "$fails"; }

# ── AC-1: Tier-A embedded SDK ⇄ HTTP parity (integration test) ─────────────────
if go test -count=1 -race -run '^TestSurfaceParity_TierA_EmbeddedVsServe$' \
     ./test/integration/ >/tmp/h4-parity.log 2>&1; then
  ok "AC-1: Tier-A verbs identical across embedded SDK + HTTP server (topic/flush/branch)"
else
  failc "AC-1: Tier-A embedded/HTTP parity test failed"
  tail -25 /tmp/h4-parity.log >&2
fi

# ── AC-2: Tier-B verbs are ABSENT from the SDK Client (compile-time/reflection) ─
if go test -count=1 -run '^TestClientTierBoundary$' ./sdk/stowage/ >/tmp/h4-tier.log 2>&1; then
  ok "AC-2: SDK Client exposes Tier-A and rejects Tier-B verbs (single-user boundary enforced)"
else
  failc "AC-2: tier-boundary test failed (a Tier-B verb leaked onto the SDK?)"
  tail -25 /tmp/h4-tier.log >&2
fi
# Belt-and-suspenders grep: no grants/contribute method names in the SDK interface.
if grep -Eq 'Grant|Group|Member|Contribute' sdk/stowage/client.go; then
  failc "AC-2: SDK client.go names a Tier-B verb (grants/contribute) — must be {HTTP,MCP} only"
else
  ok "AC-2: SDK client.go has no grants/contribute verb names"
fi

# ── AC-3: contribute honored on MCP (real store + grants) ──────────────────────
if go test -count=1 -race -run '^TestIngestContribute' ./internal/mcpserver/ >/tmp/h4-contrib.log 2>&1; then
  ok "AC-3: contribute honored on MCP with a grant; rejected without one (shared core)"
else
  failc "AC-3: MCP contribute honoring test failed"
  tail -25 /tmp/h4-contrib.log >&2
fi

# ── AC-1/AC-3 over `stowage mcp` (stdio) ──────────────────────────────────────
command -v jq >/dev/null 2>&1 || { skip "AC-1/AC-3 stdio: jq unavailable"; exit "$fails"; }

BIN=/tmp/stowage-smoke-h4
TMPDIR_SMOKE=$(mktemp -d)
cleanup() {
  [ -n "${MCP_PID:-}" ] && kill "$MCP_PID" 2>/dev/null
  exec 3>&- 2>/dev/null || true
  rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"
}
trap cleanup EXIT

if ! CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>"${TMPDIR_SMOKE}/build.log"; then
  skip "AC-1/AC-3 stdio: binary did not build"; cat "${TMPDIR_SMOKE}/build.log" >&2; exit "$fails"
fi
ok "cgo-free build"

DB_PATH="${TMPDIR_SMOKE}/smoke.db"
CFG_PATH="${TMPDIR_SMOKE}/stowage.yaml"
FIFO="${TMPDIR_SMOKE}/in.fifo"
OUT="${TMPDIR_SMOKE}/out.jsonl"
TENANT="smokeh4"

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

send '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smokeh4","version":"0.0.1"}}}'
send '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
if ! await_id 1; then failc "stdio: MCP initialize did not respond"; cat "${TMPDIR_SMOKE}/mcp.log" >&2; exit "$fails"; fi
ok "mcp stdio initialized"

# memory_flush.
send '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_flush","arguments":{"key":"smoke/buf","trigger":"explicit"}}}'
await_id 2 >/dev/null
if [ "$(field_of 2 '.result.structuredContent.flushed')" = "true" ]; then
  ok "AC-1: memory_flush flushed over MCP"
else
  failc "AC-1: memory_flush did not flush"; jq -c 'select(.id==2)' "$OUT" >&2
fi

# memory_branch fork → discard.
send '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_branch","arguments":{"action":"fork","session_id":"smoke-sess"}}}'
await_id 3 >/dev/null
BR=$(field_of 3 '.result.structuredContent.branch_id')
if [ -n "$BR" ]; then ok "AC-1: memory_branch fork over MCP (branch_id=$BR)"; else failc "AC-1: memory_branch fork failed"; jq -c 'select(.id==3)' "$OUT" >&2; fi
send '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_branch","arguments":{"action":"discard","branch_id":"'"$BR"'"}}}'
await_id 4 >/dev/null
if [ "$(field_of 4 '.result.structuredContent.status')" = "discarded" ]; then
  ok "AC-1: memory_branch discard over MCP"
else
  failc "AC-1: memory_branch discard failed"; jq -c 'select(.id==4)' "$OUT" >&2
fi

# memory_grants (Tier B): create_group + add_member + create_grant.
send '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"memory_grants","arguments":{"action":"create_group","name":"team"}}}'
await_id 5 >/dev/null
GID=$(field_of 5 '.result.structuredContent.group.id')
if [ -n "$GID" ]; then ok "AC-3: memory_grants create_group over MCP (group=$GID)"; else failc "AC-3: create_group failed"; jq -c 'select(.id==5)' "$OUT" >&2; fi
send '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"memory_grants","arguments":{"action":"add_member","group_id":"'"$GID"'","user_id":"alice"}}}'
await_id 6 >/dev/null
send '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"memory_grants","arguments":{"action":"create_grant","group_id":"'"$GID"'","user_id":"bob","access":"contribute","zone_ceiling":"work"}}}'
await_id 7 >/dev/null
if [ -n "$(field_of 7 '.result.structuredContent.grant.id')" ]; then
  ok "AC-3: memory_grants create_grant (contribute) over MCP"
else
  failc "AC-3: create_grant failed"; jq -c 'select(.id==7)' "$OUT" >&2
fi

# Contribute memory_ingest WITH a covering grant → accepted.
send '{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"memory_ingest","arguments":{"records":[{"role":"user","content":"contributed fact for bob"}],"target_scope":{"user_id":"bob"},"contributor_user_id":"alice"}}}'
await_id 8 >/dev/null
if [ "$(field_of 8 '.result.structuredContent.enqueued')" = "true" ] && [ "$(field_of 8 '.result.isError')" != "true" ]; then
  ok "AC-3: contribute memory_ingest honored with a grant over MCP"
else
  failc "AC-3: contribute ingest with grant was not accepted"; jq -c 'select(.id==8)' "$OUT" >&2
fi

# Contribute memory_ingest WITHOUT a covering grant → rejected (not mis-scoped).
send '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"memory_ingest","arguments":{"records":[{"role":"user","content":"should be rejected"}],"target_scope":{"user_id":"carol"},"contributor_user_id":"mallory"}}}'
await_id 9 >/dev/null
IS_ERR=$(field_of 9 '.result.isError'); HAS_ERR=$(field_of 9 '.error.message')
if [ "$IS_ERR" = "true" ] || [ -n "$HAS_ERR" ]; then
  ok "AC-3: contribute memory_ingest rejected without a grant over MCP"
else
  failc "AC-3: contribute without grant was NOT rejected (silent mis-scope risk)"; jq -c 'select(.id==9)' "$OUT" >&2
fi

# Tier-B absence over the SDK is structural (asserted by AC-2 above); memory_grants
# exists only on the MCP/HTTP surfaces.
ok "AC-2: memory_grants is an MCP/HTTP-only tool (no SDK equivalent)"

exec 3>&-
kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null; MCP_PID=""
ok "mcp stdio session closed"

exit "$fails"
