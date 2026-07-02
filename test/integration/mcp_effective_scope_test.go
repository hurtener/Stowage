// mcp_effective_scope_test.go proves ae2b (D-140/M1, §17 — closes the seam
// ae7/ae8 opened): with project_id/user_id deleted from the 13 MCP input
// contracts, identity resolves purely from _meta/JWT via
// resolveScope→identity.ResolveReadScope, for every one of the 13 tools. Each
// subtest seeds distinct {tenant,user} data, calls the tool with ONLY a
// _meta.user (never an arg — the arg no longer exists on the Go struct, so
// there is nothing left to set), and asserts the resolved scope is correct.
//
// Where the underlying store predicate is user-scoped (retrieve, get,
// episodes, causal, review, resolve, rollback, playbook, branch — the
// majority of the 13), the assertion is real cross-user ISOLATION: u1's call
// sees u1's row and never u2's. Where the underlying sub-store predicate is
// tenant-scoped only by pre-existing design (verify/trace's citation and
// response_id lookups, drilldown's memory_id junction fetch, feedback's
// citation/response_id apply — internal/store/sqlitestore's injections.go and
// GetJunctions filter on tenant_id only, NOT user_id; this is unchanged,
// pre-ae2b store behavior, not something this phase alters or is responsible
// for fixing), the assertion is that the call resolves and succeeds via
// _meta alone — proving the args were never needed, without asserting an
// isolation guarantee the store never provided.
//
// Runs under -race. Postgres subtests are gated on STOWAGE_TEST_PG_DSN (the
// established pattern, pgstore_test.go) — sqlite always runs.
package integration

import (
	"context"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oklog/ulid/v2"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/store"
)

// effScopeConfig returns a baseConfig for the ae2b effective-scope tests, with
// Profile set — memory_playbook's token budget is profile-internal (D-042).
func effScopeConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := baseConfig(t)
	cfg.Profile = "assistant"
	return cfg
}

// callEffScope boots a fresh Services bound to tenant, calls the named tool
// with in + an optional _meta map, and returns the raw result plus whether
// the call failed (a protocol-level error or an IsError tool result) — the
// one shared shape every per-tool subtest below uses.
func callEffScope(t *testing.T, cfg config.Config, tenant, name string, in any, meta map[string]any) (*mcpsdk.CallToolResult, bool) {
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
		Store: stk.Store, Retriever: stk.Retriever, Gateway: stk.Gateway,
		TopicSvc: stk.TopicSvc, TraceSigner: stk.TraceSigner,
		PipelineIn: p.In, PipelineStage: p.Stage,
		Log: stk.Log, ScopeFn: mcpserver.StdioScopeFn(tenant), Profile: cfg.Profile,
	}
	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, svc)
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "effscope-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	params := &mcpsdk.CallToolParams{Name: name, Arguments: in}
	if len(meta) > 0 {
		params.Meta = mcpsdk.Meta(meta)
	}
	res, cerr := session.CallTool(ctx, params)
	failed := cerr != nil || (res != nil && res.IsError)
	return res, failed
}

