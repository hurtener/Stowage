package episodes

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register the sqlite driver
)

func openStore(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, config.StoreConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "v.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	return st
}

func TestList_Get_Window(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	scope := identity.Scope{Tenant: "vt"}

	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: "m1", Kind: "narrative", Content: "the first episode story", Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "episodic", Stability: 1.0,
		CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatalf("insert narrative: %v", err)
	}
	mk := func(id, sess, title string, start, end int64, narr string) store.Episode {
		return store.Episode{ID: id, SessionID: sess, Title: title, Status: "closed", Outcome: "success", StartedAt: start, EndedAt: end, NarrativeMemoryID: narr, CreatedAt: start, UpdatedAt: start}
	}
	if err := st.Episodes().CreateEpisode(ctx, scope, mk("e1", "s1", "T1", 100, 200, "m1")); err != nil {
		t.Fatalf("create e1: %v", err)
	}
	if err := st.Episodes().CreateEpisode(ctx, scope, mk("e2", "s2", "T2", 5000, 6000, "")); err != nil {
		t.Fatalf("create e2: %v", err)
	}

	// Unfiltered: most-recent-first (e2 then e1); e1 carries its narrative.
	res, err := List(ctx, st, scope, ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Episodes) != 2 || res.Episodes[0].ID != "e2" || res.Episodes[1].ID != "e1" {
		t.Fatalf("list order wrong: %+v", res.Episodes)
	}
	if res.Episodes[1].Narrative != "the first episode story" {
		t.Errorf("e1 narrative not attached: %q", res.Episodes[1].Narrative)
	}
	if res.Episodes[0].Narrative != "" {
		t.Errorf("e2 should have no narrative, got %q", res.Episodes[0].Narrative)
	}

	// Window From=4000 → only e2 (ends 6000 ≥ 4000; e1 ends 200 < 4000).
	w1, _ := List(ctx, st, scope, ListOptions{Limit: 10, From: 4000})
	if len(w1.Episodes) != 1 || w1.Episodes[0].ID != "e2" {
		t.Errorf("From window wrong: %+v", w1.Episodes)
	}
	// Window Until=300 → only e1 (starts 100 ≤ 300; e2 starts 5000 > 300).
	w2, _ := List(ctx, st, scope, ListOptions{Limit: 10, Until: 300})
	if len(w2.Episodes) != 1 || w2.Episodes[0].ID != "e1" {
		t.Errorf("Until window wrong: %+v", w2.Episodes)
	}
	// Session filter.
	w3, _ := List(ctx, st, scope, ListOptions{Limit: 10, SessionID: "s1"})
	if len(w3.Episodes) != 1 || w3.Episodes[0].ID != "e1" {
		t.Errorf("session filter wrong: %+v", w3.Episodes)
	}

	// Get one + missing.
	got, err := Get(ctx, st, scope, "e1")
	if err != nil || got.Narrative != "the first episode story" || got.NarrativeMemoryID != "m1" {
		t.Errorf("Get e1 wrong: %+v / %v", got, err)
	}
	if _, err := Get(ctx, st, scope, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get missing = %v, want ErrNotFound", err)
	}

	// Scope isolation: another tenant sees nothing.
	iso, _ := List(ctx, st, identity.Scope{Tenant: "other"}, ListOptions{Limit: 10})
	if len(iso.Episodes) != 0 {
		t.Errorf("cross-scope leak: %d", len(iso.Episodes))
	}
}

// fakeSearcher is a deterministic NarrativeSearcher for the Similar core tests.
type fakeSearcher struct {
	ids      []string
	scores   []float64
	degraded bool
	err      error
}

func (f fakeSearcher) SimilarNarratives(_ context.Context, _ identity.Scope, _ string, _ int) ([]string, []float64, bool, error) {
	return f.ids, f.scores, f.degraded, f.err
}

func TestSimilar(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	scope := identity.Scope{Tenant: "vt"}

	mk := func(id, sess, title string, start, end int64) store.Episode {
		return store.Episode{ID: id, SessionID: sess, Title: title, Status: "closed", Outcome: "success", StartedAt: start, EndedAt: end, CreatedAt: start, UpdatedAt: start}
	}
	for _, e := range []store.Episode{mk("e1", "s1", "T1", 100, 200), mk("e2", "s2", "T2", 300, 400)} {
		if err := st.Episodes().CreateEpisode(ctx, scope, e); err != nil {
			t.Fatalf("create %s: %v", e.ID, err)
		}
	}

	// Rank preserved, scores stamped, missing episode skipped (preserving rank).
	views, degraded, err := Similar(ctx, st, fakeSearcher{
		ids:    []string{"e2", "gone", "e1"},
		scores: []float64{0.9, 0.5, 0.3},
	}, scope, "a situation", 5)
	if err != nil {
		t.Fatalf("Similar: %v", err)
	}
	if degraded {
		t.Errorf("degraded should be false")
	}
	if len(views) != 2 {
		t.Fatalf("want 2 views (missing skipped), got %d: %+v", len(views), views)
	}
	if views[0].ID != "e2" || views[0].Score != 0.9 {
		t.Errorf("view[0] wrong: %+v", views[0])
	}
	if views[1].ID != "e1" || views[1].Score != 0.3 {
		t.Errorf("view[1] wrong: %+v", views[1])
	}

	// Degraded passthrough: searcher degraded ⇒ empty + degraded, no error.
	dv, deg, err := Similar(ctx, st, fakeSearcher{degraded: true}, scope, "x", 5)
	if err != nil || !deg || len(dv) != 0 {
		t.Errorf("degraded passthrough wrong: views=%d deg=%v err=%v", len(dv), deg, err)
	}

	// Searcher error ⇒ propagated.
	if _, _, err := Similar(ctx, st, fakeSearcher{err: errors.New("boom")}, scope, "x", 5); err == nil {
		t.Errorf("expected searcher error to propagate")
	}
}

