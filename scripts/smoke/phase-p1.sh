#!/usr/bin/env bash
# Phase P1 smoke — profiling & leak-detection harness (CLAUDE.md §4.2).
# Contract: "OK <check>" per pass, "FAIL <check>" per fail, "SKIP <check>" where
# the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails+1)); }
skip() { echo "SKIP $*"; }

# --- build -------------------------------------------------------------------

BIN=/tmp/stowage-smoke-p1
TMPDIR_SMOKE=$(mktemp -d)

# Kill background servers and clean up on exit.
SERVER_PID=""
SERVER2_PID=""
cleanup() {
  [ -n "$SERVER_PID" ]  && kill "$SERVER_PID"  2>/dev/null || true
  [ -n "$SERVER2_PID" ] && kill "$SERVER2_PID" 2>/dev/null || true
  rm -f "$BIN"
  rm -rf "$TMPDIR_SMOKE"
}
trap cleanup EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  || { failc "binary build failed — cannot run live checks"; exit "$fails"; }

# --- checks ------------------------------------------------------------------

# 1. pprof disabled by default (empty server.pprof_listen ⇒ no /debug/pprof).
#    Start a server WITHOUT server.pprof_listen, then confirm curl to PPORT
#    fails with a connection error (exit code 7).

PORT=$(( 50000 + RANDOM % 5000 ))
PPORT=$(( PORT + 1 ))
DB1="${TMPDIR_SMOKE}/smoke-p1-a.db"
CFG1="${TMPDIR_SMOKE}/stowage-p1-a.yaml"
cat > "$CFG1" <<YAML
server:
  listen: "127.0.0.1:${PORT}"
store:
  driver: sqlite
  dsn: "${DB1}"
gateway:
  driver: mock
YAML

"$BIN" serve --config "$CFG1" >"${TMPDIR_SMOKE}/serve-a.log" 2>&1 &
SERVER_PID=$!

# Wait for the server to be ready (up to 10 s).
for i in $(seq 1 20); do
  if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    failc "pprof disabled by default (server-a did not start)"
    cat "${TMPDIR_SMOKE}/serve-a.log"
    exit "$fails"
  fi
done

# Curl the PPORT — must fail with connection refused (exit 7).
curl --max-time 2 -sf "http://127.0.0.1:${PPORT}/debug/pprof/" >/dev/null 2>&1
CURL_EXIT=$?
if [ "$CURL_EXIT" -eq 7 ]; then
  ok "pprof disabled by default"
else
  failc "pprof disabled by default (curl exited $CURL_EXIT, expected 7 connection-refused)"
fi

kill "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true
SERVER_PID=""

# 2. pprof requires an admin key when enabled (401 without auth, 401 bad key,
#    200 with valid admin key).  Start a second server WITH server.pprof_listen.

PORT2=$(( 55000 + RANDOM % 5000 ))
PPORT2=$(( PORT2 + 1 ))
# Ensure ports differ from the first server (extremely unlikely to collide but be safe).
while [ "$PORT2" -eq "$PORT" ] || [ "$PPORT2" -eq "$PORT" ] || [ "$PPORT2" -eq "$PPORT" ]; do
  PORT2=$(( 55000 + RANDOM % 5000 ))
  PPORT2=$(( PORT2 + 1 ))
done
DB2="${TMPDIR_SMOKE}/smoke-p1-b.db"
CFG2="${TMPDIR_SMOKE}/stowage-p1-b.yaml"
cat > "$CFG2" <<YAML
server:
  listen: "127.0.0.1:${PORT2}"
  pprof_listen: "127.0.0.1:${PPORT2}"
store:
  driver: sqlite
  dsn: "${DB2}"
gateway:
  driver: mock
YAML

"$BIN" serve --config "$CFG2" >"${TMPDIR_SMOKE}/serve-b.log" 2>&1 &
SERVER2_PID=$!

# Wait for the API server to be ready.
for i in $(seq 1 20); do
  if curl -sf "http://127.0.0.1:${PORT2}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    failc "pprof requires admin key (server-b did not start)"
    cat "${TMPDIR_SMOKE}/serve-b.log"
    exit "$fails"
  fi
done

# Bootstrap an admin key (keyring is empty — no auth required).
RESP_FILE="${TMPDIR_SMOKE}/bootstrap.json"
STATUS=$(curl -s -X POST "http://127.0.0.1:${PORT2}/v1/admin/keys" \
  -H "Content-Type: application/json" \
  -d '{"tenant_id":"smoke-p1","role":"admin"}' \
  -o "$RESP_FILE" -w '%{http_code}' 2>/dev/null)
if [ "$STATUS" != "201" ]; then
  failc "pprof requires admin key (bootstrap failed: $STATUS)"
  cat "$RESP_FILE"
  exit "$fails"
fi
ADMIN_KEY=$(grep -o '"plaintext":"[^"]*"' "$RESP_FILE" | sed 's/.*":"\(.*\)"/\1/' | head -1)
if [ -z "$ADMIN_KEY" ]; then
  failc "pprof requires admin key (no plaintext in bootstrap response)"
  exit "$fails"
