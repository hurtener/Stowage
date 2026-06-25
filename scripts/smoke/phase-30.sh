#!/usr/bin/env bash
# Smoke for Phase 30 — read/write scope enforcement (multi-user-per-tenant isolation, D-124/125).
# Asserts the load-bearing invariants survive; behavioural proof is in the package tests
# (conformance RecordAppendScopeFill + retrieval TestRetrieve_UserScopeIsolation).
set -uo pipefail
cd "$(dirname "$0")/../.."
fails=0
ok(){ echo "OK   $*"; }; failc(){ echo "FAIL $*"; fails=$((fails+1)); }
has(){ grep -q "$1" "$2" && ok "$3" || failc "$3 ($2 :: $1)"; }

# D-124 — write: Append fills per-record scope (scope wins, record fills the gap), both drivers.
has "scopeOrRecord(scope.Project, rec.ProjectID)" internal/store/sqlitestore/records.go "sqlite Append fills per-record project/user/session"
has "scopeOrRecord(scope.Project, rec.ProjectID)" internal/store/pgstore/records.go "pgstore Append fills per-record project/user/session"
has "func scopeOrRecord" internal/store/sqlitestore/records.go "sqlite scopeOrRecord helper (scope-authoritative)"
has "func scopeOrRecord" internal/store/pgstore/records.go "pgstore scopeOrRecord helper"

# D-125 — read: retrieve builds the scope from caller-supplied project/user across all surfaces.
has "Project: req.ProjectID, User: req.UserID" internal/api/retrieve_handler.go "HTTP retrieve scopes by project/user"
has 'json:"project_id"' internal/api/retrieve_handler.go "HTTP retrieve request has project_id"
has "Project: req.ProjectID, User: req.UserID" sdk/stowage/embedded.go "SDK retrieve scopes by project/user"
has 'ProjectID  string `json:"project_id,omitempty"`' sdk/stowage/types.go "SDK RetrieveRequest has project_id"
has "Project: in.ProjectID, User: in.UserID" internal/mcpserver/handlers.go "MCP retrieve scopes by project/user"
has 'ProjectID  string `json:"project_id,omitempty"`' internal/mcpserver/contracts.go "MCP RetrieveInput has project_id"

# Regression guards present.
has "testRecordAppendScopeFill" internal/store/conformance/conformance.go "write-scope conformance present"
has "TestRetrieve_UserScopeIsolation" internal/retrieval/retrieval_test.go "read-isolation guard present"

go build ./... >/dev/null 2>&1 && ok "build green" || failc "build"
echo "phase-30 smoke: $((13 - fails)) passed, $fails failed"
exit "$fails"
