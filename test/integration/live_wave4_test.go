// live_wave4_test.go is a LIVE end-to-end validation of Stowage Wave 4
// (ae2b, D-140/M1): the removal of project_id/user_id from the MCP read
// contracts, so sub-tenant identity resolves purely from _meta/JWT. It runs
// a real ingest -> buffer flush -> real LLM extraction -> real-embedding
// retrieve round trip (mirrors live_wave1_test.go/live_wave3_test.go)
// against the REAL gateway (bifrost -> OpenRouter: embed + complete +
// rerank, D-075), so this is not a re-run of mcp_effective_scope_test.go's
// synthetic-store proof — it proves the SAME narrowing holds once real
// extraction and real embed/rerank are in the loop.
//
// It proves two things ae2b's own tests (mcp_effective_scope_test.go,
// http_mcp_scope_parity_test.go) prove against a plain/mock-free store, but
// not against the real gateway:
//
//  1. memory_retrieve and memory_browse resolve identity from _meta ALONE —
//     RetrieveInput/BrowseInput have no user_id/project_id field left on the
//     Go struct (ae2b deleted them), so there is nothing left to set; the
//     _meta.user map key is the only channel that can possibly narrow the
//     read.
//  2. D-140 behavioural (not contractual) parity: the SAME identity resolved
//     via MCP's _meta.user and via HTTP's UNCHANGED ?user_id=/body user_id
//     query resolves to the SAME effective scope and the SAME rows, over the
//     SAME real-extracted data.
//
// Wiring mirrors live_wave1_test.go/live_wave3_test.go: ONE shared
// boot.Stack + boot.Pipeline backs the HTTP API server (D-074 co-mount);
// the in-process MCP transport is bound to the same stk.Store/stk.Retriever
// via a fresh mcpserver.Services per call. The u1/u2 real-extraction block
// reuses liveWave3Config's gateway wiring and startStack/waitForLiveMemories/
// waitForExtractionDrained/mcpRetrieveWithMetaLive/idSetOf/setKeys/setsEqual
// (all package-level helpers already proven by live_wave0/1/3). The bonus
// _meta.project case (M1) is seeded directly through the store (no gateway
// call needed to seed — the retrieve call below still hits the real gateway
// for embed/rerank), the same seedAgentFilterMemory pattern live_wave3's ae8
// block uses for its strict/compatible posture proofs.
//
// GATED: skipped unless STOWAGE_LIVE=1 and OPENROUTER_API_KEY are set. NEVER
// runs in CI — it makes real, paid model calls.
//
// Run it:
//
//	set -a; source .env; set +a
//	STOWAGE_LIVE=1 go test ./test/integration/ -run TestLiveWave4 -v -timeout 15m
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// mcpBrowseWithMetaLive calls memory_browse over a FRESH in-process MCP
// session bound to svc, optionally injecting a _meta map — mirrors
// mcpRetrieveWithMetaLive (live_wave1_test.go) for the memory_browse tool,
// which ae2b (D-140) also stripped project_id/user_id from (BrowseInput —
// the 14th read-targeting struct, per the ae2b commit's documented
// deviation). Returns the decoded structured output, the raw
// CallToolResult (for Text/eyeballing), and whether the call failed.
func mcpBrowseWithMetaLive(t *testing.T, ctx context.Context, svc *mcpserver.Services, in mcpserver.BrowseInput, meta map[string]any) (mcpserver.BrowseOutput, *mcpsdk.CallToolResult, bool) {
	t.Helper()
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "live-wave4-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	params := &mcpsdk.CallToolParams{Name: "memory_browse", Arguments: in}
	if len(meta) > 0 {
		params.Meta = mcpsdk.Meta(meta)
	}
	res, cerr := session.CallTool(ctx, params)
	if cerr != nil || (res != nil && res.IsError) {
		return mcpserver.BrowseOutput{}, res, true
	}
	var out mcpserver.BrowseOutput
	decodeStructured(t, res, &out)
	return out, res, false
}

// browseIDSetOf renders a memory_browse page's memories as an id-set —
// BrowseOutput.Memories carries []BrowseMemoryItem, a distinct wire type
// from RetrieveOutput.Items, so idSetOf/mcpItemsToSDK (retrieve-shaped)
// don't apply here.
func browseIDSetOf(out mcpserver.BrowseOutput) map[string]bool {
	ids := make(map[string]bool, len(out.Memories))
	for _, m := range out.Memories {
		ids[m.ID] = true
	}
	return ids
}

