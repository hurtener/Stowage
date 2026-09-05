package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

func (e *eventStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Event, error) {
	where, args, err := buildExactScopeWhere(scope)
	if err != nil { return nil, err }
	args = append(args, id)
	var ev store.Event
	err = e.s.rdb.QueryRowContext(ctx, `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), type, subject_id, reason, payload, created_at FROM events WHERE `+where+` AND id = ?`, args...).Scan(&ev.ID, &ev.TenantID, &ev.ProjectID, &ev.UserID, &ev.SessionID, &ev.Type, &ev.SubjectID, &ev.Reason, &ev.Payload, &ev.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) { return nil, store.ErrNotFound }
	if err != nil { return nil, fmt.Errorf("sqlitestore: get event: %w", err) }
	return &ev, nil
}

func guardCommandSQLite(tx *sql.Tx, scope identity.Scope, g *store.CommandGuard, now int64) error {
	if g == nil { return nil }
	if g.Receipt.ID == "" || g.Source.ID == "" { return store.ErrCommandConflict }
	if err := insertEventSQLite(tx, scope, g.Receipt, now); err != nil {
		if sqliteIsUnique(err) { return store.ErrCommandReplay }
		return fmt.Errorf("sqlitestore: reserve command receipt: %w", err)
	}
	ss := identity.Scope{Tenant: scope.Tenant, Project: g.Source.ProjectID, User: g.Source.UserID, Session: g.Source.SessionID}
	if ss.Project != scope.Project || ss.User != scope.User || g.Source.TenantID != scope.Tenant { return store.ErrCommandConflict }
	where, args, err := buildExactScopeWhere(ss)
	if err != nil { return err }
	args = append(args, g.Source.ID)
	var role, content string
	var occurred int64
	err = tx.QueryRow(`SELECT role, content, occurred_at FROM records WHERE `+where+` AND id = ?`, args...).Scan(&role, &content, &occurred)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (role != "user" || role != g.Source.Role || content != g.Source.Content || occurred != g.Source.OccurredAt)) { return store.ErrCommandConflict }
	if err != nil { return fmt.Errorf("sqlitestore: guard source: %w", err) }
	for _, target := range g.Targets {
		if target.TenantID != scope.Tenant || target.ProjectID != scope.Project || target.UserID != scope.User { return store.ErrCommandConflict }
		ts := identity.Scope{Tenant: scope.Tenant, Project: target.ProjectID, User: target.UserID, Session: target.SessionID}
		where, args, err := buildExactScopeWhere(ts)
		if err != nil { return err }
		args = append(args, target.ID)
		actual, err := scanMemory(tx.QueryRow(`SELECT `+memorySelectCols+` FROM memories WHERE `+where+` AND id = ?`, args...))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && store.MemoryRevision(*actual) != store.MemoryRevision(target)) { return store.ErrCommandConflict }
		if err != nil { return fmt.Errorf("sqlitestore: guard target: %w", err) }
	}
	return nil
}
