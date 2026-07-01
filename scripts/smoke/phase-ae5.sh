#!/usr/bin/env bash
# Smoke — phase ae5: list/browse (most-recent-first, superseded filter), D-143.
# Contract (CLAUDE.md §4.2): print "OK <check>" per pass, "FAIL <check>" per fail,
# "SKIP <check>" where the surface isn't built yet. Exit non-zero iff any FAIL.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails+1)); }
skip() { echo "SKIP $*"; }

STORE=internal/store/store.go
SQLITE=internal/store/sqlitestore/memories.go
PG=internal/store/pgstore/memories.go
CONF=internal/store/conformance/conformance.go
BROWSE=internal/retrieval/browse.go
APISRV=internal/api/server.go
MCPSRV=internal/mcpserver/server.go
SDKCLIENT=sdk/stowage/client.go
CFG=internal/config/config.go
PARITY=test/integration/browse_test.go

if [ ! -f "$BROWSE" ]; then
  skip "ae5 not built yet ($BROWSE absent)"
  exit "$fails"
fi

# --- store seam: ListByScopeRecent on the interface + both drivers ----------
if grep -q "ListByScopeRecent" "$STORE" 2>/dev/null; then
  ok "seam: MemoryStore.ListByScopeRecent declared"
else
  failc "seam: MemoryStore.ListByScopeRecent not on the interface"
fi

if grep -q "ListByScopeRecent" "$SQLITE" 2>/dev/null && grep -q "ListByScopeRecent" "$PG" 2>/dev/null; then
  ok "drivers: ListByScopeRecent implemented on sqlite + postgres"
else
  failc "drivers: ListByScopeRecent not implemented on both drivers"
fi

# --- conformance registers the new ordered-scan test + scope isolation ------
if grep -q "MemoryListByScopeRecent" "$CONF" 2>/dev/null; then
  ok "conformance: MemoryListByScopeRecent registered"
else
  failc "conformance: MemoryListByScopeRecent not registered"
fi
if grep -q "MemoryListByScopeRecentScopeIsolation" "$CONF" 2>/dev/null; then
  ok "conformance: MemoryListByScopeRecentScopeIsolation (P3) registered"
else
  failc "conformance: no scope-isolation subtest for ListByScopeRecent"
fi

# --- H4: superseded REUSES ListByStatus — no new superseded query -----------
if grep -qE "func.*ListSuperseded\(|func.*ListByScopeSuperseded\(" internal/store -r 2>/dev/null; then
  failc "H4: a new superseded query exists (must reuse ListByStatus)"
else
  ok "H4: no new ListSuperseded/ListByScopeSuperseded method on the seam or drivers"
fi
if grep -q 'ListByStatus(ctx, scope, "superseded"' "$BROWSE" 2>/dev/null; then
  ok "H4: BrowseSuperseded reuses the EXISTING ListByStatus, no new superseded query"
else
  failc "H4: browse.go does not reference ListByStatus for the superseded mode"
fi

# --- Browse core: present, closed-enum parser, gateway-free (D-036) ---------
if grep -q "func Browse(" "$BROWSE" && grep -q "type BrowseMode" "$BROWSE"; then
  ok "core: retrieval.Browse + BrowseMode defined"
else
  failc "core: retrieval.Browse / BrowseMode missing"
fi
if grep -q "func ParseBrowseMode(" "$BROWSE"; then
  ok "core: retrieval.ParseBrowseMode (closed mode enum) defined"
else
  failc "core: ParseBrowseMode missing"
fi
if grep -q "internal/gateway" "$BROWSE"; then
  failc "D-036: browse.go imports the gateway (must be gateway-free)"
else
  ok "D-036: browse.go is gateway-free"
fi

# --- config knob: retrieval.browse_default_limit (D-034) --------------------
# The only config CLI surface is `stowage config explain` (no `config get`
# subcommand exists) — it iterates allKeys, so a registered key appears with
# its resolved default.
if grep -q "browse_default_limit" "$CFG" 2>/dev/null; then
  ok "knob: retrieval.browse_default_limit registered in config"
  BIN=/tmp/stowage-smoke-ae5
  trap 'rm -f "$BIN"' EXIT
  if CGO_ENABLED=0 go build -o "$BIN" ./cmd/stowage 2>/dev/null; then
    EXPLAIN_OUT=$("$BIN" config explain 2>&1 || true)
    if echo "$EXPLAIN_OUT" | grep -Eq 'retrieval\.browse_default_limit[[:space:]]+=[[:space:]]+30[[:space:]]+\[default\]'; then
      ok "knob: config explain shows retrieval.browse_default_limit = 30 [default]"
    else
      failc "knob: browse_default_limit default not found via config explain (got: $(echo "$EXPLAIN_OUT" | grep browse_default_limit || echo '(nothing)'))"
    fi
  else
    failc "knob: cgo-free build failed — cannot check config explain"
  fi
else
  failc "knob: retrieval.browse_default_limit not in config"
fi

# --- surface parity {SDK, HTTP, MCP} ----------------------------------------
if grep -qE 'GET /v1/memories"' "$APISRV" 2>/dev/null; then
  ok "HTTP: GET /v1/memories route registered"
else
  failc "HTTP: GET /v1/memories browse route not registered"
fi

if grep -q "memory_browse" "$MCPSRV" 2>/dev/null; then
  ok "MCP: memory_browse tool registered"
else
  failc "MCP: memory_browse tool not registered"
fi

if grep -q "Browse(ctx context.Context, req BrowseRequest)" "$SDKCLIENT" 2>/dev/null; then
  ok "SDK: Client.Browse method present"
else
  failc "SDK: Client.Browse not on the interface"
fi

# --- tests: store, retrieval core, cross-surface parity ----------------------
if go test ./internal/store/... -run 'ListByScopeRecent|Conformance' -count=1 >/dev/null 2>&1; then
  ok "test: internal/store ListByScopeRecent/Conformance tests pass"
else
  failc "test: internal/store ListByScopeRecent/Conformance tests fail"
fi

if go test ./internal/retrieval/ -run Browse -count=1 >/dev/null 2>&1; then
  ok "test: retrieval Browse unit tests pass"
else
  failc "test: retrieval Browse unit tests fail"
fi

if [ -f "$PARITY" ]; then
  if go test ./test/integration/ -run Browse -count=1 >/dev/null 2>&1; then
    ok "test: cross-surface (SDK/HTTP/MCP) browse parity test passes"
  else
    failc "test: test/integration Browse tests fail"
  fi
else
  failc "test: $PARITY missing"
fi

exit "$fails"
