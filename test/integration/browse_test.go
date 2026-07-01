// browse_test.go proves the ae5 (D-143) list/browse capability end to end over
// real drivers: a multi-page created_at DESC sweep is gap-free and dup-free
// and byte-identical in ordering across all three single-user surfaces (SDK,
// HTTP, MCP); scope isolation holds; mode=superseded returns only superseded
// rows (oldest-first — the deliberate H4 ordering asymmetry); and a malformed
// cursor fails closed on every surface (an error, never a panic). Runs under
// -race. Postgres subtests are gated on STOWAGE_TEST_PG_DSN, the established
// pattern (internal/store/pgstore/pgstore_test.go, retrieve_lean_read_test.go)
// — sqlite always runs.
package integration

import (
	"context"
	"encoding/json"
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
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// browseDefaultLimitTest mirrors the config default (retrieval.browse_default_limit,
// D-143) so tests exercising the omit-limit path see the same page size a real
// deployment would.
const browseDefaultLimitTest = 30

// seedBrowseMemories inserts n active memories with distinct, strictly
// increasing created_at (base, base+1, …) plus 2 superseded memories (later
// timestamps), directly via a side store connection against cfg.Store —
// mirrors seedEpisodes/seedLeanReadMemory. Returns the active ids and the
// superseded ids, both in OLDEST-FIRST (insertion) order.
func seedBrowseMemories(t *testing.T, cfg config.Config, tenant string, n int) (activeIDs, supersededIDs []string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("seed browse: open: %v", err)
	}
	defer func() { _ = st.Close(ctx) }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("seed browse: migrate: %v", err)
	}
	scope := identity.Scope{Tenant: tenant}
	base := time.Now().UnixMilli()

	insert := func(id, status string, createdAt int64) {
		if err := st.Memories().Insert(ctx, scope, store.Memory{
			ID: id, Kind: "fact", Content: "content-" + id, Status: status,
			Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}); err != nil {
			t.Fatalf("seed browse insert %s: %v", id, err)
		}
	}
	for i := 0; i < n; i++ {
		id := ulid.Make().String()
		insert(id, "active", base+int64(i))
		activeIDs = append(activeIDs, id)
	}
	for i := 0; i < 2; i++ {
		id := ulid.Make().String()
		insert(id, "superseded", base+int64(10_000+i))
		supersededIDs = append(supersededIDs, id)
	}
	return activeIDs, supersededIDs
}

func browseEmbedded(t *testing.T, cfg config.Config, tenant string, req stowage.BrowseRequest) stowage.BrowseResponse {
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
	resp, err := client.Browse(ctx, req)
	if err != nil {
		t.Fatalf("embedded Browse: %v", err)
	}
	return resp
}

func browseEmbeddedErr(t *testing.T, cfg config.Config, tenant string, req stowage.BrowseRequest) error {
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
	_, err = client.Browse(ctx, req)
	return err
}

func browseHTTP(t *testing.T, cfg config.Config, tenant string, req stowage.BrowseRequest) stowage.BrowseResponse {
	t.Helper()
	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetStage(p.Stage)
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
	resp, err := client.Browse(ctx, req)
	if err != nil {
		t.Fatalf("http Browse: %v", err)
	}
	return resp
}

func browseHTTPErr(t *testing.T, cfg config.Config, tenant string, req stowage.BrowseRequest) error {
	t.Helper()
	ctx := context.Background()
	stk, p := startStack(t, cfg)
	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetStage(p.Stage)
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
	_, err = client.Browse(ctx, req)
	return err
}

