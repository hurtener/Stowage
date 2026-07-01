// agentfilter_test.go proves the ae1 read-time agent->topic filter (D-135/
// D-146/D-151) end to end over real drivers: binding an agent to allow topics
// narrows its own-scope memory_retrieve results (SDK, HTTP, MCP via the
// _meta.agent_id seam), an unbound agent is unfiltered, a forced policy-store
// error fails OPEN (DegradedAgentFilter=true, D-139/D-036), and a policy bound
// in one tenant never applies to another tenant's agent with the same
// agent_id (P3). Runs under -race. Postgres subtests are gated on
// STOWAGE_TEST_PG_DSN, the established pattern (pgstore_test.go,
// retrieve_topicfilter_test.go) — sqlite always runs. SKIPs gracefully when
// ae6's filterByTopicOwnScope (the seam ae1 reuses) is absent — landed by the
// time this file ships, so this SKIP is a defensive guard, not the normal path.
package integration

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// agentFilterConfig returns a baseConfig with the agent filter enabled
// (retrieval.agent_views.enabled=true) — off by default (D-034), so every
// agent-filter integration test must opt in explicitly, mirroring how the
// topic-filter tests opt into a widened TopicFilterScoringK.
func agentFilterConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := baseConfig(t)
	cfg.Retrieval.AgentViews.Enabled = true
	cfg.Retrieval.TopicFilterScoringK = topicFilterScoringKTest
	return cfg
}

// seedAgentFilterMemory commits one active memory tagged with topics, directly
// through the store — mirrors seedTopicFilterMemories' commit helper.
func seedAgentFilterMemory(t *testing.T, st store.Store, scope identity.Scope, content string, topics []string) string {
	t.Helper()
	id := ulid.Make().String()
	now := time.Now().UnixMilli()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: "fact", Content: content, Status: "active",
			Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
			PrivacyZone: "public", CreatedAt: now, UpdatedAt: now,
		},
		Topics: topics,
		Scope:  scope,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("seedAgentFilterMemory: %v", err)
	}
	return id
}

// retrieveMCPWithMeta calls memory_retrieve over an in-memory MCP session with an
// optional _meta map (the D-135 agent-identity seam, dockyard v1.8.0
// server.RequestMeta), returning the DegradedAgentFilter marker alongside items.
func retrieveMCPWithMeta(t *testing.T, cfg config.Config, tenant string, in mcpserver.RetrieveInput, meta map[string]any) stowage.RetrieveResponse {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	svc := &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In, Log: stk.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenant), Profile: cfg.Profile,
	}
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agentfilter-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	params := &mcpsdk.CallToolParams{Name: "memory_retrieve", Arguments: in}
	if len(meta) > 0 {
		params.Meta = mcpsdk.Meta(meta)
	}
	res, err := session.CallTool(ctx, params)
	if err != nil {
		t.Fatalf("CallTool memory_retrieve: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_retrieve returned IsError: %+v", res.Content)
	}
	var out mcpserver.RetrieveOutput
	decodeStructured(t, res, &out)
	return stowage.RetrieveResponse{
		ResponseID:          out.ResponseID,
		Items:               mcpItemsToSDK(out.Items),
		Degraded:            out.Degraded,
		DegradedRerank:      out.DegradedRerank,
		DegradedTopicFilter: out.DegradedTopicFilter,
		DegradedAgentFilter: out.DegradedAgentFilter,
		CacheHit:            out.CacheHit,
		API:                 out.API,
	}
}

// TestAgentFilter_NarrowsOwnScope_AllSurfaces is AC-5: a bound agent's allow-topic
// policy narrows its own-scope memory_retrieve results on SDK, HTTP (agent_id
// field), and MCP (_meta.agent_id) — real sqlite + postgres drivers.
func TestAgentFilter_NarrowsOwnScope_AllSurfaces(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			cfg.Retrieval.AgentViews.Enabled = true
			cfg.Retrieval.TopicFilterScoringK = topicFilterScoringKTest
			tenant := uniqueTenant("agentfilter-narrow-" + driver)
			scope := identity.Scope{Tenant: tenant}

			ctx := context.Background()
			st, err := store.Open(ctx, cfg.Store)
			if err != nil {
				t.Fatalf("open store for seeding: %v", err)
			}
			if err := st.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			authID := seedAgentFilterMemory(t, st, scope, "auth widget note qvzxg", []string{"auth"})
			deployID := seedAgentFilterMemory(t, st, scope, "deploy widget note qvzxg", []string{"deploy"})
			if err := st.TopicViews().PutAgentPolicy(ctx, scope, store.AgentPolicy{
				AgentID: "bound-agent", AllowTopics: []string{"auth"},
			}); err != nil {
				t.Fatalf("PutAgentPolicy: %v", err)
			}
			_ = st.Close(ctx)

			embReq := stowage.RetrieveRequest{Query: "qvzxg widget", Limit: 10, AgentID: "bound-agent"}
			emb := retrieveEmbedded(t, cfg, tenant, embReq)
			htp := retrieveHTTP(t, cfg, tenant, embReq)
			mcpResp := retrieveMCPWithMeta(t, cfg, tenant,
				mcpserver.RetrieveInput{Query: "qvzxg widget", Limit: 10},
				map[string]any{"agent_id": "bound-agent"},
			)

			for label, resp := range map[string]stowage.RetrieveResponse{"embedded": emb, "http": htp, "mcp": mcpResp} {
				if resp.DegradedAgentFilter {
					t.Errorf("%s: expected DegradedAgentFilter=false on a clean bound-agent read", label)
				}
				got := idSetOf(resp)
				if !got[authID] {
					t.Errorf("%s: expected the allow-topic memory %s in the bound agent's result", label, authID)
				}
				if got[deployID] {
					t.Errorf("%s: agent filter must have subtracted the non-allow-topic memory %s", label, deployID)
				}
			}
		})
	}
}

