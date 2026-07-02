// topicviews_test.go proves the ae9 named per-agent/per-key topic VIEWS
// (D-149/D-151) end to end over real drivers: the full
// create->apply->update->apply->delete lifecycle narrows a bound subject's
// own-scope memory_retrieve results and an unbound subject is unfiltered
// (SDK, HTTP, MCP), a forced views-store error fails OPEN by default
// (DegradedView=true, D-139/D-036) and fails CLOSED under the
// on_policy_error=closed operator override, the "key" subject resolves via
// the real MCP KeyIDFromContext plumbing over a real HTTP-authenticated MCP
// session, and a view bound in one tenant never applies to (or is visible
// from) another tenant (P3). Runs under -race. Postgres subtests are gated on
// STOWAGE_TEST_PG_DSN, the established pattern (pgstore_test.go,
// agentfilter_test.go) — sqlite always runs.
package integration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
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
	"github.com/hurtener/stowage/internal/views"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// topicViewsTestLog returns a quiet slog.Logger for helpers in this file that
// construct a views.Service directly against a re-opened store handle.
func topicViewsTestLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// topicViewsConfig returns a baseConfig with the named-view apply path
// enabled (retrieval.agent_views.enabled=true, the SAME shared knob ae1's
// agent filter uses, D-151) — off by default (D-034), so every topic-view
// integration test must opt in explicitly.
func topicViewsConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := baseConfig(t)
	cfg.Retrieval.AgentViews.Enabled = true
	cfg.Retrieval.TopicFilterScoringK = topicFilterScoringKTest
	return cfg
}

// seedTopicViewMemory commits one active memory tagged with topics, directly
// through the store (mirrors seedAgentFilterMemory).
func seedTopicViewMemory(t *testing.T, st store.Store, scope identity.Scope, content string, topics []string) string {
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
		t.Fatalf("seedTopicViewMemory: %v", err)
	}
	return id
}

// retrieveMCPWithViewAndMeta calls memory_retrieve over an in-memory MCP
// session with view_name plus an optional _meta map (mirrors
// retrieveMCPWithMeta, agentfilter_test.go), returning the DegradedView
// marker alongside items.
func retrieveMCPWithViewAndMeta(t *testing.T, cfg config.Config, tenant string, in mcpserver.RetrieveInput, meta map[string]any) stowage.RetrieveResponse {
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "topicviews-client", Version: "0.0.0"}, nil)
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
		DegradedView:        out.DegradedView,
		CacheHit:            out.CacheHit,
		API:                 out.API,
	}
}

