package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type topicStore struct{ s *pgStore }

// Upsert inserts or updates a topic keyed by (scope, key).
// Uses UPDATE-then-INSERT for correct NULL-safe composite key handling.
func (t *topicStore) Upsert(ctx context.Context, scope identity.Scope, topic store.Topic) error {
	now := time.Now().UnixMilli()
	if topic.CreatedAt == 0 {
		topic.CreatedAt = now
	}
	if topic.UpdatedAt == 0 {
		topic.UpdatedAt = now
	}

	tx, err := t.s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: upsert topic begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Try UPDATE first (handles existing row by composite key regardless of id).
	pj := nullStr(scope.Project)
	us := nullStr(scope.User)
	se := nullStr(scope.Session)
	tag, err := tx.Exec(ctx, `
		UPDATE topics SET description=$1, status=$2, pack=$3, updated_at=$4
		WHERE tenant_id=$5
		  AND project_id IS NOT DISTINCT FROM $6
		  AND user_id    IS NOT DISTINCT FROM $7
		  AND session_id IS NOT DISTINCT FROM $8
		  AND key=$9`,
		topic.Description, topic.Status, topic.Pack, topic.UpdatedAt,
		scope.Tenant, pj, us, se, topic.Key,
	)
	if err != nil {
		return fmt.Errorf("pgstore: upsert topic update: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return tx.Commit(ctx)
	}

	// INSERT new row.
	if _, err := tx.Exec(ctx, `
		INSERT INTO topics
			(id, tenant_id, project_id, user_id, session_id, key, description, status, pack, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		topic.ID, scope.Tenant, pj, us, se,
		topic.Key, topic.Description, topic.Status, topic.Pack,
		topic.CreatedAt, topic.UpdatedAt,
	); err != nil {
		return fmt.Errorf("pgstore: upsert topic insert: %w", err)
	}
	return tx.Commit(ctx)
}

func (t *topicStore) Get(ctx context.Context, scope identity.Scope, key string) (*store.Topic, error) {
	whereClause, args, next := buildScopeWhere(scope, 1)
	args = append(args, key)
	row := t.s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        key, description, status, pack, created_at, updated_at
		 FROM topics WHERE `+whereClause+fmt.Sprintf(` AND key = $%d AND status != 'deleted'`, next),
		args...,
	)
	var topic store.Topic
	err := row.Scan(
		&topic.ID, &topic.TenantID, &topic.ProjectID, &topic.UserID, &topic.SessionID,
		&topic.Key, &topic.Description, &topic.Status, &topic.Pack,
		&topic.CreatedAt, &topic.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &topic, nil
}

func (t *topicStore) List(ctx context.Context, scope identity.Scope) ([]store.Topic, error) {
	whereClause, args, _ := buildScopeWhere(scope, 1)
	rows, err := t.s.pool.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        key, description, status, pack, created_at, updated_at
		 FROM topics WHERE `+whereClause+` AND status != 'deleted'
		 ORDER BY created_at ASC`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list topics: %w", err)
	}
	defer rows.Close()
	var out []store.Topic
	for rows.Next() {
		var topic store.Topic
		if err := rows.Scan(
			&topic.ID, &topic.TenantID, &topic.ProjectID, &topic.UserID, &topic.SessionID,
			&topic.Key, &topic.Description, &topic.Status, &topic.Pack,
			&topic.CreatedAt, &topic.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, topic)
	}
	return out, rows.Err()
}

func (t *topicStore) Delete(ctx context.Context, scope identity.Scope, key string) error {
	whereClause, whereArgs, next := buildScopeWhere(scope, 3)
	args := []interface{}{
		"deleted",
		time.Now().UnixMilli(),
	}
	args = append(args, whereArgs...)
	args = append(args, key)
	_, err := t.s.pool.Exec(ctx,
		fmt.Sprintf(`UPDATE topics SET status=$1, updated_at=$2 WHERE %s AND key=$%d`, whereClause, next),
		args...,
	)
	return err
}
