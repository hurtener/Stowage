#!/usr/bin/env bash
# Smoke script template (CLAUDE.md §4.2). Copy to phase-NN.sh.
# Contract: print "OK <check>" per passing check, "FAIL <check>" per failing,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails+1)); }
skip() { echo "SKIP $*"; }

# --- checks ---------------------------------------------------------------
# example:
# ./bin/stowage version >/dev/null && ok "version prints" || failc "version prints"

exit "$fails"
