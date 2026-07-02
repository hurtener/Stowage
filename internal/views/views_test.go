package views_test

// views_test.go — unit tests for internal/views (ae9, D-149/D-151).
//
// Coverage targets (D-034/§11): ≥80% of internal/views (scripts/coverage.json).
//
// Test categories:
//   - Service.CreateView/UpdateView/DeleteView/ListViews/GetView CRUD, backed by
//     a real (temp-file) sqlite store — the driver methods are already proven by
//     the store conformance suite, so these tests exercise the SERVICE layer:
//     Validate() wiring, tenant stamping, and read-back.
//   - Event emission: exactly one governance event per mutating op
//     (create/update/delete), via a mock EventStore.
//   - Nil-events no-op path (mirrors grants.Service's TestService_NilEventStore_NoPanic).

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register sqlite driver
	"github.com/hurtener/stowage/internal/views"
)

func noopLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, config.StoreConfig{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "views.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	return st
}

// mockEventStore is a minimal in-memory EventStore for testing (mirrors
// grants_test.go's mockEventStore).
type mockEventStore struct {
	mu     sync.Mutex
	events []store.Event
}

func (e *mockEventStore) Emit(_ context.Context, _ identity.Scope, ev store.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	return nil
}

func (e *mockEventStore) List(_ context.Context, _ identity.Scope, _ int, _ string) ([]store.Event, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.events, "", nil
}

