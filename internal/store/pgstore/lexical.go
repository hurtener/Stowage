package pgstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// LexicalSearch returns the top-k memories matching query via tsvector GIN index
// on content+context (ts_rank). Results are ordered by ts_rank score descending.
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

	var sb strings.Builder
	args := []interface{}{}
	idx := 1

	// plainto_tsquery parses plain English into a tsquery (no special syntax).
	fmt.Fprintf(&sb, `
		SELECT m.id, ts_rank(m.tsv, plainto_tsquery('simple', $%d)) AS rank
		FROM memories m
		WHERE m.tsv @@ plainto_tsquery('simple', $%d) AND m.tenant_id = $%d AND m.status = 'active'`,
		idx, idx, idx+1)
	args = append(args, query, scope.Tenant)
	idx += 2

	if scope.Project != "" {
		fmt.Fprintf(&sb, ` AND m.project_id = $%d`, idx)
		args = append(args, scope.Project)
		idx++
	}
	if scope.User != "" {
		fmt.Fprintf(&sb, ` AND m.user_id = $%d`, idx)
		args = append(args, scope.User)
		idx++
	}
	if scope.Session != "" {
		fmt.Fprintf(&sb, ` AND m.session_id = $%d`, idx)
		args = append(args, scope.Session)
		idx++
	}
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, kk := range kinds {
			placeholders[i] = fmt.Sprintf("$%d", idx)
			args = append(args, kk)
			idx++
		}
		sb.WriteString(` AND m.kind IN (` + strings.Join(placeholders, ",") + `)`)
	}
	if w.From > 0 {
		fmt.Fprintf(&sb, ` AND m.created_at >= $%d`, idx)
		args = append(args, w.From)
		idx++
	}
	if w.Until > 0 {
		fmt.Fprintf(&sb, ` AND m.created_at <= $%d`, idx)
		args = append(args, w.Until)
		idx++
	}
	fmt.Fprintf(&sb, ` ORDER BY rank DESC LIMIT $%d`, idx)
	args = append(args, k)

	rows, err := m.s.pool.Query(ctx, sb.String(), args...) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("pgstore: lexical search: %w", err)
	}
	defer rows.Close()

	var out []store.LexicalHit
	for rows.Next() {
		var h store.LexicalHit
		if err := rows.Scan(&h.MemoryID, &h.Rank); err != nil {
			return nil, fmt.Errorf("pgstore: lexical search row: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// QuerySearch returns the top-k memories whose anticipated queries match the
// given query text via tsvector GIN index on memory_queries.
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

	var sb strings.Builder
	args := []interface{}{}
	idx := 1

	fmt.Fprintf(&sb, `
		SELECT m.id, ts_rank(mq.tsv, plainto_tsquery('simple', $%d)) AS rank
		FROM memory_queries mq
		JOIN memories m ON m.id = mq.memory_id
		WHERE mq.tsv @@ plainto_tsquery('simple', $%d) AND m.tenant_id = $%d AND m.status = 'active'`,
		idx, idx, idx+1)
	args = append(args, query, scope.Tenant)
	idx += 2

	if scope.Project != "" {
		fmt.Fprintf(&sb, ` AND m.project_id = $%d`, idx)
		args = append(args, scope.Project)
		idx++
	}
	if scope.User != "" {
		fmt.Fprintf(&sb, ` AND m.user_id = $%d`, idx)
		args = append(args, scope.User)
		idx++
	}
	if scope.Session != "" {
		fmt.Fprintf(&sb, ` AND m.session_id = $%d`, idx)
		args = append(args, scope.Session)
		idx++
	}
	if w.From > 0 {
		fmt.Fprintf(&sb, ` AND m.created_at >= $%d`, idx)
		args = append(args, w.From)
		idx++
	}
	if w.Until > 0 {
		fmt.Fprintf(&sb, ` AND m.created_at <= $%d`, idx)
		args = append(args, w.Until)
		idx++
	}
	fmt.Fprintf(&sb, ` ORDER BY rank DESC LIMIT $%d`, idx)
	args = append(args, k)

	rows, err := m.s.pool.Query(ctx, sb.String(), args...) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("pgstore: query search: %w", err)
	}
	defer rows.Close()

	var out []store.LexicalHit
	for rows.Next() {
		var h store.LexicalHit
		if err := rows.Scan(&h.MemoryID, &h.Rank); err != nil {
			return nil, fmt.Errorf("pgstore: query search row: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetMany returns memories for the given IDs within scope. IDs not found are
// silently omitted. Order matches the order of ids.
func (m *memoryStore) GetMany(ctx context.Context, scope identity.Scope, ids []string) ([]store.Memory, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	if len(ids) == 0 {
		return nil, nil
	}

	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}

	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", next)
		args = append(args, id)
		next++
	}

	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + //nolint:gosec
		` AND id IN (` + strings.Join(placeholders, ",") + `)`

	rows, err := m.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: get many: %w", err)
	}
	defer rows.Close()

	byID := make(map[string]store.Memory)
	for rows.Next() {
		mem, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("pgstore: get many row: %w", err)
		}
		byID[mem.ID] = *mem
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]store.Memory, 0, len(ids))
	for _, id := range ids {
		if mem, ok := byID[id]; ok {
			out = append(out, mem)
		}
	}
	return out, nil
}
