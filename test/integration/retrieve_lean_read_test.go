// retrieve_lean_read_test.go proves the ae4a lean-MCP-read seam (D-142) end to
// end over real drivers: the memory_retrieve Text/rendered/Rendered body's
// [cite:<ULID>] drill handle round-trips through the UNCHANGED
// memory_drilldown citation path (H1: no new store method), an episode-bearing
// memory surfaces its [episode:<id>] hook, a cross-scope drill fails (the
// failure mode), and the three single-user read surfaces (MCP, HTTP, SDK)
// render byte-identical bodies over the same underlying data (D-067/D-073,
// modulo the necessarily-unique-per-response citation nonce). Runs under
// -race. Postgres subtests are gated on STOWAGE_TEST_PG_DSN, the established
// pattern from internal/store/pgstore/pgstore_test.go — sqlite always runs.
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
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
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"

	_ "github.com/hurtener/stowage/internal/store/pgstore" // register the postgres driver (env-gated below)
)

// citeHandleRe extracts the [cite:<handle>] drill handle from a rendered body.
var citeHandleRe = regexp.MustCompile(`\[cite:([^\]]+)\]`)

// leanReadDrivers is the set of drivers TestRetrieveLeanRead_* runs against:
// sqlite always, postgres subtest present but SKIPped (t.Skip, visible in
// output) when STOWAGE_TEST_PG_DSN is unset — the same env-gated pattern
// pgstore_test.go uses, never invented fresh here.
func leanReadDrivers() []string {
	return []string{"sqlite", "postgres"}
}

// leanReadConfig returns a baseConfig wired to driver; the postgres subtest
// SKIPs (rather than fails) when the env var is unset.
func leanReadConfig(t *testing.T, driver string) config.Config {
	t.Helper()
	cfg := baseConfig(t)
	if driver == "postgres" {
		dsn := os.Getenv("STOWAGE_TEST_PG_DSN")
		if dsn == "" {
			t.Skip("STOWAGE_TEST_PG_DSN not set — skipping postgres lean-read test")
		}
		cfg.Store.Driver = "postgres"
		cfg.Store.DSN = dsn
	}
	return cfg
}

// uniqueTenant returns a per-run-unique tenant so repeated runs against a
// persistent postgres instance never collide (no truncation needed — Stowage
// isolates by tenant scope, P3).
func uniqueTenant(prefix string) string {
	return prefix + "-" + strings.ToLower(ulid.Make().String())
}

// seedLeanReadMemory commits one memory + its provenance-linked record
// directly (bypassing the pipeline) so it is lexically retrievable (FTS
// trigger on Commit) and its drilldown spans are deterministic. Returns the
// backing record's ID.
func seedLeanReadMemory(t *testing.T, st store.Store, scope identity.Scope, memID, content, episodeID string) string {
	t.Helper()
	return seedLeanReadMemoryAt(t, st, scope, memID, content, episodeID, time.Now().UnixMilli())
}

// seedLeanReadMemoryAt is seedLeanReadMemory with an explicit timestamp
// (ValidFrom/CreatedAt/UpdatedAt/OccurredAt), so a test that seeds the same
// logical memory across several surfaces can pin ONE timestamp for all of
// them. The render date suffix ("| When: YYYY-MM-DD") is daily-granularity,
// so separate time.Now() calls across seeds can straddle a UTC-midnight
// boundary and flake a parity assertion (TestRetrieveLeanRead_SurfaceParity) —
// callers that need byte-identical renders across seeds must share a
// timestamp.
func seedLeanReadMemoryAt(t *testing.T, st store.Store, scope identity.Scope, memID, content, episodeID string, now int64) string {
	t.Helper()
	ctx := context.Background()
	recID := ulid.Make().String()
	if err := st.Records().Append(ctx, scope, []store.Record{{
		ID: recID, TenantID: scope.Tenant, Role: "user", Content: content,
		OccurredAt: now, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("append record: %v", err)
	}
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, TenantID: scope.Tenant, Kind: "fact", Content: content,
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: ulid.Make().String(), EpisodeID: episodeID,
			ValidFrom: now, CreatedAt: now, UpdatedAt: now,
		},
		Provenance: []store.Provenance{{
			ID: ulid.Make().String(), MemoryID: memID, RecordID: recID,
			SpanStart: 0, SpanEnd: len(content), TenantID: scope.Tenant, CreatedAt: now,
		}},
		Events: []store.Event{{ID: ulid.Make().String(), Type: "memory.added", SubjectID: memID, Payload: "{}", CreatedAt: now}},
		Scope:  scope,
	}
	if err := st.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("commit memory: %v", err)
	}
	return recID
}

