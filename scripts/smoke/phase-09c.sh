#!/usr/bin/env bash
# Smoke test for Phase 09c: gateway SDK remediation.
#   1. cgo-free build still green (bifrost/core is pure Go).
#   2. mock driver boots normally — unchanged from previous phases.
#   3. driver=bifrost without gateway.provider inherits the accepted OpenRouter
#      default (D-131); an explicitly unknown provider still fails closed.
#   4. driver=bifrost with no API key env var set fails closed at boot
#      (config.gateway.api_key unresolvable).
#   5. driver=openaicompat boots normally (rename behavior-preserving).
#   6. Unit tests pass for both driver packages (mock driver path).
#
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

BIN=/tmp/stowage-smoke-09c
TMPDIR_SMOKE=$(mktemp -d)
SERVER_PIDS=()

cleanup() {
  local pid
  for pid in "${SERVER_PIDS[@]}"; do
    if kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
      for _ in $(seq 1 10); do
        if ! kill -0 "$pid" 2>/dev/null; then break; fi
        sleep 0.1
      done
      if kill -0 "$pid" 2>/dev/null; then
        kill -KILL "$pid" 2>/dev/null || true
      fi
    fi
    wait "$pid" 2>/dev/null || true
  done
  rm -f "$BIN"
  rm -rf "$TMPDIR_SMOKE"
}
trap cleanup EXIT

track_pid() {
  SERVER_PIDS+=("$1")
}

