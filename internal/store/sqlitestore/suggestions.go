package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type suggestionStore struct{ s *sqliteStore }

const suggestionCols = `id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
	trigger_kind, memory_id, episode_id, status, accept_count, dismiss_count, created_at, updated_at`

func (ss *suggestionStore) Create(ctx context.Context, scope identity.Scope, sugs []store.Suggestion) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	if len(sugs) == 0 {
		return nil
	}
	return ss.s.exec(ctx, func(tx *sql.Tx) error {
		for _, g := range sugs {
			status := g.Status
			if status == "" {
				status = "pending"
			}
			if _, err := tx.Exec(`
				INSERT OR IGNORE INTO suggestions
					(id, tenant_id, project_id, user_id, session_id, trigger_kind, memory_id, episode_id, status, accept_count, dismiss_count, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				g.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(g.SessionID),
				g.TriggerKind, g.MemoryID, g.EpisodeID, status, g.AcceptCount, g.DismissCount, g.CreatedAt, g.UpdatedAt,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (ss *suggestionStore) ListBySession(ctx context.Context, scope identity.Scope, sessionID, status string, limit int) ([]store.Suggestion, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	whereClause += " AND session_id = ?"
	args = append(args, sessionID)
	if status != "" {
		whereClause += " AND status = ?"
		args = append(args, status)
	}
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	q := `SELECT ` + suggestionCols + ` FROM suggestions WHERE ` + whereClause + ` ORDER BY created_at DESC, id DESC LIMIT ?` //nolint:gosec
	rows, err := ss.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list suggestions: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	return scanSuggestions(rows)
}

func (ss *suggestionStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Suggestion, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	row := ss.s.rdb.QueryRowContext(ctx,
		`SELECT `+suggestionCols+` FROM suggestions WHERE tenant_id = ? AND id = ?`,
		scope.Tenant, id,
	)
	g, err := scanSuggestion(row)
	if errors.Is(err, sql.ErrNoRows) {
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
		return nil, fmt.Errorf("sqlitestore: resolve suggestion: invalid action %q (want accept|dismiss)", action)
	}
	var resolved *store.Suggestion
	err := ss.s.exec(ctx, func(tx *sql.Tx) error {
		// CAS: only a pending row transitions (no double-resolve race, D-085 lesson).
		// col is one of two hardcoded column names from the action switch above —
		// never user input (gosec G202 false positive).
		q := `UPDATE suggestions SET status = ?, ` + col + ` = ` + col + ` + 1, updated_at = ? WHERE tenant_id = ? AND id = ? AND status = 'pending'` //nolint:gosec
		res, err := tx.Exec(q, status, now, scope.Tenant, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotPending
		}
		row := tx.QueryRow(`SELECT `+suggestionCols+` FROM suggestions WHERE tenant_id = ? AND id = ?`, scope.Tenant, id)
		resolved, err = scanSuggestion(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

func (ss *suggestionStore) CountByTrigger(ctx context.Context, scope identity.Scope, triggerKind string) (accepted, dismissed int, err error) {
	whereClause, args, werr := buildScopeWhere(scope)
	if werr != nil {
		return 0, 0, werr
	}
	whereClause += " AND trigger_kind = ?"
	args = append(args, triggerKind)
	q := `SELECT
			COALESCE(SUM(CASE WHEN status='accepted' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status='dismissed' THEN 1 ELSE 0 END),0)
		  FROM suggestions WHERE ` + whereClause //nolint:gosec
	err = ss.s.rdb.QueryRowContext(ctx, q, args...).Scan(&accepted, &dismissed)
	return accepted, dismissed, err
}

func (ss *suggestionStore) ListPendingBefore(ctx context.Context, scope identity.Scope, before int64, limit int) ([]store.Suggestion, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	whereClause += " AND status = 'pending' AND created_at < ?"
	args = append(args, before)
	if limit <= 0 {
		limit = 200
	}
	args = append(args, limit)
	q := `SELECT ` + suggestionCols + ` FROM suggestions WHERE ` + whereClause + ` ORDER BY created_at ASC LIMIT ?` //nolint:gosec
	rows, err := ss.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list pending suggestions: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	return scanSuggestions(rows)
}

func (ss *suggestionStore) ExpirePending(ctx context.Context, scope identity.Scope, ids []string, now int64) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	if len(ids) == 0 {
		return nil
	}
	return ss.s.exec(ctx, func(tx *sql.Tx) error {
		ph := make([]string, len(ids))
		args := make([]interface{}, 0, len(ids)+2)
		args = append(args, now, scope.Tenant)
		for i, id := range ids {
			ph[i] = "?"
			args = append(args, id)
		}
		// The interpolated fragment is only "?" placeholders (one per id); the ids
		// themselves are bound parameters (gosec G202 false positive).
		q := `UPDATE suggestions SET status='expired', updated_at=? WHERE tenant_id=? AND status='pending' AND id IN (` + strings.Join(ph, ",") + `)` //nolint:gosec
		_, err := tx.Exec(q, args...)
		return err
	})
}

type suggestionScanner interface {
	Scan(dest ...interface{}) error
}

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

func scanSuggestions(rows *sql.Rows) ([]store.Suggestion, error) {
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
