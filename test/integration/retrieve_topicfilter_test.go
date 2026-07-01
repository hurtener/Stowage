// retrieve_topicfilter_test.go proves the ae6 own-scope topic filter (D-139/
// D-144) end to end over real drivers: the no-underfill fixture (on-topic
// memories ranked below the balanced profile's default scoringK) still fills
// `limit` on all three single-user surfaces (SDK, HTTP, MCP), a topic filter
// never returns another scope's row (P3), and a forced topic-store error
// fails OPEN — DegradedTopicFilter=true with the caller's own unfiltered
// results, never an error and never a dropped result. Runs under -race.
// Postgres subtests are gated on STOWAGE_TEST_PG_DSN, the established
// pattern (internal/store/pgstore/pgstore_test.go, retrieve_lean_read_test.go)
// — sqlite always runs.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// topicFilterScoringKTest mirrors the config default (retrieval.topic_filter_scoring_k,
// D-144) so tests exercising the widened-window path see the same window a real
// deployment would.
const topicFilterScoringKTest = 100

// seedTopicFilterMemories seeds offCount "off-topic" memories that hit ALL THREE
// non-vector lanes (lexical + queries + structured) — so their RRF score dominates
// the fused top-ScoringK — and onCount "on-topic" memories (tagged topicKey) that
// hit the lexical lane ONLY, ranking below the balanced profile's default
// ScoringK (20) but within its LaneK (100). This is the AC-2 no-underfill fixture:
// filtering the scoringK-TRIMMED pool (the naive/rejected approach) would drop
// every on-topic candidate; filtering the fused pool BEFORE the trim (D-144, the
// pinned approach) does not.
func seedTopicFilterMemories(t *testing.T, cfg config.Config, tenant, query, topicKey string, offCount, onCount int) (offIDs, onIDs []string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("seed topicfilter: open: %v", err)
	}
	defer func() { _ = st.Close(ctx) }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("seed topicfilter: migrate: %v", err)
	}
	scope := identity.Scope{Tenant: tenant}
	now := time.Now().UnixMilli()

	commit := func(content string, entities, keywords, queries, topics []string) string {
		id := ulid.Make().String()
		cs := store.CommitSet{
			Action: store.ActionAdd,
			Memory: store.Memory{
				ID: id, Kind: "fact", Content: content, Status: "active",
				Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
				PrivacyZone: "public", CreatedAt: now, UpdatedAt: now,
			},
			Entities: entities, Keywords: keywords, Queries: queries, Topics: topics,
			Scope: scope,
		}
		if err := st.Memories().Commit(ctx, scope, cs); err != nil {
			t.Fatalf("seed topicfilter commit: %v", err)
		}
		return id
	}

	for i := 0; i < offCount; i++ {
		offIDs = append(offIDs, commit(fmt.Sprintf("%s note off-topic %d", query, i),
			[]string{query}, []string{query}, []string{query + " note"}, nil))
	}
	for i := 0; i < onCount; i++ {
		onIDs = append(onIDs, commit(fmt.Sprintf("%s config on-topic %d", query, i),
			nil, nil, nil, []string{topicKey}))
	}
	return offIDs, onIDs
}

func retrieveEmbedded(t *testing.T, cfg config.Config, tenant string, req stowage.RetrieveRequest) stowage.RetrieveResponse {
	t.Helper()
	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(tenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	})
	resp, err := client.Retrieve(ctx, req)
	if err != nil {
		t.Fatalf("embedded Retrieve: %v", err)
	}
	return resp
}

func retrieveHTTP(t *testing.T, cfg config.Config, tenant string, req stowage.RetrieveRequest) stowage.RetrieveResponse {
	t.Helper()
	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetStage(p.Stage)
	srv.SetRetriever(stk.Retriever)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	key, plaintext, err := auth.Generate(tenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}
	client := stowage.NewHTTP(ts.URL, plaintext)
	resp, err := client.Retrieve(ctx, req)
	if err != nil {
		t.Fatalf("http Retrieve: %v", err)
	}
	return resp
}

func retrieveMCP(t *testing.T, cfg config.Config, tenant string, in mcpserver.RetrieveInput) stowage.RetrieveResponse {
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "topicfilter-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_retrieve", Arguments: in})
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
		CacheHit:            out.CacheHit,
		API:                 out.API,
	}
}

// mcpItemsToSDK projects mcpserver.RetrieveItem to sdk.MemoryItem for the ID-set
// comparisons this file needs (Content/Kind/etc. carried through for completeness).
func mcpItemsToSDK(items []mcpserver.RetrieveItem) []stowage.MemoryItem {
	out := make([]stowage.MemoryItem, len(items))
	for i, it := range items {
		out[i] = stowage.MemoryItem{ID: it.ID, Kind: it.Kind, Content: it.Content, Score: it.Score, Citation: it.Citation}
	}
	return out
}

