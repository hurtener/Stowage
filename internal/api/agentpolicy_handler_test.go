package api_test

// agentpolicy_handler_test.go — HTTP-level tests for the read-time agent->topic
// policy binding endpoints (Phase ae1, D-135/D-146/D-151):
//
//	GET    /v1/scopes/agent-policies
//	PUT    /v1/scopes/agent-policies
//	GET    /v1/scopes/agent-policies/{agent_id}
//	DELETE /v1/scopes/agent-policies/{agent_id}

import (
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// setAgentPolicyRetriever wires a retriever with the agent-policy store wired and
// the filter enabled, backed by the given store's real TopicViewStore.
func setAgentPolicyRetriever(t *testing.T, srv *api.Server, st store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	vi := vindex.New(st.Vectors(), 4, "test-model")
	r := retrieval.New(st.Memories(), st.Records(), vi, nil, log).WithAgentPolicy(st.TopicViews(), true)
	srv.SetRetriever(r)
}

// TestAgentPolicy_Unauthorized proves 401 with no auth header on every route.
func TestAgentPolicy_Unauthorized(t *testing.T) {
	t.Parallel()
	_, ts, _ := newTestServer(t)

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/v1/scopes/agent-policies"},
		{"PUT", "/v1/scopes/agent-policies"},
		{"GET", "/v1/scopes/agent-policies/agent-1"},
		{"DELETE", "/v1/scopes/agent-policies/agent-1"},
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

// TestAgentPolicy_NilRetriever proves 503 when no retriever is wired.
func TestAgentPolicy_NilRetriever(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "tenant-nilr-ap")

	resp := doJSON(t, "GET", ts.URL+"/v1/scopes/agent-policies", pt, nil)
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("list, nil retriever: got %d want 503", resp.StatusCode)
	}
}

// TestAgentPolicy_CRUD exercises put/get/list/delete end to end over HTTP.
func TestAgentPolicy_CRUD(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setAgentPolicyRetriever(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-ap-crud")

	// PUT (create).
	putResp := doJSON(t, "PUT", ts.URL+"/v1/scopes/agent-policies", pt, map[string]interface{}{
		"agent_id":     "agent-1",
		"allow_topics": []string{"auth", "billing"},
		"deny_topics":  []string{"secrets"},
	})
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: got %d", putResp.StatusCode)
	}
	var putOut agentPolicyWire
	decodeJSON(t, putResp, &putOut)
	if putOut.AgentID != "agent-1" || len(putOut.AllowTopics) != 2 {
		t.Fatalf("PUT: unexpected body: %+v", putOut)
	}

	// GET one.
	getResp := doJSON(t, "GET", ts.URL+"/v1/scopes/agent-policies/agent-1", pt, nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: got %d", getResp.StatusCode)
	}
	var getOut agentPolicyWire
	decodeJSON(t, getResp, &getOut)
	if len(getOut.DenyTopics) != 1 || getOut.DenyTopics[0] != "secrets" {
		t.Fatalf("GET: unexpected body: %+v", getOut)
	}

	// GET missing -> 404.
	missResp := doJSON(t, "GET", ts.URL+"/v1/scopes/agent-policies/never-bound", pt, nil)
	drainClose(missResp.Body)
	if missResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET missing: got %d want 404", missResp.StatusCode)
	}

	// LIST.
	putResp2 := doJSON(t, "PUT", ts.URL+"/v1/scopes/agent-policies", pt, map[string]interface{}{
		"agent_id": "agent-2", "allow_topics": []string{"x"},
	})
	drainClose(putResp2.Body)
	listResp := doJSON(t, "GET", ts.URL+"/v1/scopes/agent-policies", pt, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("LIST: got %d", listResp.StatusCode)
	}
	var listOut struct {
		Policies []agentPolicyWire `json:"policies"`
	}
	decodeJSON(t, listResp, &listOut)
	if len(listOut.Policies) != 2 {
		t.Fatalf("LIST: got %d policies want 2", len(listOut.Policies))
	}

	// Re-PUT (atomic replace).
	replaceResp := doJSON(t, "PUT", ts.URL+"/v1/scopes/agent-policies", pt, map[string]interface{}{
		"agent_id": "agent-1", "allow_topics": []string{"only-this"},
	})
	var replaceOut agentPolicyWire
	decodeJSON(t, replaceResp, &replaceOut)
	if len(replaceOut.AllowTopics) != 1 || len(replaceOut.DenyTopics) != 0 {
		t.Errorf("replace: expected fully-replaced sets, got %+v", replaceOut)
	}

	// DELETE.
	delResp := doJSON(t, "DELETE", ts.URL+"/v1/scopes/agent-policies/agent-1", pt, nil)
	drainClose(delResp.Body)
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: got %d", delResp.StatusCode)
	}
	delAgainResp := doJSON(t, "DELETE", ts.URL+"/v1/scopes/agent-policies/agent-1", pt, nil)
	drainClose(delAgainResp.Body)
	if delAgainResp.StatusCode != http.StatusNotFound {
		t.Errorf("double DELETE: got %d want 404", delAgainResp.StatusCode)
	}
}

// TestAgentPolicy_PutMissingAgentID proves 400 when agent_id is omitted.
func TestAgentPolicy_PutMissingAgentID(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTestServer(t)
	setAgentPolicyRetriever(t, srv, st)
	_, pt := mustCreateAgentKey(t, st, "tenant-ap-missing")

	resp := doJSON(t, "PUT", ts.URL+"/v1/scopes/agent-policies", pt, map[string]interface{}{
		"allow_topics": []string{"auth"},
	})
	drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("PUT missing agent_id: got %d want 400", resp.StatusCode)
	}
}

type agentPolicyWire struct {
	AgentID     string   `json:"agent_id"`
	AllowTopics []string `json:"allow_topics,omitempty"`
	DenyTopics  []string `json:"deny_topics,omitempty"`
	CreatedAt   int64    `json:"created_at,omitempty"`
	UpdatedAt   int64    `json:"updated_at,omitempty"`
}
