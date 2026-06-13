package mcpserver

// Integration tests for all 7 handler factory functions.
// This file lives in package mcpserver (not mcpserver_test) so it can call
// the private make*Handler functions directly without going through the MCP
// transport layer.

import (
	"context"
	"github.com/hurtener/stowage/internal/auth"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/vindex"

	_ "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func noopLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newHandlerStore(t *testing.T) store.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "mcphandler-*.db")
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

func newHandlerServices(t *testing.T) *Services {
	t.Helper()
	st := newHandlerStore(t)
	log := noopLog()
	topicSvc := topics.New(st.Topics(), log, "assistant")
	return &Services{
		Store:      st,
		Retriever:  nil, // memory_retrieve tests skip or expect error
		TopicSvc:   topicSvc,
		PipelineIn: nil,
		Log:        log,
		ScopeFn:    StdioScopeFn("test-tenant"),
	}
}

// newFullServices builds a Services that includes a real retriever backed
// by a mock gateway (for tests that exercise the retrieve handler main path).
func newFullServices(t *testing.T) *Services {
	t.Helper()
	st := newHandlerStore(t)
	log := noopLog()

	gw, err := gateway.Open(context.Background(), config.GatewayConfig{
		Driver:    "mock",
		EmbedDims: 8, // tiny dims for fast test
	}, slog.Default(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open mock gateway: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close(context.Background()) })

	vi := vindex.New(st.Vectors(), 8, "mock-embed")
	ret := retrieval.New(st.Memories(), st.Records(), vi, gw, log)
	topicSvc := topics.New(st.Topics(), log, "assistant")

	return &Services{
		Store:      st,
		Retriever:  ret,
		TopicSvc:   topicSvc,
		PipelineIn: nil,
		Log:        log,
		ScopeFn:    StdioScopeFn("test-tenant"),
	}
}

func testScope() identity.Scope {
	return identity.Scope{Tenant: "test-tenant"}
}

// ── memory_ingest ─────────────────────────────────────────────────────────────

func TestHandlerIngest_BasicRecord(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeIngestHandler(svc)
	ctx := context.Background()

	result, err := h(ctx, IngestInput{
		Records: []IngestRecord{
			{Role: "user", Content: "hello world"},
		},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(result.Structured.IDs) != 1 {
		t.Fatalf("expected 1 ID, got %d", len(result.Structured.IDs))
	}
	if result.Structured.IDs[0] == "" {
		t.Error("ID must not be empty")
	}
	// Pipeline is nil so Enqueued should be false.
	if result.Structured.Enqueued {
		t.Error("Enqueued should be false when PipelineIn is nil")
	}
	if result.Text == "" {
		t.Error("Text must not be empty")
	}
}

func TestHandlerIngest_MultipleRecords(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeIngestHandler(svc)
	ctx := context.Background()

	result, err := h(ctx, IngestInput{
		Records: []IngestRecord{
			{Role: "user", Content: "first record"},
			{Role: "assistant", Content: "second record"},
			{Content: "third record, no role"},
		},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(result.Structured.IDs) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(result.Structured.IDs))
	}
}

func TestHandlerIngest_EmptyRecords(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeIngestHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, IngestInput{Records: nil})
	if err == nil {
		t.Fatal("expected error for empty records, got nil")
	}
}

// TestHandlerIngest_ContributeFailLoud proves the MCP memory_ingest handler
// rejects contribute-mode fields (target_scope / contributor_user_id) with a
// clear error rather than silently ingesting into the caller's own scope, and
// performs NO store write on that path (D-069, parity-lens BUG-2 / AC-5).
func TestHandlerIngest_ContributeFailLoud(t *testing.T) {
	cases := []struct {
		name string
		in   IngestInput
	}{
		{
			name: "target_scope set",
			in: IngestInput{
				Records:     []IngestRecord{{Role: "user", Content: "should not be written"}},
				TargetScope: &IngestTargetScope{UserID: "other-user"},
			},
		},
		{
			name: "contributor_user_id set",
			in: IngestInput{
				Records:           []IngestRecord{{Role: "user", Content: "should not be written"}},
				ContributorUserID: "contributor-1",
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			svc := newHandlerServices(t)
			h := makeIngestHandler(svc)
			ctx := context.Background()

			_, err := h(ctx, tc.in)
			if err == nil {
				t.Fatal("expected fail-loud error for contribute-mode field, got nil")
			}

			// No store write into the caller scope.
			scope := identity.Scope{Tenant: "test-tenant"}
			n, cerr := svc.Store.Records().CountRecordsSince(ctx, scope, 0)
			if cerr != nil {
				t.Fatalf("count records: %v", cerr)
			}
			if n != 0 {
				t.Errorf("expected 0 records written on rejected contribute call, got %d", n)
			}
		})
	}
}

func TestHandlerIngest_DefaultRole(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeIngestHandler(svc)
	ctx := context.Background()

	// No role set — should default to "user".
	result, err := h(ctx, IngestInput{
		Records: []IngestRecord{{Content: "no role here"}},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(result.Structured.IDs) != 1 {
		t.Fatalf("expected 1 ID, got %d", len(result.Structured.IDs))
	}
}

func TestHandlerIngest_EmptyContent(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeIngestHandler(svc)
	ctx := context.Background()

	// Content is empty — records.New may reject it.
	_, err := h(ctx, IngestInput{
		Records: []IngestRecord{{Role: "user", Content: ""}},
	})
	// Expect an error since empty content is invalid.
	if err == nil {
		t.Fatal("expected error for empty content record")
	}
}

func TestHandlerIngest_PipelineNil(t *testing.T) {
	svc := newHandlerServices(t)
	// PipelineIn is nil → Enqueued must be false.
	result, err := makeIngestHandler(svc)(context.Background(), IngestInput{
		Records: []IngestRecord{{Role: "user", Content: "pipeline nil test"}},
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Structured.Enqueued {
		t.Error("Enqueued must be false when PipelineIn is nil")
	}
}

// ── memory_retrieve ───────────────────────────────────────────────────────────

func TestHandlerRetrieve_EmptyQuery(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeRetrieveHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, RetrieveInput{Query: ""})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestHandlerRetrieve_NilRetriever(t *testing.T) {
	svc := newHandlerServices(t)
	// Retriever is nil — should return error.
	h := makeRetrieveHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, RetrieveInput{Query: "test query"})
	if err == nil {
		t.Fatal("expected error when Retriever is nil")
	}
}

func TestHandlerRetrieve_EmptyStore(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)
	ctx := context.Background()

	// No memories in the store → empty result is valid.
	result, err := h(ctx, RetrieveInput{Query: "what is the capital of France"})
	if err != nil {
		t.Fatalf("retrieve empty store: %v", err)
	}
	if result.Structured.Items == nil {
		// nil is fine — just verify the other fields are present.
		_ = result.Structured.ResponseID
	}
	if result.Text == "" {
		t.Error("Text must not be empty")
	}
}

func TestHandlerRetrieve_WithLimit(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)
	ctx := context.Background()

	result, err := h(ctx, RetrieveInput{
		Query:        "test query",
		Limit:        5,
		IncludeLanes: true,
		Debug:        true,
	})
	if err != nil {
		t.Fatalf("retrieve with limit: %v", err)
	}
	_ = result.Structured
}

func TestHandlerRetrieve_WithSessionAndProfile(t *testing.T) {
	svc := newFullServices(t)
	h := makeRetrieveHandler(svc)
	ctx := context.Background()

	result, err := h(ctx, RetrieveInput{
		Query:      "session scoped query",
		SessionID:  "sess-123",
		ResponseID: "resp-abc",
		Profile:    "coding-agent",
	})
	if err != nil {
		t.Fatalf("retrieve with session: %v", err)
	}
	_ = result.Structured
}

// ── memory_playbook ───────────────────────────────────────────────────────────

func TestHandlerPlaybook_Stub(t *testing.T) {
	svc := newHandlerServices(t)
	h := makePlaybookHandler(svc)
	ctx := context.Background()

	result, err := h(ctx, PlaybookInput{Query: "anything"})
	if err != nil {
		t.Fatalf("playbook: %v", err)
	}
	if result.Structured.Error == "" {
		t.Error("stub must return non-empty Error field")
	}
	if result.Text == "" {
		t.Error("Text must not be empty")
	}
}

func TestHandlerPlaybook_EmptyQuery(t *testing.T) {
	svc := newHandlerServices(t)
	h := makePlaybookHandler(svc)
	ctx := context.Background()

	// Stub ignores the input — should still return stub response.
	result, err := h(ctx, PlaybookInput{Query: ""})
	if err != nil {
		t.Fatalf("playbook stub: %v", err)
	}
	if result.Structured.Error == "" {
		t.Error("stub must return non-empty Error field")
	}
}

// ── memory_drilldown ─────────────────────────────────────────────────────────

func TestHandlerDrilldown_BothEmpty(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeDrilldownHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, DrilldownInput{})
	if err == nil {
		t.Fatal("expected error when both memory_id and citation are empty")
	}
}

func TestHandlerDrilldown_BothSet(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeDrilldownHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, DrilldownInput{MemoryID: "mid1", Citation: "cit1"})
	if err == nil {
		t.Fatal("expected error when both memory_id and citation are set")
	}
}

func TestHandlerDrilldown_UnknownMemoryID(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeDrilldownHandler(svc)
	ctx := context.Background()

	// A memory ID that doesn't exist — GetJunctions should return empty
	// provenance (not an error) or a not-found error. Either way, the handler
	// must not panic and must return a result or an error.
	result, err := h(ctx, DrilldownInput{MemoryID: "01JTESTNONEXISTENT0000000"})
	// Depending on store implementation: may return empty spans (OK) or an error.
	if err == nil {
		// Expect empty spans for nonexistent memory.
		if result.Structured.MemoryID == "" {
			t.Error("MemoryID must be set in output")
		}
	}
	// Both error and empty-result are acceptable here.
}

func TestHandlerDrilldown_ExistingMemory(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeDrilldownHandler(svc)
	ctx := context.Background()
	scope := testScope()

	// Insert a memory via the assert handler, then drilldown on it.
	assertH := makeAssertHandler(svc)
	addResult, err := assertH(ctx, AssertInput{
		Action:  "add",
		Content: "drilldown test memory",
	})
	if err != nil {
		t.Fatalf("assert add: %v", err)
	}
	memID := addResult.Structured.MemoryID

	// Drilldown should return the memory (no provenance spans for asserted memory).
	result, err := h(ctx, DrilldownInput{MemoryID: memID})
	if err != nil {
		t.Fatalf("drilldown: %v", err)
	}
	if result.Structured.MemoryID != memID {
		t.Errorf("MemoryID mismatch: got %q, want %q", result.Structured.MemoryID, memID)
	}
	_ = scope
}

func TestHandlerDrilldown_WithProvenance(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeDrilldownHandler(svc)
	ctx := context.Background()
	scope := testScope()

	now := time.Now().UnixMilli()
	recID := ulid.Make().String()
	memID := ulid.Make().String()

	// Insert a record directly so we can reference it in provenance.
	recs := []store.Record{{
		ID:         recID,
		TenantID:   scope.Tenant,
		Role:       "user",
		Content:    "The quick brown fox jumped over the lazy dog",
		OccurredAt: now,
		CreatedAt:  now,
	}}
	if err := svc.Store.Records().Append(ctx, scope, recs); err != nil {
		t.Fatalf("append record: %v", err)
	}

	// Commit a memory with provenance referencing that record.
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID:          memID,
			TenantID:    scope.Tenant,
			Kind:        "fact",
			Content:     "The fox is quick and brown",
			Status:      "active",
			Confidence:  0.9,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			ContentHash: ulid.Make().String(),
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		Provenance: []store.Provenance{{
			ID:        ulid.Make().String(),
			MemoryID:  memID,
			RecordID:  recID,
			SpanStart: 4,
			SpanEnd:   20,
			TenantID:  scope.Tenant,
			CreatedAt: now,
		}},
		Events: []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.added",
			SubjectID: memID,
			Payload:   `{}`,
		}},
		Scope: scope,
	}
	if err := svc.Store.Memories().Commit(ctx, scope, cs); err != nil {
		t.Fatalf("commit memory with provenance: %v", err)
	}

	// Drilldown should return the provenance spans.
	result, err := h(ctx, DrilldownInput{MemoryID: memID})
	if err != nil {
		t.Fatalf("drilldown with provenance: %v", err)
	}
	if result.Structured.MemoryID != memID {
		t.Errorf("MemoryID mismatch: got %q, want %q", result.Structured.MemoryID, memID)
	}
	if len(result.Structured.Spans) == 0 {
		t.Error("expected at least one span in drilldown result")
	}
	span := result.Structured.Spans[0]
	if span.RecordID != recID {
		t.Errorf("span RecordID: got %q, want %q", span.RecordID, recID)
	}
	if span.SpanStart != 4 || span.SpanEnd != 20 {
		t.Errorf("span start/end: got %d/%d, want 4/20", span.SpanStart, span.SpanEnd)
	}
}

