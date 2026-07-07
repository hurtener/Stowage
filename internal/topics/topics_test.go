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
		if v.Source != topics.PackPreferences {
			t.Errorf("topic %q: want Source=%q, got %q", v.Key, topics.PackPreferences, v.Source)
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

// ── ae13 regression: sub-tenant scopes resolve tenant topics (D-154) ─────────

// TestResolve_SubTenantScope_ReturnsTenantTopics is the criterion-1 regression
// matrix (D-154). Two explicit topics are written with a tenant-only scope (as
// every writer — HTTP, MCP, SDK — has always done, via Upsert which stores only
// TenantID). Resolve MUST return those two topics — not the profile default pack
// — when the caller scope carries a sub-tenant dimension: a per-user
// (Scope{Tenant,User}), a per-project+user (Scope{Tenant,Project,User}), and a
// per-session (Scope{Tenant,Session}) caller. Before D-154, the store's
// buildScopeWhere added `AND user_id = ?` / `AND project_id = ?` / `AND
// session_id = ?` for those set fields and matched zero stored topics, silently
// falling back to pack:preferences. Topics are tenant-level curation (D-154),
// so resolution normalizes any caller scope to tenant-only (topicScope) and reads
// match writes.
func TestResolve_SubTenantScope_ReturnsTenantTopics(t *testing.T) {
	t.Parallel()

	readCases := []struct {
		name  string
		scope identity.Scope
	}{
		{"user", identity.Scope{Tenant: "t-d154", User: "u1"}},
		{"project+user", identity.Scope{Tenant: "t-d154", Project: "p1", User: "u1"}},
		{"session", identity.Scope{Tenant: "t-d154", Session: "s1"}},
		{"project+user+session", identity.Scope{Tenant: "t-d154", Project: "p1", User: "u1", Session: "s1"}},
	}

	for _, rc := range readCases {
		rc := rc
		t.Run(rc.name, func(t *testing.T) {
			t.Parallel()
			st := newTestStore(t)
			svc := topics.New(st.Topics(), noopLog(), "assistant")
			ctx := context.Background()
			// Write tenant-only, exactly as the real writers do.
			writeScope := identity.Scope{Tenant: "t-d154"}
			if _, err := svc.Upsert(ctx, writeScope, []topics.TopicUpsert{
				{Key: "tenant-topic-a", Description: "first tenant topic"},
				{Key: "tenant-topic-b", Description: "second tenant topic"},
			}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}

			res, err := svc.Resolve(ctx, rc.scope)
			if err != nil {
				t.Fatalf("Resolve under %v: %v", rc.scope, err)
			}
			if len(res.Topics) != 2 {
				var keys []string
				for _, v := range res.Topics {
					keys = append(keys, v.Key)
				}
				t.Fatalf("scope %v: want 2 tenant topics, got %d (%v) — default-pack fallback indicates D-154 regressed", rc.scope, len(res.Topics), keys)
			}
			seen := map[string]bool{}
			for _, v := range res.Topics {
				if v.Source != "explicit" {
					t.Errorf("scope %v: topic %q Source=%q, want explicit (no default-pack entry)", rc.scope, v.Key, v.Source)
				}
				seen[v.Key] = true
			}
			if !seen["tenant-topic-a"] || !seen["tenant-topic-b"] {
				t.Errorf("scope %v: missing one of the two tenant topics (seen=%v)", rc.scope, seen)
			}
		})
	}
}

// TestResolve_TwoTenant_Isolation asserts that tenant B resolves none of tenant
// A's topics — isolation is preserved by D-154 (tenant is the one dimension
// topicScope keeps, so it cannot widen into another tenant).
func TestResolve_TwoTenant_Isolation(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	ctx := context.Background()

	if _, err := svc.Upsert(ctx, identity.Scope{Tenant: "tenantA"}, []topics.TopicUpsert{
		{Key: "a-only-topic", Description: "tenant A topic"},
	}); err != nil {
		t.Fatalf("Upsert A: %v", err)
	}
	// Tenant B — even carrying a user — sees none of A's topics, so it falls
	// back to the default pack (it has no explicit topics of its own).
	res, err := svc.Resolve(ctx, identity.Scope{Tenant: "tenantB", User: "b-1"})
	if err != nil {
		t.Fatalf("Resolve B: %v", err)
	}
	for _, v := range res.Topics {
		if v.Key == "a-only-topic" {
			t.Errorf("D-154 isolation: tenant B resolved tenant A's topic %q", v.Key)
		}
		if v.Source != topics.PackPreferences {
			t.Errorf("tenant B (no topics) want default pack %q, got source %q for %q", topics.PackPreferences, v.Source, v.Key)
		}
	}
}

// TestResolve_EmptyTenant_FailsClosed asserts P3 still holds after D-154:
// topicScope drops all sub-tenant dimensions but keeps tenant, and an empty
// tenant is rejected by the store's buildScopeWhere (ErrScopeRequired). The
// service surfaces it wrapped, never silently.
func TestResolve_EmptyTenant_FailsClosed(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")

	// A sub-tenant scope with a non-empty tenant still resolves (D-154 keeps
	// tenant); this proves the next case fails because the tenant is empty, not
	// because of the sub-tenant dimension.
	if _, err := svc.Resolve(context.Background(), identity.Scope{Tenant: "t-good", User: "u"}); err != nil {
		t.Fatalf("non-empty-tenant scope with a user should resolve, got %v", err)
	}
	// Empty tenant — even when a sub-tenant dimension is set — fails closed.
	if _, err := svc.Resolve(context.Background(), identity.Scope{User: "u", Project: "p"}); err == nil {
		t.Fatal("empty-tenant scope must fail closed (P3), got nil")
	}
}

// TestResolve_NoTopicsResolvesDefaultPack asserts the zero-config fallback is
// unchanged by D-154: a tenant with no stored topics still resolves the profile
// default pack (criterion 4).
func TestResolve_NoTopicsResolvesDefaultPack(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	ctx := context.Background()

	res, err := svc.Resolve(ctx, identity.Scope{Tenant: "t-empty"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(res.Topics) == 0 {
		t.Fatal("zero-config tenant should resolve the profile default pack, got none")
	}
	for _, v := range res.Topics {
		if v.Source != topics.PackPreferences {
			t.Errorf("want default-pack source %q, got %q for %q", topics.PackPreferences, v.Source, v.Key)
		}
	}
}