// --- Phase 24b: Arc (cross-session threading, D-081) -------------------------

func arcSeed(t *testing.T, st store.Store, scope identity.Scope, epID, narrID string, start int64) {
	t.Helper()
	ctx := context.Background()
	if err := st.Memories().Insert(ctx, scope, store.Memory{
		ID: narrID, Kind: "narrative", Content: "narr " + epID, Status: "active",
		Importance: 3, Confidence: 0.8, TrustSource: "episodic", Stability: 1.0,
		EpisodeID: epID, CreatedAt: start, UpdatedAt: start,
	}); err != nil {
		t.Fatalf("insert narrative: %v", err)
	}
	if err := st.Episodes().CreateEpisode(ctx, scope, store.Episode{
		ID: epID, SessionID: epID + "-s", Title: epID, Status: "closed", Outcome: "success",
		StartedAt: start, EndedAt: start + 100, NarrativeMemoryID: narrID, CreatedAt: start, UpdatedAt: start,
	}); err != nil {
		t.Fatalf("create episode: %v", err)
	}
}

func arcLink(t *testing.T, st store.Store, scope identity.Scope, from, to string) {
	t.Helper()
	if err := st.Memories().InsertLinks(context.Background(), scope, []store.Link{{
		ID: "lnk-" + from + "-" + to, TenantID: scope.Tenant, FromMemory: from, ToMemory: to,
		Type: "relates_to", Source: "inferred", Confidence: 0.5, CreatedAt: 1,
	}}); err != nil {
		t.Fatalf("link: %v", err)
	}
}

func TestArc(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	scope := identity.Scope{Tenant: "arc-t"}

	// A — B — C chain (transitive arc) + D unthreaded.
	arcSeed(t, st, scope, "A", "nA", 100)
	arcSeed(t, st, scope, "B", "nB", 300)
	arcSeed(t, st, scope, "C", "nC", 200)
	arcSeed(t, st, scope, "D", "nD", 50)
	arcLink(t, st, scope, "nA", "nB")
	arcLink(t, st, scope, "nB", "nC")

	got, err := Arc(ctx, st, scope, "A")
	if err != nil {
		t.Fatalf("Arc: %v", err)
	}
	ids := map[string]bool{}
	for _, v := range got {
		ids[v.ID] = true
	}
	if len(got) != 3 || !ids["A"] || !ids["B"] || !ids["C"] {
		t.Fatalf("arc of A should be {A,B,C}, got %+v", got)
	}
	// Most-recent-first by StartedAt: B(300), C(200), A(100).
	if got[0].ID != "B" || got[1].ID != "C" || got[2].ID != "A" {
		t.Errorf("arc order wrong: %+v", got)
	}
	if ids["D"] {
		t.Error("unthreaded episode D leaked into the arc")
	}

	// Unthreaded seed ⇒ just itself.
	d, _ := Arc(ctx, st, scope, "D")
	if len(d) != 1 || d[0].ID != "D" {
		t.Errorf("arc of unthreaded D should be [D], got %+v", d)
	}

	// Missing seed ⇒ empty.
	if m, _ := Arc(ctx, st, scope, "nope"); len(m) != 0 {
		t.Errorf("arc of missing seed should be empty, got %+v", m)
	}

	// Cross-tenant isolation.
	if x, _ := Arc(ctx, st, identity.Scope{Tenant: "other"}, "A"); len(x) != 0 {
		t.Errorf("cross-tenant arc leak: %+v", x)
	}
}

func TestArc_NonActiveNarrativeSkipped(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	scope := identity.Scope{Tenant: "arc-t2"}
	arcSeed(t, st, scope, "A", "nA", 100)
	arcSeed(t, st, scope, "B", "nB", 200)
	arcLink(t, st, scope, "nA", "nB")
	// Supersede B's narrative → B must drop out of the arc.
	if err := st.Memories().SetStatus(ctx, scope, "nB", "superseded", 300); err != nil {
		t.Fatalf("set status: %v", err)
	}
	got, _ := Arc(ctx, st, scope, "A")
	if len(got) != 1 || got[0].ID != "A" {
		t.Errorf("non-active narrative B must be skipped, got %+v", got)
	}
}
