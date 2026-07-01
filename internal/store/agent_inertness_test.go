package store_test

// agent_inertness_test.go — the AC-1/C1 grep gate (Phase ae1, D-135): proves
// identity.Scope.Agent is provably INERT on writes and scope predicates. Neither
// driver's buildScopeWhere/buildExactScopeWhere (scope.go) nor any INSERT into a
// scope-bearing table (records.go, memories.go — the two files that write the 12
// scope tables' tenant/project/user/session columns) ever reference Agent or an
// agent_id column, on EITHER driver. A future change that threads Scope.Agent
// into a scope-WHERE builder or a write path breaks this test — that is exactly
// the drift it exists to catch.
//
// scope.go never legitimately mentions "agent" in any form today, so a blunt
// case-insensitive substring check is precise. records.go DOES legitimately
// carry a `source_agent` column (a records-only provenance label, D-135) — the
// checks below use "agent_id" (the SQL column-naming convention the 12 scope
// tables use for user_id/session_id/etc.) and ".Agent" (the Go selector form)
// rather than a blanket "agent" substring, so source_agent never false-positives.

import (
	"os"
	"strings"
	"testing"
)

const (
	sqliteScopeGoPath   = "sqlitestore/scope.go"
	pgScopeGoPath       = "pgstore/scope.go"
	sqliteRecordsGoPath = "sqlitestore/records.go"
	pgRecordsGoPath     = "pgstore/records.go"
	sqliteMemoriesGo    = "sqlitestore/memories.go"
	pgMemoriesGo        = "pgstore/memories.go"
)

func readSourceFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestScopeWhereBuilders_NeverReferenceAgent is the C1 gate on the scope-WHERE
// builders: buildScopeWhere/buildExactScopeWhere build their clause field-by-field
// for Tenant/Project/User/Session ONLY — a new Agent field can never enter a scope
// predicate because these files never mention "agent" in any form.
func TestScopeWhereBuilders_NeverReferenceAgent(t *testing.T) {
	t.Parallel()
	for _, path := range []string{sqliteScopeGoPath, pgScopeGoPath} {
		src := strings.ToLower(readSourceFile(t, path))
		if strings.Contains(src, "agent") {
			t.Errorf("%s: must never reference Agent/agent_id (C1 inertness, D-135) — found a match", path)
		}
	}
}

// TestScopeTableInserts_NeverReferenceAgentColumn is the C1 gate on the write
// path: every INSERT into a scope table (records.go, memories.go) enumerates its
// columns explicitly and binds Tenant/Project/User/Session by name/position —
// none bind Agent, and there is no agent_id column to bind, on either driver.
func TestScopeTableInserts_NeverReferenceAgentColumn(t *testing.T) {
	t.Parallel()
	for _, path := range []string{sqliteRecordsGoPath, pgRecordsGoPath, sqliteMemoriesGo, pgMemoriesGo} {
		src := readSourceFile(t, path)
		if strings.Contains(src, "agent_id") {
			t.Errorf("%s: must never bind an agent_id column (C1 inertness, D-135) — found a match", path)
		}
		if strings.Contains(src, ".Agent") {
			t.Errorf("%s: must never reference the .Agent selector (C1 inertness, D-135) — found a match", path)
		}
	}
}
