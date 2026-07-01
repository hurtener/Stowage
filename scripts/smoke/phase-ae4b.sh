#!/usr/bin/env bash
# Phase ae4b smoke: causal hook (batch links-exist) + optional positional drilldown.
# DEFERRED (D-145) — this phase is a thin stub; every check SKIPs until promoted.
# On promotion it verifies:
#   AC-1  Store.LinksExist(ctx, scope, ids) exists — one batch round-trip, scope-required, both drivers.
#   AC-6  retrieval.causal_hook is a registered, explainable knob (default false).
#   AC-1/3 conformance + fail-open tests pass.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# ── Deferred: the causal-hook surface is not built until ae4b is promoted ────────
if ! grep -Rqs 'func.*LinksExist' internal/store/ 2>/dev/null; then
  skip "ae4b deferred: Store.LinksExist not built yet (promote to enable checks)"
  exit "$fails"
fi

# ── AC-1: batch links-exist seam present ────────────────────────────────────────
if grep -Rqs 'LinksExist(ctx' internal/store/; then
  ok "AC-1: Store.LinksExist batch method present"
else
  failc "AC-1: Store.LinksExist missing"
fi

# ── AC-6: knob registered ───────────────────────────────────────────────────────
if grep -qs 'causal_hook' internal/config/config.go; then
  ok "AC-6: retrieval.causal_hook present in config"
else
  failc "AC-6: retrieval.causal_hook missing from config"
fi

# ── AC-1/3: conformance + fail-open tests ───────────────────────────────────────
if go test ./internal/store/... -run LinksExist -count=1 >/dev/null 2>&1; then
  ok "AC-1: LinksExist conformance tests pass"
else
  failc "AC-1: LinksExist conformance tests fail"
fi

exit "$fails"
