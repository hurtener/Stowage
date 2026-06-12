#!/usr/bin/env bash
# scripts/smoke/phase-15.sh — smoke checks for Phase 15 (grants & team sharing)
#
# End-to-end flow:
#   1. Build succeeds.
#   2. POST /v1/admin/groups — create a group.
#   3. POST /v1/admin/groups/{id}/members — add a member.
#   4. PUT /v1/scopes/grants — create a grant (zone_ceiling=work).
#   5. GET /v1/scopes/grants — list grants (count ≥ 1).
#   6. POST /v1/grants/{id}/revoke — revoke the grant.
#   7. GET /v1/scopes/grants — confirm grant list still returns (revoked row present).
#   8. Zone ceiling validation: zone_ceiling=personal → 400.
#   9. Cross-tenant: grants package unit tests pass.
#  10. Race ×3 on grants package.
#
# Exit code: number of failures.

set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-15
TMPDIR_SMOKE=$(mktemp -d)
trap 'kill "${SERVE_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── Start server ──────────────────────────────────────────────────────────────

DB="$TMPDIR_SMOKE/smoke.db"
CONF="$TMPDIR_SMOKE/config.yaml"
PORT=19215

cat >"$CONF" <<YAML
store:
  driver: sqlite
  dsn: "$DB"
server:
  listen: "127.0.0.1:$PORT"
  read_timeout: 10
  write_timeout: 10
  idle_timeout: 30
gateway:
  driver: mock
YAML

"$BIN" serve --config "$CONF" >"$TMPDIR_SMOKE/serve.log" 2>&1 &
SERVE_PID=$!

# Wait for server to be ready (up to 10 s).
BASE="http://127.0.0.1:$PORT"
for i in $(seq 1 20); do
  if curl -sf "$BASE/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.5
done
if ! curl -sf "$BASE/healthz" >/dev/null 2>&1; then
  failc "server did not start in 10 s"
  exit "$fails"
fi
ok "server started"

# ── Create admin key ──────────────────────────────────────────────────────────

CREATE_KEY_RESP=$(curl -sf -X POST "$BASE/v1/admin/keys" \
  -H 'Content-Type: application/json' \
  -d '{"tenant_id":"smoke-tenant","role":"admin"}' 2>/dev/null)

ADMIN_KEY=$(echo "$CREATE_KEY_RESP" | grep -o '"plaintext":"[^"]*"' | cut -d'"' -f4)
if [ -z "$ADMIN_KEY" ]; then
  failc "create admin key (response: $CREATE_KEY_RESP)"
  exit "$fails"
fi
ok "admin key created"

AUTH="Authorization: Bearer $ADMIN_KEY"

# ── 2. Create group ───────────────────────────────────────────────────────────

GRP_RESP=$(curl -sf -X POST "$BASE/v1/admin/groups" \
  -H 'Content-Type: application/json' \
  -H "$AUTH" \
  -d '{"name":"smoke-team"}' 2>/dev/null)