// TestTopicViews_Lifecycle_AllSurfaces is AC-1/AC-6/AC-7: the full
// create->apply->update->apply->delete lifecycle over real drivers, proving a
// bound view narrows own-scope results (SDK ViewName, HTTP view_name+agent_id,
// MCP view_name+_meta.agent_id) and the same view no longer applies (unfiltered,
// not degraded) once deleted.
func TestTopicViews_Lifecycle_AllSurfaces(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			cfg.Retrieval.AgentViews.Enabled = true
			cfg.Retrieval.TopicFilterScoringK = topicFilterScoringKTest
			tenant := uniqueTenant("topicviews-lifecycle-" + driver)
			scope := identity.Scope{Tenant: tenant}

			ctx := context.Background()
			st, err := store.Open(ctx, cfg.Store)
			if err != nil {
				t.Fatalf("open store for seeding: %v", err)
			}
			if err := st.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			authID := seedTopicViewMemory(t, st, scope, "auth widget note qvw01", []string{"auth"})
			deployID := seedTopicViewMemory(t, st, scope, "deploy widget note qvw01", []string{"deploy"})
			billingID := seedTopicViewMemory(t, st, scope, "billing widget note qvw01", []string{"billing"})

			viewsSvc := views.New(st.TopicViews(), st.Events(), topicViewsTestLog())
			if _, err := viewsSvc.CreateView(ctx, scope, store.TopicView{
				SubjectKind: "agent", SubjectID: "bound-agent", ViewName: "work",
				AllowTopics: []string{"auth"},
			}); err != nil {
				t.Fatalf("CreateView: %v", err)
			}
			_ = st.Close(ctx)

			embReq := stowage.RetrieveRequest{Query: "qvw01 widget", Limit: 10, AgentID: "bound-agent", ViewName: "work"}
			emb := retrieveEmbedded(t, cfg, tenant, embReq)
			htp := retrieveHTTP(t, cfg, tenant, embReq)
			mcpResp := retrieveMCPWithViewAndMeta(t, cfg, tenant,
				mcpserver.RetrieveInput{Query: "qvw01 widget", Limit: 10, ViewName: "work"},
				map[string]any{"agent_id": "bound-agent"},
			)

			for label, resp := range map[string]stowage.RetrieveResponse{"embedded": emb, "http": htp, "mcp": mcpResp} {
				if resp.DegradedView {
					t.Errorf("%s: expected DegradedView=false on a clean bound-view read", label)
				}
				got := idSetOf(resp)
				if !got[authID] {
					t.Errorf("%s: expected the allow-topic memory %s in the bound view's result", label, authID)
				}
				if got[deployID] || got[billingID] {
					t.Errorf("%s: named view must have subtracted the non-allow-topic memories", label)
				}
			}

			// ── UPDATE: replace the allow set — apply narrows differently ──
			st2, err := store.Open(ctx, cfg.Store)
			if err != nil {
				t.Fatalf("re-open store for update: %v", err)
			}
			viewsSvc2 := views.New(st2.TopicViews(), st2.Events(), topicViewsTestLog())
			if _, err := viewsSvc2.UpdateView(ctx, scope, store.TopicView{
				SubjectKind: "agent", SubjectID: "bound-agent", ViewName: "work",
				AllowTopics: []string{"billing"},
			}); err != nil {
				t.Fatalf("UpdateView: %v", err)
			}
			_ = st2.Close(ctx)

			embUpdated := retrieveEmbedded(t, cfg, tenant, embReq)
			gotUpdated := idSetOf(embUpdated)
			if !gotUpdated[billingID] {
				t.Error("after update: expected the NEW allow-topic memory (billing) in the result")
			}
			if gotUpdated[authID] {
				t.Error("after update: the view's OLD allow set (auth) must no longer apply (full replace)")
			}

			// ── DELETE: apply is now unbound (unfiltered, not degraded) ──
			st3, err := store.Open(ctx, cfg.Store)
			if err != nil {
				t.Fatalf("re-open store for delete: %v", err)
			}
			viewsSvc3 := views.New(st3.TopicViews(), st3.Events(), topicViewsTestLog())
			if err := viewsSvc3.DeleteView(ctx, scope, "agent", "bound-agent", "work"); err != nil {
				t.Fatalf("DeleteView: %v", err)
			}
			_ = st3.Close(ctx)

			embDeleted := retrieveEmbedded(t, cfg, tenant, embReq)
			if embDeleted.DegradedView {
				t.Error("after delete: unbound view_name must not surface DegradedView=true")
			}
			gotDeleted := idSetOf(embDeleted)
			if !gotDeleted[authID] || !gotDeleted[deployID] || !gotDeleted[billingID] {
				t.Errorf("after delete: expected all memories unfiltered (view unbound), got %v", gotDeleted)
			}
		})
	}
}

// TestTopicViews_UnboundViewName_AllSurfaces proves a view_name with no
// matching row leaves the caller's own-scope results unfiltered and
// DegradedView=false, on every surface (AC-2's ae9 analogue).
func TestTopicViews_UnboundViewName_AllSurfaces(t *testing.T) {
	cfg := topicViewsConfig(t)
	tenant := uniqueTenant("topicviews-unbound")
	scope := identity.Scope{Tenant: tenant}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	authID := seedTopicViewMemory(t, st, scope, "auth widget note qvw02", []string{"auth"})
	deployID := seedTopicViewMemory(t, st, scope, "deploy widget note qvw02", []string{"deploy"})
	_ = st.Close(ctx)

	embReq := stowage.RetrieveRequest{Query: "qvw02 widget", Limit: 10, AgentID: "never-bound-agent", ViewName: "no-such-view"}
	emb := retrieveEmbedded(t, cfg, tenant, embReq)
	htp := retrieveHTTP(t, cfg, tenant, embReq)
	mcpResp := retrieveMCPWithViewAndMeta(t, cfg, tenant,
		mcpserver.RetrieveInput{Query: "qvw02 widget", Limit: 10, ViewName: "no-such-view"},
		map[string]any{"agent_id": "never-bound-agent"},
	)

	for label, resp := range map[string]stowage.RetrieveResponse{"embedded": emb, "http": htp, "mcp": mcpResp} {
		if resp.DegradedView {
			t.Errorf("%s: unbound view_name must not surface DegradedView=true", label)
		}
		got := idSetOf(resp)
		if !got[authID] || !got[deployID] {
			t.Errorf("%s: unbound view must leave results unfiltered, got %v", label, got)
		}
	}
}

// viewFaultStore wraps a real store.TopicViewStore but fails every GetView
// call, forcing the D-139 fail-open/fail-closed path deterministically over a
// real driver-backed store (mirrors agentPolicyFaultStore).
type viewFaultStore struct {
	store.TopicViewStore
}

func (v viewFaultStore) GetView(context.Context, identity.Scope, string, string, string) (*store.TopicView, error) {
	return nil, errors.New("synthetic GetView failure (integration fault injection)")
}