func browseMCP(t *testing.T, cfg config.Config, tenant string, in mcpserver.BrowseInput) stowage.BrowseResponse {
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
		Store: stk.Store, PipelineIn: p.In, Log: stk.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenant), Profile: cfg.Profile,
		BrowseDefaultLimit: cfg.Retrieval.BrowseDefaultLimit,
	}
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "browse-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_browse", Arguments: in})
	if err != nil {
		t.Fatalf("CallTool memory_browse: %v", err)
	}
	if res.IsError {
		t.Fatalf("memory_browse returned IsError: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var resp stowage.BrowseResponse
	if err := json.Unmarshal(b, &resp); err != nil {
		t.Fatalf("remap memory_browse -> SDK: %v", err)
	}
	return resp
}

// browseMCPIsFailure calls memory_browse and reports whether it failed — either
// as a protocol-level CallTool error or an IsError tool result — mirroring
// TestRetrieveLeanRead_ScopeFailure's failure-mode check.
func browseMCPIsFailure(t *testing.T, cfg config.Config, tenant string, in mcpserver.BrowseInput) bool {
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
		Store: stk.Store, PipelineIn: p.In, Log: stk.Log,
		ScopeFn: mcpserver.StdioScopeFn(tenant), Profile: cfg.Profile,
		BrowseDefaultLimit: cfg.Retrieval.BrowseDefaultLimit,
	}
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "browse-client-err", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	res, cerr := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_browse", Arguments: in})
	return cerr != nil || (res != nil && res.IsError)
}

func idsOf(resp stowage.BrowseResponse) []string {
	ids := make([]string, len(resp.Memories))
	for i, m := range resp.Memories {
		ids[i] = m.ID
	}
	return ids
}

func statusesOf(resp stowage.BrowseResponse) []string {
	statuses := make([]string, len(resp.Memories))
	for i, m := range resp.Memories {
		statuses[i] = m.Status
	}
	return statuses
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func reverseIDs(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

// assertSameSet fails unless got contains exactly the ids in want, each
// exactly once — the gap-free/dup-free full-sweep property (Q1).
func assertSameSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d ids want %d\n got=%v\nwant=%v", label, len(got), len(want), got, want)
	}
	seen := make(map[string]int, len(got))
	for _, id := range got {
		seen[id]++
	}
	for _, id := range want {
		if seen[id] != 1 {
			t.Errorf("%s: id %q seen %d times want exactly 1", label, id, seen[id])
		}
	}
}

// browseSweep runs page(cursor) repeatedly ("" first) until NextCursor is
// empty, concatenating every page's ids in order. Guards against a
// non-terminating sweep on a bug.
func browseSweep(t *testing.T, page func(cursor string) stowage.BrowseResponse) []string {
	t.Helper()
	var all []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatalf("browseSweep: did not terminate after 20 pages")
		}
		resp := page(cursor)
		all = append(all, idsOf(resp)...)
		cursor = resp.NextCursor
		if cursor == "" {
			break
		}
	}
	return all
}

