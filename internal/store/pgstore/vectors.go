package pgstore

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type vectorStore struct{ s *pgStore }

// encodeVec encodes a float32 slice into little-endian bytes (D-046).
func encodeVec(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVec decodes little-endian bytes into a float32 slice (D-046).
func decodeVec(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// Upsert inserts or replaces the vector for v.MemoryID within scope.
func (vs *vectorStore) Upsert(ctx context.Context, scope identity.Scope, v store.StoredVector) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	blob := encodeVec(v.Vec)
	_, err := vs.s.pool.Exec(ctx, `
		INSERT INTO memory_vectors
			(memory_id, tenant_id, project_id, user_id, session_id, model, dims, vec)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(memory_id) DO UPDATE SET
			model = EXCLUDED.model,
			dims  = EXCLUDED.dims,
			vec   = EXCLUDED.vec`,
		v.MemoryID, scope.Tenant,
		nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
		v.Model, v.Dims, blob,
	)
	if err != nil {
		return fmt.Errorf("pgstore: vectors upsert: %w", err)
	}
	return nil
}

// Delete removes the vector for memoryID. No-op when absent.
func (vs *vectorStore) Delete(ctx context.Context, scope identity.Scope, memoryID string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	_, err := vs.s.pool.Exec(ctx,
		`DELETE FROM memory_vectors WHERE memory_id = $1 AND tenant_id = $2`,
		memoryID, scope.Tenant)
	if err != nil {
		return fmt.Errorf("pgstore: vectors delete: %w", err)
	}
	return nil
}

// Scan returns all stored vectors for the scope (for brute-force cosine in vindex).
func (vs *vectorStore) Scan(ctx context.Context, scope identity.Scope, kinds []string, w store.Window) ([]store.StoredVector, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}

	var sb strings.Builder
	args := []interface{}{}
	idx := 1

	fmt.Fprintf(&sb, `
		SELECT mv.memory_id, mv.tenant_id,
		       COALESCE(mv.project_id,''), COALESCE(mv.user_id,''), COALESCE(mv.session_id,''),
		       mv.model, mv.dims, mv.vec, m.kind, m.created_at
		FROM memory_vectors mv
		JOIN memories m ON m.id = mv.memory_id
		WHERE mv.tenant_id = $%d AND m.status = 'active'`, idx)
	args = append(args, scope.Tenant)
	idx++

	if scope.Project != "" {
		fmt.Fprintf(&sb, ` AND mv.project_id = $%d`, idx)
		args = append(args, scope.Project)
		idx++
	}
	if scope.User != "" {
		fmt.Fprintf(&sb, ` AND mv.user_id = $%d`, idx)
		args = append(args, scope.User)
		idx++
	}
	if scope.Session != "" {
		fmt.Fprintf(&sb, ` AND mv.session_id = $%d`, idx)
		args = append(args, scope.Session)
		idx++
	}
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, k := range kinds {
			placeholders[i] = fmt.Sprintf("$%d", idx)
			args = append(args, k)
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
		_ = idx
	}

	rows, err := vs.s.pool.Query(ctx, sb.String(), args...) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("pgstore: vectors scan: %w", err)
	}
	defer rows.Close()

	var out []store.StoredVector
	for rows.Next() {
		var sv store.StoredVector
		var blob []byte
		if err := rows.Scan(
			&sv.MemoryID, &sv.TenantID, &sv.ProjectID, &sv.UserID, &sv.SessionID,
			&sv.Model, &sv.Dims, &blob, &sv.Kind, &sv.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("pgstore: vectors scan row: %w", err)
		}
		sv.Vec = decodeVec(blob)
		out = append(out, sv)
	}
	return out, rows.Err()
}

// ListWithoutVectors returns at most limit active memories that have no vector
// entry, with the junction rows needed to build enriched text for embedding.
func (vs *vectorStore) ListWithoutVectors(ctx context.Context, limit int) ([]store.MemoryForEmbed, error) {
	rows, err := vs.s.pool.Query(ctx, `
		SELECT m.id, m.tenant_id,
		       COALESCE(m.project_id,''), COALESCE(m.user_id,''), COALESCE(m.session_id,''),
		       m.content,
		       COALESCE(STRING_AGG(DISTINCT me.entity,  ','), '') AS entities,
		       COALESCE(STRING_AGG(DISTINCT mk.keyword, ','), '') AS keywords,
		       COALESCE(STRING_AGG(DISTINCT mq.query,   ','), '') AS queries
		FROM memories m
		LEFT JOIN memory_entities me ON me.memory_id = m.id
		LEFT JOIN memory_keywords mk ON mk.memory_id = m.id
		LEFT JOIN memory_queries  mq ON mq.memory_id = m.id
		LEFT JOIN memory_vectors  mv ON mv.memory_id = m.id
		WHERE m.status = 'active' AND mv.memory_id IS NULL
		GROUP BY m.id, m.tenant_id, m.project_id, m.user_id, m.session_id, m.content
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list without vectors: %w", err)
	}
	defer rows.Close()

	var out []store.MemoryForEmbed
	for rows.Next() {
		var m store.MemoryForEmbed
		var entities, keywords, queries string
		if err := rows.Scan(
			&m.MemoryID, &m.TenantID, &m.ProjectID, &m.UserID, &m.SessionID,
			&m.Content, &entities, &keywords, &queries,
		); err != nil {
			return nil, fmt.Errorf("pgstore: list without vectors row: %w", err)
		}
		m.Entities = splitCSV(entities)
		m.Keywords = splitCSV(keywords)
		m.Queries = splitCSV(queries)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DistinctModels returns the distinct embedding model names across all vectors.
func (vs *vectorStore) DistinctModels(ctx context.Context) ([]string, error) {
	rows, err := vs.s.pool.Query(ctx, `SELECT DISTINCT model FROM memory_vectors WHERE model <> '' ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("pgstore: distinct vector models: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, 2)
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// splitCSV splits a comma-separated string, returning nil for empty input.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
