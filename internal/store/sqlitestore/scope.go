package sqlitestore

import "github.com/hurtener/stowage/internal/identity"

// buildScopeWhere builds a WHERE clause fragment for scope isolation.
// Tenant is always required; project/user/session are added when non-empty.
// Returns (clause, args) where clause starts with "tenant_id = ?".
func buildScopeWhere(scope identity.Scope) (string, []interface{}) {
	clause := "tenant_id = ?"
	args := []interface{}{scope.Tenant}
	if scope.Project != "" {
		clause += " AND project_id = ?"
		args = append(args, scope.Project)
	}
	if scope.User != "" {
		clause += " AND user_id = ?"
		args = append(args, scope.User)
	}
	if scope.Session != "" {
		clause += " AND session_id = ?"
		args = append(args, scope.Session)
	}
	return clause, args
}
