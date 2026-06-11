package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type bufferStore struct{ s *sqliteStore }

func (b *bufferStore) AppendItem(ctx context.Context, scope identity.Scope, item store.BufferItem) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	return b.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if item.CreatedAt == 0 {
			item.CreatedAt = now
		}
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO buffer_items
				(id, tenant_id, project_id, user_id, session_id,
				 buffer_key, branch_id, record_id, token_estimate, flushed_at, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			item.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
			item.BufferKey, item.BranchID, item.RecordID, item.TokenEstimate, item.FlushedAt, item.CreatedAt,
		)
		return err
	})
}

func (b *bufferStore) ListDue(ctx context.Context, scope identity.Scope, bufferKey string, limit int) ([]store.BufferItem, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, bufferKey, limit)
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), buffer_key, branch_id, record_id, token_estimate, flushed_at, created_at FROM buffer_items WHERE ` + whereClause + ` AND buffer_key = ? AND flushed_at = 0 ORDER BY created_at ASC LIMIT ?` //nolint:gosec // whereClause is built from controlled helper, not user input
	rows, err := b.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list due buffer items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanBufferItems(rows)
}

// Flush atomically marks all unflushed items for bufferKey as flushed.
func (b *bufferStore) Flush(ctx context.Context, scope identity.Scope, bufferKey string) ([]store.BufferItem, error) {
	whereClause, whereArgs, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	var flushed []store.BufferItem

	err = b.s.exec(ctx, func(tx *sql.Tx) error {
		// Select unflushed items (inside the transaction = atomic).
		selArgs := append(append([]interface{}(nil), whereArgs...), bufferKey)
		qf := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), buffer_key, branch_id, record_id, token_estimate, flushed_at, created_at FROM buffer_items WHERE ` + whereClause + ` AND buffer_key = ? AND flushed_at = 0 ORDER BY created_at ASC` //nolint:gosec // whereClause is built from controlled helper, not user input
		rows, err := tx.Query(qf, selArgs...)
		if err != nil {
			return fmt.Errorf("sqlitestore: flush select: %w", err)
		}
		items, err := scanBufferItems(rows)
		_ = rows.Close()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}

		now := time.Now().UnixMilli()
		for _, item := range items {
			if _, err := tx.Exec(
				`UPDATE buffer_items SET flushed_at = ? WHERE id = ?`, now, item.ID,
			); err != nil {
				return fmt.Errorf("sqlitestore: flush update %q: %w", item.ID, err)
			}
			item.FlushedAt = now
		}
		flushed = items
		return nil
	})
	return flushed, err
}

func scanBufferItems(rows *sql.Rows) ([]store.BufferItem, error) {
	var out []store.BufferItem
	for rows.Next() {
		var item store.BufferItem
		if err := rows.Scan(
			&item.ID, &item.TenantID, &item.ProjectID, &item.UserID, &item.SessionID,
			&item.BufferKey, &item.BranchID, &item.RecordID, &item.TokenEstimate, &item.FlushedAt, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
