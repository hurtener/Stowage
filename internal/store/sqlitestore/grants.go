package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type grantStore struct{ s *sqliteStore }

// --- Groups ---

func (g *grantStore) CreateGroup(ctx context.Context, scope identity.Scope, grp store.Group) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return g.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if grp.CreatedAt == 0 {
			grp.CreatedAt = now
		}
		_, err := tx.Exec(
			`INSERT INTO groups (id, tenant_id, name, created_at) VALUES (?,?,?,?)`,
			grp.ID, scope.Tenant, grp.Name, grp.CreatedAt,
		)
		if err != nil {
			if sqliteIsUnique(err) {
				return store.ErrConflict
			}
			return fmt.Errorf("sqlitestore: create group: %w", err)
		}
		return nil
	})
}

func (g *grantStore) GetGroup(ctx context.Context, scope identity.Scope, id string) (*store.Group, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	var grp store.Group
	err := g.s.rdb.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, created_at FROM groups WHERE id = ? AND tenant_id = ?`,
		id, scope.Tenant,
	).Scan(&grp.ID, &grp.TenantID, &grp.Name, &grp.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("sqlitestore: get group: %w", err)
	}
	return &grp, nil
}

func (g *grantStore) ListGroups(ctx context.Context, scope identity.Scope) ([]store.Group, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := g.s.rdb.QueryContext(ctx,
		`SELECT id, tenant_id, name, created_at FROM groups WHERE tenant_id = ? ORDER BY created_at ASC`,
		scope.Tenant,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list groups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.Group
	for rows.Next() {
		var grp store.Group
		if err := rows.Scan(&grp.ID, &grp.TenantID, &grp.Name, &grp.CreatedAt); err != nil {
			return nil, fmt.Errorf("sqlitestore: scan group: %w", err)
		}
		out = append(out, grp)
	}
	return out, rows.Err()
}

func (g *grantStore) DeleteGroup(ctx context.Context, scope identity.Scope, id string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return g.s.exec(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`DELETE FROM groups WHERE id = ? AND tenant_id = ?`,
			id, scope.Tenant,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: delete group: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// --- Members ---

func (g *grantStore) AddMember(ctx context.Context, scope identity.Scope, m store.GroupMember) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return g.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if m.CreatedAt == 0 {
			m.CreatedAt = now
		}
		_, err := tx.Exec(
			`INSERT OR IGNORE INTO group_members (id, group_id, user_id, tenant_id, created_at)
			 VALUES (?,?,?,?,?)`,
			m.ID, m.GroupID, m.UserID, scope.Tenant, m.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: add member: %w", err)
		}
		return nil
	})
}

func (g *grantStore) RemoveMember(ctx context.Context, scope identity.Scope, groupID, userID string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return g.s.exec(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`DELETE FROM group_members WHERE group_id = ? AND user_id = ? AND tenant_id = ?`,
			groupID, userID, scope.Tenant,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: remove member: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (g *grantStore) ListMembers(ctx context.Context, scope identity.Scope, groupID string) ([]store.GroupMember, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := g.s.rdb.QueryContext(ctx,
		`SELECT id, group_id, user_id, tenant_id, created_at
		 FROM group_members WHERE group_id = ? AND tenant_id = ? ORDER BY created_at ASC`,
		groupID, scope.Tenant,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.GroupMember
	for rows.Next() {
		var m store.GroupMember
		if err := rows.Scan(&m.ID, &m.GroupID, &m.UserID, &m.TenantID, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("sqlitestore: scan member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- Grants ---

func (g *grantStore) CreateGrant(ctx context.Context, scope identity.Scope, gr store.Grant) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return g.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if gr.CreatedAt == 0 {
			gr.CreatedAt = now
		}
		if gr.UpdatedAt == 0 {
			gr.UpdatedAt = now
		}
		_, err := tx.Exec(`
			INSERT INTO grants
				(id, tenant_id, project_id, user_id, session_id,
				 group_id, access, topic_filter, kind_filter, zone_ceiling,
				 redaction_profile, revoked_at, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			gr.ID, scope.Tenant, nullStr(gr.ProjectID), nullStr(gr.UserID), nullStr(gr.SessionID),
			gr.GroupID, gr.Access, gr.TopicFilter, gr.KindFilter, gr.ZoneCeiling,
			gr.RedactionProfile, gr.RevokedAt, gr.CreatedAt, gr.UpdatedAt,
		)
		if err != nil {
			if sqliteIsUnique(err) {
				return store.ErrConflict
			}
			return fmt.Errorf("sqlitestore: create grant: %w", err)
		}
		return nil
	})
}

