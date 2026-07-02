package retrieval_test

// views_test.go — unit coverage for the ae9 read-time named topic-VIEW apply
// path (D-149/D-151): subject resolution (agent-only, key-only, both with
// precedence in both orders, none→unbound), resolveAndApplyView's
// allow/deny/both/empty application, GetView→ErrNotFound pass-through,
// fail-OPEN (default) and fail-CLOSED (on_policy_error=closed) fault
// injection, the Retrieve()-level composed pass, cache-bypass, concurrency,
// and the AC-4 grep gate proving ae9 reuses ae6's filterByTopicOwnScope and
// defines no second filter.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// viewFailStore wraps a real store.TopicViewStore but fails every GetView
// call, to exercise the D-139/D-149 fail-open/fail-closed divergence
// deterministically (mirrors agentPolicyFailStore's pattern, agentfilter_test.go).
type viewFailStore struct {
	store.TopicViewStore
}

func (v viewFailStore) GetView(context.Context, identity.Scope, string, string, string) (*store.TopicView, error) {
	return nil, errors.New("synthetic GetView failure")
}

// --- resolveAndApplyView unit coverage ---------------------------------------

func TestResolveAndApplyView_DisabledInert(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), false).
		SetTopicViews(st.TopicViews(), false, "agent,key")
	scope := identity.Scope{Tenant: "t-" + newID(), Agent: "agent-1"}

	kept, degraded := r.ExportResolveAndApplyView(context.Background(), scope, retrieval.Request{}, []string{"a", "b"})
	if degraded {
		t.Error("disabled: must not be degraded")
	}
	assertIDSet(t, "kept", kept, []string{"a", "b"})
}

func TestResolveAndApplyView_NilStoreInert(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(nil, false, "agent,key")
	scope := identity.Scope{Tenant: "t-" + newID(), Agent: "agent-1"}

	kept, degraded := r.ExportResolveAndApplyView(context.Background(), scope, retrieval.Request{}, []string{"a", "b"})
	if degraded {
		t.Error("nil store: must not be degraded")
	}
	assertIDSet(t, "kept", kept, []string{"a", "b"})
}

func TestResolveAndApplyView_NoSubjectUnbound(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")
	scope := identity.Scope{Tenant: "t-" + newID()} // no Agent

	kept, degraded := r.ExportResolveAndApplyView(context.Background(), scope, retrieval.Request{}, []string{"a", "b"})
	if degraded {
		t.Error("no subject: must not be degraded")
	}
	assertIDSet(t, "kept", kept, []string{"a", "b"})
}

func TestResolveAndApplyView_AgentSubject_Bound(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	if err := st.TopicViews().CreateView(context.Background(), tenantScope, store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-1", ViewName: "default", AllowTopics: []string{"a"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")
	readScope := tenantScope
	readScope.Agent = "agent-1"

	// Candidate ids "a"/"b" aren't real memory IDs, so filterByTopicOwnScope's
	// MemoriesTopics lookup returns them untagged — use a bound EMPTY-allow-set
	// resolution instead: prove the view resolves (no unbound pass-through) by
	// checking against a subject WITHOUT a view (unbound) vs WITH one (bound,
	// active) using the exported resolver directly against real memory ids.
	authID := commitMemoryFull(t, st, tenantScope, "auth widget note vw01", "fact", nil, nil, nil, []string{"a"})
	deployID := commitMemoryFull(t, st, tenantScope, "deploy widget note vw01", "fact", nil, nil, nil, []string{"deploy-topic"})

	kept, degraded := r.ExportResolveAndApplyView(context.Background(), readScope, retrieval.Request{}, []string{authID, deployID})
	if degraded {
		t.Error("bound view: must not be degraded")
	}
	assertIDSet(t, "kept", kept, []string{authID})
}

