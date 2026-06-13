package sqlitestore

import (
	"context"
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// LexicalSearch returns the top-k memories matching query via FTS5 on
// content+context (memories_fts). Results are ordered by bm25 score descending
// (bm25() returns negative values in SQLite — we negate for natural ordering).
func (m *memoryStore) LexicalSearch(
	ctx context.Context, scope identity.Scope,
	query string, k int, w store.Window, kinds []string,
) ([]store.LexicalHit, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	if query == "" || k <= 0 {
		return nil, nil
	}

	// Sanitize the user text into a safe FTS5 MATCH argument (BUG-4). An input
	// with no indexable term yields a clean empty result, never a lane error.
	matchArg := ftsMatchArg(query)
	if matchArg == "" {
		return nil, nil
	}

	// SQLite FTS5 bm25() returns negative values; negate to get a positive score.
	// We join memories to apply scope, status, kind, and window filters.
	var sb strings.Builder
	args := []interface{}{}

	sb.WriteString(`
		SELECT m.id, -bm25(memories_fts) AS rank
		FROM memories_fts
		JOIN memories m ON m.rowid = memories_fts.rowid
		WHERE memories_fts MATCH ? AND m.tenant_id = ? AND m.status = 'active'`)
	args = append(args, matchArg, scope.Tenant)

	if scope.Project != "" {
		sb.WriteString(` AND m.project_id = ?`)
		args = append(args, scope.Project)
	}
	if scope.User != "" {
		sb.WriteString(` AND m.user_id = ?`)
		args = append(args, scope.User)
	}
	if scope.Session != "" {
		sb.WriteString(` AND m.session_id = ?`)
		args = append(args, scope.Session)
	}
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, kk := range kinds {
			placeholders[i] = "?"
			args = append(args, kk)
		}
		sb.WriteString(` AND m.kind IN (` + strings.Join(placeholders, ",") + `)`)
	}
	if w.From > 0 {
		sb.WriteString(` AND m.created_at >= ?`)
		args = append(args, w.From)
	}
	if w.Until > 0 {
		sb.WriteString(` AND m.created_at <= ?`)
		args = append(args, w.Until)
	}
	sb.WriteString(` ORDER BY rank DESC LIMIT ?`)
	args = append(args, k)

	rows, err := m.s.rdb.QueryContext(ctx, sb.String(), args...) //nolint:gosec
	if err != nil {
		// FTS5 match syntax errors surface as runtime errors; surface them.
		return nil, fmt.Errorf("sqlitestore: lexical search: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.LexicalHit
	for rows.Next() {
		var h store.LexicalHit
		if err := rows.Scan(&h.MemoryID, &h.Rank); err != nil {
			return nil, fmt.Errorf("sqlitestore: lexical search row: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// QuerySearch returns the top-k memories whose anticipated queries match the
// given query text via FTS5 on memory_queries_fts.
func (m *memoryStore) QuerySearch(
	ctx context.Context, scope identity.Scope,
	query string, k int, w store.Window,
) ([]store.LexicalHit, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	if query == "" || k <= 0 {
		return nil, nil
	}

	// Sanitize the user text into a safe FTS5 MATCH argument (BUG-4).
	matchArg := ftsMatchArg(query)
	if matchArg == "" {
		return nil, nil
	}

	var sb strings.Builder
	args := []interface{}{}

	sb.WriteString(`
		SELECT m.id, -bm25(memory_queries_fts) AS rank
		FROM memory_queries_fts
		JOIN memory_queries mq ON mq.rowid = memory_queries_fts.rowid
		JOIN memories m ON m.id = mq.memory_id
		WHERE memory_queries_fts MATCH ? AND m.tenant_id = ? AND m.status = 'active'`)
	args = append(args, matchArg, scope.Tenant)

	if scope.Project != "" {
		sb.WriteString(` AND m.project_id = ?`)
		args = append(args, scope.Project)
	}
	if scope.User != "" {
		sb.WriteString(` AND m.user_id = ?`)
		args = append(args, scope.User)
	}
	if scope.Session != "" {
		sb.WriteString(` AND m.session_id = ?`)
		args = append(args, scope.Session)
	}
	if w.From > 0 {
		sb.WriteString(` AND m.created_at >= ?`)
		args = append(args, w.From)
	}
	if w.Until > 0 {
		sb.WriteString(` AND m.created_at <= ?`)
		args = append(args, w.Until)
	}
	sb.WriteString(` ORDER BY rank DESC LIMIT ?`)
	args = append(args, k)

	rows, err := m.s.rdb.QueryContext(ctx, sb.String(), args...) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: query search: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.LexicalHit
	for rows.Next() {
		var h store.LexicalHit
		if err := rows.Scan(&h.MemoryID, &h.Rank); err != nil {
			return nil, fmt.Errorf("sqlitestore: query search row: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetMany returns memories for the given IDs within scope. IDs not found are
// silently omitted; order matches the order of ids.
func (m *memoryStore) GetMany(ctx context.Context, scope identity.Scope, ids []string) ([]store.Memory, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	if len(ids) == 0 {
		return nil, nil
	}

	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}

	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}

	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + //nolint:gosec
		` AND id IN (` + strings.Join(placeholders, ",") + `)`

	rows, err := m.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: get many: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	byID := make(map[string]store.Memory)
	for rows.Next() {
		mem, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlitestore: get many row: %w", err)
		}
		byID[mem.ID] = *mem
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Return in the requested ID order, omitting missing entries.
	out := make([]store.Memory, 0, len(ids))
	for _, id := range ids {
		if mem, ok := byID[id]; ok {
			out = append(out, mem)
		}
	}
	return out, nil
}
