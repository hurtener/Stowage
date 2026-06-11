package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type memoryStore struct{ s *pgStore }

func (m *memoryStore) Insert(ctx context.Context, scope identity.Scope, mem store.Memory) error {
	now := time.Now().UnixMilli()
	if mem.CreatedAt == 0 {
		mem.CreatedAt = now
	}
	if mem.UpdatedAt == 0 {
		mem.UpdatedAt = now
	}
	_, err := m.s.pool.Exec(ctx, `
		INSERT INTO memories
			(id, tenant_id, project_id, user_id, session_id, kind, content, context, status,
			 importance, confidence, trust_source,
			 match_count, inject_count, use_count, save_count, fail_count, noise_count,
			 stability, last_accessed_at, valid_from, valid_until,
			 episode_id, supersedes_id, superseded_by_id, privacy_zone,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28)`,
		mem.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
		mem.Kind, mem.Content, mem.Context, mem.Status,
		mem.Importance, mem.Confidence, mem.TrustSource,
		mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
		mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
		mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
		mem.CreatedAt, mem.UpdatedAt,
	)
	return err
}

func (m *memoryStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Memory, error) {
	whereClause, args, next := buildScopeWhere(scope, 1)
	args = append(args, id)
	row := m.s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        kind, content, context, status,
		        importance, confidence, trust_source,
		        match_count, inject_count, use_count, save_count, fail_count, noise_count,
		        stability, last_accessed_at, valid_from, valid_until,
		        episode_id, supersedes_id, superseded_by_id, privacy_zone,
		        created_at, updated_at
		 FROM memories WHERE `+whereClause+fmt.Sprintf(` AND id = $%d`, next),
		args...,
	)
	mem, err := scanMemory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return mem, err
}

func (m *memoryStore) Update(ctx context.Context, scope identity.Scope, mem store.Memory) error {
	now := time.Now().UnixMilli()
	if mem.UpdatedAt == 0 {
		mem.UpdatedAt = now
	}
	whereClause, whereArgs, next := buildScopeWhere(scope, 1)
	// Build update args: mem fields first, then scope args, then id.
	args := []interface{}{
		mem.Kind, mem.Content, mem.Context, mem.Status,
		mem.Importance, mem.Confidence, mem.TrustSource,
		mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
		mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
		mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
		mem.UpdatedAt,
	}
	baseIdx := len(args) + 1
	// Rebuild scope where with correct indices.
	whereClause2, scopeArgs, finalNext := buildScopeWhere(scope, baseIdx)
	args = append(args, scopeArgs...)
	args = append(args, mem.ID)
	_ = whereClause
	_ = whereArgs
	_ = next
	_, err := m.s.pool.Exec(ctx,
		fmt.Sprintf(`UPDATE memories SET
			kind=$1, content=$2, context=$3, status=$4,
			importance=$5, confidence=$6, trust_source=$7,
			match_count=$8, inject_count=$9, use_count=$10, save_count=$11, fail_count=$12, noise_count=$13,
			stability=$14, last_accessed_at=$15, valid_from=$16, valid_until=$17,
			episode_id=$18, supersedes_id=$19, superseded_by_id=$20, privacy_zone=$21,
			updated_at=$22
			WHERE %s AND id=$%d`, whereClause2, finalNext),
		args...,
	)
	return err
}

func (m *memoryStore) SetStatus(ctx context.Context, scope identity.Scope, id string, status string, updatedAt int64) error {
	whereClause, args, next := buildScopeWhere(scope, 3)
	args = append([]interface{}{status, updatedAt}, args...)
	args = append(args, id)
	_, err := m.s.pool.Exec(ctx,
		fmt.Sprintf(`UPDATE memories SET status=$1, updated_at=$2 WHERE %s AND id=$%d`, whereClause, next),
		args...,
	)
	return err
}

func (m *memoryStore) ListByStatus(ctx context.Context, scope identity.Scope, status string, limit int, cursor string) ([]store.Memory, string, error) {
	whereClause, args, next := buildScopeWhere(scope, 1)
	whereClause += fmt.Sprintf(` AND status = $%d`, next)
	args = append(args, status)
	next++

	if cursor != "" {
		whereClause += fmt.Sprintf(` AND created_at > (SELECT created_at FROM memories WHERE id = $%d)`, next)
		args = append(args, cursor)
		next++
	}
	args = append(args, limit+1)

	rows, err := m.s.pool.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        kind, content, context, status,
		        importance, confidence, trust_source,
		        match_count, inject_count, use_count, save_count, fail_count, noise_count,
		        stability, last_accessed_at, valid_from, valid_until,
		        episode_id, supersedes_id, superseded_by_id, privacy_zone,
		        created_at, updated_at
		 FROM memories WHERE `+whereClause+fmt.Sprintf(` ORDER BY created_at ASC LIMIT $%d`, next),
		args...,
	)
	if err != nil {
		return nil, "", fmt.Errorf("pgstore: list memories: %w", err)
	}
	defer rows.Close()

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
		nextCursor = out[limit].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

func (m *memoryStore) InsertLinks(ctx context.Context, scope identity.Scope, links []store.Link) error {
	if len(links) == 0 {
		return nil
	}
	tx, err := m.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, l := range links {
		now := time.Now().UnixMilli()
		if l.CreatedAt == 0 {
			l.CreatedAt = now
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO links (id, tenant_id, from_memory, to_memory, type, source, confidence, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(id) DO NOTHING`,
			l.ID, scope.Tenant, l.FromMemory, l.ToMemory, l.Type, l.Source, l.Confidence, l.CreatedAt,
		); err != nil {
			return fmt.Errorf("pgstore: insert link %q: %w", l.ID, err)
		}
	}
	return tx.Commit(ctx)
}

func (m *memoryStore) ListLinks(ctx context.Context, scope identity.Scope, fromMemoryID, toMemoryID string) ([]store.Link, error) {
	clause := "tenant_id = $1"
	args := []interface{}{scope.Tenant}
	next := 2
	if fromMemoryID != "" {
		clause += fmt.Sprintf(" AND from_memory = $%d", next)
		args = append(args, fromMemoryID)
		next++
	}
	if toMemoryID != "" {
		clause += fmt.Sprintf(" AND to_memory = $%d", next)
		args = append(args, toMemoryID)
	}
	rows, err := m.s.pool.Query(ctx,
		`SELECT id, tenant_id, from_memory, to_memory, type, source, confidence, created_at
		 FROM links WHERE `+clause+` ORDER BY created_at ASC`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list links: %w", err)
	}
	defer rows.Close()
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

func (m *memoryStore) AddProvenance(ctx context.Context, scope identity.Scope, provRows []store.Provenance) error {
	if len(provRows) == 0 {
		return nil
	}
	tx, err := m.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, p := range provRows {
		now := time.Now().UnixMilli()
		if p.CreatedAt == 0 {
			p.CreatedAt = now
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO provenance (id, memory_id, record_id, span_start, span_end, tenant_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(id) DO NOTHING`,
			p.ID, p.MemoryID, p.RecordID, p.SpanStart, p.SpanEnd, scope.Tenant, p.CreatedAt,
		); err != nil {
			return fmt.Errorf("pgstore: add provenance %q: %w", p.ID, err)
		}
	}
	return tx.Commit(ctx)
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