// TestTopicViews_FailsOpen_HTTPAndMCP is D-139/D-036 (default on_policy_error):
// a forced views-store error on a real driver-backed store returns the
// caller's own UNFILTERED results with DegradedView=true, on HTTP and MCP.
func TestTopicViews_FailsOpen_HTTPAndMCP(t *testing.T) {
	cfg := topicViewsConfig(t)
	tenant := uniqueTenant("topicviews-failopen")
	scope := identity.Scope{Tenant: tenant}

	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	const uniqueTerm = "failopenqvw03topicviewintegration"
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
	viewsSvc := views.New(stk.Store.TopicViews(), stk.Store.Events(), stk.Log)
	if _, err := viewsSvc.CreateView(context.Background(), scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "bound-agent", ViewName: "work", AllowTopics: []string{"any-topic"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	faultyViews := viewFaultStore{stk.Store.TopicViews()}
	stk.Retriever = retrieval.New(stk.Store.Memories(), stk.Store.Records(), stk.VIndex, stk.Gateway, stk.Log).
		WithTopicFilterScoringK(cfg.Retrieval.TopicFilterScoringK).
		WithAgentPolicy(stk.Store.TopicViews(), true).
		SetTopicViews(faultyViews, false, "agent,key")

	// HTTP and MCP below share ONE Retriever (and its result cache) — disable
	// caching so the second call cannot silently hit the first call's cached
	// entry (mirrors agentfilter_test.go's identical caveat).
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
		Query: uniqueTerm, Limit: 5, AgentID: "bound-agent", ViewName: "work",
	})
	if err != nil {
		t.Fatalf("http Retrieve: %v", err)
	}
	if !httpResp.DegradedView {
		t.Error("http: expected DegradedView=true on a forced GetView error")
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
	mcpClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "topicviews-failopen", Version: "0.0.0"}, nil)
	session, err := mcpClient.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "memory_retrieve",
		Arguments: mcpserver.RetrieveInput{Query: uniqueTerm, Limit: 5, ViewName: "work"},
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
	if !mcpOut.DegradedView {
		t.Error("mcp: expected DegradedView=true on a forced GetView error")
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

// TestTopicViews_FailsClosed_OperatorOverride proves
// retrieval.agent_views.on_policy_error=closed drops results (still
// DegradedView=true) on a forced views-store error, rather than the D-139
// default fail-open behaviour.
func TestTopicViews_FailsClosed_OperatorOverride(t *testing.T) {
	cfg := topicViewsConfig(t)
	tenant := uniqueTenant("topicviews-failclosed")
	scope := identity.Scope{Tenant: tenant}

	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	const uniqueTerm = "failclosedqvw04topicviewintegration"
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
	viewsSvc := views.New(stk.Store.TopicViews(), stk.Store.Events(), stk.Log)
	if _, err := viewsSvc.CreateView(context.Background(), scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "bound-agent", ViewName: "work", AllowTopics: []string{"any-topic"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	faultyViews := viewFaultStore{stk.Store.TopicViews()}
	// on_policy_error=closed: the 2nd SetTopicViews arg is true.
	stk.Retriever = retrieval.New(stk.Store.Memories(), stk.Store.Records(), stk.VIndex, stk.Gateway, stk.Log).
		WithTopicFilterScoringK(cfg.Retrieval.TopicFilterScoringK).
		WithAgentPolicy(stk.Store.TopicViews(), true).
		SetTopicViews(faultyViews, true, "agent,key")

	t.Setenv("STOWAGE_CACHE_OFF", "1")

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
		Query: uniqueTerm, Limit: 5, AgentID: "bound-agent", ViewName: "work",
	})
	if err != nil {
		t.Fatalf("http Retrieve: %v", err)
	}
	if !httpResp.DegradedView {
		t.Error("expected DegradedView=true on a forced GetView error under on_policy_error=closed")
	}
	if len(httpResp.Items) != 0 {
		t.Errorf("on_policy_error=closed must drop ALL results, got %d items", len(httpResp.Items))
	}
}

// TestTopicViews_KeySubject_MCP_ViaKeyIDFromContext proves the "key" view
// subject resolves over MCP through the REAL AuthMiddleware/KeyringMiddleware
// plumbing (not stdio's StdioScopeFn, which carries no per-request key) — a
// view bound to (subject_kind="key", subject_id=<the caller's OWN verified
// key.ID>) narrows its own-scope results when the caller supplies no
// _meta.agent_id at all.
func TestTopicViews_KeySubject_MCP_ViaKeyIDFromContext(t *testing.T) {
	cfg := topicViewsConfig(t)
	tenant := uniqueTenant("topicviews-keysubject")
	scope := identity.Scope{Tenant: tenant}

	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	authID := seedTopicViewMemory(t, stk.Store, scope, "auth widget note qvw05", []string{"auth"})
	deployID := seedTopicViewMemory(t, stk.Store, scope, "deploy widget note qvw05", []string{"deploy"})

	key, plaintext, err := auth.Generate(tenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	// Bind a "key" view to THIS caller's own verified key id (key.ID) —
	// resolved server-side, never a wire argument.
	viewsSvc := views.New(stk.Store.TopicViews(), stk.Store.Events(), stk.Log)
	if _, err := viewsSvc.CreateView(context.Background(), scope, store.TopicView{
		SubjectKind: "key", SubjectID: key.ID, ViewName: "default", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("CreateView (key subject): %v", err)
	}

	// Real MCP-over-HTTP with KeyringMiddleware — the ONLY path that populates
	// KeyIDFromContext (mirrors comount_test.go's mcpSession harness).
	mcpSrv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In, Log: stk.Log,
		ScopeFn: mcpserver.CtxScopeFn(), Profile: cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	mcpHandler, err := mcpSrv.HTTPHandler(nil)
	if err != nil {
		t.Fatalf("mcp HTTPHandler: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mcpHTTP := &http.Server{
		Handler:           mcpserver.KeyringMiddleware(stk.Store.Keys(), mcpHandler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = mcpHTTP.Serve(ln) }()
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mcpHTTP.Shutdown(shutCtx)
	})

	ctx := context.Background()
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:             "http://" + ln.Addr().String(),
		HTTPClient:           &http.Client{Transport: bearerRT{base: http.DefaultTransport, token: plaintext}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "topicviews-keysubject-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	// NO _meta.agent_id — the caller has no agent identity, so the "key"
	// subject (this session's OWN verified key.ID) must resolve instead.
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "memory_retrieve",
		Arguments: mcpserver.RetrieveInput{Query: "qvw05 widget", Limit: 10, ViewName: "default"},
	})
	if err != nil {
		t.Fatalf("CallTool memory_retrieve: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_retrieve returned IsError: %+v", res.Content)
	}
	var out mcpserver.RetrieveOutput
	decodeStructured(t, res, &out)
	if out.DegradedView {
		t.Error("expected DegradedView=false on a clean key-subject read")
	}
	got := map[string]bool{}
	for _, it := range out.Items {
		got[it.ID] = true
	}
	if !got[authID] {
		t.Error("expected the allow-topic memory via the key-subject view")
	}
	if got[deployID] {
		t.Error("the key-subject view must have subtracted the non-allow-topic memory")
	}
}

// TestTopicViews_CrossScopeGuard is P3: a view bound in tenant A must never
// apply to (or be visible from) tenant B — an unbound agent in tenant B with
// the SAME agent_id sees its own unfiltered results, and tenant B's admin
// list/get never surface tenant A's view.
func TestTopicViews_CrossScopeGuard(t *testing.T) {
	cfg := topicViewsConfig(t)
	tenantA := uniqueTenant("topicviews-iso-a")
	tenantB := uniqueTenant("topicviews-iso-b")
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
	viewsSvc := views.New(st.TopicViews(), st.Events(), topicViewsTestLog())
	if _, err := viewsSvc.CreateView(ctx, scopeA, store.TopicView{
		SubjectKind: "agent", SubjectID: "shared-id", ViewName: "work", AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("CreateView tenant A: %v", err)
	}
	authID := seedTopicViewMemory(t, st, scopeB, "auth widget note qvw06", []string{"auth"})
	deployID := seedTopicViewMemory(t, st, scopeB, "deploy widget note qvw06", []string{"deploy"})

	// Admin cross-tenant leak guard: tenant B's list/get must never see tenant A's view.
	if _, err := viewsSvc.GetView(ctx, scopeB, "agent", "shared-id", "work"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant GetView: got %v want ErrNotFound", err)
	}
	listB, err := viewsSvc.ListViews(ctx, scopeB, "", "")
	if err != nil {
		t.Fatalf("ListViews B: %v", err)
	}
	for _, v := range listB {
		if v.SubjectID == "shared-id" {
			t.Error("cross-tenant view visible in tenant B's list (P3 leak)")
		}
	}
	_ = st.Close(ctx)

	// Apply-path leak guard: tenant B's caller (same agent_id) is UNBOUND —
	// unfiltered, not degraded.
	embReq := stowage.RetrieveRequest{Query: "qvw06 widget", Limit: 10, AgentID: "shared-id", ViewName: "work"}
	emb := retrieveEmbedded(t, cfg, tenantB, embReq)
	if emb.DegradedView {
		t.Error("cross-tenant unbound view must not surface DegradedView=true")
	}
	got := idSetOf(emb)
	if !got[authID] || !got[deployID] {
		t.Errorf("tenant A's view must not apply to tenant B's agent (cross-tenant isolation, P3); got %v", got)
	}
}
