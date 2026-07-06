#!/usr/bin/env bash
# Phase ae12 — memory_ingest_run, the Harbor run-completion sink (D-153).
#
# Boots jwt-mode MCP-over-HTTP (static JWKS + sqlite; reuses the ae11 smoke's
# test-only JWT minter) and drives the run-completion-sink end to end with curl:
# the 24th tool is on the open-handshake tools/list, a bearer-less call is 401,
# a per-call-bearer call with a full 13-key format_version-1 Harbor payload
# ingests the transcript verbatim (stamped by the JWT scope), a format_version-2
# payload and a mismatched tenant_id fail closed, and memory_ingest is unchanged.
#
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { printf 'OK   %s\n' "$*"; }
failc(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip() { printf 'SKIP %s\n' "$*"; }

# Surface gate: SKIP cleanly until the tool lands (CLAUDE.md §4.2).
if ! grep -q "memory_ingest_run" internal/mcpserver/server.go 2>/dev/null; then
  skip "ae12 surface not built yet (memory_ingest_run absent)"
  exit 0
fi

BIN=/tmp/stowage-smoke-ae12
TMPDIR_SMOKE=$(mktemp -d)
MINTDIR=$(mktemp -d "$(pwd)/.ae12mint.XXXXXX")
cleanup() {
  kill "${MCP_PID:-}" 2>/dev/null
  rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE" "$MINTDIR"
}
trap cleanup EXIT

# ── Build ───────────────────────────────────────────────────────────────────
CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Mint a test JWT + matching static JWKS (test-only signer, ae11 pattern) ──
JWKS_PATH="${TMPDIR_SMOKE}/jwks.json"
cat > "${MINTDIR}/main.go" <<'GO'
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// args: <jwksOutPath> <tenant> <user>
func main() {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	const kid = "ae12-smoke-kid"
	pub := priv.PublicKey
	jwks := map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": kid, "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}}
	jb, _ := json.Marshal(jwks)
	if err := os.WriteFile(os.Args[1], jb, 0o600); err != nil {
		panic(err)
	}
	claims := jwt.MapClaims{
		"tenant": os.Args[2], "user": os.Args[3], "session": "s1",
		"iss": "harbor", "aud": "stowage", "sub": os.Args[3],
		"scopes": []string{"read"},
		"iat":    time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	if err != nil {
		panic(err)
	}
	_, _ = os.Stdout.WriteString(signed)
}
GO

TOKEN=$(go run "$MINTDIR" "$JWKS_PATH" ae12-smoke ae12-user 2>/dev/null || true)
if [ -n "$TOKEN" ] && [ -s "$JWKS_PATH" ]; then
  ok "test JWT + static JWKS minted (test-only signer)"
else
  skip "test JWT mint unavailable (go run signer failed) — authenticated checks skipped"
  echo ""
  echo "phase-ae12 smoke: $fails check(s) FAILED"
  exit "$fails"
fi

# ── Boot MCP-over-HTTP in jwt mode ───────────────────────────────────────────
MCP_PORT=17191
DB_PATH="${TMPDIR_SMOKE}/ae12.db"
CFG="${TMPDIR_SMOKE}/jwt.yaml"
cat > "$CFG" <<YAML
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
auth:
  mode: jwt
  issuer: harbor
  audience: stowage
  jwks:
    file: "${JWKS_PATH}"
    max_stale: 3600
YAML

"$BIN" migrate --config "$CFG" >/dev/null 2>&1 \
  && ok "migrate applied (jwt mode)" \
  || { failc "migrate failed (jwt mode)"; exit "$fails"; }

"$BIN" mcp --config "$CFG" --http ":${MCP_PORT}" >"${TMPDIR_SMOKE}/mcp.log" 2>&1 &
MCP_PID=$!

MCP_URL="http://127.0.0.1:${MCP_PORT}"
ACCEPT='Accept: application/json, text/event-stream'
CT='Content-Type: application/json'
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"ae12-smoke","version":"0"}}}'

READY=0
for _ in $(seq 1 50); do
  sleep 0.1
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$INIT" 2>/dev/null || true)
  if [ "$CODE" = "200" ]; then READY=1; break; fi
done
if [ "$READY" -eq 0 ]; then
  failc "jwt MCP server did not become ready"
  cat "${TMPDIR_SMOKE}/mcp.log" >&2
  exit "$fails"
fi

