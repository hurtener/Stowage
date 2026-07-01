#!/usr/bin/env bash
# Phase ae10 smoke: layer/intent read-shaping argument (DEFERRED — own-or-drop, M2).
# This phase is deferred: it either OWNS layer/intent as an additive read-time
# retrieval-OUTPUT-SHAPING arg threaded through ae3's parameterized renderer (with
# all-tier parity + a knob under D-034), OR drops it and amends the charter identity
# principle in the same PR. Until an owner promotes or drops it, every check SKIPs.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# --- checks ---------------------------------------------------------------
# Deferred stub: no surface is built. When promoted (OWN path), replace with
# checks that layer/intent is present + omitempty in all three retrieve contracts,
# threaded through internal/retrieval, with parity + any D-034 knob registered; or
# (DROP path) assert this stub and the charter promise are removed together.
skip "ae10 deferred (M2 own-or-drop): layer/intent read-shaping not built"

exit "$fails"