fi

# Wait for pprof listener (up to 5 s).
for i in $(seq 1 10); do
  if curl --max-time 1 -sf "http://127.0.0.1:${PPORT2}/debug/pprof/" \
      -H "Authorization: Bearer $ADMIN_KEY" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

# 2a. No Authorization header → 401.
S=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
  "http://127.0.0.1:${PPORT2}/debug/pprof/" 2>/dev/null)
[ "$S" = "401" ] \
  && ok "pprof no-auth → 401" \
  || failc "pprof no-auth → 401 (got $S)"

# 2b. Bogus Bearer token → 401.
S=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
  -H "Authorization: Bearer sk_bogus" \
  "http://127.0.0.1:${PPORT2}/debug/pprof/" 2>/dev/null)
[ "$S" = "401" ] \
  && ok "pprof bad-key → 401" \
  || failc "pprof bad-key → 401 (got $S)"

# 2c. Valid admin key → 200.
S=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
  -H "Authorization: Bearer $ADMIN_KEY" \
  "http://127.0.0.1:${PPORT2}/debug/pprof/" 2>/dev/null)
[ "$S" = "200" ] \
  && ok "pprof requires admin key" \
  || failc "pprof requires admin key (admin key got $S, want 200)"

kill "$SERVER2_PID" 2>/dev/null || true
wait "$SERVER2_PID" 2>/dev/null || true
SERVER2_PID=""

# 3. runtime sampler emits periodic sample + go runtime gauges on /metrics.
#    Start a server with runtime_sample_interval=1 (1 s), wait for /healthz,
#    sleep 2 s, assert the serve log contains a "runtime.sample" line, then
#    curl /metrics and assert it contains go_goroutines from the GoCollector.

PORT3=$(( 58000 + RANDOM % 2000 ))
while [ "$PORT3" -eq "$PORT" ] || [ "$PORT3" -eq "$PORT2" ] \
   || [ "$PORT3" -eq "$PPORT" ] || [ "$PORT3" -eq "$PPORT2" ]; do
  PORT3=$(( 58000 + RANDOM % 2000 ))
done
DB3="${TMPDIR_SMOKE}/smoke-p1-c.db"
CFG3="${TMPDIR_SMOKE}/stowage-p1-c.yaml"
cat > "$CFG3" <<YAML
server:
  listen: "127.0.0.1:${PORT3}"
store:
  driver: sqlite
  dsn: "${DB3}"
gateway:
  driver: mock
telemetry:
  runtime_sample_interval: 1
YAML

"$BIN" serve --config "$CFG3" >"${TMPDIR_SMOKE}/serve-c.log" 2>&1 &
SERVER_PID=$!

# Wait for server ready (up to 10 s).
for i in $(seq 1 20); do
  if curl -sf "http://127.0.0.1:${PORT3}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    failc "runtime sampler emits periodic sample (server-c did not start)"
    cat "${TMPDIR_SMOKE}/serve-c.log"
    kill "$SERVER_PID" 2>/dev/null || true
    SERVER_PID=""
    # skip remaining runtime-sampler sub-checks and continue
  fi
done

if [ -n "$SERVER_PID" ]; then
  # Let at least two ticks fire (interval=1 s, wait 2.5 s with headroom).
  sleep 2.5

  if grep -q "runtime.sample" "${TMPDIR_SMOKE}/serve-c.log"; then
    ok "runtime sampler emits periodic sample"
  else
    failc "runtime sampler emits periodic sample (no runtime.sample in log)"
    cat "${TMPDIR_SMOKE}/serve-c.log"
  fi

  # /metrics is served on the main server port.
  METRICS=$(curl -sf --max-time 5 "http://127.0.0.1:${PORT3}/metrics" 2>/dev/null)
  if echo "$METRICS" | grep -q "^go_goroutines"; then
    ok "go runtime gauges exposed on /metrics"
  else
    failc "go runtime gauges exposed on /metrics (go_goroutines not found)"
  fi

  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
fi

# 4. the -tags=profile rig compiles.
if [ -d internal/bench/profile ]; then
  if go test -tags=profile -run '^$' ./internal/bench/profile/ >/dev/null 2>&1; then
    ok "profile rig builds"
  else
    failc "profile rig builds"
  fi
else
  skip "profile rig builds (internal/bench/profile absent)"
fi

# 5. leakcheck wired into at least one goroutine-launching package.
if grep -rqs "leakcheck.Run" internal/; then
  ok "leakcheck wired (leakcheck.Run present)"
else
  skip "leakcheck wired (no leakcheck.Run yet)"
fi

# 6. PROFILE.md baseline present.
if [ -f eval/PROFILE.md ]; then
  ok "PROFILE.md baseline present"
else
  skip "PROFILE.md baseline present (eval/PROFILE.md absent)"
fi

# 7. postgres backend-under-load cut requires a DSN.
if [ -n "${STOWAGE_TEST_PG_DSN:-}" ]; then
  skip "postgres profile cut (rig not built yet)"
else
  skip "postgres profile cut (STOWAGE_TEST_PG_DSN unset)"
fi

exit "$fails"
