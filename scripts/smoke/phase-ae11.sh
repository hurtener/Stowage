#!/usr/bin/env bash
# Phase ae11 — method-aware MCP handshake auth (D-152) smoke checks.
#
# Boots the built binary as an MCP-over-HTTP server in jwt mode (static JWKS
# file + sqlite) and drives the motivating host's dial sequence with curl:
# a bearer-less handshake (initialize / notifications/initialized / tools/list)
# is served, but tools/call keeps requiring the per-call bearer. Then a keyring-
# mode reboot proves the strict gate is byte-identical, and a zero-config
# `stowage serve` start re-checks that invariant.
#
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { printf 'OK   %s\n' "$*"; }
failc(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip() { printf 'SKIP %s\n' "$*"; }

# Surface gate: SKIP cleanly until the classifier lands (CLAUDE.md §4.2).
if ! grep -q "MethodAwareAuthMiddleware" internal/mcpserver/*.go 2>/dev/null; then
  skip "ae11 surface not built yet (MethodAwareAuthMiddleware absent)"
  exit 0
fi

BIN=/tmp/stowage-smoke-ae11
TMPDIR_SMOKE=$(mktemp -d)
# Minting the test JWT needs a signer; it lives in a throwaway package INSIDE
# the module (so golang-jwt resolves) and is removed on exit — Stowage itself
# never signs (verify-never-mint, ae7/AC-2).
MINTDIR=$(mktemp -d "$(pwd)/.ae11mint.XXXXXX")
cleanup() {
  kill "${MCP_PID:-}" "${KR_PID:-}" "${SRV_PID:-}" 2>/dev/null
  rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE" "$MINTDIR"
}
trap cleanup EXIT

# ── Build ───────────────────────────────────────────────────────────────────
CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Mint a test JWT + matching static JWKS (test-only signer) ────────────────
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
	const kid = "ae11-smoke-kid"
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

TOKEN=$(go run "$MINTDIR" "$JWKS_PATH" ae11-smoke ae11-user 2>/dev/null || true)
MINT_OK=0
if [ -n "$TOKEN" ] && [ -s "$JWKS_PATH" ]; then
  MINT_OK=1
  ok "test JWT + static JWKS minted (test-only signer)"
else
  skip "test JWT mint unavailable (go run signer failed) — authenticated tools/call check skipped"
fi

# ── Boot MCP-over-HTTP in jwt mode (static JWKS file + sqlite) ───────────────
MCP_PORT=17181
DB_PATH="${TMPDIR_SMOKE}/ae11.db"
CFG_JWT="${TMPDIR_SMOKE}/jwt.yaml"
cat > "$CFG_JWT" <<YAML
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

"$BIN" migrate --config "$CFG_JWT" >/dev/null 2>&1 \
  && ok "migrate applied (jwt mode)" \
  || { failc "migrate failed (jwt mode)"; exit "$fails"; }

"$BIN" mcp --config "$CFG_JWT" --http ":${MCP_PORT}" >"${TMPDIR_SMOKE}/mcp.log" 2>&1 &
MCP_PID=$!

MCP_URL="http://127.0.0.1:${MCP_PORT}"
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"ae11-smoke","version":"0"}}}'
ACCEPT='Accept: application/json, text/event-stream'
CT='Content-Type: application/json'

# Readiness: poll bearer-less initialize until it answers 200.
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

# ── jwt mode: bearer-less handshake is served (D-152) ───────────────────────
INIT_BODY=$(curl -s -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$INIT" 2>/dev/null || true)
if printf '%s' "$INIT_BODY" | grep -q 'protocolVersion'; then
  ok "jwt: bearer-less initialize -> 200 (handshake served)"
else
  failc "jwt: bearer-less initialize did not return a valid result (body: ${INIT_BODY})"
fi

INITIALIZED='{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}'
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$INITIALIZED" 2>/dev/null || true)
if [ "$CODE" = "202" ] || [ "$CODE" = "200" ]; then
  ok "jwt: bearer-less notifications/initialized -> accepted (${CODE})"
else
  failc "jwt: bearer-less notifications/initialized got HTTP ${CODE}"
fi

TOOLS_LIST='{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
LIST_BODY=$(curl -s -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$TOOLS_LIST" 2>/dev/null || true)
if printf '%s' "$LIST_BODY" | grep -q 'memory_retrieve'; then
  ok "jwt: bearer-less tools/list -> catalog contains memory_retrieve"
else
  failc "jwt: bearer-less tools/list missing memory_retrieve (body: ${LIST_BODY})"
fi

# ── jwt mode: tools/call keeps requiring the per-call bearer ─────────────────
TOOLS_CALL='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_browse","arguments":{}}}'
CALL=$(curl -s -w '\n%{http_code}' -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$TOOLS_CALL" 2>/dev/null || true)
CODE=$(printf '%s' "$CALL" | tail -1)
BODY=$(printf '%s' "$CALL" | sed '$d')
if [ "$CODE" = "401" ] && printf '%s' "$BODY" | grep -q 'authorization required'; then
  ok "jwt: bearer-less tools/call -> 401 authorization required"
else
  failc "jwt: bearer-less tools/call got HTTP ${CODE} (body: ${BODY})"
fi

# Default-deny: a JSON-RPC batch array without a bearer is protected -> 401.
BATCH='[{"jsonrpc":"2.0","id":1,"method":"initialize"}]'
CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$BATCH" 2>/dev/null || true)
if [ "$CODE" = "401" ]; then
  ok "jwt: bearer-less batch array -> 401 (default-deny)"
else
  failc "jwt: bearer-less batch array got HTTP ${CODE}, want 401"
fi

# Authenticated tools/call with the per-call bearer -> 200.
if [ "$MINT_OK" -eq 1 ]; then
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$MCP_URL" \
    -H "$CT" -H "$ACCEPT" -H "Authorization: Bearer ${TOKEN}" -d "$TOOLS_CALL" 2>/dev/null || true)
  if [ "$CODE" = "200" ]; then
    ok "jwt: per-call-bearer tools/call -> 200 (authenticated)"
  else
    failc "jwt: per-call-bearer tools/call got HTTP ${CODE}, want 200"
  fi
else
  skip "jwt: authenticated tools/call (no minted token)"
fi

kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null; MCP_PID=""

# ── keyring-mode regression: the strict gate is byte-identical ──────────────
KR_PORT=17182
CFG_KR="${TMPDIR_SMOKE}/keyring.yaml"
cat > "$CFG_KR" <<YAML
store:
  driver: sqlite
  dsn: "${TMPDIR_SMOKE}/ae11kr.db"
gateway:
  driver: mock
YAML
"$BIN" migrate --config "$CFG_KR" >/dev/null 2>&1
"$BIN" mcp --config "$CFG_KR" --http ":${KR_PORT}" >"${TMPDIR_SMOKE}/kr.log" 2>&1 &
KR_PID=$!
KR_URL="http://127.0.0.1:${KR_PORT}"
KR_READY=0
for _ in $(seq 1 50); do
  sleep 0.1
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$KR_URL" -H "$CT" -H "$ACCEPT" -d "$INIT" 2>/dev/null || true)
  # keyring strict gate: bearer-less request is 401 once the server is up.
  if [ "$CODE" = "401" ]; then KR_READY=1; break; fi
done
if [ "$KR_READY" -eq 1 ]; then
  ok "keyring: bearer-less initialize -> 401 (strict gate byte-identical)"
else
  failc "keyring: bearer-less initialize did not return 401 (server up? see kr.log)"
  cat "${TMPDIR_SMOKE}/kr.log" >&2
fi
kill "$KR_PID" 2>/dev/null; wait "$KR_PID" 2>/dev/null; KR_PID=""

# ── zero-config `stowage serve` start unaffected (existing invariant) ───────
SRV_PORT=17183
CFG_SRV="${TMPDIR_SMOKE}/serve.yaml"
cat > "$CFG_SRV" <<YAML
store:
  driver: sqlite
  dsn: "${TMPDIR_SMOKE}/ae11srv.db"
gateway:
  driver: mock
server:
  listen: ":${SRV_PORT}"
YAML
"$BIN" migrate --config "$CFG_SRV" >/dev/null 2>&1
"$BIN" serve --config "$CFG_SRV" >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SRV_PID=$!
SRV_READY=0
for _ in $(seq 1 50); do
  sleep 0.1
  if curl -sf "http://127.0.0.1:${SRV_PORT}/healthz" >/dev/null 2>&1; then SRV_READY=1; break; fi
done
if [ "$SRV_READY" -eq 1 ]; then
  ok "zero-config stowage serve start unaffected (/healthz 200)"
else
  failc "zero-config stowage serve did not become ready"
  cat "${TMPDIR_SMOKE}/serve.log" >&2
fi
kill "$SRV_PID" 2>/dev/null; wait "$SRV_PID" 2>/dev/null; SRV_PID=""

# ── Summary ─────────────────────────────────────────────────────────────────
echo ""
if [ "$fails" -eq 0 ]; then
  echo "phase-ae11 smoke: ALL CHECKS PASSED"
else
  echo "phase-ae11 smoke: $fails check(s) FAILED" >&2
fi
exit "$fails"
