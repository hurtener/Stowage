package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type opsStore struct{ s *pgStore }

func (o *opsStore) PutDeadLetter(ctx context.Context, d store.DeadLetter) error {
	now := time.Now().UnixMilli()
	if d.CreatedAt == 0 {
		d.CreatedAt = now
	}
	if d.ID == "" {
		d.ID = ulid.Make().String()
	}
	_, err := o.s.pool.Exec(ctx, `
		INSERT INTO dead_letters (id, stage, item_id, error, attempts, resolved_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		d.ID, d.Stage, d.ItemID, d.Error, d.Attempts, d.ResolvedAt, d.CreatedAt,
	)
	return err
}

func (o *opsStore) ListDeadLetters(ctx context.Context, stage string, limit int) ([]store.DeadLetter, error) {
	var (
		rows interface {
			Next() bool
			Scan(...interface{}) error
			Err() error
			Close()
		}
		err error
	)
	if stage == "" {
		r, e := o.s.pool.Query(ctx,
			`SELECT id, stage, item_id, error, attempts, resolved_at, created_at
			 FROM dead_letters WHERE resolved_at = 0 ORDER BY created_at ASC LIMIT $1`, limit)
		rows, err = r, e
	} else {
		r, e := o.s.pool.Query(ctx,
			`SELECT id, stage, item_id, error, attempts, resolved_at, created_at
			 FROM dead_letters WHERE stage = $1 AND resolved_at = 0 ORDER BY created_at ASC LIMIT $2`,
			stage, limit)
		rows, err = r, e
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: list dead letters: %w", err)
	}
	defer rows.Close()

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
	tag, err := o.s.pool.Exec(ctx,
		`UPDATE dead_letters SET resolved_at = $1 WHERE id = $2 AND resolved_at = 0`,
		resolvedAt, id,
	)
	if err != nil {
		return fmt.Errorf("pgstore: resolve dead letter %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: dead letter %q", store.ErrNotFound, id)
	}
	return nil
}

// CheckAndSetJobMarker atomically checks whether (job, marker) has run and sets
// it if not (O1). Drops the pre-SELECT TOCTOU: a single INSERT ... ON CONFLICT
// DO NOTHING is atomic — RowsAffected()==1 means newly set (run), 0 means
// already present (skip).
func (o *opsStore) CheckAndSetJobMarker(ctx context.Context, job, marker string, ranAt int64) (bool, error) {
	id := ulid.Make().String()
	tag, err := o.s.pool.Exec(ctx,
		`INSERT INTO job_markers (id, job, marker, ran_at) VALUES ($1,$2,$3,$4)
		 ON CONFLICT(job, marker) DO NOTHING`,
		id, job, marker, ranAt,
	)
	if err != nil {
		return false, fmt.Errorf("pgstore: set job marker: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// AdvisoryLock acquires a PostgreSQL session-level advisory lock.
func (o *opsStore) AdvisoryLock(ctx context.Context, key int64) (func() error, error) {
	conn, err := o.s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("pgstore: acquire conn for advisory lock: %w", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
		conn.Release()
		return nil, fmt.Errorf("pgstore: advisory lock %d: %w", key, err)
	}
	return func() error {
		defer conn.Release()
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key); err != nil {
			return fmt.Errorf("pgstore: advisory unlock %d: %w", key, err)
		}
		return nil
	}, nil
}

// DeleteUserData cascades a DSAR erasure of ALL data for (tenant, user) in one
// transaction, FK-safe (children before parents), and emits a tenant-scoped
// `user.purged` audit event. This is the ONLY path that deletes verbatim records
// (P1 exception, D-098). See OpsStore.DeleteUserData for the full contract.
func (o *opsStore) DeleteUserData(ctx context.Context, scope identity.Scope) (store.DSARCounts, error) {
	if scope.Tenant == "" || scope.User == "" {
		return store.DSARCounts{}, store.ErrScopeRequired
	}
	t, u := scope.Tenant, scope.User

	tx, err := o.s.pool.Begin(ctx)
	if err != nil {
		return store.DSARCounts{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var c store.DSARCounts
	// del records the row count into dst; the first failure short-circuits the rest
	// and is surfaced once after the run (the tx is rolled back via the defer).
	var derr error
	del := func(dst *int64, query string, args ...any) {
		if derr != nil {
			return
		}
		tag, err := tx.Exec(ctx, query, args...)
		if err != nil {
			derr = fmt.Errorf("pgstore: dsar delete: %w", err)
			return
		}
		*dst = tag.RowsAffected()
	}

	// 1. Children of the user's memories (+ cross-user rows referencing them, so
	//    the FK-restricted memories/records deletes below never fail).
	del(&c.Provenance,
		`DELETE FROM provenance
		   WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=$1 AND user_id=$2)
		      OR record_id IN (SELECT id FROM records  WHERE tenant_id=$3 AND user_id=$4)`,
		t, u, t, u)
	del(&c.Entities,
		`DELETE FROM memory_entities WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=$1 AND user_id=$2)`,
		t, u)
	del(&c.Keywords,
		`DELETE FROM memory_keywords WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=$1 AND user_id=$2)`,
		t, u)
	del(&c.Queries,
		`DELETE FROM memory_queries WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=$1 AND user_id=$2)`,
		t, u)
	del(&c.MemoryTopics,
		`DELETE FROM memory_topics WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=$1 AND user_id=$2)`,
		t, u)
	del(&c.Vectors,
		`DELETE FROM memory_vectors WHERE memory_id IN (SELECT id FROM memories WHERE tenant_id=$1 AND user_id=$2)`,
		t, u)
	del(&c.Links,
		`DELETE FROM links
		   WHERE tenant_id=$1 AND (from_memory IN (SELECT id FROM memories WHERE tenant_id=$2 AND user_id=$3)
		                       OR  to_memory  IN (SELECT id FROM memories WHERE tenant_id=$4 AND user_id=$5))`,
		t, t, u, t, u)
	del(&c.Feedback,
		`DELETE FROM feedback
		   WHERE tenant_id=$1 AND memory_id IN (SELECT id FROM memories WHERE tenant_id=$2 AND user_id=$3)`,
		t, t, u)
	del(&c.Injections,
		`DELETE FROM injections
		   WHERE tenant_id=$1 AND (user_id=$2 OR memory_id IN (SELECT id FROM memories WHERE tenant_id=$3 AND user_id=$4))`,
		t, u, t, u)

	// 2. user_id-scoped tables (no memory FK; order among these is free).
	del(&c.Topics, `DELETE FROM topics WHERE tenant_id=$1 AND user_id=$2`, t, u)
	del(&c.Suggestions, `DELETE FROM suggestions WHERE tenant_id=$1 AND user_id=$2`, t, u)
	del(&c.BufferItems, `DELETE FROM buffer_items WHERE tenant_id=$1 AND user_id=$2`, t, u)
	del(&c.ScopeSettings, `DELETE FROM scope_settings WHERE tenant_id=$1 AND user_id=$2`, t, u)
	del(&c.GroupMembers, `DELETE FROM group_members WHERE tenant_id=$1 AND user_id=$2`, t, u)
	del(&c.Grants, `DELETE FROM grants WHERE tenant_id=$1 AND user_id=$2`, t, u)
	del(&c.Branches, `DELETE FROM branches WHERE tenant_id=$1 AND user_id=$2`, t, u)
	del(&c.Episodes, `DELETE FROM episodes WHERE tenant_id=$1 AND user_id=$2`, t, u)

	// 3. Parents — memories then the verbatim records (P1 exception, D-098).
	del(&c.Memories, `DELETE FROM memories WHERE tenant_id=$1 AND user_id=$2`, t, u)
	del(&c.Records, `DELETE FROM records WHERE tenant_id=$1 AND user_id=$2`, t, u)

	// 4. The user's own events. The user.purged event below is emitted at TENANT
	//    scope (user_id NULL) so it survives this delete.
	del(&c.Events, `DELETE FROM events WHERE tenant_id=$1 AND user_id=$2`, t, u)

	if derr != nil {
		return store.DSARCounts{}, derr
	}

	// 5. Emit the audit event at tenant scope (the purged user is the subject).
	now := time.Now().UnixMilli()
	payload, err := json.Marshal(struct {
		UserID string           `json:"user_id"`
		Counts store.DSARCounts `json:"counts"`
	}{UserID: u, Counts: c})
	if err != nil {
		return store.DSARCounts{}, fmt.Errorf("pgstore: dsar event payload: %w", err)
	}
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "user.purged",
		SubjectID: u,
		Reason:    "dsar",
		Payload:   string(payload),
		CreatedAt: now,
	}
	if err := insertEventPG(ctx, tx, identity.Scope{Tenant: t}, ev, now); err != nil {
		return store.DSARCounts{}, fmt.Errorf("pgstore: dsar event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return store.DSARCounts{}, err
	}
	return c, nil
}
