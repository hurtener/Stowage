package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/auth"
)

type keyStore struct{ s *sqliteStore }

// Lookup returns the Key with the given ID.
func (k *keyStore) Lookup(id string) (*auth.Key, error) {
	row := k.s.rdb.QueryRowContext(context.Background(),
		`SELECT id, tenant_id, role, hash, created_at, revoked_at
		 FROM api_keys WHERE id = ?`,
		id,
	)
	return scanKey(row)
}

// Insert stores a new key.
func (k *keyStore) Insert(key auth.Key) error {
	return k.s.exec(context.Background(), func(tx *sql.Tx) error {
		var revokedAt int64
		if key.RevokedAt != nil {
			revokedAt = key.RevokedAt.UnixMilli()
		}
		_, err := tx.Exec(`
			INSERT INTO api_keys (id, tenant_id, role, hash, created_at, revoked_at)
			VALUES (?,?,?,?,?,?)`,
			key.ID, key.TenantID, string(key.Role), key.Hash[:],
			key.CreatedAt.UnixMilli(), revokedAt,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: insert key %q: %w", key.ID, err)
		}
		return nil
	})
}

// Revoke marks a key as revoked.
func (k *keyStore) Revoke(id string, at time.Time) error {
	return k.s.exec(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE api_keys SET revoked_at = ? WHERE id = ?`,
			at.UnixMilli(), id,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: revoke key %q: %w", id, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return auth.ErrKeyNotFound
		}
		return nil
	})
}

// List returns all keys, optionally filtered by tenantID.
func (k *keyStore) List(tenantID string) ([]auth.Key, error) {
	var (
		q    string
		args []interface{}
	)
	if tenantID != "" {
		q = `SELECT id, tenant_id, role, hash, created_at, revoked_at FROM api_keys WHERE tenant_id = ?`
		args = []interface{}{tenantID}
	} else {
		q = `SELECT id, tenant_id, role, hash, created_at, revoked_at FROM api_keys`
	}
	rows, err := k.s.rdb.QueryContext(context.Background(), q, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []auth.Key
	for rows.Next() {
		key, scanErr := scanKey(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, *key)
	}
	return out, rows.Err()
}

func scanKey(row rowScanner) (*auth.Key, error) {
	var (
		id, tenantID, role string
		hashBytes          []byte
		createdAtMs        int64
		revokedAtMs        int64
	)
	err := row.Scan(&id, &tenantID, &role, &hashBytes, &createdAtMs, &revokedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, auth.ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: scan key: %w", err)
	}
	key := &auth.Key{
		ID:        id,
		TenantID:  tenantID,
		Role:      auth.Role(role),
		CreatedAt: time.UnixMilli(createdAtMs).UTC(),
	}
	if len(hashBytes) == 32 {
		copy(key.Hash[:], hashBytes)
	}
	if revokedAtMs > 0 {
		t := time.UnixMilli(revokedAtMs).UTC()
		key.RevokedAt = &t
	}
	return key, nil
}