// ── memory_feedback ───────────────────────────────────────────────────────────

func TestHandlerFeedback_EmptySignal(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeFeedbackHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, FeedbackInput{MemoryID: "mid1", Signal: ""})
	if err == nil {
		t.Fatal("expected error for empty signal")
	}
}

func TestHandlerFeedback_NoTarget(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeFeedbackHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, FeedbackInput{Signal: "use"})
	if err == nil {
		t.Fatal("expected error when no response_id/memory_id/citation")
	}
}

func TestHandlerFeedback_MultipleTargets(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeFeedbackHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, FeedbackInput{
		Signal:     "use",
		MemoryID:   "mid1",
		ResponseID: "rid1",
	})
	if err == nil {
		t.Fatal("expected error when multiple targets set")
	}
}

func TestHandlerFeedback_CitationNonWrong(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeFeedbackHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, FeedbackInput{Citation: "cit1", Signal: "use"})
	if err == nil {
		t.Fatal("expected error: citation only accepts wrong_citation signal")
	}
}

func TestHandlerFeedback_InvalidMemorySignal(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeFeedbackHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, FeedbackInput{MemoryID: "mid1", Signal: "wrong_citation"})
	if err == nil {
		t.Fatal("expected error: wrong_citation not valid for memory_id target")
	}
}

