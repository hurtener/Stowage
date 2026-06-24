package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type memoryStore struct{ s *sqliteStore }

// sqliteIsUnique reports whether err is a SQLite UNIQUE constraint violation.
// modernc.org/sqlite surfaces these as errors containing the string
// "UNIQUE constraint failed"; we detect them by message inspection since the
// pure-Go driver does not export a typed error sentinel.
func sqliteIsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

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
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if mem.CreatedAt == 0 {
			mem.CreatedAt = now
		}
		if mem.UpdatedAt == 0 {
			mem.UpdatedAt = now
		}
		// Scope fields take precedence; fall back to the memory struct's own fields
		// so that callers that pre-populate mem.SessionID (e.g. test helpers, imports)
		// have their values persisted when no session is present in the scope.
		sessionVal := scope.Session
		if sessionVal == "" {
			sessionVal = mem.SessionID
		}
		_, err := tx.Exec(`
			INSERT INTO memories
				(id, tenant_id, project_id, user_id, session_id, kind, content, context, status,
				 importance, confidence, trust_source,
				 match_count, inject_count, use_count, save_count, fail_count, noise_count,
				 stability, last_accessed_at, valid_from, valid_until,
				 episode_id, supersedes_id, superseded_by_id, privacy_zone,
				 created_at, updated_at, content_hash)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			mem.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(sessionVal),
			mem.Kind, mem.Content, mem.Context, mem.Status,
			mem.Importance, mem.Confidence, mem.TrustSource,
			mem.MatchCount, mem.InjectCount, mem.UseCount, mem.SaveCount, mem.FailCount, mem.NoiseCount,
			mem.Stability, mem.LastAccessedAt, mem.ValidFrom, mem.ValidUntil,
			mem.EpisodeID, mem.SupersedesID, mem.SupersededByID, mem.PrivacyZone,
			mem.CreatedAt, mem.UpdatedAt, nullStr(mem.ContentHash),
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
	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + ` AND id = ?` //nolint:gosec
	row := m.s.rdb.QueryRowContext(ctx, q, args...)
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
			mem.UpdatedAt, nullStr(mem.ContentHash),
		}
		queryArgs = append(queryArgs, args...)
		queryArgs = append(queryArgs, mem.ID)
		qu := `UPDATE memories SET kind=?, content=?, context=?, status=?, importance=?, confidence=?, trust_source=?, match_count=?, inject_count=?, use_count=?, save_count=?, fail_count=?, noise_count=?, stability=?, last_accessed_at=?, valid_from=?, valid_until=?, episode_id=?, supersedes_id=?, superseded_by_id=?, privacy_zone=?, updated_at=?, content_hash=? WHERE ` + whereClause + ` AND id=?` //nolint:gosec
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
		qs := `UPDATE memories SET status=?, updated_at=? WHERE ` + whereClause + ` AND id=?` //nolint:gosec
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

	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + ` ORDER BY created_at ASC, id ASC LIMIT ?` //nolint:gosec
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

	ql := `SELECT id, tenant_id, from_memory, to_memory, type, source, confidence, created_at FROM links WHERE ` + clause + ` ORDER BY created_at ASC` //nolint:gosec
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

// GetJunctions returns the junction rows (entities, keywords, queries, provenance)
// for a memory within scope. Returns empty slices (not ErrNotFound) when no rows exist.
func (m *memoryStore) GetJunctions(ctx context.Context, scope identity.Scope, id string) (store.MemoryJunctions, error) {
	if scope.Tenant == "" {
		return store.MemoryJunctions{}, store.ErrScopeRequired
	}
	var j store.MemoryJunctions

	// Entities.
	rows, err := m.s.rdb.QueryContext(ctx,
		`SELECT entity FROM memory_entities WHERE memory_id = ? AND tenant_id = ? ORDER BY rowid`,
		id, scope.Tenant)
	if err != nil {
		return j, fmt.Errorf("sqlitestore: get entities: %w", err)
	}
	for rows.Next() {
		var e string
		if scanErr := rows.Scan(&e); scanErr != nil {
			_ = rows.Close()
			return j, scanErr
		}
		j.Entities = append(j.Entities, e)
	}
	if err = rows.Close(); err != nil {
		return j, err
	}
	if err = rows.Err(); err != nil {
		return j, err
	}

	// Keywords.
	kwRows, err := m.s.rdb.QueryContext(ctx,
		`SELECT keyword FROM memory_keywords WHERE memory_id = ? AND tenant_id = ? ORDER BY rowid`,
		id, scope.Tenant)
	if err != nil {
		return j, fmt.Errorf("sqlitestore: get keywords: %w", err)
	}
	for kwRows.Next() {
		var k string
		if scanErr := kwRows.Scan(&k); scanErr != nil {
			_ = kwRows.Close()
			return j, scanErr
		}
		j.Keywords = append(j.Keywords, k)
	}
	if err = kwRows.Close(); err != nil {
		return j, err
	}
	if err = kwRows.Err(); err != nil {
		return j, err
	}

	// Anticipated queries.
	qRows, err := m.s.rdb.QueryContext(ctx,
		`SELECT query FROM memory_queries WHERE memory_id = ? AND tenant_id = ? ORDER BY rowid`,
		id, scope.Tenant)
	if err != nil {
		return j, fmt.Errorf("sqlitestore: get queries: %w", err)
	}
	for qRows.Next() {
		var q string
		if scanErr := qRows.Scan(&q); scanErr != nil {
			_ = qRows.Close()
			return j, scanErr
		}
		j.Queries = append(j.Queries, q)
	}
	if err = qRows.Close(); err != nil {
		return j, err
	}
	if err = qRows.Err(); err != nil {
		return j, err
	}

	// Topics (D-089).
	tRows, err := m.s.rdb.QueryContext(ctx,
		`SELECT topic_key FROM memory_topics WHERE memory_id = ? AND tenant_id = ? ORDER BY rowid`,
		id, scope.Tenant)
	if err != nil {
		return j, fmt.Errorf("sqlitestore: get topics: %w", err)
	}
	for tRows.Next() {
		var tk string
		if scanErr := tRows.Scan(&tk); scanErr != nil {
			_ = tRows.Close()
			return j, scanErr
		}
		j.Topics = append(j.Topics, tk)
	}
	if err = tRows.Close(); err != nil {
		return j, err
	}
	if err = tRows.Err(); err != nil {
		return j, err
	}

	// Provenance.
	pRows, err := m.s.rdb.QueryContext(ctx,
		`SELECT id, memory_id, record_id, span_start, span_end, tenant_id, created_at
		 FROM provenance WHERE memory_id = ? AND tenant_id = ? ORDER BY created_at`,
		id, scope.Tenant)
	if err != nil {
		return j, fmt.Errorf("sqlitestore: get provenance: %w", err)
	}
	for pRows.Next() {
		var p store.Provenance
		if scanErr := pRows.Scan(&p.ID, &p.MemoryID, &p.RecordID, &p.SpanStart, &p.SpanEnd, &p.TenantID, &p.CreatedAt); scanErr != nil {
			_ = pRows.Close()
			return j, scanErr
		}
		j.Provenance = append(j.Provenance, p)
	}
	if err = pRows.Close(); err != nil {
		return j, err
	}
	return j, pRows.Err()
}

// MemoriesTopics returns topic keys per memory id within scope (D-089).
func (m *memoryStore) MemoriesTopics(ctx context.Context, scope identity.Scope, ids []string) (map[string][]string, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	out := make(map[string][]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, scope.Tenant)
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	q := `SELECT memory_id, topic_key FROM memory_topics WHERE tenant_id = ? AND memory_id IN (` + strings.Join(ph, ",") + `) ORDER BY rowid` //nolint:gosec
	rows, err := m.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: memories topics: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var mid, tk string
		if err := rows.Scan(&mid, &tk); err != nil {
			return nil, err
		}
		out[mid] = append(out[mid], tk)
	}
	return out, rows.Err()
}

// GetByContentHashStatus returns the memory matching hash AND status within scope.
// Returns ErrNotFound when absent (D-065, Phase 18 parked-duplicate check).
func (m *memoryStore) GetByContentHashStatus(ctx context.Context, scope identity.Scope, hash, status string) (*store.Memory, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, status, hash)
	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + ` AND status = ? AND content_hash = ? LIMIT 1` //nolint:gosec
	row := m.s.rdb.QueryRowContext(ctx, q, args...)
	mem, err := scanMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return mem, err
}

// ListSupersededBy returns memories whose superseded_by_id = supersederID within scope.
// Used by the merge-rollback path to find all siblings (Phase 18, D-064).
func (m *memoryStore) ListSupersededBy(ctx context.Context, scope identity.Scope, supersederID string) ([]store.Memory, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, supersederID)
	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + ` AND superseded_by_id = ?` //nolint:gosec
	rows, err := m.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var mems []store.Memory
	for rows.Next() {
		mem, scanErr := scanMemory(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		mems = append(mems, *mem)
	}
	return mems, rows.Err()
}

// GetByContentHash returns the active memory matching hash within scope.
// Returns ErrNotFound when absent (D-044).
func (m *memoryStore) GetByContentHash(ctx context.Context, scope identity.Scope, hash string) (*store.Memory, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, hash)
	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + ` AND status = 'active' AND content_hash = ? LIMIT 1` //nolint:gosec
	row := m.s.rdb.QueryRowContext(ctx, q, args...)
	mem, err := scanMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return mem, err
}

// FindNeighbors returns active memories sharing entities or keywords with q,
// ranked by overlap count descending then recency (D-044).
// Scope-parameterized per P3; cross-tenant isolation proven by conformance.
func (m *memoryStore) FindNeighbors(ctx context.Context, scope identity.Scope, q store.NeighborQuery) ([]store.Memory, error) {
	if len(q.Entities) == 0 && len(q.Keywords) == 0 {
		return nil, nil
	}
	scopeWhere, scopeArgs, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 8
	}

	var unionParts []string
	var cteArgs []interface{}

	if len(q.Entities) > 0 {
		ph := make([]string, len(q.Entities))
		cteArgs = append(cteArgs, scope.Tenant)
		for i, e := range q.Entities {
			ph[i] = "?"
			cteArgs = append(cteArgs, e)
		}
		unionParts = append(unionParts, "SELECT memory_id FROM memory_entities WHERE tenant_id = ? AND entity IN ("+strings.Join(ph, ",")+")")
	}
	if len(q.Keywords) > 0 {
		ph := make([]string, len(q.Keywords))
		cteArgs = append(cteArgs, scope.Tenant)
		for i, k := range q.Keywords {
			ph[i] = "?"
			cteArgs = append(cteArgs, k)
		}
		unionParts = append(unionParts, "SELECT memory_id FROM memory_keywords WHERE tenant_id = ? AND keyword IN ("+strings.Join(ph, ",")+")")
	}

	allArgs := append(cteArgs, scopeArgs...) //nolint:gocritic

	// Optional kind filter (NeighborQuery.Kinds): reflection candidates restrict
	// neighbors to reflection kinds so a strategy cannot dedupe/supersede a fact
	// (D-077 #5). Empty Kinds = all kinds (the topic-extraction default).
	kindClause := ""
	if len(q.Kinds) > 0 {
		ph := make([]string, len(q.Kinds))
		for i, k := range q.Kinds {
			ph[i] = "?"
			allArgs = append(allArgs, k)
		}
		kindClause = " AND m.kind IN (" + strings.Join(ph, ",") + ")"
	}
	allArgs = append(allArgs, limit)

	// Build query: all variable parts are either compile-time constants or built
	// from controlled helpers (unionParts/kindClause contain only ? placeholders;
	// scopeWhere is from buildScopeWhere; memorySelectCols is a constant).
	cteUnion := strings.Join(unionParts, " UNION ALL ")
	qStr := "WITH overlap AS (SELECT memory_id,COUNT(*) AS cnt FROM (" + cteUnion + ") sub GROUP BY memory_id) " + //nolint:gosec
		"SELECT " + memorySelectCols + " FROM memories m " +
		"JOIN overlap o ON o.memory_id=m.id " +
		"WHERE " + scopeWhere + " AND m.status='active'" + kindClause + " " +
		"ORDER BY o.cnt DESC,m.created_at DESC LIMIT ?"

	rows, err := m.s.rdb.QueryContext(ctx, qStr, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: find neighbors: %w", err)
	}
	defer func() { _ = rows.Close() }()

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
		return fmt.Errorf("sqlitestore: unknown counter %q", counter)
	}
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return err
	}
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		queryArgs := append(args, id)                                                                                 //nolint:gocritic
		_, err := tx.Exec(`UPDATE memories SET `+col+` = `+col+` + 1 WHERE `+whereClause+` AND id = ?`, queryArgs...) //nolint:gosec
		return err
	})
}

// Commit executes one reconciliation outcome as a single atomic transaction.
// SQLite driver: ONE exec closure = ONE sql.Tx (D-045).
// Events in CommitSet.Events are written directly into the same tx.
func (m *memoryStore) Commit(ctx context.Context, scope identity.Scope, cs store.CommitSet) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		return execCommitSQLite(tx, scope, cs)
	})
}

// execCommitSQLite runs the full commit logic inside a sql.Tx.
func execCommitSQLite(tx *sql.Tx, scope identity.Scope, cs store.CommitSet) error {
	now := time.Now().UnixMilli()

	switch cs.Action {
	case store.ActionAdd, store.ActionPark:
		if err := insertMemorySQLite(tx, scope, cs.Memory, now); err != nil {
			if sqliteIsUnique(err) {
				return store.ErrDuplicateContent
			}
			return fmt.Errorf("sqlitestore: commit %s insert memory: %w", cs.Action, err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := insertJunctionsSQLite(tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries, cs.Topics); err != nil {
			return err
		}
		if err := insertProvenanceSQLite(tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		if err := insertLinksSQLite(tx, scope, cs.Links, now); err != nil {
			return err
		}

	case store.ActionUpdate:
		// C1: targeted update — only content, context, content_hash, updated_at change.
		// Counters, trust_source, stability, importance, confidence, validity window,
		// episode_id, and privacy_zone are preserved from the existing row.
		if err := updateMemoryContentSQLite(tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("sqlitestore: commit update: %w", err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := deleteJunctionsSQLite(tx, cs.Memory.ID); err != nil {
			return err
		}
		if err := insertJunctionsSQLite(tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries, cs.Topics); err != nil {
			return err
		}
		if err := insertProvenanceSQLite(tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		if err := insertLinksSQLite(tx, scope, cs.Links, now); err != nil {
			return err
		}

	case store.ActionMerge:
		if err := insertMemorySQLite(tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("sqlitestore: commit merge insert: %w", err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := insertJunctionsSQLite(tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries, cs.Topics); err != nil {
			return err
		}
		if err := insertProvenanceSQLite(tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		for _, t := range cs.Targets {
			if _, err := tx.Exec(
				`UPDATE memories SET status='superseded', superseded_by_id=?, updated_at=? WHERE tenant_id=? AND id=?`,
				cs.Memory.ID, now, scope.Tenant, t.ID,
			); err != nil {
				return fmt.Errorf("sqlitestore: commit merge supersede %q: %w", t.ID, err)
			}
		}
		if err := insertLinksSQLite(tx, scope, cs.Links, now); err != nil {
			return err
		}

	case store.ActionSupersede:
		if err := insertMemorySQLite(tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("sqlitestore: commit supersede insert: %w", err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := insertJunctionsSQLite(tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries, cs.Topics); err != nil {
			return err
		}
		if err := insertProvenanceSQLite(tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		for _, t := range cs.Targets {
			if _, err := tx.Exec(
				`UPDATE memories SET status='superseded', superseded_by_id=?, updated_at=? WHERE tenant_id=? AND id=?`,
				cs.Memory.ID, now, scope.Tenant, t.ID,
			); err != nil {
				return fmt.Errorf("sqlitestore: commit supersede target %q: %w", t.ID, err)
			}
		}
		if err := insertLinksSQLite(tx, scope, cs.Links, now); err != nil {
			return err
		}

	case store.ActionDiscard:
		// Nothing to persist; events carry the reason.

	case store.ActionRollback:
		// Full-row restore from a D-017 prior-state snapshot (Phase 18, D-064).
		// One or more Memory rows are fully replaced (all scalar fields including
		// status/superseded_by_id), their junctions and provenance are replaced
		// (delete + insert), and Targets are tombstoned to status 'deleted'.
		//
		// CommitSet layout for rollback:
		//   Memory      — the primary restored row (from priorStateJSON)
		//   Entities/Keywords/Queries — replacement junctions for Memory
		//   Provenance  — replacement provenance for Memory
		//   Targets     — rows to tombstone (status='deleted')
		//   ExtraMemories — additional restored rows (merge siblings; same semantics)
		if err := restoreMemoryFullSQLite(tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("sqlitestore: commit rollback restore: %w", err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		if err := deleteJunctionsSQLite(tx, cs.Memory.ID); err != nil {
			return err
		}
		if err := insertJunctionsSQLite(tx, scope, cs.Memory.ID, cs.Entities, cs.Keywords, cs.Queries, cs.Topics); err != nil {
			return err
		}
		if err := deleteProvenanceSQLite(tx, cs.Memory.ID); err != nil {
			return err
		}
		if err := insertProvenanceSQLite(tx, scope, cs.Provenance, now); err != nil {
			return err
		}
		// Restore extra memories (merge siblings).
		for _, xm := range cs.ExtraMemories {
			if err := restoreMemoryFullSQLite(tx, scope, xm.Memory, now); err != nil {
				return fmt.Errorf("sqlitestore: commit rollback restore extra: %w", err)
			}
			if err := deleteJunctionsSQLite(tx, xm.Memory.ID); err != nil {
				return err
			}
			if err := insertJunctionsSQLite(tx, scope, xm.Memory.ID, xm.Entities, xm.Keywords, xm.Queries, xm.Topics); err != nil {
				return err
			}
			if err := deleteProvenanceSQLite(tx, xm.Memory.ID); err != nil {
				return err
			}
			if err := insertProvenanceSQLite(tx, scope, xm.Provenance, now); err != nil {
				return err
			}
		}
		// Tombstone result rows.
		for _, t := range cs.Targets {
			if _, err := tx.Exec(
				`UPDATE memories SET status='deleted', updated_at=? WHERE tenant_id=? AND id=?`,
				now, scope.Tenant, t.ID,
			); err != nil {
				return fmt.Errorf("sqlitestore: commit rollback tombstone %q: %w", t.ID, err)
			}
		}

	case store.ActionConfirm:
		// Promote a pending_confirmation row to the given status (D-065, Phase 18).
		// CommitSet.Memory carries the new status; Targets are superseded.
		if err := confirmMemoryStatusSQLite(tx, scope, cs.Memory, now); err != nil {
			return fmt.Errorf("sqlitestore: commit confirm: %w", err)
		}
		if cs.FaultHook != nil {
			if err := cs.FaultHook(); err != nil {
				return err
			}
		}
		for _, t := range cs.Targets {
			if _, err := tx.Exec(
				`UPDATE memories SET status='superseded', superseded_by_id=?, updated_at=? WHERE tenant_id=? AND id=?`,
				cs.Memory.ID, now, scope.Tenant, t.ID,
			); err != nil {
				return fmt.Errorf("sqlitestore: commit confirm supersede %q: %w", t.ID, err)
			}
		}

	default:
		return fmt.Errorf("sqlitestore: commit: unknown action %q", cs.Action)
	}

	// Write all events in the same transaction (D-045).
	for _, ev := range cs.Events {
		if err := insertEventSQLite(tx, scope, ev, now); err != nil {
			return fmt.Errorf("sqlitestore: commit event: %w", err)
		}
	}
	return nil
}

// insertMemorySQLite inserts a new memory row within an existing tx.
func insertMemorySQLite(tx *sql.Tx, scope identity.Scope, mem store.Memory, now int64) error {
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
			 created_at, updated_at, content_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
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

// updateMemoryContentSQLite performs a targeted UPDATE that touches ONLY the
// four mutable content fields (content, context, content_hash, updated_at).
// All counters, trust_source, stability, importance, confidence, validity window,
// episode_id, and privacy_zone are left unchanged — implementing the C1 fix.
func updateMemoryContentSQLite(tx *sql.Tx, scope identity.Scope, mem store.Memory, now int64) error {
	if mem.UpdatedAt == 0 {
		mem.UpdatedAt = now
	}
	_, err := tx.Exec(`
		UPDATE memories SET
			content = ?, context = ?, content_hash = ?, updated_at = ?
		WHERE tenant_id = ? AND id = ?`,
		mem.Content, mem.Context, nullStr(mem.ContentHash),
		mem.UpdatedAt, scope.Tenant, mem.ID,
	)
	return err
}

// insertJunctionsSQLite inserts entities, keywords, queries for a memory.
func insertJunctionsSQLite(tx *sql.Tx, scope identity.Scope, memID string, entities, keywords, queries, topics []string) error {
	for _, e := range entities {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO memory_entities (id, memory_id, entity, tenant_id) VALUES (?,?,?,?)`,
			ulid.Make().String(), memID, e, scope.Tenant,
		); err != nil {
			return fmt.Errorf("sqlitestore: insert entity: %w", err)
		}
	}
	for _, tp := range topics {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO memory_topics (id, memory_id, topic_key, tenant_id) VALUES (?,?,?,?)`,
			ulid.Make().String(), memID, tp, scope.Tenant,
		); err != nil {
			return fmt.Errorf("sqlitestore: insert topic: %w", err)
		}
	}
	for _, k := range keywords {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO memory_keywords (id, memory_id, keyword, tenant_id) VALUES (?,?,?,?)`,
			ulid.Make().String(), memID, k, scope.Tenant,
		); err != nil {
			return fmt.Errorf("sqlitestore: insert keyword: %w", err)
		}
	}
	for _, q := range queries {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO memory_queries (id, memory_id, query, tenant_id) VALUES (?,?,?,?)`,
			ulid.Make().String(), memID, q, scope.Tenant,
		); err != nil {
			return fmt.Errorf("sqlitestore: insert query: %w", err)
		}
	}
	return nil
}

// deleteJunctionsSQLite removes all junction rows for a memory (used on update).
func deleteJunctionsSQLite(tx *sql.Tx, memID string) error {
	for _, table := range []string{"memory_entities", "memory_keywords", "memory_queries", "memory_topics"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE memory_id = ?`, memID); err != nil { //nolint:gosec
			return fmt.Errorf("sqlitestore: delete junctions from %s: %w", table, err)
		}
	}
	return nil
}