# ── bearer-less tools/list contains memory_ingest_run (24 tools) ─────────────
TOOLS_LIST='{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
LIST_BODY=$(curl -s -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$TOOLS_LIST" 2>/dev/null || true)
LIST_JSON=$(printf '%s' "$LIST_BODY" | sed -n 's/^data: //p')
[ -z "$LIST_JSON" ] && LIST_JSON="$LIST_BODY"
COUNT=$(printf '%s' "$LIST_JSON" | jq '.result.tools | length' 2>/dev/null || echo 0)
if printf '%s' "$LIST_JSON" | jq -e '.result.tools[] | select(.name=="memory_ingest_run")' >/dev/null 2>&1; then
  ok "tools/list contains memory_ingest_run"
else
  failc "tools/list missing memory_ingest_run (body: ${LIST_BODY})"
fi
if [ "$COUNT" = "24" ]; then
  ok "tools/list returns exactly 24 tools"
else
  failc "tools/list returned ${COUNT} tools, want 24"
fi

# ── bearer-less memory_ingest_run -> 401 (a tool like any other, D-152) ──────
RUN_ARGS='{"format_version":1,"tenant_id":"ae12-smoke","user_id":"ae12-user","session_id":"s1","run_id":"run-1","agent_id":"agent-x","outcome":"goal","started_at":"2026-07-06T10:00:00Z","completed_at":"2026-07-06T10:05:00Z","duration_ms":300000,"step_count":1,"tool_invocations":1,"conversation":[{"role":"user","kind":"goal","content":"book a flight","step":0},{"role":"assistant","kind":"final_answer","content":"booked","step":1,"at":"2026-07-06T10:02:00Z"}]}'
CALL=$(printf '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_ingest_run","arguments":%s}}' "$RUN_ARGS")
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$CALL" 2>/dev/null || true)
if [ "$CODE" = "401" ]; then
  ok "bearer-less memory_ingest_run -> 401"
else
  failc "bearer-less memory_ingest_run got HTTP ${CODE}, want 401"
fi

# ── per-call-bearer memory_ingest_run -> 200, ids count == entry count ───────
RESP=$(curl -s -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -H "Authorization: Bearer ${TOKEN}" -d "$CALL" 2>/dev/null || true)
RESP_JSON=$(printf '%s' "$RESP" | sed -n 's/^data: //p')
[ -z "$RESP_JSON" ] && RESP_JSON="$RESP"
IDS=$(printf '%s' "$RESP_JSON" | jq '.result.structuredContent.ids | length' 2>/dev/null || echo 0)
if [ "$IDS" = "2" ]; then
  ok "jwt memory_ingest_run ingested 2 records (ids count == entry count)"
else
  failc "jwt memory_ingest_run ids count = ${IDS}, want 2 (resp: ${RESP})"
fi

# ── sqlite: rows stamped with the JWT tenant/user + outcome + shared session ─
sleep 0.3 # let the durable append settle
ROWS=$(sqlite3 "file:${DB_PATH}?mode=ro" \
  "SELECT count(*) FROM records WHERE tenant_id='ae12-smoke' AND user_id='ae12-user' AND session_id='s1' AND outcome='success' AND outcome_detail='goal';" 2>/dev/null || echo 0)
if [ "$ROWS" = "2" ]; then
  ok "sqlite: 2 records stamped by the JWT tenant/user + outcome + shared session"
else
  failc "sqlite: expected 2 JWT-scoped records with outcome, got ${ROWS}"
fi

# ── format_version: 2 -> tool error naming version 1 ─────────────────────────
V2_ARGS=$(printf '%s' "$RUN_ARGS" | sed 's/"format_version":1/"format_version":2/')
V2_CALL=$(printf '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_ingest_run","arguments":%s}}' "$V2_ARGS")
V2_RESP=$(curl -s -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -H "Authorization: Bearer ${TOKEN}" -d "$V2_CALL" 2>/dev/null || true)
if printf '%s' "$V2_RESP" | grep -q 'want 1'; then
  ok "format_version 2 -> tool error naming supported version 1"
else
  failc "format_version 2 did not fail loudly naming version 1 (resp: ${V2_RESP})"
fi

# ── mismatched tenant_id -> fail closed ──────────────────────────────────────
BAD_ARGS=$(printf '%s' "$RUN_ARGS" | sed 's/"tenant_id":"ae12-smoke"/"tenant_id":"evilcorp"/')
BAD_CALL=$(printf '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"memory_ingest_run","arguments":%s}}' "$BAD_ARGS")
BAD_RESP=$(curl -s -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -H "Authorization: Bearer ${TOKEN}" -d "$BAD_CALL" 2>/dev/null || true)
if printf '%s' "$BAD_RESP" | grep -qi 'does not match'; then
  ok "mismatched tenant_id -> rejected (fail closed, D-153)"
else
  failc "mismatched tenant_id was not rejected (resp: ${BAD_RESP})"
fi

# ── memory_ingest regression: a plain records call still succeeds ────────────
ING_ARGS='{"records":[{"role":"user","content":"plain ingest still works"}]}'
ING_CALL=$(printf '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"memory_ingest","arguments":%s}}' "$ING_ARGS")
ING_RESP=$(curl -s -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -H "Authorization: Bearer ${TOKEN}" -d "$ING_CALL" 2>/dev/null || true)
ING_JSON=$(printf '%s' "$ING_RESP" | sed -n 's/^data: //p')
[ -z "$ING_JSON" ] && ING_JSON="$ING_RESP"
ING_IDS=$(printf '%s' "$ING_JSON" | jq '.result.structuredContent.ids | length' 2>/dev/null || echo 0)
if [ "$ING_IDS" = "1" ]; then
  ok "memory_ingest regression: plain records call still succeeds"
else
  failc "memory_ingest regression failed (ids=${ING_IDS}, resp: ${ING_RESP})"
fi

kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null; MCP_PID=""

# ── Summary ─────────────────────────────────────────────────────────────────
echo ""
if [ "$fails" -eq 0 ]; then
  echo "phase-ae12 smoke: ALL CHECKS PASSED"
else
  echo "phase-ae12 smoke: $fails check(s) FAILED" >&2
fi
exit "$fails"