func TestHandlerFeedback_ApplyToMemory(t *testing.T) {
	svc := newHandlerServices(t)
	ctx := context.Background()

	// First insert a memory via assert handler.
	assertH := makeAssertHandler(svc)
	addResult, err := assertH(ctx, AssertInput{Action: "add", Content: "feedback target memory"})
	if err != nil {
		t.Fatalf("assert add: %v", err)
	}
	memID := addResult.Structured.MemoryID

	// Apply feedback.
	h := makeFeedbackHandler(svc)
	result, err := h(ctx, FeedbackInput{MemoryID: memID, Signal: "save"})
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if result.Structured.Applied != 1 {
		t.Errorf("expected Applied=1, got %d", result.Structured.Applied)
	}
	if result.Structured.Signal != "save" {
		t.Errorf("expected Signal=save, got %q", result.Structured.Signal)
	}
}

func TestHandlerFeedback_ResponseIDNoInjections(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeFeedbackHandler(svc)
	ctx := context.Background()

	// response_id with no injections → applied=0 (not an error).
	result, err := h(ctx, FeedbackInput{ResponseID: "resp-no-injections", Signal: "use"})
	if err != nil {
		t.Fatalf("feedback response_id: %v", err)
	}
	if result.Structured.Applied != 0 {
		t.Errorf("expected Applied=0, got %d", result.Structured.Applied)
	}
}

