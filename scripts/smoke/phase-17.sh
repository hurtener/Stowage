#!/usr/bin/env bash
# Phase 17 smoke test: SDKs + zero-config agent wiring.
#
# Verifies all seven acceptance criteria:
#   AC-1 same-suite parity: shared test suite passes against both HTTP and
#        embedded constructors.
#   AC-2 embedded example builds CGO_ENABLED=0 and runs offline (degraded OK).
#   AC-3 Harbor adapter compiles against Harbor v1.3.1; tools register +
#        round-trip with identity lifted correctly.
#   AC-4 WireOutcomes: synthetic task.completed → outcome ingest (fake-bus test).
#   AC-5 Python client smoke green against live serve (ingest→retrieve→feedback).
#   AC-6 Core go.mod is unchanged by Harbor (adapter is the only go.mod with it).
#   AC-7 eval-ci green; coverage thresholds; smokes 01–17 (coverage+eval checked
#        by the build-test CI job; race run optional here due to time).
#
# Mirrors the other phase smoke scripts: CGo-free build, temp DB + config,
# background server killed after session.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-17
TMPDIR_SMOKE=$(mktemp -d)
trap 'rm -f "$BIN"; rm -f /tmp/stowage-embedded-17; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "CGo-free binary build" \
  || { failc "CGo-free binary build"; exit "$fails"; }

# ── AC-1: same-suite parity ───────────────────────────────────────────────────

if CGO_ENABLED=1 go test -count=1 -timeout=120s ./sdk/stowage/... 2>/tmp/sdk-test-17.log; then
  ok "AC-1: sdk/stowage test suite passes (HTTP + embedded constructors)"
else
  failc "AC-1: sdk/stowage test suite failed"
  cat /tmp/sdk-test-17.log >&2
fi

# ── AC-2: embedded example builds CGo-free and runs offline ──────────────────

EMBEDDED_BIN=/tmp/stowage-embedded-17
if CGO_ENABLED=0 go build -o "$EMBEDDED_BIN" ./examples/embedded 2>/tmp/embedded-build-17.log; then
  ok "AC-2: examples/embedded CGo-free build"
else
  failc "AC-2: examples/embedded CGo-free build failed"
  cat /tmp/embedded-build-17.log >&2
fi

if [ -x "$EMBEDDED_BIN" ]; then
  if "$EMBEDDED_BIN" >/tmp/embedded-run-17.log 2>&1; then
    ok "AC-2: embedded example ran offline (output follows)"
    head -5 /tmp/embedded-run-17.log | sed 's/^/       /'
  else
    failc "AC-2: embedded example exited non-zero"
    cat /tmp/embedded-run-17.log >&2
  fi
fi

# ── AC-3 + AC-4: Harbor adapter compiles and tests pass ──────────────────────

HARBOR_DIR="adapters/harbor"
if [ -d "$HARBOR_DIR" ]; then
  if (cd "$HARBOR_DIR" && go build ./... 2>/tmp/harbor-build-17.log); then
    ok "AC-3: adapters/harbor builds against Harbor v1.3.1"
  else
    failc "AC-3: adapters/harbor build failed"
    cat /tmp/harbor-build-17.log >&2
  fi

  if (cd "$HARBOR_DIR" && CGO_ENABLED=1 go test -count=1 -timeout=60s ./... 2>/tmp/harbor-test-17.log); then
    ok "AC-3+AC-4: adapters/harbor tests pass (tools registration + WireOutcomes)"
  else
    failc "AC-3+AC-4: adapters/harbor tests failed"
    cat /tmp/harbor-test-17.log >&2
  fi
else
  failc "AC-3: adapters/harbor directory not found"
fi

# ── AC-5: Python client smoke against live server ────────────────────────────

if ! command -v python3 &>/dev/null; then
  skip "AC-5: python3 not found — skipping Python smoke"
