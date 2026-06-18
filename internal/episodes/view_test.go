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