// seedEffScopeMemory inserts one active "fact" memory directly (bypassing the
// pipeline), scoped to scope, lexically retrievable via FTS.
func seedEffScopeMemory(t *testing.T, st store.Store, scope identity.Scope, id, content string) {
	t.Helper()
	now := time.Now().UnixMilli()
	if err := st.Memories().Insert(context.Background(), scope, store.Memory{
		ID: id, TenantID: scope.Tenant, Kind: "fact", Content: content, Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
		ContentHash: ulid.Make().String(), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed memory %s: %v", id, err)
	}
}

// TestMCPEffectiveScope_Retrieve (AC-2): _meta.user alone narrows
// memory_retrieve — RetrieveInput no longer has a user_id/project_id arg.
func TestMCPEffectiveScope_Retrieve(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-retrieve")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedLeanReadMemory(t, st, identity.Scope{Tenant: tenant, User: "u1"}, "01EFFRETU1AAAAAAAAAAAAAAA", "effscope retrieve qzxu1", "")
	seedLeanReadMemory(t, st, identity.Scope{Tenant: tenant, User: "u2"}, "01EFFRETU2AAAAAAAAAAAAAAA", "effscope retrieve qzxu1", "")
	_ = st.Close(ctx)

	res, failed := callEffScope(t, cfg, tenant, "memory_retrieve",
		mcpserver.RetrieveInput{Query: "qzxu1", Limit: 10}, map[string]any{"user": "u1"})
	if failed {
		t.Fatalf("memory_retrieve with _meta.user failed: %+v", res)
	}
	var out mcpserver.RetrieveOutput
	decodeStructured(t, res, &out)
	ids := map[string]bool{}
	for _, it := range out.Items {
		ids[it.ID] = true
	}
	if !ids["01EFFRETU1AAAAAAAAAAAAAAA"] {
		t.Errorf("expected u1's memory via _meta.user, got %+v", out.Items)
	}
	if ids["01EFFRETU2AAAAAAAAAAAAAAA"] {
		t.Errorf("P3 LEAK: u2's memory visible under _meta.user=u1, got %+v", out.Items)
	}
}

// TestMCPEffectiveScope_Get (AC-2): _meta.user alone narrows memory_get.
func TestMCPEffectiveScope_Get(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-get")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedEffScopeMemory(t, st, identity.Scope{Tenant: tenant, User: "u1"}, "01EFFGETU1AAAAAAAAAAAAAAA", "effscope get fixture u1")
	_ = st.Close(ctx)

	// u1 sees its own memory.
	res, failed := callEffScope(t, cfg, tenant, "memory_get",
		mcpserver.GetInput{MemoryID: "01EFFGETU1AAAAAAAAAAAAAAA"}, map[string]any{"user": "u1"})
	if failed {
		t.Fatalf("memory_get (owner) with _meta.user failed: %+v", res)
	}
	var out mcpserver.GetOutput
	decodeStructured(t, res, &out)
	if out.Memory.ID != "01EFFGETU1AAAAAAAAAAAAAAA" {
		t.Errorf("expected u1's memory via _meta.user, got %+v", out.Memory)
	}

	// u2 must NOT be able to get u1's memory (P3 isolation, args gone).
	_, failed2 := callEffScope(t, cfg, tenant, "memory_get",
		mcpserver.GetInput{MemoryID: "01EFFGETU1AAAAAAAAAAAAAAA"}, map[string]any{"user": "u2"})
	if !failed2 {
		t.Error("P3 LEAK: u2 resolved u1's memory via memory_get")
	}
}

// TestMCPEffectiveScope_Episodes (AC-2): _meta.user alone narrows memory_episodes.
func TestMCPEffectiveScope_Episodes(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-episodes")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UnixMilli()
	mk := func(id, user string) store.Episode {
		return store.Episode{ID: id, SessionID: "sess-" + user, Title: "Effscope " + user, Status: "closed",
			Outcome: "success", StartedAt: now, EndedAt: now + 500, CreatedAt: now, UpdatedAt: now}
	}
	if err := st.Episodes().CreateEpisode(ctx, identity.Scope{Tenant: tenant, User: "u1"}, mk("01EFFEPU1AAAAAAAAAAAAAAAA", "u1")); err != nil {
		t.Fatalf("seed u1 episode: %v", err)
	}
	if err := st.Episodes().CreateEpisode(ctx, identity.Scope{Tenant: tenant, User: "u2"}, mk("01EFFEPU2AAAAAAAAAAAAAAAA", "u2")); err != nil {
		t.Fatalf("seed u2 episode: %v", err)
	}
	_ = st.Close(ctx)

	res, failed := callEffScope(t, cfg, tenant, "memory_episodes",
		mcpserver.EpisodesInput{Limit: 10}, map[string]any{"user": "u1"})
	if failed {
		t.Fatalf("memory_episodes with _meta.user failed: %+v", res)
	}
	var out mcpserver.EpisodesOutput
	decodeStructured(t, res, &out)
	ids := map[string]bool{}
	for _, e := range out.Episodes {
		ids[e.ID] = true
	}
	if !ids["01EFFEPU1AAAAAAAAAAAAAAAA"] {
		t.Errorf("expected u1's episode via _meta.user, got %+v", out.Episodes)
	}
	if ids["01EFFEPU2AAAAAAAAAAAAAAAA"] {
		t.Errorf("P3 LEAK: u2's episode visible under _meta.user=u1, got %+v", out.Episodes)
	}
}

// TestMCPEffectiveScope_Causal (AC-2): _meta.user alone narrows memory_causal
// — a root memory that exists only in u2's scope resolves to an EMPTY graph
// under _meta.user=u1 (mirrors the existing missing-root contract; causal
// traversal reads memories through the same user-scoped store predicate).
func TestMCPEffectiveScope_Causal(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-causal")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UnixMilli()
	u2Scope := identity.Scope{Tenant: tenant, User: "u2"}
	for _, m := range []store.Memory{
		{ID: "01EFFCAUSEU2AAAAAAAAAAAAA", Kind: "decision", Content: "u2 cause", Status: "active",
			Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: now, UpdatedAt: now},
		{ID: "01EFFEFFECTU2AAAAAAAAAAAA", Kind: "decision", Content: "u2 effect", Status: "active",
			Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.Memories().Insert(ctx, u2Scope, m); err != nil {
			t.Fatalf("seed memory %s: %v", m.ID, err)
		}
	}
	if err := st.Memories().InsertLinks(ctx, u2Scope, []store.Link{{
		ID: ulid.Make().String(), TenantID: tenant,
		FromMemory: "01EFFCAUSEU2AAAAAAAAAAAAA", ToMemory: "01EFFEFFECTU2AAAAAAAAAAAA",
		Type: "led_to", Source: "inferred", Confidence: 0.9, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("seed link: %v", err)
	}
	_ = st.Close(ctx)

	// u1 traversing u2's root sees an empty graph (cross-user isolation).
	res, failed := callEffScope(t, cfg, tenant, "memory_causal",
		mcpserver.CausalInput{MemoryID: "01EFFEFFECTU2AAAAAAAAAAAA", Direction: "backward", Depth: 3},
		map[string]any{"user": "u1"})
	if failed {
		t.Fatalf("memory_causal with _meta.user failed: %+v", res)
	}
	var out mcpserver.CausalOutput
	decodeStructured(t, res, &out)
	if len(out.Nodes) != 0 || len(out.Edges) != 0 {
		t.Errorf("P3 LEAK: u1 traversed u2's causal graph via _meta.user, got %+v", out)
	}

	// u2 sees its own graph.
	res2, failed2 := callEffScope(t, cfg, tenant, "memory_causal",
		mcpserver.CausalInput{MemoryID: "01EFFEFFECTU2AAAAAAAAAAAA", Direction: "backward", Depth: 3},
		map[string]any{"user": "u2"})
	if failed2 {
		t.Fatalf("memory_causal (owner) with _meta.user failed: %+v", res2)
	}
	var out2 mcpserver.CausalOutput
	decodeStructured(t, res2, &out2)
	if len(out2.Nodes) != 2 || len(out2.Edges) != 1 {
		t.Errorf("expected u2's own 2-node/1-edge graph via _meta.user, got %+v", out2)
	}
}

// TestMCPEffectiveScope_Review (AC-2): _meta.user alone narrows memory_review
// list AND approve — mirrors scope_parity_test.go's cross-user-denied bar,
// re-proven here specifically because the args are now gone.
func TestMCPEffectiveScope_Review(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-review")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := st.Memories().Insert(ctx, identity.Scope{Tenant: tenant, User: "u1"}, store.Memory{
		ID: "01EFFREVU1AAAAAAAAAAAAAAA", Kind: "fact", Content: "u1's uncited claim.", Status: "pending_review",
		Confidence: 0.5, TrustSource: "asserted", Stability: 1.0, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed u1 pending: %v", err)
	}
	if err := st.Memories().Insert(ctx, identity.Scope{Tenant: tenant, User: "u2"}, store.Memory{
		ID: "01EFFREVU2AAAAAAAAAAAAAAA", Kind: "fact", Content: "u2's uncited claim.", Status: "pending_review",
		Confidence: 0.5, TrustSource: "asserted", Stability: 1.0, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed u2 pending: %v", err)
	}
	_ = st.Close(ctx)

	// u1's list sees only its own pending memory.
	res, failed := callEffScope(t, cfg, tenant, "memory_review",
		mcpserver.ReviewInput{Action: "list"}, map[string]any{"user": "u1"})
	if failed {
		t.Fatalf("memory_review list with _meta.user failed: %+v", res)
	}
	var out mcpserver.ReviewOutput
	decodeStructured(t, res, &out)
	ids := map[string]bool{}
	for _, it := range out.Items {
		ids[it.ID] = true
	}
	if !ids["01EFFREVU1AAAAAAAAAAAAAAA"] || ids["01EFFREVU2AAAAAAAAAAAAAAA"] {
		t.Errorf("P3 LEAK/miss: expected only u1's pending memory, got %+v", out.Items)
	}

	// u1 attempting to approve u2's pending memory (MUTATE) must fail.
	_, failedApprove := callEffScope(t, cfg, tenant, "memory_review",
		mcpserver.ReviewInput{Action: "approve", ID: "01EFFREVU2AAAAAAAAAAAAAAA"}, map[string]any{"user": "u1"})
	if !failedApprove {
		t.Error("P3 MUTATE LEAK: u1 approved u2's pending memory via memory_review")
	}
}

// TestMCPEffectiveScope_Resolve (AC-2): _meta.user alone narrows
// memory_resolve — confirming another user's pending_confirmation memory fails.
func TestMCPEffectiveScope_Resolve(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-resolve")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := st.Memories().Insert(ctx, identity.Scope{Tenant: tenant, User: "u1"}, store.Memory{
		ID: "01EFFRESU1AAAAAAAAAAAAAAA", Kind: "fact", Content: "u1 parked", Status: "pending_confirmation",
		Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed u1 parked: %v", err)
	}
	_ = st.Close(ctx)

	// u2 cannot resolve u1's parked memory.
	_, failed := callEffScope(t, cfg, tenant, "memory_resolve",
		mcpserver.ResolveInput{MemoryID: "01EFFRESU1AAAAAAAAAAAAAAA", Action: "confirm"}, map[string]any{"user": "u2"})
	if !failed {
		t.Error("P3 LEAK: u2 resolved u1's pending_confirmation memory via memory_resolve")
	}

	// u1 resolves its own.
	res, failed2 := callEffScope(t, cfg, tenant, "memory_resolve",
		mcpserver.ResolveInput{MemoryID: "01EFFRESU1AAAAAAAAAAAAAAA", Action: "confirm"}, map[string]any{"user": "u1"})
	if failed2 {
		t.Fatalf("memory_resolve (owner) with _meta.user failed: %+v", res)
	}
	var out mcpserver.ResolveOutput
	decodeStructured(t, res, &out)
	if out.ID != "01EFFRESU1AAAAAAAAAAAAAAA" || out.Status == "" {
		t.Errorf("unexpected resolve output: %+v", out)
	}
}

// TestMCPEffectiveScope_Rollback (AC-2): _meta.user alone narrows
// memory_rollback — cross-user rollback fails with a scope miss (ErrNotFound),
// same-user rollback finds the record (fails later with ErrNoPriorState, no
// restorable event was seeded — mirrors the existing unit-level pattern).
func TestMCPEffectiveScope_Rollback(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-rollback")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedEffScopeMemory(t, st, identity.Scope{Tenant: tenant, User: "u1"}, "01EFFROLU1AAAAAAAAAAAAAAA", "effscope rollback fixture")
	_ = st.Close(ctx)

	// u2 gets a scope miss (ErrNotFound-shaped failure — the record isn't in u2's scope).
	_, failedCross := callEffScope(t, cfg, tenant, "memory_rollback",
		mcpserver.RollbackInput{MemoryID: "01EFFROLU1AAAAAAAAAAAAAAA"}, map[string]any{"user": "u2"})
	if !failedCross {
		t.Error("P3 LEAK: u2 rolled back u1's memory via memory_rollback")
	}

	// u1 finds the record via _meta.user (fails later on no-restorable-event,
	// not a scope miss — a DIFFERENT failure than u2's, proving the record WAS found).
	res, failedOwn := callEffScope(t, cfg, tenant, "memory_rollback",
		mcpserver.RollbackInput{MemoryID: "01EFFROLU1AAAAAAAAAAAAAAA"}, map[string]any{"user": "u1"})
	if !failedOwn {
		t.Fatal("expected an error (no restorable event seeded) — but the record must be FOUND, not scope-missed")
	}
	_ = res
}

// TestMCPEffectiveScope_Playbook (AC-2): _meta.user alone scopes
// memory_playbook assembly — a strategy memory in u1's scope is packed for u1
// and never for u2.
func TestMCPEffectiveScope_Playbook(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-playbook")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := st.Memories().Insert(ctx, identity.Scope{Tenant: tenant, User: "u1"}, store.Memory{
		ID: "01EFFPBU1AAAAAAAAAAAAAAAA", Kind: "strategy", Content: "Always write a failing test first.", Status: "active",
		Importance: 3, Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0, UseCount: 5,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed u1 strategy: %v", err)
	}
	_ = st.Close(ctx)

	res, failed := callEffScope(t, cfg, tenant, "memory_playbook", mcpserver.PlaybookInput{}, map[string]any{"user": "u1"})
	if failed {
		t.Fatalf("memory_playbook (owner) with _meta.user failed: %+v", res)
	}
	var out mcpserver.PlaybookOutput
	var found bool
	decodeStructured(t, res, &out)
	for _, sec := range out.Sections {
		for _, it := range sec.Items {
			if it.MemoryID == "01EFFPBU1AAAAAAAAAAAAAAAA" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected u1's strategy memory packed via _meta.user, got %+v", out.Sections)
	}

	res2, failed2 := callEffScope(t, cfg, tenant, "memory_playbook", mcpserver.PlaybookInput{}, map[string]any{"user": "u2"})
	if failed2 {
		t.Fatalf("memory_playbook (non-owner) with _meta.user failed: %+v", res2)
	}
	var out2 mcpserver.PlaybookOutput
	decodeStructured(t, res2, &out2)
	for _, sec := range out2.Sections {
		for _, it := range sec.Items {
			if it.MemoryID == "01EFFPBU1AAAAAAAAAAAAAAAA" {
				t.Errorf("P3 LEAK: u2's playbook packed u1's strategy memory")
			}
		}
	}
}

// TestMCPEffectiveScope_Branch (AC-2): _meta.user alone scopes memory_branch
// — a branch forked under u1 cannot be discarded by u2.
func TestMCPEffectiveScope_Branch(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-branch")

	fork, failed := callEffScope(t, cfg, tenant, "memory_branch",
		mcpserver.BranchInput{Action: "fork", SessionID: "sess-u1"}, map[string]any{"user": "u1"})
	if failed {
		t.Fatalf("memory_branch fork with _meta.user failed: %+v", fork)
	}
	var forkOut mcpserver.BranchOutput
	decodeStructured(t, fork, &forkOut)
	if forkOut.BranchID == "" {
		t.Fatalf("expected a branch_id from fork, got %+v", forkOut)
	}

	// u2 cannot discard u1's branch.
	_, failedDiscard := callEffScope(t, cfg, tenant, "memory_branch",
		mcpserver.BranchInput{Action: "discard", BranchID: forkOut.BranchID}, map[string]any{"user": "u2"})
	if !failedDiscard {
		t.Error("P3 LEAK: u2 discarded u1's branch via memory_branch")
	}

	// u1 can discard its own branch.
	_, failedOwn := callEffScope(t, cfg, tenant, "memory_branch",
		mcpserver.BranchInput{Action: "discard", BranchID: forkOut.BranchID}, map[string]any{"user": "u1"})
	if failedOwn {
		t.Error("u1 should be able to discard its own branch via _meta.user")
	}
}

// TestMCPEffectiveScope_TenantOnlySurfaces (AC-2, smoke): memory_verify,
// memory_trace, memory_drilldown, and memory_feedback resolve their scope
// purely from _meta (no project_id/user_id arg to fall back to) and succeed —
// these four tools' underlying sub-store lookups (injections, GetJunctions,
// trace events) are tenant-scoped only by pre-existing design (unrelated to
// ae2b), so this proves the args were never needed rather than asserting a
// per-user isolation guarantee the store never provided.
func TestMCPEffectiveScope_TenantOnlySurfaces(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-tenantonly")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	scope := identity.Scope{Tenant: tenant, User: "u1"}
	now := time.Now().UnixMilli()
	if err := st.Records().Append(ctx, scope, []store.Record{{
		ID: "01EFFTORECAAAAAAAAAAAAAAA", Role: "user", Content: "What is the capital of France?",
		OccurredAt: now, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: "01EFFTOMEMAAAAAAAAAAAAAAA", Kind: "fact", Content: "Paris is the capital of France.", Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	if err := st.Memories().AddProvenance(ctx, scope, []store.Provenance{{
		ID: "01EFFTOPVAAAAAAAAAAAAAAAA", MemoryID: "01EFFTOMEMAAAAAAAAAAAAAAA", RecordID: "01EFFTORECAAAAAAAAAAAAAAA",
		SpanStart: 0, SpanEnd: 30, TenantID: tenant, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("seed provenance: %v", err)
	}
	if err := st.Injections().Append(ctx, scope, []store.Injection{{
		ID: "01EFFTOINJAAAAAAAAAAAAAAA", ResponseID: "resp-effscope-1", MemoryID: "01EFFTOMEMAAAAAAAAAAAAAAA",
		Rank: 0, Score: 0.9, WasCited: true, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("seed injection: %v", err)
	}
	if err := st.Events().Emit(ctx, scope, store.Event{
		ID: "01EFFTOEVQAAAAAAAAAAAAAAA", TenantID: tenant, Type: "retrieve.query", SubjectID: "resp-effscope-1",
		Payload: `{"query":"What is the capital of France?","support":"strong","degraded":false}`, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed query event: %v", err)
	}
	_ = st.Close(ctx)

	meta := map[string]any{"user": "u1"}

	if _, failed := callEffScope(t, cfg, tenant, "memory_verify",
		mcpserver.VerifyInput{Claim: "Paris is the capital of France.", Citations: []string{"01EFFTOINJAAAAAAAAAAAAAAA"}}, meta); failed {
		t.Error("memory_verify with _meta.user (no args) should resolve and succeed")
	}
	if _, failed := callEffScope(t, cfg, tenant, "memory_trace",
		mcpserver.TraceInput{ResponseID: "resp-effscope-1"}, meta); failed {
		t.Error("memory_trace with _meta.user (no args) should resolve and succeed")
	}
	if _, failed := callEffScope(t, cfg, tenant, "memory_drilldown",
		mcpserver.DrilldownInput{MemoryID: "01EFFTOMEMAAAAAAAAAAAAAAA"}, meta); failed {
		t.Error("memory_drilldown with _meta.user (no args) should resolve and succeed")
	}
	if _, failed := callEffScope(t, cfg, tenant, "memory_feedback",
		mcpserver.FeedbackInput{MemoryID: "01EFFTOMEMAAAAAAAAAAAAAAA", Signal: "use"}, meta); failed {
		t.Error("memory_feedback with _meta.user (no args) should resolve and succeed")
	}
}

// TestMCPEffectiveScope_MetaProject_Narrows (AC-4, M1): _meta.project alone —
// the sole remaining MCP channel for project narrowing — resolves
// memory_retrieve to the right project scope, over a real driver via the full
// MCP transport (complements the handler-level
// TestHandlerRetrieve_MetaProject_Narrows in internal/mcpserver).
func TestMCPEffectiveScope_MetaProject_Narrows(t *testing.T) {
	cfg := effScopeConfig(t)
	tenant := uniqueTenant("effscope-project")
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store for seeding: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedLeanReadMemory(t, st, identity.Scope{Tenant: tenant, Project: "p1"}, "01EFFPRJP1AAAAAAAAAAAAAAA", "effscope project qzpj1", "")
	seedLeanReadMemory(t, st, identity.Scope{Tenant: tenant, Project: "p2"}, "01EFFPRJP2AAAAAAAAAAAAAAA", "effscope project qzpj1", "")
	_ = st.Close(ctx)

	res, failed := callEffScope(t, cfg, tenant, "memory_retrieve",
		mcpserver.RetrieveInput{Query: "qzpj1", Limit: 10}, map[string]any{"project": "p1"})
	if failed {
		t.Fatalf("memory_retrieve with _meta.project failed: %+v", res)
	}
	var out mcpserver.RetrieveOutput
	decodeStructured(t, res, &out)
	ids := map[string]bool{}
	for _, it := range out.Items {
		ids[it.ID] = true
	}
	if !ids["01EFFPRJP1AAAAAAAAAAAAAAA"] {
		t.Errorf("expected p1's memory via _meta.project, got %+v", out.Items)
	}
	if ids["01EFFPRJP2AAAAAAAAAAAAAAA"] {
		t.Errorf("P3 LEAK: p2's memory visible under _meta.project=p1, got %+v", out.Items)
	}
}

// TestMCPEffectiveScope_NoIdentity_MatchesReadPosture (AC-2, the required
// failure mode): a memory_retrieve call carrying NEITHER _meta NOR a JWT
// claim behaves exactly as ae8's retrieval.read_posture already defines —
// compatible ⇒ tenant-wide (byte-identical to today), strict ⇒
// ErrIdentityRequired refused before any store call. ae2b introduces NO third
// fallback behaviour: this reuses ae8's retrieveMCPStrict helper
// (effective_scope_test.go) to prove the strict refusal still holds now that
// there is no arg left to accidentally satisfy it with.
func TestMCPEffectiveScope_NoIdentity_MatchesReadPosture(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("effscope-noident-" + driver)

			ctx := context.Background()
			st, err := store.Open(ctx, cfg.Store)
			if err != nil {
				t.Fatalf("open store for seeding: %v", err)
			}
			if err := st.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			u1ID := ulid.Make().String()
			u2ID := ulid.Make().String()
			seedLeanReadMemory(t, st, identity.Scope{Tenant: tenant, User: "u1"}, u1ID, "effscope noident qzxni", "")
			seedLeanReadMemory(t, st, identity.Scope{Tenant: tenant, User: "u2"}, u2ID, "effscope noident qzxni", "")
			_ = st.Close(ctx)

			// compatible (default): no _meta, no args ⇒ tenant-wide, sees both.
			resp := retrieveMCPWithMeta(t, cfg, tenant, mcpserver.RetrieveInput{Query: "qzxni", Limit: 10}, nil)
			got := idSetOf(resp)
			if !got[u1ID] || !got[u2ID] {
				t.Errorf("compatible posture + no identity: expected tenant-wide (both users), got %v", got)
			}

			// strict: no _meta, no args ⇒ ErrIdentityRequired, refused before any store call.
			_, refused := retrieveMCPStrict(t, cfg, tenant, mcpserver.RetrieveInput{Query: "qzxni", Limit: 10}, nil)
			if !refused {
				t.Error("strict posture + no identity: expected a refusal (ErrIdentityRequired), got success")
			}
		})
	}
}
