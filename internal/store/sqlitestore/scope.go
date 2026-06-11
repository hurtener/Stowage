package sqlitestore

import (
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// buildScopeWhere builds a WHERE clause fragment for scope isolation.
// Returns ErrScopeRequired if scope.Tenant is empty (P3: store fails closed).
// Tenant is always required; project/user/session are added when non-empty.
// Returns (clause, args, error) where clause starts with "tenant_id = ?".
func buildScopeWhere(scope identity.Scope) (string, []interface{}, error) {
	if scope.Tenant == "" {
		return "", nil, store.ErrScopeRequired
	}
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
	return clause, args, nil
}
