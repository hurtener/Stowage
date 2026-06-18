package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/mock" // register the mock gateway
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

type verifyBody struct {
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation"`
	Degraded    bool    `json:"degraded"`
}

// TestVerify_NoGatewayDegrades proves POST /v1/verify returns 200 unclear+degraded
// when no gateway is wired (D-036) — and a resolved citation reaches the verify path.
func TestVerify_NoGatewayDegrades(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t) // no SetGateway → s.gw nil
	tenant := "tenant-verify-nogw"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	_ = st.Memories().Insert(ctx, scope, store.Memory{
		ID: "vm1", Kind: "fact", Content: "x", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1,
	})
	_ = st.Injections().Append(ctx, scope, []store.Injection{{ID: "vc1", ResponseID: "r1", MemoryID: "vm1", CreatedAt: 1}})

	resp, err := doRequest(t, http.MethodPost, ts.URL+"/v1/verify", bytes.NewBufferString(`{"claim":"x is true","citations":["vc1"]}`), agentKey)
	if err != nil {
		t.Fatalf("POST /v1/verify: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body verifyBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Verdict != "unclear" || !body.Degraded {
		t.Errorf("no-gateway verify: want unclear+degraded, got %+v", body)
	}
}

// TestVerify_WithGateway exercises the full path: a wired mock gateway + a resolved
// citation ⇒ a real (non-degraded) verdict.
func TestVerify_WithGateway(t *testing.T) {
	t.Parallel()
	srv, ts, st := newTopicsTestServer(t)
	gw, err := gateway.Open(context.Background(), config.GatewayConfig{Driver: "mock", EmbedDims: 8}, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open mock gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close(context.Background()) })
	srv.SetGateway(gw)

	tenant := "tenant-verify-gw"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	_ = st.Memories().Insert(ctx, scope, store.Memory{
		ID: "vm2", Kind: "fact", Content: "Water boils at 100C.", Status: "active", Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: 1, UpdatedAt: 1,
	})
	_ = st.Injections().Append(ctx, scope, []store.Injection{{ID: "vc2", ResponseID: "r1", MemoryID: "vm2", CreatedAt: 1}})

	resp, err := doRequest(t, http.MethodPost, ts.URL+"/v1/verify", bytes.NewBufferString(`{"claim":"Water boils at 100C.","citations":["vc2"]}`), agentKey)
	if err != nil {
		t.Fatalf("POST /v1/verify: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body verifyBody
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Degraded {
		t.Errorf("gateway present ⇒ not degraded, got %+v", body)
	}
	if body.Verdict == "" {
		t.Error("verdict must not be empty")
	}
}

// TestVerify_MissingClaim proves 400 when claim is absent.
func TestVerify_MissingClaim(t *testing.T) {
	t.Parallel()
	_, ts, st := newTopicsTestServer(t)
	tenant := "tenant-verify-noclaim"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	resp, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/verify", bytes.NewBufferString(`{"citations":["x"]}`), agentKey)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing claim: want 400, got %d", resp.StatusCode)
	}

	// Malformed JSON ⇒ 400.
	r2, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/verify", bytes.NewBufferString(`{bad`), agentKey)
	defer drainClose(r2.Body)
	if r2.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed body: want 400, got %d", r2.StatusCode)
	}
}