// leanReadMCPClient spins up the mcpserver in-process for svc and returns a
// connected session plus a callTool helper that decodes StructuredContent and
// exposes the raw *mcpsdk.CallToolResult (so callers can read Text too).
func leanReadMCPClient(t *testing.T, ctx context.Context, svc *mcpserver.Services) func(name string, args any, out any) *mcpsdk.CallToolResult {
	t.Helper()
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "lean-read-test", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return func(name string, args any, out any) *mcpsdk.CallToolResult {
		res, cerr := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if cerr != nil {
			t.Fatalf("CallTool %s: %v", name, cerr)
		}
		if res.IsError {
			t.Fatalf("CallTool %s returned IsError: %+v", name, res.Content)
		}
		if out != nil {
			b, _ := json.Marshal(res.StructuredContent)
			if uerr := json.Unmarshal(b, out); uerr != nil {
				t.Fatalf("decode %s result: %v", name, uerr)
			}
		}
		return res
	}
}

// resultText extracts the model-facing Text from an MCP CallToolResult.
func resultText(t *testing.T, res *mcpsdk.CallToolResult) string {
	t.Helper()
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatal("CallToolResult carries no TextContent")
	return ""
}

// TestRetrieveLeanRead_CitationDrillRoundTrip is AC3: the [cite:<ULID>] drill
// handle in the rendered Text equals the item's existing citation ULID, and
// feeding it to memory_drilldown resolves via the UNCHANGED
// Injections().Get → inj.MemoryID path to the correct verbatim provenance
// spans — zero new store code (H1).
func TestRetrieveLeanRead_CitationDrillRoundTrip(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("lean-cite-" + driver)
			scope := identity.Scope{Tenant: tenant}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stk, p := startStack(t, cfg)
			t.Cleanup(func() {
				shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
				defer c()
				_ = p.Drain(shutCtx)
				_ = stk.Close(shutCtx)
			})

			content := "The disaster-recovery runbook lives in the ops wiki under Runbooks."
			memID := ulid.Make().String()
			recID := seedLeanReadMemory(t, stk.Store, scope, memID, content, "")

			svc := &mcpserver.Services{
				Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In,
				Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(tenant),
			}
			callTool := leanReadMCPClient(t, ctx, svc)

			var retOut mcpserver.RetrieveOutput
			res := callTool("memory_retrieve", mcpserver.RetrieveInput{Query: "disaster recovery runbook ops wiki", Limit: 5}, &retOut)
			text := resultText(t, res)

			m := citeHandleRe.FindStringSubmatch(text)
			if m == nil {
				t.Fatalf("no [cite:...] handle found in rendered Text: %q", text)
			}
			citation := m[1]

			var wantCitation string
			for _, it := range retOut.Items {
				if it.ID == memID {
					wantCitation = it.Citation
				}
			}
			if wantCitation == "" {
				t.Fatalf("seeded memory %s not retrieved: %+v", memID, retOut.Items)
			}
			if citation != wantCitation {
				t.Errorf("drill handle %q != Structured citation %q", citation, wantCitation)
			}

			// Force-drain the async injection writer so the citation row is durable
			// before drilldown resolves it (P2: Retrieve never blocks on this write).
			stk.Retriever.Close()

			var dd mcpserver.DrilldownOutput
			callTool("memory_drilldown", mcpserver.DrilldownInput{Citation: citation}, &dd)
			if dd.MemoryID != memID {
				t.Errorf("drilldown MemoryID = %q, want %q", dd.MemoryID, memID)
			}
			if len(dd.Spans) == 0 {
				t.Fatal("expected at least one provenance span")
			}
			if dd.Spans[0].RecordID != recID {
				t.Errorf("drilldown span RecordID = %q, want %q", dd.Spans[0].RecordID, recID)
			}
		})
	}
}

