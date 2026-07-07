// mcp_runsink_test.go is the §17 integration bar for phase ae12 (the Harbor
// run-completion sink, memory_ingest_run, D-153): it dispatches a REAL marshaled
// Harbor-shaped RunCompletionPayload (time.Time fields ⇒ RFC3339 on the wire)
// as a per-call-bearer tools/call over the ae11 open handshake (D-152), and
// proves the transcript lands as verbatim records stamped by the VERIFIED JWT
// scope — not the payload — with cross-user isolation.
//
// Real drivers throughout: a real sqlite store + live pipeline (startStack, with
// the buffer Stage wired for the eager flush), a real static JWKS file, the real
// auth Validator over a test-only RSA signer, and the real go-sdk streamable HTTP
// client behind MethodAwareAuthMiddleware. Store rows are asserted by querying the
// store directly (the P3 backstop). Runs under -race.
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
)

// harborRunEntry / harborRunPayload mirror Harbor's TranscriptEntry /
// RunCompletionPayload (format_version 1) — defined locally because Stowage never
// imports Harbor (cross-module internal/, separate product). The time.Time fields
// marshal to RFC3339 strings, exactly as Harbor's hook dispatches them.
type harborRunEntry struct {
	Role    string     `json:"role"`
	Kind    string     `json:"kind"`
	Content string     `json:"content"`
	Step    int        `json:"step"`
	At      *time.Time `json:"at,omitempty"`
}

type harborRunPayload struct {
	FormatVersion   int              `json:"format_version"`
	TenantID        string           `json:"tenant_id"`
	UserID          string           `json:"user_id"`
	SessionID       string           `json:"session_id"`
	RunID           string           `json:"run_id"`
	AgentID         string           `json:"agent_id,omitempty"`
	Outcome         string           `json:"outcome"`
	StartedAt       time.Time        `json:"started_at"`
	CompletedAt     time.Time        `json:"completed_at"`
	DurationMS      int64            `json:"duration_ms"`
	StepCount       int              `json:"step_count"`
	ToolInvocations int              `json:"tool_invocations"`
	Conversation    []harborRunEntry `json:"conversation"`
}

// startRunSinkMCP boots a jwt-mode MCP-over-HTTP server (real store + pipeline,
// the buffer Stage wired for the eager flush) behind MethodAwareAuthMiddleware.
// Returns the live URL and the boot Stack (for direct store assertions).
func startRunSinkMCP(t *testing.T, jwksPath string) (string, *boot.Stack) {
	t.Helper()
	cfg := baseConfig(t)
	cfg.Auth = config.AuthConfig{
		Mode:     "jwt",
		Issuer:   "harbor",
		Audience: "stowage",
		JWKS:     config.JWKSConfig{File: jwksPath, MaxStale: 3600},
	}
	stk, p := startStack(t, cfg)
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	keys, err := auth.NewJWKSKeySet(context.Background(), auth.JWKSSource{File: jwksPath}, 3600*time.Second)
	if err != nil {
		t.Fatalf("NewJWKSKeySet(file): %v", err)
	}
	v, err := auth.NewValidator(keys, auth.WithIssuer("harbor"), auth.WithAudience("stowage"))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	authn := auth.NewJWTAuthenticator(v)

	mcpSrv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store: stk.Store, Retriever: stk.Retriever, TopicSvc: stk.TopicSvc,
		PipelineIn: p.In, PipelineStage: p.Stage,
		Log: stk.Log, ScopeFn: mcpserver.CtxScopeFn(), Profile: cfg.Profile,
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	handler, err := mcpSrv.HTTPHandler(&server.HTTPOptions{Stateless: true})
	if err != nil {
		t.Fatalf("HTTPHandler: %v", err)
	}
	ts := httptest.NewServer(mcpserver.MethodAwareAuthMiddleware(authn, handler))
	t.Cleanup(ts.Close)
	return ts.URL, stk
}

