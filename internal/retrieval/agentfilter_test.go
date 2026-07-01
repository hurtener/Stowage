package retrieval_test

// agentfilter_test.go — unit coverage for the ae1 read-time agent->topic resolver
// (D-135/D-139/D-146): resolveAgentTopics's inert/unbound/bound/fail-open
// behaviour, and the Retrieve-level composed pass over ae6's EXISTING
// filterByTopicOwnScope. AC-7: ae1 defines a resolver, never a second filter —
// TestAgentFilter_NoSecondFilterFunction pins that with a source grep.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// agentPolicyFailStore wraps a real store.TopicViewStore but fails every
// GetAgentPolicy call, to exercise the D-139 fail-open resolver deterministically
// (mirrors topicfilter_test.go's memoriesTopicsFailStore pattern).
type agentPolicyFailStore struct {
	store.TopicViewStore
}

func (a agentPolicyFailStore) GetAgentPolicy(context.Context, identity.Scope, string) (*store.AgentPolicy, error) {
	return nil, errors.New("synthetic GetAgentPolicy failure")
}

func newTestRetriever(t *testing.T, st store.Store) *retrieval.Retriever {
	t.Helper()
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return retrieval.New(st.Memories(), st.Records(), vi, gw, log)
}

// --- resolveAgentTopics unit coverage ----------------------------------------

func TestResolveAgentTopics_DisabledInert(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), false)
	scope := identity.Scope{Tenant: "t-" + newID(), Agent: "agent-1"}

	allow, deny, active, degraded := r.ExportResolveAgentTopics(context.Background(), scope)
	if active || degraded || allow != nil || deny != nil {
		t.Errorf("disabled: got allow=%v deny=%v active=%v degraded=%v, want fully inert", allow, deny, active, degraded)
	}
}

func TestResolveAgentTopics_NoAgentInert(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true)
	scope := identity.Scope{Tenant: "t-" + newID()} // no Agent set

	allow, deny, active, degraded := r.ExportResolveAgentTopics(context.Background(), scope)
	if active || degraded || allow != nil || deny != nil {
		t.Errorf("no agent: got active=%v degraded=%v, want inert", active, degraded)
	}
}

func TestResolveAgentTopics_NilStoreInert(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	r := newTestRetriever(t, st).WithAgentPolicy(nil, true)
	scope := identity.Scope{Tenant: "t-" + newID(), Agent: "agent-1"}

	allow, deny, active, degraded := r.ExportResolveAgentTopics(context.Background(), scope)
	if active || degraded || allow != nil || deny != nil {
		t.Errorf("nil store: got active=%v degraded=%v, want inert", active, degraded)
	}
}

func TestResolveAgentTopics_Unbound(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true)
	scope := identity.Scope{Tenant: "t-" + newID(), Agent: "never-bound"}

	allow, deny, active, degraded := r.ExportResolveAgentTopics(context.Background(), scope)
	if active {
		t.Error("unbound agent must not be active")
	}
	if degraded {
		t.Error("unbound agent must NOT be degraded — ErrNotFound is a legitimate no-filter outcome, not a fault")
	}
	if allow != nil || deny != nil {
		t.Errorf("unbound agent: got allow=%v deny=%v, want nil,nil", allow, deny)
	}
}

