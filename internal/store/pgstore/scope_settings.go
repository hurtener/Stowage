package pgstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type scopeSettingsStore struct{ s *pgStore }

// scopeMatchExact pins to one precise scope (project/user/session compared as ” when
// empty, since the rows are stored with empty strings — SQL UNIQUE treats NULL as
// distinct, so scope_settings uses ” for the dimensions).
func scopeMatchExact(scope identity.Scope) (string, []interface{}, error) {
	if scope.Tenant == "" {
		return "", nil, store.ErrScopeRequired
	}
	clause := "tenant_id = $1 AND COALESCE(project_id,'') = $2 AND COALESCE(user_id,'') = $3 AND COALESCE(session_id,'') = $4"
	return clause, []interface{}{scope.Tenant, scope.Project, scope.User, scope.Session}, nil
}

func (ss *scopeSettingsStore) Get(ctx context.Context, scope identity.Scope, key string) (string, bool, error) {
	clause, args, err := scopeMatchExact(scope)
	if err != nil {
		return "", false, err
	}
	args = append(args, key)
	var value string
	err = ss.s.pool.QueryRow(ctx, `SELECT value FROM scope_settings WHERE `+clause+` AND key = $5`, args...).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func (ss *scopeSettingsStore) Set(ctx context.Context, scope identity.Scope, key, value string, now int64) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	// Empty strings (NOT NULL) for the scope dims so UNIQUE(scope,key) dedupes (NULLs
	// are distinct in SQL UNIQUE).
	_, err := ss.s.pool.Exec(ctx, `
		INSERT INTO scope_settings (id, tenant_id, project_id, user_id, session_id, key, value, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (tenant_id, project_id, user_id, session_id, key)
		DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		ulid.Make().String(), scope.Tenant, scope.Project, scope.User, scope.Session, key, value, now, now,
	)
	return err
}

func (ss *scopeSettingsStore) List(ctx context.Context, scope identity.Scope) (map[string]string, error) {
	clause, args, err := scopeMatchExact(scope)
	if err != nil {
		return nil, err
	}
	rows, err := ss.s.pool.Query(ctx, `SELECT key, value FROM scope_settings WHERE `+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (ss *scopeSettingsStore) Delete(ctx context.Context, scope identity.Scope, key string) error {
	clause, args, err := scopeMatchExact(scope)
	if err != nil {
		return err
	}
	args = append(args, key)
	_, derr := ss.s.pool.Exec(ctx, `DELETE FROM scope_settings WHERE `+clause+` AND key = $5`, args...)
	return derr
}
