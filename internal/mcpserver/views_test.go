package mcpserver

// views_test.go — Phase ae9 (D-149/D-151) coverage: the memory_views admin
// tool (create_view/update_view/delete_view/list_views), the view_name apply
// arg + degraded_view marker on memory_retrieve, and the KeyIDFromContext MCP
// plumbing (the "key" topic-view subject fallback).

import (
	"context"
	"log/slog"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/views"
	"github.com/hurtener/stowage/internal/vindex"
)

// newViewsServices builds a Services with a real Retriever + views.Service
// wired for ae9 (retrieval.agent_views.enabled=true equivalent, TopicViews
// wired on both the retriever's read path and the admin service), so
// memory_views and memory_retrieve's view_name apply arg can be exercised
// end-to-end against a real sqlite store.
func newViewsServices(t *testing.T) *Services {
	t.Helper()
	st := newHandlerStore(t)
	log := noopLog()

	gw, err := gateway.Open(context.Background(), config.GatewayConfig{
		Driver:    "mock",
		EmbedDims: 8,
	}, slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open mock gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close(context.Background()) })

	vi := vindex.New(st.Vectors(), 8, "mock-embed")
	ret := retrieval.New(st.Memories(), st.Records(), vi, gw, log).
		WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")
	topicSvc := topics.New(st.Topics(), log, "assistant")
	viewsSvc := views.New(st.TopicViews(), st.Events(), log)

	return &Services{
		Store:      st,
		Retriever:  ret,
		Gateway:    gw,
		TopicSvc:   topicSvc,
		ViewsSvc:   viewsSvc,
		PipelineIn: nil,
		Log:        log,
		ScopeFn:    StdioScopeFn("views-tenant"),
	}
}

func TestViewsHandler_CRUD(t *testing.T) {
	svc := newViewsServices(t)
	ctx := context.Background()
	h := makeViewsHandler(svc)

	// create_view.
	created, err := h(ctx, ViewsInput{
		Action: "create_view", SubjectKind: "agent", SubjectID: "agent-1", ViewName: "work",
		AllowTopics: []string{"auth", "billing"}, DenyTopics: []string{"secrets"},
	})
	if err != nil {
		t.Fatalf("create_view: %v", err)
	}
	if created.Structured.View == nil || created.Structured.View.SubjectID != "agent-1" || created.Structured.View.ViewName != "work" {
		t.Fatalf("create_view: unexpected view: %+v", created.Structured.View)
	}

	// update_view (full replace).
	updated, err := h(ctx, ViewsInput{
		Action: "update_view", SubjectKind: "agent", SubjectID: "agent-1", ViewName: "work",
		AllowTopics: []string{"only-this"},
	})
	if err != nil {
		t.Fatalf("update_view: %v", err)
	}
	if len(updated.Structured.View.AllowTopics) != 1 || updated.Structured.View.AllowTopics[0] != "only-this" {
		t.Fatalf("update_view: unexpected view: %+v", updated.Structured.View)
	}
	if len(updated.Structured.View.DenyTopics) != 0 {
		t.Errorf("update_view: DenyTopics must be fully replaced (empty), got %v", updated.Structured.View.DenyTopics)
	}

	// list_views.
	if _, err := h(ctx, ViewsInput{
		Action: "create_view", SubjectKind: "key", SubjectID: "sk_abc", AllowTopics: []string{"x"},
	}); err != nil {
		t.Fatalf("create_view (key subject): %v", err)
	}
	listed, err := h(ctx, ViewsInput{Action: "list_views"})
	if err != nil {
		t.Fatalf("list_views: %v", err)
	}
	if len(listed.Structured.Views) != 2 {
		t.Fatalf("list_views: want 2 views, got %d", len(listed.Structured.Views))
	}
	narrowed, err := h(ctx, ViewsInput{Action: "list_views", SubjectKind: "agent", SubjectID: "agent-1"})
	if err != nil {
		t.Fatalf("list_views narrowed: %v", err)
	}
	if len(narrowed.Structured.Views) != 1 {
		t.Fatalf("list_views narrowed: want 1 view, got %d", len(narrowed.Structured.Views))
	}

	// delete_view.
	deleted, err := h(ctx, ViewsInput{Action: "delete_view", SubjectKind: "agent", SubjectID: "agent-1", ViewName: "work"})
	if err != nil {
		t.Fatalf("delete_view: %v", err)
	}
	if deleted.Structured.Deleted != "agent/agent-1/work" {
		t.Errorf("delete_view: got %q want agent/agent-1/work", deleted.Structured.Deleted)
	}
	if _, err := h(ctx, ViewsInput{Action: "update_view", SubjectKind: "agent", SubjectID: "agent-1", ViewName: "work", AllowTopics: []string{"x"}}); err == nil {
		t.Error("update_view after delete: expected ErrNotFound-shaped error")
	}
}

