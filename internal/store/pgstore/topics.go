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
//
// Uses a single atomic INSERT ... ON CONFLICT ... DO UPDATE (M2).
// The UNIQUE NULLS NOT DISTINCT constraint on topics ensures that NULL-valued
// scope fields (project_id, user_id, session_id) are treated as equal for
// conflict detection, making the single-statement upsert TOCTOU-free.
//
// SQLite uses UPDATE-then-INSERT serialized through its single writer goroutine
// instead (see sqlitestore/topics.go) — the NULLS NOT DISTINCT syntax is only
// available in SQLite 3.45+ and is not used in the modernc pure-Go driver.
func (t *topicStore) Upsert(ctx context.Context, scope identity.Scope, topic store.Topic) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	now := time.Now().UnixMilli()
	if topic.CreatedAt == 0 {
		topic.CreatedAt = now
	}
	if topic.UpdatedAt == 0 {
		topic.UpdatedAt = now
	}
	pj := nullStr(scope.Project)
	us := nullStr(scope.User)
	se := nullStr(scope.Session)
	_, err := t.s.pool.Exec(ctx, `
		INSERT INTO topics
			(id, tenant_id, project_id, user_id, session_id,
			 key, description, status, pack, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (tenant_id, project_id, user_id, session_id, key)
		DO UPDATE SET
			description = EXCLUDED.description,
			status      = EXCLUDED.status,
			pack        = EXCLUDED.pack,
			updated_at  = EXCLUDED.updated_at`,
		topic.ID, scope.Tenant, pj, us, se,
		topic.Key, topic.Description, topic.Status, topic.Pack,
		topic.CreatedAt, topic.UpdatedAt,
	)
	return err
}

func (t *topicStore) Get(ctx context.Context, scope identity.Scope, key string) (*store.Topic, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	args = append(args, key)
	row := t.s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        key, description, status, pack, created_at, updated_at
		 FROM topics WHERE `+whereClause+fmt.Sprintf(` AND key = $%d AND status != 'deleted'`, next),
		args...,
	)
	var topic store.Topic
	err = row.Scan(
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
	whereClause, args, _, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
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
	whereClause, whereArgs, next, err := buildScopeWhere(scope, 3)
	if err != nil {
		return err
	}
	args := []interface{}{
		"deleted",
		time.Now().UnixMilli(),
	}
	args = append(args, whereArgs...)
	args = append(args, key)
	tag, err := t.s.pool.Exec(ctx,
		fmt.Sprintf(`UPDATE topics SET status=$1, updated_at=$2 WHERE %s AND key=$%d`, whereClause, next),
		args...,
	)
	if err != nil {
		return err
	}
	// Driver parity with sqlitestore: deleting a missing topic is ErrNotFound.
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
