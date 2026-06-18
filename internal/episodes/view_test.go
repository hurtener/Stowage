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
