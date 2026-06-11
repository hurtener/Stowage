package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type eventStore struct{ s *pgStore }

func (e *eventStore) Emit(ctx context.Context, scope identity.Scope, ev store.Event) error {
	now := time.Now().UnixMilli()
	if ev.CreatedAt == 0 {
		ev.CreatedAt = now
	}
	if ev.Payload == "" {
		ev.Payload = "{}"
	}
	_, err := e.s.pool.Exec(ctx, `
		INSERT INTO events
			(id, tenant_id, project_id, user_id, session_id,
			 type, subject_id, reason, payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		ev.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
		ev.Type, ev.SubjectID, ev.Reason, ev.Payload, ev.CreatedAt,
	)
	return err
}

func (e *eventStore) List(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]store.Event, string, error) {
	whereClause, args, next := buildScopeWhere(scope, 1)
	if cursor != "" {
		whereClause += fmt.Sprintf(` AND created_at > (SELECT created_at FROM events WHERE id = $%d)`, next)
		args = append(args, cursor)
		next++
	}
	args = append(args, limit+1)

	rows, err := e.s.pool.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        type, subject_id, reason, payload, created_at
		 FROM events WHERE `+whereClause+fmt.Sprintf(` ORDER BY created_at ASC LIMIT $%d`, next),
		args...,
	)
	if err != nil {
		return nil, "", fmt.Errorf("pgstore: list events: %w", err)
	}
	defer rows.Close()

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
		nextCursor = out[limit].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}
