package sqlitestore

// topicviews.go — the general (subject_kind, subject_id, view_name) -> topic-key
// policy-binding sub-store (Phase ae1, D-135/D-146/D-151). NOT a scope table: no
// memory rows, no user_id. ae1 is the only caller of this phase and always
// operates on (subject_kind='agent', view_name='default') rows via the
// agent-shaped methods (store.TopicViewStore); ae9 generalizes to named views on
// the same table with other subject_kind/view_name values.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type topicViewStore struct{ s *sqliteStore }

const (
	subjectKindAgent = "agent"
	viewNameDefault  = "default"
)

// PutAgentPolicy upserts the (tenant, 'agent', agentID, 'default') binding as an
// atomic delete-then-insert inside one tx (safe under sqlite's single writer
// goroutine serialization, matching topicStore.Upsert's documented pattern).
func (t *topicViewStore) PutAgentPolicy(ctx context.Context, scope identity.Scope, p store.AgentPolicy) error {
	if scope.Tenant == "" { // P3: fail closed
		return store.ErrScopeRequired
	}
	if p.AgentID == "" {
		return fmt.Errorf("sqlitestore: PutAgentPolicy: agent_id is required")
	}
	if len(p.AllowTopics) == 0 && len(p.DenyTopics) == 0 {
		// Reject BEFORE the delete-then-insert replace, so an empty Put can never
		// silently wipe an existing binding (ae1, D-146). Use DeleteAgentPolicy to remove.
		return store.ErrEmptyPolicy
	}
	return t.s.exec(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		if _, err := tx.Exec(`
			DELETE FROM topic_views
			WHERE tenant_id=? AND subject_kind=? AND subject_id=? AND view_name=?`,
			scope.Tenant, subjectKindAgent, p.AgentID, viewNameDefault,
		); err != nil {
			return fmt.Errorf("sqlitestore: PutAgentPolicy delete: %w", err)
		}
		insert := func(topicKey, effect string) error {
			_, err := tx.Exec(`
				INSERT INTO topic_views
					(id, tenant_id, subject_kind, subject_id, view_name, topic_key, effect, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?)`,
				ulid.Make().String(), scope.Tenant, subjectKindAgent, p.AgentID, viewNameDefault,
				topicKey, effect, now, now,
			)
			return err
		}
		for _, k := range p.AllowTopics {
			if err := insert(k, "allow"); err != nil {
				return fmt.Errorf("sqlitestore: PutAgentPolicy insert allow: %w", err)
			}
		}
		for _, k := range p.DenyTopics {
			if err := insert(k, "deny"); err != nil {
				return fmt.Errorf("sqlitestore: PutAgentPolicy insert deny: %w", err)
			}
		}
		return nil
	})
}

