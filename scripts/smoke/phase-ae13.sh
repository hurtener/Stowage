#!/usr/bin/env bash
# Phase ae13 — topics resolve tenant-wide (D-154) smoke checks.
#
# Boots ONE `stowage serve` in JWT mode with the MCP-over-HTTP surface
# co-mounted (server.mcp_listen) and a mock gateway, then exercises the
# sub-tenant paths D-154 repairs:
#
#   1. PUT two explicit topics via HTTP /v1/topics under a JWT bearer.
#   2. MCP memory_topics list under a user-carrying (JWT) bearer returns the
#      two tenant topics and no pack:preferences keys (the ae13 fix — pre-D-154
#      a per-user scope matched zero stored topics and fell back to the default
#      pack).
#   3. per-user ingest + explicit flush: with the mock gateway scripted to tag
#      a candidate with the tenant topic key AND a default-pack key,
#      filterToActiveTopics keeps ONLY the tenant topic (explicit topics
#      suppress the default pack), proving the per-user flush resolved the
#      tenant's configured topic set — the strongest hermetic assertion
#      available (memory→topic junction row in sqlite).
#   4. tenant-only HTTP GET /v1/topics regression: the previously-working path
#      still returns the two topics.
#   5. a fresh zero-config tenant (no stored topics) still resolves the profile
#      default pack (pack:preferences).
#
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { printf 'OK   %s\n' "$*"; }
failc(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip() { printf 'SKIP %s\n' "$*"; }

# Surface gate: SKIP cleanly until the fix lands (CLAUDE.md §4.2).
if ! grep -q "topicScope" internal/topics/topics.go 2>/dev/null; then
  skip "ae13 fix not built yet (topicScope normalization absent)"
  exit 0
fi

BIN=/tmp/stowage-smoke-ae13
TMPDIR_SMOKE=$(mktemp -d)
MINTDIR=$(mktemp -d "$(pwd)/.ae13mint.XXXXXX")
cleanup() {
  kill "${SERVER_PID:-}" 2>/dev/null
  rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE" "$MINTDIR"
}
trap cleanup EXIT

# ── Build ───────────────────────────────────────────────────────────────────
CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Mint two test JWTs + matching static JWKS (test-only signer, ae11/ae12) ──
JWKS_PATH="${TMPDIR_SMOKE}/jwks.json"
cat > "${MINTDIR}/main.go" <<'GO'
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// args: <jwksOutPath> <privOutPath> <tenant> <user> <session>
// Generates an RSA keypair once (writing JWKS + the private key to privOutPath),
// reusing the persisted key on later calls so every signed token validates
// against the single static JWKS (a fresh keypair per call would invalidate
// previously-minted tokens).
const kid = "ae13-smoke-kid"

func main() {
	jwksPath, privPath := os.Args[1], os.Args[2]
	var priv *rsa.PrivateKey
	if data, err := os.ReadFile(privPath); err == nil {
		p, _ := pem.Decode(data)
		if p != nil {
			k, derr := x509raw(p.Bytes)
			if derr == nil {
				priv = k
			}
		}
	}
	if priv == nil {
		var err error
		priv, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		_ = os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: rawPriv(priv)}), 0o600)
	}
	pub := priv.PublicKey
	jwks := map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": kid, "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}}
	jb, _ := json.Marshal(jwks)
	if err := os.WriteFile(jwksPath, jb, 0o600); err != nil {
		panic(err)
	}
	claims := jwt.MapClaims{
		"tenant": os.Args[3], "user": os.Args[4], "session": os.Args[5],
		"iss": "harbor", "aud": "stowage", "sub": os.Args[4],
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

# Minimal PKCS#1 helpers so the script needs no extra x509 plumbing beyond stdlib.
cat > "${MINTDIR}/keywrap.go" <<'GO'
package main

import (
	"crypto/rsa"
	"crypto/x509"
)

func rawPriv(k *rsa.PrivateKey) []byte { return x509.MarshalPKCS1PrivateKey(k) }
func x509raw(b []byte) (*rsa.PrivateKey, error) { return x509.ParsePKCS1PrivateKey(b) }
GO

PRIV_PATH="${TMPDIR_SMOKE}/priv.pem"
mint() { go run "$MINTDIR" "$JWKS_PATH" "$PRIV_PATH" "$@" 2>/dev/null; }

# Tenant A: the one with two configured topics + a per-user caller.
TENANT_A="ae13-smoke"
TOKEN_A=$(mint "$TENANT_A" "ae13-user" "ae13-sess")
# Tenant B: a fresh zero-config tenant (no topics) → default-pack fallback.
TENANT_B="ae13-fresh"
TOKEN_B=$(mint "$TENANT_B" "ae13-fresh-u" "ae13-fresh-s")

if [ -n "$TOKEN_A" ] && [ -n "$TOKEN_B" ] && [ -s "$JWKS_PATH" ]; then
  ok "two test JWTs + static JWKS minted (test-only signer)"
else
  skip "test JWT mint unavailable (go run signer failed) — authenticated checks skipped"
  echo ""
  echo "phase-ae13 smoke: $fails check(s) FAILED"
  exit "$fails"
fi

# ── Boot `stowage serve` in JWT mode with the MCP surface co-mounted ─────────
HTTP_PORT=$(( 50000 + RANDOM % 10000 ))
MCP_PORT=$(( 50000 + RANDOM % 10000 ))
DB_PATH="${TMPDIR_SMOKE}/ae13.db"
CFG="${TMPDIR_SMOKE}/jwt.yaml"
cat > "$CFG" <<YAML
server:
  listen: ":${HTTP_PORT}"
  mcp_listen: ":${MCP_PORT}"
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
  embed_dims: 8
auth:
  mode: jwt
  issuer: harbor
  audience: stowage
  jwks:
    file: "${JWKS_PATH}"
    max_stale: 3600
YAML

# Lazy mock-gateway script file (read at each Complete call; ae13 writes a
# candidate after it has captured the ingested record ID).
export STOWAGE_MOCK_SCRIPT="${TMPDIR_SMOKE}/mockscript.json"

"$BIN" migrate --config "$CFG" >/dev/null 2>&1 \
  && ok "migrate applied (jwt mode)" \
  || { failc "migrate failed (jwt mode)"; exit "$fails"; }

"$BIN" serve --config "$CFG" >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SERVER_PID=$!

HTTP_URL="http://127.0.0.1:${HTTP_PORT}"
MCP_URL="http://127.0.0.1:${MCP_PORT}"
ACCEPT='Accept: application/json, text/event-stream'
CT='Content-Type: application/json'
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"ae13-smoke","version":"0"}}}'

