package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type grantStore struct{ s *pgStore }

// --- Groups ---

func (g *grantStore) CreateGroup(ctx context.Context, scope identity.Scope, grp store.Group) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	now := time.Now().UnixMilli()
	if grp.CreatedAt == 0 {
		grp.CreatedAt = now
	}
	_, err := g.s.pool.Exec(ctx,
		`INSERT INTO groups (id, tenant_id, name, created_at) VALUES ($1,$2,$3,$4)`,
		grp.ID, scope.Tenant, grp.Name, grp.CreatedAt,
	)
	if err != nil {
		if pgIsUnique(err) {
			return store.ErrConflict
		}
		return fmt.Errorf("pgstore: create group: %w", err)
	}
	return nil
}

func (g *grantStore) GetGroup(ctx context.Context, scope identity.Scope, id string) (*store.Group, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	var grp store.Group
	err := g.s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, name, created_at FROM groups WHERE id = $1 AND tenant_id = $2`,
		id, scope.Tenant,
	).Scan(&grp.ID, &grp.TenantID, &grp.Name, &grp.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("pgstore: get group: %w", err)
	}
	return &grp, nil
}

func (g *grantStore) ListGroups(ctx context.Context, scope identity.Scope) ([]store.Group, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := g.s.pool.Query(ctx,
		`SELECT id, tenant_id, name, created_at FROM groups WHERE tenant_id = $1 ORDER BY created_at ASC`,
		scope.Tenant,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list groups: %w", err)
	}
	defer rows.Close()
	var out []store.Group
	for rows.Next() {
		var grp store.Group
		if err := rows.Scan(&grp.ID, &grp.TenantID, &grp.Name, &grp.CreatedAt); err != nil {
			return nil, fmt.Errorf("pgstore: scan group: %w", err)
		}
		out = append(out, grp)
	}
	return out, rows.Err()
}

func (g *grantStore) DeleteGroup(ctx context.Context, scope identity.Scope, id string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	tag, err := g.s.pool.Exec(ctx,
		`DELETE FROM groups WHERE id = $1 AND tenant_id = $2`,
		id, scope.Tenant,
	)
	if err != nil {
		return fmt.Errorf("pgstore: delete group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- Members ---

func (g *grantStore) AddMember(ctx context.Context, scope identity.Scope, m store.GroupMember) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	now := time.Now().UnixMilli()
	if m.CreatedAt == 0 {
		m.CreatedAt = now
	}
	_, err := g.s.pool.Exec(ctx,
		`INSERT INTO group_members (id, group_id, user_id, tenant_id, created_at)
		 VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		m.ID, m.GroupID, m.UserID, scope.Tenant, m.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("pgstore: add member: %w", err)
	}
	return nil
}

