package traces

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
	st, err := store.Open(ctx, config.StoreConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "t.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	return st
}

func TestReconstruct_FullChain(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "tr-t"}
	ctx := context.Background()
	const resp = "resp-1"

	// A verbatim record + a memory derived from it (with provenance) + a link.
	if err := st.Records().Append(ctx, scope, []store.Record{{ID: "rec-1", Role: "user", Content: "Paris is the capital of France.", OccurredAt: 1, CreatedAt: 1}}); err != nil {
		t.Fatalf("append record: %v", err)
	}
	for _, m := range []store.Memory{
		{ID: "mem-1", Kind: "fact", Content: "Paris is the capital.", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1},
		{ID: "mem-2", Kind: "fact", Content: "France is in Europe.", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 2, UpdatedAt: 2},
	} {
		if err := st.Memories().Insert(ctx, scope, m); err != nil {
			t.Fatalf("insert %s: %v", m.ID, err)
		}
	}
	if err := st.Memories().AddProvenance(ctx, scope, []store.Provenance{{ID: "pv-1", MemoryID: "mem-1", RecordID: "rec-1", SpanStart: 0, SpanEnd: 26, TenantID: scope.Tenant, CreatedAt: 1}}); err != nil {
		t.Fatalf("prov: %v", err)
	}
	if err := st.Memories().InsertLinks(ctx, scope, []store.Link{{ID: "lk-1", TenantID: scope.Tenant, FromMemory: "mem-1", ToMemory: "mem-2", Type: "led_to", Source: "inferred", Confidence: 0.7, CreatedAt: 1}}); err != nil {
		t.Fatalf("link: %v", err)
	}
	// Two injections for the response (ranked).
	if err := st.Injections().Append(ctx, scope, []store.Injection{
		{ID: "inj-1", ResponseID: resp, MemoryID: "mem-1", Rank: 0, Score: 0.9, Lane: "vector", WasCited: true, CreatedAt: 1},
		{ID: "inj-2", ResponseID: resp, MemoryID: "mem-2", Rank: 1, Score: 0.5, Lane: "lexical", CreatedAt: 1},
	}); err != nil {
		t.Fatalf("inj: %v", err)
	}
	// The captured query + verdict events (keyed by response_id).
	for _, e := range []store.Event{
		{ID: "ev-q", TenantID: scope.Tenant, Type: EventRetrieveQuery, SubjectID: resp, Payload: `{"query":"capital of France?","support":"strong","degraded":false}`, CreatedAt: 1},
		{ID: "ev-v", TenantID: scope.Tenant, Type: EventVerifyVerdict, SubjectID: resp, Payload: `{"claim":"Paris is the capital","verdict":"entailed","confidence":0.9}`, CreatedAt: 2},
	} {
		if err := st.Events().Emit(ctx, scope, e); err != nil {
			t.Fatalf("emit %s: %v", e.ID, err)
		}
	}

	tr, err := Reconstruct(ctx, st, scope, resp, 12345)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if tr.ResponseID != resp || tr.GeneratedAt != 12345 {
		t.Fatalf("header wrong: %+v", tr)
	}
	if tr.Query != "capital of France?" || tr.Support != "strong" {
		t.Errorf("query/support not folded in: %+v", tr)
	}
	if len(tr.Items) != 2 || tr.Items[0].MemoryID != "mem-1" || tr.Items[1].MemoryID != "mem-2" {
		t.Fatalf("items wrong: %+v", tr.Items)
	}
	it := tr.Items[0]
	if it.Kind != "fact" || it.Rank != 0 || it.Score != 0.9 || it.Lane != "vector" || !it.WasCited {
		t.Errorf("item[0] fields wrong: %+v", it)
	}
	if len(it.Provenance) != 1 || it.Provenance[0].RecordID != "rec-1" || it.Provenance[0].Excerpt == "" {
		t.Errorf("item[0] provenance/excerpt wrong: %+v", it.Provenance)
	}
	if len(it.Links) != 1 || it.Links[0].To != "mem-2" || it.Links[0].Type != "led_to" {
		t.Errorf("item[0] links wrong: %+v", it.Links)
	}
	if len(tr.Verdicts) != 1 || tr.Verdicts[0].Verdict != "entailed" || tr.Verdicts[0].Confidence != 0.9 {
		t.Errorf("verdicts wrong: %+v", tr.Verdicts)
	}
}

func TestReconstruct_UnknownResponse(t *testing.T) {
	st := openStore(t)
	tr, err := Reconstruct(context.Background(), st, identity.Scope{Tenant: "x"}, "nope", 1)
	if err != nil {
		t.Fatalf("unknown response must not error: %v", err)
	}
	if tr.ResponseID != "nope" || len(tr.Items) != 0 || tr.Query != "" {
		t.Errorf("unknown response should be empty, got %+v", tr)
	}
	// Empty response_id ⇒ empty trace.
	if tr2, _ := Reconstruct(context.Background(), st, identity.Scope{Tenant: "x"}, "", 1); len(tr2.Items) != 0 {
		t.Errorf("empty response_id should yield empty trace")
	}
}

// TestReconstruct_DeletedMemoryStillShown: a memory injected then status→deleted still
// appears in the trace (it WAS used) carrying its deleted status — the audit shows what
// was injected, including memories later removed, rather than hiding them.
func TestReconstruct_DeletedMemoryStillShown(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "tg-t"}
	ctx := context.Background()
	if err := st.Memories().Insert(ctx, scope, store.Memory{ID: "dm", Kind: "fact", Content: "was used", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.Injections().Append(ctx, scope, []store.Injection{{ID: "i1", ResponseID: "r1", MemoryID: "dm", Rank: 0, Score: 0.5, CreatedAt: 1}}); err != nil {
		t.Fatalf("inj: %v", err)
	}
	if err := st.Memories().SetStatus(ctx, scope, "dm", "deleted", 2); err != nil {
		t.Fatalf("set status: %v", err)
	}

	tr, err := Reconstruct(ctx, st, scope, "r1", 1)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if len(tr.Items) != 1 || tr.Items[0].MemoryID != "dm" {
		t.Fatalf("the injected (now deleted) memory must still appear: %+v", tr.Items)
	}
	if tr.Items[0].Status != "deleted" {
		t.Errorf("the trace should carry the memory's current (deleted) status, got %q", tr.Items[0].Status)
	}
}

func TestReconstruct_ScopeIsolation(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	a := identity.Scope{Tenant: "ta"}
	_ = st.Memories().Insert(ctx, a, store.Memory{ID: "m", Kind: "fact", Content: "secret", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1})
	_ = st.Injections().Append(ctx, a, []store.Injection{{ID: "i", ResponseID: "r", MemoryID: "m", CreatedAt: 1}})

	// Tenant b reconstructs nothing for tenant a's response.
	tr, _ := Reconstruct(ctx, st, identity.Scope{Tenant: "tb"}, "r", 1)
	if len(tr.Items) != 0 {
		t.Errorf("cross-tenant trace leak: %+v", tr.Items)
	}
}
