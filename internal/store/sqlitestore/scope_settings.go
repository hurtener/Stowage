package sqlitestore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type scopeSettingsStore struct{ s *sqliteStore }

// scopeMatch builds an EXACT-scope predicate: each of project/user/session must equal
// the given value, treating "" as SQL NULL (the columns are nullable). This pins a row
// to one precise scope (not a prefix), matching the table's UNIQUE(scope,key).
func scopeMatchExact(scope identity.Scope) (string, []interface{}, error) {
	if scope.Tenant == "" {
		return "", nil, store.ErrScopeRequired
	}
	clause := "tenant_id = ?" +
		" AND COALESCE(project_id,'') = ?" +
		" AND COALESCE(user_id,'') = ?" +
		" AND COALESCE(session_id,'') = ?"
	return clause, []interface{}{scope.Tenant, scope.Project, scope.User, scope.Session}, nil
}

func (ss *scopeSettingsStore) Get(ctx context.Context, scope identity.Scope, key string) (string, bool, error) {
	clause, args, err := scopeMatchExact(scope)
	if err != nil {
		return "", false, err
	}
	args = append(args, key)
	var value string
	err = ss.s.rdb.QueryRowContext(ctx,
		`SELECT value FROM scope_settings WHERE `+clause+` AND key = ?`, args..., //nolint:gosec
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
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
	return ss.s.exec(ctx, func(tx *sql.Tx) error {
		// Upsert keyed by the UNIQUE(tenant,project,user,session,key) constraint.
		// Store empty strings (NOT NULL) for the scope dimensions: SQL UNIQUE treats
		// NULLs as distinct, so NULL-scoped rows would never conflict and the upsert
		// would duplicate. Empty strings make UNIQUE(scope,key) behave (and match the
		// COALESCE(...,'') reads in scopeMatchExact).
		_, err := tx.Exec(`
			INSERT INTO scope_settings (id, tenant_id, project_id, user_id, session_id, key, value, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT(tenant_id, project_id, user_id, session_id, key)
			DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			ulid.Make().String(), scope.Tenant, scope.Project, scope.User, scope.Session,
			key, value, now, now,
		)
		return err
	})
}

func (ss *scopeSettingsStore) List(ctx context.Context, scope identity.Scope) (map[string]string, error) {
	clause, args, err := scopeMatchExact(scope)
	if err != nil {
		return nil, err
	}
	rows, err := ss.s.rdb.QueryContext(ctx,
		`SELECT key, value FROM scope_settings WHERE `+clause, args..., //nolint:gosec
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
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
	return ss.s.exec(ctx, func(tx *sql.Tx) error {
		_, derr := tx.Exec(`DELETE FROM scope_settings WHERE `+clause+` AND key = ?`, args...) //nolint:gosec
		return derr
	})
}
