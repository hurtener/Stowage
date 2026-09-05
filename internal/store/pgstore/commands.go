package pgstore

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

func (e *eventStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Event, error) {
	where, args, next, err := buildExactScopeWhere(scope, 1)
	if err != nil { return nil, err }
	args = append(args, id)
	var ev store.Event
	err = e.s.pool.QueryRow(ctx, `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), type, subject_id, reason, payload, created_at FROM events WHERE `+where+fmt.Sprintf(` AND id = $%d`, next), args...).Scan(&ev.ID, &ev.TenantID, &ev.ProjectID, &ev.UserID, &ev.SessionID, &ev.Type, &ev.SubjectID, &ev.Reason, &ev.Payload, &ev.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) { return nil, store.ErrNotFound }
	if err != nil { return nil, fmt.Errorf("pgstore: get event: %w", err) }
	return &ev, nil
}

func guardCommandPG(ctx context.Context, tx pgx.Tx, scope identity.Scope, g *store.CommandGuard, now int64) error {
	if g == nil { return nil }
	if g.Receipt.ID == "" || g.Source.ID == "" { return store.ErrCommandConflict }
	if err := insertEventPG(ctx, tx, scope, g.Receipt, now); err != nil {
		if pgIsUnique(err) { return store.ErrCommandReplay }
		return fmt.Errorf("pgstore: reserve command receipt: %w", err)
	}
	ss := identity.Scope{Tenant: scope.Tenant, Project: g.Source.ProjectID, User: g.Source.UserID, Session: g.Source.SessionID}
	if ss.Project != scope.Project || ss.User != scope.User || g.Source.TenantID != scope.Tenant { return store.ErrCommandConflict }
	where, args, next, err := buildExactScopeWhere(ss, 1)
	if err != nil { return err }
	args = append(args, g.Source.ID)
	var role, content string
	var occurred int64
	err = tx.QueryRow(ctx, `SELECT role, content, occurred_at FROM records WHERE `+where+fmt.Sprintf(` AND id = $%d FOR SHARE`, next), args...).Scan(&role, &content, &occurred)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (role != "user" || role != g.Source.Role || content != g.Source.Content || occurred != g.Source.OccurredAt)) { return store.ErrCommandConflict }
	if err != nil { return fmt.Errorf("pgstore: guard source: %w", err) }
	for _, target := range g.Targets {
		if target.TenantID != scope.Tenant || target.ProjectID != scope.Project || target.UserID != scope.User { return store.ErrCommandConflict }
		ts := identity.Scope{Tenant: scope.Tenant, Project: target.ProjectID, User: target.UserID, Session: target.SessionID}
		where, args, next, err := buildExactScopeWhere(ts, 1)
		if err != nil { return err }
		args = append(args, target.ID)
		actual, err := scanMemory(tx.QueryRow(ctx, `SELECT `+memorySelectCols+` FROM memories WHERE `+where+fmt.Sprintf(` AND id = $%d FOR UPDATE`, next), args...))
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && store.MemoryRevision(*actual) != store.MemoryRevision(target)) { return store.ErrCommandConflict }
		if err != nil { return fmt.Errorf("pgstore: guard target: %w", err) }
	}
	return nil
}