func (g *grantStore) RemoveMember(ctx context.Context, scope identity.Scope, groupID, userID string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	tag, err := g.s.pool.Exec(ctx,
		`DELETE FROM group_members WHERE group_id = $1 AND user_id = $2 AND tenant_id = $3`,
		groupID, userID, scope.Tenant,
	)
	if err != nil {
		return fmt.Errorf("pgstore: remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (g *grantStore) ListMembers(ctx context.Context, scope identity.Scope, groupID string) ([]store.GroupMember, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := g.s.pool.Query(ctx,
		`SELECT id, group_id, user_id, tenant_id, created_at
		 FROM group_members WHERE group_id = $1 AND tenant_id = $2 ORDER BY created_at ASC`,
		groupID, scope.Tenant,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list members: %w", err)
	}
	defer rows.Close()
	var out []store.GroupMember
	for rows.Next() {
		var m store.GroupMember
		if err := rows.Scan(&m.ID, &m.GroupID, &m.UserID, &m.TenantID, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("pgstore: scan member: %w", err)
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
	now := time.Now().UnixMilli()
	if gr.CreatedAt == 0 {
		gr.CreatedAt = now
	}
	if gr.UpdatedAt == 0 {
		gr.UpdatedAt = now
	}
	_, err := g.s.pool.Exec(ctx, `
		INSERT INTO grants
			(id, tenant_id, project_id, user_id, session_id,
			 group_id, access, topic_filter, kind_filter, zone_ceiling,
			 redaction_profile, revoked_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		gr.ID, scope.Tenant, nullStr(gr.ProjectID), nullStr(gr.UserID), nullStr(gr.SessionID),
		gr.GroupID, gr.Access, gr.TopicFilter, gr.KindFilter, gr.ZoneCeiling,
		gr.RedactionProfile, gr.RevokedAt, gr.CreatedAt, gr.UpdatedAt,
	)
	if err != nil {
		if pgIsUnique(err) {
			return store.ErrConflict
		}
		return fmt.Errorf("pgstore: create grant: %w", err)
	}
	return nil
}

func (g *grantStore) GetGrant(ctx context.Context, scope identity.Scope, id string) (*store.Grant, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	var gr store.Grant
	err := g.s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		       group_id, access, topic_filter, kind_filter, zone_ceiling,
		       redaction_profile, revoked_at, created_at, updated_at
		FROM grants WHERE id = $1 AND tenant_id = $2`,
		id, scope.Tenant,
	).Scan(
		&gr.ID, &gr.TenantID, &gr.ProjectID, &gr.UserID, &gr.SessionID,
		&gr.GroupID, &gr.Access, &gr.TopicFilter, &gr.KindFilter, &gr.ZoneCeiling,
		&gr.RedactionProfile, &gr.RevokedAt, &gr.CreatedAt, &gr.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("pgstore: get grant: %w", err)
	}
	return &gr, nil
}

func (g *grantStore) ListGrants(ctx context.Context, scope identity.Scope) ([]store.Grant, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := g.s.pool.Query(ctx, `
		SELECT id, tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''),
		       group_id, access, topic_filter, kind_filter, zone_ceiling,
		       redaction_profile, revoked_at, created_at, updated_at
		FROM grants WHERE tenant_id = $1 ORDER BY created_at ASC`,
		scope.Tenant,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: list grants: %w", err)
	}
	defer rows.Close()
	var out []store.Grant
	for rows.Next() {
		var gr store.Grant
		if err := rows.Scan(
			&gr.ID, &gr.TenantID, &gr.ProjectID, &gr.UserID, &gr.SessionID,
			&gr.GroupID, &gr.Access, &gr.TopicFilter, &gr.KindFilter, &gr.ZoneCeiling,
			&gr.RedactionProfile, &gr.RevokedAt, &gr.CreatedAt, &gr.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("pgstore: scan grant: %w", err)
		}
		out = append(out, gr)
	}
	return out, rows.Err()
}

func (g *grantStore) RevokeGrant(ctx context.Context, scope identity.Scope, id string, revokedAt int64) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	tag, err := g.s.pool.Exec(ctx,
		`UPDATE grants SET revoked_at = $1, updated_at = $2 WHERE id = $3 AND tenant_id = $4 AND revoked_at = 0`,
		revokedAt, time.Now().UnixMilli(), id, scope.Tenant,
	)
	if err != nil {
		return fmt.Errorf("pgstore: revoke grant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// EffectiveScopes resolves the caller's readable scopes (own + granted).
// Single SQL JOIN query (≤1 extra query per retrieve, D-060).
// Defense: zone_ceiling restricted to ('public','work'); cross-tenant
// grants impossible (both grant and group_member scoped to same tenant_id).
func (g *grantStore) EffectiveScopes(ctx context.Context, callerScope identity.Scope) ([]store.ScopedQuery, error) {
	if callerScope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	own := store.ScopedQuery{Scope: callerScope}
	result := []store.ScopedQuery{own}

	if callerScope.User == "" {
		return result, nil
	}

	rows, err := g.s.pool.Query(ctx, `
		SELECT DISTINCT gr.tenant_id, COALESCE(gr.project_id,''), COALESCE(gr.user_id,''), COALESCE(gr.session_id,''), gr.zone_ceiling
		FROM grants gr
		JOIN group_members gm ON gm.group_id = gr.group_id AND gm.tenant_id = gr.tenant_id
		WHERE gr.tenant_id = $1 AND gm.user_id = $2 AND gr.revoked_at = 0
		  AND gr.zone_ceiling IN ('public','work')`,
		callerScope.Tenant, callerScope.User,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: effective scopes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID, projectID, userID, sessionID, zoneCeiling string
		if err := rows.Scan(&tenantID, &projectID, &userID, &sessionID, &zoneCeiling); err != nil {
			return nil, fmt.Errorf("pgstore: scan effective scope: %w", err)
		}
		if tenantID != callerScope.Tenant {
			continue
		}
		if projectID == callerScope.Project && userID == callerScope.User &&
			sessionID == callerScope.Session {
			continue
		}
		result = append(result, store.ScopedQuery{
			Scope: identity.Scope{
				Tenant:  tenantID,
				Project: projectID,
				User:    userID,
				Session: sessionID,
			},
			ZoneCeiling: zoneCeiling,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore: effective scopes rows: %w", err)
	}
	return result, nil
}
