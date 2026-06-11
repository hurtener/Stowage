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

type recordStore struct{ s *pgStore }

func (r *recordStore) Append(ctx context.Context, scope identity.Scope, records []store.Record) error {
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
			rec.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
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
	whereClause, args, next := buildScopeWhere(scope, 1)
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

func (r *recordStore) ListBySession(ctx context.Context, scope identity.Scope, sessionID, branchID string, limit int, cursor string) ([]store.Record, string, error) {
	whereClause, args, next := buildScopeWhere(scope, 1)
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
	if cursor != "" {
		whereClause += fmt.Sprintf(` AND occurred_at > (SELECT occurred_at FROM records WHERE id = $%d)`, next)
		args = append(args, cursor)
		next++
	}
	args = append(args, limit+1)

	rows, err := r.s.pool.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        branch_id, role, content, source_agent, response_id, outcome, outcome_detail,
		        token_estimate, occurred_at, created_at, processed_at
		 FROM records
		 WHERE `+whereClause+fmt.Sprintf(` ORDER BY occurred_at ASC LIMIT $%d`, next),
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
		nextCursor = out[limit].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}
