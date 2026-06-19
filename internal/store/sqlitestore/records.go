package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type recordStore struct{ s *sqliteStore }

// Append stores records. Duplicate IDs are silently ignored (idempotent).
func (r *recordStore) Append(ctx context.Context, scope identity.Scope, records []store.Record) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	if len(records) == 0 {
		return nil
	}
	return r.s.exec(ctx, func(tx *sql.Tx) error {
		for _, rec := range records {
			now := time.Now().UnixMilli()
			createdAt := rec.CreatedAt
			if createdAt == 0 {
				createdAt = now
			}
			_, err := tx.Exec(`
				INSERT OR IGNORE INTO records
					(id, tenant_id, project_id, user_id, session_id, branch_id,
					 role, content, source_agent, response_id, outcome, outcome_detail,
					 token_estimate, occurred_at, created_at, processed_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				rec.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
				rec.BranchID, rec.Role, rec.Content, rec.SourceAgent, rec.ResponseID,
				rec.Outcome, rec.OutcomeDetail, rec.TokenEstimate,
				rec.OccurredAt, createdAt, rec.ProcessedAt,
			)
			if err != nil {
				return fmt.Errorf("sqlitestore: append record %q: %w", rec.ID, err)
			}
		}
		return nil
	})
}

// Get returns a record by ID within scope.
func (r *recordStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Record, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, id)
	qr := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), branch_id, role, content, source_agent, response_id, outcome, outcome_detail, token_estimate, occurred_at, created_at, processed_at FROM records WHERE ` + whereClause + ` AND id = ?` //nolint:gosec // whereClause is built from controlled helper, not user input
	row := r.s.rdb.QueryRowContext(ctx, qr, args...)
	rec, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return rec, err
}

// ListBySession returns records for a session/branch, ordered by (occurred_at, id) ASC.
// cursor is an opaque "<millis>:<id>" pagination token (Q1 composite cursor);
// pass "" for the first page.
func (r *recordStore) ListBySession(ctx context.Context, scope identity.Scope, sessionID, branchID string, limit int, cursor string) ([]store.Record, string, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, "", err
	}
	if sessionID != "" {
		whereClause += " AND session_id = ?"
		args = append(args, sessionID)
	}
	if branchID != "" {
		whereClause += " AND branch_id = ?"
		args = append(args, branchID)
	}
	// Q1: composite cursor — (occurred_at, id) tuple comparison avoids dropping
	// rows when multiple records share the same occurred_at timestamp.
	if cursor != "" {
		ts, cid, perr := parseCursor(cursor)
		if perr != nil {
			return nil, "", perr
		}
		whereClause += " AND (occurred_at > ? OR (occurred_at = ? AND id > ?))"
		args = append(args, ts, ts, cid)
	}
	args = append(args, limit+1)

	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), branch_id, role, content, source_agent, response_id, outcome, outcome_detail, token_estimate, occurred_at, created_at, processed_at FROM records WHERE ` + whereClause + ` ORDER BY occurred_at ASC, id ASC LIMIT ?` //nolint:gosec // whereClause is built from controlled helper, not user input
	rows, err := r.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("sqlitestore: list records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	recs, nextCursor, err := collectRecords(rows, limit)
	return recs, nextCursor, err
}

// ListUnprocessed returns records where processed_at == 0 and occurred_at < olderThan.
func (r *recordStore) ListUnprocessed(ctx context.Context, olderThan int64, limit int) ([]store.Record, error) {
	rows, err := r.s.rdb.QueryContext(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        branch_id, role, content, source_agent, response_id, outcome, outcome_detail,
		        token_estimate, occurred_at, created_at, processed_at
		 FROM records
		 WHERE processed_at = 0 AND occurred_at < ?
		 ORDER BY occurred_at ASC
		 LIMIT ?`,
		olderThan, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list unprocessed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// MarkProcessed sets processed_at for the given IDs.
func (r *recordStore) MarkProcessed(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	return r.s.exec(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(
				`UPDATE records SET processed_at = ? WHERE id = ?`, now, id,
			); err != nil {
				return fmt.Errorf("sqlitestore: mark processed %q: %w", id, err)
			}
		}
		return nil
	})
}

// CountRecordsSince returns the count of records in scope with created_at >
// sinceMs. Scope-indexed by tenant/project/user; used for ActivityTurns
// approximation in the Phase 10 scoring decay function.
func (r *recordStore) CountRecordsSince(ctx context.Context, scope identity.Scope, sinceMs int64) (int64, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return 0, err
	}
	args = append(args, sinceMs)
	q := `SELECT COUNT(*) FROM records WHERE ` + whereClause + ` AND created_at > ?` //nolint:gosec
	row := r.s.rdb.QueryRowContext(ctx, q, args...)
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("sqlitestore: count records since: %w", err)
	}
	return count, nil
}

