#!/usr/bin/env bash
# Phase a2 smoke: per-learner-stage model selection (D-132). Each learner stage
# (extract / reconcile / reflect) can pin its own completion model; empty falls
# back to gateway.model.
#
# Verifies:
#   AC-1  config explain surfaces the three keys, defaulting empty (→ gateway.model).
#   AC-2  STOWAGE_GATEWAY_EXTRACT_MODEL overrides gateway.extract_model via env.
#   AC-3  the per-stage model reaches the Complete call at each stage (unit gate).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Pre-build SKIP-graceful guard (CLAUDE.md §4.2).
if ! grep -q 'ExtractModel' internal/config/config.go 2>/dev/null; then
  skip "AC-1: gateway.{extract,reconcile,reflect}_model keys (pending a2)"
  skip "AC-2: per-stage model env override (pending a2)"
  skip "AC-3: per-stage model reaches Complete (pending a2)"
  exit "$fails"
fi

BIN=/tmp/stowage-smoke-a2
trap 'rm -f "$BIN"' EXIT
CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "CGo-free binary build" \
  || { failc "CGo-free binary build"; exit "$fails"; }

# ── AC-1: keys surfaced, default empty ─────────────────────────────────────────
EXPLAIN=$(env -u STOWAGE_GATEWAY_EXTRACT_MODEL -u STOWAGE_GATEWAY_RECONCILE_MODEL -u STOWAGE_GATEWAY_REFLECT_MODEL "$BIN" config explain 2>/dev/null || true)
for k in gateway.extract_model gateway.reconcile_model gateway.reflect_model; do
  if printf '%s' "$EXPLAIN" | grep -Eq "$k[[:space:]]*=[[:space:]]*\["; then
    ok "AC-1: $k surfaced, defaults empty (→ gateway.model)"
  else
    failc "AC-1: $k not surfaced empty"
  fi
done

# ── AC-2: env override reaches the effective value ─────────────────────────────
EXPLAIN2=$(STOWAGE_GATEWAY_EXTRACT_MODEL="openai/gpt-5.4-mini" "$BIN" config explain 2>/dev/null || true)
if printf '%s' "$EXPLAIN2" | grep -Eq 'gateway.extract_model[[:space:]]*=[[:space:]]*openai/gpt-5.4-mini[[:space:]]+\[env\]'; then
  ok "AC-2: STOWAGE_GATEWAY_EXTRACT_MODEL overrides gateway.extract_model"
else
  failc "AC-2: extract_model env override not reflected"
fi

# ── AC-3: the model reaches the Complete call at each stage ────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=180s \
    -run 'TestExtract_ModelWiring|TestStageModelWiring|TestReflect_ModelWiring' \
    ./internal/pipeline/ ./internal/reconcile/ ./internal/reflect/ >/tmp/a2-wiring.log 2>&1; then
  ok "AC-3: per-stage model reaches Complete (extract + reconcile + reflect)"
else
  failc "AC-3: per-stage model-wiring tests failed"
  cat /tmp/a2-wiring.log >&2
fi

exit "$fails"
