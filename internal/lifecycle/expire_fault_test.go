package lifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// faultSuggestions makes the two store calls the expiry sweep depends on fail, so
// the sweep's error branches are exercised (the offers stay pending, no panic).
type faultSuggestions struct {
	store.SuggestionStore
	failList   bool
	failExpire bool
}

func (f faultSuggestions) ListPendingBefore(ctx context.Context, scope identity.Scope, before int64, limit int) ([]store.Suggestion, error) {
	if f.failList {
		return nil, errors.New("injected list failure")
	}
	return f.SuggestionStore.ListPendingBefore(ctx, scope, before, limit)
}

func (f faultSuggestions) ExpirePending(ctx context.Context, scope identity.Scope, ids []string, now int64) error {
	if f.failExpire {
		return errors.New("injected expire failure")
	}
	return f.SuggestionStore.ExpirePending(ctx, scope, ids, now)
}

type faultSuggestStore struct {
	store.Store
	failList   bool
	failExpire bool
}

func (f faultSuggestStore) Suggestions() store.SuggestionStore {
	return faultSuggestions{SuggestionStore: f.Store.Suggestions(), failList: f.failList, failExpire: f.failExpire}
}

func TestExpireSuggestions_ListErrorIsHandled(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "tenant-fault-list"}
	now := time.Now().UnixMilli()
	insertMemory(t, st, scope, store.Memory{})
	id := createSuggestion(t, st, scope, now-int64(48*time.Hour/time.Millisecond))

	profile := lifecycle.Profile{SuggestExpireInterval: 15 * time.Minute, SuggestTTL: 24 * time.Hour, SuggestExpireBatch: 200}
	mgr := lifecycle.New(faultSuggestStore{Store: st, failList: true}, testLogger(), profile, make(chan pipeline.Item, 1))
	mgr.RunForce(context.Background()) // must not panic; the stale offer stays pending

	g, _ := st.Suggestions().Get(context.Background(), scope, id)
	if g.Status != "pending" {
		t.Errorf("list failure should leave the offer pending, got %q", g.Status)
	}
}

func TestExpireSuggestions_ExpireErrorIsHandled(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := identity.Scope{Tenant: "tenant-fault-expire"}
	now := time.Now().UnixMilli()
	insertMemory(t, st, scope, store.Memory{})
	id := createSuggestion(t, st, scope, now-int64(48*time.Hour/time.Millisecond))

	profile := lifecycle.Profile{SuggestExpireInterval: 15 * time.Minute, SuggestTTL: 24 * time.Hour, SuggestExpireBatch: 200}
	mgr := lifecycle.New(faultSuggestStore{Store: st, failExpire: true}, testLogger(), profile, make(chan pipeline.Item, 1))
	mgr.RunForce(context.Background())

	g, _ := st.Suggestions().Get(context.Background(), scope, id)
	if g.Status != "pending" {
		t.Errorf("expire failure should leave the offer pending, got %q", g.Status)
	}
}
