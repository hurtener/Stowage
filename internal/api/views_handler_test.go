package api_test

// views_handler_test.go — HTTP-level tests for named topic views (Phase ae9,
// D-149/D-151):
//
//	GET    /v1/scopes/views
//	POST   /v1/scopes/views
//	PUT    /v1/scopes/views
//	DELETE /v1/scopes/views/{subject_kind}/{subject_id}/{view_name}

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/views"
	"github.com/hurtener/stowage/internal/vindex"
)

// setViewsService wires a views.Service backed by the given store's real
// TopicViewStore, plus the events store (for the event-emission parity test).
func setViewsService(t *testing.T, srv *api.Server, st store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := views.New(st.TopicViews(), st.Events(), log)
	srv.SetViewsService(svc)
}

// TestViews_Unauthorized proves 401 with no auth header on every route.
func TestViews_Unauthorized(t *testing.T) {
	t.Parallel()
	_, ts, _ := newTestServer(t)

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/v1/scopes/views"},
		{"POST", "/v1/scopes/views"},
		{"PUT", "/v1/scopes/views"},
		{"DELETE", "/v1/scopes/views/agent/agent-1/default"},
	} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		drainClose(resp.Body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d want 401", tc.method, tc.path, resp.StatusCode)
		}
	}
}

// TestViews_NilService proves 503 when no views service is wired.
func TestViews_NilService(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-nilv")

	resp := doJSON(t, "GET", ts.URL+"/v1/scopes/views", pt, nil)
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("list, nil views service: got %d want 503", resp.StatusCode)
	}
}

// TestViews_CRUD exercises create/update/list/delete end to end over HTTP.
func TestViews_CRUD(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-views-crud")

	// POST (create).
	createResp := doJSON(t, "POST", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "agent-1", "view_name": "work",
		"allow_topics": []string{"auth", "billing"}, "deny_topics": []string{"secrets"},
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: got %d", createResp.StatusCode)
	}
	var createOut viewWire
	decodeJSON(t, createResp, &createOut)
	if createOut.SubjectID != "agent-1" || createOut.ViewName != "work" || len(createOut.AllowTopics) != 2 {
		t.Fatalf("POST: unexpected body: %+v", createOut)
	}

	// POST again (same natural key) -> 409 conflict.
	conflictResp := doJSON(t, "POST", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "agent-1", "view_name": "work",
		"allow_topics": []string{"x"},
	})
	drainClose(conflictResp.Body)
	if conflictResp.StatusCode != http.StatusConflict {
		t.Errorf("POST duplicate: got %d want 409", conflictResp.StatusCode)
	}

	// PUT (update — full replace).
	updateResp := doJSON(t, "PUT", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "agent-1", "view_name": "work",
		"allow_topics": []string{"only-this"},
	})
	if updateResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: got %d", updateResp.StatusCode)
	}
	var updateOut viewWire
	decodeJSON(t, updateResp, &updateOut)
	if len(updateOut.AllowTopics) != 1 || updateOut.AllowTopics[0] != "only-this" {
		t.Fatalf("PUT: unexpected body: %+v", updateOut)
	}
	if len(updateOut.DenyTopics) != 0 {
		t.Errorf("PUT: DenyTopics must be fully replaced (empty), got %v", updateOut.DenyTopics)
	}

	// PUT missing (not found) -> 404.
	missResp := doJSON(t, "PUT", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "never-created", "allow_topics": []string{"x"},
	})
	drainClose(missResp.Body)
	if missResp.StatusCode != http.StatusNotFound {
		t.Errorf("PUT missing: got %d want 404", missResp.StatusCode)
	}

	// LIST.
	createResp2 := doJSON(t, "POST", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "key", "subject_id": "sk_1", "allow_topics": []string{"y"},
	})
	drainClose(createResp2.Body)
	listResp := doJSON(t, "GET", ts.URL+"/v1/scopes/views", pt, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("LIST: got %d", listResp.StatusCode)
	}
	var listOut struct {
		Views []viewWire `json:"views"`
	}
	decodeJSON(t, listResp, &listOut)
	if len(listOut.Views) != 2 {
		t.Fatalf("LIST: got %d views want 2", len(listOut.Views))
	}

	// LIST narrowed by subject.
	narrowedResp := doJSON(t, "GET", ts.URL+"/v1/scopes/views?subject_kind=agent&subject_id=agent-1", pt, nil)
	var narrowedOut struct {
		Views []viewWire `json:"views"`
	}
	decodeJSON(t, narrowedResp, &narrowedOut)
	if len(narrowedOut.Views) != 1 {
		t.Fatalf("LIST narrowed: got %d views want 1", len(narrowedOut.Views))
	}

	// DELETE.
	delResp := doJSON(t, "DELETE", ts.URL+"/v1/scopes/views/agent/agent-1/work", pt, nil)
	drainClose(delResp.Body)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: got %d", delResp.StatusCode)
	}
	delAgainResp := doJSON(t, "DELETE", ts.URL+"/v1/scopes/views/agent/agent-1/work", pt, nil)
	drainClose(delAgainResp.Body)
	if delAgainResp.StatusCode != http.StatusNotFound {
		t.Errorf("double DELETE: got %d want 404", delAgainResp.StatusCode)
	}
}

