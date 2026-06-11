package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type memoryStore struct{ s *pgStore }

// memorySelectCols is the column list for all SELECT queries on the memories table.
// content_hash uses COALESCE to return "" for pre-Phase-08 NULL rows.
const memorySelectCols = `id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
       kind, content, context, status,
       importance, confidence, trust_source,
       match_count, inject_count, use_count, save_count, fail_count, noise_count,
       stability, last_accessed_at, valid_from, valid_until,
       episode_id, supersedes_id, superseded_by_id, privacy_zone,
       created_at, updated_at, COALESCE(content_hash,'')`

func (m *memoryStore) Insert(ctx context.Context, scope identity.Scope, mem store.Memory) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
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
			 created_at, updated_at, content_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)`,
		mem.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
		mem.Kind, mem.Content, mem.Context, mem.Status,
		mem.Importance, mem.Confidence, mem.TrustSource,
		mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
		mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
		mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
		mem.CreatedAt, mem.UpdatedAt, nullStr(mem.ContentHash),
	)
	return err
}

func (m *memoryStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Memory, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	args = append(args, id)
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+memorySelectCols+` FROM memories WHERE `+whereClause+fmt.Sprintf(` AND id = $%d`, next),
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
	args := []interface{}{
		mem.Kind, mem.Content, mem.Context, mem.Status,
		mem.Importance, mem.Confidence, mem.TrustSource,
		mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
		mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
		mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
		mem.UpdatedAt, nullStr(mem.ContentHash),
	}
	baseIdx := len(args) + 1
	whereClause, scopeArgs, finalNext, err := buildScopeWhere(scope, baseIdx)
	if err != nil {
		return err
	}
	args = append(args, scopeArgs...)
	args = append(args, mem.ID)
	_, err = m.s.pool.Exec(ctx,
		fmt.Sprintf(`UPDATE memories SET
			kind=$1, content=$2, context=$3, status=$4,
			importance=$5, confidence=$6, trust_source=$7,
			match_count=$8, inject_count=$9, use_count=$10, save_count=$11, fail_count=$12, noise_count=$13,
			stability=$14, last_accessed_at=$15, valid_from=$16, valid_until=$17,
			episode_id=$18, supersedes_id=$19, superseded_by_id=$20, privacy_zone=$21,
			updated_at=$22, content_hash=$23
			WHERE %s AND id=$%d`, whereClause, finalNext),
		args...,
	)
	return err
}

func (m *memoryStore) SetStatus(ctx context.Context, scope identity.Scope, id string, status string, updatedAt int64) error {
	whereClause, args, next, err := buildScopeWhere(scope, 3)
	if err != nil {
		return err
	}
	args = append([]interface{}{status, updatedAt}, args...)
	args = append(args, id)
	_, err = m.s.pool.Exec(ctx,
		fmt.Sprintf(`UPDATE memories SET status=$1, updated_at=$2 WHERE %s AND id=$%d`, whereClause, next),
		args...,
	)
	return err
}

// ListByStatus returns memories ordered by (created_at, id) ASC.
// cursor is an opaque "<millis>:<id>" pagination token (Q1 composite cursor).
func (m *memoryStore) ListByStatus(ctx context.Context, scope identity.Scope, status string, limit int, cursor string) ([]store.Memory, string, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, "", err
	}
	whereClause += fmt.Sprintf(` AND status = $%d`, next)
	args = append(args, status)
	next++

	if cursor != "" {
		ts, cid, perr := parseCursor(cursor)
		if perr != nil {
			return nil, "", perr
		}
		whereClause += fmt.Sprintf(` AND (created_at, id) > ($%d, $%d)`, next, next+1)
		args = append(args, ts, cid)
		next += 2
	}
	args = append(args, limit+1)

	rows, err := m.s.pool.Query(ctx,
		`SELECT `+memorySelectCols+` FROM memories WHERE `+whereClause+fmt.Sprintf(` ORDER BY created_at ASC, id ASC LIMIT $%d`, next),
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
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
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

// GetByContentHash returns the active memory matching hash within scope (D-044).
func (m *memoryStore) GetByContentHash(ctx context.Context, scope identity.Scope, hash string) (*store.Memory, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	args = append(args, hash)
	row := m.s.pool.QueryRow(ctx,
		`SELECT `+memorySelectCols+` FROM memories WHERE `+whereClause+
			fmt.Sprintf(` AND status = 'active' AND content_hash = $%d LIMIT 1`, next),
		args...,
	)
	mem, err := scanMemory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return mem, err
}

// FindNeighbors returns active memories sharing entities or keywords with q,
// ranked by overlap count descending then recency (D-044).
func (m *memoryStore) FindNeighbors(ctx context.Context, scope identity.Scope, q store.NeighborQuery) ([]store.Memory, error) {
	if len(q.Entities) == 0 && len(q.Keywords) == 0 {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 8
	}

	var unionParts []string
	var cteArgs []interface{}
	nextCTE := 1

	if len(q.Entities) > 0 {
		cteArgs = append(cteArgs, scope.Tenant)
		cteArgs = append(cteArgs, q.Entities) // pgx handles []string as text[]
		unionParts = append(unionParts,
			fmt.Sprintf("SELECT memory_id FROM memory_entities WHERE tenant_id = $%d AND entity = ANY($%d)", nextCTE, nextCTE+1))
		nextCTE += 2
	}
	if len(q.Keywords) > 0 {
		cteArgs = append(cteArgs, scope.Tenant)
		cteArgs = append(cteArgs, q.Keywords)
		unionParts = append(unionParts,
			fmt.Sprintf("SELECT memory_id FROM memory_keywords WHERE tenant_id = $%d AND keyword = ANY($%d)", nextCTE, nextCTE+1))
		nextCTE += 2
	}

	scopeWhere, scopeArgs, next, err := buildScopeWhere(scope, nextCTE)
	if err != nil {
		return nil, err
	}

	allArgs := append(cteArgs, scopeArgs...) //nolint:gocritic
	allArgs = append(allArgs, limit)

	qStr := `WITH overlap AS (
		SELECT memory_id, COUNT(*) AS cnt
		FROM (` + strings.Join(unionParts, " UNION ALL ") + `) sub
		GROUP BY memory_id
	)
	SELECT ` + memorySelectCols + `
	FROM memories m
	JOIN overlap o ON o.memory_id = m.id
	WHERE ` + scopeWhere + ` AND m.status = 'active'
	ORDER BY o.cnt DESC, m.created_at DESC
	LIMIT $` + strconv.Itoa(next) //nolint:gosec

	rows, err := m.s.pool.Query(ctx, qStr, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: find neighbors: %w", err)
	}
	defer rows.Close()

	var out []store.Memory
	for rows.Next() {
		mem, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *mem)
	}
	return out, rows.Err()
}

// counterColumn maps the counter name to its SQL column.
var counterColumn = map[string]string{
	"match":  "match_count",
	"inject": "inject_count",
	"use":    "use_count",
	"save":   "save_count",
	"fail":   "fail_count",
	"noise":  "noise_count",
}

// IncrementCounter atomically increments one utility counter on a memory.
func (m *memoryStore) IncrementCounter(ctx context.Context, scope identity.Scope, id, counter string) error {
	col, ok := counterColumn[counter]
	if !ok {
		return fmt.Errorf("pgstore: unknown counter %q", counter)
	}
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return err
	}
	args = append(args, id)
	_, err = m.s.pool.Exec(ctx,
		fmt.Sprintf(`UPDATE memories SET %s = %s + 1 WHERE %s AND id = $%d`, col, col, whereClause, next), //nolint:gosec
		args...,
	)
	return err
}

// Commit executes one reconciliation outcome as a single atomic transaction.
// PostgreSQL driver: pool.Begin → pgx.Tx (D-045).
// Events in CommitSet.Events are written directly into the same tx.
func (m *memoryStore) Commit(ctx context.Context, scope identity.Scope, cs store.CommitSet) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	tx, err := m.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := execCommitPG(ctx, tx, scope, cs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// execCommitPG runs the full commit logic inside a pgx.Tx.
func execCommitPG(ctx context.Context, tx pgx.Tx, scope identity.Scope, cs store.CommitSet) error {
	now := time.Now().UnixMilli()

	switch cs.Action {
	case store.ActionAdd, store.ActionPark:
		if err := insertMemoryPG(ctx, tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("pgstore: commit %s insert memory: %w", cs.Action, err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := insertJunctionsPG(ctx, tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries); err != nil {
			return err
		}
		if err := insertProvenancePG(ctx, tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		if err := insertLinksPG(ctx, tx, scope, cs.Links, now); err != nil {
			return err
		}

	case store.ActionUpdate:
		if err := updateMemoryPG(ctx, tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("pgstore: commit update: %w", err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := deleteJunctionsPG(ctx, tx, cs.Memory.ID); err != nil {
			return err
		}
		if err := insertJunctionsPG(ctx, tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries); err != nil {
			return err
		}
		if err := insertProvenancePG(ctx, tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		if err := insertLinksPG(ctx, tx, scope, cs.Links, now); err != nil {
			return err
		}

	case store.ActionMerge:
		if err := insertMemoryPG(ctx, tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("pgstore: commit merge insert: %w", err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := insertJunctionsPG(ctx, tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries); err != nil {
			return err
		}
		if err := insertProvenancePG(ctx, tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		for _, t := range cs.Targets {
			if _, err := tx.Exec(ctx,
				`UPDATE memories SET status='superseded', superseded_by_id=$1, updated_at=$2 WHERE tenant_id=$3 AND id=$4`,
				cs.Memory.ID, now, scope.Tenant, t.ID,
			); err != nil {
				return fmt.Errorf("pgstore: commit merge supersede %q: %w", t.ID, err)
			}
		}
		if err := insertLinksPG(ctx, tx, scope, cs.Links, now); err != nil {
			return err
		}

	case store.ActionSupersede:
		if err := insertMemoryPG(ctx, tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("pgstore: commit supersede insert: %w", err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := insertJunctionsPG(ctx, tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries); err != nil {
			return err
		}
		if err := insertProvenancePG(ctx, tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		for _, t := range cs.Targets {
			if _, err := tx.Exec(ctx,
				`UPDATE memories SET status='superseded', superseded_by_id=$1, updated_at=$2 WHERE tenant_id=$3 AND id=$4`,
				cs.Memory.ID, now, scope.Tenant, t.ID,
			); err != nil {
				return fmt.Errorf("pgstore: commit supersede target %q: %w", t.ID, err)
			}
		}
		if err := insertLinksPG(ctx, tx, scope, cs.Links, now); err != nil {
			return err
		}

	case store.ActionDiscard:
		// Nothing to persist; events carry the reason.

	default:
		return fmt.Errorf("pgstore: commit: unknown action %q", cs.Action)
	}

	// Write all events in the same transaction (D-045).
	for _, ev := range cs.Events {
		if err := insertEventPG(ctx, tx, scope, ev, now); err != nil {
			return fmt.Errorf("pgstore: commit event: %w", err)
		}
	}
	return nil
}

func insertMemoryPG(ctx context.Context, tx pgx.Tx, scope identity.Scope, mem store.Memory, now int64) error {
	if mem.CreatedAt == 0 {
		mem.CreatedAt = now
	}
	if mem.UpdatedAt == 0 {
		mem.UpdatedAt = now
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO memories
			(id, tenant_id, project_id, user_id, session_id, kind, content, context, status,
			 importance, confidence, trust_source,
			 match_count, inject_count, use_count, save_count, fail_count, noise_count,
			 stability, last_accessed_at, valid_from, valid_until,
			 episode_id, supersedes_id, superseded_by_id, privacy_zone,
			 created_at, updated_at, content_hash)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29)`,
		mem.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
		mem.Kind, mem.Content, mem.Context, mem.Status,
		mem.Importance, mem.Confidence, mem.TrustSource,
		mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
		mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
		mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
		mem.CreatedAt, mem.UpdatedAt, nullStr(mem.ContentHash),
	)
	return err
}

