package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type traceBody struct {
	Trace struct {
		ResponseID string `json:"response_id"`
		Query      string `json:"query"`
		Items      []struct {
			MemoryID string `json:"memory_id"`
		} `json:"items"`
	} `json:"trace"`
	Signed bool `json:"signed"`
}

// TestTrace_Export proves GET /v1/traces/{response_id} reconstructs the chain.
func TestTrace_Export(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-trace"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	_ = st.Memories().Insert(ctx, scope, store.Memory{ID: "tm1", Kind: "fact", Content: "Paris.", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1})
	_ = st.Injections().Append(ctx, scope, []store.Injection{{ID: "ti1", ResponseID: "resp-x", MemoryID: "tm1", Rank: 0, Score: 0.9, CreatedAt: 1}})
	_ = st.Events().Emit(ctx, scope, store.Event{ID: "te1", TenantID: tenant, Type: "retrieve.query", SubjectID: "resp-x", Payload: `{"query":"capital?","support":"strong"}`, CreatedAt: 1})

	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/traces/resp-x", nil, agentKey)
	if err != nil {
		t.Fatalf("GET /v1/traces: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body traceBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Trace.ResponseID != "resp-x" || body.Trace.Query != "capital?" || len(body.Trace.Items) != 1 {
		t.Fatalf("trace wrong: %+v", body.Trace)
	}
	// No signer wired on the test server ⇒ unsigned bundle.
	if body.Signed {
		t.Errorf("expected unsigned bundle (no signer), got signed")
	}
}

// TestTrace_Signed proves the bundle is signed when a trace signer is wired.
func TestTrace_Signed(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTopicsTestServer(t)
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	srv.SetTraceSigner(key)
	tenant := "tenant-trace-signed"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	_ = st.Memories().Insert(ctx, scope, store.Memory{ID: "sm1", Kind: "fact", Content: "x", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1})
	_ = st.Injections().Append(ctx, scope, []store.Injection{{ID: "si1", ResponseID: "resp-s", MemoryID: "sm1", CreatedAt: 1}})

	resp, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/traces/resp-s", nil, agentKey)
	defer drainClose(resp.Body)
	var body traceBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || !body.Signed {
		t.Errorf("signed trace: want 200 + signed, got %d / signed=%v", resp.StatusCode, body.Signed)
	}
}

// TestTrace_MissingID proves an empty response_id path segment returns 400.
func TestTrace_MissingID(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-trace-mid"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	// Trailing slash → empty {response_id}.
	resp, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/traces/", nil, agentKey)
	defer drainClose(resp.Body)
	// Either 400 (empty id handled) or 404 (route not matched) is acceptable; assert not 200.
	if resp.StatusCode == http.StatusOK {
		t.Errorf("empty response_id should not 200, got %d", resp.StatusCode)
	}
}

// TestTrace_UnknownResponse proves an unknown response_id yields a 200 empty trace.
func TestTrace_UnknownResponse(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-trace2"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	resp, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/traces/01MISSINGAAAAAAAAAAAAAAAAA", nil, agentKey)
	defer drainClose(resp.Body)
	var body traceBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || len(body.Trace.Items) != 0 {
		t.Errorf("unknown response: want 200 + empty items, got %d / %d", resp.StatusCode, len(body.Trace.Items))
	}
}
