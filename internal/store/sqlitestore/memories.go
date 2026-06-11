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

type memoryStore struct{ s *sqliteStore }

func (m *memoryStore) Insert(ctx context.Context, scope identity.Scope, mem store.Memory) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if mem.CreatedAt == 0 {
			mem.CreatedAt = now
		}
		if mem.UpdatedAt == 0 {
			mem.UpdatedAt = now
		}
		_, err := tx.Exec(`
			INSERT INTO memories
				(id, tenant_id, project_id, user_id, session_id, kind, content, context, status,
				 importance, confidence, trust_source,
				 match_count, inject_count, use_count, save_count, fail_count, noise_count,
				 stability, last_accessed_at, valid_from, valid_until,
				 episode_id, supersedes_id, superseded_by_id, privacy_zone,
				 created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			mem.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
			mem.Kind, mem.Content, mem.Context, mem.Status,
			mem.Importance, mem.Confidence, mem.TrustSource,
			mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
			mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
			mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
			mem.CreatedAt, mem.UpdatedAt,
		)
		return err
	})
}

func (m *memoryStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Memory, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, id)
	qg := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), kind, content, context, status, importance, confidence, trust_source, match_count, inject_count, use_count, save_count, fail_count, noise_count, stability, last_accessed_at, valid_from, valid_until, episode_id, supersedes_id, superseded_by_id, privacy_zone, created_at, updated_at FROM memories WHERE ` + whereClause + ` AND id = ?` //nolint:gosec // whereClause is built from controlled helper, not user input
	row := m.s.rdb.QueryRowContext(ctx, qg, args...)
	mem, err := scanMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return mem, err
}

func (m *memoryStore) Update(ctx context.Context, scope identity.Scope, mem store.Memory) error {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return err
	}
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if mem.UpdatedAt == 0 {
			mem.UpdatedAt = now
		}
		queryArgs := []interface{}{
			mem.Kind, mem.Content, mem.Context, mem.Status,
			mem.Importance, mem.Confidence, mem.TrustSource,
			mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
			mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
			mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
			mem.UpdatedAt,
		}
		queryArgs = append(queryArgs, args...)
		queryArgs = append(queryArgs, mem.ID)
		qu := `UPDATE memories SET kind=?, content=?, context=?, status=?, importance=?, confidence=?, trust_source=?, match_count=?, inject_count=?, use_count=?, save_count=?, fail_count=?, noise_count=?, stability=?, last_accessed_at=?, valid_from=?, valid_until=?, episode_id=?, supersedes_id=?, superseded_by_id=?, privacy_zone=?, updated_at=? WHERE ` + whereClause + ` AND id=?` //nolint:gosec // whereClause is built from controlled helper, not user input
		_, err := tx.Exec(qu, queryArgs...)
		return err
	})
}

func (m *memoryStore) SetStatus(ctx context.Context, scope identity.Scope, id string, status string, updatedAt int64) error {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return err
	}
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		queryArgs := []interface{}{status, updatedAt}
		queryArgs = append(queryArgs, args...)
		queryArgs = append(queryArgs, id)
		qs := `UPDATE memories SET status=?, updated_at=? WHERE ` + whereClause + ` AND id=?` //nolint:gosec // whereClause is built from controlled helper, not user input
		_, err := tx.Exec(qs, queryArgs...)
		return err
	})
}

