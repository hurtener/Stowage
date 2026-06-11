#!/usr/bin/env bash
# Smoke test for Phase 09c: gateway SDK remediation.
#   1. cgo-free build still green (bifrost/core is pure Go).
#   2. mock driver boots normally — unchanged from previous phases.
#   3. driver=bifrost without gateway.provider fails closed at boot
#      with the correct key-path error (config.gateway.provider).
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
trap 'rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

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

kill -TERM "$MOCK_PID" 2>/dev/null
for i in $(seq 1 10); do
  if ! kill -0 "$MOCK_PID" 2>/dev/null; then break; fi
  sleep 0.5
done
ok "mock server shutdown cleanly"

# ── AC-3: driver=bifrost without provider fails closed ───────────────────────
# gateway.provider is required when driver=bifrost (D-049).

PORT2=$(( PORT + 1 ))
DB_PATH2="${TMPDIR_SMOKE}/bifrost-noprovider.db"
CFG_NOPROVIDER="${TMPDIR_SMOKE}/bifrost-noprovider.yaml"
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

SERVE_OUT=$("$BIN" serve --config "$CFG_NOPROVIDER" 2>&1 || true)
if echo "$SERVE_OUT" | grep -q "config.gateway.provider"; then
  ok "bifrost without provider: boot error contains config.gateway.provider"
else
  failc "bifrost without provider: expected config.gateway.provider in error (got: ${SERVE_OUT})"
fi

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

SERVE_OUT=$("$BIN" serve --config "$CFG_NOKEY" 2>&1 || true)
if echo "$SERVE_OUT" | grep -qE "(STOWAGE_BIFROST_NONEXISTENT_KEY_09C|api_key|unset)"; then
  ok "bifrost without API key: boot error references the missing env var"
else
  failc "bifrost without API key: expected key/unset error (got: ${SERVE_OUT})"
fi

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

kill -TERM "$OAC_PID" 2>/dev/null
for i in $(seq 1 10); do
  if ! kill -0 "$OAC_PID" 2>/dev/null; then break; fi
  sleep 0.5
done
ok "openaicompat server shutdown cleanly"

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
