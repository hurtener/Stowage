// live_wave3_test.go is a LIVE 3-surface end-to-end validation of Stowage
// Wave 3's read-time identity capabilities — ae8 the effective-scope resolver
// + read posture (D-148/D-137) and ae9 the named per-agent/per-key topic
// views (D-149/D-151) — against the REAL gateway (bifrost -> OpenRouter:
// embed + complete + rerank, D-075).
//
// Block A (ae8) proves retrieval.read_posture=strict isolates a resolved
// user's own rows and REFUSES a read that resolves to neither a user nor an
// agent (ErrIdentityRequired) BEFORE any store call, alongside the default
// compatible-posture tenant-wide baseline, on the SDK/HTTP/MCP single-user
// read surfaces. Block B (ae9) runs a real ingest -> buffer flush -> real LLM
// extraction -> real-embedding retrieve round trip (mirrors live_wave1_test.go)
// so the topic key a named view binds on is the REAL topic the live learner
// assigned (not scripted), then exercises a bound view's narrowing and an
// unbound-agent pass-through, on all three surfaces.
//
// Wiring note: ae8's ResolveOptions (posture) are baked into each surface at
// CONSTRUCTION time (internal/api/server.go's resolveOpts, and
// mcpserver.Services.ResolveOpts) — never a per-request argument — so Block
// A's strict and compatible scenarios each get their OWN boot.Stack (own
// sqlite file, own tenant). Block B reuses ONE shared boot.Stack +
// boot.Pipeline across HTTP/MCP/SDK(HTTP mode), the live_wave1 D-074 co-mount
// pattern, since ae9's view binding is a per-request/per-subject concern, not
// a construction-time one.
//
// GATED: skipped unless STOWAGE_LIVE=1 and OPENROUTER_API_KEY are set. NEVER
// runs in CI — it makes real, paid model calls.
//
// Run it:
//
//	set -a; source .env; set +a
//	STOWAGE_LIVE=1 go test ./test/integration/ -run TestLiveWave3 -v -timeout 15m
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/views"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// liveWave3Config returns the real bifrost/OpenRouter gateway wiring Wave 3
// boots from — identical model/provider choices to live_wave1_test.go's
// config builder — pointed at a caller-supplied sqlite DSN. The caller sets
// any scenario-specific fields (retrieval.read_posture,
// retrieval.agent_views.enabled) on the returned copy and calls Validate
// itself, since those knobs differ per Block-A/Block-B boot.
func liveWave3Config(dsn string) config.Config {
	cfg := *config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dsn
	cfg.Profile = "assistant"
	cfg.Gateway.Driver = "bifrost"
	cfg.Gateway.Provider = "openrouter"
	cfg.Gateway.BaseURL = "https://openrouter.ai/api"
	cfg.Gateway.RerankBaseURL = "https://openrouter.ai/api/v1"
	cfg.Gateway.APIKey = "env.OPENROUTER_API_KEY" //nolint:gosec // G101: env-var reference, not a credential (D-030)
	cfg.Gateway.Model = "openai/gpt-5.4-nano"
	cfg.Gateway.EmbedModel = "perplexity/pplx-embed-v1-0.6b"
	cfg.Gateway.EmbedDims = 1024
	cfg.Gateway.RerankModel = "cohere/rerank-4-fast"
	return cfg
}

