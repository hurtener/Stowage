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

type injectionStore struct{ s *sqliteStore }

func (inj *injectionStore) Append(ctx context.Context, scope identity.Scope, rows []store.Injection) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	if len(rows) == 0 {
		return nil
	}
	return inj.s.exec(ctx, func(tx *sql.Tx) error {
		for _, r := range rows {
			wasCited := 0
			if r.WasCited {
				wasCited = 1
			}
			_, err := tx.Exec(`
				INSERT OR IGNORE INTO injections
					(id, tenant_id, project_id, user_id, session_id, response_id, memory_id,
					 rank, score, lane, was_cited, feedback, query_sig, created_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				r.ID, scope.Tenant, nullStr(scope.Project), nullStr(scope.User), nullStr(scope.Session),
				r.ResponseID, r.MemoryID,
				r.Rank, r.Score, r.Lane, wasCited, r.Feedback, r.QuerySig, r.CreatedAt,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (inj *injectionStore) ListByResponse(ctx context.Context, scope identity.Scope, responseID string) ([]store.Injection, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := inj.s.rdb.QueryContext(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        response_id, memory_id, rank, score, lane, was_cited, feedback, query_sig, created_at
		   FROM injections
		  WHERE tenant_id = ? AND response_id = ?
		  ORDER BY rank ASC`,
		scope.Tenant, responseID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	return scanInjections(rows)
}

func (inj *injectionStore) Get(ctx context.Context, scope identity.Scope, id string) (*store.Injection, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	row := inj.s.rdb.QueryRowContext(ctx,
		`SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		        response_id, memory_id, rank, score, lane, was_cited, feedback, query_sig, created_at
		   FROM injections
		  WHERE tenant_id = ? AND id = ?`,
		scope.Tenant, id,
	)
	r, err := scanInjection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return r, err
}

// MarkWrongCitation atomically sets injection.feedback='wrong_citation' and
// increments memory.noise_count + fail_count + last_accessed_at in one write tx.
func (inj *injectionStore) MarkWrongCitation(ctx context.Context, scope identity.Scope, injectionID string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return inj.s.exec(ctx, func(tx *sql.Tx) error {
		// 1. Resolve memory_id from the injection (within the same write tx).
		var memoryID string
		err := tx.QueryRow(
			`SELECT memory_id FROM injections WHERE tenant_id = ? AND id = ?`,
			scope.Tenant, injectionID,
		).Scan(&memoryID)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		// 2. Mark injection feedback.
		if _, err = tx.Exec(
			`UPDATE injections SET feedback = 'wrong_citation' WHERE tenant_id = ? AND id = ?`,
			scope.Tenant, injectionID,
		); err != nil {
			return err
		}
		// 3. Bump memory noise+fail + touch last_accessed_at.
		now := time.Now().UnixMilli()
		_, err = tx.Exec(
			`UPDATE memories SET noise_count = noise_count + 1, fail_count = fail_count + 1, last_accessed_at = ? WHERE tenant_id = ? AND id = ?`,
			now, scope.Tenant, memoryID,
		)
		return err
	})
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
	// Build the IN placeholder list. Args: tenant, sinceMs, then each memory ID.
	placeholders := make([]string, len(memoryIDs))
	args := make([]any, 0, len(memoryIDs)+2)
	args = append(args, scope.Tenant, sinceMs)
	for i, id := range memoryIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	//nolint:gosec // placeholders is a join of static "?" markers, not user input; all values are bound args
	query := `SELECT memory_id, COUNT(DISTINCT query_sig)
	            FROM injections
	           WHERE tenant_id = ? AND created_at >= ? AND query_sig <> ''
	             AND memory_id IN (` + strings.Join(placeholders, ",") + `)
	           GROUP BY memory_id`
	rows, err := inj.s.rdb.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
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

// --- scan helpers ------------------------------------------------------------

type injectionScanner interface {
	Scan(dest ...interface{}) error
}

func scanInjection(s injectionScanner) (*store.Injection, error) {
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

func scanInjections(rows *sql.Rows) ([]store.Injection, error) {
	var out []store.Injection
	for rows.Next() {
		r, err := scanInjection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// --- feedbackColumn + ApplyFeedback on memoryStore --------------------------

// feedbackColumn maps a feedback signal to the counter column it increments.
var feedbackColumn = map[string]string{
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
	col, ok := feedbackColumn[signal]
	if !ok {
		return fmt.Errorf("sqlitestore: unknown feedback signal %q (want use|save|fail|noise)", signal)
	}
	now := time.Now().UnixMilli()
	return m.s.exec(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec( //nolint:gosec
			`UPDATE memories SET `+col+` = `+col+` + 1, last_accessed_at = ? WHERE tenant_id = ? AND id = ?`,
			now, scope.Tenant, memoryID,
		)
		return err
	})
}