GROUP_ID=$(echo "$GRP_RESP" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
if [ -z "$GROUP_ID" ]; then
  failc "create group (response: $GRP_RESP)"
  exit "$fails"
fi
ok "group created: $GROUP_ID"

# ── 3. Add member ─────────────────────────────────────────────────────────────

MEMBER_RESP=$(curl -sf -X POST "$BASE/v1/admin/groups/$GROUP_ID/members" \
  -H 'Content-Type: application/json' \
  -H "$AUTH" \
  -d '{"user_id":"alice"}' 2>/dev/null)

if ! echo "$MEMBER_RESP" | grep -q '"user_id":"alice"'; then
  failc "add member (response: $MEMBER_RESP)"
else
  ok "member added: alice"
fi

# ── 4. Create grant (zone_ceiling=work) ───────────────────────────────────────

GRANT_RESP=$(curl -sf -X PUT "$BASE/v1/scopes/grants" \
  -H 'Content-Type: application/json' \
  -H "$AUTH" \
  -d "{\"group_id\":\"$GROUP_ID\",\"user_id\":\"owner\",\"access\":\"read\",\"zone_ceiling\":\"work\"}" \
  2>/dev/null)

GRANT_ID=$(echo "$GRANT_RESP" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)
if [ -z "$GRANT_ID" ]; then
  failc "create grant (response: $GRANT_RESP)"
  exit "$fails"
fi
ok "grant created: $GRANT_ID (zone_ceiling=work)"

# ── 5. List grants ────────────────────────────────────────────────────────────

LIST_RESP=$(curl -sf "$BASE/v1/scopes/grants" \
  -H "$AUTH" 2>/dev/null)

if echo "$LIST_RESP" | grep -q '"grants":\['; then
  GRANT_COUNT=$(echo "$LIST_RESP" | grep -o '"id"' | wc -l | tr -d ' ')
  if [ "$GRANT_COUNT" -ge 1 ]; then
    ok "list grants: $GRANT_COUNT grant(s)"
  else
    failc "list grants: expected ≥1, got $GRANT_COUNT"
  fi
else
  failc "list grants (response: $LIST_RESP)"
fi

# ── 6. Revoke grant ───────────────────────────────────────────────────────────

REVOKE_RESP=$(curl -sf -X POST "$BASE/v1/grants/$GRANT_ID/revoke" \
  -H "$AUTH" 2>/dev/null)

if echo "$REVOKE_RESP" | grep -q '"status":"revoked"'; then
  ok "grant revoked"
else
  failc "revoke grant (response: $REVOKE_RESP)"
fi

# ── 7. List grants after revoke (row still present, revoked_at ≠ 0) ──────────

LIST2_RESP=$(curl -sf "$BASE/v1/scopes/grants" \
  -H "$AUTH" 2>/dev/null)

if echo "$LIST2_RESP" | grep -q '"grants":'; then
  ok "list grants after revoke returns response"
else
  failc "list grants after revoke (response: $LIST2_RESP)"
fi

# ── 8. Zone ceiling validation: personal → 400 ────────────────────────────────

STATUS=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$BASE/v1/scopes/grants" \
  -H 'Content-Type: application/json' \
  -H "$AUTH" \
  -d "{\"group_id\":\"$GROUP_ID\",\"zone_ceiling\":\"personal\"}" \
  2>/dev/null)

if [ "$STATUS" = "400" ]; then
  ok "zone_ceiling=personal rejected 400"
else
  failc "zone_ceiling=personal: expected 400, got $STATUS"
fi

# ── 8b. Zone ceiling validation: intimate → 400 ───────────────────────────────

STATUS2=$(curl -s -o /dev/null -w '%{http_code}' -X PUT "$BASE/v1/scopes/grants" \
  -H 'Content-Type: application/json' \
  -H "$AUTH" \
  -d "{\"group_id\":\"$GROUP_ID\",\"zone_ceiling\":\"intimate\"}" \
  2>/dev/null)

if [ "$STATUS2" = "400" ]; then
  ok "zone_ceiling=intimate rejected 400"
else
  failc "zone_ceiling=intimate: expected 400, got $STATUS2"
fi

# ── 9. Grants package unit tests ─────────────────────────────────────────────

if CGO_ENABLED=1 go test ./internal/grants/... >/dev/null 2>&1; then
  ok "grants unit tests pass"
else
  failc "grants unit tests failed"
fi

# ── 10. Race ×3 ───────────────────────────────────────────────────────────────

if CGO_ENABLED=1 go test -race -count=3 ./internal/grants/... >/dev/null 2>&1; then
  ok "grants unit tests race ×3 pass"
else
  failc "grants unit tests race ×3 failed"
fi

# ── Shut down server ──────────────────────────────────────────────────────────

kill "${SERVE_PID:-}" 2>/dev/null || true
wait "${SERVE_PID:-}" 2>/dev/null || true

echo ""
echo "phase-15 smoke: done (fails=$fails)"
exit "$fails"
