package topics_test

// write_test.go — D-071: the topics.Service.{Upsert,Delete} shared write core
// consumed by the embedded SDK. pack:off is a normal Upsert of {Key: "pack:off"}.

import (
	"context"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/topics"
)

func TestServiceUpsertDelete(t *testing.T) {
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t-write"}

	n, err := svc.Upsert(ctx, scope, []topics.TopicUpsert{
		{Key: "alpha", Description: "first"},
		{Key: "beta", Description: "second", Status: "paused"},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if n != 2 {
		t.Errorf("Upsert: want 2 got %d", n)
	}

	// alpha is active → ActiveTopics returns it; beta is paused → excluded.
	views, err := svc.ActiveTopics(ctx, scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	found := false
	for _, v := range views {
		if v.Key == "alpha" {
			found = true
		}
		if v.Key == "beta" {
			t.Error("paused topic beta should be excluded from ActiveTopics")
		}
	}
	if !found {
		t.Error("active topic alpha missing from ActiveTopics")
	}

	// Delete alpha.
	if err := svc.Delete(ctx, scope, "alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	views, _ = svc.ActiveTopics(ctx, scope)
	for _, v := range views {
		if v.Key == "alpha" {
			t.Error("alpha still active after delete")
		}
	}
}

func TestServiceUpsertPackOff(t *testing.T) {
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t-packoff"}

	if _, err := svc.Upsert(ctx, scope, []topics.TopicUpsert{{Key: topics.PackOff}}); err != nil {
		t.Fatalf("Upsert pack:off: %v", err)
	}
	views, err := svc.ActiveTopics(ctx, scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("pack:off should suppress the virtual default pack; got %d topics", len(views))
	}
}

func TestServiceUpsertDeleteValidation(t *testing.T) {
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t-val"}

	cases := []struct {
		name  string
		items []topics.TopicUpsert
	}{
		{"empty list", nil},
		{"empty key", []topics.TopicUpsert{{Key: ""}}},
		{"bad status", []topics.TopicUpsert{{Key: "k", Status: "bogus"}}},
	}
	for _, tc := range cases {
		if _, err := svc.Upsert(ctx, scope, tc.items); err == nil {
			t.Errorf("Upsert %s: expected error, got nil", tc.name)
		}
	}
	if err := svc.Delete(ctx, scope, ""); err == nil {
		t.Error("Delete empty key: expected error, got nil")
	}
}