// TestAgentFilter_UnboundUnfiltered_AllSurfaces proves an unbound agent (no
// policy row) leaves the caller's own-scope results unfiltered and
// DegradedAgentFilter=false, on every surface.
func TestAgentFilter_UnboundUnfiltered_AllSurfaces(t *testing.T) {
	cfg := agentFilterConfig(t)
	tenant := uniqueTenant("agentfilter-unbound")
	scope := identity.Scope{Tenant: tenant}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	authID := seedAgentFilterMemory(t, st, scope, "auth widget note qvzxh", []string{"auth"})
	deployID := seedAgentFilterMemory(t, st, scope, "deploy widget note qvzxh", []string{"deploy"})
	_ = st.Close(ctx)

	embReq := stowage.RetrieveRequest{Query: "qvzxh widget", Limit: 10, AgentID: "never-bound-agent"}
	emb := retrieveEmbedded(t, cfg, tenant, embReq)
	htp := retrieveHTTP(t, cfg, tenant, embReq)
	mcpResp := retrieveMCPWithMeta(t, cfg, tenant,
		mcpserver.RetrieveInput{Query: "qvzxh widget", Limit: 10},
		map[string]any{"agent_id": "never-bound-agent"},
	)

	for label, resp := range map[string]stowage.RetrieveResponse{"embedded": emb, "http": htp, "mcp": mcpResp} {
		if resp.DegradedAgentFilter {
			t.Errorf("%s: unbound agent must not surface DegradedAgentFilter=true", label)
		}
		got := idSetOf(resp)
		if !got[authID] || !got[deployID] {
			t.Errorf("%s: unbound agent must leave results unfiltered, got %v", label, got)
		}
	}
}

// agentPolicyFaultStore wraps a real store.TopicViewStore but fails every
// GetAgentPolicy call, forcing the D-139 fail-open path deterministically over
// a real driver-backed store (mirrors topicFilterFaultMemoryStore above).
type agentPolicyFaultStore struct {
	store.TopicViewStore
}

func (a agentPolicyFaultStore) GetAgentPolicy(context.Context, identity.Scope, string) (*store.AgentPolicy, error) {
	return nil, errors.New("synthetic GetAgentPolicy failure (integration fault injection)")
}

