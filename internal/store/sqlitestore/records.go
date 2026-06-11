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

type recordStore struct{ s *sqliteStore }

// Append stores records. Duplicate IDs are silently ignored (idempotent).
func (r *recordStore) Append(ctx context.Context, scope identity.Scope, records []store.Record) error {
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
	whereClause, args := buildScopeWhere(scope)
	args = append(args, id)
	qr := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), branch_id, role, content, source_agent, response_id, outcome, outcome_detail, token_estimate, occurred_at, created_at, processed_at FROM records WHERE ` + whereClause + ` AND id = ?` //nolint:gosec // whereClause is built from controlled helper, not user input
	row := r.s.rdb.QueryRowContext(ctx, qr, args...)
	rec, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return rec, err
}

// ListBySession returns records for a session/branch, ordered by occurred_at ASC.
// If sessionID or branchID is empty, that filter is omitted.
func (r *recordStore) ListBySession(ctx context.Context, scope identity.Scope, sessionID, branchID string, limit int, cursor string) ([]store.Record, string, error) {
	whereClause, args := buildScopeWhere(scope)
	if sessionID != "" {
		whereClause += " AND session_id = ?"
		args = append(args, sessionID)
	}
	if branchID != "" {
		whereClause += " AND branch_id = ?"
		args = append(args, branchID)
	}
	if cursor != "" {
		whereClause += " AND occurred_at > (SELECT occurred_at FROM records WHERE id = ?)"
		args = append(args, cursor)
	}
	args = append(args, limit+1)

	q := `SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), branch_id, role, content, source_agent, response_id, outcome, outcome_detail, token_estimate, occurred_at, created_at, processed_at FROM records WHERE ` + whereClause + ` ORDER BY occurred_at ASC LIMIT ?` //nolint:gosec // whereClause is built from controlled helper, not user input
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
		nextCursor = out[limit].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}