// insertProvenanceSQLite inserts provenance rows within an existing tx.
func insertProvenanceSQLite(tx *sql.Tx, scope identity.Scope, rows []store.Provenance, now int64) error {
	for _, p := range rows {
		if p.ID == "" {
			// Defensive: an empty PK would collide on the second row and be
			// silently dropped by the conflict-ignoring insert.
			p.ID = ulid.Make().String()
		}
		if p.CreatedAt == 0 {
			p.CreatedAt = now
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO provenance (id, memory_id, record_id, span_start, span_end, tenant_id, created_at)
			VALUES (?,?,?,?,?,?,?)`,
			p.ID, p.MemoryID, p.RecordID, p.SpanStart, p.SpanEnd, scope.Tenant, p.CreatedAt,
		); err != nil {
			return fmt.Errorf("sqlitestore: insert provenance: %w", err)
		}
	}
	return nil
}

// insertLinksSQLite inserts link rows within an existing tx.
func insertLinksSQLite(tx *sql.Tx, scope identity.Scope, links []store.Link, now int64) error {
	for _, l := range links {
		if l.CreatedAt == 0 {
			l.CreatedAt = now
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO links (id, tenant_id, from_memory, to_memory, type, source, confidence, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			l.ID, scope.Tenant, l.FromMemory, l.ToMemory, l.Type, l.Source, l.Confidence, l.CreatedAt,
		); err != nil {
			return fmt.Errorf("sqlitestore: insert link: %w", err)
		}
	}
	return nil
}

// restoreMemoryFullSQLite performs a full-row UPDATE of ALL memory scalar fields
// (including status, superseded_by_id) for ActionRollback (Phase 18, D-064).
func restoreMemoryFullSQLite(tx *sql.Tx, scope identity.Scope, mem store.Memory, now int64) error {
	if mem.UpdatedAt == 0 {
		mem.UpdatedAt = now
	}
	_, err := tx.Exec(`
		UPDATE memories SET
			kind=?, content=?, context=?, status=?,
			importance=?, confidence=?, trust_source=?,
			match_count=?, inject_count=?, use_count=?, save_count=?, fail_count=?, noise_count=?,
			stability=?, last_accessed_at=?, valid_from=?, valid_until=?,
			episode_id=?, supersedes_id=?, superseded_by_id=?, privacy_zone=?,
			updated_at=?, content_hash=?
		WHERE tenant_id=? AND id=?`,
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

// deleteProvenanceSQLite removes all provenance rows for a memory (used on rollback).
func deleteProvenanceSQLite(tx *sql.Tx, memID string) error {
	_, err := tx.Exec(`DELETE FROM provenance WHERE memory_id = ?`, memID)
	return err
}

// confirmMemoryStatusSQLite sets status (and updated_at) for ActionConfirm (Phase 18, D-065).
// superseded_by_id uses the raw string value (empty string) because the column is NOT NULL DEFAULT ”.
func confirmMemoryStatusSQLite(tx *sql.Tx, scope identity.Scope, mem store.Memory, now int64) error {
	if mem.UpdatedAt == 0 {
		mem.UpdatedAt = now
	}
	_, err := tx.Exec(
		`UPDATE memories SET status=?, superseded_by_id=?, updated_at=? WHERE tenant_id=? AND id=?`,
		mem.Status, mem.SupersededByID, mem.UpdatedAt, scope.Tenant, mem.ID,
	)
	return err
}

// insertEventSQLite writes one event row within an existing tx (D-045).
func insertEventSQLite(tx *sql.Tx, scope identity.Scope, ev store.Event, now int64) error {
	if ev.CreatedAt == 0 {
		ev.CreatedAt = now
	}
	if ev.Payload == "" {
		ev.Payload = "{}"
	}
	_, err := tx.Exec(`
		INSERT INTO events (id, tenant_id, project_id, user_id, session_id, type, subject_id, reason, payload, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		ev.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
		ev.Type, ev.SubjectID, ev.Reason, ev.Payload, ev.CreatedAt,
	)
	return err
}

// ListActiveForDecay returns at most limit active memories for the scope,
// ordered by (created_at, id) ascending. cursor is an opaque pagination token.
// Scope is tenant-only for the decay sweep (no project/user/session filtering).
func (m *memoryStore) ListActiveForDecay(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]store.Memory, string, error) {
	if scope.Tenant == "" {
		return nil, "", store.ErrScopeRequired
	}
	whereClause := "tenant_id = ? AND status = 'active'"
	args := []interface{}{scope.Tenant}

	if cursor != "" {
		ts, cid, perr := parseCursor(cursor)
		if perr != nil {
			return nil, "", perr
		}
		whereClause += " AND (created_at > ? OR (created_at = ? AND id > ?))"
		args = append(args, ts, ts, cid)
	}
	args = append(args, limit+1)

	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + ` ORDER BY created_at ASC, id ASC LIMIT ?` //nolint:gosec
	rows, err := m.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlitestore: list active for decay: %w", err)
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

// ListByKinds returns active memories in scope whose kind is one of kinds,
// ordered by (created_at, id) ascending (D-072 playbook view). Scope-enforced
// (P3); empty kinds returns an empty slice.
func (m *memoryStore) ListByKinds(ctx context.Context, scope identity.Scope, kinds []string) ([]store.Memory, error) {
	if len(kinds) == 0 {
		return []store.Memory{}, nil
	}
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	whereClause += " AND status = 'active'"
	placeholders := make([]string, len(kinds))
	for i, k := range kinds {
		placeholders[i] = "?"
		args = append(args, k)
	}
	whereClause += " AND kind IN (" + strings.Join(placeholders, ",") + ")"

	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + ` ORDER BY created_at ASC, id ASC` //nolint:gosec
	rows, err := m.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list memories by kinds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]store.Memory, 0)
	for rows.Next() {
		mem, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *mem)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListMemoriesByRecords returns active memories whose provenance references any of
// recordIDs, optionally filtered to kinds. DISTINCT by id, scope-enforced (P3),
// ordered (created_at, id) ASC. Backs Phase-24 causal candidate gathering.
func (m *memoryStore) ListMemoriesByRecords(ctx context.Context, scope identity.Scope, recordIDs []string, kinds []string) ([]store.Memory, error) {
	if len(recordIDs) == 0 {
		return []store.Memory{}, nil
	}
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	whereClause += " AND status = 'active'"
	if len(kinds) > 0 {
		kph := make([]string, len(kinds))
		for i, k := range kinds {
			kph[i] = "?"
			args = append(args, k)
		}
		whereClause += " AND kind IN (" + strings.Join(kph, ",") + ")"
	}
	// Reverse-provenance subquery: scope provenance by tenant too (belt-and-suspenders;
	// the outer scope already restricts to tenant memories).
	rph := make([]string, len(recordIDs))
	for i, rid := range recordIDs {
		rph[i] = "?"
		args = append(args, rid)
	}
	args = append(args, scope.Tenant)
	whereClause += " AND id IN (SELECT memory_id FROM provenance WHERE record_id IN (" +
		strings.Join(rph, ",") + ") AND tenant_id = ?)"

	q := `SELECT ` + memorySelectCols + ` FROM memories WHERE ` + whereClause + ` ORDER BY created_at ASC, id ASC` //nolint:gosec
	rows, err := m.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list memories by records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]store.Memory, 0)
	for rows.Next() {
		mem, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *mem)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SetValidUntil sets the valid_until field of a memory (unix millis).