func TestViewsHandler_DeleteView_DefaultsViewName(t *testing.T) {
	svc := newViewsServices(t)
	ctx := context.Background()
	h := makeViewsHandler(svc)

	if _, err := h(ctx, ViewsInput{Action: "create_view", SubjectKind: "agent", SubjectID: "a1", AllowTopics: []string{"x"}}); err != nil {
		t.Fatalf("create_view: %v", err)
	}
	// No ViewName on delete_view — must default to "default", matching create.
	deleted, err := h(ctx, ViewsInput{Action: "delete_view", SubjectKind: "agent", SubjectID: "a1"})
	if err != nil {
		t.Fatalf("delete_view (default view name): %v", err)
	}
	if deleted.Structured.Deleted != "agent/a1/default" {
		t.Errorf("delete_view: got %q want agent/a1/default", deleted.Structured.Deleted)
	}
}

func TestViewsHandler_Validation(t *testing.T) {
	svc := newViewsServices(t)
	ctx := context.Background()
	h := makeViewsHandler(svc)

	if _, err := h(ctx, ViewsInput{Action: "create_view"}); err == nil {
		t.Error("create_view without subject_id: expected error")
	}
	if _, err := h(ctx, ViewsInput{Action: "create_view", SubjectKind: "bogus", SubjectID: "a1", AllowTopics: []string{"x"}}); err == nil {
		t.Error("create_view with invalid subject_kind: expected error")
	}
	if _, err := h(ctx, ViewsInput{Action: "delete_view"}); err == nil {
		t.Error("delete_view without subject_id: expected error")
	}
	if _, err := h(ctx, ViewsInput{Action: "bogus"}); err == nil {
		t.Error("unknown action: expected error")
	}

	// Nil ViewsSvc -> error (mirrors TestAgentPolicyHandler_Validation's nil-service branch).
	svc2 := newHandlerServices(t) // ViewsSvc is nil
	h2 := makeViewsHandler(svc2)
	if _, err := h2(ctx, ViewsInput{Action: "list_views"}); err == nil {
		t.Error("nil views service: expected error")
	}
}

// TestViewsHandler_DoesNotEmitEventItself is the D-067/D-073 grep-adjacent
// assertion at the handler level: makeViewsHandler routes every mutation
// through svc.ViewsSvc.*, never touching an EventStore directly (the
// governance event is produced by the core, once, regardless of surface).
func TestViewsHandler_DoesNotEmitEventItself(t *testing.T) {
	svc := newViewsServices(t)
	ctx := context.Background()
	h := makeViewsHandler(svc)

	before, _, err := svc.Store.Events().List(ctx, identity.Scope{Tenant: "views-tenant"}, 100, "")
	if err != nil {
		t.Fatalf("list events before: %v", err)
	}
	if _, err := h(ctx, ViewsInput{
		Action: "create_view", SubjectKind: "agent", SubjectID: "a1", AllowTopics: []string{"x"},
	}); err != nil {
		t.Fatalf("create_view: %v", err)
	}
	after, _, err := svc.Store.Events().List(ctx, identity.Scope{Tenant: "views-tenant"}, 100, "")
	if err != nil {
		t.Fatalf("list events after: %v", err)
	}
	// Exactly ONE new event ("view.created") — the core's emitEvent, not a
	// handler-level duplicate.
	if len(after)-len(before) != 1 {
		t.Fatalf("expected exactly 1 new event from the core, got %d", len(after)-len(before))
	}
	found := false
	for _, ev := range after {
		if ev.Type == "view.created" {
			found = true
		}
	}
	if !found {
		t.Error("expected a view.created event emitted by the core")
	}
}

// ── memory_retrieve view_name apply + CredentialKeyID (KeyIDFromContext) ──────