// liveWave3HTTPAndSDK boots the HTTP API (from cfg, so ae8's resolveOpts —
// read_posture/multiplexing — is baked in exactly as api.New wires it) over
// stk's shared store/pipeline, returning a raw httpReq closure (mirrors
// live_wave1_test.go's inline helper) plus an SDK client in HTTP mode pointed
// at the SAME httptest listener — a distinct client code path over the
// identical server/store (the live_wave0/live_wave1 co-mount pattern).
// Factored out since Block A needs this twice (strict boot, compatible boot)
// and Block B once.
func liveWave3HTTPAndSDK(t *testing.T, cfg config.Config, stk *boot.Stack, p *boot.Pipeline, tenant string) (func(method, path string, body any) (int, []byte), stowage.Client) {
	t.Helper()
	httpSrv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	httpSrv.SetPipelineIn(p.In)
	httpSrv.SetStage(p.Stage)
	httpSrv.SetTopicService(stk.TopicSvc)
	httpSrv.SetRetriever(stk.Retriever)
	httpSrv.SetGrantsService(stk.GrantsSvc)
	ts := httptest.NewServer(httpSrv)
	t.Cleanup(ts.Close)

	agentKey, agentPlain, err := auth.Generate(tenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(agentKey); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	httpReq := func(method, path string, body any) (int, []byte) {
		t.Helper()
		var reader *strings.Reader
		if body != nil {
			b, merr := json.Marshal(body)
			if merr != nil {
				t.Fatalf("marshal %s %s: %v", method, path, merr)
			}
			reader = strings.NewReader(string(b))
		} else {
			reader = strings.NewReader("")
		}
		req, rerr := http.NewRequestWithContext(context.Background(), method, ts.URL+path, reader)
		if rerr != nil {
			t.Fatalf("new request %s %s: %v", method, path, rerr)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+agentPlain)
		resp, derr := ts.Client().Do(req)
		if derr != nil {
			t.Fatalf("%s %s: %v", method, path, derr)
		}
		defer func() { _ = resp.Body.Close() }()
		out, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			t.Fatalf("%s %s: read body: %v", method, path, rerr)
		}
		return resp.StatusCode, out
	}

	sdkClient := stowage.NewHTTP(ts.URL, agentPlain)
	return httpReq, sdkClient
}

// TestLiveWave3_ThreeSurface is the LIVE Wave-3 acceptance bar: ae8's
// effective-scope resolver + read posture and ae9's named topic views,
// exercised over the real gateway on the SDK, HTTP, and MCP single-user read
// surfaces. Block A (ae8) needs no real extraction (memories are seeded
// directly through the store, per the phase's own integration-test
// convention — effective_scope_test.go); Block B (ae9) runs a real
// ingest->extract round trip so its view binds on a REAL topic key.
func TestLiveWave3_ThreeSurface(t *testing.T) {
	if os.Getenv("STOWAGE_LIVE") == "" {
		t.Skip("live gateway test; set STOWAGE_LIVE=1 (needs OPENROUTER_API_KEY) to run")
	}
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set — export it (e.g. `set -a; source .env; set +a`) to run the live wave-3 validation")
	}

	t.Run("ae8_read_posture", testLiveWave3AE8)
	t.Run("ae9_topic_views", testLiveWave3AE9)
}

