#!/usr/bin/env bash
# Phase 02 smoke — config, identity, telemetry, keys, profiles.
# Contract: print "OK <check>" / "FAIL <check>" / "SKIP <check>".
# Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails + 1)); }
skip() { echo "SKIP $*"; }

BIN=./bin/stowage

# --- checks -------------------------------------------------------------------

# 1. config explain works with zero config file (env-only).
if [ -x "$BIN" ]; then
    if "$BIN" config explain >/dev/null 2>&1; then
        ok "config explain env-only"
    else
        failc "config explain env-only"
    fi
else
    skip "config explain env-only (binary not built)"
fi

# 2. Unknown config sub-subcommand exits 2.
if [ -x "$BIN" ]; then
    "$BIN" config bogus-sub >/dev/null 2>&1
    code=$?
    if [ "$code" -eq 2 ]; then
        ok "config unknown sub-subcommand exits 2"
    else
        failc "config unknown sub-subcommand exits 2 (got exit $code)"
    fi
else
    skip "config unknown sub-subcommand exits 2 (binary not built)"
fi

# 3. Invalid config file → exits non-zero with a key-path message.
if [ -x "$BIN" ]; then
    tmp=$(mktemp)
    printf 'gateway:\n  api_key: literal-value\n' > "$tmp"
    errmsg=$("$BIN" config explain --config "$tmp" 2>&1 || true)
    rm -f "$tmp"
    if echo "$errmsg" | grep -q "config.gateway.api_key"; then
        ok "invalid config key-path error"
    else
        failc "invalid config key-path error (got: $errmsg)"
    fi
else
    skip "invalid config key-path error (binary not built)"
fi

# 4. config explain output does not contain raw secret values.
if [ -x "$BIN" ]; then
    output=$(STOWAGE_GATEWAY_API_KEY="super-secret-xyz" "$BIN" config explain 2>/dev/null || true)
    if echo "$output" | grep -q "super-secret-xyz"; then
        failc "config explain must not print secret value"
    else
        ok "config explain secrets redacted"
    fi
else
    skip "config explain secrets redacted (binary not built)"
fi

# 5. coverage-check.sh exists and is executable.
if [ -x "scripts/coverage-check.sh" ]; then
    ok "coverage-check.sh exists and is executable"
else
    failc "coverage-check.sh exists and is executable"
fi

# 6. coverage.json exists.
if [ -f "scripts/coverage.json" ]; then
    ok "coverage.json exists"
else
    failc "coverage.json exists"
fi

exit "$fails"
