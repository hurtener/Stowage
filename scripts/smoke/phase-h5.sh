#!/usr/bin/env bash
# Phase h5 smoke: deterministic, LLM-free playbook assembly across {SDK, MCP,
# HTTP} — D-067 Wave C, D-072.
#
# Verifies:
#   AC-1  internal/playbook assembles a sectioned/ranked/budget-packed playbook
#         AND imports no gateway (the §6 LLM-free lint passes).
#   AC-2  same memories ⇒ byte-identical output; append-bias prefix-stable.
#   AC-3  Store.ListByKinds proven on the sqlite driver via the conformance suite.
#   AC-4  GET /v1/playbook is a real wired route returning a sectioned playbook
#         with a budget and NO `stub`/`entries` placeholder.
#   AC-5  the assembled playbook is identical across {SDK embedded, HTTP, MCP}.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

# Pre-build SKIP-graceful guard (CLAUDE.md §4.2).
if [ ! -d internal/playbook ] || ! grep -rq 'func Assemble' internal/playbook/ 2>/dev/null; then
  skip "AC-1: internal/playbook.Assemble not yet implemented (plan skeleton)"
  skip "AC-4: GET /v1/playbook + SDK + MCP real (pending h5)"
  skip "AC-5: playbook identical across surfaces (pending h5)"
  exit "$fails"
fi

# ── Build ─────────────────────────────────────────────────────────────────────

BIN=/tmp/stowage-smoke-h5
TMPDIR_SMOKE=$(mktemp -d)
trap 'rm -f "$BIN"; rm -rf "$TMPDIR_SMOKE"' EXIT

CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null \
  && ok "CGo-free binary build" \
  || { failc "CGo-free binary build"; exit "$fails"; }

# ── AC-1 + AC-2: assembly + LLM-free lint + determinism/append-bias/budget ─────

if CGO_ENABLED=1 go test -count=1 -timeout=120s ./internal/playbook/ 2>/tmp/playbook-test-h5.log; then
  ok "AC-1/AC-2: internal/playbook tests pass (LLM-free lint + golden + append-bias + budget)"
else
  failc "AC-1/AC-2: internal/playbook tests failed"
  cat /tmp/playbook-test-h5.log >&2
fi

# ── AC-3: ListByKinds conformance on the sqlite driver ────────────────────────

if CGO_ENABLED=1 go test -count=1 -timeout=120s -run 'TestConformance|Conformance|Sqlite' \
     ./internal/store/sqlitestore/ 2>/tmp/conf-test-h5.log; then
  ok "AC-3: store conformance (incl. MemoryListByKinds*) passes on sqlite"
else
  failc "AC-3: store conformance failed"
  cat /tmp/conf-test-h5.log >&2
fi

# ── AC-5: all-surfaces-identical integration test ─────────────────────────────

if CGO_ENABLED=1 go test -count=1 -timeout=180s -run TestPlaybookParity_AllSurfaces \
     ./test/integration/ 2>/tmp/parity-test-h5.log; then
  ok "AC-5: playbook identical across {SDK embedded, HTTP, MCP} (real sqlite)"
else
  failc "AC-5: playbook surface-parity failed"
  cat /tmp/parity-test-h5.log >&2
fi

# ── AC-4: live GET /v1/playbook is real (wired route, no stub) ─────────────────

SMOKE_PORT=17155
DB_PATH="${TMPDIR_SMOKE}/smokeh5.db"
CFG_PATH="${TMPDIR_SMOKE}/stowageh5.yaml"
cat > "$CFG_PATH" <<YAML
store:
  driver: sqlite
  dsn: "${DB_PATH}"
gateway:
  driver: mock
server:
  listen: ":${SMOKE_PORT}"
YAML

"$BIN" migrate --config "$CFG_PATH" >/dev/null 2>&1 \
  && ok "AC-4: migrate applied" \
  || { failc "AC-4: migrate failed"; exit "$fails"; }

"$BIN" serve --config "$CFG_PATH" >"${TMPDIR_SMOKE}/serveh5.log" 2>&1 &
SRV_PID=$!

SERVER_URL="http://127.0.0.1:${SMOKE_PORT}"
READY=0
for _ in $(seq 1 50); do
  sleep 0.1
  if curl -sf "${SERVER_URL}/healthz" >/dev/null 2>&1; then READY=1; break; fi
done

if [ "$READY" -eq 0 ]; then
  failc "AC-4: server did not become ready within 5 s"
  kill "$SRV_PID" 2>/dev/null; wait "$SRV_PID" 2>/dev/null
else
  KEY_RESP=$(curl -sf -X POST "${SERVER_URL}/v1/admin/keys" \
    -H "Content-Type: application/json" \
    -d '{"tenant_id":"smokeh5-tenant","role":"admin"}' 2>/dev/null || true)
  API_KEY=$(printf '%s' "$KEY_RESP" | sed -n 's/.*"plaintext"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

  if [ -z "$API_KEY" ]; then
    failc "AC-4: could not bootstrap API key (response: ${KEY_RESP})"
  else
    PB=$(curl -sf "${SERVER_URL}/v1/playbook" -H "Authorization: Bearer ${API_KEY}" 2>/dev/null || true)
    if printf '%s' "$PB" | grep -q '"budget"' && printf '%s' "$PB" | grep -q '"token_budget"'; then
      ok "AC-4: GET /v1/playbook returns a real sectioned/budgeted response"
    else
      failc "AC-4: GET /v1/playbook missing sections/budget (response: ${PB})"
    fi
    if printf '%s' "$PB" | grep -Eq '"stub"|"entries"'; then
      failc "AC-4: GET /v1/playbook still carries the stub placeholder (response: ${PB})"
    else
      ok "AC-4: GET /v1/playbook carries no stub placeholder"
    fi
  fi
  kill "$SRV_PID" 2>/dev/null
  wait "$SRV_PID" 2>/dev/null
fi

# ── Summary ───────────────────────────────────────────────────────────────────

echo ""
if [ "$fails" -eq 0 ]; then
  echo "phase-h5 smoke: ALL CHECKS PASSED"
else
  echo "phase-h5 smoke: $fails check(s) FAILED" >&2
fi
exit "$fails"