func updateMemoryPG(ctx context.Context, tx pgx.Tx, scope identity.Scope, mem store.Memory, now int64) error {
	if mem.UpdatedAt == 0 {
		mem.UpdatedAt = now
	}
	_, err := tx.Exec(ctx, `UPDATE memories SET
		kind=$1, content=$2, context=$3, status=$4,
		importance=$5, confidence=$6, trust_source=$7,
		match_count=$8, inject_count=$9, use_count=$10, save_count=$11, fail_count=$12, noise_count=$13,
		stability=$14, last_accessed_at=$15, valid_from=$16, valid_until=$17,
		episode_id=$18, supersedes_id=$19, superseded_by_id=$20, privacy_zone=$21,
		updated_at=$22, content_hash=$23
		WHERE tenant_id=$24 AND id=$25`,
		mem.Kind, mem.Content, mem.Context, mem.Status,
		mem.Importance, mem.Confidence, mem.TrustSource,
		mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
		mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
		mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
		mem.UpdatedAt, nullStr(mem.ContentHash),
		scope.Tenant, mem.ID,
	)
	return err
}

func insertJunctionsPG(ctx context.Context, tx pgx.Tx, scope identity.Scope, memID string, entities, keywords, queries []string) error {
	for _, e := range entities {
		if _, err := tx.Exec(ctx,
			`INSERT INTO memory_entities (id, memory_id, entity, tenant_id) VALUES ($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`,
			ulid.Make().String(), memID, e, scope.Tenant,
		); err != nil {
			return fmt.Errorf("pgstore: insert entity: %w", err)
		}
	}
	for _, k := range keywords {
		if _, err := tx.Exec(ctx,
			`INSERT INTO memory_keywords (id, memory_id, keyword, tenant_id) VALUES ($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`,
			ulid.Make().String(), memID, k, scope.Tenant,
		); err != nil {
			return fmt.Errorf("pgstore: insert keyword: %w", err)
		}
	}
	for _, q := range queries {
		if _, err := tx.Exec(ctx,
			`INSERT INTO memory_queries (id, memory_id, query, tenant_id) VALUES ($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`,
			ulid.Make().String(), memID, q, scope.Tenant,
		); err != nil {
			return fmt.Errorf("pgstore: insert query: %w", err)
		}
	}
	return nil
}

