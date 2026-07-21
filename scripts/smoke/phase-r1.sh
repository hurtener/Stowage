#!/usr/bin/env bash
# Phase r1 — single-port MCP co-mount (server.mcp_mount=shared, D-155).
#
# For single-port PaaS free tiers (Render/Heroku/Fly) that expose exactly one
# port per service, `stowage serve` can co-mount the MCP-over-HTTP surface on the
# SAME port as the REST API, under the "/mcp" path prefix, instead of the default
# two-listener shape (D-074). This boots the built binary in jwt mode with
# server.mcp_mount=shared and proves that ONE port serves BOTH surfaces, while the
# D-074 invariant (MCP is not gated by the REST WriteTimeout; the strict tools/call
# bearer gate survives) still holds.
#
# Verifies:
#   AC-1  shared mode: GET /healthz on the API port -> 200 (REST on the shared port).
#   AC-2  shared mode: bearer-less POST /mcp initialize -> 200 (jwt open handshake, D-152).
#   AC-3  shared mode: bearer-less POST /mcp tools/list -> catalog has memory_retrieve.
#   AC-4  shared mode: bearer-less POST /mcp tools/call -> still rejected (strict gate survives).
#   AC-5  serve logs the "co-mounted on the API port" line (discoverability).
#   AC-6  config rejects server.mcp_mount=shared together with server.mcp_listen.
#   AC-7  default (trust_proxy off): a spoofed public Host 403s (DNS-rebinding guard active).
#   AC-8  server.mcp_trust_proxy=true: the same proxied request is served (guard relaxed, D-156).
#
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { printf 'OK   %s\n' "$*"; }
failc(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip() { printf 'SKIP %s\n' "$*"; }

# Surface gate: SKIP cleanly until the co-mount knob lands (CLAUDE.md §4.2).
if ! grep -q 'mcp_mount' internal/config/config.go 2>/dev/null; then
  skip "r1 surface not built yet (server.mcp_mount absent)"
  exit 0
fi

BIN=/tmp/stowage-smoke-r1
TMPDIR_SMOKE=$(mktemp -d)
# Minting the test JWT needs a signer; it lives in a throwaway package INSIDE the
# module (so golang-jwt resolves) and is removed on exit — Stowage itself never
# signs (verify-never-mint, ae7/AC-2). Mirrors phase-ae11.sh.
MINTDIR=$(mktemp -d "$(pwd)/.r1mint.XXXXXX")
cleanup() {
  kill "${SRV_PID:-}" 2>/dev/null
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
	const kid = "r1-smoke-kid"
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

TOKEN=$(go run "$MINTDIR" "$JWKS_PATH" r1-smoke r1-user 2>/dev/null || true)
if [ -z "$TOKEN" ] || [ ! -s "$JWKS_PATH" ]; then
  skip "test JWT mint unavailable (go run signer failed) — r1 co-mount checks skipped"
  exit "$fails"
fi
ok "test JWT + static JWKS minted (test-only signer)"

# ── Boot `stowage serve` in jwt mode with the MCP surface co-mounted (shared) ──
PORT=17191
DB_PATH="${TMPDIR_SMOKE}/r1.db"
CFG="${TMPDIR_SMOKE}/shared.yaml"
cat > "$CFG" <<YAML
server:
  listen: "127.0.0.1:${PORT}"
  mcp_mount: shared
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
telemetry:
  metrics_listen: "127.0.0.1:17192"
auth:
  mode: jwt
  issuer: harbor
  audience: stowage
  jwks:
    file: "${JWKS_PATH}"
    max_stale: 3600
YAML

env -u STOWAGE_GATEWAY_API_KEY "$BIN" serve --config "$CFG" >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SRV_PID=$!

BASE="http://127.0.0.1:${PORT}"
CT='Content-Type: application/json'
ACCEPT='Accept: application/json, text/event-stream'

# Readiness: poll /healthz until it answers 200.
READY=0
for _ in $(seq 1 60); do
  sleep 0.1
  CODE=$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/healthz" 2>/dev/null || true)
  if [ "$CODE" = "200" ]; then READY=1; break; fi
done
if [ "$READY" -eq 0 ]; then
  failc "shared: serve did not become ready on the co-mount port"
  cat "${TMPDIR_SMOKE}/serve.log" >&2
  exit "$fails"
fi

# ── AC-1: REST /healthz is on the shared port ───────────────────────────────
CODE=$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/healthz" 2>/dev/null || true)
if [ "$CODE" = "200" ]; then
  ok "AC-1: GET /healthz -> 200 (REST on the shared port)"
else
  failc "AC-1: GET /healthz got HTTP ${CODE}"
fi

# ── AC-2: MCP handshake is served bearer-less at /mcp on the same port ───────
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"r1-smoke","version":"0"}}}'
INIT_BODY=$(curl -s -X POST "${BASE}/mcp" -H "$CT" -H "$ACCEPT" -d "$INIT" 2>/dev/null || true)
if printf '%s' "$INIT_BODY" | grep -q 'protocolVersion'; then
  ok "AC-2: bearer-less POST /mcp initialize -> handshake served on the shared port"
else
  failc "AC-2: POST /mcp initialize did not return a valid result (body: ${INIT_BODY})"
fi

