package pgstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type episodeStore struct{ s *pgStore }

const episodeCols = `id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), title, status, started_at, ended_at, narrative_memory_id, outcome, created_at, updated_at`

func (e *episodeStore) CreateEpisode(ctx context.Context, scope identity.Scope, ep store.Episode) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	_, err := e.s.pool.Exec(ctx, `
		INSERT INTO episodes
			(id, tenant_id, project_id, user_id, session_id, title, status,
			 started_at, ended_at, narrative_memory_id, outcome, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		ep.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(ep.SessionID),
		ep.Title, ep.Status, ep.StartedAt, ep.EndedAt, ep.NarrativeMemoryID, ep.Outcome,
		ep.CreatedAt, ep.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("pgstore: create episode %q: %w", ep.ID, err)
	}
	return nil
}

func (e *episodeStore) GetEpisode(ctx context.Context, scope identity.Scope, id string) (*store.Episode, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	args = append(args, id)
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE ` + whereClause + fmt.Sprintf(` AND id = $%d`, next) //nolint:gosec
	return scanEpisodeRow(e.s.pool.QueryRow(ctx, q, args...))
}

func (e *episodeStore) GetEpisodeBySession(ctx context.Context, scope identity.Scope, sessionID string) (*store.Episode, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	args = append(args, sessionID)
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE ` + whereClause + fmt.Sprintf(` AND session_id = $%d ORDER BY started_at ASC LIMIT 1`, next) //nolint:gosec
	return scanEpisodeRow(e.s.pool.QueryRow(ctx, q, args...))
}

func (e *episodeStore) ListEpisodesNeedingNarrative(ctx context.Context, limit int) ([]store.Episode, error) {
	rows, err := e.s.pool.Query(ctx,
		`SELECT `+episodeCols+` FROM episodes WHERE narrative_memory_id = '' ORDER BY started_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list episodes needing narrative: %w", err)
	}
	defer rows.Close()
	return collectEpisodes(rows)
}

func (e *episodeStore) SetEpisodeNarrative(ctx context.Context, scope identity.Scope, episodeID, narrativeMemoryID, title string, updatedAt int64) error {
	whereClause, args, next, err := buildScopeWhere(scope, 4)
	if err != nil {
		return err
	}
	full := []interface{}{narrativeMemoryID, title, updatedAt}
	full = append(full, args...)
	full = append(full, episodeID)
	q := fmt.Sprintf(`UPDATE episodes SET narrative_memory_id = $1, title = $2, updated_at = $3 WHERE %s AND id = $%d`, whereClause, next) //nolint:gosec
	tag, err := e.s.pool.Exec(ctx, q, full...)
	if err != nil {
		return fmt.Errorf("pgstore: set episode narrative %q: %w", episodeID, err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (e *episodeStore) ListEpisodes(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]store.Episode, string, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, "", err
	}
	if cursor != "" {
		ts, cid, perr := parseCursor(cursor)
		if perr != nil {
			return nil, "", perr
		}
		whereClause += fmt.Sprintf(` AND (started_at, id) < ($%d, $%d)`, next, next+1)
		args = append(args, ts, cid)
		next += 2
	}
	args = append(args, limit+1)
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE ` + whereClause + fmt.Sprintf(` ORDER BY started_at DESC, id DESC LIMIT $%d`, next) //nolint:gosec
	rows, err := e.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("pgstore: list episodes: %w", err)
	}
	defer rows.Close()
	eps, err := collectEpisodes(rows)
	if err != nil {
		return nil, "", err
	}
	next2 := ""
	if len(eps) > limit {
		last := eps[limit-1]
		next2 = encodeCursor(last.StartedAt, last.ID)
		eps = eps[:limit]
	}
	return eps, next2, nil
}

type epScanner interface {
	Scan(dest ...any) error
}

func scanEpisodeRow(row epScanner) (*store.Episode, error) {
	var ep store.Episode
	err := row.Scan(
		&ep.ID, &ep.TenantID, &ep.ProjectID, &ep.UserID, &ep.SessionID,
		&ep.Title, &ep.Status, &ep.StartedAt, &ep.EndedAt, &ep.NarrativeMemoryID,
		&ep.Outcome, &ep.CreatedAt, &ep.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: scan episode: %w", err)
	}
	return &ep, nil
}

func collectEpisodes(rows pgx.Rows) ([]store.Episode, error) {
	var out []store.Episode
	for rows.Next() {
		ep, err := scanEpisodeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ep)
	}
	return out, rows.Err()
}