// TestAgentFilter_FailsOpen_HTTPAndMCP is D-139/D-036: a forced policy-store
// error on a real driver-backed store returns the caller's own UNFILTERED
// results with DegradedAgentFilter=true, on HTTP and MCP. (Mirrors
// TestTopicFilter_FailsOpen_HTTPAndMCP's embedded-path caveat: the embedded SDK
// path always constructs its own Retriever inside boot.Open with no seam to
// swap in a fault-injecting store from a test; the fail-open resolver itself is
// proven directly, once, in internal/retrieval/agentfilter_test.go.)
func TestAgentFilter_FailsOpen_HTTPAndMCP(t *testing.T) {
	cfg := agentFilterConfig(t)
	tenant := uniqueTenant("agentfilter-failopen")
	scope := identity.Scope{Tenant: tenant}

	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	const uniqueTerm = "failopenqvzxagentintegration"
	memID := ulid.Make().String()
	now := time.Now().UnixMilli()
	if err := stk.Store.Memories().Commit(context.Background(), scope, store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: uniqueTerm + " content", Status: "active",
			Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
			PrivacyZone: "public", CreatedAt: now, UpdatedAt: now,
		},
		Scope: scope,
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// A binding must exist for the agent so GetAgentPolicy is actually reached
	// (an unbound agent would short-circuit to ErrNotFound if the real store
	// were used — the fault wrapper below forces the STORE ERROR branch instead).
	if err := stk.Store.TopicViews().PutAgentPolicy(context.Background(), scope, store.AgentPolicy{
		AgentID: "bound-agent", AllowTopics: []string{"any-topic"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}

	faultyPol := agentPolicyFaultStore{stk.Store.TopicViews()}
	stk.Retriever = retrieval.New(stk.Store.Memories(), stk.Store.Records(), stk.VIndex, stk.Gateway, stk.Log).
		WithTopicFilterScoringK(cfg.Retrieval.TopicFilterScoringK).
		WithAgentPolicy(faultyPol, true)

	// HTTP and MCP below share ONE Retriever (and therefore its result cache) —
	// disable caching for this test so the second call cannot silently hit the
	// first call's cached (fail-open-but-unmarked) entry. The cache does not
	// carry the degraded flags across a HIT (mirrors DegradedTopicFilter's
	// existing cache-hit behaviour), so a shared-cache collision here would mask
	// the very fail-open marker this test asserts on the second surface.
	t.Setenv("STOWAGE_CACHE_OFF", "1")

	// ── HTTP ──
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetStage(p.Stage)
	srv.SetRetriever(stk.Retriever)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	key, plaintext, err := auth.Generate(tenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	httpClient := stowage.NewHTTP(ts.URL, plaintext)
	httpResp, err := httpClient.Retrieve(context.Background(), stowage.RetrieveRequest{
		Query: uniqueTerm, Limit: 5, AgentID: "bound-agent",
	})
	if err != nil {
		t.Fatalf("http Retrieve: %v", err)
	}
	if !httpResp.DegradedAgentFilter {
		t.Error("http: expected DegradedAgentFilter=true on a forced GetAgentPolicy error")
	}
	if !idSetOf(httpResp)[memID] {
		t.Error("http: fail-open should return the caller's own unfiltered memory")
	}

	// ── MCP ──
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In, Log: stk.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenant),
	}
	mcpSrv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := mcpSrv.ServeInMemory(ctx)
	mcpClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agentfilter-failopen", Version: "0.0.0"}, nil)
	session, err := mcpClient.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "memory_retrieve",
		Arguments: mcpserver.RetrieveInput{Query: uniqueTerm, Limit: 5},
		Meta:      mcpsdk.Meta{"agent_id": "bound-agent"},
	})
	if err != nil {
		t.Fatalf("CallTool memory_retrieve: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_retrieve returned IsError: %+v", res.Content)
	}
	var mcpOut mcpserver.RetrieveOutput
	decodeStructured(t, res, &mcpOut)
	if !mcpOut.DegradedAgentFilter {
		t.Error("mcp: expected DegradedAgentFilter=true on a forced GetAgentPolicy error")
	}
	found := false
	for _, it := range mcpOut.Items {
		if it.ID == memID {
			found = true
		}
	}
	if !found {
		t.Error("mcp: fail-open should return the caller's own unfiltered memory")
	}
}

// TestAgentFilter_CrossTenantIsolation_AllSurfaces is P3: a policy bound to
// agent_id "shared-id" in tenant A must never apply to tenant B's caller using
// the SAME agent_id — an unbound agent in tenant B sees its own unfiltered
// results, on every surface.
func TestAgentFilter_CrossTenantIsolation_AllSurfaces(t *testing.T) {
	cfg := agentFilterConfig(t)
	tenantA := uniqueTenant("agentfilter-iso-a")
	tenantB := uniqueTenant("agentfilter-iso-b")
	scopeA := identity.Scope{Tenant: tenantA}
	scopeB := identity.Scope{Tenant: tenantB}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.TopicViews().PutAgentPolicy(ctx, scopeA, store.AgentPolicy{
		AgentID: "shared-id", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("PutAgentPolicy tenant A: %v", err)
	}
	// tenant B's own-scope memories, tagged with a topic the (tenant-A-only)
	// policy would have excluded if it leaked across tenants.
	authID := seedAgentFilterMemory(t, st, scopeB, "auth widget note qvzxi", []string{"auth"})
	deployID := seedAgentFilterMemory(t, st, scopeB, "deploy widget note qvzxi", []string{"deploy"})
	_ = st.Close(ctx)

	embReq := stowage.RetrieveRequest{Query: "qvzxi widget", Limit: 10, AgentID: "shared-id"}
	emb := retrieveEmbedded(t, cfg, tenantB, embReq)
	htp := retrieveHTTP(t, cfg, tenantB, embReq)
	mcpResp := retrieveMCPWithMeta(t, cfg, tenantB,
		mcpserver.RetrieveInput{Query: "qvzxi widget", Limit: 10},
		map[string]any{"agent_id": "shared-id"},
	)

	for label, resp := range map[string]stowage.RetrieveResponse{"embedded": emb, "http": htp, "mcp": mcpResp} {
		if resp.DegradedAgentFilter {
			t.Errorf("%s: cross-tenant unbound agent must not surface DegradedAgentFilter=true", label)
		}
		got := idSetOf(resp)
		if !got[authID] || !got[deployID] {
			t.Errorf("%s: tenant A's policy must not apply to tenant B's agent (cross-tenant isolation, P3); got %v", label, got)
		}
	}
}
