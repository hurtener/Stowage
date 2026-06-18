package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// threadManager builds a Manager with threading enabled + episodes wired (the sweep
// is gateway-free but threadingOn requires episodesEnabled).
func threadManager(t *testing.T, st store.Store, minOverlap float64) *lifecycle.Manager {
	t.Helper()
	prof := lifecycle.Profile{
		EpisodeDetectInterval:  15 * time.Minute,
		EpisodeNarrateInterval: 15 * time.Minute,
		EpisodeIdleWindow:      time.Second,
		EpisodeBatchSize:       100,
		ThreadingEnabled:       true,
		ThreadInterval:         30 * time.Minute,
		ThreadMinOverlap:       minOverlap,
		ThreadWindow:           30 * 24 * time.Hour,
		ThreadBatchSize:        50,
	}
	mgr := lifecycle.New(st, testLogger(), prof, make(chan pipeline.Item, 8))
	mgr.SetEpisodes(&narrateGateway{}) // enables episodesEnabled; threading itself is gateway-free
	return mgr
}

// seedNarratedEpisode inserts a narrative memory + a closed narrated episode.
func seedNarratedEpisode(t *testing.T, st store.Store, scope identity.Scope, epID, narrID, content string, start, end int64) {
	t.Helper()
	ctx := context.Background()
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: narrID, Kind: "narrative", Content: content, Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "episodic", Stability: 1.0,
		EpisodeID: epID, CreatedAt: start, UpdatedAt: start,
	}); err != nil {
		t.Fatalf("insert narrative %s: %v", narrID, err)
	}
	if err := st.Episodes().CreateEpisode(ctx, scope, store.Episode{
		ID: epID, SessionID: epID + "-sess", Title: "T", Status: "closed", Outcome: "success",
		StartedAt: start, EndedAt: end, NarrativeMemoryID: narrID, CreatedAt: start, UpdatedAt: start,
	}); err != nil {
		t.Fatalf("create episode %s: %v", epID, err)
	}
}

func relatesLinks(t *testing.T, st store.Store, scope identity.Scope, narrID string) []store.Link {
	t.Helper()
	links, err := st.Memories().ListLinks(context.Background(), scope, narrID, "")
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	to, err := st.Memories().ListLinks(context.Background(), scope, "", narrID)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	var out []store.Link
	for _, l := range append(links, to...) {
		if l.Type == "relates_to" {
			out = append(out, l)
		}
	}
	return out
}

func TestThreadingSweep_LinksOverlapping(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "th-t", Project: "p", User: "u"}
	base := time.Now().Add(-48 * time.Hour).UnixMilli()

	// Two episodes with strongly overlapping narratives, within the window.
	seedNarratedEpisode(t, st, scope, "ep-A", "narr-A", "migrating the billing service to the new gateway under a lock", base, base+1000)
	seedNarratedEpisode(t, st, scope, "ep-B", "narr-B", "migrating the billing service to the new gateway with a lock", base+2000, base+3000)
	// A third, unrelated narrative — must not thread.
	seedNarratedEpisode(t, st, scope, "ep-C", "narr-C", "debugging a flaky frontend test about button colors", base+4000, base+5000)

	mgr := threadManager(t, st, 0.3)
	mgr.RunForce(ctx)
	mgr.RunForce(ctx) // idempotent

	tenant := identity.Scope{Tenant: "th-t"}
	ab := relatesLinks(t, st, tenant, "narr-A")
	if len(ab) != 1 {
		t.Fatalf("expected exactly 1 relates_to edge on narr-A (idempotent), got %d: %+v", len(ab), ab)
	}
	other := ab[0].FromMemory
	if other == "narr-A" {
		other = ab[0].ToMemory
	}
	if other != "narr-B" {
		t.Errorf("narr-A should thread to narr-B, got %q", other)
	}
	if ab[0].Source != "inferred" {
		t.Errorf("threaded edge source should be inferred, got %q", ab[0].Source)
	}
	// narr-C must not be threaded to anything.
	if c := relatesLinks(t, st, tenant, "narr-C"); len(c) != 0 {
		t.Errorf("unrelated narrative should not thread, got %+v", c)
	}
}

func TestThreadingSweep_DisabledByDefault(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "th-off", Project: "p", User: "u"}
	base := time.Now().Add(-48 * time.Hour).UnixMilli()
	seedNarratedEpisode(t, st, scope, "ep-A", "narr-A", "same content here exactly", base, base+1000)
	seedNarratedEpisode(t, st, scope, "ep-B", "narr-B", "same content here exactly", base+2000, base+3000)

	// episodeManager has ThreadingEnabled=false (default).
	episodeManager(t, st, &narrateGateway{}).RunForce(ctx)

	if l := relatesLinks(t, st, identity.Scope{Tenant: "th-off"}, "narr-A"); len(l) != 0 {
		t.Errorf("threading disabled should create no edges, got %+v", l)
	}
}

