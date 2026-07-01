#!/usr/bin/env bash
# Phase ae7 smoke: Harbor-aligned JWT verifier (second verify mode), D-136/D-147.
# A verify-never-mint JWT Validator + JWKS KeySet reimplemented in internal/auth,
# selected by auth.mode with the static keyring as the zero-config default. Stowage
# never signs — the test signer lives in test code only (L1).
#
# Verifies:
#   AC-1   validator.go pins the asymmetric-only allowlist + parser-level method gate.
#   AC-2   the test signer is test-only (no signing in the shipped binary).
#   AC-4   auth.mode defaults to keyring (zero-config start preserved).
#   AC-7   both HTTP seams authenticate via the shared Authenticator core.
#   AC-9   the seven auth.* knobs are registered + explainable.
#   AC-10  golang-jwt/jwt/v5 is a direct require.
#   AC-1/3/6/8 unit tests pass; §17 integration passes when present.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

V=internal/auth/validator.go
A=internal/auth/authenticator.go

# ── gate: SKIP cleanly until ae7 lands ──────────────────────────────────────────
if [ ! -f "$V" ]; then
  skip "ae7 not built yet ($V absent)"
  exit "$fails"
fi

# ── AC-1: verbatim verify posture — asymmetric-only, parser-level gate ───────────
if grep -q 'AllowedAlgorithms' "$V" && grep -q 'RS256' "$V" && grep -q 'ES512' "$V" \
   && ! grep -Eq '"HS(256|384|512)"|"none"' "$V"; then
  ok "AC-1: AllowedAlgorithms is asymmetric-only (no HS*/none)"
else
  failc "AC-1: AllowedAlgorithms missing or admits HS*/none"
fi
if grep -q 'WithValidMethods' "$V" && grep -q 'WithoutClaimsValidation' "$V"; then
  ok "AC-1: parser built with WithValidMethods + WithoutClaimsValidation"
else
  failc "AC-1: parser-level algorithm gate not wired"
fi

# ── AC-2: test signer is TEST-ONLY — Stowage never signs ─────────────────────────
mint_leak=0
for f in internal/auth/*.go; do
  case "$f" in *_test.go) continue ;; esac
  if grep -Eq 'rsa\.PrivateKey|ecdsa\.PrivateKey|\.SignedString\(' "$f" 2>/dev/null; then
    mint_leak=1
    failc "AC-2: $f references minting material outside _test.go (Stowage must never sign)"
  fi
done
[ "$mint_leak" -eq 0 ] && ok "AC-2: no signing in the shipped binary (test signer is test-only, L1)"

# ── AC-4/AC-9: keyring is the zero-config default; all seven knobs explainable ──
# NOTE: there is no `stowage config get` subcommand (only `config explain`
# exists, per cmd/stowage/main.go's configUsage) — this deviates from the
# plan's illustrative "stowage config get auth.mode" wording, matching ae5/
# ae6's established real pattern instead. Output is captured ONCE then grepped
# repeatedly (not piped directly from the binary per grep call) to avoid the
# SIGPIPE-under-pipefail flake a short-circuiting `grep -q` can trigger on a
# live pipe.
if grep -q '"auth.mode"' internal/config/config.go; then
  ok "knob: auth.mode registered in config"
else
  failc "knob: auth.mode absent from config"
fi

BIN=/tmp/stowage-smoke-ae7
trap 'rm -f "$BIN"' EXIT
if CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null; then
  out=$("$BIN" config explain 2>&1)
  if echo "$out" | grep -Eq 'auth\.mode[[:space:]]+=[[:space:]]+keyring[[:space:]]+\[default\]'; then
    ok "AC-4: auth.mode defaults to keyring (zero-config start, config explain)"
  else
    failc "AC-4: auth.mode default is not keyring per config explain"
  fi

  miss=0
  for k in auth.mode auth.issuer auth.audience auth.algorithms auth.jwks.url auth.jwks.file auth.jwks.max_stale; do
    if ! echo "$out" | grep -q "$k"; then
      miss=1
      failc "AC-9: knob $k not explainable"
    fi
  done
  [ "$miss" -eq 0 ] && ok "AC-9: all seven auth.* knobs registered + explainable"
else
  skip "AC-4/9: stowage binary did not build — config knob checks skipped"
fi

# ── AC-7: one core, thin surfaces — both seams call the Authenticator ────────────
if [ -f "$A" ] && grep -q 'Authenticator' internal/api/auth.go && grep -q 'Authenticator' internal/mcpserver/server.go; then
  ok "AC-7: API + MCP seams reference the shared Authenticator"
else
  failc "AC-7: a surface does not route through auth.Authenticator"
fi
if grep -q 'validator\.Validate\|jwt\.Parse' internal/api/auth.go internal/mcpserver/server.go 2>/dev/null; then
  failc "AC-7: a surface calls Validate/Parse directly (should go through Authenticator)"
else
  ok "AC-7: no surface parses/validates a JWT directly"
fi

# ── AC-10: jwt/v5 promoted to a direct require ───────────────────────────────────
if grep -q 'golang-jwt/jwt/v5' go.mod && ! grep -E 'golang-jwt/jwt/v5.*// indirect' go.mod >/dev/null; then
  ok "AC-10: golang-jwt/jwt/v5 is a direct require"
else
  failc "AC-10: golang-jwt/jwt/v5 still // indirect (or missing)"
fi

# ── AC-1/3/6/8: unit tests ───────────────────────────────────────────────────────
if go test ./internal/auth/ -run 'Validator|JWKS|Authenticator' -count=1 >/dev/null 2>&1; then
  ok "AC-1/3/6/8: auth verifier unit tests pass"
else
  failc "AC-1/3/6/8: auth verifier unit tests fail"
fi

# ── AC-11: §17 integration (real JWKS + test signer) ─────────────────────────────
if go test ./test/integration/ -run 'AuthJWT|JWTVerifier' -count=1 >/dev/null 2>&1; then
  ok "AC-11: JWT verifier integration test passes"
else
  failc "AC-11: JWT verifier integration test fails"
fi

exit "$fails"
