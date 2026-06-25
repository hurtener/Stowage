package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type recordStore struct{ s *pgStore }

// scopeOrRecord implements the scope-authoritative write rule (D-124): scope wins when set,
// the per-record value fills an empty dimension — a record can never override a declared scope (P3).
func scopeOrRecord(scopeVal, recVal string) string {
	if scopeVal != "" {
		return scopeVal
	}
	return recVal
}

func (r *recordStore) Append(ctx context.Context, scope identity.Scope, records []store.Record) error {
	if scope.Tenant == "" { // S1: fail closed
		return store.ErrScopeRequired
	}
	if len(records) == 0 {
		return nil
	}
	tx, err := r.s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, rec := range records {
		now := time.Now().UnixMilli()
		createdAt := rec.CreatedAt
		if createdAt == 0 {
			createdAt = now
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO records
				(id, tenant_id, project_id, user_id, session_id, branch_id,
				 role, content, source_agent, response_id, outcome, outcome_detail,
				 token_estimate, occurred_at, created_at, processed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
			ON CONFLICT(id) DO NOTHING`,
			// Scope-authoritative write (D-124): scope dimension wins; record fills an empty one (P3).
			rec.ID, scope.Tenant, nullStr(scopeOrRecord(scope.Project, rec.ProjectID)), nullStr(scopeOrRecord(scope.User, rec.UserID)), nullStr(scopeOrRecord(scope.Session, rec.SessionID)),
			rec.BranchID, rec.Role, rec.Content, rec.SourceAgent, rec.ResponseID,
			rec.Outcome, rec.OutcomeDetail, rec.TokenEstimate,
			rec.OccurredAt, createdAt, rec.ProcessedAt,
		)
		if err != nil {
			return fmt.Errorf("pgstore: append record %q: %w", rec.ID, err)
		}
	}
	return tx.Commit(ctx)
}

func (r *recordStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Record, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	args = append(args, id)
	row := r.s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        branch_id, role, content, source_agent, response_id, outcome, outcome_detail,
		        token_estimate, occurred_at, created_at, processed_at
		 FROM records WHERE `+whereClause+fmt.Sprintf(` AND id = $%d`, next),
		args...,
	)
	rec, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return rec, err
}

// ListBySession returns records ordered by (occurred_at, id) ASC.
// cursor is an opaque "<millis>:<id>" pagination token (Q1 composite cursor).
func (r *recordStore) ListBySession(ctx context.Context, scope identity.Scope, sessionID, branchID string, limit int, cursor string) ([]store.Record, string, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, "", err
	}
	if sessionID != "" {
		whereClause += fmt.Sprintf(` AND session_id = $%d`, next)
		args = append(args, sessionID)
		next++
	}
	if branchID != "" {
		whereClause += fmt.Sprintf(` AND branch_id = $%d`, next)
		args = append(args, branchID)
		next++
	}
	// Q1: composite cursor — PostgreSQL row-value comparison (occurred_at, id) > ($n, $m).
	if cursor != "" {
		ts, cid, perr := parseCursor(cursor)
		if perr != nil {
			return nil, "", perr
		}
		whereClause += fmt.Sprintf(` AND (occurred_at, id) > ($%d, $%d)`, next, next+1)
		args = append(args, ts, cid)
		next += 2
	}
	args = append(args, limit+1)

	rows, err := r.s.pool.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        branch_id, role, content, source_agent, response_id, outcome, outcome_detail,
		        token_estimate, occurred_at, created_at, processed_at
		 FROM records
		 WHERE `+whereClause+fmt.Sprintf(` ORDER BY occurred_at ASC, id ASC LIMIT $%d`, next),
		args...,
	)
	if err != nil {
		return nil, "", fmt.Errorf("pgstore: list records: %w", err)
	}
	defer rows.Close()
	return collectRecords(rows, limit)
}

func (r *recordStore) ListUnprocessed(ctx context.Context, olderThan int64, limit int) ([]store.Record, error) {
	rows, err := r.s.pool.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        branch_id, role, content, source_agent, response_id, outcome, outcome_detail,
		        token_estimate, occurred_at, created_at, processed_at
		 FROM records WHERE processed_at = 0 AND occurred_at < $1
		 ORDER BY occurred_at ASC LIMIT $2`,
		olderThan, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list unprocessed: %w", err)
	}
	defer rows.Close()
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

func (r *recordStore) MarkProcessed(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	tx, err := r.s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin mark processed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i, id := range ids {
		if _, err := tx.Exec(ctx,
			`UPDATE records SET processed_at = $1 WHERE id = $2`, now, id,
		); err != nil {
			return fmt.Errorf("pgstore: mark processed[%d]: %w", i, err)
		}
	}
	return tx.Commit(ctx)
}

// CountRecordsSince returns the count of records in scope with created_at >
// sinceMs. Scope-indexed by tenant/project/user; used for ActivityTurns
// approximation in the Phase 10 scoring decay function.
func (r *recordStore) CountRecordsSince(ctx context.Context, scope identity.Scope, sinceMs int64) (int64, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return 0, err
	}
	args = append(args, sinceMs)
	row := r.s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM records WHERE `+whereClause+fmt.Sprintf(` AND created_at > $%d`, next),
		args...,
	)
	var count int64
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("pgstore: count records since: %w", err)
	}
	return count, nil
}

func (r *recordStore) RecordCreatedAtsSince(ctx context.Context, scope identity.Scope, sinceMs int64, limit int) ([]int64, error) {
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20000
	}
	args = append(args, sinceMs, limit)
	q := `SELECT created_at FROM records WHERE ` + whereClause +
		fmt.Sprintf(` AND created_at > $%d ORDER BY created_at ASC LIMIT $%d`, next, next+1)
	rows, err := r.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: record created_ats since: %w", err)
	}
	defer rows.Close()
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
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	// Build $N, $N+1, ... placeholders for the IN clause.
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", next+i)
		args = append(args, id)
	}
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
	       branch_id, role, content, source_agent, response_id, outcome, outcome_detail,
	       token_estimate, occurred_at, created_at, processed_at
	  FROM records WHERE ` + whereClause + ` AND id IN (` + strings.Join(placeholders, ",") + `)` //nolint:gosec
	rows, err := r.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: records get many: %w", err)
	}
	defer rows.Close()
	byID := make(map[string]store.Record)
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("pgstore: records get many row: %w", err)
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
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	placeholders := make([]string, len(outcomes))
	for i, o := range outcomes {
		placeholders[i] = fmt.Sprintf("$%d", next+i)
		args = append(args, o)
	}
	sinceN := next + len(outcomes)
	limitN := sinceN + 1
	args = append(args, since, limit)
	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
	       branch_id, role, content, source_agent, response_id, outcome, outcome_detail,
	       token_estimate, occurred_at, created_at, processed_at
	  FROM records WHERE ` + whereClause +
		` AND outcome IN (` + strings.Join(placeholders, ",") + `)` +
		fmt.Sprintf(` AND occurred_at > $%d ORDER BY session_id ASC, branch_id ASC, occurred_at ASC, id ASC LIMIT $%d`, sinceN, limitN) //nolint:gosec
	rows, err := r.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list by outcome: %w", err)
	}
	defer rows.Close()
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
	whereClause, args, next, err := buildScopeWhere(scope, 1)
	if err != nil {
		return nil, err
	}
	idleN, limitN := next, next+1
	args = append(args, idleBefore, limit)
	q := `SELECT COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), branch_id, MIN(occurred_at), MAX(occurred_at), COUNT(*)
	      FROM records WHERE ` + whereClause + ` AND session_id IS NOT NULL AND session_id <> ''
	      GROUP BY project_id, user_id, session_id, branch_id
	      HAVING MAX(occurred_at) <= $` + fmt.Sprintf("%d", idleN) +
		` ORDER BY MAX(occurred_at) ASC, session_id ASC, branch_id ASC LIMIT $` + fmt.Sprintf("%d", limitN) //nolint:gosec
	rows, err := r.s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore: distinct sessions: %w", err)
	}
	defer rows.Close()
	var out []store.SessionInfo
	for rows.Next() {
		var si store.SessionInfo
		if err := rows.Scan(&si.ProjectID, &si.UserID, &si.SessionID, &si.BranchID, &si.FirstOccurred, &si.LastOccurred, &si.RecordCount); err != nil {
			return nil, fmt.Errorf("pgstore: scan session info: %w", err)
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
func collectRecords(rows pgx.Rows, limit int) ([]store.Record, string, error) {
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
		nextCursor = encodeCursor(out[limit-1].OccurredAt, out[limit-1].ID)
		out = out[:limit]
	}
	return out, nextCursor, nil
}
