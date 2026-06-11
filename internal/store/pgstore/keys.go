package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hurtener/stowage/internal/auth"
)

type keyStore struct{ s *pgStore }

func (k *keyStore) Lookup(id string) (*auth.Key, error) {
	row := k.s.pool.QueryRow(context.Background(),
		`SELECT id, tenant_id, role, hash, created_at, revoked_at FROM api_keys WHERE id = $1`, id,
	)
	return scanKey(row)
}

func (k *keyStore) Insert(key auth.Key) error {
	var revokedAt int64
	if key.RevokedAt != nil {
		revokedAt = key.RevokedAt.UnixMilli()
	}
	_, err := k.s.pool.Exec(context.Background(), `
		INSERT INTO api_keys (id, tenant_id, role, hash, created_at, revoked_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		key.ID, key.TenantID, string(key.Role), key.Hash[:],
		key.CreatedAt.UnixMilli(), revokedAt,
	)
	if err != nil {
		return fmt.Errorf("pgstore: insert key %q: %w", key.ID, err)
	}
	return nil
}

func (k *keyStore) Revoke(id string, at time.Time) error {
	tag, err := k.s.pool.Exec(context.Background(),
		`UPDATE api_keys SET revoked_at = $1 WHERE id = $2`, at.UnixMilli(), id,
	)
	if err != nil {
		return fmt.Errorf("pgstore: revoke key %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrKeyNotFound
	}
	return nil
}

func scanKey(row rowScanner) (*auth.Key, error) {
	var (
		id, tenantID, role string
		hashBytes          []byte
		createdAtMs        int64
		revokedAtMs        int64
	)
	err := row.Scan(&id, &tenantID, &role, &hashBytes, &createdAtMs, &revokedAtMs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auth.ErrKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pgstore: scan key: %w", err)
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
