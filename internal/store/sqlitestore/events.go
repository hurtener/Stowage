package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type eventStore struct{ s *sqliteStore }

func (e *eventStore) Emit(ctx context.Context, scope identity.Scope, ev store.Event) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	return e.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if ev.CreatedAt == 0 {
			ev.CreatedAt = now
		}
		if ev.Payload == "" {
			ev.Payload = "{}"
		}
		_, err := tx.Exec(`
			INSERT INTO events
				(id, tenant_id, project_id, user_id, session_id,
				 type, subject_id, reason, payload, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			ev.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
			ev.Type, ev.SubjectID, ev.Reason, ev.Payload, ev.CreatedAt,
		)
		return err
	})
}

// List returns events ordered by (created_at, id) ASC.
// cursor is an opaque "<millis>:<id>" pagination token (Q1 composite cursor).
func (e *eventStore) List(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]store.Event, string, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, "", err
	}
	if cursor != "" {
		ts, cid, perr := parseCursor(cursor)
		if perr != nil {
			return nil, "", perr
		}
		whereClause += " AND (created_at > ? OR (created_at = ? AND id > ?))"
		args = append(args, ts, ts, cid)
	}
	args = append(args, limit+1)

	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), type, subject_id, reason, payload, created_at FROM events WHERE ` + whereClause + ` ORDER BY created_at ASC, id ASC LIMIT ?` //nolint:gosec // whereClause is built from controlled helper, not user input
	rows, err := e.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlitestore: list events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.Event
	for rows.Next() {
		var ev store.Event
		if err := rows.Scan(
			&ev.ID, &ev.TenantID, &ev.ProjectID, &ev.UserID, &ev.SessionID,
			&ev.Type, &ev.SubjectID, &ev.Reason, &ev.Payload, &ev.CreatedAt,
		); err != nil {
			return nil, "", err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var nextCursor string
	if len(out) > limit {
		nextCursor = encodeCursor(out[limit-1].CreatedAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, nextCursor, nil
}