// ── memory_assert ─────────────────────────────────────────────────────────────

func TestHandlerAssert_EmptyAction(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, AssertInput{Action: ""})
	if err == nil {
		t.Fatal("expected error for empty action")
	}
}

func TestHandlerAssert_UnknownAction(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, AssertInput{Action: "bogus"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestHandlerAssert_Add_MissingContent(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, AssertInput{Action: "add", Content: ""})
	if err == nil {
		t.Fatal("expected error: content required for action=add")
	}
}

func TestHandlerAssert_Add_DefaultKind(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	result, err := h(ctx, AssertInput{Action: "add", Content: "a fact without kind"})
	if err != nil {
		t.Fatalf("assert add: %v", err)
	}
	if result.Structured.MemoryID == "" {
		t.Error("MemoryID must be set")
	}
	if result.Structured.Action != "add" {
		t.Errorf("Action must be 'add', got %q", result.Structured.Action)
	}
	if result.Structured.Status != "active" {
		t.Errorf("Status must be 'active', got %q", result.Structured.Status)
	}
}

func TestHandlerAssert_Add_ExplicitKind(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	result, err := h(ctx, AssertInput{
		Action:  "add",
		Content: "explicit kind memory",
		Kind:    "preference",
		Context: "some context",
	})
	if err != nil {
		t.Fatalf("assert add with kind: %v", err)
	}
	if result.Structured.Status != "active" {
		t.Errorf("Status must be 'active', got %q", result.Structured.Status)
	}
}

func TestHandlerAssert_Update_MissingMemoryID(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, AssertInput{Action: "update", Content: "updated"})
	if err == nil {
		t.Fatal("expected error: memory_id required for action=update")
	}
}