// TestRetrieveLeanRead_EpisodeHook is AC2: an episode-bearing memory surfaces
// its [episode:<id>] hook in the rendered body, with zero new store calls (the
// hook is a field read on the already-loaded Memory.EpisodeID).
func TestRetrieveLeanRead_EpisodeHook(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("lean-ep-" + driver)
			scope := identity.Scope{Tenant: tenant}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stk, p := startStack(t, cfg)
			t.Cleanup(func() {
				shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
				defer c()
				_ = p.Drain(shutCtx)
				_ = stk.Close(shutCtx)
			})

			content := "The Q3 launch retro identified three blockers in the deploy pipeline."
			memID := ulid.Make().String()
			seedLeanReadMemory(t, stk.Store, scope, memID, content, "ep-q3-launch-retro")

			svc := &mcpserver.Services{
				Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In,
				Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(tenant),
			}
			callTool := leanReadMCPClient(t, ctx, svc)

			res := callTool("memory_retrieve", mcpserver.RetrieveInput{Query: "Q3 launch retro deploy pipeline blockers", Limit: 5}, nil)
			text := resultText(t, res)

			if !strings.Contains(text, "[episode:ep-q3-launch-retro]") {
				t.Errorf("rendered body missing episode hook: %q", text)
			}
		})
	}
}

// TestRetrieveLeanRead_ScopeFailure is the required failure mode: a citation
// minted in tenant A cannot be drilled down by tenant B (P3 store-layer scope
// isolation) — the drill fails rather than leaking cross-scope.
func TestRetrieveLeanRead_ScopeFailure(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenantA := uniqueTenant("lean-scope-a-" + driver)
			tenantB := uniqueTenant("lean-scope-b-" + driver)
			scopeA := identity.Scope{Tenant: tenantA}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stk, p := startStack(t, cfg)
			t.Cleanup(func() {
				shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
				defer c()
				_ = p.Drain(shutCtx)
				_ = stk.Close(shutCtx)
			})

			content := "Tenant-A-only secret: the rotation key lives in vault path secret/rotation."
			memID := ulid.Make().String()
			seedLeanReadMemory(t, stk.Store, scopeA, memID, content, "")

			svcA := &mcpserver.Services{
				Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In,
				Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(tenantA),
			}
			callToolA := leanReadMCPClient(t, ctx, svcA)
			res := callToolA("memory_retrieve", mcpserver.RetrieveInput{Query: "tenant-A-only secret rotation key vault", Limit: 5}, nil)
			text := resultText(t, res)
			m := citeHandleRe.FindStringSubmatch(text)
			if m == nil {
				t.Fatalf("no [cite:...] handle found in rendered Text: %q", text)
			}
			citation := m[1]
			stk.Retriever.Close() // drain so the row is durable before the cross-scope attempt

			// Tenant B attempts to drill the SAME citation via a separate MCP server
			// bound to a different tenant scope.
			svcB := &mcpserver.Services{
				Store: stk.Store, PipelineIn: p.In,
				Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(tenantB),
			}
			srvB, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svcB)
			if err != nil {
				t.Fatalf("mcpserver.New (tenant B): %v", err)
			}
			clientTB := srvB.ServeInMemory(ctx)
			clientB := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "lean-read-scope-test", Version: "0.0.0"}, nil)
			sessionB, err := clientB.Connect(ctx, clientTB, nil)
			if err != nil {
				t.Fatalf("mcp connect (tenant B): %v", err)
			}
			defer func() { _ = sessionB.Close() }()

			resB, cerr := sessionB.CallTool(ctx, &mcpsdk.CallToolParams{
				Name: "memory_drilldown", Arguments: mcpserver.DrilldownInput{Citation: citation},
			})
			// The scope-isolation failure surfaces either as a protocol-level error
			// or as an IsError tool result — both mean "did not resolve", which is
			// the required behavior (P3: no unscoped read leaks across tenants).
			if cerr == nil && (resB == nil || !resB.IsError) {
				t.Fatalf("expected tenant B drilldown to fail (cross-scope citation), got success: %+v", resB)
			}
		})
	}
}