func TestThreadingSweep_CrossUserNotThreaded(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	base := time.Now().Add(-48 * time.Hour).UnixMilli()
	// Same tenant, DIFFERENT users — must never thread (P3).
	seedNarratedEpisode(t, st, identity.Scope{Tenant: "th-x", Project: "p", User: "u1"}, "ep-A", "narr-A", "identical narrative content for both", base, base+1000)
	seedNarratedEpisode(t, st, identity.Scope{Tenant: "th-x", Project: "p", User: "u2"}, "ep-B", "narr-B", "identical narrative content for both", base+2000, base+3000)

	threadManager(t, st, 0.3).RunForce(ctx)

	if l := relatesLinks(t, st, identity.Scope{Tenant: "th-x"}, "narr-A"); len(l) != 0 {
		t.Errorf("cross-user episodes must not thread, got %+v", l)
	}
}

// threadFaultyStore fails Tenants to exercise the threading sweep's error branch.
type threadFaultyStore struct{ store.Store }

func (f *threadFaultyStore) Tenants(context.Context) ([]string, error) {
	return nil, errThreadInjected
}

var errThreadInjected = &threadErr{}

type threadErr struct{}

func (*threadErr) Error() string { return "injected Tenants failure" }

// TestThreadingSweep_TenantsError: a Tenants() error aborts the sweep cleanly.
func TestThreadingSweep_TenantsError(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	fs := &threadFaultyStore{Store: st}
	prof := lifecycle.Profile{
		EpisodeDetectInterval: 15 * time.Minute,
		EpisodeIdleWindow:     time.Second,
		EpisodeBatchSize:      100,
		ThreadingEnabled:      true,
		ThreadInterval:        30 * time.Minute,
		ThreadMinOverlap:      0.3,
		ThreadWindow:          30 * 24 * time.Hour,
		ThreadBatchSize:       50,
	}
	mgr := lifecycle.New(fs, testLogger(), prof, make(chan pipeline.Item, 8))
	mgr.SetEpisodes(&narrateGateway{})
	mgr.RunForce(context.Background()) // must not panic despite the Tenants error
}

// TestThreadingSweep_EmptyNarrativesNoThread: degenerate/empty narratives must never
// thread (M1 guard — word-set floor), even though both are "active".
func TestThreadingSweep_EmptyNarrativesNoThread(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "th-e", Project: "p", User: "u"}
	base := time.Now().Add(-48 * time.Hour).UnixMilli()
	seedNarratedEpisode(t, st, scope, "ep-A", "narr-A", "", base, base+1000)
	seedNarratedEpisode(t, st, scope, "ep-B", "narr-B", "  ", base+2000, base+3000)

	threadManager(t, st, 0.3).RunForce(ctx)

	if l := relatesLinks(t, st, identity.Scope{Tenant: "th-e"}, "narr-A"); len(l) != 0 {
		t.Errorf("empty/degenerate narratives must not thread, got %+v", l)
	}
}

// TestThreadingSweep_UnrelatedLongProseNoThread: two long but topically-unrelated
// prose narratives must not thread at the default threshold (M2 — word-set overlap is
// topical, unlike character-bigram Jaccard which saturates on any English prose).
func TestThreadingSweep_UnrelatedLongProseNoThread(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "th-p", Project: "p", User: "u"}
	base := time.Now().Add(-48 * time.Hour).UnixMilli()
	seedNarratedEpisode(t, st, scope, "ep-A", "narr-A",
		"The agent investigated the database connection pool exhaustion, traced it to a leaked transaction in the billing reconciler, and shipped a fix that closes the handle on every error path.", base, base+1000)
	seedNarratedEpisode(t, st, scope, "ep-B", "narr-B",
		"We redesigned the onboarding tutorial screens, rewrote the welcome copy, swapped the illustration set, and ran a usability session with five participants to validate the new flow.", base+2000, base+3000)

	threadManager(t, st, 0.3).RunForce(ctx)

	if l := relatesLinks(t, st, identity.Scope{Tenant: "th-p"}, "narr-A"); len(l) != 0 {
		t.Errorf("unrelated long prose must not thread at default threshold, got %+v", l)
	}
}

func TestThreadingSweep_OutOfWindow(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "th-w", Project: "p", User: "u"}
	base := time.Now().Add(-200 * 24 * time.Hour).UnixMilli()
	// Two overlapping narratives but ~100 days apart (> 30-day window).
	seedNarratedEpisode(t, st, scope, "ep-A", "narr-A", "migrating the billing service under a lock", base, base+1000)
	seedNarratedEpisode(t, st, scope, "ep-B", "narr-B", "migrating the billing service under a lock", base+100*24*3600*1000, base+100*24*3600*1000+1000)

	threadManager(t, st, 0.3).RunForce(ctx)

	if l := relatesLinks(t, st, identity.Scope{Tenant: "th-w"}, "narr-A"); len(l) != 0 {
		t.Errorf("out-of-window episodes must not thread, got %+v", l)
	}
}