func TestResolveAgentTopics_BoundAllowDenyBoth(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	if err := st.TopicViews().PutAgentPolicy(context.Background(), tenantScope, store.AgentPolicy{
		AgentID: "agent-1", AllowTopics: []string{"a", "b"}, DenyTopics: []string{"c"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true)
	readScope := tenantScope
	readScope.Agent = "agent-1"

	allow, deny, active, degraded := r.ExportResolveAgentTopics(context.Background(), readScope)
	if !active || degraded {
		t.Fatalf("bound agent: active=%v degraded=%v, want active=true degraded=false", active, degraded)
	}
	assertIDSet(t, "allow", allow, []string{"a", "b"})
	assertIDSet(t, "deny", deny, []string{"c"})
}

func TestResolveAgentTopics_FailsOpen(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	if err := st.TopicViews().PutAgentPolicy(context.Background(), tenantScope, store.AgentPolicy{
		AgentID: "agent-1", AllowTopics: []string{"a"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(agentPolicyFailStore{st.TopicViews()}, true)
	readScope := tenantScope
	readScope.Agent = "agent-1"

	allow, deny, active, degraded := r.ExportResolveAgentTopics(context.Background(), readScope)
	if active {
		t.Error("fail-open resolver must report active=false")
	}
	if !degraded {
		t.Error("expected degraded=true on a GetAgentPolicy error (D-139 fail-open)")
	}
	if allow != nil || deny != nil {
		t.Errorf("fail-open: got allow=%v deny=%v, want nil,nil", allow, deny)
	}
}

// --- Retrieve()-level composed pass -------------------------------------------

// TestRetrieve_AgentFilter_Subtracts proves a bound agent's allow-topic policy
// narrows the caller's own-scope retrieve results (P3: only subtracts).
func TestRetrieve_AgentFilter_Subtracts(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).WithAgentPolicy(st.TopicViews(), true)

	scope := identity.Scope{Tenant: "t-agentfilter-sub-" + newID()}
	authID := commitMemoryFull(t, st, scope, "auth widget note qvzxa", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy widget note qvzxa", "fact", nil, nil, nil, []string{"deploy"})

	if err := st.TopicViews().PutAgentPolicy(context.Background(), scope, store.AgentPolicy{
		AgentID: "bound-agent", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}

	readScope := scope
	readScope.Agent = "bound-agent"
	resp, err := r.Retrieve(context.Background(), readScope, retrieval.Request{Query: "qvzxa widget", Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.DegradedAgentFilter {
		t.Error("expected DegradedAgentFilter=false on a clean read")
	}
	gotIDs := map[string]bool{}
	for _, it := range resp.Items {
		gotIDs[it.Memory.ID] = true
	}
	if !gotIDs[authID] {
		t.Error("expected the allow-topic memory in the result")
	}
	if gotIDs[deployID] {
		t.Error("agent filter must have subtracted the non-allow-topic memory")
	}
}

// TestRetrieve_AgentFilter_UnboundUnfiltered proves an unbound agent (no policy
// row) leaves the caller's own-scope results unfiltered and NOT degraded.
func TestRetrieve_AgentFilter_UnboundUnfiltered(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).WithAgentPolicy(st.TopicViews(), true)

	scope := identity.Scope{Tenant: "t-agentfilter-unbound-" + newID()}
	authID := commitMemoryFull(t, st, scope, "auth widget note qvzxb", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy widget note qvzxb", "fact", nil, nil, nil, []string{"deploy"})

	readScope := scope
	readScope.Agent = "never-bound-agent"
	resp, err := r.Retrieve(context.Background(), readScope, retrieval.Request{Query: "qvzxb widget", Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.DegradedAgentFilter {
		t.Error("unbound agent must not surface DegradedAgentFilter=true")
	}
	gotIDs := map[string]bool{}
	for _, it := range resp.Items {
		gotIDs[it.Memory.ID] = true
	}
	if !gotIDs[authID] || !gotIDs[deployID] {
		t.Errorf("unbound agent must leave results unfiltered: got %v, want both %s and %s", gotIDs, authID, deployID)
	}
}

// TestRetrieve_AgentFilter_FailsOpen_DegradedMarker proves a policy-store error
// surfaces the caller's own UNFILTERED results with DegradedAgentFilter=true
// (D-139/D-036 fail-open) rather than an error or a dropped result.
func TestRetrieve_AgentFilter_FailsOpen_DegradedMarker(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	scope := identity.Scope{Tenant: "t-agentfilter-failopen-" + newID()}
	authID := commitMemoryFull(t, st, scope, "auth widget note qvzxc", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy widget note qvzxc", "fact", nil, nil, nil, []string{"deploy"})

	if err := st.TopicViews().PutAgentPolicy(context.Background(), scope, store.AgentPolicy{
		AgentID: "bound-agent", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}

	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).
		WithAgentPolicy(agentPolicyFailStore{st.TopicViews()}, true)

	readScope := scope
	readScope.Agent = "bound-agent"
	resp, err := r.Retrieve(context.Background(), readScope, retrieval.Request{Query: "qvzxc widget", Limit: 10})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if !resp.DegradedAgentFilter {
		t.Error("expected DegradedAgentFilter=true when the policy store errors")
	}
	gotIDs := map[string]bool{}
	for _, it := range resp.Items {
		gotIDs[it.Memory.ID] = true
	}
	if !gotIDs[authID] || !gotIDs[deployID] {
		t.Errorf("fail-open: expected both unfiltered items, got %v", gotIDs)
	}
}

// TestRetrieve_AgentFilter_ComposesWithRequestTopicFilter proves ae1's pass runs
// AFTER ae6's request-topic pass, so the effective result is their intersection
// (request filter ∩ agent filter) — not a union, and not either filter alone.
func TestRetrieve_AgentFilter_ComposesWithRequestTopicFilter(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).WithAgentPolicy(st.TopicViews(), true)

	scope := identity.Scope{Tenant: "t-agentfilter-compose-" + newID()}
	authOnlyID := commitMemoryFull(t, st, scope, "auth widget note qvzxd", "fact", nil, nil, nil, []string{"auth"})
	authInternalID := commitMemoryFull(t, st, scope, "auth internal widget note qvzxd", "fact", nil, nil, nil, []string{"auth", "internal"})
	deployID := commitMemoryFull(t, st, scope, "deploy widget note qvzxd", "fact", nil, nil, nil, []string{"deploy"})

	// Agent policy allows only "auth" — subtracts deployID.
	if err := st.TopicViews().PutAgentPolicy(context.Background(), scope, store.AgentPolicy{
		AgentID: "bound-agent", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}

	readScope := scope
	readScope.Agent = "bound-agent"
	// Request-level filter excludes "internal" — subtracts authInternalID.
	resp, err := r.Retrieve(context.Background(), readScope, retrieval.Request{
		Query: "qvzxd widget", Limit: 10, ExcludeTopics: []string{"internal"},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.DegradedTopicFilter || resp.DegradedAgentFilter {
		t.Error("expected both filters to apply cleanly (no degradation)")
	}
	gotIDs := map[string]bool{}
	for _, it := range resp.Items {
		gotIDs[it.Memory.ID] = true
	}
	if !gotIDs[authOnlyID] {
		t.Error("expected the auth-only memory to survive both filters")
	}
	if gotIDs[authInternalID] {
		t.Error("request filter (exclude internal) must have subtracted the auth+internal memory")
	}
	if gotIDs[deployID] {
		t.Error("agent filter (allow auth) must have subtracted the deploy-only memory")
	}
}

// TestAgentFilter_NoSecondFilterFunction is the AC-7 grep gate: agentfilter.go
// must define ONLY a resolver (resolveAgentTopics) and must call ae6's EXISTING
// filterByTopicOwnScope — never redefine a topic-filter function.
func TestAgentFilter_NoSecondFilterFunction(t *testing.T) {
	t.Parallel()
	agentSrc, err := os.ReadFile("agentfilter.go")
	if err != nil {
		t.Fatalf("read agentfilter.go: %v", err)
	}
	retrievalSrc, err := os.ReadFile("retrieval.go")
	if err != nil {
		t.Fatalf("read retrieval.go: %v", err)
	}
	agent := string(agentSrc)
	all := agent + string(retrievalSrc)

	if !strings.Contains(agent, "func (r *Retriever) resolveAgentTopics(") {
		t.Error("agentfilter.go must define resolveAgentTopics")
	}
	for _, forbidden := range []string{
		"func (r *Retriever) filterByTopicOwnScope(",
		"func (r *Retriever) filterByAgentTopics(",
		"func filterByAgentTopics(",
	} {
		if strings.Contains(agent, forbidden) {
			t.Errorf("agentfilter.go must not redefine a topic-filter function (AC-7); found %q", forbidden)
		}
	}
	if !strings.Contains(all, "r.filterByTopicOwnScope(") {
		t.Error("ae1 must reuse ae6's existing filterByTopicOwnScope, not a private copy")
	}
}