// TestHandlerRetrieve_ViewNameSeam proves the AC-1/AC-3 wiring end to end: a
// caller supplying view_name narrows memory_retrieve's own-scope results
// according to the bound agent's named view, and DegradedView surfaces false
// on a clean, filtered read.
func TestHandlerRetrieve_ViewNameSeam(t *testing.T) {
	svc := newViewsServices(t)
	ctx := context.Background()

	viewsH := makeViewsHandler(svc)
	if _, err := viewsH(ctx, ViewsInput{
		Action: "create_view", SubjectKind: "agent", SubjectID: "bound-agent", ViewName: "work",
		AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("create_view: %v", err)
	}

	scope := identity.Scope{Tenant: "views-tenant"}
	commitTopicTaggedMemory(t, svc.Store, scope, "auth widget note qview-mcp", []string{"auth"})
	commitTopicTaggedMemory(t, svc.Store, scope, "deploy widget note qview-mcp", []string{"deploy"})

	retrieveH := makeRetrieveHandler(svc)
	metaCtx := server.WithRequestMeta(ctx, map[string]any{"agent_id": "bound-agent"})
	resp, err := retrieveH(metaCtx, RetrieveInput{Query: "qview-mcp widget", Limit: 10, ViewName: "work"})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if resp.Structured.DegradedView {
		t.Error("expected DegradedView=false on a clean bound-view read")
	}
	foundAuth, foundDeploy := false, false
	for _, it := range resp.Structured.Items {
		if it.Content == "auth widget note qview-mcp" {
			foundAuth = true
		}
		if it.Content == "deploy widget note qview-mcp" {
			foundDeploy = true
		}
	}
	if !foundAuth {
		t.Error("expected the allow-topic memory in the bound-view's result")
	}
	if foundDeploy {
		t.Error("the named view must have subtracted the non-allow-topic memory")
	}
}

// TestKeyIDFromContext_RoundTrips proves the ae9 MCP plumbing: AuthMiddleware
// stashes the verified key id and KeyIDFromContext resolves it; absent
// context (e.g. stdio mode) resolves to "".
func TestKeyIDFromContext_RoundTrips(t *testing.T) {
	if got := KeyIDFromContext(context.Background()); got != "" {
		t.Errorf("KeyIDFromContext(no key stashed) = %q, want \"\"", got)
	}
	ctx := withKeyID(context.Background(), "sk_test123")
	if got := KeyIDFromContext(ctx); got != "sk_test123" {
		t.Errorf("KeyIDFromContext = %q, want sk_test123", got)
	}
}

// TestHandlerRetrieve_CredentialKeyIDFromContext proves the retrieve handler
// reads KeyIDFromContext and threads it as Request.CredentialKeyID, resolving
// a "key"-subject view when the caller has no _meta.agent_id.
func TestHandlerRetrieve_CredentialKeyIDFromContext(t *testing.T) {
	svc := newViewsServices(t)
	ctx := context.Background()

	viewsH := makeViewsHandler(svc)
	if _, err := viewsH(ctx, ViewsInput{
		Action: "create_view", SubjectKind: "key", SubjectID: "sk_caller", ViewName: "default",
		AllowTopics: []string{"auth"},
	}); err != nil {
		t.Fatalf("create_view (key subject): %v", err)
	}

	scope := identity.Scope{Tenant: "views-tenant"}
	commitTopicTaggedMemory(t, svc.Store, scope, "auth widget note qview-key", []string{"auth"})
	commitTopicTaggedMemory(t, svc.Store, scope, "deploy widget note qview-key", []string{"deploy"})

	retrieveH := makeRetrieveHandler(svc)
	keyCtx := withKeyID(ctx, "sk_caller") // no _meta.agent_id — key subject fallback
	resp, err := retrieveH(keyCtx, RetrieveInput{Query: "qview-key widget", Limit: 10})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if resp.Structured.DegradedView {
		t.Error("expected DegradedView=false on a clean key-subject read")
	}
	foundAuth, foundDeploy := false, false
	for _, it := range resp.Structured.Items {
		if it.Content == "auth widget note qview-key" {
			foundAuth = true
		}
		if it.Content == "deploy widget note qview-key" {
			foundDeploy = true
		}
	}
	if !foundAuth {
		t.Error("expected the allow-topic memory via the key-subject view")
	}
	if foundDeploy {
		t.Error("the key-subject view must have subtracted the non-allow-topic memory")
	}
}