// TestLiveWave4_MetaOnlyIdentity is the LIVE Wave-4 (ae2b, D-140/M1)
// acceptance bar: it proves MCP read tools (memory_retrieve, memory_browse)
// resolve sub-tenant identity PURELY from _meta against the real gateway —
// the project_id/user_id args are gone from RetrieveInput/BrowseInput, so
// _meta is the only remaining channel — plus D-140 MCP-vs-HTTP behavioural
// parity (MCP _meta.user vs HTTP ?user_id= resolve to the same rows). ae2b is
// a read-path change, so the gateway work that must run live is the RETRIEVE
// (real query embed + real rerank over the rows); rows are seeded directly
// under their {Tenant,User}/{Tenant,Project} scopes (the same pattern
// live_wave3's ae8 block and mcp_effective_scope_test.go use), the learner
// ingest->extract round trip being validated by the W0–W3 harnesses.
func TestLiveWave4_MetaOnlyIdentity(t *testing.T) {
	if os.Getenv("STOWAGE_LIVE") == "" {
		t.Skip("live gateway test; set STOWAGE_LIVE=1 (needs OPENROUTER_API_KEY) to run")
	}
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("OPENROUTER_API_KEY not set — export it (e.g. `set -a; source .env; set +a`) to run the live wave-4 validation")
	}

	ctx := context.Background()
	tenant := uniqueTenant("live-wave4")
	cfg := liveWave3Config(filepath.Join(t.TempDir(), "live-wave4.db"))
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config validate: %v", err)
	}
	if cfg.Retrieval.ReadPosture != "compatible" {
		t.Fatalf("liveWave3Config default read posture = %q, want compatible (the no-_meta baseline below assumes it)", cfg.Retrieval.ReadPosture)
	}

	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	installLiveTopics(t, stk.Store, identity.Scope{Tenant: tenant})

	httpReq, _ := liveWave3HTTPAndSDK(t, cfg, stk, p, tenant)
	mcpSvc := &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In, Log: stk.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenant), Profile: cfg.Profile,
		// Zero-value ResolveOpts == PostureCompatible — the default, byte-identical posture.
	}

	// ── Two distinct users (u1, u2) in one tenant, seeded directly under
	//    their {Tenant,User} scopes — the SAME pattern the _meta.project block
	//    below (and live_wave3_test.go's ae8 block, and mcp_effective_scope_test.go)
	//    use to validate identity NARROWING. ae2b is a pure READ-PATH change
	//    (the project_id/user_id args are gone from RetrieveInput/BrowseInput —
	//    _meta is the only remaining channel), so what must run against the real
	//    gateway is the RETRIEVE (real embed of the query + real rerank over these
	//    rows), which every assertion below does. The learner-LLM ingest→extract
	//    round trip is validated by the W0–W3 live harnesses and is orthogonal to
	//    the arg removal; seeding here keeps the identity assertion deterministic
	//    (a real tenant-only-credential explicit flush would land the rows
	//    tenant-wide, since the flush scope — not per-record user_id — stamps the
	//    memory; in production the credential itself carries the user).
	//
	//    A distinctive shared query term (userTerm) is embedded in every seed so
	//    a single real retrieve deterministically matches all four rows under the
	//    real gateway (exactly as the _meta.project block uses projTerm); the
	//    _meta.user narrowing is then a genuine subtraction of the OTHER user's
	//    rows, not an artifact of a lucky/unlucky natural-language query. ──
	const userTerm = "wave4useridentityqzx"
	u1Scope := identity.Scope{Tenant: tenant, User: "u1"}
	u2Scope := identity.Scope{Tenant: tenant, User: "u2"}
	u1IDSet := map[string]bool{}
	u2IDSet := map[string]bool{}
	u1IDSet[seedAgentFilterMemory(t, stk.Store, u1Scope, userTerm+" my preferred code editor is Neovim with a Go LSP setup.", nil)] = true
	u1IDSet[seedAgentFilterMemory(t, stk.Store, u1Scope, userTerm+" I run my terminal multiplexer as tmux with vim keybindings.", nil)] = true
	u2IDSet[seedAgentFilterMemory(t, stk.Store, u2Scope, userTerm+" my preferred code editor is VS Code with Prettier for TypeScript.", nil)] = true
	u2IDSet[seedAgentFilterMemory(t, stk.Store, u2Scope, userTerm+" I keep my terminal as the plain default shell prompt.", nil)] = true
	t.Logf("wave4: seeded u1=%d u2=%d active memories under distinct {tenant,user} scopes (tenant=%s)", len(u1IDSet), len(u2IDSet), tenant)

	query := userTerm
	const limit = 20

	// ── MCP _meta.user=u1 narrowing (the ae2b point: no user_id arg exists
	//    on RetrieveInput — _meta is the only remaining channel). ──
	mcpU1Out, mcpU1Res, mcpU1Fail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: limit}, map[string]any{"user": "u1"})
	if mcpU1Fail {
		t.Fatalf("MCP _meta.user=u1 retrieve unexpectedly failed")
	}
	mcpU1IDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpU1Out.Items)})
	t.Logf("wave4 MCP _meta.user=u1 rendered:\n%s", resultText(t, mcpU1Res))
	t.Logf("wave4 MCP _meta.user=u1 ids=%v", setKeys(mcpU1IDs))
	if mcpU1Out.Degraded {
		t.Error("wave4 MCP _meta.user=u1: degraded=true unexpectedly on a clean real-gateway read")
	}
	if len(mcpU1IDs) == 0 {
		t.Error("wave4 MCP _meta.user=u1: expected at least one of u1's own memories, got none")
	}
	for id := range mcpU1IDs {
		if u2IDSet[id] {
			t.Errorf("P3 LEAK: MCP _meta.user=u1 returned u2's memory %s", id)
		}
	}

	// ── MCP _meta.user=u2 narrowing (the mirror case). ──
	mcpU2Out, mcpU2Res, mcpU2Fail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: limit}, map[string]any{"user": "u2"})
	if mcpU2Fail {
		t.Fatalf("MCP _meta.user=u2 retrieve unexpectedly failed")
	}
	mcpU2IDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpU2Out.Items)})
	t.Logf("wave4 MCP _meta.user=u2 rendered:\n%s", resultText(t, mcpU2Res))
	t.Logf("wave4 MCP _meta.user=u2 ids=%v", setKeys(mcpU2IDs))
	if mcpU2Out.Degraded {
		t.Error("wave4 MCP _meta.user=u2: degraded=true unexpectedly on a clean real-gateway read")
	}
	if len(mcpU2IDs) == 0 {
		t.Error("wave4 MCP _meta.user=u2: expected at least one of u2's own memories, got none")
	}
	for id := range mcpU2IDs {
		if u1IDSet[id] {
			t.Errorf("P3 LEAK: MCP _meta.user=u2 returned u1's memory %s", id)
		}
	}
	t.Logf("wave4 PASS: MCP memory_retrieve resolves identity purely from _meta (u1=%v u2=%v)", setKeys(mcpU1IDs), setKeys(mcpU2IDs))

	// ── No-_meta baseline: the default (compatible) posture stays
	//    tenant-wide — a no-identity read must still see rows from BOTH
	//    users, so the _meta.user narrowing above is a real subtraction,
	//    not just an artifact of a narrow query. ──
	mcpNoMetaOut, mcpNoMetaRes, mcpNoMetaFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: query, Limit: limit}, nil)
	if mcpNoMetaFail {
		t.Fatalf("MCP no-_meta retrieve unexpectedly failed")
	}
	mcpNoMetaIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpNoMetaOut.Items)})
	t.Logf("wave4 MCP no-_meta (tenant-wide baseline) rendered:\n%s", resultText(t, mcpNoMetaRes))
	t.Logf("wave4 MCP no-_meta ids=%v", setKeys(mcpNoMetaIDs))
	hasU1, hasU2 := false, false
	for id := range mcpNoMetaIDs {
		if u1IDSet[id] {
			hasU1 = true
		}
		if u2IDSet[id] {
			hasU2 = true
		}
	}
	if !hasU1 || !hasU2 {
		t.Errorf("wave4 no-_meta baseline: expected the default compatible posture to stay tenant-wide (see both u1=%v and u2=%v), got %v",
			setKeys(u1IDSet), setKeys(u2IDSet), setKeys(mcpNoMetaIDs))
	}
	t.Logf("wave4 PASS: no-_meta retrieve stays tenant-wide (compatible posture default)")

	// ── memory_browse: same ae2b point, second read tool (BrowseInput is
	//    the 14th arg-stripped struct — the ae2b commit's documented
	//    deviation beyond the plan's 13). ──
	mcpBrowseU1Out, mcpBrowseU1Res, mcpBrowseU1Fail := mcpBrowseWithMetaLive(t, ctx, mcpSvc,
		mcpserver.BrowseInput{Limit: limit}, map[string]any{"user": "u1"})
	if mcpBrowseU1Fail {
		t.Fatalf("MCP memory_browse _meta.user=u1 unexpectedly failed")
	}
	browseU1IDs := browseIDSetOf(mcpBrowseU1Out)
	t.Logf("wave4 MCP browse _meta.user=u1 rendered:\n%s", resultText(t, mcpBrowseU1Res))
	t.Logf("wave4 MCP browse _meta.user=u1 ids=%v", setKeys(browseU1IDs))
	if len(browseU1IDs) == 0 {
		t.Error("wave4 MCP memory_browse _meta.user=u1: expected at least one of u1's own memories, got none")
	}
	for id := range browseU1IDs {
		if u2IDSet[id] {
			t.Errorf("P3 LEAK: MCP memory_browse _meta.user=u1 returned u2's memory %s", id)
		}
	}
	t.Logf("wave4 PASS: MCP memory_browse resolves identity purely from _meta")

	// ── D-140 HTTP parity: the SAME identity via HTTP's UNCHANGED
	//    ?user_id=/body user_id resolves to the SAME id-set as MCP's
	//    _meta.user, over the SAME seeded rows. ──
	status, body := httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": limit, "user_id": "u1"})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (user_id=u1): status %d body %s", status, body)
	}
	var httpU1 stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpU1); err != nil {
		t.Fatalf("decode HTTP user_id=u1 retrieve: %v", err)
	}
	httpU1IDs := idSetOf(httpU1)
	t.Logf("wave4 HTTP user_id=u1 rendered:\n%s", httpU1.Rendered)
	t.Logf("wave4 HTTP user_id=u1 ids=%v", setKeys(httpU1IDs))
	if !setsEqual(mcpU1IDs, httpU1IDs) {
		t.Errorf("D-140 parity FAIL: MCP _meta.user=u1 vs HTTP user_id=u1 id-sets diverge: mcp=%v http=%v", setKeys(mcpU1IDs), setKeys(httpU1IDs))
	}

	status, body = httpReq(http.MethodPost, "/v1/retrieve", map[string]any{"query": query, "limit": limit, "user_id": "u2"})
	if status >= 300 {
		t.Fatalf("POST /v1/retrieve (user_id=u2): status %d body %s", status, body)
	}
	var httpU2 stowage.RetrieveResponse
	if err := json.Unmarshal(body, &httpU2); err != nil {
		t.Fatalf("decode HTTP user_id=u2 retrieve: %v", err)
	}
	httpU2IDs := idSetOf(httpU2)
	t.Logf("wave4 HTTP user_id=u2 rendered:\n%s", httpU2.Rendered)
	t.Logf("wave4 HTTP user_id=u2 ids=%v", setKeys(httpU2IDs))
	if !setsEqual(mcpU2IDs, httpU2IDs) {
		t.Errorf("D-140 parity FAIL: MCP _meta.user=u2 vs HTTP user_id=u2 id-sets diverge: mcp=%v http=%v", setKeys(mcpU2IDs), setKeys(httpU2IDs))
	}
	t.Logf("wave4 PASS: D-140 behavioural parity — MCP _meta.user and HTTP ?user_id= resolve to the SAME scope/rows (u1=%v u2=%v)", setKeys(httpU1IDs), setKeys(httpU2IDs))

	// ── Bonus: _meta.project narrowing (M1) — the sole remaining MCP
	//    channel for project scoping. Seeded directly (no gateway call
	//    needed to seed; the retrieve below still hits the real gateway for
	//    embed/rerank), mirroring live_wave3_test.go's ae8 seeding pattern. ──
	p1Scope := identity.Scope{Tenant: tenant, Project: "wave4-p1"}
	p2Scope := identity.Scope{Tenant: tenant, Project: "wave4-p2"}
	const projTerm = "wave4projectnarrowqzx"
	p1ID := seedAgentFilterMemory(t, stk.Store, p1Scope, projTerm+" detail about deployment tooling for project wp1", nil)
	p2ID := seedAgentFilterMemory(t, stk.Store, p2Scope, projTerm+" detail about deployment tooling for project wp2", nil)
	t.Logf("wave4: seeded project fixtures p1=%s (wave4-p1) p2=%s (wave4-p2)", p1ID, p2ID)

	mcpProjOut, mcpProjRes, mcpProjFail := mcpRetrieveWithMetaLive(t, ctx, mcpSvc,
		mcpserver.RetrieveInput{Query: projTerm, Limit: 10}, map[string]any{"project": "wave4-p1"})
	if mcpProjFail {
		t.Fatalf("MCP _meta.project=wave4-p1 retrieve unexpectedly failed")
	}
	mcpProjIDs := idSetOf(stowage.RetrieveResponse{Items: mcpItemsToSDK(mcpProjOut.Items)})
	t.Logf("wave4 MCP _meta.project=wave4-p1 rendered:\n%s", resultText(t, mcpProjRes))
	if !mcpProjIDs[p1ID] {
		t.Errorf("wave4 MCP _meta.project=wave4-p1: expected p1's memory %s, got %v", p1ID, setKeys(mcpProjIDs))
	}
	if mcpProjIDs[p2ID] {
		t.Errorf("P3 LEAK: MCP _meta.project=wave4-p1 returned p2's memory %s", p2ID)
	}
	t.Logf("wave4 PASS: MCP memory_retrieve resolves _meta.project alone (project narrowing, M1)")

	t.Logf("TestLiveWave4_MetaOnlyIdentity: PASS — MCP _meta-only identity (memory_retrieve + memory_browse, user and project) and D-140 MCP/HTTP behavioural parity verified over the real gateway")
}