// TestBrowse_MultiPageRecentSweep_AllSurfaces is AC-1/AC-4: a multi-page
// mode=recent sweep (page size 3 over 7 rows) visits every seeded active
// memory exactly once, most-recent-first, and the ordered id sequence is
// BYTE IDENTICAL across the embedded SDK, HTTP, and MCP surfaces — the
// gap-free/dup-free keyset property proven over real drivers.
func TestBrowse_MultiPageRecentSweep_AllSurfaces(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("browse-sweep-" + driver)
			// ListByScopeRecent (mode=recent) is status-agnostic — it walks EVERY
			// memory in the scope regardless of status (only mode=superseded
			// filters by status, via the reused ListByStatus). So the full sweep's
			// expected set is active+superseded together, oldest-to-newest by
			// insertion (seedBrowseMemories stamps superseded rows with LATER
			// created_at than the active rows).
			activeIDs, supersededIDs := seedBrowseMemories(t, cfg, tenant, 7)
			allSeeded := append(append([]string{}, activeIDs...), supersededIDs...)

			embSweep := browseSweep(t, func(cursor string) stowage.BrowseResponse {
				return browseEmbedded(t, cfg, tenant, stowage.BrowseRequest{Limit: 3, Cursor: cursor})
			})
			httpSweep := browseSweep(t, func(cursor string) stowage.BrowseResponse {
				return browseHTTP(t, cfg, tenant, stowage.BrowseRequest{Limit: 3, Cursor: cursor})
			})
			mcpSweep := browseSweep(t, func(cursor string) stowage.BrowseResponse {
				return browseMCP(t, cfg, tenant, mcpserver.BrowseInput{Limit: 3, Cursor: cursor})
			})

			assertSameSet(t, "embedded", embSweep, allSeeded)
			assertSameSet(t, "http", httpSweep, allSeeded)
			assertSameSet(t, "mcp", mcpSweep, allSeeded)

			if !stringSlicesEqual(embSweep, httpSweep) {
				t.Errorf("embedded vs http sweep order diverged:\n emb=%v\nhttp=%v", embSweep, httpSweep)
			}
			if !stringSlicesEqual(embSweep, mcpSweep) {
				t.Errorf("embedded vs mcp sweep order diverged:\n emb=%v\n mcp=%v", embSweep, mcpSweep)
			}

			// Most-recent-first: the exact reverse of insertion order.
			want := reverseIDs(allSeeded)
			if !stringSlicesEqual(embSweep, want) {
				t.Errorf("sweep order wrong: got %v want %v (most-recent-first)", embSweep, want)
			}
		})
	}
}

// TestBrowse_ScopeIsolation_AllSurfaces is AC-2 (P3): a cross-tenant browse
// never returns another tenant's memories, on every surface, over real
// drivers.
func TestBrowse_ScopeIsolation_AllSurfaces(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenantA := uniqueTenant("browse-iso-a-" + driver)
			tenantB := uniqueTenant("browse-iso-b-" + driver)
			activeA, _ := seedBrowseMemories(t, cfg, tenantA, 2)
			_, _ = seedBrowseMemories(t, cfg, tenantB, 1)

			embB := browseEmbedded(t, cfg, tenantB, stowage.BrowseRequest{Limit: 10})
			httpB := browseHTTP(t, cfg, tenantB, stowage.BrowseRequest{Limit: 10})
			mcpB := browseMCP(t, cfg, tenantB, mcpserver.BrowseInput{Limit: 10})

			for label, resp := range map[string]stowage.BrowseResponse{"embedded": embB, "http": httpB, "mcp": mcpB} {
				for _, id := range idsOf(resp) {
					for _, aID := range activeA {
						if id == aID {
							t.Fatalf("%s: cross-tenant leak — tenant B saw tenant A's memory %s", label, aID)
						}
					}
				}
			}
		})
	}
}

// TestBrowse_SupersededMode_AllSurfaces is AC-3 (H4): mode=superseded returns
// ONLY superseded rows, ordered oldest-first (the deliberate ListByStatus
// ASC asymmetry with mode=recent's DESC), identically on every surface.
func TestBrowse_SupersededMode_AllSurfaces(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("browse-superseded-" + driver)
			_, supersededIDs := seedBrowseMemories(t, cfg, tenant, 3)

			embR := browseEmbedded(t, cfg, tenant, stowage.BrowseRequest{Mode: "superseded", Limit: 10})
			httpR := browseHTTP(t, cfg, tenant, stowage.BrowseRequest{Mode: "superseded", Limit: 10})
			mcpR := browseMCP(t, cfg, tenant, mcpserver.BrowseInput{Mode: "superseded", Limit: 10})

			for label, resp := range map[string]stowage.BrowseResponse{"embedded": embR, "http": httpR, "mcp": mcpR} {
				for _, status := range statusesOf(resp) {
					if status != "superseded" {
						t.Errorf("%s: non-superseded row in mode=superseded: %+v", label, resp.Memories)
					}
				}
				if !stringSlicesEqual(idsOf(resp), supersededIDs) {
					t.Errorf("%s: superseded order wrong (want oldest-first): got %v want %v", label, idsOf(resp), supersededIDs)
				}
			}
		})
	}
}

