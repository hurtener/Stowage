package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type episodeStore struct{ s *sqliteStore }

const episodeCols = `id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), title, status, started_at, ended_at, narrative_memory_id, outcome, created_at, updated_at`

func (e *episodeStore) CreateEpisode(ctx context.Context, scope identity.Scope, ep store.Episode) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return e.s.exec(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO episodes
				(id, tenant_id, project_id, user_id, session_id, title, status,
				 started_at, ended_at, narrative_memory_id, outcome, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ep.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(ep.SessionID),
			ep.Title, ep.Status, ep.StartedAt, ep.EndedAt, ep.NarrativeMemoryID, ep.Outcome,
			ep.CreatedAt, ep.UpdatedAt,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: create episode %q: %w", ep.ID, err)
		}
		return nil
	})
}

func (e *episodeStore) GetEpisode(ctx context.Context, scope identity.Scope, id string) (*store.Episode, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, id)
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE ` + whereClause + ` AND id = ?` //nolint:gosec
	return scanEpisode(e.s.rdb.QueryRowContext(ctx, q, args...))
}

func (e *episodeStore) GetEpisodeBySession(ctx context.Context, scope identity.Scope, sessionID string) (*store.Episode, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, sessionID)
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE ` + whereClause + ` AND session_id = ? ORDER BY started_at ASC LIMIT 1` //nolint:gosec
	return scanEpisode(e.s.rdb.QueryRowContext(ctx, q, args...))
}

func (e *episodeStore) ListEpisodesNeedingNarrative(ctx context.Context, limit int) ([]store.Episode, error) {
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE narrative_memory_id = '' ORDER BY started_at ASC LIMIT ?`
	rows, err := e.s.rdb.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list episodes needing narrative: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEpisodes(rows)
}

func (e *episodeStore) SetEpisodeNarrative(ctx context.Context, scope identity.Scope, episodeID, narrativeMemoryID, title string, updatedAt int64) error {
	whereClause, whereArgs, err := buildScopeWhere(scope)
	if err != nil {
		return err
	}
	args := []interface{}{narrativeMemoryID, title, updatedAt}
	args = append(args, whereArgs...)
	args = append(args, episodeID)
	return e.s.exec(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.Exec(
			`UPDATE episodes SET narrative_memory_id = ?, title = ?, updated_at = ? WHERE `+whereClause+` AND id = ?`, //nolint:gosec
			args...,
		)
		if execErr != nil {
			return fmt.Errorf("sqlitestore: set episode narrative %q: %w", episodeID, execErr)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (e *episodeStore) ListEpisodes(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]store.Episode, string, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, "", err
	}
	if cursor != "" {
		ts, cid, perr := parseCursor(cursor)
		if perr != nil {
			return nil, "", perr
		}
		whereClause += " AND (started_at < ? OR (started_at = ? AND id < ?))"
		args = append(args, ts, ts, cid)
	}
	args = append(args, limit+1)
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE ` + whereClause + ` ORDER BY started_at DESC, id DESC LIMIT ?` //nolint:gosec
	rows, err := e.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlitestore: list episodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	eps, err := collectEpisodes(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(eps) > limit {
		last := eps[limit-1]
		next = encodeCursor(last.StartedAt, last.ID)
		eps = eps[:limit]
	}
	return eps, next, nil
}

func scanEpisode(row rowScanner) (*store.Episode, error) {
	var ep store.Episode
	err := row.Scan(
		&ep.ID, &ep.TenantID, &ep.ProjectID, &ep.UserID, &ep.SessionID,
		&ep.Title, &ep.Status, &ep.StartedAt, &ep.EndedAt, &ep.NarrativeMemoryID,
		&ep.Outcome, &ep.CreatedAt, &ep.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: scan episode: %w", err)
	}
	return &ep, nil
}

func collectEpisodes(rows *sql.Rows) ([]store.Episode, error) {
	var out []store.Episode
	for rows.Next() {
		ep, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ep)
	}
	return out, rows.Err()
}
