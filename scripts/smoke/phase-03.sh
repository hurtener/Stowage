#!/usr/bin/env bash
# Smoke test for Phase 03: store seam + day-one schema.
# Exit code == number of failures.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }

BIN=/tmp/stowage-smoke-03
DB=$(mktemp /tmp/stowage-phase03.XXXXXX)  # X's at end for macOS mktemp compatibility
trap 'rm -f "$DB" "$BIN"' EXIT

# ── Build ────────────────────────────────────────────────────────────────────

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" \
  || { failc "cgo-free build"; exit "$fails"; }

# ── migrate --status (before applying) ──────────────────────────────────────
# Capture to variable then grep — avoids SIGPIPE when grep -q exits early
# (which with pipefail would report the binary's 141 exit, not grep's 0).

status_out=$("$BIN" migrate --dsn "$DB" --status 2>&1) \
  && echo "$status_out" | grep -qE "pending|0001|migrations" \
  && ok "migrate --status works" \
  || failc "migrate --status works"

# ── migrate (apply) ─────────────────────────────────────────────────────────

"$BIN" migrate --dsn "$DB" 2>&1 \
  && ok "migrate applies" \
  || failc "migrate applies"

# ── migrate (idempotent re-run) ──────────────────────────────────────────────

"$BIN" migrate --dsn "$DB" 2>&1 \
  && ok "migrate idempotent" \
  || failc "migrate idempotent"

# ── unknown flag exits 2 ────────────────────────────────────────────────────

"$BIN" migrate --unknown-flag 2>/dev/null; code=$?
[[ "$code" -eq 2 ]] \
  && ok "unknown flag exits 2" \
  || failc "unknown flag exits 2 (got $code)"

exit "$fails"