// GetAgentPolicy aggregates the agent's topic_views rows into an AgentPolicy.
// Returns store.ErrNotFound when no rows exist for (tenant, agentID) — an
// UNBOUND agent.
func (t *topicViewStore) GetAgentPolicy(ctx context.Context, scope identity.Scope, agentID string) (*store.AgentPolicy, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := t.s.rdb.QueryContext(ctx, `
		SELECT topic_key, effect, created_at, updated_at FROM topic_views
		WHERE tenant_id=? AND subject_kind=? AND subject_id=? AND view_name=?
		ORDER BY topic_key ASC`,
		scope.Tenant, subjectKindAgent, agentID, viewNameDefault,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: GetAgentPolicy: %w", err)
	}
	defer func() { _ = rows.Close() }()

	p := store.AgentPolicy{TenantID: scope.Tenant, AgentID: agentID}
	found := false
	for rows.Next() {
		found = true
		var topicKey, effect string
		var createdAt, updatedAt int64
		if err := rows.Scan(&topicKey, &effect, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if p.CreatedAt == 0 || createdAt < p.CreatedAt {
			p.CreatedAt = createdAt
		}
		if updatedAt > p.UpdatedAt {
			p.UpdatedAt = updatedAt
		}
		switch effect {
		case "allow":
			p.AllowTopics = append(p.AllowTopics, topicKey)
		case "deny":
			p.DenyTopics = append(p.DenyTopics, topicKey)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

// ListAgentPolicies returns all agent bindings for the tenant, ordered by
// agent_id ascending (the row order already produces that, since the SELECT is
// ORDER BY subject_id ASC).
func (t *topicViewStore) ListAgentPolicies(ctx context.Context, scope identity.Scope) ([]store.AgentPolicy, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := t.s.rdb.QueryContext(ctx, `
		SELECT subject_id, topic_key, effect, created_at, updated_at FROM topic_views
		WHERE tenant_id=? AND subject_kind=? AND view_name=?
		ORDER BY subject_id ASC, topic_key ASC`,
		scope.Tenant, subjectKindAgent, viewNameDefault,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: ListAgentPolicies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byAgent := make(map[string]*store.AgentPolicy)
	var order []string
	for rows.Next() {
		var agentID, topicKey, effect string
		var createdAt, updatedAt int64
		if err := rows.Scan(&agentID, &topicKey, &effect, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p, ok := byAgent[agentID]
		if !ok {
			p = &store.AgentPolicy{TenantID: scope.Tenant, AgentID: agentID, CreatedAt: createdAt, UpdatedAt: updatedAt}
			byAgent[agentID] = p
			order = append(order, agentID)
		}
		if createdAt < p.CreatedAt {
			p.CreatedAt = createdAt
		}
		if updatedAt > p.UpdatedAt {
			p.UpdatedAt = updatedAt
		}
		switch effect {
		case "allow":
			p.AllowTopics = append(p.AllowTopics, topicKey)
		case "deny":
			p.DenyTopics = append(p.DenyTopics, topicKey)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]store.AgentPolicy, 0, len(order))
	for _, id := range order {
		out = append(out, *byAgent[id])
	}
	return out, nil
}

// DeleteAgentPolicy removes the binding for (tenant, agentID).
// Returns store.ErrNotFound when absent.
func (t *topicViewStore) DeleteAgentPolicy(ctx context.Context, scope identity.Scope, agentID string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return t.s.exec(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			DELETE FROM topic_views
			WHERE tenant_id=? AND subject_kind=? AND subject_id=? AND view_name=?`,
			scope.Tenant, subjectKindAgent, agentID, viewNameDefault,
		)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// --- ae9 (D-149/D-151): named-view admin CRUD over the SAME junction rows ---

// viewID synthesizes a readable, deterministic domain-level identifier for a
// view — the junction table has no single row that owns a view's identity (a
// view is a SET of per-key rows), so this is informational only; every
// CreateView/UpdateView/DeleteView/GetView/ListViews call still addresses a
// view by its natural key, never by this string.
func viewID(subjectKind, subjectID, viewName string) string {
	return subjectKind + "/" + subjectID + "/" + viewName
}

// viewRowsExistTx reports whether any row exists for the given natural key,
// within tx. Used by CreateView (conflict check) and UpdateView (existence
// check); sqlite's single-writer serialization (s.exec) makes the check+write
// atomic relative to other writes.
func viewRowsExistTx(tx *sql.Tx, tenant, subjectKind, subjectID, viewName string) (bool, error) {
	var n int
	err := tx.QueryRow(`
		SELECT count(*) FROM topic_views
		WHERE tenant_id=? AND subject_kind=? AND subject_id=? AND view_name=?`,
		tenant, subjectKind, subjectID, viewName,
	).Scan(&n)
	return n > 0, err
}

// insertViewRowsTx inserts one row per allow/deny topic key, within tx.
func insertViewRowsTx(tx *sql.Tx, tenant, subjectKind, subjectID, viewName string, allow, deny []string, now int64) error {
	insert := func(topicKey, effect string) error {
		_, err := tx.Exec(`
			INSERT INTO topic_views
				(id, tenant_id, subject_kind, subject_id, view_name, topic_key, effect, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			ulid.Make().String(), tenant, subjectKind, subjectID, viewName,
			topicKey, effect, now, now,
		)
		return err
	}
	for _, k := range allow {
		if err := insert(k, "allow"); err != nil {
			return err
		}
	}
	for _, k := range deny {
		if err := insert(k, "deny"); err != nil {
			return err
		}
	}
	return nil
}

// CreateView inserts a new named view. ErrConflict when a view already exists
// for the natural key (tenant_id, subject_kind, subject_id, view_name) — the
// pre-check runs inside the same write transaction as the insert (existence,
// not just the per-key UNIQUE index, since two non-overlapping topic-key sets
// for the same natural key would not otherwise collide on that index).
func (t *topicViewStore) CreateView(ctx context.Context, scope identity.Scope, v store.TopicView) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	if err := v.Validate(); err != nil {
		return err
	}
	return t.s.exec(ctx, func(tx *sql.Tx) error {
		exists, err := viewRowsExistTx(tx, scope.Tenant, v.SubjectKind, v.SubjectID, v.ViewName)
		if err != nil {
			return fmt.Errorf("sqlitestore: CreateView exists check: %w", err)
		}
		if exists {
			return store.ErrConflict
		}
		now := time.Now().UnixMilli()
		if err := insertViewRowsTx(tx, scope.Tenant, v.SubjectKind, v.SubjectID, v.ViewName, v.AllowTopics, v.DenyTopics, now); err != nil {
			if sqliteIsUnique(err) {
				return store.ErrConflict
			}
			return fmt.Errorf("sqlitestore: CreateView insert: %w", err)
		}
		return nil
	})
}

// UpdateView atomically replaces an existing view's AllowTopics/DenyTopics
// (delete-then-insert, matching PutAgentPolicy's precedent) — the end row set
// always matches v's lists exactly. ErrNotFound when the view does not exist.
func (t *topicViewStore) UpdateView(ctx context.Context, scope identity.Scope, v store.TopicView) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	if err := v.Validate(); err != nil {
		return err
	}
	return t.s.exec(ctx, func(tx *sql.Tx) error {
		exists, err := viewRowsExistTx(tx, scope.Tenant, v.SubjectKind, v.SubjectID, v.ViewName)
		if err != nil {
			return fmt.Errorf("sqlitestore: UpdateView exists check: %w", err)
		}
		if !exists {
			return store.ErrNotFound
		}
		if _, err := tx.Exec(`
			DELETE FROM topic_views
			WHERE tenant_id=? AND subject_kind=? AND subject_id=? AND view_name=?`,
			scope.Tenant, v.SubjectKind, v.SubjectID, v.ViewName,
		); err != nil {
			return fmt.Errorf("sqlitestore: UpdateView delete: %w", err)
		}
		now := time.Now().UnixMilli()
		if err := insertViewRowsTx(tx, scope.Tenant, v.SubjectKind, v.SubjectID, v.ViewName, v.AllowTopics, v.DenyTopics, now); err != nil {
			return fmt.Errorf("sqlitestore: UpdateView insert: %w", err)
		}
		return nil
	})
}

// DeleteView removes every row for a view's natural key. ErrNotFound when absent.
func (t *topicViewStore) DeleteView(ctx context.Context, scope identity.Scope, subjectKind, subjectID, viewName string) error {
	if scope.Tenant == "" {
		return store.ErrScopeRequired
	}
	return t.s.exec(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			DELETE FROM topic_views
			WHERE tenant_id=? AND subject_kind=? AND subject_id=? AND view_name=?`,
			scope.Tenant, subjectKind, subjectID, viewName,
		)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

// ListViews returns all views for the tenant, optionally narrowed to one
// subject (subjectKind/subjectID both non-empty), ordered by CREATED_AT
// ascending (the earliest row of the earliest-created view appears first;
// see the row-order aggregation note on aggregateViewRows).
func (t *topicViewStore) ListViews(ctx context.Context, scope identity.Scope, subjectKind, subjectID string) ([]store.TopicView, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	var rows *sql.Rows
	var err error
	if subjectKind != "" && subjectID != "" {
		rows, err = t.s.rdb.QueryContext(ctx, `
			SELECT subject_kind, subject_id, view_name, topic_key, effect, created_at, updated_at
			FROM topic_views
			WHERE tenant_id=? AND subject_kind=? AND subject_id=?
			ORDER BY created_at ASC, subject_kind ASC, subject_id ASC, view_name ASC, topic_key ASC`,
			scope.Tenant, subjectKind, subjectID,
		)
	} else {
		rows, err = t.s.rdb.QueryContext(ctx, `
			SELECT subject_kind, subject_id, view_name, topic_key, effect, created_at, updated_at
			FROM topic_views
			WHERE tenant_id=?
			ORDER BY created_at ASC, subject_kind ASC, subject_id ASC, view_name ASC, topic_key ASC`,
			scope.Tenant,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: ListViews: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return aggregateViewRows(scope.Tenant, rows)
}

// aggregateViewRows groups (subject_kind, subject_id, view_name, topic_key,
// effect) rows into []store.TopicView, aggregating AllowTopics/DenyTopics and
// the CreatedAt/UpdatedAt bounds per view, preserving the row stream's
// first-seen order per distinct natural key (the caller's SQL ORDER BY
// created_at ASC makes that also the correct view-level CreatedAt ordering).
func aggregateViewRows(tenant string, rows *sql.Rows) ([]store.TopicView, error) {
	byKey := make(map[string]*store.TopicView)
	var order []string
	for rows.Next() {
		var subjectKind, subjectID, viewName, topicKey, effect string
		var createdAt, updatedAt int64
		if err := rows.Scan(&subjectKind, &subjectID, &viewName, &topicKey, &effect, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		key := subjectKind + "\x00" + subjectID + "\x00" + viewName
		v, ok := byKey[key]
		if !ok {
			v = &store.TopicView{
				ID: viewID(subjectKind, subjectID, viewName), TenantID: tenant,
				SubjectKind: subjectKind, SubjectID: subjectID, ViewName: viewName,
				CreatedAt: createdAt, UpdatedAt: updatedAt,
			}
			byKey[key] = v
			order = append(order, key)
		}
		if createdAt < v.CreatedAt {
			v.CreatedAt = createdAt
		}
		if updatedAt > v.UpdatedAt {
			v.UpdatedAt = updatedAt
		}
		switch effect {
		case "allow":
			v.AllowTopics = append(v.AllowTopics, topicKey)
		case "deny":
			v.DenyTopics = append(v.DenyTopics, topicKey)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]store.TopicView, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out, nil
}

// GetView resolves one view by natural key, aggregating its junction rows.
// ErrNotFound when no rows exist for the natural key.
func (t *topicViewStore) GetView(ctx context.Context, scope identity.Scope, subjectKind, subjectID, viewName string) (*store.TopicView, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := t.s.rdb.QueryContext(ctx, `
		SELECT subject_kind, subject_id, view_name, topic_key, effect, created_at, updated_at
		FROM topic_views
		WHERE tenant_id=? AND subject_kind=? AND subject_id=? AND view_name=?
		ORDER BY topic_key ASC`,
		scope.Tenant, subjectKind, subjectID, viewName,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: GetView: %w", err)
	}
	defer func() { _ = rows.Close() }()
	views, err := aggregateViewRows(scope.Tenant, rows)
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		return nil, store.ErrNotFound
	}
	return &views[0], nil
}