// dialRunSink opens the bearer-less handshake and returns a live session whose
// tools/call carries the given per-call bearer (the motivating host shape).
func dialRunSink(t *testing.T, url, token string) *mcpsdk.ClientSession {
	t.Helper()
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint:             url,
		HTTPClient:           &http.Client{Transport: perCallBearerRT{base: http.DefaultTransport, token: token}},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ae12-runsink-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("bearer-less handshake failed: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// TestMCPRunSink_TranscriptLandsScopedByJWT dispatches a Harbor-shaped payload
// over alice's bearer and proves: one verbatim record per entry (in order), the
// store rows carry the JWT tenant/user (not the payload), occurred_at honors
// entry.at (else completed_at), outcome is stamped, and bob's scope sees none of
// alice's rows (criteria 1, 2, 5).
func TestMCPRunSink_TranscriptLandsScopedByJWT(t *testing.T) {
	key := newAuthJWTKey(t)
	jwksPath := writeStaticJWKS(t, key)
	url, stk := startRunSinkMCP(t, jwksPath)

	// The JWT carries session "s1"; align the payload session so the D-124
	// scope-authoritative write is unambiguous (scope session wins regardless).
	aliceToken := key.mint(t, "runsink-tenant", "alice", "s1", time.Now().Add(time.Hour), []string{"read"})
	session := dialRunSink(t, url, aliceToken)

	at1 := time.Date(2026, 7, 6, 10, 1, 0, 0, time.UTC)
	at2 := time.Date(2026, 7, 6, 10, 2, 0, 0, time.UTC)
	completed := time.Date(2026, 7, 6, 10, 9, 0, 0, time.UTC)
	payload := harborRunPayload{
		FormatVersion:   1,
		TenantID:        "runsink-tenant",
		UserID:          "alice",
		SessionID:       "s1",
		RunID:           "run-ae12-1",
		AgentID:         "agent-x",
		Outcome:         "goal",
		StartedAt:       time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC),
		CompletedAt:     completed,
		DurationMS:      540000,
		StepCount:       2,
		ToolInvocations: 1,
		Conversation: []harborRunEntry{
			{Role: "user", Kind: "goal", Content: "book me a flight", Step: 0, At: &at1},
			{Role: "assistant", Kind: "tool", Content: "search_flights: ok", Step: 0, At: &at2},
			{Role: "assistant", Kind: "final_answer", Content: "done — booked", Step: 1}, // omits at → completed_at
		},
	}

	res, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "memory_ingest_run",
		Arguments: payload,
	})
	if err != nil {
		t.Fatalf("tools/call memory_ingest_run failed: %v", err)
	}
	if res.IsError {
		cb, _ := json.Marshal(res.Content)
		t.Fatalf("memory_ingest_run returned IsError: %s", cb)
	}
	var out mcpserver.IngestRunOutput
	decodeStructured(t, res, &out)
	if len(out.IDs) != 3 {
		t.Fatalf("want 3 ids, got %d", len(out.IDs))
	}
	if !out.Flushed {
		t.Error("eager flush should succeed (Flushed=true) with a wired pipeline stage")
	}

	// Store backstop: alice's rows, in order, JWT-scoped, outcome stamped.
	aliceScope := identity.Scope{Tenant: "runsink-tenant", User: "alice"}
	recs, _, err := stk.Store.Records().ListBySession(context.Background(), aliceScope, "s1", "", 100, "")
	if err != nil {
		t.Fatalf("ListBySession(alice): %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 stored records, got %d", len(recs))
	}
	wantContent := []string{"book me a flight", "search_flights: ok", "done — booked"}
	for i, r := range recs {
		if r.Content != wantContent[i] {
			t.Errorf("record[%d] content = %q, want %q (order not preserved)", i, r.Content, wantContent[i])
		}
		if r.TenantID != "runsink-tenant" || r.UserID != "alice" {
			t.Errorf("record[%d] scoped %q/%q, want runsink-tenant/alice (D-124 JWT scope)", i, r.TenantID, r.UserID)
		}
		if r.Outcome != "success" || r.OutcomeDetail != "goal" {
			t.Errorf("record[%d] outcome/detail = %q/%q, want success/goal", i, r.Outcome, r.OutcomeDetail)
		}
	}
	// occurred_at honors entry.at, and the no-`at` entry inherits completed_at.
	if recs[0].OccurredAt != at1.UnixMilli() {
		t.Errorf("record[0] occurred_at = %d, want entry at %d", recs[0].OccurredAt, at1.UnixMilli())
	}
	if recs[2].OccurredAt != completed.UnixMilli() {
		t.Errorf("record[2] occurred_at = %d, want completed_at %d", recs[2].OccurredAt, completed.UnixMilli())
	}

	// P3 cross-user negative: bob's scope sees none of alice's rows.
	bobScope := identity.Scope{Tenant: "runsink-tenant", User: "bob"}
	bobRecs, _, err := stk.Store.Records().ListBySession(context.Background(), bobScope, "s1", "", 100, "")
	if err != nil {
		t.Fatalf("ListBySession(bob): %v", err)
	}
	if len(bobRecs) != 0 {
		t.Fatalf("P3 LEAK — bob's scope saw %d of alice's records", len(bobRecs))
	}
}

// TestMCPRunSink_FormatVersion2Rejected proves the pin: a format_version-2 payload
// is a tool error (criterion 4) — a future Harbor v2 fails loudly, never misparses.
func TestMCPRunSink_FormatVersion2Rejected(t *testing.T) {
	key := newAuthJWTKey(t)
	jwksPath := writeStaticJWKS(t, key)
	url, stk := startRunSinkMCP(t, jwksPath)

	aliceToken := key.mint(t, "runsink-tenant", "alice", "s1", time.Now().Add(time.Hour), []string{"read"})
	session := dialRunSink(t, url, aliceToken)

	payload := harborRunPayload{
		FormatVersion: 2, // unsupported
		TenantID:      "runsink-tenant",
		UserID:        "alice",
		SessionID:     "s1",
		RunID:         "run-ae12-v2",
		Outcome:       "goal",
		StartedAt:     time.Now(),
		CompletedAt:   time.Now(),
		Conversation:  []harborRunEntry{{Role: "user", Content: "hi"}},
	}
	res, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "memory_ingest_run",
		Arguments: payload,
	})
	if err != nil {
		t.Fatalf("tools/call transport error: %v", err)
	}
	if !res.IsError {
		t.Fatal("format_version 2 must produce a tool error")
	}
	// Nothing was written.
	recs, _, err := stk.Store.Records().ListBySession(context.Background(),
		identity.Scope{Tenant: "runsink-tenant", User: "alice"}, "s1", "", 100, "")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("format_version 2 must not write records, got %d", len(recs))
	}
}
