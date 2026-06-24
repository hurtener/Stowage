#!/usr/bin/env bash
# Smoke for Phase 29d (correctness wave) — D-110/111/114/115/116/117/118 + the §17
# adversarial-review fixes D-119/120/121. Asserts the load-bearing invariants survive
# (the deep behavioural guards live in the package unit tests, several mutation-verified).
set -uo pipefail
cd "$(dirname "$0")/../.."
fails=0
ok(){ echo "OK   $*"; }; failc(){ echo "FAIL $*"; fails=$((fails+1)); }
has(){ grep -q "$1" "$2" && ok "$3" || failc "$3 ($2 :: $1)"; }

# B1 / D-119 — dedupe isolates partitions with EXACT-leaf scope (no cross-user merge).
has "ListActiveInScope" internal/store/store.go "store seam exposes ListActiveInScope"
has "func buildExactScopeWhere" internal/store/sqlitestore/scope.go "sqlite buildExactScopeWhere (IS NULL leaf)"
has "func buildExactScopeWhere" internal/store/pgstore/scope.go "pgstore buildExactScopeWhere (IS NULL leaf)"
has "ExactScope bool" internal/store/types.go "NeighborQuery.ExactScope flag"
has "ListActiveInScope" internal/lifecycle/dedupe.go "dedupe seeds from ListActiveInScope"
has "ExactScope: true" internal/lifecycle/dedupe.go "dedupe FindNeighbors uses ExactScope"

# D-110 — decay grace in milliseconds (not ns-as-ms).
has "DecayInterval.Milliseconds()" internal/lifecycle/decay.go "decay grace computed in milliseconds"

# D-120 — expire/rollup reversibility (P4).
has '"memory.expired"' internal/reconcile/rollback.go "memory.expired is restorable"
has 'Type:      "memory.merged"' internal/lifecycle/rollup.go "rollup emits memory.merged (sibling-safe rollback)"

# D-121 — cache invalidation matches the tenant-keyed cache; key includes effective limit.
has "invalidateScope(identity.Scope{Tenant: scope.Tenant})" internal/lifecycle/dedupe.go "dedupe invalidates at tenant scope"
has "includeLanes bool, limit int" internal/retrieval/cache.go "cache key includes effective limit"

# D-115 — rerank runs before the trim-to-limit.
has "Trim to the requested limit AFTER" internal/retrieval/retrieval.go "rerank precedes trim-to-limit"

# Regression guards present (the behavioural assertions).
has "TestDedupeSweepNeverMergesAcrossUsers" internal/lifecycle/dedupe_test.go "cross-user dedupe guard present"
has "TestDecayExpireIsReversible" internal/lifecycle/decay_test.go "expire-rollback guard present"
has "TestRollupSweepIsReversible" internal/lifecycle/rollup_test.go "rollup-rollback guard present"
has "TestRerankPromotesBelowLimitCandidate" internal/retrieval/retrieval_test.go "rerank-before-trim guard present"
has "testMemoryListActiveInScope" internal/store/conformance/conformance.go "exact-scope conformance test present"

go build ./... >/dev/null 2>&1 && ok "build green" || failc "build"
echo "phase-29d smoke: $((18 - fails)) passed, $fails failed"
exit "$fails"
