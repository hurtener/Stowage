package causal

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register sqlite driver
)

func openStore(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, config.StoreConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "c.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	return st
}

func mem(t *testing.T, st store.Store, scope identity.Scope, id, status string) {
	t.Helper()
	if err := st.Memories().Insert(context.Background(), scope, store.Memory{
		ID: id, Kind: "decision", Content: "content of " + id, Context: "ctx", Status: status,
		Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func link(t *testing.T, st store.Store, scope identity.Scope, id, from, to, typ string) {
	t.Helper()
	if err := st.Memories().InsertLinks(context.Background(), scope, []store.Link{{
		ID: id, TenantID: scope.Tenant, FromMemory: from, ToMemory: to, Type: typ, Source: "inferred", Confidence: 0.9, CreatedAt: 1,
	}}); err != nil {
		t.Fatalf("link %s: %v", id, err)
	}
}

// Graph: A led_to B led_to C ; D caused_by B (i.e. B caused D) ; B contradicts E (non-causal, ignored).
func seedGraph(t *testing.T, st store.Store, scope identity.Scope) {
	for _, id := range []string{"A", "B", "C", "D", "E"} {
		mem(t, st, scope, id, "active")
	}
	link(t, st, scope, "l1", "A", "B", "led_to")
	link(t, st, scope, "l2", "B", "C", "led_to")
	link(t, st, scope, "l3", "D", "B", "caused_by") // D caused_by B → B is cause of D
	link(t, st, scope, "l4", "B", "E", "contradicts")
}

func ids(g Graph) map[string]bool {
	m := map[string]bool{}
	for _, n := range g.Nodes {
		m[n.MemoryID] = true
	}
	return m
}

func TestTraverse_Backward(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "t"}
	seedGraph(t, st, scope)

	// Causes of C: B (led_to B→C), then A (led_to A→B). depth 5.
	g, err := Traverse(context.Background(), st, scope, "C", Backward, 5)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	got := ids(g)
	if !got["C"] || !got["B"] || !got["A"] {
		t.Fatalf("backward from C should reach A,B,C; got %+v", got)
	}
	if got["E"] {
		t.Error("contradicts edge must not be traversed")
	}
	// All edges canonical cause→effect.
	for _, e := range g.Edges {
		if e.Type != "led_to" && e.Type != "caused_by" {
			t.Errorf("non-causal edge surfaced: %+v", e)
		}
	}
}

func TestTraverse_Forward(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "t"}
	seedGraph(t, st, scope)

	// Effects of B: C (led_to B→C) and D (caused_by D→B ⇒ B is cause of D).
	g, err := Traverse(context.Background(), st, scope, "B", Forward, 5)
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	got := ids(g)
	if !got["B"] || !got["C"] || !got["D"] {
		t.Fatalf("forward from B should reach B,C,D; got %+v", got)
	}
	if got["A"] {
		t.Error("A is a cause of B, must not appear in forward")
	}
}

func TestTraverse_DepthLimit(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "t"}
	seedGraph(t, st, scope)

	// depth 1 backward from C: reach B only (not A).
	g, _ := Traverse(context.Background(), st, scope, "C", Backward, 1)
	got := ids(g)
	if !got["C"] || !got["B"] {
		t.Fatalf("depth1 should reach C,B; got %+v", got)
	}
	if got["A"] {
		t.Error("depth1 must not reach A (2 hops)")
	}
}

func TestTraverse_NonActiveSkipped(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "t"}
	mem(t, st, scope, "A", "active")
	mem(t, st, scope, "B", "superseded") // a superseded cause
	link(t, st, scope, "l1", "B", "A", "led_to")

	g, _ := Traverse(context.Background(), st, scope, "A", Backward, 5)
	if ids(g)["B"] {
		t.Error("superseded node must not be traversed")
	}
	if len(g.Edges) != 0 {
		t.Errorf("edge to a non-active node must be omitted, got %+v", g.Edges)
	}
}

func TestTraverse_CycleSafe(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "t"}
	mem(t, st, scope, "A", "active")
	mem(t, st, scope, "B", "active")
	link(t, st, scope, "l1", "A", "B", "led_to")
	link(t, st, scope, "l2", "B", "A", "led_to") // cycle

	g, err := Traverse(context.Background(), st, scope, "A", Both, 5)
	if err != nil {
		t.Fatalf("cycle traverse: %v", err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("cycle should visit each node once, got %d", len(g.Nodes))
	}
}

func TestTraverse_DepthClampTruncated(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "t"}
	mem(t, st, scope, "A", "active")
	// Requesting a depth above the hard cap must flag Truncated (no silent clamp).
	g, err := Traverse(context.Background(), st, scope, "A", Backward, 999)
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	if !g.Truncated {
		t.Error("depth above maxDepth should set Truncated")
	}
}

func TestTraverse_MissingRoot(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "t"}
	g, err := Traverse(context.Background(), st, scope, "nope", Backward, 5)
	if err != nil {
		t.Fatalf("missing root should not error: %v", err)
	}
	if len(g.Nodes) != 0 || g.Root != "nope" {
		t.Errorf("missing root ⇒ empty graph, got %+v", g)
	}
}

func TestTraverse_ScopeIsolation(t *testing.T) {
	st := openStore(t)
	a := identity.Scope{Tenant: "ta"}
	b := identity.Scope{Tenant: "tb"}
	seedGraph(t, st, a)
	// Tenant b sees nothing for A.
	g, err := Traverse(context.Background(), st, b, "C", Backward, 5)
	if err != nil {
		t.Fatalf("traverse: %v", err)
	}
	if len(g.Nodes) != 0 {
		t.Errorf("cross-tenant leak: %+v", ids(g))
	}
}

func TestTraverse_InvalidDirection(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "t"}
	mem(t, st, scope, "A", "active")
	if _, err := Traverse(context.Background(), st, scope, "A", Direction("sideways"), 5); err == nil {
		t.Error("expected error on invalid direction")
	}
}