# ── AC-3: tools/list is served bearer-less at /mcp ──────────────────────────
LIST_BODY=$(curl -s -X POST "${BASE}/mcp" -H "$CT" -H "$ACCEPT" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' 2>/dev/null || true)
if printf '%s' "$LIST_BODY" | grep -q 'memory_retrieve'; then
  ok "AC-3: bearer-less POST /mcp tools/list -> catalog contains memory_retrieve"
else
  failc "AC-3: POST /mcp tools/list missing memory_retrieve (body: ${LIST_BODY})"
fi

# ── AC-4: the strict tools/call gate survives the co-mount ──────────────────
CALL='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"memory_retrieve","arguments":{"query":"x"}}}'
CALL_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/mcp" -H "$CT" -H "$ACCEPT" -d "$CALL" 2>/dev/null || true)
if [ "$CALL_CODE" = "401" ] || [ "$CALL_CODE" = "403" ]; then
  ok "AC-4: bearer-less POST /mcp tools/call -> rejected (${CALL_CODE}; strict gate survives)"
else
  failc "AC-4: bearer-less POST /mcp tools/call got HTTP ${CALL_CODE} (want 401/403)"
fi

# ── AC-5: discoverability log line ──────────────────────────────────────────
if grep -q 'co-mounted on the API port' "${TMPDIR_SMOKE}/serve.log"; then
  ok "AC-5: serve logs the co-mount-on-API-port line"
else
  failc "AC-5: serve did not log the co-mount line"
  cat "${TMPDIR_SMOKE}/serve.log" >&2
fi

# ── AC-7: WITHOUT trust_proxy, the SDK DNS-rebinding guard 403s a request whose
# local socket addr is loopback but Host is a public domain — exactly what a
# reverse proxy (Render) presents. initialize is auth-open in jwt mode, so it
# reaches the guard (D-156). This is the failure a proxied deploy hits by default.
GUARD_CODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "${BASE}/mcp" \
  -H 'Host: stowage.onrender.com' -H "$CT" -H "$ACCEPT" -d "$INIT" 2>/dev/null || true)
if [ "$GUARD_CODE" = "403" ]; then
  ok "AC-7: default (trust_proxy off) 403s a spoofed public Host (DNS-rebinding guard active)"
else
  failc "AC-7: spoofed public Host got HTTP ${GUARD_CODE}, want 403 (guard should be active by default)"
fi

kill "$SRV_PID" 2>/dev/null; wait "$SRV_PID" 2>/dev/null || true; SRV_PID=""

# ── AC-8: WITH server.mcp_trust_proxy=true, the guard is relaxed so the same
# proxied request (loopback local addr + public Host) is served — the fix that
# lets the co-mount work behind Render/Heroku/Fly (D-156). Cross-origin +
# Content-Type protection stay on.
CFG_TP="${TMPDIR_SMOKE}/trustproxy.yaml"
cat > "$CFG_TP" <<YAML
server:
  listen: "127.0.0.1:${PORT}"
  mcp_mount: shared
  mcp_trust_proxy: true
store:
  driver: sqlite
  dsn: "${TMPDIR_SMOKE}/tp.db"
gateway:
  driver: mock
telemetry:
  metrics_listen: "127.0.0.1:17192"
auth:
  mode: jwt
  issuer: harbor
  audience: stowage
  jwks:
    file: "${JWKS_PATH}"
    max_stale: 3600
YAML
env -u STOWAGE_GATEWAY_API_KEY "$BIN" serve --config "$CFG_TP" >"${TMPDIR_SMOKE}/tp.log" 2>&1 &
SRV_PID=$!
READY=0
for _ in $(seq 1 60); do
  sleep 0.1
  [ "$(curl -s -o /dev/null -w '%{http_code}' "${BASE}/healthz" 2>/dev/null)" = "200" ] && { READY=1; break; }
done
if [ "$READY" -eq 0 ]; then
  failc "trust_proxy: serve did not become ready"
  cat "${TMPDIR_SMOKE}/tp.log" >&2
else
  TP_BODY=$(curl -s -X POST "${BASE}/mcp" -H 'Host: stowage.onrender.com' -H "$CT" -H "$ACCEPT" -d "$INIT" 2>/dev/null || true)
  if printf '%s' "$TP_BODY" | grep -q 'protocolVersion'; then
    ok "AC-8: trust_proxy=true serves a spoofed public Host (guard relaxed; proxied deploy works)"
  else
    failc "AC-8: trust_proxy=true did not serve the proxied initialize (body: ${TP_BODY})"
  fi
fi
kill "$SRV_PID" 2>/dev/null; wait "$SRV_PID" 2>/dev/null || true; SRV_PID=""

# ── AC-6: shared + mcp_listen is rejected (mutually exclusive) ──────────────
CFG_BAD="${TMPDIR_SMOKE}/bad.yaml"
cat > "$CFG_BAD" <<YAML
server:
  listen: "127.0.0.1:17195"
  mcp_mount: shared
  mcp_listen: "127.0.0.1:17196"
store:
  driver: sqlite
  dsn: "${TMPDIR_SMOKE}/bad.db"
gateway:
  driver: mock
YAML
BAD_OUT=$(env -u STOWAGE_GATEWAY_API_KEY "$BIN" serve --config "$CFG_BAD" 2>&1 || true)
if printf '%s' "$BAD_OUT" | grep -q 'mcp_listen: must be empty when server.mcp_mount=shared'; then
  ok "AC-6: config rejects shared + mcp_listen (mutually exclusive)"
else
  failc "AC-6: config did not reject shared + mcp_listen (out: ${BAD_OUT})"
fi

exit "$fails"