// ListByStatus returns memories ordered by (created_at, id) ASC.
// cursor is an opaque "<millis>:<id>" pagination token (Q1 composite cursor).
func (m *memoryStore) ListByStatus(ctx context.Context, scope identity.Scope, status string, limit int, cursor string) ([]store.Memory, string, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, "", err
	}
	whereClause += " AND status = ?"
	args = append(args, status)

	if cursor != "" {
		ts, cid, perr := parseCursor(cursor)
		if perr != nil {
			return nil, "", perr
		}
		whereClause += " AND (created_at > ? OR (created_at = ? AND id > ?))"
		args = append(args, ts, ts, cid)
	}
	args = append(args, limit+1)

	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), kind, content, context, status, importance, confidence, trust_source, match_count, inject_count, use_count, save_count, fail_count, noise_count, stability, last_accessed_at, valid_from, valid_until, episode_id, supersedes_id, superseded_by_id, privacy_zone, created_at, updated_at FROM memories WHERE ` + whereClause + ` ORDER BY created_at ASC, id ASC LIMIT ?` //nolint:gosec // whereClause is built from controlled helper, not user input
	rows, err := m.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlitestore: list memories by status: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.Memory
	for rows.Next() {
		mem, err := scanMemory(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *mem)
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

func (m *memoryStore) InsertLinks(ctx context.Context, scope identity.Scope, links []store.Link) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	if len(links) == 0 {
		return nil
	}
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		for _, l := range links {
			now := time.Now().UnixMilli()
			if l.CreatedAt == 0 {
				l.CreatedAt = now
			}
			_, err := tx.Exec(`
				INSERT OR IGNORE INTO links
					(id, tenant_id, from_memory, to_memory, type, source, confidence, created_at)
				VALUES (?,?,?,?,?,?,?,?)`,
				l.ID, scope.Tenant, l.FromMemory, l.ToMemory, l.Type, l.Source, l.Confidence, l.CreatedAt,
			)
			if err != nil {
				return fmt.Errorf("sqlitestore: insert link %q: %w", l.ID, err)
			}
		}
		return nil
	})
}

// ListLinks returns edges matching fromMemoryID or toMemoryID within the tenant.
//
// Note: links are scoped by tenant_id only (the links table has no project_id,
// user_id or session_id columns). This is by design — links connect memories
// that may span users/projects within a tenant. scope.Project, scope.User and
// scope.Session are intentionally ignored here.
func (m *memoryStore) ListLinks(ctx context.Context, scope identity.Scope, fromMemoryID, toMemoryID string) ([]store.Link, error) {
	if scope.Tenant == "" { // S1: fail closed
		return nil, store.ErrScopeRequired
	}
	clause := "tenant_id = ?"
	args := []interface{}{scope.Tenant}
	if fromMemoryID != "" {
		clause += " AND from_memory = ?"
		args = append(args, fromMemoryID)
	}
	if toMemoryID != "" {
		clause += " AND to_memory = ?"
		args = append(args, toMemoryID)
	}

	ql := `SELECT id, tenant_id, from_memory, to_memory, type, source, confidence, created_at FROM links WHERE ` + clause + ` ORDER BY created_at ASC` //nolint:gosec // clause is built from controlled params, not user input
	rows, err := m.s.rdb.QueryContext(ctx, ql, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list links: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.Link
	for rows.Next() {
		var l store.Link
		if err := rows.Scan(&l.ID, &l.TenantID, &l.FromMemory, &l.ToMemory, &l.Type, &l.Source, &l.Confidence, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (m *memoryStore) AddProvenance(ctx context.Context, scope identity.Scope, rows []store.Provenance) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	if len(rows) == 0 {
		return nil
	}
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		for _, p := range rows {
			now := time.Now().UnixMilli()
			if p.CreatedAt == 0 {
				p.CreatedAt = now
			}
			_, err := tx.Exec(`
				INSERT OR IGNORE INTO provenance
					(id, memory_id, record_id, span_start, span_end, tenant_id, created_at)
				VALUES (?,?,?,?,?,?,?)`,
				p.ID, p.MemoryID, p.RecordID, p.SpanStart, p.SpanEnd, scope.Tenant, p.CreatedAt,
			)
			if err != nil {
				return fmt.Errorf("sqlitestore: add provenance %q: %w", p.ID, err)
			}
		}
		return nil
	})
}

func scanMemory(row rowScanner) (*store.Memory, error) {
	var mem store.Memory
	err := row.Scan(
		&mem.ID, &mem.TenantID, &mem.ProjectID, &mem.UserID, &mem.SessionID,
		&mem.Kind, &mem.Content, &mem.Context, &mem.Status,
		&mem.Importance, &mem.Confidence, &mem.TrustSource,
		&mem.MatchCount, &mem.InjectCount, &mem.UseCount, &mem.SaveCount, &mem.FailCount, &mem.NoiseCount,
		&mem.Stability, &mem.LastAccessedAt, &mem.ValidFrom, &mem.ValidUntil,
		&mem.EpisodeID, &mem.SupersedesID, &mem.SupersededByID, &mem.PrivacyZone,
		&mem.CreatedAt, &mem.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &mem, nil
}
