// live_wave1_test.go is a LIVE 3-surface end-to-end validation of Stowage
// Wave 1's read-time identity capabilities — ae1 the read-time agent->topic
// filter (D-135/D-146/D-151) and ae2 the additive _meta identity intake
// (D-137/D-138) — against the REAL gateway (bifrost -> OpenRouter: embed +
// complete + rerank, D-075). It runs a real ingest -> buffer flush -> real
// LLM extraction -> real-embedding retrieve round trip, so the topic keys
// ae1's agent policy curates on are the REAL topics the live learner
// assigned (not a scripted mock) — then exercises ae1 narrowing and ae2
// _meta intake on the three single-user surfaces: the SDK (HTTP mode), raw
// HTTP, and the in-process MCP transport.
//
// Wiring mirrors live_wave0_test.go: ONE shared boot.Stack + boot.Pipeline
// backs BOTH the HTTP API server and the in-process MCP server (D-074
// co-mount), with the SDK surface as stowage.NewHTTP pointed at the SAME
// httptest listener — a distinct client code path over the identical
// server/store. cfg.Retrieval.AgentViews.Enabled=true is the ae1 master
// switch (off by default, D-034/D-135); boot.Open wires
// s.Retriever.WithAgentPolicy(s.Store.TopicViews(), cfg.Retrieval.AgentViews.Enabled)
// unconditionally (internal/boot/boot.go), so setting the flag before
// startStack is sufficient to make the agent filter live for every surface
// that shares stk.Retriever (HTTP via SetRetriever, MCP via
// mcpserver.Services.Retriever).
//
// GATED: skipped unless STOWAGE_LIVE=1 and OPENROUTER_API_KEY are set. NEVER
// runs in CI — it makes real, paid model calls.
//
// Run it:
//
//	set -a; source .env; set +a
//	STOWAGE_LIVE=1 go test ./test/integration/ -run TestLiveWave1 -v -timeout 15m
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

// mcpRetrieveWithMetaLive calls memory_retrieve over a FRESH in-process MCP
// session bound to the already-shared live Services (co-mounted with the
// HTTP+SDK surfaces this test wires below — mirrors live_wave0_test.go's
// mcpBrowseIsFailureLive helper), optionally injecting a _meta map (the ae2
// D-137 identity seam, and the ae1 D-135 _meta.agent_id seam). Returns the
// decoded structured output, the raw CallToolResult (for Text/eyeballing),
// and whether the call failed (a protocol-level error or an IsError tool
// result) — the same "did not succeed" contract retrieveMCPIsFailure uses in
// meta_intake_test.go.
func mcpRetrieveWithMetaLive(t *testing.T, ctx context.Context, svc *mcpserver.Services, in mcpserver.RetrieveInput, meta map[string]any) (mcpserver.RetrieveOutput, *mcpsdk.CallToolResult, bool) {
	t.Helper()
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "live-wave1-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	params := &mcpsdk.CallToolParams{Name: "memory_retrieve", Arguments: in}
	if len(meta) > 0 {
		params.Meta = mcpsdk.Meta(meta)
	}
	res, cerr := session.CallTool(ctx, params)
	if cerr != nil || (res != nil && res.IsError) {
		return mcpserver.RetrieveOutput{}, res, true
	}
	var out mcpserver.RetrieveOutput
	decodeStructured(t, res, &out)
	return out, res, false
}