// idSet returns a set of the ids in resp.Items.
func idSetOf(resp stowage.RetrieveResponse) map[string]bool {
	s := make(map[string]bool, len(resp.Items))
	for _, it := range resp.Items {
		s[it.ID] = true
	}
	return s
}

// TestTopicFilter_NoUnderfill_AllSurfaces is AC-2 (the core no-underfill risk):
// with on-topic memories ranked BELOW the balanced profile's default ScoringK
// (20) but within its LaneK (100), a topic-filtered retrieve still fills
// `limit` — on SDK, HTTP, and MCP, over real drivers.
func TestTopicFilter_NoUnderfill_AllSurfaces(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			cfg.Retrieval.TopicFilterScoringK = topicFilterScoringKTest
			tenant := uniqueTenant("topicfilter-underfill-" + driver)
			const query = "gizmoqvzx"
			const topicKey = "target"
			const onCount = 12
			_, onIDs := seedTopicFilterMemories(t, cfg, tenant, query, topicKey, 25, onCount)

			req := stowage.RetrieveRequest{Query: query, Limit: onCount, Profile: "balanced", IncludeTopics: []string{topicKey}}

			emb := retrieveEmbedded(t, cfg, tenant, req)
			htp := retrieveHTTP(t, cfg, tenant, req)
			mcp := retrieveMCP(t, cfg, tenant, mcpserver.RetrieveInput{
				Query: query, Limit: onCount, Profile: "balanced", IncludeTopics: []string{topicKey},
			})

			for label, resp := range map[string]stowage.RetrieveResponse{"embedded": emb, "http": htp, "mcp": mcp} {
				if len(resp.Items) != onCount {
					t.Errorf("%s: no-underfill violated: got %d items, want %d (limit)", label, len(resp.Items), onCount)
				}
				if resp.DegradedTopicFilter {
					t.Errorf("%s: expected DegradedTopicFilter=false on a clean topic-store read", label)
				}
				got := idSetOf(resp)
				for _, id := range onIDs {
					if !got[id] {
						t.Errorf("%s: missing on-topic memory %s from the filled result", label, id)
					}
				}
			}
		})
	}
}

// TestTopicFilter_ScopeIsolation_AllSurfaces is AC-1 (P3): a topic-filtered
// retrieve never returns another tenant's memory, even when that memory
// carries the SAME topic key, on every surface, over real drivers.
func TestTopicFilter_ScopeIsolation_AllSurfaces(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			cfg.Retrieval.TopicFilterScoringK = topicFilterScoringKTest
			tenantA := uniqueTenant("topicfilter-iso-a-" + driver)
			tenantB := uniqueTenant("topicfilter-iso-b-" + driver)
			const query = "widgetqvzx"
			const topicKey = "shared-topic"

			_, aIDs := seedTopicFilterMemories(t, cfg, tenantA, query, topicKey, 0, 3)
			_, _ = seedTopicFilterMemories(t, cfg, tenantB, query, topicKey, 0, 1)

			req := stowage.RetrieveRequest{Query: query, Limit: 20, IncludeTopics: []string{topicKey}}
			embB := retrieveEmbedded(t, cfg, tenantB, req)
			htpB := retrieveHTTP(t, cfg, tenantB, req)
			mcpB := retrieveMCP(t, cfg, tenantB, mcpserver.RetrieveInput{Query: query, Limit: 20, IncludeTopics: []string{topicKey}})

			for label, resp := range map[string]stowage.RetrieveResponse{"embedded": embB, "http": htpB, "mcp": mcpB} {
				got := idSetOf(resp)
				for _, id := range aIDs {
					if got[id] {
						t.Fatalf("%s: P3 violated — tenant B's topic-filtered retrieve saw tenant A's memory %s", label, id)
					}
				}
			}
		})
	}
}

// topicFilterFaultMemoryStore wraps a real store.MemoryStore but fails every
// MemoriesTopics call, forcing the D-139 fail-open path deterministically over
// a real driver-backed store (mirrors the unit-level fault store in
// internal/retrieval/topicfilter_test.go).
type topicFilterFaultMemoryStore struct {
	store.MemoryStore
}

func (m topicFilterFaultMemoryStore) MemoriesTopics(context.Context, identity.Scope, []string) (map[string][]string, error) {
	return nil, errors.New("synthetic MemoriesTopics failure (integration fault injection)")
}