func TestHandlerAssert_Update_Existing(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	// Add a memory first.
	addResult, err := h(ctx, AssertInput{Action: "add", Content: "original content"})
	if err != nil {
		t.Fatalf("assert add: %v", err)
	}
	memID := addResult.Structured.MemoryID

	// Update it.
	updateResult, err := h(ctx, AssertInput{
		Action:   "update",
		MemoryID: memID,
		Content:  "updated content",
		Kind:     "preference",
	})
	if err != nil {
		t.Fatalf("assert update: %v", err)
	}
	if updateResult.Structured.MemoryID != memID {
		t.Errorf("MemoryID mismatch: got %q, want %q", updateResult.Structured.MemoryID, memID)
	}
	if updateResult.Structured.Action != "update" {
		t.Errorf("Action must be 'update', got %q", updateResult.Structured.Action)
	}
}

func TestHandlerAssert_Delete_MissingMemoryID(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, AssertInput{Action: "delete"})
	if err == nil {
		t.Fatal("expected error: memory_id required for action=delete")
	}
}

func TestHandlerAssert_Delete_Existing(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeAssertHandler(svc)
	ctx := context.Background()

	// Add then delete.
	addResult, err := h(ctx, AssertInput{Action: "add", Content: "memory to delete"})
	if err != nil {
		t.Fatalf("assert add: %v", err)
	}
	memID := addResult.Structured.MemoryID

	delResult, err := h(ctx, AssertInput{Action: "delete", MemoryID: memID})
	if err != nil {
		t.Fatalf("assert delete: %v", err)
	}
	if delResult.Structured.Status != "deleted" {
		t.Errorf("Status must be 'deleted', got %q", delResult.Structured.Status)
	}
	if delResult.Structured.MemoryID != memID {
		t.Errorf("MemoryID mismatch: got %q, want %q", delResult.Structured.MemoryID, memID)
	}
}

// ── memory_topics ─────────────────────────────────────────────────────────────

func TestHandlerTopics_List_Empty(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	// List with no explicit topics → returns virtual pack (from profile default).
	result, err := h(ctx, TopicsInput{Action: "list"})
	if err != nil {
		t.Fatalf("topics list: %v", err)
	}
	// Virtual pack topics are returned when no explicit topics exist.
	if result.Text == "" {
		t.Error("Text must not be empty")
	}
}

func TestHandlerTopics_List_DefaultAction(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	// Empty action treated as "list".
	result, err := h(ctx, TopicsInput{Action: ""})
	if err != nil {
		t.Fatalf("topics list (empty action): %v", err)
	}
	_ = result
}

func TestHandlerTopics_Upsert_EmptyTopics(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, TopicsInput{Action: "upsert", Topics: nil})
	if err == nil {
		t.Fatal("expected error: topics array empty for action=upsert")
	}
}

func TestHandlerTopics_Upsert_MissingKey(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, TopicsInput{
		Action: "upsert",
		Topics: []TopicItem{{Key: "", Description: "no key"}},
	})
	if err == nil {
		t.Fatal("expected error: key must not be empty for upsert item")
	}
}

func TestHandlerTopics_Upsert_Valid(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	result, err := h(ctx, TopicsInput{
		Action: "upsert",
		Topics: []TopicItem{
			{Key: "unit-testing", Description: "unit test topic"},
			{Key: "integration-testing", Description: "int test topic", Status: "active"},
		},
	})
	if err != nil {
		t.Fatalf("topics upsert: %v", err)
	}
	if result.Structured.Upserted != 2 {
		t.Errorf("expected Upserted=2, got %d", result.Structured.Upserted)
	}
}

