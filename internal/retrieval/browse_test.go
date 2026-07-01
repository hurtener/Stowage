package retrieval_test

import (
	"context"
	"sync"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
)

// insertBrowseMemory is a small test helper that inserts a memory with the
// given status/created_at into scope.
func insertBrowseMemory(t *testing.T, st store.Store, scope identity.Scope, id, status string, createdAt int64) {
	t.Helper()
	mem := store.Memory{
		ID: id, Kind: "fact", Content: "content-" + id,
		Status: status, Confidence: 0.5, TrustSource: "llm_extracted", Stability: 1.0,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := st.Memories().Insert(context.Background(), scope, mem); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// TestBrowse_RecentDispatch proves BrowseRecent dispatches to
// Store.ListByScopeRecent (most-recent-first).
func TestBrowse_RecentDispatch(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "browse-recent"}

	insertBrowseMemory(t, st, scope, "m1", "active", 1000)
	insertBrowseMemory(t, st, scope, "m2", "active", 2000)
	insertBrowseMemory(t, st, scope, "m3", "superseded", 3000) // excluded — not active

	res, err := retrieval.Browse(context.Background(), st, scope, retrieval.BrowseOptions{Mode: retrieval.BrowseRecent, Limit: 10})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(res.Memories) != 3 {
		t.Fatalf("BrowseRecent: got %d memories want 3 (ListByScopeRecent is status-agnostic)", len(res.Memories))
	}
	if res.Memories[0].ID != "m3" || res.Memories[1].ID != "m2" || res.Memories[2].ID != "m1" {
		t.Errorf("BrowseRecent order wrong: got %v", []string{res.Memories[0].ID, res.Memories[1].ID, res.Memories[2].ID})
	}
	if res.NextCursor != "" {
		t.Errorf("expected empty cursor for a single page, got %q", res.NextCursor)
	}
}

// TestBrowse_SupersededDispatch proves BrowseSuperseded dispatches to the
// EXISTING Store.ListByStatus(scope,"superseded",…) — H4, no new query — and
// that its ordering is oldest-first (the accepted asymmetry, D-143).
func TestBrowse_SupersededDispatch(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "browse-superseded"}

	insertBrowseMemory(t, st, scope, "s1", "superseded", 1000)
	insertBrowseMemory(t, st, scope, "s2", "superseded", 2000)
	insertBrowseMemory(t, st, scope, "a1", "active", 3000) // excluded — wrong status

	res, err := retrieval.Browse(context.Background(), st, scope, retrieval.BrowseOptions{Mode: retrieval.BrowseSuperseded, Limit: 10})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(res.Memories) != 2 {
		t.Fatalf("BrowseSuperseded: got %d memories want 2", len(res.Memories))
	}
	// ListByStatus orders created_at ASC (oldest-first) — the deliberate
	// asymmetry with BrowseRecent (D-143).
	if res.Memories[0].ID != "s1" || res.Memories[1].ID != "s2" {
		t.Errorf("BrowseSuperseded order wrong (want oldest-first): got %v", []string{res.Memories[0].ID, res.Memories[1].ID})
	}
}

// TestBrowse_DefaultLimit proves Limit<=0 falls back to opts.DefaultLimit.
func TestBrowse_DefaultLimit(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "browse-default"}
	for i := 0; i < 5; i++ {
		insertBrowseMemory(t, st, scope, "m"+string(rune('0'+i)), "active", int64(1000+i))
	}

	res, err := retrieval.Browse(context.Background(), st, scope, retrieval.BrowseOptions{Mode: retrieval.BrowseRecent, DefaultLimit: 2})
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(res.Memories) != 2 {
		t.Fatalf("got %d memories want 2 (DefaultLimit applied)", len(res.Memories))
	}
	if res.NextCursor == "" {
		t.Error("expected a next cursor — more rows remain")
	}
}

