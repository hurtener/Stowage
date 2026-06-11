package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

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
