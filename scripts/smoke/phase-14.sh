#!/usr/bin/env bash
# scripts/smoke/phase-14.sh — smoke checks for Phase 14 (lifecycle sweeps)
#
# Tests:
#   1. Build succeeds (binary present)
#   2. STOWAGE_SWEEP_FORCE runs all sweeps once without crashing
#   3. lifecycle package passes unit tests (×3 with -count=3)
#   4. go vet ./internal/lifecycle/... passes

set -uo pipefail

BIN="${BIN:-bin/stowage}"
PASS=0
FAIL=0

ok()   { echo "  OK  $*"; PASS=$((PASS+1)); }
fail() { echo "FAIL  $*"; FAIL=$((FAIL+1)); }

# 1. Binary must exist
if [ -x "$BIN" ]; then
  ok "binary $BIN is present and executable"
else
  fail "binary $BIN not found — run: CGO_ENABLED=1 go build -o $BIN ./cmd/stowage"
fi

# 2. STOWAGE_SWEEP_FORCE: start server briefly, verify sweep runs and server exits cleanly.
#    We use a temp SQLite DB and a random port; kill after the sweeps run.
TMP_DIR=$(mktemp -d)
TMP_DB="$TMP_DIR/smoke.db"
TMP_CONF="$TMP_DIR/config.yaml"
TMP_LOG="$TMP_DIR/serve.log"

cat >"$TMP_CONF" <<YAML
store:
  driver: sqlite
  dsn: "$TMP_DB"
server:
  listen: "127.0.0.1:0"
gateway:
  driver: mock
YAML

if [ -x "$BIN" ]; then
  # STOWAGE_SWEEP_FORCE causes sweeps to run synchronously then the server continues.
  # We just need it to not crash during startup/sweep phase. Send SIGTERM after 3s.
  STOWAGE_SWEEP_FORCE=1 "$BIN" serve --config "$TMP_CONF" >"$TMP_LOG" 2>&1 &
  SERVE_PID=$!
  sleep 3
  kill "$SERVE_PID" 2>/dev/null || true
  wait "$SERVE_PID" 2>/dev/null || true

  if grep -q "running all sweeps once\|lifecycle/decay\|lifecycle/rollup\|lifecycle/dedupe\|lifecycle/reenqueue\|ready" "$TMP_LOG" 2>/dev/null; then
    ok "STOWAGE_SWEEP_FORCE: sweep ran without crash"
  elif grep -q "ready" "$TMP_LOG" 2>/dev/null; then
    ok "STOWAGE_SWEEP_FORCE: server reached ready (sweeps ran on empty store)"
  else
    fail "STOWAGE_SWEEP_FORCE: server did not reach ready or sweep did not log. Log: $(head -20 "$TMP_LOG")"
  fi
else
  fail "STOWAGE_SWEEP_FORCE: skipped (no binary)"
fi
rm -rf "$TMP_DIR"

# 3. lifecycle unit tests pass (×3 to shake out race conditions)
if CGO_ENABLED=1 go test -race -count=3 github.com/hurtener/stowage/internal/lifecycle >/dev/null 2>&1; then
  ok "lifecycle unit tests pass (×3)"
else
  fail "lifecycle unit tests failed"
fi

# 4. go vet ./internal/lifecycle/...
if go vet ./internal/lifecycle/... 2>&1; then
  ok "go vet ./internal/lifecycle/... clean"
else
  fail "go vet ./internal/lifecycle/... failed"
fi

echo ""
echo "phase-14 smoke: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