// TestViews_CreateNilService proves 503 on POST when no views service is
// wired (mirrors TestViews_NilService, which only covers GET).
func TestViews_CreateNilService(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-nilv-post")

	resp := doJSON(t, "POST", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "a1", "allow_topics": []string{"x"},
	})
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("create, nil views service: got %d want 503", resp.StatusCode)
	}
}

// TestViews_UpdateNilService proves 503 on PUT when no views service is wired.
func TestViews_UpdateNilService(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-nilv-put")

	resp := doJSON(t, "PUT", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "a1", "allow_topics": []string{"x"},
	})
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("update, nil views service: got %d want 503", resp.StatusCode)
	}
}

// TestViews_DeleteNilService proves 503 on DELETE when no views service is wired.
func TestViews_DeleteNilService(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-nilv-del")

	resp := doJSON(t, "DELETE", ts.URL+"/v1/scopes/views/agent/a1/default", pt, nil)
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("delete, nil views service: got %d want 503", resp.StatusCode)
	}
}

// TestViews_CreateWrongContentType covers the requireJSON path in handleCreateView.
func TestViews_CreateWrongContentType(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-views-ct")

	req, _ := http.NewRequest("POST", ts.URL+"/v1/scopes/views",
		strings.NewReader(`{"subject_kind":"agent","subject_id":"a1","allow_topics":["x"]}`))
	req.Header.Set("Authorization", "Bearer "+pt)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST wrong content-type: %v", err)
	}
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("create wrong content-type: got %d want 415", resp.StatusCode)
	}
}

// TestViews_UpdateWrongContentType covers the requireJSON path in handleUpdateView.
func TestViews_UpdateWrongContentType(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-views-ct2")

	req, _ := http.NewRequest("PUT", ts.URL+"/v1/scopes/views",
		strings.NewReader(`{"subject_kind":"agent","subject_id":"a1","allow_topics":["x"]}`))
	req.Header.Set("Authorization", "Bearer "+pt)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT wrong content-type: %v", err)
	}
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("update wrong content-type: got %d want 415", resp.StatusCode)
	}
}

// TestViews_CreateMalformedJSON covers the decode-error path in handleCreateView.
func TestViews_CreateMalformedJSON(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-views-mj")

	req, _ := http.NewRequest("POST", ts.URL+"/v1/scopes/views", strings.NewReader(`{not json`))
	req.Header.Set("Authorization", "Bearer "+pt)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST malformed json: %v", err)
	}
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("create malformed json: got %d want 400", resp.StatusCode)
	}
}

// TestViews_UpdateMalformedJSON covers the decode-error path in handleUpdateView.
func TestViews_UpdateMalformedJSON(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-views-mj2")

	req, _ := http.NewRequest("PUT", ts.URL+"/v1/scopes/views", strings.NewReader(`{not json`))
	req.Header.Set("Authorization", "Bearer "+pt)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT malformed json: %v", err)
	}
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("update malformed json: got %d want 400", resp.StatusCode)
	}
}

// TestViews_CreateEmptyRejected proves 400 when neither allow_topics nor
// deny_topics is set (ErrEmptyPolicy — the junction table cannot represent an
// empty view).
func TestViews_CreateEmptyRejected(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-views-empty")

	resp := doJSON(t, "POST", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "a1",
	})
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST empty view: got %d want 400", resp.StatusCode)
	}
}

// TestViews_CreateInvalidSubjectKind proves 400 on an unrecognized subject_kind.
func TestViews_CreateInvalidSubjectKind(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-views-badkind")

	resp := doJSON(t, "POST", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "bogus", "subject_id": "a1", "allow_topics": []string{"x"},
	})
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST invalid subject_kind: got %d want 400", resp.StatusCode)
	}
}

// TestViews_EventEmittedOnceFromCore proves the HTTP admin surface produces
// exactly ONE governance audit event per mutation, from views.Service (never
// from the handler) — D-067/D-073.
func TestViews_EventEmittedOnceFromCore(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	key, pt := mustCreateAgentKey(t, st, "tenant-views-events")

	createResp := doJSON(t, "POST", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "a1", "allow_topics": []string{"x"},
	})
	drainClose(createResp.Body)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: got %d", createResp.StatusCode)
	}

	events, _, err := st.Events().List(context.Background(), identity.Scope{Tenant: key.TenantID}, 100, "")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	count := 0
	for _, ev := range events {
		if ev.Type == "view.created" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 view.created event, got %d", count)
	}
}

