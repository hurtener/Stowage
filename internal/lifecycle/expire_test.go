package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

func createSuggestion(t *testing.T, st store.Store, scope identity.Scope, createdAt int64) string {
	t.Helper()
	id := ulid.Make().String()
	if err := st.Suggestions().Create(context.Background(), scope, []store.Suggestion{{
		ID: id, SessionID: "s1", TriggerKind: "expiring", MemoryID: "m1",
		Status: "pending", CreatedAt: createdAt, UpdatedAt: createdAt,
	}}); err != nil {
		t.Fatalf("create suggestion: %v", err)
	}
	return id
}

func TestExpireSuggestions_StalePendingExpired(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "tenant-expire"}
	now := time.Now().UnixMilli()

	// A suggestion always references a memory in its tenant; seed one so the sweep's
	// tenant discovery (memories ∪ records) sees this tenant.
	insertMemory(t, st, scope, store.Memory{})
	stale := createSuggestion(t, st, scope, now-int64(48*time.Hour/time.Millisecond)) // 48h old
	fresh := createSuggestion(t, st, scope, now)                                      // brand new

	profile := lifecycle.Profile{
		SuggestExpireInterval: 15 * time.Minute,
		SuggestTTL:            24 * time.Hour,
		SuggestExpireBatch:    200,
	}
	mgr := lifecycle.New(st, testLogger(), profile, make(chan pipeline.Item, 1))
	mgr.RunForce(context.Background())

	gs, err := st.Suggestions().Get(context.Background(), scope, stale)
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if gs.Status != "expired" {
		t.Errorf("stale pending offer should be expired, got %q", gs.Status)
	}
	// Audit trail (§8): a suggestion.expired event is emitted for the GC'd offer.
	evs, err := st.Events().ListBySubject(context.Background(), scope, stale, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Type == "suggestion.expired" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a suggestion.expired event for %s, got %+v", stale, evs)
	}
	gf, err := st.Suggestions().Get(context.Background(), scope, fresh)
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	if gf.Status != "pending" {
		t.Errorf("fresh offer should stay pending, got %q", gf.Status)
	}
}

// TestExpireSuggestions_ZeroProfileDefaults exercises the ttl<=0 and batch<=0
// fallback paths (24h TTL, 200 batch) plus the small-batch remaining<pageSize path.
func TestExpireSuggestions_ZeroProfileDefaults(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "tenant-expire-defaults"}
	now := time.Now().UnixMilli()
	insertMemory(t, st, scope, store.Memory{})
	stale := createSuggestion(t, st, scope, now-int64(48*time.Hour/time.Millisecond))

	// Interval>0 registers the sweep; TTL=0 and Batch=0 take the defaults; a Batch
	// smaller than the page size exercises the remaining<pageSize trim.
	profile := lifecycle.Profile{SuggestExpireInterval: 15 * time.Minute, SuggestTTL: 0, SuggestExpireBatch: 1}
	mgr := lifecycle.New(st, testLogger(), profile, make(chan pipeline.Item, 1))
	mgr.RunForce(context.Background())

	g, _ := st.Suggestions().Get(context.Background(), scope, stale)
	if g.Status != "expired" {
		t.Errorf("stale offer should be expired under default TTL, got %q", g.Status)
	}
}

func TestExpireSuggestions_NoneWhenAllFresh(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "tenant-expire-fresh"}
	now := time.Now().UnixMilli()
	insertMemory(t, st, scope, store.Memory{})
	id := createSuggestion(t, st, scope, now)

	profile := lifecycle.Profile{
		SuggestExpireInterval: 15 * time.Minute,
		SuggestTTL:            24 * time.Hour,
		SuggestExpireBatch:    200,
	}
	mgr := lifecycle.New(st, testLogger(), profile, make(chan pipeline.Item, 1))
	mgr.RunForce(context.Background())

	g, _ := st.Suggestions().Get(context.Background(), scope, id)
	if g.Status != "pending" {
		t.Errorf("fresh offer must not be expired, got %q", g.Status)
	}
}