func TestResolveAndApplyView_KeySubject_FallbackWhenNoAgent(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	if err := st.TopicViews().CreateView(context.Background(), tenantScope, store.TopicView{
		SubjectKind: "key", SubjectID: "sk_abc", ViewName: "default", AllowTopics: []string{"a"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")

	authID := commitMemoryFull(t, st, tenantScope, "auth widget note vw02", "fact", nil, nil, nil, []string{"a"})
	deployID := commitMemoryFull(t, st, tenantScope, "deploy widget note vw02", "fact", nil, nil, nil, []string{"deploy-topic"})

	req := retrieval.Request{CredentialKeyID: "sk_abc"} // no scope.Agent set
	kept, degraded := r.ExportResolveAndApplyView(context.Background(), tenantScope, req, []string{authID, deployID})
	if degraded {
		t.Error("key subject: must not be degraded")
	}
	assertIDSet(t, "kept", kept, []string{authID})
}

func TestResolveAndApplyView_Precedence_AgentWinsByDefault(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	ctx := context.Background()
	// Agent view allows "a"; key view allows "b" — with both present and the
	// default precedence "agent,key", the AGENT view must win.
	if err := st.TopicViews().CreateView(ctx, tenantScope, store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-1", ViewName: "default", AllowTopics: []string{"a"},
	}); err != nil {
		t.Fatalf("CreateView agent: %v", err)
	}
	if err := st.TopicViews().CreateView(ctx, tenantScope, store.TopicView{
		SubjectKind: "key", SubjectID: "sk_abc", ViewName: "default", AllowTopics: []string{"b"},
	}); err != nil {
		t.Fatalf("CreateView key: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")

	aID := commitMemoryFull(t, st, tenantScope, "note vw03 topic a", "fact", nil, nil, nil, []string{"a"})
	bID := commitMemoryFull(t, st, tenantScope, "note vw03 topic b", "fact", nil, nil, nil, []string{"b"})

	readScope := tenantScope
	readScope.Agent = "agent-1"
	req := retrieval.Request{CredentialKeyID: "sk_abc"}
	kept, degraded := r.ExportResolveAndApplyView(ctx, readScope, req, []string{aID, bID})
	if degraded {
		t.Error("precedence: must not be degraded")
	}
	assertIDSet(t, "kept (agent wins)", kept, []string{aID})
}

func TestResolveAndApplyView_Precedence_KeyFirstFlips(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	ctx := context.Background()
	if err := st.TopicViews().CreateView(ctx, tenantScope, store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-1", ViewName: "default", AllowTopics: []string{"a"},
	}); err != nil {
		t.Fatalf("CreateView agent: %v", err)
	}
	if err := st.TopicViews().CreateView(ctx, tenantScope, store.TopicView{
		SubjectKind: "key", SubjectID: "sk_abc", ViewName: "default", AllowTopics: []string{"b"},
	}); err != nil {
		t.Fatalf("CreateView key: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "key,agent") // FLIPPED precedence

	aID := commitMemoryFull(t, st, tenantScope, "note vw04 topic a", "fact", nil, nil, nil, []string{"a"})
	bID := commitMemoryFull(t, st, tenantScope, "note vw04 topic b", "fact", nil, nil, nil, []string{"b"})

	readScope := tenantScope
	readScope.Agent = "agent-1"
	req := retrieval.Request{CredentialKeyID: "sk_abc"}
	kept, degraded := r.ExportResolveAndApplyView(ctx, readScope, req, []string{aID, bID})
	if degraded {
		t.Error("flipped precedence: must not be degraded")
	}
	assertIDSet(t, "kept (key wins)", kept, []string{bID})
}

func TestResolveAndApplyView_UnboundViewName_NotDegraded(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")
	scope := identity.Scope{Tenant: "t-" + newID(), Agent: "never-bound"}

	kept, degraded := r.ExportResolveAndApplyView(context.Background(), scope, retrieval.Request{}, []string{"a", "b"})
	if degraded {
		t.Error("unbound view name: ErrNotFound must NOT be degraded")
	}
	assertIDSet(t, "kept", kept, []string{"a", "b"})
}

func TestResolveAndApplyView_FailsOpen_Default(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	if err := st.TopicViews().CreateView(context.Background(), tenantScope, store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-1", ViewName: "default", AllowTopics: []string{"a"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(viewFailStore{st.TopicViews()}, false, "agent,key") // on_policy_error=open (default)
	readScope := tenantScope
	readScope.Agent = "agent-1"

	kept, degraded := r.ExportResolveAndApplyView(context.Background(), readScope, retrieval.Request{}, []string{"a", "b"})
	if !degraded {
		t.Error("fail-open: expected degraded=true on a GetView error")
	}
	assertIDSet(t, "kept (fail-open, unfiltered)", kept, []string{"a", "b"})
}

func TestResolveAndApplyView_FailsClosed_OperatorOverride(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	if err := st.TopicViews().CreateView(context.Background(), tenantScope, store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-1", ViewName: "default", AllowTopics: []string{"a"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(viewFailStore{st.TopicViews()}, true, "agent,key") // on_policy_error=closed
	readScope := tenantScope
	readScope.Agent = "agent-1"

	kept, degraded := r.ExportResolveAndApplyView(context.Background(), readScope, retrieval.Request{}, []string{"a", "b"})
	if !degraded {
		t.Error("fail-closed: expected degraded=true on a GetView error")
	}
	if kept != nil {
		t.Errorf("fail-closed: expected kept=nil, got %v", kept)
	}
}

// --- Retrieve()-level composed pass -------------------------------------------

// TestRetrieve_View_Subtracts proves a bound view narrows the caller's
// own-scope retrieve results (AC-1: only subtracts).
func TestRetrieve_View_Subtracts(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).
		WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")

	scope := identity.Scope{Tenant: "t-view-sub-" + newID()}
	authID := commitMemoryFull(t, st, scope, "auth widget note qview1", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy widget note qview1", "fact", nil, nil, nil, []string{"deploy"})

	if err := st.TopicViews().CreateView(context.Background(), scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "bound-agent", ViewName: "work", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	readScope := scope
	readScope.Agent = "bound-agent"
	resp, err := r.Retrieve(context.Background(), readScope, retrieval.Request{
		Query: "qview1 widget", Limit: 10, ViewName: "work",
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.DegradedView {
		t.Error("expected DegradedView=false on a clean read")
	}
	gotIDs := map[string]bool{}
	for _, it := range resp.Items {
		gotIDs[it.Memory.ID] = true
	}
	if !gotIDs[authID] {
		t.Error("expected the allow-topic memory in the result")
	}
	if gotIDs[deployID] {
		t.Error("view must have subtracted the non-allow-topic memory")
	}
}

// TestRetrieve_View_UnboundViewNamePassesThrough proves a view_name with no
// matching row leaves results unfiltered and DegradedView=false.
func TestRetrieve_View_UnboundViewNamePassesThrough(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).
		WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")

	scope := identity.Scope{Tenant: "t-view-unbound-" + newID()}
	authID := commitMemoryFull(t, st, scope, "auth widget note qview2", "fact", nil, nil, nil, []string{"auth"})
	deployID := commitMemoryFull(t, st, scope, "deploy widget note qview2", "fact", nil, nil, nil, []string{"deploy"})

	readScope := scope
	readScope.Agent = "never-bound-agent"
	resp, err := r.Retrieve(context.Background(), readScope, retrieval.Request{
		Query: "qview2 widget", Limit: 10, ViewName: "no-such-view",
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.DegradedView {
		t.Error("unbound view name must not surface DegradedView=true")
	}
	gotIDs := map[string]bool{}
	for _, it := range resp.Items {
		gotIDs[it.Memory.ID] = true
	}
	if !gotIDs[authID] || !gotIDs[deployID] {
		t.Errorf("unbound view: results must be unfiltered, got %v", gotIDs)
	}
}

// TestRetrieve_View_ComposesWithAgentFilterAndRequestFilter proves ae9's pass
// runs AFTER both ae6's request-topic pass and ae1's agent-policy pass, so the
// effective result is a THREE-way intersection.
func TestRetrieve_View_ComposesWithAgentFilterAndRequestFilter(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	gw := openMockGateway(t, 4)
	vi := vindex.New(st.Vectors(), 4, "test")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r := retrieval.New(st.Memories(), st.Records(), vi, gw, log).
		WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")

	scope := identity.Scope{Tenant: "t-view-compose-" + newID()}
	// survivor: tagged "auth" only — passes the request filter (no "internal"),
	// the agent policy (auth ∈ {auth,billing}), and the named view (auth ∈
	// {auth,public}).
	survivorID := commitMemoryFull(t, st, scope, "note qview3 survivor", "fact", nil, nil, nil, []string{"auth"})
	// failsRequest: tagged "auth","internal" — passes agent+view but the
	// REQUEST filter (exclude internal) subtracts it.
	failsRequestID := commitMemoryFull(t, st, scope, "note qview3 internal", "fact", nil, nil, nil, []string{"auth", "internal"})
	// failsAgent: tagged "deploy" only — "deploy" is NOT in the agent policy's
	// allow set {auth,billing}, so the AGENT pass subtracts it (before the view
	// pass would even see it).
	failsAgentID := commitMemoryFull(t, st, scope, "note qview3 deploy", "fact", nil, nil, nil, []string{"deploy"})
	// failsView: tagged "billing" only — "billing" IS in the agent policy's
	// allow set (so ae1 lets it through) but is NOT in the named view's allow
	// set {auth,public}, so the VIEW pass subtracts it — isolating ae9's
	// contribution from ae1's.
	failsViewID := commitMemoryFull(t, st, scope, "note qview3 billing", "fact", nil, nil, nil, []string{"billing"})

	// ae1 agent policy: allow "auth"/"billing" (excludes "deploy").
	if err := st.TopicViews().PutAgentPolicy(context.Background(), scope, store.AgentPolicy{
		AgentID: "bound-agent", AllowTopics: []string{"auth", "billing"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}
	// ae9 named view "work": allow "auth"/"public" (excludes "billing") — a
	// DIFFERENT, narrower constraint than the agent policy above, so the two
	// passes each subtract something the other alone would not.
	if err := st.TopicViews().CreateView(context.Background(), scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "bound-agent", ViewName: "work", AllowTopics: []string{"auth", "public"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	readScope := scope
	readScope.Agent = "bound-agent"
	resp, err := r.Retrieve(context.Background(), readScope, retrieval.Request{
		Query: "qview3", Limit: 10, ViewName: "work", ExcludeTopics: []string{"internal"},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if resp.DegradedTopicFilter || resp.DegradedAgentFilter || resp.DegradedView {
		t.Error("expected all three passes to apply cleanly (no degradation)")
	}
	gotIDs := map[string]bool{}
	for _, it := range resp.Items {
		gotIDs[it.Memory.ID] = true
	}
	if !gotIDs[survivorID] {
		t.Error("expected the auth-only memory to survive all three filters")
	}
	if gotIDs[failsRequestID] {
		t.Error("request filter (exclude internal) must have subtracted the auth+internal memory")
	}
	if gotIDs[failsAgentID] {
		t.Error("agent policy (allow auth,billing) must have subtracted the deploy-only memory")
	}
	if gotIDs[failsViewID] {
		t.Error("named view (allow auth,public) must have subtracted the billing-only memory")
	}
}

// --- Cache bypass -------------------------------------------------------------

func TestHasViewApply(t *testing.T) {
	t.Parallel()
	st := openStore(t)

	cases := []struct {
		name         string
		enabled      bool
		wireStore    bool
		scope        identity.Scope
		req          retrieval.Request
		wantHasApply bool
	}{
		{"disabled", false, true, identity.Scope{Tenant: "t1", Agent: "a1"}, retrieval.Request{}, false},
		{"no store", true, false, identity.Scope{Tenant: "t1", Agent: "a1"}, retrieval.Request{}, false},
		{"no subject", true, true, identity.Scope{Tenant: "t1"}, retrieval.Request{}, false},
		{"agent subject", true, true, identity.Scope{Tenant: "t1", Agent: "a1"}, retrieval.Request{}, true},
		{"key subject", true, true, identity.Scope{Tenant: "t1"}, retrieval.Request{CredentialKeyID: "sk_1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), tc.enabled)
			if tc.wireStore {
				r = r.SetTopicViews(st.TopicViews(), false, "agent,key")
			} else {
				r = r.SetTopicViews(nil, false, "agent,key")
			}
			if got := r.ExportHasViewApply(tc.scope, tc.req); got != tc.wantHasApply {
				t.Errorf("hasViewApply = %v, want %v", got, tc.wantHasApply)
			}
		})
	}
}

// --- Concurrency (§5) ----------------------------------------------------------

// TestResolveAndApplyView_ConcurrentSafe proves resolveAndApplyView is safe to
// call from N goroutines on shared input (proven under -race).
func TestResolveAndApplyView_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	st := openStore(t)
	tenantScope := identity.Scope{Tenant: "t-" + newID()}
	if err := st.TopicViews().CreateView(context.Background(), tenantScope, store.TopicView{
		SubjectKind: "agent", SubjectID: "agent-1", ViewName: "default", AllowTopics: []string{"a"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	r := newTestRetriever(t, st).WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")
	readScope := tenantScope
	readScope.Agent = "agent-1"

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.ExportResolveAndApplyView(context.Background(), readScope, retrieval.Request{}, []string{"x", "y"})
		}()
	}
	wg.Wait()
}

// --- AC-4 grep gate: ae9 reuses ae6's filter, defines no second filter -------

// TestViews_NoSecondFilterFunction is the AC-4 grep gate: views.go must define
// ONLY a resolver (resolveAndApplyView) and must call ae6's EXISTING
// filterByTopicOwnScope — never redefine a topic-filter function. Also asserts
// grants' fail-CLOSED filterByTopic remains textually distinct (D-139 not
// collapsed).
func TestViews_NoSecondFilterFunction(t *testing.T) {
	t.Parallel()
	viewsSrc, err := os.ReadFile("views.go")
	if err != nil {
		t.Fatalf("read views.go: %v", err)
	}
	grantsSrc, err := os.ReadFile("grants.go")
	if err != nil {
		t.Fatalf("read grants.go: %v", err)
	}
	views := string(viewsSrc)

	if !strings.Contains(views, "func (r *Retriever) resolveAndApplyView(") {
		t.Error("views.go must define resolveAndApplyView")
	}
	for _, forbidden := range []string{
		"func (r *Retriever) filterByTopicOwnScope(",
		"func (r *Retriever) filterByView(",
		"func filterByView(",
	} {
		if strings.Contains(views, forbidden) {
			t.Errorf("views.go must not redefine a topic-filter function (AC-4); found %q", forbidden)
		}
	}
	if !strings.Contains(views, "r.filterByTopicOwnScope(") {
		t.Error("ae9 must reuse ae6's existing filterByTopicOwnScope, not a private copy")
	}
	if !strings.Contains(string(grantsSrc), "func (r *Retriever) filterByTopic(") {
		t.Error("grants' fail-closed filterByTopic must remain distinct (D-139 not collapsed)")
	}
}