// setsEqual reports whether two id-sets carry exactly the same members.
func setsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// setKeys renders a map[string]bool id-set as a sorted slice, for stable
// t.Logf/t.Errorf output.
func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestLiveWave1_ThreeSurface is the LIVE Wave-1 acceptance bar: a real
// ingest->extract->retrieve round trip against the real gateway, with ae1
// (read-time agent->topic filter) and ae2 (_meta identity intake) asserted
// on the single-user surfaces (SDK, HTTP, MCP for ae1; MCP for ae2, per the
// _meta seam's MCP-only transport).
func TestLiveWave1_ThreeSurface(t *testing.T) {
	if os.Getenv("STOWAGE_LIVE") == "" {
		t.Skip("live gateway test; set STOWAGE_LIVE=1 (needs OPENROUTER_API_KEY) to run")
	}
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set — export it (e.g. `set -a; source .env; set +a`) to run the live wave-1 validation")
	}

	// ── Config: the real gateway (bifrost → OpenRouter), mirroring
	//    live_wave0_test.go's config builder exactly, PLUS the ae1 master
	//    switch — off by default (D-034), so this live run must opt in
	//    explicitly or the agent filter is inert. ──
	cfg := *config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = filepath.Join(t.TempDir(), "live-wave1.db")
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
	cfg.Retrieval.AgentViews.Enabled = true // ae1 master switch (D-135/D-146/D-151)

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validate: %v", err)
	}

	tenant := uniqueTenant("live-wave1")
	scope := identity.Scope{Tenant: tenant}
	ctx := context.Background()

	// ── ONE shared stack + pipeline behind BOTH the HTTP API and the
	//    in-process MCP transport (comount-style co-mount, D-074) — every
	//    surface reads/writes the SAME store + retrieval cache, and shares
	//    the SAME *retrieval.Retriever the agent-policy flag above wired. ──
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	installLiveTopics(t, stk.Store, scope) // preferences / decisions / gotchas (the same live extraction magnets live_wave0 uses)

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
	}

	// ── SDK surface: the public Go SDK in HTTP mode, pointed at the SAME
	//    httptest listener — a distinct client code path over the identical
	//    server/store. ──
	sdkClient := stowage.NewHTTP(ts.URL, agentPlain)

	// ── Ingest content spanning >=2 clearly different extraction magnets:
	//    editor/dev-tools statements (→ "preferences") and database/infra
	//    decisions (→ "decisions") — real extraction assigns the topics ae1
	//    filters on, so the split below is discovered, not assumed. ──
	const sessID = "live-wave1-sess"
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
	t.Logf("ingested %d records (session=%s)", len(ingestRecords), sessID)

	// Ingest enqueues to the buffer stage ASYNCHRONOUSLY; settle briefly
	// before the explicit flush so it doesn't race the buffer-append
	// (mirrors live_wave0_test.go / full-cycle-live.sh's sleep 3).
	time.Sleep(3 * time.Second)
	status, body = httpReq(http.MethodPost, "/v1/buffers/"+sessID+"/flush", map[string]any{"trigger": "explicit"})
	if status >= 300 {
		t.Fatalf("POST /v1/buffers/%s/flush: status %d body %s", sessID, status, body)
	}
	t.Logf("flushed buffer %s — waiting for the real learner LLM to extract + embed…", sessID)

	mems := waitForLiveMemories(t, ctx, stk.Store, scope, 2, 4*time.Minute)
	// The learner may still be committing/topic-tagging additional memories after
	// the >=2 threshold trips. Wait for the extraction pipeline to DRAIN (no
	// unprocessed records) so topic assignment is complete before we snapshot
	// topics — otherwise a topic that lands after the snapshot but before the
	// retrieve makes our snapshot disagree with the store the filter actually
	// queries (a harness race, not a filter bug). Then re-snapshot the settled set.
	waitForExtractionDrained(t, ctx, stk.Store, 90*time.Second)
	if settled, _, lerr := stk.Store.Memories().ListByStatus(ctx, scope, "active", 200, ""); lerr == nil && len(settled) >= len(mems) {
		mems = settled
	}
	t.Logf("real extraction produced %d active memories (settled)", len(mems))
	memIDs := make([]string, len(mems))
	idToContent := make(map[string]string, len(mems))
	for i, m := range mems {
		memIDs[i] = m.ID
		idToContent[m.ID] = m.Content
		t.Logf("  memory[%d] id=%s kind=%s content=%q", i, m.ID, m.Kind, m.Content)
	}

	// ── Discover the REAL topic keys extraction assigned, and pick T: a
	//    topic that tags a STRICT SUBSET of the extracted memories. If the
	//    live model didn't produce a usable split, skip rather than fail —
	//    this is model nondeterminism, not a Stowage bug. ──
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
	t.Logf("topics observed across %d memories: %+v", len(mems), counts)

	var topicKey string
	for _, k := range topicKeys {
		if counts[k] > 0 && counts[k] < len(mems) {
			topicKey = k
			break
		}
	}
	if topicKey == "" {
		t.Skipf("real extraction did not produce a usable topic split across %d memories (topics observed: %+v) — skipping ae1/ae2 (model nondeterminism), not a Stowage bug", len(mems), counts)
	}
	t.Logf("ae1: selected topic key %q as the agent allow-topic (tags %d/%d memories)", topicKey, counts[topicKey], len(mems))

	tTagged := map[string]bool{}
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
			tTagged[id] = true
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

	// Build a retrieve query that lexically crosses BOTH groups — derived
	// from the REAL extracted content, not scripted — so the unfiltered
	// baseline actually spans topics and the agent filter has something to
	// subtract.
	firstWords := func(s string, n int) []string {
		w := strings.Fields(s)
		if len(w) > n {
			w = w[:n]
		}
		return w
	}
	queryWords := append(firstWords(idToContent[repTaggedID], 5), firstWords(idToContent[repUntaggedID], 5)...)
	query := strings.Join(queryWords, " ")
	t.Logf("ae1 cross-topic retrieve query (derived from real extraction): %q", query)

	// ── ae1: bind agent "ag-live" -> AllowTopics=[topicKey] directly
	//    through the store (the same seam the HTTP admin surface uses). ──
	if err := stk.Store.TopicViews().PutAgentPolicy(ctx, scope, store.AgentPolicy{
		AgentID: "ag-live", AllowTopics: []string{topicKey},
	}); err != nil {
		t.Fatalf("PutAgentPolicy: %v", err)
	}

	const agentLimit = 20
	// MCP: unfiltered (no _meta), bound "ag-live", unbound "ag-none".
	mcpUnfilteredOut, _, mcpUnfilteredFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: agentLimit, Profile: "precise"}, nil)
	if mcpUnfilteredFail {
		t.Fatalf("MCP unfiltered retrieve failed")
	}
	mcpFilteredOut, mcpFilteredRes, mcpFilteredFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: agentLimit, Profile: "precise"}, map[string]any{"agent_id": "ag-live"})
	if mcpFilteredFail {
		t.Fatalf("MCP agent-filtered retrieve failed")
	}
	mcpUnboundOut, _, mcpUnboundFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: agentLimit, Profile: "precise"}, map[string]any{"agent_id": "ag-none"})
	if mcpUnboundFail {
		t.Fatalf("MCP unbound-agent retrieve failed")
	}
	t.Logf("MCP agent-filtered rendered:\n%s", resultText(t, mcpFilteredRes))

	// HTTP: unfiltered, bound "ag-live" (agent_id field), unbound "ag-none".
	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": agentLimit, "profile": "precise"})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (unfiltered): status %d body %s", status, body)
	}
	var httpUnfiltered stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpUnfiltered); err != nil {
		t.Fatalf("decode HTTP unfiltered retrieve: %v", err)
	}

	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": agentLimit, "profile": "precise", "agent_id": "ag-live"})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (agent-filtered): status %d body %s", status, body)
	}
	var httpFiltered stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpFiltered); err != nil {
		t.Fatalf("decode HTTP agent-filtered retrieve: %v", err)
	}
	t.Logf("HTTP agent-filtered rendered:\n%s", httpFiltered.Rendered)

	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": agentLimit, "profile": "precise", "agent_id": "ag-none"})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (unbound agent): status %d body %s", status, body)
	}
	var httpUnbound stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpUnbound); err != nil {
		t.Fatalf("decode HTTP unbound-agent retrieve: %v", err)
	}

	// SDK: unfiltered, bound "ag-live" (AgentID field), unbound "ag-none".
	sdkUnfiltered, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: query, Limit: agentLimit, Profile: "precise"})
	if err != nil {
		t.Fatalf("SDK Retrieve (unfiltered): %v", err)
	}
	sdkFiltered, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: query, Limit: agentLimit, Profile: "precise", AgentID: "ag-live"})
	if err != nil {
		t.Fatalf("SDK Retrieve (agent-filtered): %v", err)
	}
	t.Logf("SDK agent-filtered rendered:\n%s", sdkFiltered.Rendered)
	sdkUnbound, err := sdkClient.Retrieve(ctx, stowage.RetrieveRequest{Query: query, Limit: agentLimit, Profile: "precise", AgentID: "ag-none"})
	if err != nil {
		t.Fatalf("SDK Retrieve (unbound agent): %v", err)
	}

	mcpUnfilteredIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpUnfilteredOut.Items)})
	mcpFilteredIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpFilteredOut.Items)})
	mcpUnboundIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpUnboundOut.Items)})
	httpUnfilteredIDs := idSetOf(httpUnfiltered)
	httpFilteredIDs := idSetOf(httpFiltered)
	httpUnboundIDs := idSetOf(httpUnbound)
	sdkUnfilteredIDs := idSetOf(sdkUnfiltered)
	sdkFilteredIDs := idSetOf(sdkFiltered)
	sdkUnboundIDs := idSetOf(sdkUnbound)

	type surfaceResult struct {
		label               string
		unfilteredIDs       map[string]bool
		filteredIDs         map[string]bool
		unboundIDs          map[string]bool
		degradedAgentFilter bool
	}
	surfaces := []surfaceResult{
		{"mcp", mcpUnfilteredIDs, mcpFilteredIDs, mcpUnboundIDs, mcpFilteredOut.DegradedAgentFilter},
		{"http", httpUnfilteredIDs, httpFilteredIDs, httpUnboundIDs, httpFiltered.DegradedAgentFilter},
		{"sdk", sdkUnfilteredIDs, sdkFilteredIDs, sdkUnboundIDs, sdkFiltered.DegradedAgentFilter},
	}
	// Recompute the allow-topic membership from a FRESH MemoriesTopics over every
	// id any surface returned — this is the exact ground truth the retrieval filter
	// queried, so the invariant "every filtered id is tagged T" is checked against
	// the same store state (immune to the snapshot-vs-retrieve race that an early
	// tTagged snapshot would suffer). tTagged (the pre-retrieve snapshot) still
	// drives topicKey selection above; freshTagged authoritatively validates the filter.
	returnedIDs := map[string]bool{}
	for _, s := range []surfaceResult{
		{"mcp", mcpUnfilteredIDs, mcpFilteredIDs, mcpUnboundIDs, false},
		{"http", httpUnfilteredIDs, httpFilteredIDs, httpUnboundIDs, false},
		{"sdk", sdkUnfilteredIDs, sdkFilteredIDs, sdkUnboundIDs, false},
	} {
		for id := range s.unfilteredIDs {
			returnedIDs[id] = true
		}
		for id := range s.filteredIDs {
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
		t.Logf("ae1 %s: unfiltered=%v filtered=%v unbound=%v", s.label, setKeys(s.unfilteredIDs), setKeys(s.filteredIDs), setKeys(s.unboundIDs))

		// Fail-open must NOT be falsely triggered on the happy path.
		if s.degradedAgentFilter {
			t.Errorf("%s: degraded_agent_filter unexpectedly true on a clean bound-agent read", s.label)
		}

		// Every filtered id must carry the allow-topic (narrowing, not leakage) —
		// validated against the fresh, retrieve-time topic membership.
		for id := range s.filteredIDs {
			if !freshTagged[id] {
				t.Errorf("%s: agent-filtered retrieve returned a non-allow-topic memory %s (fresh topics=%v)", s.label, id, freshTopics[id])
			}
		}

		// The filter must actually subtract: filtered strictly narrower than unfiltered.
		if len(s.filteredIDs) == 0 {
			t.Errorf("%s: agent-filtered retrieve (topic=%q) returned no items", s.label, topicKey)
		}
		if len(s.filteredIDs) >= len(s.unfilteredIDs) {
			t.Errorf("%s: agent filter did not narrow — filtered=%d unfiltered=%d", s.label, len(s.filteredIDs), len(s.unfilteredIDs))
		}

		// An unbound agent ("ag-none") must leave results unfiltered.
		if !setsEqual(s.unboundIDs, s.unfilteredIDs) {
			t.Errorf("%s: unbound agent (ag-none) must return the SAME set as unfiltered; unbound=%v unfiltered=%v", s.label, setKeys(s.unboundIDs), setKeys(s.unfilteredIDs))
		}
	}

	// Surface parity: the three surfaces' filtered id-sets are IDENTICAL.
	if !setsEqual(mcpFilteredIDs, httpFilteredIDs) {
		t.Errorf("ae1 parity: MCP/HTTP filtered id-sets diverge: mcp=%v http=%v", setKeys(mcpFilteredIDs), setKeys(httpFilteredIDs))
	}
	if !setsEqual(mcpFilteredIDs, sdkFilteredIDs) {
		t.Errorf("ae1 parity: MCP/SDK filtered id-sets diverge: mcp=%v sdk=%v", setKeys(mcpFilteredIDs), setKeys(sdkFilteredIDs))
	}
	t.Logf("ae1 PASS: topic=%q filtered id-set (parity across MCP/HTTP/SDK)=%v", topicKey, setKeys(mcpFilteredIDs))

	// ── ae2: _meta identity intake, MCP surface (the _meta seam is
	//    MCP-only — HTTP/SDK source the same dimension via a first-class
	//    user_id/AgentID request field instead, D-140). Two users seeded
	//    directly through the shared store (no gateway call needed to seed;
	//    the retrieve calls below still hit the real gateway for
	//    embed/rerank). ──
	u1Scope := identity.Scope{Tenant: tenant, User: "u1"}
	u2Scope := identity.Scope{Tenant: tenant, User: "u2"}
	const metaTerm = "metaintakeliveqzx"
	u1ID := seedAgentFilterMemory(t, stk.Store, u1Scope, metaTerm+" u1 detail about database tooling preferences", nil)
	u2ID := seedAgentFilterMemory(t, stk.Store, u2Scope, metaTerm+" u2 detail about editor tooling preferences", nil)
	t.Logf("ae2: seeded u1=%s u2=%s under tenant %s", u1ID, u2ID, tenant)

	u1Out, u1Res, u1Fail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: metaTerm, Limit: 10}, map[string]any{"user": "u1"})
	if u1Fail {
		t.Fatalf("MCP _meta.user=u1 retrieve failed")
	}
	u1IDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(u1Out.Items)})
	t.Logf("ae2 _meta.user=u1 rendered:\n%s", resultText(t, u1Res))
	t.Logf("ae2 _meta.user=u1 ids=%v", setKeys(u1IDs))
	if !u1IDs[u1ID] {
		t.Errorf("ae2: _meta.user=u1 must return u1's memory %s, got %v", u1ID, setKeys(u1IDs))
	}
	if u1IDs[u2ID] {
		t.Errorf("ae2: _meta.user=u1 must NOT return u2's memory %s, got %v", u2ID, setKeys(u1IDs))
	}

	noMetaOut, _, noMetaFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: metaTerm, Limit: 10}, nil)
	if noMetaFail {
		t.Fatalf("MCP no-_meta retrieve failed")
	}
	noMetaIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(noMetaOut.Items)})
	t.Logf("ae2 no-_meta (tenant-wide) ids=%v", setKeys(noMetaIDs))
	if !noMetaIDs[u1ID] || !noMetaIDs[u2ID] {
		t.Errorf("ae2: a no-_meta retrieve must be tenant-wide (both users' rows), got %v", setKeys(noMetaIDs))
	}
	if len(noMetaIDs) <= len(u1IDs) {
		t.Errorf("ae2: the no-_meta retrieve must be BROADER than the _meta.user=u1 retrieve: no_meta=%d u1=%d", len(noMetaIDs), len(u1IDs))
	}

	_, _, mismatchFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: metaTerm, Limit: 10}, map[string]any{"tenant": "attacker-tenant-" + tenant})
	if !mismatchFail {
		t.Errorf("ae2: expected a _meta.tenant mismatch to fail closed (D-138), got success")
	}
	t.Logf("ae2 PASS: _meta.user narrows, no-_meta stays tenant-wide, _meta.tenant mismatch fails closed")

	t.Logf("TestLiveWave1_ThreeSurface: PASS — ae1 agent narrowing (SDK/HTTP/MCP, parity) and ae2 _meta intake (MCP) verified over the real gateway")
}

// waitForExtractionDrained polls until the extraction pipeline has drained (no
// unprocessed records), so topic assignment is complete before the harness
// snapshots topic membership. Records are marked processed_at once their memories
// (and topic tags) are committed, so an empty unprocessed set is a sound
// "topics assigned" signal. Best-effort: on timeout it logs and returns (the
// fresh-topics-at-assertion recomputation is the authoritative guard regardless).
func waitForExtractionDrained(t *testing.T, ctx context.Context, st store.Store, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		recs, err := st.Records().ListUnprocessed(ctx, time.Now().UnixMilli(), 1)
		if err == nil && len(recs) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Logf("waitForExtractionDrained: still %d unprocessed after %s (proceeding; assertion uses fresh topics)", len(recs), timeout)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waitForExtractionDrained: context done: %v", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}
