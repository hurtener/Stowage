package lifecycle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// reflectFakeGateway returns a schema-shaped reflection response referencing a
// real record id parsed from the prompt. Stand-in for the paid model.
type reflectFakeGateway struct{ calls int }

var reflectRecRe = regexp.MustCompile(`\[record (\S+)\]`)

func (f *reflectFakeGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	f.calls++
	user := ""
	if len(req.Messages) > 0 {
		user = req.Messages[0].Content
	}
	rid := ""
	if m := reflectRecRe.FindStringSubmatch(user); len(m) == 2 {
		rid = m[1]
	}
	resp := fmt.Sprintf(`{"reflections":[{"kind":"strategy","content":"retry with backoff","context":"",`+
		`"entities":["retry"],"keywords":["backoff"],"anticipated_queries":["q"],"importance":4,"confidence":0.9,`+
		`"provenance":[{"record_id":%q,"span_start":0,"span_end":1}]}]}`, rid)
	return gateway.CompleteResponse{JSON: json.RawMessage(resp)}, nil
}
func (f *reflectFakeGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (f *reflectFakeGateway) Probe(context.Context) error { return nil }
func (f *reflectFakeGateway) Close(context.Context) error { return nil }
func (f *reflectFakeGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

func drainBatches(ch <-chan pipeline.CandidateBatch) []pipeline.CandidateBatch {
	var out []pipeline.CandidateBatch
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, b)
		default:
			return out
		}
	}
}

// TestReflectSweep_EmitsAndIsIdempotent covers the lifecycle reflection sweep:
// it reads outcome-tagged records, reflects, emits candidates, and a second pass
// in the same epoch emits nothing (per-trajectory job marker).
func TestReflectSweep_EmitsAndIsIdempotent(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	sess := identity.Scope{Tenant: "rt", Project: "p", User: "u", Session: "s1"}
	base := time.Now().UnixMilli()
	if err := st.Records().Append(ctx, sess, []store.Record{
		{ID: "rec-1", BranchID: "main", Role: "tool", Content: "used a retry and it worked", Outcome: "success", OccurredAt: base - 1000, CreatedAt: base - 1000},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	reflectCh := make(chan pipeline.CandidateBatch, 16)
	gw := &reflectFakeGateway{}
	profile := lifecycle.Profile{ReflectInterval: 30 * time.Minute, ReflectBatchSize: 200, ReflectEpochEvery: 8}
	mgr := lifecycle.New(st, testLogger(), profile, make(chan pipeline.Item, 8))
	mgr.SetReflection(gw, reflectCh)

	mgr.RunForce(ctx)
	first := drainBatches(reflectCh)
	if len(first) == 0 {
		t.Fatal("reflection sweep emitted no candidate batch")
	}
	var sawStrategy bool
	for _, b := range first {
		if b.Scope.Tenant != "rt" {
			t.Errorf("batch scope tenant = %q, want rt", b.Scope.Tenant)
		}
		for _, c := range b.Candidates {
			if c.Kind == "strategy" {
				sawStrategy = true
			}
			if c.TrustSource != "llm_reflected" {
				t.Errorf("candidate trust source = %q, want llm_reflected", c.TrustSource)
			}
		}
	}
	if !sawStrategy {
		t.Error("expected a strategy candidate")
	}
	callsAfterFirst := gw.calls

	// Second pass, same epoch → marker hits → no new gateway calls, no new batch.
	mgr.RunForce(ctx)
	second := drainBatches(reflectCh)
	if len(second) != 0 {
		t.Errorf("re-reflection in the same epoch should emit nothing, got %d batches", len(second))
	}
	if gw.calls != callsAfterFirst {
		t.Errorf("re-reflection should not call the gateway again: was %d now %d", callsAfterFirst, gw.calls)
	}
}

// errGateway / emptyGateway exercise the reflectTenant error + zero-candidate
// branches.
type errGateway struct{ reflectFakeGateway }

func (errGateway) Complete(context.Context, gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	return gateway.CompleteResponse{}, fmt.Errorf("simulated gateway failure")
}

type emptyGateway struct{ reflectFakeGateway }

func (emptyGateway) Complete(context.Context, gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	return gateway.CompleteResponse{JSON: json.RawMessage(`{"reflections":[]}`)}, nil
}

func runReflectWith(t *testing.T, gw gateway.Gateway, outcome string) []pipeline.CandidateBatch {
	t.Helper()
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	sess := identity.Scope{Tenant: "rt", Project: "p", User: "u", Session: "s1"}
	now := time.Now().UnixMilli()
	if err := st.Records().Append(ctx, sess, []store.Record{
		{ID: "rr-1", BranchID: "main", Role: "tool", Content: "did a thing", Outcome: outcome, OccurredAt: now - 500, CreatedAt: now - 500},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	ch := make(chan pipeline.CandidateBatch, 8)
	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{}, make(chan pipeline.Item, 8))
	mgr.SetReflection(gw, ch)
	mgr.RunForce(ctx)
	return drainBatches(ch)
}

// TestReflectSweep_GatewayErrorContinues: a reflect gateway error is logged and
// skipped, emitting nothing (no panic, no partial batch).
func TestReflectSweep_GatewayErrorContinues(t *testing.T) {
	if b := runReflectWith(t, &errGateway{}, "failure"); len(b) != 0 {
		t.Errorf("gateway error should emit no batch, got %d", len(b))
	}
}

// TestReflectSweep_ZeroReflectionsNoBatch: an empty reflections array emits nothing.
func TestReflectSweep_ZeroReflectionsNoBatch(t *testing.T) {
	if b := runReflectWith(t, &emptyGateway{}, "success"); len(b) != 0 {
		t.Errorf("zero reflections should emit no batch, got %d", len(b))
	}
}

// TestReflectSweep_TenantWithNoOutcomeRecords: a tenant present in Tenants() (it
// has a memory) but with no outcome-tagged records is skipped cleanly.
func TestReflectSweep_TenantWithNoOutcomeRecords(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "rt-empty"}
	insertMemory(t, st, scope, store.Memory{Content: "a plain memory", CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli()})
	ch := make(chan pipeline.CandidateBatch, 8)
	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{}, make(chan pipeline.Item, 8))
	mgr.SetReflection(&reflectFakeGateway{}, ch)
	mgr.RunForce(ctx)
	if b := drainBatches(ch); len(b) != 0 {
		t.Errorf("tenant with no outcome records should emit nothing, got %d", len(b))
	}
}

// TestReflectSweep_StopHaltsEmit covers the emit stop-path: when the manager is
// stopping, an in-flight emit selects stopCh and the sweep returns without
// blocking on an unread channel.
func TestReflectSweep_StopHaltsEmit(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	sess := identity.Scope{Tenant: "rt-stop", Project: "p", User: "u", Session: "s1"}
	now := time.Now().UnixMilli()
	_ = st.Records().Append(ctx, sess, []store.Record{
		{ID: "rs-1", BranchID: "main", Role: "tool", Content: "x", Outcome: "success", OccurredAt: now - 500, CreatedAt: now - 500},
	})
	ch := make(chan pipeline.CandidateBatch) // unbuffered, never read
	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{}, make(chan pipeline.Item, 8))
	mgr.SetReflection(&reflectFakeGateway{}, ch)
	mgr.Stop() // closes stopCh before the run; the emit select must pick it
	mgr.RunForce(ctx)
	// Reaching here without deadlock/panic is the assertion.
}