// ── memory_retrieve view_name apply + CredentialKeyID (keyFromContext) ────────

// setViewRetriever wires a retriever with both the agent-policy filter and the
// named-view apply path enabled, backed by the given store's real
// TopicViewStore (mirrors setAgentPolicyRetriever, agentpolicy_handler_test.go).
func setViewRetriever(t *testing.T, srv *api.Server, st store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	vi := vindex.New(st.Vectors(), 4, "test-model")
	r := retrieval.New(st.Memories(), st.Records(), vi, nil, log).
		WithAgentPolicy(st.TopicViews(), true).
		SetTopicViews(st.TopicViews(), false, "agent,key")
	srv.SetRetriever(r)
}

// TestRetrieve_ViewNameSeam proves POST /v1/retrieve's view_name arg narrows
// results according to the caller's agent-bound named view over HTTP, and
// degraded_view is false (omitted) on a clean read.
func TestRetrieve_ViewNameSeam(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewsService(t, srv, st)
	setViewRetriever(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-view-http")

	createResp := doJSON(t, "POST", ts.URL+"/v1/scopes/views", pt, map[string]interface{}{
		"subject_kind": "agent", "subject_id": "bound-agent", "view_name": "work",
		"allow_topics": []string{"auth"},
	})
	drainClose(createResp.Body)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create view: got %d", createResp.StatusCode)
	}

	scope := identity.Scope{Tenant: "tenant-view-http"}
	authID := commitTopicTaggedMemoryAPI(t, st, scope, "auth widget note qviewhttp", []string{"auth"})
	deployID := commitTopicTaggedMemoryAPI(t, st, scope, "deploy widget note qviewhttp", []string{"deploy"})

	retResp := doJSON(t, "POST", ts.URL+"/v1/retrieve", pt, map[string]interface{}{
		"query": "qviewhttp widget", "limit": 10, "agent_id": "bound-agent", "view_name": "work",
	})
	if retResp.StatusCode != http.StatusOK {
		t.Fatalf("retrieve: got %d", retResp.StatusCode)
	}
	var out struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		DegradedView bool `json:"degraded_view"`
	}
	decodeJSON(t, retResp, &out)
	if out.DegradedView {
		t.Error("expected degraded_view=false on a clean bound-view read")
	}
	gotIDs := map[string]bool{}
	for _, it := range out.Items {
		gotIDs[it.ID] = true
	}
	if !gotIDs[authID] {
		t.Error("expected the allow-topic memory in the result")
	}
	if gotIDs[deployID] {
		t.Error("the named view must have subtracted the non-allow-topic memory")
	}
}

// TestRetrieve_CredentialKeyIDNeverAWireField proves a client cannot spoof the
// "key" view subject via a JSON body field — CredentialKeyID is
// SERVER-INJECTED from keyFromContext, and any attempted "credential_key_id"
// wire field is rejected by DisallowUnknownFields (it is simply not part of
// retrieveRequest at all).
func TestRetrieve_CredentialKeyIDNeverAWireField(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setViewRetriever(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-view-nospoof")

	resp := doJSON(t, "POST", ts.URL+"/v1/retrieve", pt, map[string]interface{}{
		"query": "anything", "credential_key_id": "sk_someone_elses_key",
	})
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("credential_key_id as a wire field: got %d want 400 (unknown field)", resp.StatusCode)
	}
}

// commitTopicTaggedMemoryAPI inserts an active memory tagged with topics
// directly through the store (mirrors internal/mcpserver's
// commitTopicTaggedMemory helper) for the HTTP-level view seam test.
func commitTopicTaggedMemoryAPI(t *testing.T, st store.Store, scope identity.Scope, content string, topics []string) string {
	t.Helper()
	nowMs := time.Now().UnixMilli()
	id := fmt.Sprintf("01vwh%016x%04d", nowMs, len(content))
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: "fact", Content: content, Status: "active",
			Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0,
			PrivacyZone: "public", ContentHash: id, CreatedAt: nowMs, UpdatedAt: nowMs,
		},
		Topics: topics,
		Events: []store.Event{{ID: fmt.Sprintf("01vwe%016x%04d", nowMs, len(content)), Type: "memory.added", SubjectID: id, Payload: `{}`}},
		Scope:  scope,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("commitTopicTaggedMemoryAPI: %v", err)
	}
	return id
}

type viewWire struct {
	SubjectKind string   `json:"subject_kind"`
	SubjectID   string   `json:"subject_id"`
	ViewName    string   `json:"view_name"`
	AllowTopics []string `json:"allow_topics,omitempty"`
	DenyTopics  []string `json:"deny_topics,omitempty"`
	CreatedAt   int64    `json:"created_at,omitempty"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
}
