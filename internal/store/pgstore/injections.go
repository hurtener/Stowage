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

type injectionStore struct{ s *pgStore }

func (inj *injectionStore) Append(ctx context.Context, scope identity.Scope, rows []store.Injection) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	if len(rows) == 0 {
		return nil
	}
	tx, err := inj.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, r := range rows {
		wasCited := 0
		if r.WasCited {
			wasCited = 1
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO injections
				(id, tenant_id, project_id, user_id, session_id, response_id, memory_id,
				 rank, score, lane, was_cited, feedback, query_sig, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (id) DO NOTHING`,
			r.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
			r.ResponseID, r.MemoryID,
			r.Rank, r.Score, r.Lane, wasCited, r.Feedback, r.QuerySig, r.CreatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (inj *injectionStore) ListByResponse(ctx context.Context, scope identity.Scope, responseID string) ([]store.Injection, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := inj.s.pool.Query(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        response_id, memory_id, rank, score, lane, was_cited, feedback, query_sig, created_at
		   FROM injections
		  WHERE tenant_id = $1 AND response_id = $2
		  ORDER BY rank ASC`,
		scope.Tenant, responseID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Injection
	for rows.Next() {
		r, err := scanInjectionPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (inj *injectionStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Injection, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	row := inj.s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        response_id, memory_id, rank, score, lane, was_cited, feedback, query_sig, created_at
		   FROM injections
		  WHERE tenant_id = $1 AND id = $2`,
		scope.Tenant, id,
	)
	r, err := scanInjectionPG(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return r, err
}

// MarkWrongCitation atomically sets injection.feedback='wrong_citation' and
// increments memory.noise_count + fail_count + last_accessed_at in one pgx tx.
func (inj *injectionStore) MarkWrongCitation(ctx context.Context, scope identity.Scope, injectionID string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	tx, err := inj.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Resolve memory_id within the same transaction.
	var memoryID string
	err = tx.QueryRow(ctx,
		`SELECT memory_id FROM injections WHERE tenant_id = $1 AND id = $2`,
		scope.Tenant, injectionID,
	).Scan(&memoryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	// 2. Mark injection feedback.
	if _, err = tx.Exec(ctx,
		`UPDATE injections SET feedback = 'wrong_citation' WHERE tenant_id = $1 AND id = $2`,
		scope.Tenant, injectionID,
	); err != nil {
		return err
	}
	// 3. Bump memory noise+fail + touch last_accessed_at.
	now := time.Now().UnixMilli()
	if _, err = tx.Exec(ctx,
		`UPDATE memories SET noise_count = noise_count + 1, fail_count = fail_count + 1, last_accessed_at = $1 WHERE tenant_id = $2 AND id = $3`,
		now, scope.Tenant, memoryID,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// HubSignals returns the count of DISTINCT non-empty query_sig values per memory
// across injections created at or after sinceMs, scoped to the tenant (P3). The
// durable replacement for the former per-process hub LRU (D-092).
func (inj *injectionStore) HubSignals(ctx context.Context, scope identity.Scope, memoryIDs []string, sinceMs int64) (map[string]int, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	out := make(map[string]int, len(memoryIDs))
	if len(memoryIDs) == 0 {
		return out, nil
	}
	rows, err := inj.s.pool.Query(ctx,
		`SELECT memory_id, COUNT(DISTINCT query_sig)
		   FROM injections
		  WHERE tenant_id = $1 AND created_at >= $2 AND query_sig <> ''
		    AND memory_id = ANY($3)
		  GROUP BY memory_id`,
		scope.Tenant, sinceMs, memoryIDs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// --- pgRowScanner is the common interface for pgx.Row and pgx.Rows -----------

type pgRowScanner interface {
	Scan(dest ...any) error
}

func scanInjectionPG(s pgRowScanner) (*store.Injection, error) {
	var r store.Injection
	var wasCited int
	err := s.Scan(
		&r.ID, &r.TenantID, &r.ProjectID, &r.UserID, &r.SessionID,
		&r.ResponseID, &r.MemoryID, &r.Rank, &r.Score, &r.Lane,
		&wasCited, &r.Feedback, &r.QuerySig, &r.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.WasCited = wasCited != 0
	return &r, nil
}

// --- feedbackColumn + ApplyFeedback on pgstore memoryStore ------------------

// pgFeedbackColumn maps a feedback signal name to its counter column.
var pgFeedbackColumn = map[string]string{
	"use":   "use_count",
	"save":  "save_count",
	"fail":  "fail_count",
	"noise": "noise_count",
}

// ApplyFeedback atomically increments the counter for signal and touches
// last_accessed_at on the memory row.
func (m *memoryStore) ApplyFeedback(ctx context.Context, scope identity.Scope, memoryID, signal string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	col, ok := pgFeedbackColumn[signal]
	if !ok {
		return fmt.Errorf("pgstore: unknown feedback signal %q (want use|save|fail|noise)", signal)
	}
	now := time.Now().UnixMilli()
	_, err := m.s.pool.Exec(ctx,
		fmt.Sprintf( //nolint:gosec
			`UPDATE memories SET %s = %s + 1, last_accessed_at = $1 WHERE tenant_id = $2 AND id = $3`,
			col, col,
		),
		now, scope.Tenant, memoryID,
	)
	return err
}
