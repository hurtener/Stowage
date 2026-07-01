#!/usr/bin/env bash
# Smoke — phase ae5: list/browse (most-recent-first, superseded filter).
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

# --- store seam: ListByScopeRecent on the interface + both drivers ----------
if grep -q "ListByScopeRecent" "$STORE" 2>/dev/null; then
  ok "seam: MemoryStore.ListByScopeRecent declared"
else
  skip "seam: MemoryStore.ListByScopeRecent not yet on the interface"
fi

if grep -q "ListByScopeRecent" "$SQLITE" 2>/dev/null && grep -q "ListByScopeRecent" "$PG" 2>/dev/null; then
  ok "drivers: ListByScopeRecent implemented on sqlite + postgres"
else
  skip "drivers: ListByScopeRecent not yet implemented on both drivers"
fi

# --- conformance registers the new ordered-scan test ------------------------
if grep -q "MemoryListByScopeRecent" "$CONF" 2>/dev/null; then
  ok "conformance: MemoryListByScopeRecent registered"
else
  skip "conformance: MemoryListByScopeRecent not yet registered"
fi

# --- H4: superseded REUSES ListByStatus — no new superseded query -----------
if [ -f "$BROWSE" ]; then
  if grep -qE "ListSuperseded|ListByScopeSuperseded" "$STORE" "$SQLITE" "$PG" 2>/dev/null; then
    failc "H4: a new superseded query exists (must reuse ListByStatus)"
  elif grep -q "ListByStatus" "$BROWSE" 2>/dev/null; then
    ok "H4: superseded mode reuses ListByStatus, no new superseded query"
  else
    failc "H4: browse.go does not reference ListByStatus for the superseded mode"
  fi
else
  skip "H4: browse core not yet built"
fi

# --- Browse core: present + gateway-free (D-036) ----------------------------
if [ -f "$BROWSE" ]; then
  if grep -q "func Browse" "$BROWSE" && grep -q "BrowseMode" "$BROWSE"; then
    ok "core: retrieval.Browse + BrowseMode defined"
  else
    failc "core: retrieval.Browse / BrowseMode missing"
  fi
  if grep -q "internal/gateway" "$BROWSE"; then
    failc "D-036: browse.go imports the gateway (must be gateway-free)"
  else
    ok "D-036: browse.go is gateway-free"
  fi
else
  skip "core: internal/retrieval/browse.go not yet built"
fi

# --- config knob: retrieval.browse_default_limit (D-034) --------------------
if grep -q "browse_default_limit" "$CFG" 2>/dev/null; then
  ok "knob: retrieval.browse_default_limit registered in config"
  if [ -x ./bin/stowage ]; then
    val=$(./bin/stowage config get retrieval.browse_default_limit 2>/dev/null)
    if [ "$val" = "30" ]; then
      ok "knob: default browse_default_limit = 30"
    else
      failc "knob: browse_default_limit default = '$val', want 30"
    fi
  else
    skip "knob: ./bin/stowage not built — cannot read default"
  fi
else
  skip "knob: retrieval.browse_default_limit not yet in config"
fi

# --- surface parity {SDK, HTTP, MCP} ----------------------------------------
if grep -q "/v1/memories\"" "$APISRV" 2>/dev/null || grep -qE 'GET /v1/memories"' "$APISRV" 2>/dev/null; then
  ok "HTTP: GET /v1/memories route registered"
else
  skip "HTTP: GET /v1/memories browse route not yet registered"
fi

if grep -q "memory_browse" "$MCPSRV" 2>/dev/null; then
  ok "MCP: memory_browse tool registered"
else
  skip "MCP: memory_browse tool not yet registered"
fi

if grep -q "Browse(ctx context.Context" "$SDKCLIENT" 2>/dev/null || grep -q "Browse(" "$SDKCLIENT" 2>/dev/null; then
  ok "SDK: Client.Browse method present"
else
  skip "SDK: Client.Browse not yet on the interface"
fi

# --- tests: conformance + core + parity -------------------------------------
if grep -q "MemoryListByScopeRecent" "$CONF" 2>/dev/null && [ -f "$BROWSE" ]; then
  if go test ./internal/retrieval/ -run Browse >/dev/null 2>&1; then
    ok "test: retrieval Browse unit tests pass"
  else
    failc "test: retrieval Browse unit tests fail"
  fi
else
  skip "test: browse tests not yet present"
fi

exit "$fails"
