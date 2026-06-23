package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type topicStore struct{ s *sqliteStore }

// Upsert inserts or updates a topic keyed by (scope, key).
//
// Uses UPDATE-then-INSERT inside a single serialized writer-goroutine transaction.
// This is safe on SQLite because the single writer goroutine serializes all
// mutations, making the two-step UPDATE+INSERT atomic from callers' perspective.
// PostgreSQL uses a single INSERT ... ON CONFLICT ... DO UPDATE instead
// (see pgstore/topics.go) because it cannot rely on serialization for isolation.
func (t *topicStore) Upsert(ctx context.Context, scope identity.Scope, topic store.Topic) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	return t.s.exec(ctx, func(tx *sql.Tx) error {
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

		// Try UPDATE first (handles existing row regardless of id).
		res, err := tx.Exec(`
			UPDATE topics SET description=?, status=?, pack=?, updated_at=?
			WHERE tenant_id=?
			  AND (project_id IS ?)
			  AND (user_id    IS ?)
			  AND (session_id IS ?)
			  AND key=?`,
			topic.Description, topic.Status, topic.Pack, topic.UpdatedAt,
			scope.Tenant, pj, us, se, topic.Key,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: upsert topic update: %w", err)
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			return nil
		}

		// INSERT new row.
		_, err = tx.Exec(`
			INSERT INTO topics
				(id, tenant_id, project_id, user_id, session_id, key, description, status, pack, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			topic.ID, scope.Tenant, pj, us, se,
			topic.Key, topic.Description, topic.Status, topic.Pack,
			topic.CreatedAt, topic.UpdatedAt,
		)
		return err
	})
}

func (t *topicStore) Get(ctx context.Context, scope identity.Scope, key string) (*store.Topic, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, key)
	qtg := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), key, description, status, pack, created_at, updated_at FROM topics WHERE ` + whereClause + ` AND key = ? AND status != 'deleted'` //nolint:gosec // whereClause is built from controlled helper, not user input
	row := t.s.rdb.QueryRowContext(ctx, qtg, args...)
	var topic store.Topic
	err = row.Scan(
		&topic.ID, &topic.TenantID, &topic.ProjectID, &topic.UserID, &topic.SessionID,
		&topic.Key, &topic.Description, &topic.Status, &topic.Pack,
		&topic.CreatedAt, &topic.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &topic, nil
}

func (t *topicStore) List(ctx context.Context, scope identity.Scope) ([]store.Topic, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	// (created_at, key) — the key tiebreak makes List order deterministic even when
	// a batch upsert stamps several topics with the same created_at, which the topic
	// composition cap (D-099) relies on for a reproducible drop set.
	qt := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), key, description, status, pack, created_at, updated_at FROM topics WHERE ` + whereClause + ` AND status != 'deleted' ORDER BY created_at ASC, key ASC` //nolint:gosec // whereClause is built from controlled helper, not user input
	rows, err := t.s.rdb.QueryContext(ctx, qt, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list topics: %w", err)
	}
	defer func() { _ = rows.Close() }()

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
	whereClause, whereArgs, err := buildScopeWhere(scope)
	if err != nil {
		return err
	}
	return t.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		queryArgs := []interface{}{now}
		queryArgs = append(queryArgs, whereArgs...)
		queryArgs = append(queryArgs, key)
		qd := `UPDATE topics SET status='deleted', updated_at=? WHERE ` + whereClause + ` AND key=? AND status != 'deleted'` //nolint:gosec // whereClause is built from controlled helper, not user input
		res, execErr := tx.Exec(qd, queryArgs...)
		if execErr != nil {
			return execErr
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}