# Wait for the HTTP API + the co-mounted MCP surface.
READY=0
for _ in $(seq 1 80); do
  sleep 0.1
  HC=$(curl -s -o /dev/null -w '%{http_code}' "${HTTP_URL}/healthz" 2>/dev/null || true)
  MC=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -d "$INIT" 2>/dev/null || true)
  if [ "$HC" = "200" ] && [ "$MC" = "200" ]; then READY=1; break; fi
done
if [ "$READY" -eq 0 ]; then
  failc "jwt serve did not become ready (HTTP + MCP)"
  cat "${TMPDIR_SMOKE}/serve.log" >&2
  exit "$fails"
fi

httpc() { # method url [body-file-or-] token
  local out="${TMPDIR_SMOKE}/httpresp"
  if [ -n "${3:-}" ]; then
    curl -s -X "$1" "${HTTP_URL}$2" -H "Authorization: Bearer $4" \
      -H "Content-Type: application/json" --data-binary "@$3" -o "$out" -w '%{http_code}' 2>/dev/null
  else
    curl -s -X "$1" "${HTTP_URL}$2" -H "Authorization: Bearer $4" -o "$out" -w '%{http_code}' 2>/dev/null
  fi
}

# ── 1) PUT two explicit topics via HTTP /v1/topics (JWT bearer) ───────────────
printf '[{"key":"ae13-topic-a","description":"first tenant topic","status":"active"},{"key":"ae13-topic-b","description":"second tenant topic","status":"active"}]' >"${TMPDIR_SMOKE}/topics.json"
STATUS=$(httpc PUT /v1/topics "${TMPDIR_SMOKE}/topics.json" "$TOKEN_A")
if [ "$STATUS" = "200" ]; then
  ok "PUT two explicit topics via HTTP /v1/topics -> 200"