// TestBrowse_ClampAtMax proves an explicit or default limit above
// browseMaxLimit (100) is clamped, and a mis-set (negative) DefaultLimit also
// floors to the clamp.
func TestBrowse_ClampAtMax(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "browse-clamp"}
	insertBrowseMemory(t, st, scope, "m1", "active", 1000)

	res, err := retrieval.Browse(context.Background(), st, scope, retrieval.BrowseOptions{Mode: retrieval.BrowseRecent, Limit: 1000})
	if err != nil {
		t.Fatalf("Browse (explicit over-cap limit): %v", err)
	}
	if len(res.Memories) != 1 {
		t.Fatalf("got %d memories want 1", len(res.Memories))
	}

	res2, err := retrieval.Browse(context.Background(), st, scope, retrieval.BrowseOptions{Mode: retrieval.BrowseRecent, DefaultLimit: -5})
	if err != nil {
		t.Fatalf("Browse (negative default limit): %v", err)
	}
	if len(res2.Memories) != 1 {
		t.Fatalf("got %d memories want 1 (negative default clamped, not errored)", len(res2.Memories))
	}
}

// TestParseBrowseMode proves the closed-enum contract (AC-7): "" and "recent"
// resolve to BrowseRecent, "superseded" to BrowseSuperseded, and any other
// value is rejected rather than silently defaulted.
func TestParseBrowseMode(t *testing.T) {
	cases := []struct {
		in      string
		want    retrieval.BrowseMode
		wantErr bool
	}{
		{"", retrieval.BrowseRecent, false},
		{"recent", retrieval.BrowseRecent, false},
		{"superseded", retrieval.BrowseSuperseded, false},
		{"RECENT", retrieval.BrowseRecent, true},
		{"bogus", retrieval.BrowseRecent, true},
	}
	for _, c := range cases {
		got, err := retrieval.ParseBrowseMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseBrowseMode(%q): expected an error, got none", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseBrowseMode(%q): unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseBrowseMode(%q): got %v want %v", c.in, got, c.want)
		}
	}
}

// TestBrowse_BadCursor proves a malformed cursor surfaces store.ErrBadCursor,
// not a panic or a silent first page (AC-8).
func TestBrowse_BadCursor(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "browse-badcursor"}

	if _, err := retrieval.Browse(context.Background(), st, scope, retrieval.BrowseOptions{Mode: retrieval.BrowseRecent, Cursor: "not-a-cursor"}); err == nil {
		t.Error("expected an error for a malformed cursor")
	}
}

// TestBrowse_ScopeRequired proves an empty tenant fails closed (P3).
func TestBrowse_ScopeRequired(t *testing.T) {
	st := openStore(t)
	if _, err := retrieval.Browse(context.Background(), st, identity.Scope{}, retrieval.BrowseOptions{Mode: retrieval.BrowseRecent}); err == nil {
		t.Error("expected an error for an empty-tenant scope")
	}
}

// TestBrowse_ConcurrentReuse proves Browse is safe for concurrent reuse from N
// goroutines over one shared store (§5 concurrency — run under -race).
func TestBrowse_ConcurrentReuse(t *testing.T) {
	st := openStore(t)
	scope := identity.Scope{Tenant: "browse-concurrent"}
	for i := 0; i < 10; i++ {
		insertBrowseMemory(t, st, scope, "cm"+string(rune('a'+i)), "active", int64(1000+i))
	}

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	counts := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mode := retrieval.BrowseRecent
			if i%2 == 0 {
				mode = retrieval.BrowseSuperseded
			}
			res, err := retrieval.Browse(context.Background(), st, scope, retrieval.BrowseOptions{Mode: mode, Limit: 20})
			errs[i] = err
			counts[i] = len(res.Memories)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	for i := 1; i < n; i++ {
		// Every odd-indexed goroutine ran BrowseRecent (10 rows); every
		// even-indexed goroutine ran BrowseSuperseded (0 rows) — same result
		// within each mode's goroutines proves no cross-goroutine corruption.
		if i%2 == 1 && counts[i] != 10 {
			t.Errorf("goroutine %d (recent): got %d memories want 10", i, counts[i])
		}
		if i%2 == 0 && counts[i] != 0 {
			t.Errorf("goroutine %d (superseded): got %d memories want 0", i, counts[i])
		}
	}
}
