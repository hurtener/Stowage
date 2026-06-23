package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/oklog/ulid/v2"
)

type opsStore struct{ s *sqliteStore }

func (o *opsStore) PutDeadLetter(ctx context.Context, d store.DeadLetter) error {
	return o.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if d.CreatedAt == 0 {
			d.CreatedAt = now
		}
		if d.ID == "" {
			d.ID = ulid.Make().String()
		}
		_, err := tx.Exec(`
			INSERT INTO dead_letters (id, stage, item_id, error, attempts, resolved_at, created_at)
			VALUES (?,?,?,?,?,?,?)`,
			d.ID, d.Stage, d.ItemID, d.Error, d.Attempts, d.ResolvedAt, d.CreatedAt,
		)
		return err
	})
}

func (o *opsStore) ListDeadLetters(ctx context.Context, stage string, limit int) ([]store.DeadLetter, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if stage == "" {
		rows, err = o.s.rdb.QueryContext(ctx,
			`SELECT id, stage, item_id, error, attempts, resolved_at, created_at
			 FROM dead_letters WHERE resolved_at = 0
			 ORDER BY created_at ASC LIMIT ?`,
			limit,
		)
	} else {
		rows, err = o.s.rdb.QueryContext(ctx,
			`SELECT id, stage, item_id, error, attempts, resolved_at, created_at
			 FROM dead_letters WHERE stage = ? AND resolved_at = 0
			 ORDER BY created_at ASC LIMIT ?`,
			stage, limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list dead letters: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.DeadLetter
	for rows.Next() {
		var d store.DeadLetter
		if err := rows.Scan(&d.ID, &d.Stage, &d.ItemID, &d.Error, &d.Attempts, &d.ResolvedAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (o *opsStore) ResolveDeadLetter(ctx context.Context, id string, resolvedAt int64) error {
	return o.s.exec(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE dead_letters SET resolved_at = ? WHERE id = ? AND resolved_at = 0`,
			resolvedAt, id,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: resolve dead letter %q: %w", id, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("%w: dead letter %q", store.ErrNotFound, id)
		}
		return nil
	})
}

// CheckAndSetJobMarker atomically checks + sets (job, marker).
// Returns true if the marker was newly set (job should run).
func (o *opsStore) CheckAndSetJobMarker(ctx context.Context, job, marker string, ranAt int64) (bool, error) {
	var set bool
	err := o.s.exec(ctx, func(tx *sql.Tx) error {
		var existing int64
		err := tx.QueryRow(
			`SELECT ran_at FROM job_markers WHERE job = ? AND marker = ?`, job, marker,
		).Scan(&existing)
		if err == nil {
			// Already set.
			set = false
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlitestore: check job marker: %w", err)
		}
		// Not set — insert it.
		id := ulid.Make().String()
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO job_markers (id, job, marker, ran_at) VALUES (?,?,?,?)`,
			id, job, marker, ranAt,
		); err != nil {
			return fmt.Errorf("sqlitestore: set job marker: %w", err)
		}
		set = true
		return nil
	})
	return set, err
}

// AdvisoryLock is a no-op on SQLite (returns a no-op release func).
func (o *opsStore) AdvisoryLock(_ context.Context, _ int64) (func() error, error) {
	return func() error { return nil }, nil
}

// DeleteUserData cascades a DSAR erasure of ALL data for (tenant, user) in one
// write transaction, FK-safe (children before parents), and emits a tenant-scoped
// `user.purged` audit event. This is the ONLY path that deletes verbatim records
// (P1 exception, D-098). See OpsStore.DeleteUserData for the full contract.
func (o *opsStore) DeleteUserData(ctx context.Context, scope identity.Scope) (store.DSARCounts, error) {
	if scope.Tenant == "" || scope.User == "" {
		return store.DSARCounts{}, store.ErrScopeRequired
	}
	var c store.DSARCounts
	err := o.s.exec(ctx, func(tx *sql.Tx) error {
		t, u := scope.Tenant, scope.User

		// del records the row count into dst; the first failure short-circuits the
		// rest and is surfaced once after the run (the whole closure is one tx, so a
		// later return rolls everything back).
		var derr error
		del := func(dst *int64, query string, args ...any) {
			if derr != nil {
				return
			}
			res, err := tx.Exec(query, args...)
			if err != nil {
				derr = fmt.Errorf("sqlitestore: dsar delete: %w", err)
				return
			}
			n, _ := res.RowsAffected()
			*dst = n
		}

		// 1. Children of the user's memories (+ cross-user rows referencing them, so
		//    the FK-restricted memories/records deletes below never fail).
		del(&c.Provenance,
			`DELETE FROM provenance
			   WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?)
			      OR record_id IN (SELECT id FROM records  WHERE tenant_id=? AND user_id=?)`,
			t, u, t, u)
		del(&c.Entities,
			`DELETE FROM memory_entities WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?)`,
			t, u)
		del(&c.Keywords,
			`DELETE FROM memory_keywords WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?)`,
			t, u)
		del(&c.Queries,
			`DELETE FROM memory_queries WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?)`,
			t, u)
		del(&c.MemoryTopics,
			`DELETE FROM memory_topics WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?)`,
			t, u)
		del(&c.Vectors,
			`DELETE FROM memory_vectors WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?)`,
			t, u)
		del(&c.Links,
			`DELETE FROM links
			   WHERE tenant_id=? AND (from_memory IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?)
			                       OR  to_memory  IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?))`,
			t, t, u, t, u)
		del(&c.Feedback,
			`DELETE FROM feedback
			   WHERE tenant_id=? AND memory_id IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?)`,
			t, t, u)
		del(&c.Injections,
			`DELETE FROM injections
			   WHERE tenant_id=? AND (user_id=? OR memory_id IN (SELECT id FROM memories WHERE tenant_id=? AND user_id=?))`,
			t, u, t, u)

		// 2. user_id-scoped tables (no memory FK; order among these is free).
		del(&c.Topics, `DELETE FROM topics WHERE tenant_id=? AND user_id=?`, t, u)
		del(&c.Suggestions, `DELETE FROM suggestions WHERE tenant_id=? AND user_id=?`, t, u)
		del(&c.BufferItems, `DELETE FROM buffer_items WHERE tenant_id=? AND user_id=?`, t, u)
		del(&c.ScopeSettings, `DELETE FROM scope_settings WHERE tenant_id=? AND user_id=?`, t, u)
		del(&c.GroupMembers, `DELETE FROM group_members WHERE tenant_id=? AND user_id=?`, t, u)
		del(&c.Grants, `DELETE FROM grants WHERE tenant_id=? AND user_id=?`, t, u)
		del(&c.Branches, `DELETE FROM branches WHERE tenant_id=? AND user_id=?`, t, u)
		del(&c.Episodes, `DELETE FROM episodes WHERE tenant_id=? AND user_id=?`, t, u)

		// 3. Parents — memories then the verbatim records (P1 exception, D-098).
		del(&c.Memories, `DELETE FROM memories WHERE tenant_id=? AND user_id=?`, t, u)
		del(&c.Records, `DELETE FROM records WHERE tenant_id=? AND user_id=?`, t, u)

		// 4. The user's own events. The user.purged event below is emitted at TENANT
		//    scope (user_id NULL) so it survives this delete.
		del(&c.Events, `DELETE FROM events WHERE tenant_id=? AND user_id=?`, t, u)

		if derr != nil {
			return derr
		}

		// 5. Emit the audit event at tenant scope (the purged user is the subject).
		return insertDSARPurgedEventSQLite(tx, scope, c, time.Now().UnixMilli())
	})
	if err != nil {
		return store.DSARCounts{}, err
	}
	return c, nil
}

// insertDSARPurgedEventSQLite writes the user.purged audit event at TENANT scope
// (so it is not itself deleted by the user's events purge), with the per-table
// counts and the purged user id in the JSON payload.
func insertDSARPurgedEventSQLite(tx *sql.Tx, scope identity.Scope, c store.DSARCounts, now int64) error {
	payload, err := json.Marshal(struct {
		UserID string           `json:"user_id"`
		Counts store.DSARCounts `json:"counts"`
	}{UserID: scope.User, Counts: c})
	if err != nil {
		return fmt.Errorf("sqlitestore: dsar event payload: %w", err)
	}
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "user.purged",
		SubjectID: scope.User,
		Reason:    "dsar",
		Payload:   string(payload),
		CreatedAt: now,
	}
	return insertEventSQLite(tx, identity.Scope{Tenant: scope.Tenant}, ev, now)
}