else
  failc "PUT /v1/topics -> 200 (got ${STATUS}; body: $(cat "${TMPDIR_SMOKE}/httpresp"))"
fi

# ── 4) tenant-only HTTP GET /v1/topics regression: both explicit topics ───────
STATUS=$(httpc GET /v1/topics "" "$TOKEN_A")
if [ "$STATUS" = "200" ]; then
  A_SRC=$(grep -o '"source":"[^"]*"' "${TMPDIR_SMOKE}/httpresp" | head -1 | sed 's/.*":"\(.*\)"/\1/')
  if grep -q '"key":"ae13-topic-a"' "${TMPDIR_SMOKE}/httpresp" && grep -q '"key":"ae13-topic-b"' "${TMPDIR_SMOKE}/httpresp" && [ "$A_SRC" = "explicit" ]; then
    ok "tenant-only HTTP GET /v1/topics -> two explicit topics (regression)"
  else
    failc "tenant-only GET /v1/topics: want two explicit tenant topics (source=${A_SRC})"
  fi
else
  failc "GET /v1/topics -> 200 (got ${STATUS})"
fi

# ── 2) MCP memory_topics list under a user-carrying JWT bearer -> 2 topics ────
TOPICS_LIST='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"memory_topics","arguments":{"action":"list"}}}'
RESP=$(curl -s -X POST "$MCP_URL" -H "$CT" -H "$ACCEPT" -H "Authorization: Bearer ${TOKEN_A}" -d "$TOPICS_LIST" 2>/dev/null || true)
RJSON=$(printf '%s' "$RESP" | sed -n 's/^data: //p')
[ -z "$RJSON" ] && RJSON="$RESP"
TCOUNT=$(printf '%s' "$RJSON" | jq '.result.structuredContent.topics | length' 2>/dev/null || echo 0)
HAS_PACK=$(printf '%s' "$RJSON" | jq -r '.result.structuredContent.topics[].source' 2>/dev/null | grep -c '^pack:preferences$' || true)
if [ "$TCOUNT" = "2" ] && [ "$HAS_PACK" = "0" ]; then
  ok "MCP memory_topics list under user-carrying scope -> 2 tenant topics, no pack:preferences"
else
  failc "MCP memory_topics list: want 2 topics / 0 pack:preferences, got ${TCOUNT} topics / ${HAS_PACK} pack:preferences (resp: ${RESP})"
fi

# ── 3) per-user ingest + flush resolves the tenant topic set ─────────────────
# Ingest two records under the tenant's per-user scope (user_id in the body;
# the HTTP API stamps user_id from the record, the JWT supplies the tenant).
printf '{"records":[{"role":"user","content":"Tell me about PostgreSQL.","user_id":"ae13-user","session_id":"ae13-sess","branch_id":"ae13-br"},{"role":"assistant","content":"PostgreSQL is a powerful ACID-compliant relational database.","user_id":"ae13-user","session_id":"ae13-sess","branch_id":"ae13-br"}]}' >"${TMPDIR_SMOKE}/ingest.json"
STATUS=$(httpc POST /v1/records "${TMPDIR_SMOKE}/ingest.json" "$TOKEN_A")
if [ "$STATUS" = "202" ]; then
  ok "per-user ingest -> 202"
else
  failc "per-user ingest -> 202 (got ${STATUS}; body: $(cat "${TMPDIR_SMOKE}/httpresp"))"
fi

sleep 0.5
# Capture the ingested record IDs and script the mock gateway to return a
# candidate tagged with BOTH the tenant topic key and a default-pack key.
ID1=$(sqlite3 "file:${DB_PATH}?mode=ro" \
  "SELECT id FROM records WHERE tenant_id='${TENANT_A}' ORDER BY created_at, id LIMIT 1;" 2>/dev/null || true)
ID2=$(sqlite3 "file:${DB_PATH}?mode=ro" \
  "SELECT id FROM records WHERE tenant_id='${TENANT_A}' ORDER BY created_at, id LIMIT 1 OFFSET 1;" 2>/dev/null || true)
