// live_wave0_test.go is a LIVE 3-surface end-to-end validation of Stowage
// Wave 0's read capabilities — ae4a lean read + drilldown, ae5 browse, ae6
// topic filter — against the REAL gateway (bifrost → OpenRouter: embed +
// complete + rerank, D-075). It runs a real ingest → buffer flush → real
// LLM extraction → real-embedding retrieve round trip, then exercises every
// Wave-0 read capability on all three single-user surfaces: the SDK (HTTP
// mode), raw HTTP, and the in-process MCP transport.
//
// Wiring: ONE shared boot.Stack + boot.Pipeline backs BOTH the HTTP API
// server and the in-process MCP server (mirrors comount_test.go's co-mount,
// D-074), so every surface reads/writes the SAME store and the SAME
// retrieval cache. The SDK surface is stowage.NewHTTP pointed at the same
// httptest listener the raw-HTTP assertions use — a distinct client code
// path (the public Go SDK) over the identical server/store, avoiding a
// second sqlite handle on the same DSN (NewEmbedded opens its own stack;
// causal_parity_test.go/playbook_parity_test.go show that pattern is safe
// only when each leg runs SEQUENTIALLY against a shared DSN — this test
// instead needs all three surfaces reading the SAME live-extracted data
// concurrently within one run, so SDK-over-HTTP is the correct choice here).
//
// GATED: skipped unless STOWAGE_LIVE=1 and OPENROUTER_API_KEY are set. NEVER
// runs in CI — it makes real, paid model calls.
//
// Run it:
//
//	set -a; source .env; set +a
//	STOWAGE_LIVE=1 go test ./test/integration/ -run TestLiveWave0 -v -timeout 15m
package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// liveNormalize replaces every [cite:<ULID>] handle in a rendered body with a
// fixed placeholder so bodies minted by independent Retrieve calls (each
// mints its own citation nonce) compare byte-identical (mirrors
// TestRetrieveLeanRead_SurfaceParity's normalize, generalized to N items).
func liveNormalize(body string) string {
	return citeHandleRe.ReplaceAllString(body, "[cite:<CITE>]")
}

// installLiveTopics upserts three realistic extraction magnets — the same
// keys scripts/acceptance/full-cycle-live.sh uses — directly via the store
// (bypassing the pipeline/HTTP, the established fixture pattern in this
// package: installTopic, seedBrowseMemories, seedLeanReadMemory).
func installLiveTopics(t *testing.T, st store.Store, scope identity.Scope) {
	t.Helper()
	now := time.Now().UnixMilli()
	topics := []store.Topic{
		{ID: "live-topic-preferences", TenantID: scope.Tenant, Key: "preferences",
			Description: "the user's stated preferences, tools, and working style", Status: "active", CreatedAt: now, UpdatedAt: now},
		{ID: "live-topic-decisions", TenantID: scope.Tenant, Key: "decisions",
			Description: "technical decisions the user or team has made and why", Status: "active", CreatedAt: now, UpdatedAt: now},
		{ID: "live-topic-gotchas", TenantID: scope.Tenant, Key: "gotchas",
			Description: "pitfalls, bugs, and lessons learned to avoid repeating", Status: "active", CreatedAt: now, UpdatedAt: now},
	}
	for _, tp := range topics {
		if err := st.Topics().Upsert(context.Background(), scope, tp); err != nil {
			t.Fatalf("install live topic %s: %v", tp.Key, err)
		}
	}
}

