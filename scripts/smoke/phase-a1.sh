#!/usr/bin/env bash
# Phase a1 smoke: gateway defaults flip to the real Bifrost/OpenRouter stack so
# `stowage serve` + one secret is a working server (the five-minute rule, D-131).
#
# Verifies:
#   AC-1  config explain shows the real default driver/provider (bifrost/openrouter).
#   AC-2  config explain shows the live-validated full-stack model/embed/rerank ids.
#   AC-3  a real driver with no key fails loud at boot, naming STOWAGE_GATEWAY_API_KEY.
#   AC-4  the `mock` driver remains a keyless escape hatch (serve boots).
#   AC-5  the config package validates the new defaults (unit gate).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Pre-build SKIP-graceful guard (CLAUDE.md §4.2): default flip not yet landed.
if ! grep -q 'Driver:        "bifrost"' internal/config/config.go 2>/dev/null; then
  skip "AC-1: gateway default driver=bifrost (pending a1)"
  skip "AC-2: gateway default model/embed/rerank ids (pending a1)"
  skip "AC-3: keyless real driver fails loud (pending a1)"
  skip "AC-4: mock keyless escape hatch (pending a1)"
  skip "AC-5: config validates new defaults (pending a1)"
  exit "$fails"
fi

BIN=/tmp/stowage-smoke-a1
TMPDIR_SMOKE=$(mktemp -d)
trap 'kill "${SRV_PID:-}" 2>/dev/null; rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "CGo-free binary build" \
  || { failc "CGo-free binary build"; exit "$fails"; }

# ── AC-1: real default driver + provider ───────────────────────────────────────
EXPLAIN=$(env -u STOWAGE_GATEWAY_DRIVER -u STOWAGE_GATEWAY_PROVIDER "$BIN" config explain 2>/dev/null || true)
if printf '%s' "$EXPLAIN" | grep -Eq 'gateway.driver[[:space:]]*=[[:space:]]*bifrost[[:space:]]+\[default\]'; then
  ok "AC-1: gateway.driver defaults to bifrost"
else
  failc "AC-1: gateway.driver default is not bifrost"
fi
if printf '%s' "$EXPLAIN" | grep -Eq 'gateway.provider[[:space:]]*=[[:space:]]*openrouter[[:space:]]+\[default\]'; then
  ok "AC-1: gateway.provider defaults to openrouter"
else
  failc "AC-1: gateway.provider default is not openrouter"
fi

# ── AC-2: live-validated full-stack ids ────────────────────────────────────────
check_default() { # key substr
  if printf '%s' "$EXPLAIN" | grep -Eq "$1[[:space:]]*=[[:space:]]*$2"; then
    ok "AC-2: $1 = $2"
  else
    failc "AC-2: $1 default missing $2"
  fi
}
check_default 'gateway.model'       'openai/gpt-5.4-nano'
check_default 'gateway.embed_model' 'perplexity/pplx-embed-v1-0.6b'
check_default 'gateway.embed_dims'  '1024'
# base_url / rerank_base_url default EMPTY on purpose (the bifrost driver supplies
# OpenRouter's …/api and …/api/v1 when empty, D-131) — assert the value is blank.
if printf '%s' "$EXPLAIN" | grep -Eq 'gateway.base_url[[:space:]]*=[[:space:]]*\['; then
  ok "AC-2: gateway.base_url defaults empty (driver supplies OpenRouter's)"
else
  failc "AC-2: gateway.base_url default is not empty"
fi

# ── AC-3: keyless real driver fails loud, naming the secret ────────────────────
KEYLESS_OUT=$(env -u STOWAGE_GATEWAY_API_KEY -u OPENROUTER_API_KEY -u STOWAGE_GATEWAY_DRIVER \
  STOWAGE_STORE_DSN="${TMPDIR_SMOKE}/a1.db" "$BIN" serve 2>&1)
KEYLESS_RC=$?
if [ "$KEYLESS_RC" -ne 0 ] && printf '%s' "$KEYLESS_OUT" | grep -q 'STOWAGE_GATEWAY_API_KEY'; then
  ok "AC-3: real driver with no key fails loud naming STOWAGE_GATEWAY_API_KEY"
else
  failc "AC-3: keyless real driver did not fail loud (rc=$KEYLESS_RC)"
  printf '%s\n' "$KEYLESS_OUT" >&2
fi

# ── AC-4: mock is a keyless escape hatch ───────────────────────────────────────
env -u STOWAGE_GATEWAY_API_KEY STOWAGE_GATEWAY_DRIVER=mock \
  STOWAGE_STORE_DSN="${TMPDIR_SMOKE}/a1mock.db" STOWAGE_SERVER_LISTEN=":17181" \
  STOWAGE_TELEMETRY_METRICS_LISTEN=":17182" \
  "$BIN" serve >"${TMPDIR_SMOKE}/mock.log" 2>&1 &
SRV_PID=$!
sleep 2
if kill -0 "$SRV_PID" 2>/dev/null; then
  ok "AC-4: mock driver boots serve with no key (escape hatch)"
  kill "$SRV_PID" 2>/dev/null
else
  failc "AC-4: mock driver did not boot keyless"
  cat "${TMPDIR_SMOKE}/mock.log" >&2
fi

# ── AC-5: config validates the new defaults ────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=120s \
    -run 'TestExplainGolden|TestRerankBaseURLDefaultEmpty|TestGatewayDriverBifrost' ./internal/config/ \
    >"${TMPDIR_SMOKE}/cfg.log" 2>&1; then
  ok "AC-5: config defaults validate (golden + rerank + bifrost-provider)"
else
  failc "AC-5: config default validation tests failed"
  cat "${TMPDIR_SMOKE}/cfg.log" >&2
fi

exit "$fails"
