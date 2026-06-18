package lifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// narrateGateway returns a scripted episode narrative for the narration sweep.
type narrateGateway struct {
	calls int
	err   error
}

func (g *narrateGateway) Complete(_ context.Context, _ gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	g.calls++
	if g.err != nil {
		return gateway.CompleteResponse{}, g.err
	}
	return gateway.CompleteResponse{JSON: json.RawMessage(`{"title":"Deploy episode","narrative":"The agent deployed v2 under a lock and it succeeded."}`)}, nil
}
func (g *narrateGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (g *narrateGateway) Probe(context.Context) error { return nil }
func (g *narrateGateway) Close(context.Context) error { return nil }
func (g *narrateGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

func episodeManager(t *testing.T, st store.Store, gw gateway.Gateway) *lifecycle.Manager {
	t.Helper()
	prof := lifecycle.Profile{
		EpisodeDetectInterval:  15 * time.Minute,
		EpisodeNarrateInterval: 15 * time.Minute,
		EpisodeIdleWindow:      time.Second, // records older than 1s count as a closed session
		EpisodeBatchSize:       100,
	}
	mgr := lifecycle.New(st, testLogger(), prof, make(chan pipeline.Item, 8))
	mgr.SetEpisodes(gw)
	return mgr
}

// TestEpisodeSweeps_DetectNarrate covers detect → narrate end to end and asserts
// idempotency across a second RunForce.
func TestEpisodeSweeps_DetectNarrate(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	scope := identity.Scope{Tenant: "ep-t", Project: "p", User: "u", Session: "s1"}
	old := time.Now().Add(-10 * time.Second).UnixMilli()
	if err := st.Records().Append(ctx, scope, []store.Record{
		{ID: "er-1", BranchID: "main", Role: "user", Content: "deploy the billing service", OccurredAt: old, CreatedAt: old},
		{ID: "er-2", BranchID: "main", Role: "tool", Content: "deploy succeeded under lock", Outcome: "success", OccurredAt: old + 100, CreatedAt: old + 100},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	gw := &narrateGateway{}
	mgr := episodeManager(t, st, gw)
	mgr.RunForce(ctx)
	mgr.RunForce(ctx) // idempotent second pass

	tenant := identity.Scope{Tenant: "ep-t"}
	ep, err := st.Episodes().GetEpisodeBySession(ctx, tenant, "s1")
	if err != nil {
		t.Fatalf("expected an episode for the session: %v", err)
	}
	if ep.Status != "closed" || ep.Outcome != "success" {
		t.Errorf("episode wrong: %+v", ep)
	}
	if ep.NarrativeMemoryID == "" || ep.Title != "Deploy episode" {
		t.Errorf("episode not narrated: %+v", ep)
	}
	// The narrative memory exists, linked to the episode.
	narr, err := st.Memories().Get(ctx, tenant, ep.NarrativeMemoryID)
	if err != nil {
		t.Fatalf("get narrative memory: %v", err)
	}
	if narr.Kind != "narrative" || narr.EpisodeID != ep.ID || narr.TrustSource != "episodic" {
		t.Errorf("narrative memory wrong: %+v", narr)
	}
	// Idempotent: exactly one episode, one narration call.
	if gw.calls != 1 {
		t.Errorf("expected exactly 1 narration call across 2 sweeps, got %d", gw.calls)
	}
	eps, _, _ := st.Episodes().ListEpisodes(ctx, tenant, 10, "")
	if len(eps) != 1 {
		t.Errorf("expected 1 episode, got %d", len(eps))
	}
}

// TestEpisodeSweeps_OpenSessionNotDetected: a session with recent activity (not
// idle) is NOT episode-d yet.
func TestEpisodeSweeps_OpenSessionNotDetected(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "ep-open", Project: "p", User: "u", Session: "s1"}
	now := time.Now().UnixMilli()
	_ = st.Records().Append(ctx, scope, []store.Record{{ID: "o-1", BranchID: "main", Role: "user", Content: "hi", OccurredAt: now, CreatedAt: now}})
	episodeManager(t, st, &narrateGateway{}).RunForce(ctx)
	if _, err := st.Episodes().GetEpisodeBySession(ctx, identity.Scope{Tenant: "ep-open"}, "s1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an open (non-idle) session should not be detected yet, got %v", err)
	}
}

// TestEpisodeSweeps_DisabledWithoutWiring: no SetEpisodes → no episode work, no panic.
func TestEpisodeSweeps_DisabledWithoutWiring(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "ep-off", Project: "p", User: "u", Session: "s1"}
	old := time.Now().Add(-time.Hour).UnixMilli()
	_ = st.Records().Append(ctx, scope, []store.Record{{ID: "x-1", BranchID: "main", Role: "user", Content: "x", OccurredAt: old, CreatedAt: old}})
	mgr := lifecycle.New(st, testLogger(), lifecycle.Profile{}, make(chan pipeline.Item, 8))
	mgr.RunForce(ctx) // episodes disabled → no panic, no episode
	if _, err := st.Episodes().GetEpisodeBySession(ctx, identity.Scope{Tenant: "ep-off"}, "s1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("episodes disabled should create nothing, got %v", err)
	}
}

// episodeFaultyStore fails Tenants to exercise the detect sweep's error branch.
type episodeFaultyStore struct{ store.Store }

func (f *episodeFaultyStore) Tenants(context.Context) ([]string, error) {
	return nil, errors.New("injected Tenants failure")
}

// TestEpisodeSweeps_DetectTenantsError: a Tenants() error aborts detect cleanly.
func TestEpisodeSweeps_DetectTenantsError(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	fs := &episodeFaultyStore{Store: st}
	mgr := episodeManager(t, fs, &narrateGateway{})
	mgr.RunForce(context.Background()) // must not panic; detect aborts on the error
}

// TestEpisodeSweeps_NarrateErrorRetries: a gateway error during narration leaves
// the episode un-narrated (retried next sweep), no panic, no partial commit.
func TestEpisodeSweeps_NarrateErrorRetries(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "ep-err", Project: "p", User: "u", Session: "s1"}
	old := time.Now().Add(-10 * time.Second).UnixMilli()
	if err := st.Records().Append(ctx, scope, []store.Record{
		{ID: "ee-1", BranchID: "main", Role: "tool", Content: "did a thing", Outcome: "failure", OccurredAt: old, CreatedAt: old},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	gw := &narrateGateway{err: errors.New("simulated gateway failure")}
	episodeManager(t, st, gw).RunForce(ctx)

	tenant := identity.Scope{Tenant: "ep-err"}
	ep, err := st.Episodes().GetEpisodeBySession(ctx, tenant, "s1")
	if err != nil {
		t.Fatalf("episode should still be detected: %v", err)
	}
	if ep.NarrativeMemoryID != "" {
		t.Errorf("episode should be un-narrated after a gateway error, got narrative %q", ep.NarrativeMemoryID)
	}
	// No narrative memory was committed.
	narr, _, _ := st.Memories().ListByStatus(ctx, tenant, "active", 50, "")
	for _, m := range narr {
		if m.Kind == "narrative" {
			t.Errorf("a narrative memory was committed despite the gateway error: %s", m.ID)
		}
	}
}

// TestEpisodeSweeps_DuplicateNarrativeRelinks proves the idempotent narration
// recovery (D-079): two sessions whose narratives are identical commit ONE
// narrative memory, and BOTH episodes are linked to it — the second episode is
// not stranded un-narrated (the ErrDuplicateContent → GetByContentHash → link path).
func TestEpisodeSweeps_DuplicateNarrativeRelinks(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	old := time.Now().Add(-10 * time.Second).UnixMilli()
	// Same user, two sessions; the fake gateway returns the same narrative for both.
	for _, sess := range []string{"s1", "s2"} {
		scope := identity.Scope{Tenant: "ep-dup", Project: "p", User: "u", Session: sess}
		if err := st.Records().Append(ctx, scope, []store.Record{
			{ID: "d-" + sess, BranchID: "main", Role: "tool", Content: "did " + sess, Outcome: "success", OccurredAt: old, CreatedAt: old},
		}); err != nil {
			t.Fatalf("append %s: %v", sess, err)
		}
	}
	episodeManager(t, st, &narrateGateway{}).RunForce(ctx)

	user := identity.Scope{Tenant: "ep-dup", Project: "p", User: "u"}
	tenant := identity.Scope{Tenant: "ep-dup"}
	e1, err1 := st.Episodes().GetEpisodeBySession(ctx, user, "s1")
	e2, err2 := st.Episodes().GetEpisodeBySession(ctx, user, "s2")
	if err1 != nil || err2 != nil {
		t.Fatalf("episodes: %v / %v", err1, err2)
	}
	if e1.NarrativeMemoryID == "" || e2.NarrativeMemoryID == "" {
		t.Fatalf("both episodes must be narrated (recovery), got %q / %q", e1.NarrativeMemoryID, e2.NarrativeMemoryID)
	}
	if e1.NarrativeMemoryID != e2.NarrativeMemoryID {
		t.Errorf("identical narratives should share one memory, got %q / %q", e1.NarrativeMemoryID, e2.NarrativeMemoryID)
	}
	// Exactly one narrative memory exists.
	mems, _, _ := st.Memories().ListByStatus(ctx, tenant, "active", 50, "")
	n := 0
	for _, m := range mems {
		if m.Kind == "narrative" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected exactly 1 narrative memory, got %d", n)
	}
}