// TestTopicFilter_FailsOpen_HTTPAndMCP is AC-3 (D-139): a forced MemoriesTopics
// error on a real driver-backed store returns the caller's own UNFILTERED
// results with DegradedTopicFilter=true, on HTTP and MCP. (The embedded SDK
// path always constructs its own Retriever inside boot.Open with no seam to
// swap in a fault-injecting store from a test, so this fault-injection test
// covers HTTP+MCP; the fail-open Retrieve behavior itself is proven directly,
// once, in internal/retrieval/topicfilter_test.go.)
func TestTopicFilter_FailsOpen_HTTPAndMCP(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Retrieval.TopicFilterScoringK = topicFilterScoringKTest
	tenant := uniqueTenant("topicfilter-failopen")
	scope := identity.Scope{Tenant: tenant}

	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	const uniqueTerm = "failopenqvzxintegration"
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

	faultyMem := topicFilterFaultMemoryStore{stk.Store.Memories()}
	stk.Retriever = retrieval.New(faultyMem, stk.Store.Records(), stk.VIndex, stk.Gateway, stk.Log).
		WithTopicFilterScoringK(cfg.Retrieval.TopicFilterScoringK)

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
		Query: uniqueTerm, Limit: 5, IncludeTopics: []string{"any-topic"},
	})
	if err != nil {
		t.Fatalf("http Retrieve: %v", err)
	}
	if !httpResp.DegradedTopicFilter {
		t.Error("http: expected DegradedTopicFilter=true on a forced MemoriesTopics error")
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
	mcpClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "topicfilter-failopen", Version: "0.0.0"}, nil)
	session, err := mcpClient.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "memory_retrieve",
		Arguments: mcpserver.RetrieveInput{
			Query: uniqueTerm, Limit: 5, IncludeTopics: []string{"any-topic"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool memory_retrieve: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_retrieve returned IsError: %+v", res.Content)
	}
	var mcpOut mcpserver.RetrieveOutput
	decodeStructured(t, res, &mcpOut)
	if !mcpOut.DegradedTopicFilter {
		t.Error("mcp: expected DegradedTopicFilter=true on a forced MemoriesTopics error")
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

// TestTopicFilter_ExcludeAndAdditive_SurfaceParity is AC-4/AC-5: an
// exclude_topics retrieve and a no-topic-args retrieve behave identically
// (ids + DegradedTopicFilter) across SDK, HTTP, and MCP for a FIXED query
// (parity is about surface wiring, not driver behavior — sqlite only,
// mirrors TestBrowse_SurfaceParity_FixedPage).
func TestTopicFilter_ExcludeAndAdditive_SurfaceParity(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Retrieval.TopicFilterScoringK = topicFilterScoringKTest
	tenant := uniqueTenant("topicfilter-parity")
	const query = "parityqvzx"
	const excludedTopic = "noisy"

	// "on" set (topicKey=excludedTopic) — the memories that must be excluded.
	_, excluded := seedTopicFilterMemories(t, cfg, tenant, query, excludedTopic, 0, 3)
	// "off" set (no topics at all) — untagged memories that must survive both requests.
	untaggedIDs, _ := seedTopicFilterMemories(t, cfg, tenant, query, "unused-topic", 2, 0)

	// ── exclude_topics=[noisy]: excluded ids must be ABSENT, untagged ids present.
	exReq := stowage.RetrieveRequest{Query: query, Limit: 10, ExcludeTopics: []string{excludedTopic}}
	embEx := retrieveEmbedded(t, cfg, tenant, exReq)
	htpEx := retrieveHTTP(t, cfg, tenant, exReq)
	mcpEx := retrieveMCP(t, cfg, tenant, mcpserver.RetrieveInput{Query: query, Limit: 10, ExcludeTopics: []string{excludedTopic}})

	for label, resp := range map[string]stowage.RetrieveResponse{"embedded": embEx, "http": htpEx, "mcp": mcpEx} {
		got := idSetOf(resp)
		for _, id := range excluded {
			if got[id] {
				t.Errorf("%s: exclude_topics=[%s] leaked excluded memory %s", label, excludedTopic, id)
			}
		}
		for _, id := range untaggedIDs {
			if !got[id] {
				t.Errorf("%s: exclude_topics=[%s] dropped untagged memory %s", label, excludedTopic, id)
			}
		}
		if resp.DegradedTopicFilter {
			t.Errorf("%s: expected DegradedTopicFilter=false", label)
		}
	}

	// ── no topic args at all: additive no-op — every memory (tagged or not) present.
	plainReq := stowage.RetrieveRequest{Query: query, Limit: 10}
	embPlain := retrieveEmbedded(t, cfg, tenant, plainReq)
	htpPlain := retrieveHTTP(t, cfg, tenant, plainReq)
	mcpPlain := retrieveMCP(t, cfg, tenant, mcpserver.RetrieveInput{Query: query, Limit: 10})

	for label, resp := range map[string]stowage.RetrieveResponse{"embedded": embPlain, "http": htpPlain, "mcp": mcpPlain} {
		got := idSetOf(resp)
		for _, id := range excluded {
			if !got[id] {
				t.Errorf("%s: no-topic-args retrieve must NOT drop %s (additive no-op)", label, id)
			}
		}
		if resp.DegradedTopicFilter {
			t.Errorf("%s: expected DegradedTopicFilter=false with no topic args", label)
		}
	}
}

// decodeStructured decodes an MCP CallToolResult's StructuredContent into out.
func decodeStructured(t *testing.T, res *mcpsdk.CallToolResult, out interface{}) {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal StructuredContent: %v", err)
	}
}