// TestBrowse_BadCursorFailureMode_AllSurfaces is the required failure mode
// (§17): a malformed cursor fails closed with an error on every surface —
// never a panic, never a silent first page.
func TestBrowse_BadCursorFailureMode_AllSurfaces(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("browse-badcursor-" + driver)
			seedBrowseMemories(t, cfg, tenant, 1)

			if err := browseEmbeddedErr(t, cfg, tenant, stowage.BrowseRequest{Cursor: "not-a-cursor"}); err == nil {
				t.Error("embedded: expected an error for a malformed cursor")
			}
			if err := browseHTTPErr(t, cfg, tenant, stowage.BrowseRequest{Cursor: "not-a-cursor"}); err == nil {
				t.Error("http: expected an error for a malformed cursor")
			}
			if !browseMCPIsFailure(t, cfg, tenant, mcpserver.BrowseInput{Cursor: "not-a-cursor"}) {
				t.Error("mcp: expected a failure for a malformed cursor")
			}
		})
	}
}

// TestBrowse_SurfaceParity_FixedPage is AC-4: for a FIXED (scope, mode, limit,
// cursor), SDK, HTTP, and MCP return identical ids + next_cursor. Runs once
// against sqlite (parity is about surface wiring, not driver behavior).
func TestBrowse_SurfaceParity_FixedPage(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Retrieval.BrowseDefaultLimit = browseDefaultLimitTest
	tenant := uniqueTenant("browse-parity")
	seedBrowseMemories(t, cfg, tenant, 5)

	req := stowage.BrowseRequest{Limit: 2}
	emb := browseEmbedded(t, cfg, tenant, req)
	htp := browseHTTP(t, cfg, tenant, req)
	mcp := browseMCP(t, cfg, tenant, mcpserver.BrowseInput{Limit: 2})

	if !stringSlicesEqual(idsOf(emb), idsOf(htp)) || emb.NextCursor != htp.NextCursor {
		t.Errorf("embedded vs http parity diverged:\n emb=%v cursor=%q\nhttp=%v cursor=%q",
			idsOf(emb), emb.NextCursor, idsOf(htp), htp.NextCursor)
	}
	if !stringSlicesEqual(idsOf(emb), idsOf(mcp)) || emb.NextCursor != mcp.NextCursor {
		t.Errorf("embedded vs mcp parity diverged:\n emb=%v cursor=%q\n mcp=%v cursor=%q",
			idsOf(emb), emb.NextCursor, idsOf(mcp), mcp.NextCursor)
	}
	if len(idsOf(emb)) != 2 || emb.NextCursor == "" {
		t.Fatalf("expected a 2-item page with a next cursor, got %v cursor=%q", idsOf(emb), emb.NextCursor)
	}
}

// TestBrowse_DefaultLimit_AllSurfaces is AC-6: a limit-omitting browse returns
// retrieval.browse_default_limit items when at least that many rows exist.
func TestBrowse_DefaultLimit_AllSurfaces(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Retrieval.BrowseDefaultLimit = 3
	tenant := uniqueTenant("browse-default-limit")
	seedBrowseMemories(t, cfg, tenant, 5)

	emb := browseEmbedded(t, cfg, tenant, stowage.BrowseRequest{})
	if len(emb.Memories) != 3 {
		t.Errorf("embedded: default-limit browse returned %d items, want 3", len(emb.Memories))
	}
	htp := browseHTTP(t, cfg, tenant, stowage.BrowseRequest{})
	if len(htp.Memories) != 3 {
		t.Errorf("http: default-limit browse returned %d items, want 3", len(htp.Memories))
	}
	mcpResp := browseMCP(t, cfg, tenant, mcpserver.BrowseInput{})
	if len(mcpResp.Memories) != 3 {
		t.Errorf("mcp: default-limit browse returned %d items, want 3", len(mcpResp.Memories))
	}
}
