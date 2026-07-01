package retrieval

// agentfilter.go — the read-time agent->topic RESOLVER (Phase ae1, D-135/D-146,
// generalized by D-151). This file defines ONLY a resolver (resolveAgentTopics)
// that feeds ae6's EXISTING filterByTopicOwnScope (topicfilter.go) a second time
// over the caller's own-scope candidates — it does NOT define a second topic-filter
// function (AC-7). Do not add filtering logic here; add it to topicfilter.go if the
// shared filter itself needs to change.
//
// Fails OPEN (D-139/D-036): a policy-STORE error degrades to unfiltered own-scope
// results with degraded=true. An UNBOUND agent (store.ErrNotFound) is unfiltered
// but NOT degraded — that is a legitimate "no filter" outcome, not a fault.

import (
	"context"
	"errors"
	"fmt"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// WithAgentPolicy wires the agent->topic policy store and the master enable flag
// (retrieval.agent_views.enabled, D-034) onto the Retriever. A nil store or
// enabled=false makes resolveAgentTopics fully inert (byte-identical zero-config
// behaviour). Call after New, before serving; not safe to call concurrently with
// Retrieve.
func (r *Retriever) WithAgentPolicy(st store.TopicViewStore, enabled bool) *Retriever {
	r.agentPolSt = st
	r.agentFilterOn = enabled
	return r
}

// resolveAgentTopics returns the (allow, deny) topic keys bound to scope.Agent, or
// (nil, nil, false, false) when agent filtering is inactive (disabled, no agent, no
// store, or an UNBOUND agent). degraded is true ONLY when the policy STORE errored
// (fail-open, D-139) — an unbound agent (ErrNotFound) is a legitimate "no filter",
// not a degradation.
func (r *Retriever) resolveAgentTopics(
	ctx context.Context, scope identity.Scope,
) (allow, deny []string, active bool, degraded bool) {
	if !r.agentFilterOn || r.agentPolSt == nil || scope.Agent == "" {
		return nil, nil, false, false
	}

	policy, err := r.agentPolSt.GetAgentPolicy(ctx, scope, scope.Agent)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, false, false // unbound agent — unfiltered, not degraded
		}
		r.log.WarnContext(ctx, "retrieval: GetAgentPolicy failed — failing OPEN on the agent filter (D-139)",
			"scope", scope.String(), "agent", scope.Agent, "err", err)
		return nil, nil, false, true // fail open
	}

	return policy.AllowTopics, policy.DenyTopics, true, false
}

// --- Agent-policy admin core (D-067/D-073) ----------------------------------
//
// PutAgentPolicy/DeleteAgentPolicy are the CORE mutation path both the HTTP and
// MCP memory_agent_policy admin surfaces call: the store write plus the cache
// invalidation (§6 blocking #2) live here, in ONE place, so no surface can forget
// to invalidate. GetAgentPolicy/ListAgentPolicies are pure reads with no cache
// side effect; they are exposed here too so every admin verb has one canonical
// entry point.

// PutAgentPolicy upserts the (scope.Tenant, agentID) binding via the wired
// TopicViewStore, then invalidates the tenant's cached agent-filtered reads
// (InvalidateScope at {Tenant} — a per-agent gen bump is a valid finer-grained
// alternative, but the cache key already carries Agent, and a tenant-wide bump is
// simple and correct) so the affected agent's next read reflects the edit
// immediately, no stale-until-TTL window (§6 blocking #2, AC-13).
func (r *Retriever) PutAgentPolicy(ctx context.Context, scope identity.Scope, p store.AgentPolicy) error {
	if r.agentPolSt == nil {
		return fmt.Errorf("retrieval: PutAgentPolicy: no agent-policy store wired")
	}
	if err := r.agentPolSt.PutAgentPolicy(ctx, scope, p); err != nil {
		return err
	}
	r.cache.InvalidateScope(identity.Scope{Tenant: scope.Tenant})
	return nil
}

// DeleteAgentPolicy removes the (scope.Tenant, agentID) binding and invalidates the
// tenant's cached agent-filtered reads (see PutAgentPolicy).
func (r *Retriever) DeleteAgentPolicy(ctx context.Context, scope identity.Scope, agentID string) error {
	if r.agentPolSt == nil {
		return fmt.Errorf("retrieval: DeleteAgentPolicy: no agent-policy store wired")
	}
	if err := r.agentPolSt.DeleteAgentPolicy(ctx, scope, agentID); err != nil {
		return err
	}
	r.cache.InvalidateScope(identity.Scope{Tenant: scope.Tenant})
	return nil
}

// GetAgentPolicy is a thin, side-effect-free passthrough to the wired
// TopicViewStore (a read never needs to invalidate the cache).
func (r *Retriever) GetAgentPolicy(ctx context.Context, scope identity.Scope, agentID string) (*store.AgentPolicy, error) {
	if r.agentPolSt == nil {
		return nil, fmt.Errorf("retrieval: GetAgentPolicy: no agent-policy store wired")
	}
	return r.agentPolSt.GetAgentPolicy(ctx, scope, agentID)
}

// ListAgentPolicies is a thin, side-effect-free passthrough to the wired
// TopicViewStore.
func (r *Retriever) ListAgentPolicies(ctx context.Context, scope identity.Scope) ([]store.AgentPolicy, error) {
	if r.agentPolSt == nil {
		return nil, fmt.Errorf("retrieval: ListAgentPolicies: no agent-policy store wired")
	}
	return r.agentPolSt.ListAgentPolicies(ctx, scope)
}
