package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type branchStore struct{ s *sqliteStore }

// Create inserts a new branch record.
func (b *branchStore) Create(ctx context.Context, scope identity.Scope, br store.Branch) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return b.s.exec(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO branches
				(id, tenant_id, project_id, user_id, session_id, parent_branch_id,
				 status, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			br.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User),
			br.SessionID, br.ParentBranchID, br.Status, br.CreatedAt, br.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: create branch %q: %w", br.ID, err)
		}
		return nil
	})
}

// Get returns a branch by ID within scope.
func (b *branchStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Branch, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, id)
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), session_id, parent_branch_id, status, created_at, updated_at FROM branches WHERE ` + whereClause + ` AND id = ?` //nolint:gosec // whereClause built from controlled helper
	row := b.s.rdb.QueryRowContext(ctx, q, args...)
	br, scanErr := scanBranch(row)
	if errors.Is(scanErr, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return br, scanErr
}

// SetStatus updates the status and updated_at of a branch within scope.
func (b *branchStore) SetStatus(ctx context.Context, scope identity.Scope, id string, status string, updatedAt int64) error {
	whereClause, whereArgs, err := buildScopeWhere(scope)
	if err != nil {
		return err
	}
	// Arg order: SET args first, then WHERE args, then id.
	args := []interface{}{status, updatedAt}
	args = append(args, whereArgs...)
	args = append(args, id)
	return b.s.exec(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.Exec(
			`UPDATE branches SET status = ?, updated_at = ? WHERE `+whereClause+` AND id = ?`, //nolint:gosec // whereClause built from controlled helper
			args...,
		)
		if execErr != nil {
			return fmt.Errorf("sqlitestore: set branch status %q: %w", id, execErr)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// ListBySession returns all branches for a session within scope.
func (b *branchStore) ListBySession(ctx context.Context, scope identity.Scope, sessionID string) ([]store.Branch, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	if sessionID != "" {
		whereClause += " AND session_id = ?"
		args = append(args, sessionID)
	}
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), session_id, parent_branch_id, status, created_at, updated_at FROM branches WHERE ` + whereClause + ` ORDER BY created_at ASC` //nolint:gosec // whereClause built from controlled helper
	rows, queryErr := b.s.rdb.QueryContext(ctx, q, args...)
	if queryErr != nil {
		return nil, fmt.Errorf("sqlitestore: list branches: %w", queryErr)
	}
	defer func() { _ = rows.Close() }()

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