func (g *grantStore) GetGrant(ctx context.Context, scope identity.Scope, id string) (*store.Grant, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	var gr store.Grant
	err := g.s.rdb.QueryRowContext(ctx, `
		SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		       group_id, access, topic_filter, kind_filter, zone_ceiling,
		       redaction_profile, revoked_at, created_at, updated_at
		FROM grants WHERE id = ? AND tenant_id = ?`,
		id, scope.Tenant,
	).Scan(
		&gr.ID, &gr.TenantID, &gr.ProjectID, &gr.UserID, &gr.SessionID,
		&gr.GroupID, &gr.Access, &gr.TopicFilter, &gr.KindFilter, &gr.ZoneCeiling,
		&gr.RedactionProfile, &gr.RevokedAt, &gr.CreatedAt, &gr.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("sqlitestore: get grant: %w", err)
	}
	return &gr, nil
}

func (g *grantStore) ListGrants(ctx context.Context, scope identity.Scope) ([]store.Grant, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := g.s.rdb.QueryContext(ctx, `
		SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		       group_id, access, topic_filter, kind_filter, zone_ceiling,
		       redaction_profile, revoked_at, created_at, updated_at
		FROM grants WHERE tenant_id = ? ORDER BY created_at ASC`,
		scope.Tenant,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: list grants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.Grant
	for rows.Next() {
		var gr store.Grant
		if err := rows.Scan(
			&gr.ID, &gr.TenantID, &gr.ProjectID, &gr.UserID, &gr.SessionID,
			&gr.GroupID, &gr.Access, &gr.TopicFilter, &gr.KindFilter, &gr.ZoneCeiling,
			&gr.RedactionProfile, &gr.RevokedAt, &gr.CreatedAt, &gr.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("sqlitestore: scan grant: %w", err)
		}
		out = append(out, gr)
	}
	return out, rows.Err()
}

func (g *grantStore) RevokeGrant(ctx context.Context, scope identity.Scope, id string, revokedAt int64) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return g.s.exec(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE grants SET revoked_at = ?, updated_at = ? WHERE id = ? AND tenant_id = ? AND revoked_at = 0`,
			revokedAt, time.Now().UnixMilli(), id, scope.Tenant,
		)
		if err != nil {
			return fmt.Errorf("sqlitestore: revoke grant: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// Either not found or already revoked — treat both as not found.
			return store.ErrNotFound
		}
		return nil
	})
}

// EffectiveScopes resolves the caller's readable scopes (own + granted).
// This is a single SQL JOIN query (≤1 extra query per retrieve, D-060).
// Zone-ceiling defence: only grants with zone_ceiling IN ('public','work') are
// returned even if a mis-stored grant has a higher ceiling.
// Cross-tenant: only grants in callerScope.Tenant are considered.
func (g *grantStore) EffectiveScopes(ctx context.Context, callerScope identity.Scope) ([]store.ScopedQuery, error) {
	if callerScope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	own := store.ScopedQuery{Scope: callerScope}
	result := []store.ScopedQuery{own}

	// No user = no group memberships possible.
	if callerScope.User == "" {
		return result, nil
	}

	// Single join: grants in this tenant where the caller's user is a member.
	rows, err := g.s.rdb.QueryContext(ctx, `
		SELECT DISTINCT gr.tenant_id, COALESCE(gr.project_id,''), COALESCE(gr.user_id,''), COALESCE(gr.session_id,''),
		       gr.zone_ceiling, COALESCE(gr.topic_filter,''), COALESCE(gr.kind_filter,'')
		FROM grants gr
		JOIN group_members gm ON gm.group_id = gr.group_id AND gm.tenant_id = gr.tenant_id
		WHERE gr.tenant_id = ? AND gm.user_id = ? AND gr.revoked_at = 0
		  AND gr.zone_ceiling IN ('public','work')`,
		callerScope.Tenant, callerScope.User,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: effective scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var sq store.ScopedQuery
		var tenantID, projectID, userID, sessionID, zoneCeiling, topicFilter, kindFilter string
		if err := rows.Scan(&tenantID, &projectID, &userID, &sessionID, &zoneCeiling, &topicFilter, &kindFilter); err != nil {
			return nil, fmt.Errorf("sqlitestore: scan effective scope: %w", err)
		}
		// Defense: only same-tenant scopes (guaranteed by query, but double-check).
		if tenantID != callerScope.Tenant {
			continue
		}
		// Defense: skip self-scope (caller already owns it).
		if projectID == callerScope.Project && userID == callerScope.User &&
			sessionID == callerScope.Session {
			continue
		}
		sq.Scope = identity.Scope{
			Tenant:  tenantID,
			Project: projectID,
			User:    userID,
			Session: sessionID,
		}
		sq.ZoneCeiling = zoneCeiling
		sq.KindFilter = kindFilter
		sq.TopicFilter = topicFilter
		result = append(result, sq)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlitestore: effective scopes rows: %w", err)
	}
	return result, nil
}
