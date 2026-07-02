// Package views implements the admin CRUD core for named topic views (ae9,
// D-149/D-151) — the read-time curation lenses stored as rows on ae1's
// topic_views junction table (Store.TopicViews(), internal/store/store.go).
// It mirrors internal/grants.Service exactly: it wraps the TopicViewStore
// seam, calls (store.TopicView).Validate() on every write, and emits ONE
// governance audit event per mutation via its own private emitEvent — the
// D-067/D-073 discipline that keeps the side effect (the audit event) in the
// core so no admin surface (HTTP or MCP) can omit it or diverge.
//
// views.Service does NOT implement view APPLY — the read-time
// resolve-then-filter at the RRF→scoringK seam is
// internal/retrieval.resolveAndApplyView, which reads the SAME
// store.TopicViewStore directly (a raw handle wired via
// Retriever.SetTopicViews, not through this service): the hot retrieve path
// must stay gateway-free and fail-open (D-139), and gets no benefit from
// routing through an admin-shaped service. This package is admin-CRUD only —
// reachable on {HTTP, MCP}, never the single-user SDK (D-067 tiering,
// matching memory_grants/memory_agent_policy's precedent).
package views

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// Service provides the topic-view admin domain logic: validation + event
// emission over the store.TopicViewStore seam. Safe for concurrent use.
type Service struct {
	st  store.TopicViewStore
	ev  store.EventStore
	log *slog.Logger
}

// New creates a Service backed by the given topic-view store and event store.
// ev may be nil (event emission disabled; used in tests, mirrors grants.New).
func New(st store.TopicViewStore, ev store.EventStore, log *slog.Logger) *Service {
	return &Service{
		st:  st,
		ev:  ev,
		log: log.With("subsystem", "views"),
	}
}

// CreateView validates and creates a new named view (scope.Tenant is the
// view's tenant — the only P3 boundary), then emits a "view.created"
// governance event exactly once. ErrConflict when a view already exists for
// the natural key (tenant_id, subject_kind, subject_id, view_name).
func (s *Service) CreateView(ctx context.Context, scope identity.Scope, v store.TopicView) (*store.TopicView, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	v.TenantID = scope.Tenant
	if err := s.st.CreateView(ctx, scope, v); err != nil {
		return nil, fmt.Errorf("views: create view: %w", err)
	}
	got, err := s.st.GetView(ctx, scope, v.SubjectKind, v.SubjectID, v.ViewName)
	if err != nil {
		return nil, fmt.Errorf("views: create view (read-back): %w", err)
	}
	s.emitEvent(ctx, scope, "view.created", got.ID, viewEventPayload(*got))
	return got, nil
}

// UpdateView validates and atomically replaces an existing view's
// AllowTopics/DenyTopics, then emits a "view.updated" governance event exactly
// once. ErrNotFound when the view does not exist.
func (s *Service) UpdateView(ctx context.Context, scope identity.Scope, v store.TopicView) (*store.TopicView, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	v.TenantID = scope.Tenant
	if err := s.st.UpdateView(ctx, scope, v); err != nil {
		return nil, fmt.Errorf("views: update view: %w", err)
	}
	got, err := s.st.GetView(ctx, scope, v.SubjectKind, v.SubjectID, v.ViewName)
	if err != nil {
		return nil, fmt.Errorf("views: update view (read-back): %w", err)
	}
	s.emitEvent(ctx, scope, "view.updated", got.ID, viewEventPayload(*got))
	return got, nil
}

// DeleteView removes a view by its natural key, then emits a "view.deleted"
// governance event exactly once. ErrNotFound when absent.
func (s *Service) DeleteView(ctx context.Context, scope identity.Scope, subjectKind, subjectID, viewName string) error {
	if err := s.st.DeleteView(ctx, scope, subjectKind, subjectID, viewName); err != nil {
		return fmt.Errorf("views: delete view: %w", err)
	}
	id := subjectKind + "/" + subjectID + "/" + viewName
	s.emitEvent(ctx, scope, "view.deleted", id,
		fmt.Sprintf(`{"subject_kind":%q,"subject_id":%q,"view_name":%q}`, subjectKind, subjectID, viewName))
	return nil
}

// ListViews returns all views for the tenant, optionally narrowed to one
// subject. A pure read — no event emission (mirrors grants.Service.ListGrants).
func (s *Service) ListViews(ctx context.Context, scope identity.Scope, subjectKind, subjectID string) ([]store.TopicView, error) {
	return s.st.ListViews(ctx, scope, subjectKind, subjectID)
}

// GetView resolves one view by natural key. A pure read — no event emission.
func (s *Service) GetView(ctx context.Context, scope identity.Scope, subjectKind, subjectID, viewName string) (*store.TopicView, error) {
	return s.st.GetView(ctx, scope, subjectKind, subjectID, viewName)
}

// viewEventPayload renders the governance-event payload for a create/update
// mutation — the view's natural key plus its resulting allow/deny key counts
// (not the topic keys themselves, to keep the audit trail compact; the full
// view is always re-fetchable via GetView).
func viewEventPayload(v store.TopicView) string {
	return fmt.Sprintf(
		`{"subject_kind":%q,"subject_id":%q,"view_name":%q,"allow_count":%d,"deny_count":%d}`,
		v.SubjectKind, v.SubjectID, v.ViewName, len(v.AllowTopics), len(v.DenyTopics),
	)
}

// emitEvent mirrors grants.Service.emitEvent exactly (internal/grants/grants.go)
// — the ONE place a topic-view mutation's audit event is produced, so neither
// the HTTP nor the MCP admin handler needs to (or may) emit it itself
// (D-067/D-073, grep-enforced by scripts/smoke/phase-ae9.sh).
func (s *Service) emitEvent(ctx context.Context, scope identity.Scope, eventType, subjectID, payload string) {
	if s.ev == nil {
		return
	}
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      eventType,
		SubjectID: subjectID,
		Payload:   payload,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := s.ev.Emit(ctx, scope, ev); err != nil {
		s.log.WarnContext(ctx, "views: emit event failed",
			slog.String("type", eventType), slog.Any("err", err))
	}
}
