#!/usr/bin/env bash
# Smoke test for Phase 04: gateway seam + bifrost driver.
# Hermetic: runs mock-driver paths via go test; live checks SKIP without env key.
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

BIN=/tmp/stowage-smoke-04
trap 'rm -f "$BIN"' EXIT

# ── Build ────────────────────────────────────────────────────────────────────

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── version still works ──────────────────────────────────────────────────────

"$BIN" version >/dev/null 2>&1 \
  && ok "version prints" \
  || failc "version prints"

# ── gateway unit tests (mock driver path) ────────────────────────────────────

CGO_ENABLED=1 go test -race -timeout 60s -count=1 \
  ./internal/gateway/... 2>/dev/null \
  && ok "gateway unit tests (mock driver)" \
  || failc "gateway unit tests (mock driver)"

# ── openaicompat hermetic tests ──────────────────────────────────────────────

CGO_ENABLED=1 go test -race -timeout 60s -count=1 \
  ./internal/gateway/openaicompat/ 2>/dev/null \
  && ok "openaicompat hermetic tests" \
  || failc "openaicompat hermetic tests"

# ── live check: SKIP without env vars ────────────────────────────────────────

if [[ -z "${STOWAGE_TEST_OPENROUTER_KEY:-}" ]] || [[ -z "${STOWAGE_TEST_OPENROUTER_MODEL:-}" ]]; then
  skip "live OpenRouter test (STOWAGE_TEST_OPENROUTER_KEY/MODEL not set)"
else
  CGO_ENABLED=1 go test -race -timeout 120s -tags=live -count=1 \
    -run TestLive ./internal/gateway/openaicompat/ 2>/dev/null \
    && ok "live OpenRouter test" \
    || failc "live OpenRouter test"
fi

exit "$fails"