// testLiveWave3AE8 is Block A: strict posture isolates a resolved user and
// refuses an unresolved read, compatible posture stays tenant-wide — on
// SDK/HTTP/MCP, each surface's resolveOpts sourced from its own boot.
func testLiveWave3AE8(t *testing.T) {
	ctx := context.Background()
	const term = "ae8livestrictqzx"

	// ── STRICT boot: its own tenant + sqlite file, since posture is baked
	//    in at api.New/mcpserver.Services construction time. ──
	tenant := uniqueTenant("live-wave3-ae8-strict")
	cfgStrict := liveWave3Config(filepath.Join(t.TempDir(), "live-wave3-ae8-strict.db"))
	cfgStrict.Retrieval.ReadPosture = "strict"
	if err := cfgStrict.Validate(); err != nil {
		t.Fatalf("config validate (strict): %v", err)
	}

	stk, p := startStack(t, cfgStrict)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	u1Scope := identity.Scope{Tenant: tenant, User: "u1"}
	u2Scope := identity.Scope{Tenant: tenant, User: "u2"}
	u1ID := seedAgentFilterMemory(t, stk.Store, u1Scope, term+" u1 detail about database tooling preferences", nil)
	u2ID := seedAgentFilterMemory(t, stk.Store, u2Scope, term+" u2 detail about editor tooling preferences", nil)
	t.Logf("ae8 strict: seeded u1=%s u2=%s under tenant %s", u1ID, u2ID, tenant)

	httpReq, sdkClient := liveWave3HTTPAndSDK(t, cfgStrict, stk, p, tenant)
	mcpSvc := &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In, Log: stk.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenant), Profile: cfgStrict.Profile,
		// The whole point of this sub-block: strict read posture (D-148/D-137),
		// mirroring effective_scope_test.go's retrieveMCPStrict exactly.
		ResolveOpts: identity.ResolveOptions{Posture: identity.PostureStrict},
	}

	// ── Strict + resolved user (u1) isolates to u1's own rows (real
	//    embed/rerank against the real gateway), on all three surfaces. ──
	mcpOut, mcpRes, mcpFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: term, Limit: 10}, map[string]any{"user": "u1"})
	if mcpFail {
		t.Fatalf("MCP strict _meta.user=u1 retrieve unexpectedly failed")
	}
	mcpIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpOut.Items)})
	t.Logf("ae8 strict MCP _meta.user=u1 rendered:\n%s", resultText(t, mcpRes))
	if mcpOut.Degraded {
		t.Error("ae8 strict MCP: degraded=true unexpectedly on a clean strict+user read")
	}
	if !mcpIDs[u1ID] || mcpIDs[u2ID] {
		t.Errorf("ae8 strict MCP: _meta.user=u1 must isolate to u1 only, got %v (u1=%s u2=%s)", setKeys(mcpIDs), u1ID, u2ID)
	}

	status, body := httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": term, "limit": 10, "user_id": "u1"})
	if status >= 300 {
		t.Fatalf("HTTP strict user_id=u1 retrieve: status %d body %s", status, body)
	}
	var httpU1 stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpU1); err != nil {
		t.Fatalf("decode HTTP strict user_id=u1 retrieve: %v", err)
	}
	t.Logf("ae8 strict HTTP user_id=u1 rendered:\n%s", httpU1.Rendered)
	httpU1IDs := idSetOf(httpU1)
	if httpU1.Degraded {
		t.Error("ae8 strict HTTP: degraded=true unexpectedly on a clean strict+user read")
	}
	if !httpU1IDs[u1ID] || httpU1IDs[u2ID] {
		t.Errorf("ae8 strict HTTP: user_id=u1 must isolate to u1 only, got %v", setKeys(httpU1IDs))
	}

	sdkU1, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: term, Limit: 10, UserID: "u1"})
	if err != nil {
		t.Fatalf("SDK strict UserID=u1 retrieve unexpectedly failed: %v", err)
	}
	t.Logf("ae8 strict SDK UserID=u1 rendered:\n%s", sdkU1.Rendered)
	sdkU1IDs := idSetOf(sdkU1)
	if sdkU1.Degraded {
		t.Error("ae8 strict SDK: degraded=true unexpectedly on a clean strict+user read")
	}
	if !sdkU1IDs[u1ID] || sdkU1IDs[u2ID] {
		t.Errorf("ae8 strict SDK: UserID=u1 must isolate to u1 only, got %v", setKeys(sdkU1IDs))
	}
	t.Logf("ae8 PASS: strict+user isolation parity (MCP/HTTP/SDK) = %v", setKeys(mcpIDs))

	// ── Strict + NO user/agent is REFUSED (ErrIdentityRequired) before any
	//    store call, on all three surfaces — the ≥1 failure mode. ──
	_, _, mcpRefuseFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: "anything", Limit: 10}, nil)
	if !mcpRefuseFail {
		t.Error("ae8 strict MCP: a retrieve resolving to no user and no agent must be REFUSED, got success")
	}

	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": "anything", "limit": 10})
	if status != http.StatusForbidden {
		t.Errorf("ae8 strict HTTP: expected 403 (ErrIdentityRequired) on no-identity retrieve, got %d body %s", status, body)
	}

	if _, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: "anything", Limit: 10}); err == nil {
		t.Error("ae8 strict SDK: a retrieve resolving to no user and no agent must be REFUSED, got success")
	}
	t.Logf("ae8 PASS: strict refusal (ErrIdentityRequired) on MCP/HTTP/SDK")

	// ── COMPATIBLE boot (the default posture): its own tenant + sqlite
	//    file, so a no-identity read stays tenant-wide (byte-identical
	//    baseline) rather than sharing the strict boot's resolveOpts. ──
	tenantC := uniqueTenant("live-wave3-ae8-compat")
	cfgCompat := liveWave3Config(filepath.Join(t.TempDir(), "live-wave3-ae8-compat.db"))
	if err := cfgCompat.Validate(); err != nil {
		t.Fatalf("config validate (compatible): %v", err)
	}
	if cfgCompat.Retrieval.ReadPosture != "compatible" {
		t.Fatalf("liveWave3Config default read posture = %q, want compatible", cfgCompat.Retrieval.ReadPosture)
	}

	stkC, pC := startStack(t, cfgCompat)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = pC.Drain(shutCtx)
		_ = stkC.Close(shutCtx)
	})

	u1ScopeC := identity.Scope{Tenant: tenantC, User: "u1"}
	u2ScopeC := identity.Scope{Tenant: tenantC, User: "u2"}
	u1CID := seedAgentFilterMemory(t, stkC.Store, u1ScopeC, term+" u1 compat baseline detail", nil)
	u2CID := seedAgentFilterMemory(t, stkC.Store, u2ScopeC, term+" u2 compat baseline detail", nil)
	t.Logf("ae8 compatible: seeded u1=%s u2=%s under tenant %s", u1CID, u2CID, tenantC)

	httpReqC, sdkClientC := liveWave3HTTPAndSDK(t, cfgCompat, stkC, pC, tenantC)
	mcpSvcC := &mcpserver.Services{
		Store: stkC.Store, Retriever: stkC.Retriever, PipelineIn: pC.In, Log: stkC.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenantC), Profile: cfgCompat.Profile,
		// Zero value == PostureCompatible — the byte-identical default.
	}

	mcpCOut, mcpCRes, mcpCFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvcC,
		mcpserver.RetrieveInput{Query: term, Limit: 10}, nil)
	if mcpCFail {
		t.Fatalf("MCP compatible no-identity retrieve unexpectedly failed")
	}
	mcpCIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpCOut.Items)})
	t.Logf("ae8 compatible MCP no-identity rendered:\n%s", resultText(t, mcpCRes))
	if !mcpCIDs[u1CID] || !mcpCIDs[u2CID] {
		t.Errorf("ae8 compatible MCP: no-identity read must stay tenant-wide, got %v (want u1=%s u2=%s)", setKeys(mcpCIDs), u1CID, u2CID)
	}

	statusC, bodyC := httpReqC(http.MethodPost, "/v1/retrieve", map[string]any{"query": term, "limit": 10})
	if statusC >= 300 {
		t.Fatalf("HTTP compatible no-identity retrieve: status %d body %s", statusC, bodyC)
	}
	var httpC stowage.RetrieveResponse
	if err := json.Unmarshal(bodyC, &httpC); err != nil {
		t.Fatalf("decode HTTP compatible no-identity retrieve: %v", err)
	}
	httpCIDs := idSetOf(httpC)
	if !httpCIDs[u1CID] || !httpCIDs[u2CID] {
		t.Errorf("ae8 compatible HTTP: no-identity read must stay tenant-wide, got %v", setKeys(httpCIDs))
	}

	sdkC, err := sdkClientC.Retrieve(ctx, stowage.RetrieveRequest{Query: term, Limit: 10})
	if err != nil {
		t.Fatalf("SDK compatible no-identity retrieve unexpectedly failed: %v", err)
	}
	sdkCIDs := idSetOf(sdkC)
	if !sdkCIDs[u1CID] || !sdkCIDs[u2CID] {
		t.Errorf("ae8 compatible SDK: no-identity read must stay tenant-wide, got %v", setKeys(sdkCIDs))
	}
	t.Logf("ae8 PASS: compatible no-identity baseline stays tenant-wide (MCP/HTTP/SDK) = %v", setKeys(mcpCIDs))
}