stop_server() {
  local name=$1 pid=$2

  kill -TERM "$pid" 2>/dev/null || true
  for _ in $(seq 1 10); do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" 2>/dev/null || true
      ok "$name shutdown cleanly"
      return
    fi
    sleep 0.5
  done

  failc "$name did not stop within 5 s; forcing termination"
  kill -KILL "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

expect_boot_failure() {
  local name=$1 cfg=$2 pattern=$3 log=$4 pid rc=0 exited=0

  "$BIN" serve --config "$cfg" >"$log" 2>&1 &
  pid=$!
  track_pid "$pid"
  for _ in $(seq 1 20); do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" || rc=$?
      exited=1
      break
    fi
    sleep 0.25
  done

  if [ "$exited" -eq 0 ]; then
    failc "$name: server remained running instead of failing closed"
    kill -TERM "$pid" 2>/dev/null || true
    for _ in $(seq 1 10); do
      if ! kill -0 "$pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if kill -0 "$pid" 2>/dev/null; then
      kill -KILL "$pid" 2>/dev/null || true
    fi
    wait "$pid" 2>/dev/null || true
    return
  fi

  if [ "$rc" -ne 0 ] && grep -qE "$pattern" "$log"; then
    ok "$name"
  else
    failc "$name: expected non-zero boot error matching $pattern (rc=$rc)"
    cat "$log" >&2
  fi
}

# ── AC-1: cgo-free build ──────────────────────────────────────────────────────

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build (bifrost/core is pure Go)" \
  || { failc "cgo-free build"; exit "$fails"; }

"$BIN" version >/dev/null 2>&1 \
  && ok "version command works" \
  || failc "version command works"

# ── AC-2: mock driver boots normally ─────────────────────────────────────────

PORT=$(( 50000 + RANDOM % 5000 ))
DB_PATH="${TMPDIR_SMOKE}/mock.db"
CFG_MOCK="${TMPDIR_SMOKE}/mock.yaml"
cat > "$CFG_MOCK" <<YAML
server:
  listen: ":${PORT}"
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
  embed_dims: 4
YAML

"$BIN" serve --config "$CFG_MOCK" >"${TMPDIR_SMOKE}/mock.log" 2>&1 &
MOCK_PID=$!
track_pid "$MOCK_PID"

for i in $(seq 1 20); do
  if curl -sf "http://localhost:${PORT}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    failc "mock server did not start in 10 s"
    cat "${TMPDIR_SMOKE}/mock.log"
    kill "$MOCK_PID" 2>/dev/null; wait "$MOCK_PID" 2>/dev/null || true
    exit "$fails"
  fi
done
ok "mock driver boots normally"

stop_server "mock server" "$MOCK_PID"

# ── AC-3: omitted provider defaults; unknown provider fails closed ────────────
# D-131 amended D-049: an omitted provider inherits the accepted OpenRouter
# default, while an explicitly unknown provider remains a boot error.

PORT2=$(( PORT + 1 ))
DB_PATH2="${TMPDIR_SMOKE}/bifrost-noprovider.db"
CFG_NOPROVIDER="${TMPDIR_SMOKE}/bifrost-default-provider.yaml"
cat > "$CFG_NOPROVIDER" <<YAML
server:
  listen: ":${PORT2}"
store:
  driver: sqlite
  dsn: "${DB_PATH2}"
gateway:
  driver: bifrost
  api_key: env.STOWAGE_TEST_DUMMY_KEY
YAML

# Export a dummy key so api_key resolution doesn't interfere with provider check.
export STOWAGE_TEST_DUMMY_KEY="dummy-for-smoke-test"

EXPLAIN_NOPROVIDER=$("$BIN" config explain --config "$CFG_NOPROVIDER" 2>&1 || true)
if printf '%s' "$EXPLAIN_NOPROVIDER" | grep -Eq \
    'gateway.provider[[:space:]]*=[[:space:]]*openrouter[[:space:]]+\[default\]'; then
  ok "bifrost without provider inherits openrouter default (D-131)"
else
  failc "bifrost without provider did not inherit openrouter default"
  printf '%s\n' "$EXPLAIN_NOPROVIDER" >&2
fi

CFG_BADPROVIDER="${TMPDIR_SMOKE}/bifrost-invalid-provider.yaml"
cat > "$CFG_BADPROVIDER" <<YAML
server:
  listen: ":${PORT2}"
store:
  driver: sqlite
  dsn: "${TMPDIR_SMOKE}/bifrost-invalid-provider.db"
gateway:
  driver: bifrost
  provider: definitely-not-a-provider
  api_key: env.STOWAGE_TEST_DUMMY_KEY
YAML

expect_boot_failure \
  "bifrost with explicitly unknown provider fails loud naming the invalid value" \
  "$CFG_BADPROVIDER" 'invalid provider.*definitely-not-a-provider' \
  "${TMPDIR_SMOKE}/bad-provider.log"

unset STOWAGE_TEST_DUMMY_KEY 2>/dev/null || true

# ── AC-4: driver=bifrost without API key fails closed ────────────────────────
# The env var referenced by api_key must be set; failing to resolve it is a
# boot error (D-030 fail-closed).

PORT3=$(( PORT + 2 ))
DB_PATH3="${TMPDIR_SMOKE}/bifrost-nokey.db"
CFG_NOKEY="${TMPDIR_SMOKE}/bifrost-nokey.yaml"
cat > "$CFG_NOKEY" <<YAML
server:
  listen: ":${PORT3}"
store:
  driver: sqlite
  dsn: "${DB_PATH3}"
gateway:
  driver: bifrost
  provider: openai
  api_key: env.STOWAGE_BIFROST_NONEXISTENT_KEY_09C
YAML

# Ensure the env var is definitely unset.
unset STOWAGE_BIFROST_NONEXISTENT_KEY_09C 2>/dev/null || true

expect_boot_failure \
  "bifrost without API key fails loud naming the missing env var" \
  "$CFG_NOKEY" 'STOWAGE_BIFROST_NONEXISTENT_KEY_09C|api_key|unset' \
  "${TMPDIR_SMOKE}/no-key.log"

# ── AC-5: driver=openaicompat boots normally ──────────────────────────────────
# Renamed from bifrost (Phase 04); registry key changed but behavior unchanged.

PORT4=$(( PORT + 3 ))
DB_PATH4="${TMPDIR_SMOKE}/openaicompat.db"
CFG_OAC="${TMPDIR_SMOKE}/openaicompat.yaml"
cat > "$CFG_OAC" <<YAML
server:
  listen: ":${PORT4}"
store:
  driver: sqlite
  dsn: "${DB_PATH4}"
gateway:
  driver: openaicompat
  base_url: http://127.0.0.1:19999/never-called
  api_key: env.STOWAGE_TEST_DUMMY_OAC_KEY
  embed_dims: 4
YAML

export STOWAGE_TEST_DUMMY_OAC_KEY="dummy-oac-smoke"

"$BIN" serve --config "$CFG_OAC" >"${TMPDIR_SMOKE}/oac.log" 2>&1 &
OAC_PID=$!
track_pid "$OAC_PID"

for i in $(seq 1 20); do
  if curl -sf "http://localhost:${PORT4}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.5
  if [ "$i" -eq 20 ]; then
    failc "openaicompat server did not start in 10 s"
    cat "${TMPDIR_SMOKE}/oac.log"
    kill "$OAC_PID" 2>/dev/null; wait "$OAC_PID" 2>/dev/null || true
    unset STOWAGE_TEST_DUMMY_OAC_KEY 2>/dev/null || true
    exit "$fails"
  fi
done
ok "openaicompat driver boots normally (rename behavior-preserving)"

stop_server "openaicompat server" "$OAC_PID"

unset STOWAGE_TEST_DUMMY_OAC_KEY 2>/dev/null || true

# ── AC-6: unit tests for both driver packages ─────────────────────────────────

CGO_ENABLED=1 go test -race -timeout 60s -count=1 \
  ./internal/gateway/bifrost/ 2>/dev/null \
  && ok "bifrost SDK driver unit tests" \
  || failc "bifrost SDK driver unit tests"

CGO_ENABLED=1 go test -race -timeout 60s -count=1 \
  ./internal/gateway/openaicompat/ 2>/dev/null \
  && ok "openaicompat driver unit tests" \
  || failc "openaicompat driver unit tests"

# ── config explain: gateway.provider appears ──────────────────────────────────

EXPLAIN_OUT=$("$BIN" config explain --config "$CFG_MOCK" 2>&1 || true)
if echo "$EXPLAIN_OUT" | grep -q "gateway.provider"; then
  ok "config explain: gateway.provider key present"
else
  failc "config explain: gateway.provider key missing"
fi

# ── live check: SKIP without env vars ────────────────────────────────────────

if [[ -z "${STOWAGE_TEST_OPENROUTER_KEY:-}" ]] || [[ -z "${STOWAGE_TEST_OPENROUTER_MODEL:-}" ]]; then
  skip "live openaicompat/OpenRouter test (STOWAGE_TEST_OPENROUTER_KEY/MODEL not set)"
else
  CGO_ENABLED=1 go test -race -timeout 120s -tags=live -count=1 \
    -run TestLive ./internal/gateway/openaicompat/ 2>/dev/null \
    && ok "live openaicompat OpenRouter test" \
    || failc "live openaicompat OpenRouter test"
fi

exit "$fails"