if [ -z "$ID1" ] || [ -z "$ID2" ]; then
  failc "could not capture ingested record IDs (ID1=${ID1} ID2=${ID2}) — flush assertion skipped"
else
  cat > "${TMPDIR_SMOKE}/mockscript.json" <<MOCKEOF
[{"candidates":[{"kind":"preference","content":"User prefers PostgreSQL for relational workloads.","context":"stated preference","entities":["postgresql"],"keywords":["postgres","database"],"anticipated_queries":["preferred database","postgresql preference","relational choice"],"importance":3,"confidence":0.9,"topics":["ae13-topic-a","user-preferences"],"provenance":[{"record_id":"${ID1}","span_start":0,"span_end":5},{"record_id":"${ID2}","span_start":0,"span_end":10}]}]}]
MOCKEOF

  # httpc with empty body-file sends no body; emit the flush body via a tmp file.
  printf '{"trigger":"explicit"}' >"${TMPDIR_SMOKE}/flush.json"
  STATUS=$(curl -s -X POST "${HTTP_URL}/v1/buffers/ae13-sess%2Fae13-br/flush" \
    -H "Authorization: Bearer ${TOKEN_A}" -H "Content-Type: application/json" \
    --data-binary "@${TMPDIR_SMOKE}/flush.json" -o "${TMPDIR_SMOKE}/httpresp" -w '%{http_code}' 2>/dev/null || true)
  if [ "$STATUS" = "202" ]; then
    ok "per-user flush -> 202"
  else
    failc "per-user flush -> 202 (got ${STATUS}; body: $(cat "${TMPDIR_SMOKE}/httpresp"))"
  fi

  # Wait for extract → reconcile → commit, then assert the memory→topic junction.
  sleep 2.5
  TAG_A=$(sqlite3 "file:${DB_PATH}?mode=ro" \
    "SELECT count(*) FROM memory_topics mt JOIN memories m ON m.id=mt.memory_id WHERE m.tenant_id='${TENANT_A}' AND mt.topic_key='ae13-topic-a';" 2>/dev/null || echo 0)
  TAG_DEFAULT=$(sqlite3 "file:${DB_PATH}?mode=ro" \
    "SELECT count(*) FROM memory_topics mt JOIN memories m ON m.id=mt.memory_id WHERE m.tenant_id='${TENANT_A}' AND mt.topic_key='user-preferences';" 2>/dev/null || echo 0)
  if [ "$TAG_A" -ge 1 ] && [ "$TAG_DEFAULT" = "0" ]; then
    ok "per-user flush tagged memory with tenant topic key 'ae13-topic-a' (default-pack key stripped)"
  else
    failc "per-user flush topic tags: want >=1 'ae13-topic-a' and 0 'user-preferences', got a=${TAG_A} default=${TAG_DEFAULT}"
    cat "${TMPDIR_SMOKE}/serve.log" >&2
  fi
fi

# ── 5) fresh zero-config tenant still resolves the profile default pack ──────
STATUS=$(httpc GET /v1/topics "" "$TOKEN_B")
if [ "$STATUS" = "200" ]; then
  if grep -q '"source":"pack:preferences"' "${TMPDIR_SMOKE}/httpresp"; then
    ok "fresh zero-config tenant resolves the default pack (pack:preferences)"
  else
    failc "fresh tenant should show pack:preferences default-pack sources (body: $(cat "${TMPDIR_SMOKE}/httpresp"))"
  fi
else
  failc "fresh tenant GET /v1/topics -> 200 (got ${STATUS})"
fi

# ── Graceful shutdown ────────────────────────────────────────────────────────
kill -TERM "$SERVER_PID" 2>/dev/null
for _ in $(seq 1 10); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  sleep 0.3
done
if kill -0 "$SERVER_PID" 2>/dev/null; then
  failc "server did not exit after SIGTERM"
  kill -9 "$SERVER_PID" 2>/dev/null
else
  ok "clean SIGTERM shutdown"
fi

# ── Summary ─────────────────────────────────────────────────────────────────
echo ""
if [ "$fails" -eq 0 ]; then
  echo "phase-ae13 smoke: ALL CHECKS PASSED"
else
  echo "phase-ae13 smoke: $fails check(s) FAILED" >&2
fi
exit "$fails"