// testLiveWave3AE9 is Block B: a real ingest->extract->retrieve round trip so
// a named per-agent topic view (ae9, D-149/D-151) binds on a REAL topic key
// the live learner assigned, then narrows a bound agent's results (with
// fresh-topic recomputation, live_wave1's snapshot-vs-retrieve race guard)
// while an unbound agent stays an unfiltered pass-through — on SDK, HTTP,
// and MCP, over ONE shared boot.Stack (D-074 co-mount).
func testLiveWave3AE9(t *testing.T) {
	ctx := context.Background()
	tenant := uniqueTenant("live-wave3-ae9")
	scope := identity.Scope{Tenant: tenant}

	cfg := liveWave3Config(filepath.Join(t.TempDir(), "live-wave3-ae9.db"))
	cfg.Retrieval.AgentViews.Enabled = true // ae9's master switch (shared with ae1, D-151) — off by default (D-034)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validate: %v", err)
	}

	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	installLiveTopics(t, stk.Store, scope)

	httpReq, sdkClient := liveWave3HTTPAndSDK(t, cfg, stk, p, tenant)
	mcpSvc := &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In, Log: stk.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenant), Profile: cfg.Profile,
	}

	// ── Real ingest spanning >=2 clearly different extraction magnets
	//    (mirrors live_wave1_test.go's split exactly) — real extraction
	//    assigns the topic key the view below binds on. ──
	const sessID = "live-wave3-ae9-sess"
	ingestRecords := []map[string]any{
		{"role": "user", "content": "My code editor of choice is Neovim, and I configure it with a heavy LSP setup for Go development.", "session_id": sessID, "buffer_key": sessID},
		{"role": "user", "content": "I always keep my terminal multiplexer as tmux with vim keybindings — that is just how I like to work.", "session_id": sessID, "buffer_key": sessID},
		{"role": "user", "content": "We decided to use PostgreSQL as the primary database for the billing service, mainly for its transactional guarantees.", "session_id": sessID, "buffer_key": sessID},
		{"role": "user", "content": "The team decided to migrate the analytics pipeline off MongoDB and onto ClickHouse for faster aggregate queries.", "session_id": sessID, "buffer_key": sessID},
	}
	status, body := httpReq(http.MethodPost, "/v1/records", map[string]any{"records": ingestRecords})
	if status >= 300 {
		t.Fatalf("POST /v1/records: status %d body %s", status, body)
	}
	t.Logf("ae9: ingested %d records (session=%s)", len(ingestRecords), sessID)

	time.Sleep(3 * time.Second)
	status, body = httpReq(http.MethodPost, "/v1/buffers/"+sessID+"/flush", map[string]any{"trigger": "explicit"})
	if status >= 300 {
		t.Fatalf("POST /v1/buffers/%s/flush: status %d body %s", sessID, status, body)
	}
	t.Logf("ae9: flushed buffer %s — waiting for the real learner LLM to extract + embed…", sessID)

	mems := waitForLiveMemories(t, ctx, stk.Store, scope, 2, 4*time.Minute)
	waitForExtractionDrained(t, ctx, stk.Store, 90*time.Second)
	if settled, _, lerr := stk.Store.Memories().ListByStatus(ctx, scope, "active", 200, ""); lerr == nil && len(settled) >= len(mems) {
		mems = settled
	}
	t.Logf("ae9: real extraction produced %d active memories (settled)", len(mems))
	memIDs := make([]string, len(mems))
	idToContent := make(map[string]string, len(mems))
	for i, m := range mems {
		memIDs[i] = m.ID
		idToContent[m.ID] = m.Content
		t.Logf("  memory[%d] id=%s kind=%s content=%q", i, m.ID, m.Kind, m.Content)
	}

	// ── Discover the REAL topic keys extraction assigned, and pick T: a
	//    topic that tags a STRICT SUBSET of the extracted memories. Skip
	//    (not fail) on model nondeterminism, exactly like live_wave1. ──
	topicsByMem, err := stk.Store.Memories().MemoriesTopics(ctx, scope, memIDs)
	if err != nil {
		t.Fatalf("MemoriesTopics: %v", err)
	}
	sortedIDs := append([]string(nil), memIDs...)
	sort.Strings(sortedIDs)
	for _, id := range sortedIDs {
		t.Logf("  topic tags: memory=%s topics=%v", id, topicsByMem[id])
	}

	counts := map[string]int{}
	for _, keys := range topicsByMem {
		seen := map[string]bool{}
		for _, k := range keys {
			if !seen[k] {
				counts[k]++
				seen[k] = true
			}
		}
	}
	var topicKeys []string
	for k := range counts {
		topicKeys = append(topicKeys, k)
	}
	sort.Strings(topicKeys)
	t.Logf("ae9: topics observed across %d memories: %+v", len(mems), counts)

	var topicKey string
	for _, k := range topicKeys {
		if counts[k] > 0 && counts[k] < len(mems) {
			topicKey = k
			break
		}
	}
	if topicKey == "" {
		t.Skipf("real extraction did not produce a usable topic split across %d memories (topics observed: %+v) — skipping ae9 (model nondeterminism), not a Stowage bug", len(mems), counts)
	}
	t.Logf("ae9: selected topic key %q as the view's allow-topic (tags %d/%d memories)", topicKey, counts[topicKey], len(mems))

	var repTaggedID, repUntaggedID string
	for _, id := range sortedIDs {
		has := false
		for _, k := range topicsByMem[id] {
			if k == topicKey {
				has = true
				break
			}
		}
		if has {
			if repTaggedID == "" {
				repTaggedID = id
			}
		} else if repUntaggedID == "" {
			repUntaggedID = id
		}
	}
	if repTaggedID == "" || repUntaggedID == "" {
		t.Fatalf("internal error selecting representative memories for topic %q: tagged=%q untagged=%q", topicKey, repTaggedID, repUntaggedID)
	}

	firstWords := func(s string, n int) []string {
		w := strings.Fields(s)
		if len(w) > n {
			w = w[:n]
		}
		return w
	}
	queryWords := append(firstWords(idToContent[repTaggedID], 5), firstWords(idToContent[repUntaggedID], 5)...)
	query := strings.Join(queryWords, " ")
	t.Logf("ae9 cross-topic retrieve query (derived from real extraction): %q", query)

	// ── Bind a named view on agent "ae9-live-agent" -> AllowTopics=[topicKey]
	//    through views.Service (the same admin core the HTTP/MCP admin
	//    surfaces use, D-149/D-151). ──
	const boundAgent = "ae9-live-agent"
	const unboundAgent = "ae9-live-agent-unbound"
	const viewName = "myview"
	viewsSvc := views.New(stk.Store.TopicViews(), stk.Store.Events(), stk.Log)
	if _, err := viewsSvc.CreateView(ctx, scope, store.TopicView{
		SubjectKind: "agent", SubjectID: boundAgent, ViewName: viewName, AllowTopics: []string{topicKey},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	const limit = 20

	// ── Unfiltered baseline (no agent, no view) on all three surfaces. ──
	mcpUnfilteredOut, _, mcpUnfilteredFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: limit, Profile: "precise"}, nil)
	if mcpUnfilteredFail {
		t.Fatalf("MCP unfiltered retrieve failed")
	}
	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": limit, "profile": "precise"})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (unfiltered): status %d body %s", status, body)
	}
	var httpUnfiltered stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpUnfiltered); err != nil {
		t.Fatalf("decode HTTP unfiltered retrieve: %v", err)
	}
	sdkUnfiltered, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: query, Limit: limit, Profile: "precise"})
	if err != nil {
		t.Fatalf("SDK Retrieve (unfiltered): %v", err)
	}

	// ── Bound view (view_name=myview, agent_id=boundAgent) on all three
	//    surfaces. ──
	mcpBoundOut, mcpBoundRes, mcpBoundFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: limit, Profile: "precise", ViewName: viewName},
		map[string]any{"agent_id": boundAgent})
	if mcpBoundFail {
		t.Fatalf("MCP bound-view retrieve failed")
	}
	t.Logf("ae9 MCP bound-view rendered:\n%s", resultText(t, mcpBoundRes))

	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{
		"query": query, "limit": limit, "profile": "precise", "agent_id": boundAgent, "view_name": viewName,
	})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (bound view): status %d body %s", status, body)
	}
	var httpBound stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpBound); err != nil {
		t.Fatalf("decode HTTP bound-view retrieve: %v", err)
	}
	t.Logf("ae9 HTTP bound-view rendered:\n%s", httpBound.Rendered)

	sdkBound, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{
		Query: query, Limit: limit, Profile: "precise", AgentID: boundAgent, ViewName: viewName,
	})
	if err != nil {
		t.Fatalf("SDK Retrieve (bound view): %v", err)
	}
	t.Logf("ae9 SDK bound-view rendered:\n%s", sdkBound.Rendered)

	// ── Unbound agent, SAME view_name — no matching view row, so results
	//    must be an unfiltered pass-through — on all three surfaces. ──
	mcpUnboundOut, _, mcpUnboundFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: limit, Profile: "precise", ViewName: viewName},
		map[string]any{"agent_id": unboundAgent})
	if mcpUnboundFail {
		t.Fatalf("MCP unbound-agent retrieve failed")
	}
	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{
		"query": query, "limit": limit, "profile": "precise", "agent_id": unboundAgent, "view_name": viewName,
	})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (unbound agent): status %d body %s", status, body)
	}
	var httpUnbound stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpUnbound); err != nil {
		t.Fatalf("decode HTTP unbound-agent retrieve: %v", err)
	}
	sdkUnbound, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{
		Query: query, Limit: limit, Profile: "precise", AgentID: unboundAgent, ViewName: viewName,
	})
	if err != nil {
		t.Fatalf("SDK Retrieve (unbound agent): %v", err)
	}

	mcpUnfilteredIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpUnfilteredOut.Items)})
	mcpBoundIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpBoundOut.Items)})
	mcpUnboundIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpUnboundOut.Items)})
	httpUnfilteredIDs := idSetOf(httpUnfiltered)
	httpBoundIDs := idSetOf(httpBound)
	httpUnboundIDs := idSetOf(httpUnbound)
	sdkUnfilteredIDs := idSetOf(sdkUnfiltered)
	sdkBoundIDs := idSetOf(sdkBound)
	sdkUnboundIDs := idSetOf(sdkUnbound)

	type surfaceResult struct {
		label          string
		unfilteredIDs  map[string]bool
		boundIDs       map[string]bool
		unboundIDs     map[string]bool
		boundDegraded  bool
		unboundDegrade bool
	}
	surfaces := []surfaceResult{
		{"mcp", mcpUnfilteredIDs, mcpBoundIDs, mcpUnboundIDs, mcpBoundOut.DegradedView, mcpUnboundOut.DegradedView},
		{"http", httpUnfilteredIDs, httpBoundIDs, httpUnboundIDs, httpBound.DegradedView, httpUnbound.DegradedView},
		{"sdk", sdkUnfilteredIDs, sdkBoundIDs, sdkUnboundIDs, sdkBound.DegradedView, sdkUnbound.DegradedView},
	}

	// Recompute the allow-topic membership from a FRESH MemoriesTopics over
	// every id any surface returned — the exact ground truth the retrieval
	// filter queried, immune to the snapshot-vs-retrieve race (live_wave1's
	// pattern, applied to ae9 instead of ae1).
	returnedIDs := map[string]bool{}
	for _, s := range surfaces {
		for id := range s.unfilteredIDs {
			returnedIDs[id] = true
		}
		for id := range s.boundIDs {
			returnedIDs[id] = true
		}
		for id := range s.unboundIDs {
			returnedIDs[id] = true
		}
	}
	returnedIDList := make([]string, 0, len(returnedIDs))
	for id := range returnedIDs {
		returnedIDList = append(returnedIDList, id)
	}
	freshTopics, err := stk.Store.Memories().MemoriesTopics(ctx, scope, returnedIDList)
	if err != nil {
		t.Fatalf("fresh MemoriesTopics for returned ids: %v", err)
	}
	freshTagged := map[string]bool{}
	for id, keys := range freshTopics {
		for _, k := range keys {
			if k == topicKey {
				freshTagged[id] = true
				break
			}
		}
	}

	for _, s := range surfaces {
		t.Logf("ae9 %s: unfiltered=%v bound=%v unbound=%v", s.label, setKeys(s.unfilteredIDs), setKeys(s.boundIDs), setKeys(s.unboundIDs))

		if s.boundDegraded {
			t.Errorf("%s: degraded_view unexpectedly true on a clean bound-view read", s.label)
		}
		if s.unboundDegrade {
			t.Errorf("%s: degraded_view unexpectedly true on an unbound-agent read", s.label)
		}

		// Every bound id must carry the allow-topic (narrowing, not leakage) —
		// validated against the fresh, retrieve-time topic membership.
		for id := range s.boundIDs {
			if !freshTagged[id] {
				t.Errorf("%s: bound-view retrieve returned a non-allow-topic memory %s (fresh topics=%v)", s.label, id, freshTopics[id])
			}
		}
		if len(s.boundIDs) == 0 {
			t.Errorf("%s: bound-view retrieve (topic=%q) returned no items", s.label, topicKey)
		}
		if len(s.boundIDs) >= len(s.unfilteredIDs) {
			t.Errorf("%s: bound view did not narrow — bound=%d unfiltered=%d", s.label, len(s.boundIDs), len(s.unfilteredIDs))
		}

		// An unbound agent (same view_name, no matching row) must leave
		// results unfiltered (pass-through, not degraded).
		if !setsEqual(s.unboundIDs, s.unfilteredIDs) {
			t.Errorf("%s: unbound agent must return the SAME set as unfiltered; unbound=%v unfiltered=%v", s.label, setKeys(s.unboundIDs), setKeys(s.unfilteredIDs))
		}
	}

	// Surface parity: the three surfaces' bound id-sets are IDENTICAL.
	if !setsEqual(mcpBoundIDs, httpBoundIDs) {
		t.Errorf("ae9 parity: MCP/HTTP bound id-sets diverge: mcp=%v http=%v", setKeys(mcpBoundIDs), setKeys(httpBoundIDs))
	}
	if !setsEqual(mcpBoundIDs, sdkBoundIDs) {
		t.Errorf("ae9 parity: MCP/SDK bound id-sets diverge: mcp=%v sdk=%v", setKeys(mcpBoundIDs), setKeys(sdkBoundIDs))
	}
	t.Logf("ae9 PASS: topic=%q bound id-set (parity across MCP/HTTP/SDK)=%v", topicKey, setKeys(mcpBoundIDs))
}
