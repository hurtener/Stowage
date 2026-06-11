package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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
