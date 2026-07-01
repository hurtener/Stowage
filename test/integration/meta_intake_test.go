// meta_intake_test.go proves the ae2 additive _meta identity intake (D-137/
// D-138) end to end over real drivers (§17 — ae2's Deps name ae1/
// internal/identity and it consumes the dockyard v1.8 _meta seam):
// _meta.user narrows a memory_retrieve read to its own scope, a no-_meta read
// stays tenant-wide exactly as it does today (the additivity guarantee, AC-5),
// and a mismatched _meta.tenant fails closed (the failure mode, D-138). Runs
// under -race. Postgres subtests are gated on STOWAGE_TEST_PG_DSN, the
// established pattern (pgstore_test.go, retrieve_topicfilter_test.go,
// agentfilter_test.go) — sqlite always runs.
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

// TestMetaIntake_UserNarrowsRetrieve is AC-1: a memory_retrieve call carrying
// _meta.user narrows the read to that user's own scope, over real sqlite and
// postgres drivers — no new store predicate, the store already filters a
// populated Scope.User (D-125).
func TestMetaIntake_UserNarrowsRetrieve(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("metaintake-narrow-" + driver)

			ctx := context.Background()
			st, err := store.Open(ctx, cfg.Store)
			if err != nil {
				t.Fatalf("open store for seeding: %v", err)
			}
			if err := st.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			u1Scope := identity.Scope{Tenant: tenant, User: "u1"}
			u2Scope := identity.Scope{Tenant: tenant, User: "u2"}
			u1ID, u2ID := ulid.Make().String(), ulid.Make().String()
			seedLeanReadMemory(t, st, u1Scope, u1ID, "u1 memory qzxrm", "")
			seedLeanReadMemory(t, st, u2Scope, u2ID, "u2 memory qzxrm", "")
			_ = st.Close(ctx)

			resp := retrieveMCPWithMeta(t, cfg, tenant,
				mcpserver.RetrieveInput{Query: "qzxrm", Limit: 10},
				map[string]any{"user": "u1"},
			)
			got := idSetOf(resp)
			if !got[u1ID] {
				t.Errorf("expected u1's memory %s in a _meta.user=u1 read, got %v", u1ID, got)
			}
			if got[u2ID] {
				t.Errorf("_meta.user=u1 must not see u2's memory %s, got %v", u2ID, got)
			}
		})
	}
}

// TestMetaIntake_NoMetaIsTenantWide is AC-5 (additivity): a memory_retrieve
// call injecting NO _meta identity resolves tenant-wide, byte-identical to
// today (no user_id arg either) — proving the zero-value metaIdentity is a
// complete no-op.
func TestMetaIntake_NoMetaIsTenantWide(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("metaintake-nometa-" + driver)

			ctx := context.Background()
			st, err := store.Open(ctx, cfg.Store)
			if err != nil {
				t.Fatalf("open store for seeding: %v", err)
			}
			if err := st.Migrate(ctx); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			u1Scope := identity.Scope{Tenant: tenant, User: "u1"}
			u2Scope := identity.Scope{Tenant: tenant, User: "u2"}
			u1ID, u2ID := ulid.Make().String(), ulid.Make().String()
			seedLeanReadMemory(t, st, u1Scope, u1ID, "u1 memory qzxrn", "")
			seedLeanReadMemory(t, st, u2Scope, u2ID, "u2 memory qzxrn", "")
			_ = st.Close(ctx)

			resp := retrieveMCPWithMeta(t, cfg, tenant,
				mcpserver.RetrieveInput{Query: "qzxrn", Limit: 10},
				nil, // no _meta injected at all
			)
			got := idSetOf(resp)
			if !got[u1ID] || !got[u2ID] {
				t.Errorf("expected BOTH users' memories on a no-_meta tenant-wide read, got %v", got)
			}
		})
	}
}

// retrieveMCPIsFailure calls memory_retrieve with an optional _meta map and
// reports whether it failed — either as a protocol-level CallTool error or an
// IsError tool result — mirroring browseMCPIsFailure's failure-mode check
// pattern.
func retrieveMCPIsFailure(t *testing.T, cfg config.Config, tenant string, in mcpserver.RetrieveInput, meta map[string]any) bool {
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
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "metaintake-client-err", Version: "0.0.0"}, nil)
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
	return cerr != nil || (res != nil && res.IsError)
}

// TestMetaIntake_TenantMismatchFailureMode is the required failure mode: a
// _meta.tenant that disagrees with the credential-authenticated tenant fails
// closed (D-138) on memory_retrieve, over real sqlite and postgres drivers.
func TestMetaIntake_TenantMismatchFailureMode(t *testing.T) {
	for _, driver := range leanReadDrivers() {
		t.Run(driver, func(t *testing.T) {
			cfg := leanReadConfig(t, driver)
			tenant := uniqueTenant("metaintake-mismatch-" + driver)

			if !retrieveMCPIsFailure(t, cfg, tenant,
				mcpserver.RetrieveInput{Query: "anything", Limit: 10},
				map[string]any{"tenant": "attacker-tenant"},
			) {
				t.Error("expected a _meta.tenant mismatch to fail closed (D-138), got success")
			}

			// Equal tenant is a no-op — the same call with the REAL tenant in _meta
			// must NOT fail.
			if retrieveMCPIsFailure(t, cfg, tenant,
				mcpserver.RetrieveInput{Query: "anything", Limit: 10},
				map[string]any{"tenant": tenant},
			) {
				t.Error("expected a _meta.tenant equal to the credential tenant to be a no-op")
			}
		})
	}
}