// waitForLiveMemories polls the store until at least minCount ACTIVE
// memories exist in scope, or the deadline passes — the real-extraction
// settle barrier (mirrors eval/harness/server.go's WaitForMemories, adapted
// for this package: no TestServer, direct store access over the shared
// stack). The real learner LLM runs asynchronously off the flush, so the
// timeout is generous (a few minutes, not the ~25s pollRetrieve budget the
// mock-gateway parity tests use).
func waitForLiveMemories(t *testing.T, ctx context.Context, st store.Store, scope identity.Scope, minCount int, timeout time.Duration) []store.Memory {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []store.Memory
	for {
		mems, _, err := st.Memories().ListByStatus(ctx, scope, "active", 200, "")
		if err == nil {
			last = mems
			if len(mems) >= minCount {
				return mems
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for >=%d active memories (real extraction): have %d (last err=%v)", minCount, len(last), err)
		}
		t.Logf("waitForLiveMemories: %d/%d active memories so far, polling…", len(last), minCount)
		select {
		case <-ctx.Done():
			t.Fatalf("waitForLiveMemories: context done: %v", ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}

// TestLiveWave0_ThreeSurface is the LIVE Wave-0 acceptance bar: a real
// ingest→extract→retrieve round trip against the real gateway, with the
// ae4a (lean read + drill), ae5 (browse), and ae6 (topic filter) read
// capabilities asserted on all three single-user surfaces.
func TestLiveWave0_ThreeSurface(t *testing.T) {
	if os.Getenv("STOWAGE_LIVE") == "" {
		t.Skip("live gateway test; set STOWAGE_LIVE=1 (needs OPENROUTER_API_KEY) to run")
	}
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set — export it (e.g. `set -a; source .env; set +a`) to run the live wave-0 validation")
	}

	// ── Config: the real gateway (bifrost → OpenRouter), mirroring
	//    eval/harness/server.go's full-mode branch + scripts/acceptance/
	//    full-cycle-live.sh's defaults. The API key is an env-var REFERENCE
	//    (D-030) — resolved at boot from OPENROUTER_API_KEY, never inlined. ──
	cfg := *config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = filepath.Join(t.TempDir(), "live.db")
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

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validate: %v", err)
	}

	tenant := uniqueTenant("live-wave0")
	scope := identity.Scope{Tenant: tenant}
	ctx := context.Background()

	// ── ONE shared stack + pipeline behind BOTH the HTTP API and the
	//    in-process MCP transport (comount-style co-mount, D-074) — every
	//    surface reads/writes the SAME store + retrieval cache. ──
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	installLiveTopics(t, stk.Store, scope)

	// ── HTTP surface ──
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
		req, rerr := http.NewRequestWithContext(ctx, method, ts.URL+path, reader)
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

	// ── MCP surface (in-process transport, StdioScopeFn — no per-request
	//    auth over stdio, D-020) over the SAME shared stack. ──
	mcpSvc := &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc, GrantsSvc: stk.GrantsSvc,
		PipelineIn: p.In, PipelineStage: p.Stage, Log: stk.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenant), Profile: cfg.Profile,
		BrowseDefaultLimit: cfg.Retrieval.BrowseDefaultLimit,
	}
	mcpCallTool := leanReadMCPClient(t, ctx, mcpSvc)

	// ── SDK surface: the public Go SDK in HTTP mode, pointed at the SAME
	//    httptest listener — a distinct client code path over the identical
	//    server/store (see file doc for why NOT NewEmbedded here). ──
	sdkClient := stowage.NewHTTP(ts.URL, agentPlain)

	// ── Ingest a small, realistic conversation (the proven-live content from
	//    scripts/acceptance/full-cycle-live.sh) via the raw HTTP surface, then
	//    settle + explicit-flush so the real learner LLM runs deterministically
	//    (not gated on the count/age triggers). ──
	const sessID = "live-wave0-sess"
	ingestRecords := []map[string]any{
		{"role": "user", "content": "My name is Dana. My code editor of choice is Neovim and I use it for everything.", "session_id": sessID, "buffer_key": sessID},
		{"role": "user", "content": "We decided to use PostgreSQL as the primary database for the billing service, mainly for its transactional guarantees.", "session_id": sessID, "buffer_key": sessID},
		{"role": "user", "content": "Gotcha: the Kafka consumer silently drops messages if you forget to commit offsets after a rebalance.", "session_id": sessID, "buffer_key": sessID},
	}
	status, body := httpReq(http.MethodPost, "/v1/records", map[string]any{"records": ingestRecords})
	if status >= 300 {
		t.Fatalf("POST /v1/records: status %d body %s", status, body)
	}
	t.Logf("ingested %d records (session=%s)", len(ingestRecords), sessID)

	// Ingest enqueues to the buffer stage ASYNCHRONOUSLY (records_handler.go:
	// non-blocking channel send → stage goroutine writes buffer_items); settle
	// briefly before the explicit flush so it doesn't race the buffer-append
	// (mirrors full-cycle-live.sh's sleep 3 before the flush call).
	time.Sleep(3 * time.Second)
	status, body = httpReq(http.MethodPost, "/v1/buffers/"+sessID+"/flush", map[string]any{"trigger": "explicit"})
	if status >= 300 {
		t.Fatalf("POST /v1/buffers/%s/flush: status %d body %s", sessID, status, body)
	}
	t.Logf("flushed buffer %s — waiting for the real learner LLM to extract + embed…", sessID)

	mems := waitForLiveMemories(t, ctx, stk.Store, scope, 1, 4*time.Minute)
	t.Logf("real extraction produced %d active memories", len(mems))
	memIDs := make([]string, len(mems))
	for i, m := range mems {
		memIDs[i] = m.ID
		t.Logf("  memory[%d] id=%s kind=%s content=%q", i, m.ID, m.Kind, m.Content)
	}

	// Build a retrieve query guaranteed to lexically hit the first extracted
	// memory (decoupled from exactly which of the 3 statements the live LLM
	// chose to extract/word).
	queryWords := strings.Fields(mems[0].Content)
	if len(queryWords) > 8 {
		queryWords = queryWords[:8]
	}
	query := strings.Join(queryWords, " ")
	t.Logf("retrieve query (derived from the first extracted memory): %q", query)

	// ── ae4a: lean MCP read + surface parity (D-142) ─────────────────────────
	// Profile=precise so the real cross-encoder rerank pass actually runs
	// (D-075) — real embeddings + rerank exercised on every surface.
	var mcpRetOut mcpserver.RetrieveOutput
	mcpRes := mcpCallTool("memory_retrieve", mcpserver.RetrieveInput{Query: query, Limit: 5, Profile: "precise"}, &mcpRetOut)
	mcpText := resultText(t, mcpRes)
	t.Logf("MCP memory_retrieve Text:\n%s", mcpText)

	if mcpText == "" {
		t.Fatal("MCP memory_retrieve Text is empty")
	}
	if strings.Contains(mcpText, "Retrieved") && strings.Contains(mcpText, "item(s)") {
		t.Errorf("MCP Text looks like the OLD placeholder body, not the ae4a lean markdown render: %q", mcpText)
	}
	if !strings.Contains(mcpText, "[cite:") {
		t.Errorf("MCP Text missing a [cite:...] drill handle: %q", mcpText)
	}
	if len(mcpRetOut.Items) == 0 {
		t.Fatalf("MCP memory_retrieve returned no items for query %q", query)
	}
	t.Logf("MCP retrieve: degraded=%v degraded_rerank=%v items=%d", mcpRetOut.Degraded, mcpRetOut.DegradedRerank, len(mcpRetOut.Items))

	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": 5, "profile": "precise"})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve: status %d body %s", status, body)
	}
	var httpRet stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpRet); err != nil {
		t.Fatalf("decode HTTP retrieve response: %v (body=%s)", err, body)
	}
	t.Logf("HTTP /v1/retrieve rendered:\n%s", httpRet.Rendered)
	if httpRet.Rendered == "" {
		t.Fatal("HTTP retrieveResponse.rendered is empty")
	}
	if len(httpRet.Items) == 0 {
		t.Fatalf("HTTP retrieve returned no items for query %q", query)
	}
	t.Logf("HTTP retrieve: degraded=%v degraded_rerank=%v items=%d", httpRet.Degraded, httpRet.DegradedRerank, len(httpRet.Items))

	sdkRet, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: query, Limit: 5, Profile: "precise"})
	if err != nil {
		t.Fatalf("SDK Retrieve: %v", err)
	}
	t.Logf("SDK Retrieve.Rendered:\n%s", sdkRet.Rendered)
	if sdkRet.Rendered == "" {
		t.Fatal("SDK RetrieveResponse.Rendered is empty")
	}
	if len(sdkRet.Items) == 0 {
		t.Fatalf("SDK retrieve returned no items for query %q", query)
	}
	t.Logf("SDK retrieve: degraded=%v degraded_rerank=%v items=%d", sdkRet.Degraded, sdkRet.DegradedRerank, len(sdkRet.Items))

	// Surface parity: MCP Text, HTTP rendered, SDK Rendered are byte-identical
	// modulo the per-response citation nonce (normalized away).
	mcpNorm := liveNormalize(mcpText)
	httpNorm := liveNormalize(httpRet.Rendered)
	sdkNorm := liveNormalize(sdkRet.Rendered)
	if mcpNorm != httpNorm {
		t.Errorf("MCP/HTTP normalized rendered bodies diverge:\n mcp:  %q\n http: %q", mcpNorm, httpNorm)
	}
	if mcpNorm != sdkNorm {
		t.Errorf("MCP/SDK normalized rendered bodies diverge:\n mcp: %q\n sdk: %q", mcpNorm, sdkNorm)
	}

	// Episode hook (conditional — the assistant profile's episode detect/
	// narrate sweeps run on a 15-minute interval, RFC §6b/D-079, so a
	// same-run episode is not guaranteed; assert the hook ONLY when a backing
	// memory record actually carries an EpisodeID).
	for _, m := range mems {
		if m.EpisodeID != "" {
			hook := "[episode:" + m.EpisodeID + "]"
			if !strings.Contains(mcpNorm, hook) {
				t.Errorf("memory %s has EpisodeID %q but rendered body is missing the hook %q", m.ID, m.EpisodeID, hook)
			}
		}
	}

	// ── ae4a: drill-down round trip (MCP + HTTP + SDK) ───────────────────────
	// Give the async injection writer (P2: Retrieve never blocks on this
	// write) a moment to durably persist the citation row before drilling.
	time.Sleep(1 * time.Second)

	mcpCite := citeHandleRe.FindStringSubmatch(mcpText)
	if mcpCite == nil {
		t.Fatalf("no [cite:...] handle found in MCP Text: %q", mcpText)
	}
	var mcpDD mcpserver.DrilldownOutput
	mcpCallTool("memory_drilldown", mcpserver.DrilldownInput{Citation: mcpCite[1]}, &mcpDD)
	if mcpDD.MemoryID == "" || len(mcpDD.Spans) == 0 {
		t.Errorf("MCP drilldown did not resolve citation %q: %+v", mcpCite[1], mcpDD)
	} else {
		t.Logf("MCP drilldown: citation %s -> memory %s (%d spans)", mcpCite[1], mcpDD.MemoryID, len(mcpDD.Spans))
	}

	httpCite := citeHandleRe.FindStringSubmatch(httpRet.Rendered)
	if httpCite == nil {
		t.Fatalf("no [cite:...] handle found in HTTP rendered body: %q", httpRet.Rendered)
	}
	status, body = httpReq(http.MethodPost, "/v1/drilldown", map[string]any{"citation": httpCite[1]})
	if status >= 300 {
		t.Fatalf("POST /v1/drilldown: status %d body %s", status, body)
	}
	var httpDD stowage.DrilldownResponse
	if err := json.Unmarshal(body, &httpDD); err != nil {
		t.Fatalf("decode HTTP drilldown response: %v", err)
	}
	if httpDD.MemoryID == "" || len(httpDD.Spans) == 0 {
		t.Errorf("HTTP drilldown did not resolve citation %q: %+v", httpCite[1], httpDD)
	} else {
		t.Logf("HTTP drilldown: citation %s -> memory %s (%d spans)", httpCite[1], httpDD.MemoryID, len(httpDD.Spans))
	}

	sdkCite := citeHandleRe.FindStringSubmatch(sdkRet.Rendered)
	if sdkCite == nil {
		t.Fatalf("no [cite:...] handle found in SDK rendered body: %q", sdkRet.Rendered)
	}
	sdkDD, err := sdkClient.Drilldown(ctx, stowage.DrilldownRequest{Citation: sdkCite[1]})
	if err != nil {
		t.Fatalf("SDK Drilldown: %v", err)
	}
	if sdkDD.MemoryID == "" || len(sdkDD.Spans) == 0 {
		t.Errorf("SDK drilldown did not resolve citation %q: %+v", sdkCite[1], sdkDD)
	} else {
		t.Logf("SDK drilldown: citation %s -> memory %s (%d spans)", sdkCite[1], sdkDD.MemoryID, len(sdkDD.Spans))
	}

	// ── ae5: browse (D-143) ───────────────────────────────────────────────────
	sdkBrowse, err := sdkClient.Browse(ctx, stowage.BrowseRequest{Mode: "recent", Limit: 50})
	if err != nil {
		t.Fatalf("SDK Browse(recent): %v", err)
	}
	status, body = httpReq(http.MethodGet, "/v1/memories?mode=recent&limit=50", nil)
	if status >= 300 {
		t.Fatalf("GET /v1/memories?mode=recent: status %d body %s", status, body)
	}
	var httpBrowse stowage.BrowseResponse
	if err := json.Unmarshal(body, &httpBrowse); err != nil {
		t.Fatalf("decode HTTP browse response: %v", err)
	}
	var mcpBrowseOut mcpserver.BrowseOutput
	mcpCallTool("memory_browse", mcpserver.BrowseInput{Mode: "recent", Limit: 50}, &mcpBrowseOut)

	sdkBrowseIDs := idsOf(sdkBrowse)
	httpBrowseIDs := idsOf(httpBrowse)
	mcpBrowseIDs := make([]string, len(mcpBrowseOut.Memories))
	for i, m := range mcpBrowseOut.Memories {
		mcpBrowseIDs[i] = m.ID
	}
	t.Logf("browse(recent) ids: sdk=%v http=%v mcp=%v", sdkBrowseIDs, httpBrowseIDs, mcpBrowseIDs)

	if len(sdkBrowseIDs) == 0 {
		t.Fatal("browse(recent) returned no memories on the SDK surface")
	}
	if !stringSlicesEqual(sdkBrowseIDs, httpBrowseIDs) {
		t.Errorf("browse(recent) id order diverges SDK vs HTTP:\n sdk=%v\nhttp=%v", sdkBrowseIDs, httpBrowseIDs)
	}
	if !stringSlicesEqual(sdkBrowseIDs, mcpBrowseIDs) {
		t.Errorf("browse(recent) id order diverges SDK vs MCP:\n sdk=%v\n mcp=%v", sdkBrowseIDs, mcpBrowseIDs)
	}
	// Newest-first: the head of the sweep must be the most recently created memory.
	newest := sdkBrowse.Memories[0]
	for _, m := range sdkBrowse.Memories {
		if m.CreatedAt > newest.CreatedAt {
			t.Errorf("browse(recent) not newest-first: %s (created_at=%d) precedes a later memory (created_at=%d)", newest.ID, newest.CreatedAt, m.CreatedAt)
		}
	}

	// mode=superseded must not error on any surface (an empty result is fine —
	// this run does not necessarily supersede anything).
	if _, err := sdkClient.Browse(ctx, stowage.BrowseRequest{Mode: "superseded", Limit: 10}); err != nil {
		t.Errorf("SDK Browse(superseded): unexpected error: %v", err)
	}
	if status, body := httpReq(http.MethodGet, "/v1/memories?mode=superseded&limit=10", nil); status >= 300 {
		t.Errorf("GET /v1/memories?mode=superseded: status %d body %s", status, body)
	}
	var mcpSupersededOut mcpserver.BrowseOutput
	mcpCallTool("memory_browse", mcpserver.BrowseInput{Mode: "superseded", Limit: 10}, &mcpSupersededOut) // fatals on error/IsError — "must not error" is the assertion

	// An unknown mode is rejected on every surface — never silently defaulted.
	if _, err := sdkClient.Browse(ctx, stowage.BrowseRequest{Mode: "not-a-real-mode"}); err == nil {
		t.Error("SDK Browse: expected an error for an unknown mode")
	}
	if status, _ := httpReq(http.MethodGet, "/v1/memories?mode=not-a-real-mode", nil); status < 400 {
		t.Errorf("GET /v1/memories?mode=not-a-real-mode: expected 4xx, got %d", status)
	}
	if !mcpBrowseIsFailureLive(t, ctx, mcpSvc, mcpserver.BrowseInput{Mode: "not-a-real-mode"}) {
		t.Error("MCP memory_browse: expected a failure for an unknown mode")
	}

	// ── ae6: topic filter (D-144) ─────────────────────────────────────────────
	topicsByMem, err := stk.Store.Memories().MemoriesTopics(ctx, scope, memIDs)
	if err != nil {
		t.Fatalf("MemoriesTopics: %v", err)
	}
	var topicKey string
	for id, keys := range topicsByMem {
		if len(keys) > 0 {
			topicKey = keys[0]
			t.Logf("discovered real topic tag: memory=%s topic=%q (all: %v)", id, topicKey, keys)
			break
		}
	}
	if topicKey == "" {
		t.Fatalf("no ingested memory carries a topic tag (topicsByMem=%+v) — cannot exercise ae6 include_topics", topicsByMem)
	}

	unfilteredCount := len(httpRet.Items) // reuse the ae4a unfiltered HTTP retrieve above

	var mcpFiltered mcpserver.RetrieveOutput
	mcpCallTool("memory_retrieve", mcpserver.RetrieveInput{Query: query, Limit: 5, Profile: "precise", IncludeTopics: []string{topicKey}}, &mcpFiltered)
	if len(mcpFiltered.Items) == 0 {
		t.Errorf("MCP retrieve with include_topics=%q returned no items", topicKey)
	}
	if len(mcpFiltered.Items) > unfilteredCount {
		t.Errorf("MCP topic-filtered retrieve (%d items) is not narrower than the unfiltered retrieve (%d items)", len(mcpFiltered.Items), unfilteredCount)
	}
	if mcpFiltered.DegradedTopicFilter {
		t.Errorf("MCP retrieve: degraded_topic_filter unexpectedly true (topic store should be healthy)")
	}

	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": 5, "profile": "precise", "include_topics": []string{topicKey}})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (include_topics): status %d body %s", status, body)
	}
	var httpFiltered stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpFiltered); err != nil {
		t.Fatalf("decode HTTP topic-filtered retrieve: %v", err)
	}
	if len(httpFiltered.Items) == 0 {
		t.Errorf("HTTP retrieve with include_topics=%q returned no items", topicKey)
	}
	if len(httpFiltered.Items) > unfilteredCount {
		t.Errorf("HTTP topic-filtered retrieve (%d items) is not narrower than the unfiltered retrieve (%d items)", len(httpFiltered.Items), unfilteredCount)
	}
	if httpFiltered.DegradedTopicFilter {
		t.Errorf("HTTP retrieve: degraded_topic_filter unexpectedly true")
	}

	sdkFiltered, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: query, Limit: 5, Profile: "precise", IncludeTopics: []string{topicKey}})
	if err != nil {
		t.Fatalf("SDK Retrieve (include_topics): %v", err)
	}
	if len(sdkFiltered.Items) == 0 {
		t.Errorf("SDK retrieve with include_topics=%q returned no items", topicKey)
	}
	if len(sdkFiltered.Items) > unfilteredCount {
		t.Errorf("SDK topic-filtered retrieve (%d items) is not narrower than the unfiltered retrieve (%d items)", len(sdkFiltered.Items), unfilteredCount)
	}
	if sdkFiltered.DegradedTopicFilter {
		t.Errorf("SDK retrieve: degraded_topic_filter unexpectedly true")
	}

	t.Logf("ae6 topic filter (%q): unfiltered=%d mcp_filtered=%d http_filtered=%d sdk_filtered=%d",
		topicKey, unfilteredCount, len(mcpFiltered.Items), len(httpFiltered.Items), len(sdkFiltered.Items))

	t.Logf("TestLiveWave0_ThreeSurface: PASS — ae4a lean read/drill, ae5 browse, ae6 topic filter all verified on SDK, HTTP, and MCP over the real gateway")
}

// mcpBrowseIsFailureLive mirrors browse_test.go's browseMCPIsFailure, but
// against an already-shared *mcpserver.Services (the live test's co-mounted
// stack) instead of standing up a fresh stack — a fresh MCP server bound to
// the same Services still exercises a real, independent in-process
// transport, and reports whether the call failed (protocol-level error or an
// IsError tool result) rather than fataling on it.
func mcpBrowseIsFailureLive(t *testing.T, ctx context.Context, svc *mcpserver.Services, in mcpserver.BrowseInput) bool {
	t.Helper()
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "live-wave0-browse-failure", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	res, cerr := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_browse", Arguments: in})
	return cerr != nil || (res != nil && res.IsError)
}