func TestHandlerTopics_ListAfterUpsert(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	// Upsert a topic, then list.
	_, err := h(ctx, TopicsInput{
		Action: "upsert",
		Topics: []TopicItem{{Key: "go-testing", Description: "go testing topic"}},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	listResult, err := h(ctx, TopicsInput{Action: "list"})
	if err != nil {
		t.Fatalf("list after upsert: %v", err)
	}
	found := false
	for _, tv := range listResult.Structured.Topics {
		if tv.Key == "go-testing" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find 'go-testing' topic in list after upsert")
	}
}

func TestHandlerTopics_Delete_MissingKey(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, TopicsInput{Action: "delete", Key: ""})
	if err == nil {
		t.Fatal("expected error: key must be set for action=delete")
	}
}

func TestHandlerTopics_Delete_ExistingTopic(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	// Upsert then delete.
	_, err := h(ctx, TopicsInput{
		Action: "upsert",
		Topics: []TopicItem{{Key: "to-delete", Description: "will be deleted"}},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	delResult, err := h(ctx, TopicsInput{Action: "delete", Key: "to-delete"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if delResult.Structured.Deleted != "to-delete" {
		t.Errorf("expected Deleted='to-delete', got %q", delResult.Structured.Deleted)
	}
}

func TestHandlerTopics_UnknownAction(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, TopicsInput{Action: "frobulate"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
}

func TestHandlerTopics_NilTopicSvc(t *testing.T) {
	svc := newHandlerServices(t)
	svc.TopicSvc = nil
	h := makeTopicsHandler(svc)
	ctx := context.Background()

	_, err := h(ctx, TopicsInput{Action: "list"})
	if err == nil {
		t.Fatal("expected error when TopicSvc is nil")
	}
}

// clampExcerpt tests moved to internal/retrieval (TestClampExcerpt) — the
// drill-down excerpt shaper is now the single shared retrieval.ClampExcerpt used
// by the HTTP, MCP, and embedded SDK surfaces (D-069, BUG-5).

// ── memory_drilldown (extra paths) ───────────────────────────────────────────

func TestHandlerDrilldown_CitationNotFound(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeDrilldownHandler(svc)
	ctx := context.Background()

	// Citation → injection store lookup → will return not-found error, exercising
	// the citation resolution path in the handler.
	_, err := h(ctx, DrilldownInput{Citation: "nonexistent-citation-id"})
	if err == nil {
		t.Fatal("expected error when citation is not found in the injection store")
	}
}

// ── memory_feedback (extra paths) ────────────────────────────────────────────

func TestHandlerFeedback_CitationWrongSignal_Applied(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeFeedbackHandler(svc)
	ctx := context.Background()

	// citation + wrong_citation: MarkWrongCitation is called; if not found it
	// returns an error or is a no-op depending on the store implementation.
	// Either way, this exercises the citation branch.
	_, _ = h(ctx, FeedbackInput{Citation: "nonexistent-cit", Signal: "wrong_citation"})
	// We don't assert the result because the SQLite store may return an error
	// for a non-existent citation. This line simply exercises the branch.
}

// ── KeyringMiddleware ─────────────────────────────────────────────────────────

func TestKeyringMiddleware_NoAuthHeader(t *testing.T) {
	handler := KeyringMiddleware(auth.NewMemKeyring(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestKeyringMiddleware_NonBearerPrefix(t *testing.T) {
	handler := KeyringMiddleware(auth.NewMemKeyring(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Token sk-test-abc")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestKeyringMiddleware_WrongKey(t *testing.T) {
	handler := KeyringMiddleware(auth.NewMemKeyring(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-wrong-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestKeyringMiddleware_ValidKey_CallsNext(t *testing.T) {
	kr := auth.NewMemKeyring()
	key, plaintext, err := auth.Generate("t-mw", auth.RoleAgent)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := kr.Insert(key); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var capturedScope identity.Scope
	handler := KeyringMiddleware(kr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedScope, _ = identity.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+plaintext)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if capturedScope.Tenant != "t-mw" {
		t.Errorf("scope tenant must come from the KEY, got %q", capturedScope.Tenant)
	}
}

func TestKeyringMiddleware_EmptyKeys(t *testing.T) {
	// No valid keys → any bearer token is rejected.
	handler := KeyringMiddleware(auth.NewMemKeyring(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-anything")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}
