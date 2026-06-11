package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type bufferStore struct{ s *pgStore }

func (b *bufferStore) AppendItem(ctx context.Context, scope identity.Scope, item store.BufferItem) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	now := time.Now().UnixMilli()
	if item.CreatedAt == 0 {
		item.CreatedAt = now
	}
	_, err := b.s.pool.Exec(ctx, `
		INSERT INTO buffer_items
			(id, tenant_id, project_id, user_id, session_id,
			 buffer_key, branch_id, record_id, token_estimate, flushed_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT(id) DO NOTHING`,
		item.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
		item.BufferKey, item.BranchID, item.RecordID, item.TokenEstimate, item.FlushedAt, item.CreatedAt,
	)
	return err
}

func (b *bufferStore) ListDue(ctx context.Context, scope identity.Scope, bufferKey string, limit int) ([]store.BufferItem, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	args = append(args, bufferKey, limit)
	bufIdx, limIdx := next, next+1
	rows, err := b.s.pool.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        buffer_key, branch_id, record_id, token_estimate, flushed_at, created_at
		 FROM buffer_items
		 WHERE `+whereClause+fmt.Sprintf(` AND buffer_key = $%d AND flushed_at = 0
		 ORDER BY created_at ASC LIMIT $%d`, bufIdx, limIdx),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list due: %w", err)
	}
	defer rows.Close()
	return scanBufferItems(rows)
}

func (b *bufferStore) Flush(ctx context.Context, scope identity.Scope, bufferKey string) ([]store.BufferItem, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	tx, err := b.s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	args = append(args, bufferKey)
	rows, err := tx.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        buffer_key, branch_id, record_id, token_estimate, flushed_at, created_at
		 FROM buffer_items
		 WHERE `+whereClause+fmt.Sprintf(` AND buffer_key = $%d AND flushed_at = 0
		 ORDER BY created_at ASC
		 FOR UPDATE`, next),
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: flush select: %w", err)
	}
	items, err := scanBufferItems(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, tx.Commit(ctx)
	}

	now := time.Now().UnixMilli()
	for i := range items {
		if _, err := tx.Exec(ctx,
			`UPDATE buffer_items SET flushed_at = $1 WHERE id = $2`, now, items[i].ID,
		); err != nil {
			return nil, fmt.Errorf("pgstore: flush update %q: %w", items[i].ID, err)
		}
		items[i].FlushedAt = now
	}
	return items, tx.Commit(ctx)
}

func scanBufferItems(rows interface {
	Next() bool
	Scan(...interface{}) error
	Err() error
}) ([]store.BufferItem, error) {
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
