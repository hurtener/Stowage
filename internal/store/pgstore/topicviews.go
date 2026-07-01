package pgstore

// topicviews.go — the general (subject_kind, subject_id, view_name) -> topic-key
// policy-binding sub-store (Phase ae1, D-135/D-146/D-151). NOT a scope table: no
// memory rows, no user_id. ae1 is the only caller of this phase and always
// operates on (subject_kind='agent', view_name='default') rows via the
// agent-shaped methods (store.TopicViewStore); ae9 generalizes to named views on
// the same table with other subject_kind/view_name values.

import (
	"context"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type topicViewStore struct{ s *pgStore }

const (
	subjectKindAgent = "agent"
	viewNameDefault  = "default"
)

// PutAgentPolicy upserts the (tenant, 'agent', agentID, 'default') binding as an
// atomic delete-then-insert inside one pgx.Tx.
func (t *topicViewStore) PutAgentPolicy(ctx context.Context, scope identity.Scope, p store.AgentPolicy) error {
	if scope.Tenant == "" { // P3: fail closed
		return store.ErrScopeRequired
	}
	if p.AgentID == "" {
		return fmt.Errorf("pgstore: PutAgentPolicy: agent_id is required")
	}
	if len(p.AllowTopics) == 0 && len(p.DenyTopics) == 0 {
		// Reject BEFORE the delete-then-insert replace, so an empty Put can never
		// silently wipe an existing binding (ae1, D-146). Use DeleteAgentPolicy to remove.
		return store.ErrEmptyPolicy
	}
	tx, err := t.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		DELETE FROM topic_views
		WHERE tenant_id=$1 AND subject_kind=$2 AND subject_id=$3 AND view_name=$4`,
		scope.Tenant, subjectKindAgent, p.AgentID, viewNameDefault,
	); err != nil {
		return fmt.Errorf("pgstore: PutAgentPolicy delete: %w", err)
	}

	now := time.Now().UnixMilli()
	insert := func(topicKey, effect string) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO topic_views
				(id, tenant_id, subject_kind, subject_id, view_name, topic_key, effect, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			ulid.Make().String(), scope.Tenant, subjectKindAgent, p.AgentID, viewNameDefault,
			topicKey, effect, now, now,
		)
		return err
	}
	for _, k := range p.AllowTopics {
		if err := insert(k, "allow"); err != nil {
			return fmt.Errorf("pgstore: PutAgentPolicy insert allow: %w", err)
		}
	}
	for _, k := range p.DenyTopics {
		if err := insert(k, "deny"); err != nil {
			return fmt.Errorf("pgstore: PutAgentPolicy insert deny: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// GetAgentPolicy aggregates the agent's topic_views rows into an AgentPolicy.
// Returns store.ErrNotFound when no rows exist for (tenant, agentID) — an
// UNBOUND agent.
func (t *topicViewStore) GetAgentPolicy(ctx context.Context, scope identity.Scope, agentID string) (*store.AgentPolicy, error) {
	if scope.Tenant == "" {
		return nil, store.ErrScopeRequired
	}
	rows, err := t.s.pool.Query(ctx, `
		SELECT topic_key, effect, created_at, updated_at FROM topic_views
		WHERE tenant_id=$1 AND subject_kind=$2 AND subject_id=$3 AND view_name=$4
		ORDER BY topic_key ASC`,
		scope.Tenant, subjectKindAgent, agentID, viewNameDefault,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: GetAgentPolicy: %w", err)
	}
	defer rows.Close()

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
	rows, err := t.s.pool.Query(ctx, `
		SELECT subject_id, topic_key, effect, created_at, updated_at FROM topic_views
		WHERE tenant_id=$1 AND subject_kind=$2 AND view_name=$3
		ORDER BY subject_id ASC, topic_key ASC`,
		scope.Tenant, subjectKindAgent, viewNameDefault,
	)
	if err != nil {
		return nil, fmt.Errorf("pgstore: ListAgentPolicies: %w", err)
	}
	defer rows.Close()

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
	tag, err := t.s.pool.Exec(ctx, `
		DELETE FROM topic_views
		WHERE tenant_id=$1 AND subject_kind=$2 AND subject_id=$3 AND view_name=$4`,
		scope.Tenant, subjectKindAgent, agentID, viewNameDefault,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