// TestRetrieveLeanRead_SurfaceParity is AC5: MCP Text, HTTP rendered, and SDK
// Rendered carry the SAME structural body over identical underlying content
// (D-067/D-073) — normalized for the one field that is legitimately unique per
// response (the citation nonce). Runs once against sqlite (parity is about
// surface wiring, not driver behavior).
func TestRetrieveLeanRead_SurfaceParity(t *testing.T) {
	const content = "The on-call escalation policy pages the secondary after fifteen minutes."
	const episodeID = "ep-oncall-policy"
	const query = "on-call escalation policy secondary fifteen minutes"
	// ONE shared timestamp for all three seeds below: the render date suffix is
	// daily-granularity, so three independent time.Now() calls could straddle a
	// UTC-midnight boundary and produce three different "| When:" dates,
	// flaking this parity assertion (wave-0 fix).
	seedAt := time.Now().UnixMilli()

	normalize := func(body, citation string) string {
		return strings.ReplaceAll(body, citation, "<CITE>")
	}

	// ── MCP ──
	mcpNorm := func() string {
		cfg := baseConfig(t)
		tenant := uniqueTenant("lean-parity-mcp")
		scope := identity.Scope{Tenant: tenant}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		stk, p := startStack(t, cfg)
		t.Cleanup(func() {
			shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
			defer c()
			_ = p.Drain(shutCtx)
			_ = stk.Close(shutCtx)
		})
		seedLeanReadMemoryAt(t, stk.Store, scope, ulid.Make().String(), content, episodeID, seedAt)

		svc := &mcpserver.Services{
			Store: stk.Store, Retriever: stk.Retriever, PipelineIn: p.In,
			Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(tenant),
		}
		callTool := leanReadMCPClient(t, ctx, svc)
		res := callTool("memory_retrieve", mcpserver.RetrieveInput{Query: query, Limit: 5}, nil)
		text := resultText(t, res)
		m := citeHandleRe.FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("mcp: no [cite:...] handle found: %q", text)
		}
		return normalize(text, m[1])
	}()

	// ── HTTP ──
	httpNorm := func() string {
		cfg := baseConfig(t)
		tenant := uniqueTenant("lean-parity-http")
		scope := identity.Scope{Tenant: tenant}

		stk, p := startStack(t, cfg)
		t.Cleanup(func() {
			shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
			defer c()
			_ = p.Drain(shutCtx)
			_ = stk.Close(shutCtx)
		})
		seedLeanReadMemoryAt(t, stk.Store, scope, ulid.Make().String(), content, episodeID, seedAt)

		srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
		if err != nil {
			t.Fatalf("api.New: %v", err)
		}
		srv.SetPipelineIn(p.In)
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

		body, _ := json.Marshal(map[string]any{"query": query, "limit": 5})
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/v1/retrieve", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+plaintext)
		resp, derr := ts.Client().Do(req)
		if derr != nil {
			t.Fatalf("POST /v1/retrieve: %v", derr)
		}
		defer func() { _ = resp.Body.Close() }()
		var rr struct {
			Items []struct {
				Citation string `json:"citation"`
			} `json:"items"`
			Rendered string `json:"rendered"`
		}
		if derr := json.NewDecoder(resp.Body).Decode(&rr); derr != nil {
			t.Fatalf("decode: %v", derr)
		}
		if len(rr.Items) == 0 {
			t.Fatal("http: no items returned")
		}
		return normalize(rr.Rendered, rr.Items[0].Citation)
	}()

	// ── SDK (embedded) ──
	sdkNorm := func() string {
		cfg := baseConfig(t)
		tenant := uniqueTenant("lean-parity-sdk")
		scope := identity.Scope{Tenant: tenant}
		ctx := context.Background()

		client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(tenant))
		if err != nil {
			t.Fatalf("NewEmbedded: %v", err)
		}
		t.Cleanup(func() {
			shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
			defer c()
			_ = closer(shutCtx)
		})

		side, err := store.Open(ctx, cfg.Store)
		if err != nil {
			t.Fatalf("side store open: %v", err)
		}
		seedLeanReadMemoryAt(t, side, scope, ulid.Make().String(), content, episodeID, seedAt)
		_ = side.Close(ctx)

		resp, err := client.Retrieve(ctx, stowage.RetrieveRequest{Query: query, Limit: 5})
		if err != nil {
			t.Fatalf("embedded retrieve: %v", err)
		}
		if len(resp.Items) == 0 {
			t.Fatal("sdk: no items returned")
		}
		return normalize(resp.Rendered, resp.Items[0].Citation)
	}()

	if mcpNorm != httpNorm {
		t.Errorf("MCP/HTTP normalized bodies diverge:\n mcp:  %q\n http: %q", mcpNorm, httpNorm)
	}
	if mcpNorm != sdkNorm {
		t.Errorf("MCP/SDK normalized bodies diverge:\n mcp: %q\n sdk: %q", mcpNorm, sdkNorm)
	}
	if !strings.Contains(mcpNorm, "[episode:"+episodeID+"]") {
		t.Errorf("normalized body missing episode hook: %q", mcpNorm)
	}
	if !strings.Contains(mcpNorm, "[cite:<CITE>]") {
		t.Errorf("normalized body missing normalized drill handle: %q", mcpNorm)
	}
}
