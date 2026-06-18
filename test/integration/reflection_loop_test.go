package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/playbook"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/reflect"
	"github.com/hurtener/stowage/internal/store"
)

// fakeReflectGateway returns a schema-shaped reflection response whose kind tracks
// the trajectory outcome and whose provenance references a REAL record id parsed
// from the prompt (so the P1 provenance-FK and validity checks pass). It is a
// gateway.Gateway stand-in for the paid model in this deterministic test.
type fakeReflectGateway struct{ calls int }

var recordIDRe = regexp.MustCompile(`\[record (\S+)\]`)

func (f *fakeReflectGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	f.calls++
	user := ""
	if len(req.Messages) > 0 {
		user = req.Messages[0].Content
	}
	rid := ""
	if m := recordIDRe.FindStringSubmatch(user); len(m) == 2 {
		rid = m[1]
	}
	// Distinct entities/keywords per kind so the two reflection candidates are NOT
	// structural neighbors — reconcile fast-adds both (no LLM decision call, which
	// is reconcile's separately-tested path; this fake only models reflection).
	kind := "strategy"
	content := "On transient errors, retry with exponential backoff."
	entities, keywords := `["retry-logic"]`, `["backoff"]`
	if strings.Contains(user, "FAILURE") {
		kind = "failure_mode"
		content = "Running a migration without acquiring a lock can deadlock; take the lock first."
		entities, keywords = `["schema-migration"]`, `["deadlock"]`
	}
	resp := fmt.Sprintf(`{"reflections":[{"kind":%q,"content":%q,"context":"",`+
		`"entities":%s,"keywords":%s,"anticipated_queries":["how to avoid deadlock"],`+
		`"importance":4,"confidence":0.9,"provenance":[{"record_id":%q,"span_start":0,"span_end":1}]}]}`,
		kind, content, entities, keywords, rid)
	return gateway.CompleteResponse{JSON: json.RawMessage(resp)}, nil
}
func (f *fakeReflectGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (f *fakeReflectGateway) Probe(context.Context) error { return nil }
func (f *fakeReflectGateway) Close(context.Context) error { return nil }
func (f *fakeReflectGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

// TestReflectionLoop_FleetLoop is the Phase 19 fleet-loop integration test
// (D-077, CLAUDE.md §17): real sqlite store + reflection sweep + reconcile core →
// strategy/failure_mode memories surfaced by the playbook. Proves the write-side
// end to end, scope isolation, and re-reflection idempotency.
func TestReflectionLoop_FleetLoop(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 4}))

	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = filepath.Join(dir, "reflect.db")
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	defer func() { _ = st.Close(ctx) }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	scope := identity.Scope{Tenant: "t-reflect"}
	other := identity.Scope{Tenant: "t-other"}
	base := time.Now().UnixMilli()

	// session_id is scope-level: each task/session is ingested under its own
	// session-scoped scope. A success trajectory (s1) and a failure trajectory
	// (s2); a tenant-level ListByOutcome (the sweep's query) sees both. Fixed
	// record IDs so the fake gateway can reference real records in provenance.
	s1 := identity.Scope{Tenant: "t-reflect", Project: "p", User: "u", Session: "s1"}
	s2 := identity.Scope{Tenant: "t-reflect", Project: "p", User: "u", Session: "s2"}
	if err := st.Records().Append(ctx, s1, []store.Record{
		{ID: "r-s1-a", BranchID: "main", Role: "tool", Content: "used a retry with exponential backoff and the call succeeded", Outcome: "success", OccurredAt: base - 2000, CreatedAt: base - 2000},
	}); err != nil {
		t.Fatalf("append s1: %v", err)
	}
	if err := st.Records().Append(ctx, s2, []store.Record{
		{ID: "r-s2-a", BranchID: "main", Role: "tool", Content: "ran the migration without acquiring a lock and it deadlocked", Outcome: "failure", OccurredAt: base - 1000, CreatedAt: base - 1000},
	}); err != nil {
		t.Fatalf("append s2: %v", err)
	}
	// A record in another tenant — must never appear in t-reflect's reflections.
	otherSess := identity.Scope{Tenant: "t-other", Project: "p", User: "u", Session: "so"}
	if err := st.Records().Append(ctx, otherSess, []store.Record{
		{ID: "r-o-a", BranchID: "main", Role: "tool", Content: "did something", Outcome: "success", OccurredAt: base - 1500, CreatedAt: base - 1500},
	}); err != nil {
		t.Fatalf("append other: %v", err)
	}

	// A pre-existing FACT sharing the strategy candidate's entity ("retry-logic").
	// With the D-077 #5 kind filter, the reflection candidate must NOT find/supersede
	// it — proving cross-kind isolation end-to-end (AC-6).
	factID := "mem-fact-retry"
	if err := st.Memories().Commit(ctx, scope, store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: factID, Kind: "fact", Content: "the retry-logic module lives in pkg/retry",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
			CreatedAt: base, UpdatedAt: base,
		},
		Entities: []string{"retry-logic"},
		Keywords: []string{"backoff"},
	}); err != nil {
		t.Fatalf("commit fact: %v", err)
	}

	gw := &fakeReflectGateway{}

	// reconcile core fed directly by the reflection channel (no extract stage in
	// this test; boot's fan-in merges them in production).
	reflectCh := make(chan pipeline.CandidateBatch, 16)
	rec := reconcile.New(st.Memories(), st.Ops(), st.Events(), gw, log, reflectCh)
	rec.Start(ctx)

	// Lifecycle manager with reflection wired (as the fleet profile does in boot).
	ingest := make(chan pipeline.Item, 64)
	lc := lifecycle.New(st, log, lifecycle.DefaultProfile(), ingest)
	lc.SetReflection(gw, reflectCh)

	// Force the reflection sweep TWICE — the second pass must be idempotent (same
	// epoch markers hit; reconcile content-hash would also dedupe).
	lc.RunForce(ctx)
	lc.RunForce(ctx)

	// Drain: stop producers, close the channel, drain reconcile.
	lc.Stop()
	close(reflectCh)
	rec.Drain(ctx)

	// --- assertions ---
	mems, err := st.Memories().ListByKinds(ctx, scope, pipeline.ReflectionKindList())
	if err != nil {
		t.Fatalf("ListByKinds: %v", err)
	}
	if len(mems) != 2 {
		t.Fatalf("expected exactly 2 reflection memories (idempotent across 2 sweeps), got %d", len(mems))
	}
	var haveStrategy, haveFailure bool
	for _, m := range mems {
		if m.TrustSource != reflect.ReflectionTrustSource {
			t.Errorf("memory %s trust source = %q, want %q", m.ID, m.TrustSource, reflect.ReflectionTrustSource)
		}
		switch m.Kind {
		case "strategy":
			haveStrategy = true
		case "failure_mode":
			haveFailure = true
		}
	}
	if !haveStrategy || !haveFailure {
		t.Errorf("expected both a strategy and a failure_mode memory, got strategy=%v failure=%v", haveStrategy, haveFailure)
	}

	// Scope isolation: the sweep reflects EACH tenant's own records in isolation.
	// t-other has exactly its own single reflection (from r-o-a); if t-reflect's
	// trajectories had leaked across the tenant boundary, these counts would not
	// hold (t-reflect would be 3, or t-other > 1). The store's scope-parameterized
	// ListByOutcome (conformance-tested) is what enforces this.
	otherMems, err := st.Memories().ListByKinds(ctx, other, pipeline.ReflectionKindList())
	if err != nil {
		t.Fatalf("ListByKinds(other): %v", err)
	}
	if len(otherMems) != 1 {
		t.Errorf("scope isolation: t-other should have exactly its own 1 reflection, got %d", len(otherMems))
	}

	// The playbook (the read side, D-072) surfaces them.
	pb, err := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 4000})
	if err != nil {
		t.Fatalf("playbook assemble: %v", err)
	}
	var pbStrategy, pbFailure bool
	for _, sec := range pb.Sections {
		for _, it := range sec.Items {
			if it.Kind == "strategy" {
				pbStrategy = true
			}
			if it.Kind == "failure_mode" {
				pbFailure = true
			}
		}
	}
	if !pbStrategy || !pbFailure {
		t.Errorf("playbook missing reflection sections: strategy=%v failure=%v", pbStrategy, pbFailure)
	}

	// AC-6 cross-kind isolation: the pre-existing fact (shared entity "retry-logic")
	// must still be active — the strategy candidate could not supersede it because
	// reflection reconciliation restricts neighbors to reflection kinds (D-077 #5).
	fact, err := st.Memories().Get(ctx, scope, factID)
	if err != nil {
		t.Fatalf("get fact: %v", err)
	}
	if fact.Status != "active" {
		t.Errorf("AC-6: pre-existing fact was modified by reflection (status=%q) — cross-kind isolation breached", fact.Status)
	}
}