// A value of 0 clears the field. Used by the decay sweep (D-058).
func (m *memoryStore) SetValidUntil(ctx context.Context, scope identity.Scope, id string, validUntil int64) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		_, err := tx.Exec(
			`UPDATE memories SET valid_until = ?, updated_at = ? WHERE tenant_id = ? AND id = ?`,
			validUntil, now, scope.Tenant, id,
		)
		return err
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
		&mem.CreatedAt, &mem.UpdatedAt, &mem.ContentHash,
	)
	if err != nil {
		return nil, err
	}
	return &mem, nil
}

// DistinctScopes returns the distinct (project_id, user_id) scopes with at least one active
// memory under scope (D-111). Tenant-scoped via buildScopeWhere; the consolidation sweep runs
// per returned scope so it never compares memories across users (P3).
func (m *memoryStore) DistinctScopes(ctx context.Context, scope identity.Scope) ([]identity.Scope, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	q := `SELECT DISTINCT COALESCE(project_id,''), COALESCE(user_id,'') FROM memories WHERE ` + whereClause + ` AND status = 'active'` //nolint:gosec // whereClause from controlled helper; values bound
	rows, err := m.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: distinct scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []identity.Scope
	for rows.Next() {
		var project, user string
		if err := rows.Scan(&project, &user); err != nil {
			return nil, fmt.Errorf("sqlitestore: scan distinct scope: %w", err)
		}
		out = append(out, identity.Scope{Tenant: scope.Tenant, Project: project, User: user})
	}
	return out, rows.Err()
}