func (r *recordStore) RecordCreatedAtsSince(ctx context.Context, scope identity.Scope, sinceMs int64, limit int) ([]int64, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20000
	}
	args = append(args, sinceMs, limit)
	q := `SELECT created_at FROM records WHERE ` + whereClause + ` AND created_at > ? ORDER BY created_at ASC LIMIT ?` //nolint:gosec
	rows, err := r.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: record created_ats since: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := make([]int64, 0, 256)
	for rows.Next() {
		var ts int64
		if err := rows.Scan(&ts); err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
	return out, rows.Err()
}

// GetMany returns records for the given IDs within scope. IDs not found are
// silently omitted; order matches the order of ids.
func (r *recordStore) GetMany(ctx context.Context, scope identity.Scope, ids []string) ([]store.Record, error) {
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
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),` + //nolint:gosec
		` branch_id, role, content, source_agent, response_id, outcome, outcome_detail,` +
		` token_estimate, occurred_at, created_at, processed_at` +
		` FROM records WHERE ` + whereClause + ` AND id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := r.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: records get many: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	byID := make(map[string]store.Record)
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlitestore: records get many row: %w", err)
		}
		byID[rec.ID] = *rec
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]store.Record, 0, len(ids))
	for _, id := range ids {
		if rec, ok := byID[id]; ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// ListByOutcome returns scope's records whose outcome ∈ outcomes and occurred_at
// > since, grouped into trajectories by (session_id, branch_id, occurred_at, id),
// capped at limit. Scope-parameterized (P3). Used by the reflection sweep.
func (r *recordStore) ListByOutcome(ctx context.Context, scope identity.Scope, outcomes []string, since int64, limit int) ([]store.Record, error) {
	if len(outcomes) == 0 {
		return nil, nil
	}
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	placeholders := make([]string, len(outcomes))
	for i, o := range outcomes {
		placeholders[i] = "?"
		args = append(args, o)
	}
	whereClause += " AND outcome IN (" + strings.Join(placeholders, ",") + ") AND occurred_at > ?"
	args = append(args, since, limit)
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),` + //nolint:gosec // whereClause from controlled helper, placeholders are bound
		` branch_id, role, content, source_agent, response_id, outcome, outcome_detail,` +
		` token_estimate, occurred_at, created_at, processed_at` +
		` FROM records WHERE ` + whereClause +
		` ORDER BY session_id ASC, branch_id ASC, occurred_at ASC, id ASC LIMIT ?`
	rows, err := r.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list by outcome: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// DistinctSessions returns scope's closed (session_id, branch_id) groups whose
// latest record is at/before idleBefore, with the time-range + count. Used by the
// episode boundary-detection sweep (Phase 22).
func (r *recordStore) DistinctSessions(ctx context.Context, scope identity.Scope, idleBefore int64, limit int) ([]store.SessionInfo, error) {
	whereClause, args, err := buildScopeWhere(scope)
	if err != nil {
		return nil, err
	}
	args = append(args, idleBefore, limit)
	q := `SELECT COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), branch_id,` + //nolint:gosec // whereClause from controlled helper; values are bound
		` MIN(occurred_at), MAX(occurred_at), COUNT(*) FROM records WHERE ` + whereClause +
		` AND session_id IS NOT NULL AND session_id <> '' GROUP BY project_id, user_id, session_id, branch_id` +
		` HAVING MAX(occurred_at) <= ? ORDER BY MAX(occurred_at) ASC, session_id ASC, branch_id ASC LIMIT ?`
	rows, err := r.s.rdb.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: distinct sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.SessionInfo
	for rows.Next() {
		var si store.SessionInfo
		if err := rows.Scan(&si.ProjectID, &si.UserID, &si.SessionID, &si.BranchID, &si.FirstOccurred, &si.LastOccurred, &si.RecordCount); err != nil {
			return nil, fmt.Errorf("sqlitestore: scan session info: %w", err)
		}
		out = append(out, si)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanRecord(row rowScanner) (*store.Record, error) {
	var rec store.Record
	err := row.Scan(
		&rec.ID, &rec.TenantID, &rec.ProjectID, &rec.UserID, &rec.SessionID,
		&rec.BranchID, &rec.Role, &rec.Content, &rec.SourceAgent, &rec.ResponseID,
		&rec.Outcome, &rec.OutcomeDetail, &rec.TokenEstimate,
		&rec.OccurredAt, &rec.CreatedAt, &rec.ProcessedAt,
	)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// collectRecords collects rows and produces a composite cursor from the last
// item on the page (Q1: cursor = last item, filter is strictly-after).
func collectRecords(rows *sql.Rows, limit int) ([]store.Record, string, error) {
	var out []store.Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var nextCursor string
	if len(out) > limit {
		// Cursor is the last item of the current page (not the overflow item),
		// so page N+1 uses (occurred_at, id) > cursor to get items strictly after.
		nextCursor = encodeCursor(out[limit-1].OccurredAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, nextCursor, nil
}