func deleteJunctionsPG(ctx context.Context, tx pgx.Tx, memID string) error {
	for _, table := range []string{"memory_entities", "memory_keywords", "memory_queries"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE memory_id = $1`, memID); err != nil { //nolint:gosec
			return fmt.Errorf("pgstore: delete junctions from %s: %w", table, err)
		}
	}
	return nil
}

func insertProvenancePG(ctx context.Context, tx pgx.Tx, scope identity.Scope, rows []store.Provenance, now int64) error {
	for _, p := range rows {
		if p.CreatedAt == 0 {
			p.CreatedAt = now
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO provenance (id, memory_id, record_id, span_start, span_end, tenant_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING`,
			p.ID, p.MemoryID, p.RecordID, p.SpanStart, p.SpanEnd, scope.Tenant, p.CreatedAt,
		); err != nil {
			return fmt.Errorf("pgstore: insert provenance: %w", err)
		}
	}
	return nil
}

func insertLinksPG(ctx context.Context, tx pgx.Tx, scope identity.Scope, links []store.Link, now int64) error {
	for _, l := range links {
		if l.CreatedAt == 0 {
			l.CreatedAt = now
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO links (id, tenant_id, from_memory, to_memory, type, source, confidence, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO NOTHING`,
			l.ID, scope.Tenant, l.FromMemory, l.ToMemory, l.Type, l.Source, l.Confidence, l.CreatedAt,
		); err != nil {
			return fmt.Errorf("pgstore: insert link: %w", err)
		}
	}
	return nil
}

func insertEventPG(ctx context.Context, tx pgx.Tx, scope identity.Scope, ev store.Event, now int64) error {
	if ev.CreatedAt == 0 {
		ev.CreatedAt = now
	}
	if ev.Payload == "" {
		ev.Payload = "{}"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO events (id, tenant_id, project_id, user_id, session_id, type, subject_id, reason, payload, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		ev.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
		ev.Type, ev.SubjectID, ev.Reason, ev.Payload, ev.CreatedAt,
	)
	return err
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
		&mem.CreatedAt, &mem.UpdatedAt, &mem.ContentHash,
	)
	if err != nil {
		return nil, err
	}
	return &mem, nil
}
