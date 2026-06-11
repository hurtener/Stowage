package pgstore

import (
	"fmt"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// buildScopeWhere builds a parameterized WHERE clause for scope isolation.
// Returns ErrScopeRequired if scope.Tenant is empty (P3: store fails closed).
// startIdx is the $N parameter index to start from (PostgreSQL uses $1, $2, ...).
// Returns (clause, args, nextIdx, error).
func buildScopeWhere(scope identity.Scope, startIdx int) (string, []interface{}, int, error) {
	if scope.Tenant == "" {
		return "", nil, startIdx, store.ErrScopeRequired
	}
	clause := fmt.Sprintf("tenant_id = $%d", startIdx)
	args := []interface{}{scope.Tenant}
	next := startIdx + 1
	if scope.Project != "" {
		clause += fmt.Sprintf(" AND project_id = $%d", next)
		args = append(args, scope.Project)
		next++
	}
	if scope.User != "" {
		clause += fmt.Sprintf(" AND user_id = $%d", next)
		args = append(args, scope.User)
		next++
	}
	if scope.Session != "" {
		clause += fmt.Sprintf(" AND session_id = $%d", next)
		args = append(args, scope.Session)
		next++
	}
	return clause, args, next, nil
}
