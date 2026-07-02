// effective_scope_test.go proves ae8's effective-scope resolver
// (identity.ResolveReadScope, D-148) end to end over real drivers (§17 — ae8's
// Deps name ae2/ae7 and it closes the read-scope seam): with
// retrieval.read_posture=strict, a retrieve carrying a resolved user isolates to
// that user's rows (identity/scope propagation through the store), and a retrieve
// resolving to NO user and NO agent is REFUSED (ErrIdentityRequired) BEFORE any
// store read — the ≥1 failure mode. Runs under -race. Postgres subtests are gated
// on STOWAGE_TEST_PG_DSN (the established pattern); sqlite always runs.
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
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// retrieveMCPStrict calls memory_retrieve through an MCP server booted with
// retrieval.read_posture=strict (Services.ResolveOpts), returning the decoded
// response and whether the call failed (a protocol error or an IsError tool
// result — the shape a resolver refusal surfaces as). Mirrors
// retrieveMCPWithMeta but flips the posture, so the strict path is exercised
// end to end, not just in the resolver unit test.
func retrieveMCPStrict(t *testing.T, cfg config.Config, tenant string, in mcpserver.RetrieveInput, meta map[string]any) (stowage.RetrieveResponse, bool) {
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
		// The whole point of this test: strict read posture.
		ResolveOpts: identity.ResolveOptions{Posture: identity.PostureStrict},
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
	params := &mcpsdk.CallToolParams{Name: "memory_retrieve", Arguments: in}
	if len(meta) > 0 {
		params.Meta = mcpsdk.Meta(meta)
	}
	res, cerr := session.CallTool(ctx, params)
	if cerr != nil || (res != nil && res.IsError) {
		return stowage.RetrieveResponse{}, true
	}
	var out mcpserver.RetrieveOutput
	decodeStructured(t, res, &out)
	return stowage.RetrieveResponse{Items: mcpItemsToSDK(out.Items)}, false
}

// TestEffectiveScope_StrictResolvedUserIsolates (AC-7): under strict posture a
// retrieve carrying a resolved user (_meta.user=u1) returns ONLY that user's own
// rows — identity/scope propagation through the store, on both drivers.
func TestEffectiveScope_StrictResolvedUserIsolates(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("effscope-strict-" + driver)

			ctx := context.Background()
			st, err := store.Open(ctx, cfg.Store)
			if err != nil {
				t.Fatalf("open store for seeding: %v", err)
			}
			if err := st.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			u1ID, u2ID := ulid.Make().String(), ulid.Make().String()
			seedLeanReadMemory(t, st, identity.Scope{Tenant: tenant, User: "u1"}, u1ID, "u1 memory effscope", "")
			seedLeanReadMemory(t, st, identity.Scope{Tenant: tenant, User: "u2"}, u2ID, "u2 memory effscope", "")
			_ = st.Close(ctx)

			resp, failed := retrieveMCPStrict(t, cfg, tenant,
				mcpserver.RetrieveInput{Query: "effscope", Limit: 10},
				map[string]any{"user": "u1"},
			)
			if failed {
				t.Fatal("strict retrieve with a resolved user must NOT be refused")
			}
			got := idSetOf(resp)
			if !got[u1ID] {
				t.Errorf("strict _meta.user=u1 read must see u1's memory %s, got %v", u1ID, got)
			}
			if got[u2ID] {
				t.Errorf("strict _meta.user=u1 read must NOT see u2's memory %s (isolation), got %v", u2ID, got)
			}
		})
	}
}

// TestEffectiveScope_StrictRefusesNoIdentity (AC-7 failure mode): under strict
// posture a retrieve that resolves to NO user and NO agent is REFUSED
// (ErrIdentityRequired), before any store call — the resolver never lets an
// omitted identity fall back to a tenant-wide read.
func TestEffectiveScope_StrictRefusesNoIdentity(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("effscope-refuse-" + driver)

			// No _meta identity and no user_id arg ⇒ nothing resolves ⇒ strict refuses.
			_, failed := retrieveMCPStrict(t, cfg, tenant,
				mcpserver.RetrieveInput{Query: "anything", Limit: 10},
				nil,
			)
			if !failed {
				t.Error("strict posture must REFUSE a retrieve that resolves to no user and no agent (ErrIdentityRequired)")
			}
		})
	}
}