func (e *mockEventStore) ListBySubject(_ context.Context, _ identity.Scope, subjectID string, limit int) ([]store.Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []store.Event
	for _, ev := range e.events {
		if ev.SubjectID == subjectID {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (e *mockEventStore) count(typ string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, ev := range e.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// ---- CreateView --------------------------------------------------------------

func TestCreateView_OK(t *testing.T) {
	st := openStore(t)
	ev := &mockEventStore{}
	svc := views.New(st.TopicViews(), ev, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	got, err := svc.CreateView(ctx, scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "a1", ViewName: "work", AllowTopics: []string{"goals"},
	})
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	if got.TenantID != "t1" || got.SubjectKind != "agent" || got.SubjectID != "a1" || got.ViewName != "work" {
		t.Errorf("CreateView identity: got %+v", got)
	}
	if len(got.AllowTopics) != 1 || got.AllowTopics[0] != "goals" {
		t.Errorf("CreateView AllowTopics: got %v", got.AllowTopics)
	}
	if ev.count("view.created") != 1 {
		t.Errorf("view.created event count = %d, want 1", ev.count("view.created"))
	}
}

// TestCreateView_DefaultsViewName proves an empty ViewName normalizes to
// "default" via (TopicView).Validate — the same normalization the store
// driver relies on.
func TestCreateView_DefaultsViewName(t *testing.T) {
	st := openStore(t)
	svc := views.New(st.TopicViews(), nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	got, err := svc.CreateView(ctx, scope, store.TopicView{
		SubjectKind: "key", SubjectID: "sk_1", AllowTopics: []string{"x"},
	})
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	if got.ViewName != "default" {
		t.Errorf("ViewName = %q, want default", got.ViewName)
	}
}

// TestCreateView_ValidateRejectsBadSubjectKind proves an invalid subject_kind
// is rejected BEFORE any store call or event emission.
func TestCreateView_ValidateRejectsBadSubjectKind(t *testing.T) {
	st := openStore(t)
	ev := &mockEventStore{}
	svc := views.New(st.TopicViews(), ev, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	_, err := svc.CreateView(ctx, scope, store.TopicView{
		SubjectKind: "bogus", SubjectID: "a1", AllowTopics: []string{"x"},
	})
	if !errors.Is(err, store.ErrInvalidSubjectKind) {
		t.Errorf("CreateView bad subject_kind: got %v want ErrInvalidSubjectKind", err)
	}
	if len(ev.events) != 0 {
		t.Errorf("no event should be emitted on a rejected create, got %d", len(ev.events))
	}
}

// TestCreateView_ValidateRejectsEmptySubjectID mirrors the bad-subject-kind
// case for an empty subject_id.
func TestCreateView_ValidateRejectsEmptySubjectID(t *testing.T) {
	st := openStore(t)
	svc := views.New(st.TopicViews(), nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	_, err := svc.CreateView(ctx, scope, store.TopicView{SubjectKind: "agent", AllowTopics: []string{"x"}})
	if !errors.Is(err, store.ErrSubjectIDRequired) {
		t.Errorf("CreateView empty subject_id: got %v want ErrSubjectIDRequired", err)
	}
}

// TestCreateView_ValidateRejectsEmptyView mirrors PutAgentPolicy's footgun
// guard: a view with no allow/deny keys is rejected (no durable representation).
func TestCreateView_ValidateRejectsEmptyView(t *testing.T) {
	st := openStore(t)
	svc := views.New(st.TopicViews(), nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	_, err := svc.CreateView(ctx, scope, store.TopicView{SubjectKind: "agent", SubjectID: "a1"})
	if !errors.Is(err, store.ErrEmptyPolicy) {
		t.Errorf("CreateView empty view: got %v want ErrEmptyPolicy", err)
	}
}

// TestCreateView_Conflict proves a duplicate natural key is rejected and does
// not emit a second event.
func TestCreateView_Conflict(t *testing.T) {
	st := openStore(t)
	ev := &mockEventStore{}
	svc := views.New(st.TopicViews(), ev, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	v := store.TopicView{SubjectKind: "agent", SubjectID: "a1", ViewName: "default", AllowTopics: []string{"x"}}
	if _, err := svc.CreateView(ctx, scope, v); err != nil {
		t.Fatalf("first CreateView: %v", err)
	}
	if _, err := svc.CreateView(ctx, scope, v); !errors.Is(err, store.ErrConflict) {
		t.Errorf("second CreateView: got %v want ErrConflict", err)
	}
	if ev.count("view.created") != 1 {
		t.Errorf("view.created event count = %d, want 1 (conflict must not emit)", ev.count("view.created"))
	}
}

// ---- UpdateView ----------------------------------------------------------------

func TestUpdateView_OK(t *testing.T) {
	st := openStore(t)
	ev := &mockEventStore{}
	svc := views.New(st.TopicViews(), ev, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	if _, err := svc.CreateView(ctx, scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "a1", ViewName: "default", AllowTopics: []string{"x"}, DenyTopics: []string{"y"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}

	got, err := svc.UpdateView(ctx, scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "a1", ViewName: "default", AllowTopics: []string{"z"},
	})
	if err != nil {
		t.Fatalf("UpdateView: %v", err)
	}
	if len(got.AllowTopics) != 1 || got.AllowTopics[0] != "z" {
		t.Errorf("AllowTopics after update: got %v want [z]", got.AllowTopics)
	}
	if len(got.DenyTopics) != 0 {
		t.Errorf("DenyTopics after update: got %v want empty (fully replaced)", got.DenyTopics)
	}
	if ev.count("view.updated") != 1 {
		t.Errorf("view.updated event count = %d, want 1", ev.count("view.updated"))
	}
}

func TestUpdateView_NotFound(t *testing.T) {
	st := openStore(t)
	ev := &mockEventStore{}
	svc := views.New(st.TopicViews(), ev, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	_, err := svc.UpdateView(ctx, scope, store.TopicView{SubjectKind: "agent", SubjectID: "absent", AllowTopics: []string{"x"}})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateView absent: got %v want ErrNotFound", err)
	}
	if len(ev.events) != 0 {
		t.Errorf("no event should be emitted on a failed update, got %d", len(ev.events))
	}
}

// ---- DeleteView ------------------------------------------------------------------

func TestDeleteView_OK(t *testing.T) {
	st := openStore(t)
	ev := &mockEventStore{}
	svc := views.New(st.TopicViews(), ev, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	if _, err := svc.CreateView(ctx, scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "a1", ViewName: "default", AllowTopics: []string{"x"},
	}); err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	if err := svc.DeleteView(ctx, scope, "agent", "a1", "default"); err != nil {
		t.Fatalf("DeleteView: %v", err)
	}
	if ev.count("view.deleted") != 1 {
		t.Errorf("view.deleted event count = %d, want 1", ev.count("view.deleted"))
	}
	if _, err := svc.GetView(ctx, scope, "agent", "a1", "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetView after delete: got %v want ErrNotFound", err)
	}
}

func TestDeleteView_NotFound(t *testing.T) {
	st := openStore(t)
	svc := views.New(st.TopicViews(), nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	if err := svc.DeleteView(ctx, scope, "agent", "never-existed", "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DeleteView absent: got %v want ErrNotFound", err)
	}
}

// ---- ListViews / GetView (pure reads, no event emission) -------------------------

func TestListViews_And_GetView(t *testing.T) {
	st := openStore(t)
	ev := &mockEventStore{}
	svc := views.New(st.TopicViews(), ev, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	for _, sid := range []string{"a1", "a2"} {
		if _, err := svc.CreateView(ctx, scope, store.TopicView{
			SubjectKind: "agent", SubjectID: sid, ViewName: "default", AllowTopics: []string{"k-" + sid},
		}); err != nil {
			t.Fatalf("CreateView %s: %v", sid, err)
		}
	}

	all, err := svc.ListViews(ctx, scope, "", "")
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListViews: got %d want 2", len(all))
	}
	// Reads must not emit governance events.
	if len(ev.events) != 2 { // only the two view.created events from setup
		t.Errorf("ListViews must not emit events: got %d total events want 2 (create-only)", len(ev.events))
	}

	one, err := svc.GetView(ctx, scope, "agent", "a1", "default")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if one.SubjectID != "a1" {
		t.Errorf("GetView: got %+v", one)
	}
}

func TestGetView_NotFound(t *testing.T) {
	st := openStore(t)
	svc := views.New(st.TopicViews(), nil, noopLog())
	_, err := svc.GetView(context.Background(), identity.Scope{Tenant: "t1"}, "agent", "no-such", "default")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetView: got %v want ErrNotFound", err)
	}
}

// ---- Nil EventStore no-op path (mirrors grants.Service precedent) ----------------

func TestService_NilEventStore_NoPanic(t *testing.T) {
	st := openStore(t)
	svc := views.New(st.TopicViews(), nil, noopLog())
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t1"}

	if _, err := svc.CreateView(ctx, scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "a1", AllowTopics: []string{"x"},
	}); err != nil {
		t.Fatalf("CreateView with nil events: %v", err)
	}
	if _, err := svc.UpdateView(ctx, scope, store.TopicView{
		SubjectKind: "agent", SubjectID: "a1", AllowTopics: []string{"y"},
	}); err != nil {
		t.Fatalf("UpdateView with nil events: %v", err)
	}
	if err := svc.DeleteView(ctx, scope, "agent", "a1", "default"); err != nil {
		t.Fatalf("DeleteView with nil events: %v", err)
	}
}

// ---- Scope isolation (P3) --------------------------------------------------------

func TestCreateView_CrossTenantIsolation(t *testing.T) {
	st := openStore(t)
	svc := views.New(st.TopicViews(), nil, noopLog())
	ctx := context.Background()
	scopeA := identity.Scope{Tenant: "tenant-A"}
	scopeB := identity.Scope{Tenant: "tenant-B"}

	if _, err := svc.CreateView(ctx, scopeA, store.TopicView{
		SubjectKind: "agent", SubjectID: "shared", ViewName: "default", AllowTopics: []string{"secret"},
	}); err != nil {
		t.Fatalf("CreateView A: %v", err)
	}
	if _, err := svc.GetView(ctx, scopeB, "agent", "shared", "default"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cross-tenant GetView: got %v want ErrNotFound", err)
	}
}