// faultyStore wraps a real store and injects errors on the methods the reflection
// sweep depends on, to prove it degrades gracefully (§17 failure-mode coverage).
type faultyStore struct {
	store.Store
	failTenants bool
	failList    bool
}

func (f *faultyStore) Tenants(ctx context.Context) ([]string, error) {
	if f.failTenants {
		return nil, fmt.Errorf("injected Tenants failure")
	}
	return f.Store.Tenants(ctx)
}
func (f *faultyStore) Records() store.RecordStore {
	if f.failList {
		return faultyRecords{f.Store.Records()}
	}
	return f.Store.Records()
}

type faultyRecords struct{ store.RecordStore }

func (r faultyRecords) ListByOutcome(context.Context, identity.Scope, []string, int64, int) ([]store.Record, error) {
	return nil, fmt.Errorf("injected ListByOutcome failure")
}

// TestReflectSweep_StoreErrorsDegrade: a Tenants() error aborts the sweep, and a
// ListByOutcome() error skips that tenant — neither panics nor emits.
func TestReflectSweep_StoreErrorsDegrade(t *testing.T) {
	real, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	sess := identity.Scope{Tenant: "rt-faulty", Project: "p", User: "u", Session: "s1"}
	_ = real.Records().Append(ctx, sess, []store.Record{
		{ID: "rf-1", BranchID: "main", Role: "tool", Content: "x", Outcome: "success", OccurredAt: time.Now().UnixMilli() - 500, CreatedAt: time.Now().UnixMilli() - 500},
	})

	for _, tc := range []struct {
		name        string
		failTenants bool
		failList    bool
	}{
		{"tenants-error", true, false},
		{"list-error", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := &faultyStore{Store: real, failTenants: tc.failTenants, failList: tc.failList}
			ch := make(chan pipeline.CandidateBatch, 8)
			mgr := lifecycle.New(fs, testLogger(), lifecycle.Profile{}, make(chan pipeline.Item, 8))
			mgr.SetReflection(&reflectFakeGateway{}, ch)
			mgr.RunForce(ctx) // must not panic
			if b := drainBatches(ch); len(b) != 0 {
				t.Errorf("store error should emit nothing, got %d batches", len(b))
			}
		})
	}
}

// TestReflectSweep_DisabledWithoutWiring confirms the sweep is a no-op when
// SetReflection has not wired a gateway (single-user profiles / non-opted callers).
func TestReflectSweep_DisabledWithoutWiring(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	sess := identity.Scope{Tenant: "rt2", Project: "p", User: "u", Session: "s1"}
	_ = st.Records().Append(ctx, sess, []store.Record{
		{ID: "r-x", BranchID: "main", Role: "tool", Content: "x", Outcome: "failure", OccurredAt: time.Now().UnixMilli() - 500, CreatedAt: time.Now().UnixMilli() - 500},
	})
	// No SetReflection → reflection disabled. RunForce must not panic.
	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{}, make(chan pipeline.Item, 8))
	mgr.RunForce(ctx) // must complete without emitting / panicking
}
