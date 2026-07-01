#!/usr/bin/env bash
# Phase a1b smoke: per-concern provider/key/base_url for embed + rerank (D-134).
# The embed and rerank lanes can target a DISTINCT provider/credential than the
# primary completion provider; empty inherits the primary (one-key default unchanged).
#
# Verifies:
#   AC-1  config explain surfaces the five keys, default empty; the two *_api_key redact.
#   AC-2  a non-`env.` *_api_key fails Validate.
#   AC-3  the Account fallback + distinct-provider routing unit tests pass.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Pre-build SKIP-graceful guard (CLAUDE.md §4.2).
if ! grep -q 'EmbedProvider' internal/config/config.go 2>/dev/null; then
  skip "AC-1: gateway per-concern keys (pending a1b)"
  skip "AC-2: per-concern *_api_key validation (pending a1b)"
  skip "AC-3: Account per-concern routing (pending a1b)"
  exit "$fails"
fi

BIN=/tmp/stowage-smoke-a1b
TMPDIR_SMOKE=$(mktemp -d)
trap 'rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT
CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "CGo-free binary build" \
  || { failc "CGo-free binary build"; exit "$fails"; }

# ── AC-1: keys surfaced empty ──────────────────────────────────────────────────
EXPLAIN=$("$BIN" config explain 2>/dev/null || true)
for k in gateway.embed_provider gateway.embed_api_key gateway.embed_base_url gateway.rerank_provider gateway.rerank_api_key; do
  if printf '%s' "$EXPLAIN" | grep -Eq "$k[[:space:]]*=[[:space:]]*\["; then
    ok "AC-1: $k surfaced, defaults empty"
  else
    failc "AC-1: $k not surfaced empty"
  fi
done

# ── AC-2: *_api_key validation + secret redaction (config unit gate) ───────────
if CGO_ENABLED=1 go test -count=1 -timeout=120s \
    -run 'TestGatewayPerConcernKeyValidation|TestGatewayPerConcernSecretRedacted' ./internal/config/ \
    >"${TMPDIR_SMOKE}/val.log" 2>&1; then
  ok "AC-2: non-env. embed_api_key/rerank_api_key fails Validate; set value redacts in explain"
else
  failc "AC-2: per-concern *_api_key validation/redaction test failed"
  cat "${TMPDIR_SMOKE}/val.log" >&2
fi

# ── AC-3: Account fallback + distinct-provider routing ─────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=180s \
    -run 'TestAccount_PerConcernEmpty_Fallback|TestAccount_DistinctEmbedProvider|TestAccount_DistinctEmbedInheritsPrimaryKey|TestAccount_RerankAPIKeyOverride|TestAccount_DistinctNativeRerankProvider|TestAccount_UnknownRerankProviderFailsLoud|TestAccount_EmbedRerankSameProviderConflict|TestDriver_EmbedRoutesToEmbedProvider' \
    ./internal/gateway/bifrost/ >"${TMPDIR_SMOKE}/acct.log" 2>&1; then
  ok "AC-3: per-concern routing (fallback, distinct embed, rerank key/provider, unknown-fail-loud, collision, driver route)"
else
  failc "AC-3: per-concern routing tests failed"
  cat "${TMPDIR_SMOKE}/acct.log" >&2
fi

exit "$fails"
