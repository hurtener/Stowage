#!/usr/bin/env bash
# Smoke script for Phase 29 — consolidation hardening (write-time core).
# This phase changes BEHAVIOUR (extraction/reconcile prompts, near-dup guard, buffer
# window), not the binary surface, so the checks assert the behavioural invariants via
# the package tests + source markers. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails+1)); }
skip() { echo "SKIP $*"; }

# --- H3: numeric-correction guard (D-104) --------------------------------------
if go test ./internal/reconcile/ -run TestNumeralsDiverge -count=1 >/dev/null 2>&1; then
  ok "H3 numeric-correction guard (NumeralsDiverge) passes"
else
  failc "H3 numeric-correction guard test"
fi
if grep -q "NumeralsDiverge(normalized, n.Content)" internal/reconcile/reconcile.go; then
  ok "H3 guard wired before the near-dup auto-discard"
else
  failc "H3 guard not wired into the near-dup discard path"
fi

# --- H2: reconcile decision prompt prefers supersede on a different value -------
if grep -q "the candidate is the newer assertion: choose \"supersede\"" internal/reconcile/prompt.go; then
  ok "H2 reconcile prompt: supersede-on-different-value rule present"
else
  failc "H2 reconcile prompt missing supersede-on-different-value rule"
fi

# --- H1: extraction prompt retains qualifiers; template version bumped ----------
if grep -q 'PromptTemplateVersion = "4"' internal/pipeline/prompt.go; then
  ok "H1 extraction PromptTemplateVersion bumped to 4"
else
  failc "H1 extraction PromptTemplateVersion not bumped"
fi
if grep -q "Do NOT narrate the change" internal/pipeline/prompt.go; then
  ok "H1 extraction prompt: forbid change-narratives (Fitbit fix)"
else
  failc "H1 extraction prompt missing change-narrative prohibition"
fi
if grep -q "PRESERVE every quantitative qualifier" internal/pipeline/prompt.go; then
  ok "H1 extraction prompt: qualifier/unit retention instruction present"
else
  failc "H1 extraction prompt missing qualifier-retention instruction"
fi

# --- H1: coarser assistant buffer window (D-107) -------------------------------
if go test ./internal/config/ -run TestBufferTriggers -count=1 >/dev/null 2>&1 \
   || go test ./internal/pipeline/ -run TestTriggersFromConfig -count=1 >/dev/null 2>&1; then
  ok "H1 buffer-window defaults (assistant 18/2500/180s) pass"
else
  failc "H1 buffer-window default test"
fi

# --- H5: dual-visibility (retain-and-flag superseded) + config knob (D-105) ----
BIN=/tmp/stowage-smoke-29
if CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage >/dev/null 2>&1; then
  if "$BIN" config explain 2>/dev/null | grep -q "retrieval.include_superseded"; then
    ok "H5 config explain: retrieval.include_superseded present"
  else
    failc "H5 config explain: retrieval.include_superseded missing"
  fi
  rm -f "$BIN"
else
  failc "build for H5 config check"
fi
if go test ./internal/retrieval/ -run TestIncludeSupersededDualVisibility -count=1 >/dev/null 2>&1; then
  ok "H5 dual-visibility: superseded predecessor surfaced flagged"
else
  failc "H5 dual-visibility test"
fi

# --- Prompt goldens still consistent (no drift) -------------------------------
if go test ./internal/reconcile/ ./internal/pipeline/ -run 'Golden|Prompt' -count=1 >/dev/null 2>&1; then
  ok "reconcile + extraction prompt goldens consistent"
else
  failc "prompt golden mismatch"
fi

exit "$fails"
