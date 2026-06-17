#!/usr/bin/env bash
# Phase h7 smoke: bifrost auto-wired Cohere-shape rerank over OpenRouter + bench
# rebase — D-075. (Live rerank is the `-tags=live` test, not this smoke.)
#
# AC verified here (unit-level; no live API call):
#   AC-2  a non-native primary + rerank_model auto-wires the custom rerank
#         provider, a native primary (cohere) does NOT; the driver routes Rerank
#         to the right provider (fake-client path).
#   AC-6  gateway.rerank_base_url validates (accept/reject) + appears in
#         `config explain` defaulting empty.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Pre-build SKIP-graceful guard (CLAUDE.md §4.2).
if ! grep -rq 'rerankProvider\|stowage-rerank' internal/gateway/bifrost/ 2>/dev/null \
   || ! grep -rq 'rerank_base_url' internal/config/ 2>/dev/null; then
  skip "AC-2: bifrost custom rerank provider not yet wired (plan skeleton)"
  skip "AC-6: gateway.rerank_base_url knob (pending h7)"
  exit "$fails"
fi

# ── Build ──────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-h7
trap 'rm -f "$BIN"' EXIT
CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "CGo-free binary build" \
  || { failc "CGo-free binary build"; exit "$fails"; }

# ── AC-6: rerank_base_url surfaced in config explain, defaults empty ─────────────

EXPLAIN=$("$BIN" config explain 2>/dev/null || true)
if printf '%s' "$EXPLAIN" | grep -q 'gateway.rerank_base_url'; then
  ok "AC-6: config explain surfaces gateway.rerank_base_url"
else
  failc "AC-6: config explain does not surface gateway.rerank_base_url"
fi
if printf '%s' "$EXPLAIN" | grep -Eq 'gateway.rerank_base_url[[:space:]]*=[[:space:]]*\['; then
  ok "AC-6: gateway.rerank_base_url defaults empty (→ use base_url)"
else
  failc "AC-6: gateway.rerank_base_url default is not empty"
fi

# ── AC-6: validation accepts valid + rejects malformed URLs (unit) ───────────────

if CGO_ENABLED=1 go test -count=1 -timeout=120s -run 'TestRerankBaseURL' ./internal/config/ >/tmp/h7-config.log 2>&1; then
  ok "AC-6: gateway.rerank_base_url validation unit tests pass (empty/valid/bad)"
else
  failc "AC-6: gateway.rerank_base_url validation unit tests failed"
  cat /tmp/h7-config.log >&2
fi

# ── AC-2: account auto-wire gate + driver routing (fake client, no live call) ────

if CGO_ENABLED=1 go test -count=1 -timeout=180s \
     -run 'TestAccount_AutoWiresCustomRerank|TestAccount_NativePrimaryNoCustomRerank|TestAccount_NoRerankModelNoCustomProvider|TestDriver_RerankRoutesTo|TestIsNativeRerankProvider' \
     ./internal/gateway/bifrost/ >/tmp/h7-bifrost.log 2>&1; then
  ok "AC-2: non-native+rerank_model wires stowage-rerank; native does not; driver routes Rerank correctly"
else
  failc "AC-2: bifrost rerank-provider wiring/routing unit tests failed"
  cat /tmp/h7-bifrost.log >&2
fi

exit "$fails"
