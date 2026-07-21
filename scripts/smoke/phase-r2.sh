#!/usr/bin/env bash
# Phase r2 — ledger descriptor self-description seam (D-157).
#
# Stowage advertises its OWN memory-ledger descriptor at
# GET /.well-known/pengui-memory-ledger so the Pengui console renders/mutates the
# ledger from Stowage's authoritative shape (source "well-known") instead of the
# console's hardcoded built-in bridge (which can drift). The endpoint is PUBLIC
# (non-sensitive API-shape metadata, like /healthz).
#
# Verifies:
#   AC-1  GET /.well-known/pengui-memory-ledger -> 200, PUBLIC (no bearer needed) even in jwt mode.
#   AC-2  the descriptor is well-formed: version "1", id_field "id", list.items_field "memories".
#   AC-3  the declared mutate ops are exactly the API's real ones: confirm, reject, rollback.
#   AC-4  the declared list/get/mutate paths are routes the API actually serves.
#
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { printf 'OK   %s\n' "$*"; }
failc(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip() { printf 'SKIP %s\n' "$*"; }

# Surface gate: SKIP cleanly until the endpoint lands (CLAUDE.md §4.2).
if ! grep -rq 'pengui-memory-ledger' internal/api/*.go 2>/dev/null; then
  skip "r2 surface not built yet (ledger descriptor absent)"
  exit 0
fi

command -v jq >/dev/null 2>&1 || { skip "jq not installed — r2 descriptor checks skipped"; exit 0; }

BIN=/tmp/stowage-smoke-r2
TMPDIR_SMOKE=$(mktemp -d)
trap 'kill "${SRV_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# Boot in jwt mode (needs a JWKS to boot) to prove the descriptor is PUBLIC even
# when the rest of the surface is bearer-guarded. A minimal static JWKS suffices
# for boot; the descriptor probe carries no token.
JWKS="${TMPDIR_SMOKE}/jwks.json"
MINTDIR=$(mktemp -d "$(pwd)/.r2mint.XXXXXX")
trap 'kill "${SRV_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE" "$MINTDIR"' EXIT
cat > "${MINTDIR}/main.go" <<'GO'
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"os"
)

func main() {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := priv.PublicKey
	jwks := map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "r2", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}}
	b, _ := json.Marshal(jwks)
	_ = os.WriteFile(os.Args[1], b, 0o600)
}
GO
if ! go run "$MINTDIR" "$JWKS" 2>/dev/null || [ ! -s "$JWKS" ]; then
  skip "JWKS generation failed — r2 checks skipped"
  exit "$fails"
fi

PORT=17197
CFG="${TMPDIR_SMOKE}/r2.yaml"
cat > "$CFG" <<YAML
server:
  listen: "127.0.0.1:${PORT}"
store:
  driver: sqlite
  dsn: "${TMPDIR_SMOKE}/r2.db"
gateway:
  driver: mock
telemetry:
  metrics_listen: "127.0.0.1:17198"
auth:
  mode: jwt
  issuer: harbor
  audience: stowage
  jwks:
    file: "${JWKS}"
    max_stale: 3600
YAML

env -u STOWAGE_GATEWAY_API_KEY "$BIN" serve --config "$CFG" >"${TMPDIR_SMOKE}/serve.log" 2>&1 &
SRV_PID=$!
for _ in $(seq 1 60); do
  sleep 0.1
  [ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/healthz" 2>/dev/null)" = "200" ] && break
done

URL="http://127.0.0.1:${PORT}/.well-known/pengui-memory-ledger"

# ── AC-1: public 200 (no bearer) even in jwt mode ───────────────────────────
CODE=$(curl -s -o "${TMPDIR_SMOKE}/desc.json" -w '%{http_code}' "$URL" 2>/dev/null || true)
if [ "$CODE" = "200" ]; then
  ok "AC-1: GET ${URL##*/} -> 200 (public, no bearer, in jwt mode)"
else
  failc "AC-1: descriptor got HTTP ${CODE} (want 200, public)"
  cat "${TMPDIR_SMOKE}/serve.log" >&2
fi

# ── AC-2: well-formed descriptor ────────────────────────────────────────────
if jq -e '.version=="1" and .id_field=="id" and .list.items_field=="memories" and .list.path=="/v1/memories" and (.fields|length>0)' \
     "${TMPDIR_SMOKE}/desc.json" >/dev/null 2>&1; then
  ok "AC-2: descriptor is well-formed (version 1, id_field id, list.items_field memories)"
else
  failc "AC-2: descriptor is malformed"
  cat "${TMPDIR_SMOKE}/desc.json" >&2
fi

# ── AC-3: mutate ops are exactly the API's real ones ────────────────────────
if [ "$(jq -r '.mutate_ops | keys | sort | join(",")' "${TMPDIR_SMOKE}/desc.json" 2>/dev/null)" = "confirm,reject,rollback" ]; then
  ok "AC-3: mutate_ops = confirm,reject,rollback (matches the real API)"
else
  failc "AC-3: mutate_ops mismatch: $(jq -c '.mutate_ops | keys' "${TMPDIR_SMOKE}/desc.json" 2>/dev/null)"
fi

# ── AC-4: declared paths are real routes ────────────────────────────────────
GET_P=$(jq -r '.get.path_template' "${TMPDIR_SMOKE}/desc.json" 2>/dev/null)
ROLLBACK_P=$(jq -r '.mutate_ops.rollback.path_template' "${TMPDIR_SMOKE}/desc.json" 2>/dev/null)
if grep -q "PATCH /v1/memories/{id}" internal/api/server.go \
   && grep -q "POST /v1/memories/{id}/rollback" internal/api/server.go \
   && [ "$GET_P" = "/v1/memories/{id}" ] && [ "$ROLLBACK_P" = "/v1/memories/{id}/rollback" ]; then
  ok "AC-4: declared get/mutate paths match real registered routes"
else
  failc "AC-4: declared paths do not match registered routes (get=${GET_P} rollback=${ROLLBACK_P})"
fi

exit "$fails"
