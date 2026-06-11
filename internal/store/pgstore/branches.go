package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type branchStore struct{ s *pgStore }

// Create inserts a new branch record.
func (b *branchStore) Create(ctx context.Context, scope identity.Scope, br store.Branch) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	_, err := b.s.pool.Exec(ctx, `
		INSERT INTO branches
			(id, tenant_id, project_id, user_id, session_id, parent_branch_id,
			 status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		br.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User),
		br.SessionID, br.ParentBranchID, br.Status, br.CreatedAt, br.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("pgstore: create branch %q: %w", br.ID, err)
	}
	return nil
}

// Get returns a branch by ID within scope.
func (b *branchStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Branch, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	args = append(args, id)
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''),
	             session_id, parent_branch_id, status, created_at, updated_at
	      FROM branches WHERE ` + whereClause + fmt.Sprintf(` AND id = $%d`, next) //nolint:gosec // whereClause built from controlled helper
	row := b.s.pool.QueryRow(ctx, q, args...)
	br, scanErr := scanBranch(row)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return br, scanErr
}

// SetStatus updates the status and updated_at of a branch within scope.
func (b *branchStore) SetStatus(ctx context.Context, scope identity.Scope, id string, status string, updatedAt int64) error {
	// PostgreSQL positional params: $1=status, $2=updatedAt, then scope params, then $N=id.
	// We start the scope WHERE at index 3 (after the two SET params).
	whereClause, whereArgs, next, err := buildScopeWhere(scope, 3)
	if err != nil {
		return err
	}
	idIdx := next
	// Arg order: status, updatedAt, <scope args...>, id
	args := []interface{}{status, updatedAt}
	args = append(args, whereArgs...)
	args = append(args, id)
	q := fmt.Sprintf(
		`UPDATE branches SET status = $1, updated_at = $2 WHERE %s AND id = $%d`, //nolint:gosec // whereClause built from controlled helper
		whereClause, idIdx,
	)
	tag, execErr := b.s.pool.Exec(ctx, q, args...)
	if execErr != nil {
		return fmt.Errorf("pgstore: set branch status %q: %w", id, execErr)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListBySession returns all branches for a session within scope.
func (b *branchStore) ListBySession(ctx context.Context, scope identity.Scope, sessionID string) ([]store.Branch, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	if sessionID != "" {
		whereClause += fmt.Sprintf(` AND session_id = $%d`, next)
		args = append(args, sessionID)
	}
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''),
	             session_id, parent_branch_id, status, created_at, updated_at
	      FROM branches WHERE ` + whereClause + ` ORDER BY created_at ASC` //nolint:gosec // whereClause built from controlled helper
	rows, queryErr := b.s.pool.Query(ctx, q, args...)
	if queryErr != nil {
		return nil, fmt.Errorf("pgstore: list branches: %w", queryErr)
	}
	defer rows.Close()

	var out []store.Branch
	for rows.Next() {
		br, scanErr := scanBranch(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *br)
	}
	return out, rows.Err()
}

func scanBranch(row rowScanner) (*store.Branch, error) {
	var br store.Branch
	err := row.Scan(
		&br.ID, &br.TenantID, &br.ProjectID, &br.UserID,
		&br.SessionID, &br.ParentBranchID, &br.Status,
		&br.CreatedAt, &br.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &br, nil
}
