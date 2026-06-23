#!/usr/bin/env bash
# scripts/smoke/phase-21.sh — smoke checks for Phase 21 (hardening & launch gate).
# Contract: "OK <check>" / "FAIL <check>" / "SKIP <check>"; exit non-zero iff any FAIL.
# Checks land as the phase executes (security-audit artifact, five-minute smoke,
# cross-build, forbidden-names history sweep, LICENSE). Until each surface exists the
# check SKIPs gracefully (CLAUDE.md §4.2).
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails+1)); }
skip() { echo "SKIP $*"; }

# 1. LICENSE present (Apache-2.0, OQ-5 → D-097)
if [ -f LICENSE ]; then
  grep -qi "Apache License" LICENSE && ok "LICENSE is Apache-2.0" || failc "LICENSE present but not Apache-2.0"
else
  skip "LICENSE not added yet (21.4)"
fi

# 2. Security-audit artifact present (21.1)
if [ -f docs/security-audit.md ]; then ok "security-audit artifact present"; else skip "security-audit.md not written yet (21.1)"; fi

# 3. Five-minute-rule smoke present (21.2)
if [ -f scripts/smoke/phase-21-fiveminute.sh ]; then ok "five-minute smoke present"; else skip "five-minute smoke not built yet (21.2)"; fi

# 4. Forbidden-names history sweep present + green (21.4)
if [ -f scripts/forbidden-history-sweep.sh ]; then
  bash scripts/forbidden-history-sweep.sh >/dev/null 2>&1 && ok "forbidden-names history sweep green" || failc "forbidden-names history sweep found a hit"
else
  skip "forbidden-names history sweep not built yet (21.4)"
fi

# 5. Full-cycle live acceptance script present (21.6 — operator-run, presence only here)
if [ -f scripts/acceptance/full-cycle-live.sh ]; then ok "full-cycle live acceptance script present"; else skip "full-cycle live acceptance not built yet (21.6)"; fi

# 6. Cross-build matrix target (21.3)
if grep -q "^release:" Makefile 2>/dev/null; then ok "make release target present"; else skip "make release not added yet (21.3)"; fi

# 7. DSAR cascading delete is wired, not stubbed (§13, D-098)
if grep -q "handleDSARStub" internal/api/*.go 2>/dev/null; then
  failc "DSAR handler is still the 501 stub (handleDSARStub present)"
elif grep -q "func (s \*Server) handleDSAR(" internal/api/keys_handler.go 2>/dev/null \
  && grep -q "DeleteUserData" internal/store/store.go 2>/dev/null; then
  ok "DSAR cascading delete wired (handleDSAR + Store.DeleteUserData)"
else
  skip "DSAR cascade not wired yet (21.1)"
fi

exit "$fails"
