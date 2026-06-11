package topics_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"

	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func noopLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "topics-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	_ = f.Close()
	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = f.Name()
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

func upsertTopic(t *testing.T, ts store.TopicStore, scope identity.Scope, key, desc, status string) {
	t.Helper()
	now := time.Now().UnixMilli()
	err := ts.Upsert(context.Background(), scope, store.Topic{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Key:         key,
		Description: desc,
		Status:      status,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("upsert topic %q: %v", key, err)
	}
}

// ── virtual pack tests ────────────────────────────────────────────────────────

// TestActiveTopics_VirtualPack_Assistant asserts that an assistant-profile scope
// with no explicit topics returns the pack:preferences virtual topics (D-043).
func TestActiveTopics_VirtualPack_Assistant(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-vp-asst"}

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	if len(views) == 0 {
		t.Fatal("expected virtual pack topics, got none")
	}
	for _, v := range views {
		if v.Source != "pack" {
			t.Errorf("topic %q: want Source=pack, got %q", v.Key, v.Source)
		}
		if v.Pack != topics.PackPreferences {
			t.Errorf("topic %q: want Pack=%q, got %q", v.Key, topics.PackPreferences, v.Pack)
		}
		if v.Status != "active" {
			t.Errorf("topic %q: want Status=active, got %q", v.Key, v.Status)
		}
	}
}

// TestActiveTopics_VirtualPack_CodingAgent asserts that a coding-agent-profile
// scope with no explicit topics returns pack:agent-learnings.
func TestActiveTopics_VirtualPack_CodingAgent(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "coding-agent")
	scope := identity.Scope{Tenant: "t-vp-ca"}

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	if len(views) == 0 {
		t.Fatal("expected virtual pack topics, got none")
	}
	for _, v := range views {
		if v.Pack != topics.PackAgentLearnings {
			t.Errorf("topic %q: want Pack=%q, got %q", v.Key, topics.PackAgentLearnings, v.Pack)
		}
	}
}

// TestActiveTopics_ExplicitTopicsSupressVirtualPack asserts that any explicit
// active topic suppresses the virtual pack (D-043: any explicit topic disables
// the pack).
func TestActiveTopics_ExplicitTopicsSupressVirtualPack(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-explicit-sup"}

	upsertTopic(t, st.Topics(), scope, "my-topic", "My custom topic", "active")

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("want 1 topic, got %d", len(views))
	}
	if views[0].Key != "my-topic" {
		t.Errorf("want Key=my-topic, got %q", views[0].Key)
	}
	if views[0].Source != "explicit" {
		t.Errorf("want Source=explicit, got %q", views[0].Source)
	}
}

// TestActiveTopics_PackOff_OptOut asserts that the pack:off sentinel suppresses
// the virtual pack and returns nil when there are no other active topics (AC-2).
func TestActiveTopics_PackOff_OptOut(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-packoff"}

	upsertTopic(t, st.Topics(), scope, topics.PackOff, "", "active")

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	if views != nil {
		t.Errorf("want nil (opt-out), got %v", views)
	}
}

// TestActiveTopics_DeletedPaused_Ignored asserts that deleted and paused topics
// are not included in the active set.
func TestActiveTopics_DeletedPaused_Ignored(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-del-paused"}

	upsertTopic(t, st.Topics(), scope, "active-topic", "Active", "active")
	upsertTopic(t, st.Topics(), scope, "paused-topic", "Paused", "paused")
	// soft-delete a topic
	if err := st.Topics().Delete(context.Background(), scope, "deleted-key"); err != nil {
		// not found is fine — we're just testing the filter
		_ = err
	}
	upsertTopic(t, st.Topics(), scope, "will-delete", "will be deleted", "active")
	if err := st.Topics().Delete(context.Background(), scope, "will-delete"); err != nil {
		t.Fatalf("delete topic: %v", err)
	}

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	for _, v := range views {
		if v.Key == "paused-topic" {
			t.Error("paused topic must not appear in active set")
		}
		if v.Key == "will-delete" {
			t.Error("deleted topic must not appear in active set")
		}
	}
}

// TestPackOff_WithOtherExplicit_PackOffIgnored asserts that when pack:off is
// present alongside other active explicit topics, pack:off is treated as just a
// suppressor of the virtual pack (and the explicit topics are returned).
// The spec: "any explicit topic disables the pack" — pack:off is excluded from
// the returned set but other active topics are returned.
func TestPackOff_WithOtherExplicit_PackOffIgnored(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	scope := identity.Scope{Tenant: "t-packoff-other"}

	upsertTopic(t, st.Topics(), scope, topics.PackOff, "", "active")
	upsertTopic(t, st.Topics(), scope, "real-topic", "A real topic", "active")

	views, err := svc.ActiveTopics(context.Background(), scope)
	if err != nil {
		t.Fatalf("ActiveTopics: %v", err)
	}
	// pack:off is excluded; "real-topic" is returned.
	if len(views) != 1 {
		t.Fatalf("want 1 topic (real-topic), got %d", len(views))
	}
	if views[0].Key != "real-topic" {
		t.Errorf("want Key=real-topic, got %q", views[0].Key)
	}
}
