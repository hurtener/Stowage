package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type suggestionStore struct{ s *pgStore }

const suggestionCols = `id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
	trigger_kind, memory_id, episode_id, status, accept_count, dismiss_count, created_at, updated_at`

func (ss *suggestionStore) Create(ctx context.Context, scope identity.Scope, sugs []store.Suggestion) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	if len(sugs) == 0 {
		return nil
	}
	tx, err := ss.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, g := range sugs {
		status := g.Status
		if status == "" {
			status = "pending"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO suggestions
				(id, tenant_id, project_id, user_id, session_id, trigger_kind, memory_id, episode_id, status, accept_count, dismiss_count, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (id) DO NOTHING`,
			g.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(g.SessionID),
			g.TriggerKind, g.MemoryID, g.EpisodeID, status, g.AcceptCount, g.DismissCount, g.CreatedAt, g.UpdatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (ss *suggestionStore) ListBySession(ctx context.Context, scope identity.Scope, sessionID, status string, limit int) ([]store.Suggestion, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	whereClause += fmt.Sprintf(" AND session_id = $%d", next)
	args = append(args, sessionID)
	next++
	if status != "" {
		whereClause += fmt.Sprintf(" AND status = $%d", next)
		args = append(args, status)
		next++
	}
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + suggestionCols + ` FROM suggestions WHERE ` + whereClause + fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, next)
	args = append(args, limit)
	rows, err := ss.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list suggestions: %w", err)
	}
	defer rows.Close()
	return scanSuggestions(rows)
}

func (ss *suggestionStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Suggestion, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	row := ss.s.pool.QueryRow(ctx,
		`SELECT `+suggestionCols+` FROM suggestions WHERE tenant_id = $1 AND id = $2`,
		scope.Tenant, id,
	)
	g, err := scanSuggestion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return g, err
}

func (ss *suggestionStore) Resolve(ctx context.Context, scope identity.Scope, id, action string, now int64) (*store.Suggestion, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	var col, status string
	switch action {
	case "accept":
		col, status = "accept_count", "accepted"
	case "dismiss":
		col, status = "dismiss_count", "dismissed"
	default:
		return nil, fmt.Errorf("pgstore: resolve suggestion: invalid action %q", action)
	}
	tx, err := ss.s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, //nolint:gosec
		`UPDATE suggestions SET status = $1, `+col+` = `+col+` + 1, updated_at = $2 WHERE tenant_id = $3 AND id = $4 AND status = 'pending'`,
		status, now, scope.Tenant, id,
	)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, store.ErrNotPending
	}
	row := tx.QueryRow(ctx, `SELECT `+suggestionCols+` FROM suggestions WHERE tenant_id = $1 AND id = $2`, scope.Tenant, id)
	g, err := scanSuggestion(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return g, nil
}

func (ss *suggestionStore) CountByTrigger(ctx context.Context, scope identity.Scope, triggerKind string) (int, int, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return 0, 0, err
	}
	whereClause += fmt.Sprintf(" AND trigger_kind = $%d", next)
	args = append(args, triggerKind)
	q := `SELECT
			COALESCE(SUM(CASE WHEN status='accepted' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status='dismissed' THEN 1 ELSE 0 END),0)
		  FROM suggestions WHERE ` + whereClause
	var accepted, dismissed int
	err = ss.s.pool.QueryRow(ctx, q, args...).Scan(&accepted, &dismissed)
	return accepted, dismissed, err
}

func (ss *suggestionStore) ListPendingBefore(ctx context.Context, scope identity.Scope, before int64, limit int) ([]store.Suggestion, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	whereClause += fmt.Sprintf(" AND status = 'pending' AND created_at < $%d", next)
	args = append(args, before)
	next++
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT ` + suggestionCols + ` FROM suggestions WHERE ` + whereClause + fmt.Sprintf(` ORDER BY created_at ASC LIMIT $%d`, next)
	args = append(args, limit)
	rows, err := ss.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list pending suggestions: %w", err)
	}
	defer rows.Close()
	return scanSuggestions(rows)
}

func (ss *suggestionStore) ExpirePending(ctx context.Context, scope identity.Scope, ids []string, now int64) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	if len(ids) == 0 {
		return nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+2)
	args = append(args, now, scope.Tenant)
	for i, id := range ids {
		ph[i] = fmt.Sprintf("$%d", i+3)
		args = append(args, id)
	}
	_, err := ss.s.pool.Exec(ctx, //nolint:gosec
		`UPDATE suggestions SET status='expired', updated_at=$1 WHERE tenant_id=$2 AND status='pending' AND id IN (`+strings.Join(ph, ",")+`)`,
		args...,
	)
	return err
}

type suggestionScanner interface{ Scan(dest ...any) error }

func scanSuggestion(s suggestionScanner) (*store.Suggestion, error) {
	var g store.Suggestion
	err := s.Scan(
		&g.ID, &g.TenantID, &g.ProjectID, &g.UserID, &g.SessionID,
		&g.TriggerKind, &g.MemoryID, &g.EpisodeID, &g.Status, &g.AcceptCount, &g.DismissCount, &g.CreatedAt, &g.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func scanSuggestions(rows pgx.Rows) ([]store.Suggestion, error) {
	out := make([]store.Suggestion, 0)
	for rows.Next() {
		g, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}
