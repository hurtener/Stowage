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

# --- checks ---------------------------------------------------------------
# Surfaces land with the phase; until then every check SKIPs gracefully.

# 1. pprof disabled by default (empty server.pprof_listen ⇒ no /debug/pprof).
skip "pprof disabled by default"

# 2. pprof requires an admin key when enabled (401 without, 200 with).
skip "pprof requires admin key"

# 3. runtime gauges registered on the metrics endpoint when sampler on.
skip "runtime gauges registered (stowage_goroutines)"

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

# 5. goleak wired into at least one goroutine-launching package.
if grep -rqs "goleak.VerifyTestMain" internal/; then
  ok "goleak wired (VerifyTestMain present)"
else
  skip "goleak wired (no VerifyTestMain yet)"
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