else
  # Start server with a temp config.
  SMOKE_PORT=17150
  DB_PATH="${TMPDIR_SMOKE}/smoke17.db"
  CFG_PATH="${TMPDIR_SMOKE}/stowage17.yaml"
  cat > "$CFG_PATH" <<YAML
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
server:
  listen: ":${SMOKE_PORT}"
YAML

  "$BIN" migrate --config "$CFG_PATH" >/dev/null 2>&1 \
    && ok "AC-5: migrate applied" \
    || { failc "AC-5: migrate failed"; exit "$fails"; }

  "$BIN" serve --config "$CFG_PATH" >"${TMPDIR_SMOKE}/serve17.log" 2>&1 &
  SRV_PID=$!

  # Wait for server to start (up to 5 s).
  SERVER_URL="http://127.0.0.1:${SMOKE_PORT}"
  READY=0
  for i in $(seq 1 50); do
    sleep 0.1
    if curl -sf "${SERVER_URL}/healthz" >/dev/null 2>&1; then
      READY=1; break
    fi
  done

  if [ "$READY" -eq 0 ]; then
    failc "AC-5: server did not become ready within 5 s"
    kill "$SRV_PID" 2>/dev/null; wait "$SRV_PID" 2>/dev/null
  else
    ok "AC-5: server ready at ${SERVER_URL}"

    # Bootstrap: create first API key (no auth needed when keyring is empty).
    KEY_RESP=$(curl -sf -X POST "${SERVER_URL}/v1/admin/keys" \
      -H "Content-Type: application/json" \
      -d '{"tenant_id":"smoke17-tenant","role":"admin"}' 2>/dev/null || true)

    API_KEY=$(echo "$KEY_RESP" | python3 -c \
      "import json,sys; d=json.load(sys.stdin); print(d.get('plaintext',''))" 2>/dev/null || true)

    if [ -z "$API_KEY" ]; then
      failc "AC-5: could not bootstrap API key (response: ${KEY_RESP})"
    else
      ok "AC-5: API key bootstrapped"

      # Run the Python smoke script.
      if python3 clients/python/smoke.py "$SERVER_URL" "$API_KEY" \
           >/tmp/pysmoke-17.log 2>&1; then
        ok "AC-5: Python client smoke passed"
        cat /tmp/pysmoke-17.log | sed 's/^/       /'
      else
        failc "AC-5: Python client smoke FAILED"
        cat /tmp/pysmoke-17.log >&2
      fi
    fi

    kill "$SRV_PID" 2>/dev/null
    wait "$SRV_PID" 2>/dev/null
  fi
fi

# ── AC-6: Core go.mod does not mention Harbor ─────────────────────────────────

if grep -qi "harbor" go.mod 2>/dev/null; then
  failc "AC-6: core go.mod contains a Harbor reference (should be adapters/harbor only)"
else
  ok "AC-6: core go.mod contains no Harbor dependency"
fi

# Verify Harbor IS in the adapter go.mod.
if grep -qi "harbor" adapters/harbor/go.mod 2>/dev/null; then
  ok "AC-6: adapters/harbor/go.mod correctly declares Harbor dependency"
else
  failc "AC-6: adapters/harbor/go.mod missing Harbor dependency"
fi

# ── AC-7: gofmt + vet ────────────────────────────────────────────────────────

FMT_ISSUES=$(gofmt -l ./sdk/stowage/ ./examples/embedded/ ./internal/boot/ 2>/dev/null | grep -v '^$' || true)
if [ -z "$FMT_ISSUES" ]; then
  ok "AC-7: gofmt clean on new packages"
else
  failc "AC-7: gofmt issues in: ${FMT_ISSUES}"
fi

if go vet ./sdk/stowage/... ./examples/embedded/... ./internal/boot/... 2>/tmp/vet-17.log; then
  ok "AC-7: go vet clean"
else
  failc "AC-7: go vet failures"
  cat /tmp/vet-17.log >&2
fi

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
if [ "$fails" -eq 0 ]; then
  echo "phase-17 smoke: ALL CHECKS PASSED"
else
  echo "phase-17 smoke: $fails check(s) FAILED" >&2
fi
exit "$fails"
