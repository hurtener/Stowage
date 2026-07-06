package mcpserver

// handlers_runsink_test.go — the memory_ingest_run handler (phase ae12, D-153):
// the D-153 §2 identity cross-check matrix (fail closed), the format_version pin,
// the eager-flush degrade path, the D-124 scope-authoritative user stamp, and the
// ST-2 golden marshal-validate test (a Harbor-shaped payload validates against the
// generated input schema — the failure mode this whole tool exists to fix).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/hurtener/stowage/internal/identity"
)

// fixedScopeFn returns a ScopeFn resolving to s — the jwt-mode stand-in (a scope
// carrying a verified User, unlike StdioScopeFn's tenant-only scope).
func fixedScopeFn(s identity.Scope) ScopeFn {
	return func(context.Context) (identity.Scope, error) { return s, nil }
}

// validRunInput returns a well-formed format_version-1 payload the cases mutate.
func validRunInput() IngestRunInput {
	return IngestRunInput{
		FormatVersion: 1,
		TenantID:      "acme",
		UserID:        "alice",
		SessionID:     "s1",
		RunID:         "run-abc",
		AgentID:       "agent-x",
		Outcome:       "goal",
		StartedAt:     "2026-07-06T10:00:00Z",
		CompletedAt:   "2026-07-06T10:05:00Z",
		DurationMS:    300000,
		StepCount:     2,
		Conversation: []IngestRunEntry{
			{Role: "user", Kind: "goal", Content: "help me plan", Step: 0},
			{Role: "assistant", Kind: "tool", Content: "planning: ok", Step: 0, At: "2026-07-06T10:02:00Z"},
		},
	}
}

func TestIngestRunHandler_IdentityCrossCheck(t *testing.T) {
	scope := identity.Scope{Tenant: "acme", User: "alice"}
	cases := []struct {
		name    string
		mutate  func(*IngestRunInput)
		wantErr error // nil ⇒ expect success
	}{
		{"matching quad", func(*IngestRunInput) {}, nil},
		{"empty payload quad fills from scope", func(in *IngestRunInput) { in.TenantID = ""; in.UserID = "" }, nil},
		{"tenant mismatch", func(in *IngestRunInput) { in.TenantID = "evilcorp" }, ErrRunTenantMismatch},
		{"user mismatch", func(in *IngestRunInput) { in.UserID = "mallory" }, ErrRunUserMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newHandlerServices(t)
			svc.ScopeFn = fixedScopeFn(scope)
			h := makeIngestRunHandler(svc)
			in := validRunInput()
			tc.mutate(&in)

			res, err := h(context.Background(), in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				// Fail closed: no partial write.
				assertRecordCount(t, svc, scope, "s1", 0)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(res.Structured.IDs) != 2 {
				t.Fatalf("want 2 ids, got %d", len(res.Structured.IDs))
			}
			// D-124: the store row's user is the verified scope user, never a
			// divergent payload value.
			recs := listRecords(t, svc, scope, "s1")
			if len(recs) != 2 {
				t.Fatalf("want 2 records, got %d", len(recs))
			}
			for _, r := range recs {
				if r.UserID != "alice" || r.TenantID != "acme" {
					t.Errorf("record scoped to %q/%q, want acme/alice", r.TenantID, r.UserID)
				}
				if r.OutcomeDetail != "goal" || r.Outcome != "success" {
					t.Errorf("record outcome/detail = %q/%q, want success/goal", r.Outcome, r.OutcomeDetail)
				}
			}
		})
	}
}

// TestIngestRunHandler_RecordsInOrder proves one verbatim record per entry, in
// order, with OccurredAt honoring entry.at (else completed_at).
func TestIngestRunHandler_RecordsInOrder(t *testing.T) {
	scope := identity.Scope{Tenant: "acme", User: "alice"}
	svc := newHandlerServices(t)
	svc.ScopeFn = fixedScopeFn(scope)
	h := makeIngestRunHandler(svc)

	in := validRunInput()
	// Strictly increasing occurred_at so the (occurred_at ASC) read order equals
	// conversation order; the last entry omits `at` and takes completed_at (the max).
	in.CompletedAt = "2026-07-06T10:09:00Z"
	in.Conversation = []IngestRunEntry{
		{Role: "user", Content: "first", At: "2026-07-06T10:01:00Z"},
		{Role: "assistant", Content: "second", At: "2026-07-06T10:02:00Z"},
		{Role: "assistant", Content: "third"}, // no at → completed_at (10:09)
	}
	if _, err := h(context.Background(), in); err != nil {
		t.Fatalf("ingest run: %v", err)
	}
	recs := listRecords(t, svc, scope, "s1")
	wantContent := []string{"first", "second", "third"}
	if len(recs) != len(wantContent) {
		t.Fatalf("want %d records, got %d", len(wantContent), len(recs))
	}
	for i, r := range recs {
		if r.Content != wantContent[i] {
			t.Errorf("record[%d] content = %q, want %q (order not preserved)", i, r.Content, wantContent[i])
		}
	}
	// The no-`at` entry inherited completed_at.
	completedMs := mustParseMillis(t, "2026-07-06T10:09:00Z")
	if recs[2].OccurredAt != completedMs {
		t.Errorf("last record OccurredAt = %d, want completed_at %d", recs[2].OccurredAt, completedMs)
	}
	if recs[0].OccurredAt != mustParseMillis(t, "2026-07-06T10:01:00Z") {
		t.Errorf("first record OccurredAt = %d, want entry at", recs[0].OccurredAt)
	}
}

func TestIngestRunHandler_FormatVersionRejected(t *testing.T) {
	svc := newHandlerServices(t)
	svc.ScopeFn = fixedScopeFn(identity.Scope{Tenant: "acme", User: "alice"})
	h := makeIngestRunHandler(svc)

	in := validRunInput()
	in.FormatVersion = 2
	_, err := h(context.Background(), in)
	if err == nil {
		t.Fatal("format_version 2 must be rejected")
	}
	// The error names the supported version (1) — a future Harbor v2 fails loudly.
	if got := err.Error(); !bytes.Contains([]byte(got), []byte("want 1")) {
		t.Errorf("error %q should name supported version 1", got)
	}
}

// TestIngestRunHandler_FlushDegradesNilStage: a nil PipelineStage degrades
// Flushed to false while the call succeeds and the records are durable (P2/D-036).
func TestIngestRunHandler_FlushDegradesNilStage(t *testing.T) {
	scope := identity.Scope{Tenant: "acme", User: "alice"}
	svc := newHandlerServices(t) // PipelineStage is nil
	svc.ScopeFn = fixedScopeFn(scope)
	h := makeIngestRunHandler(svc)

	res, err := h(context.Background(), validRunInput())
	if err != nil {
		t.Fatalf("ingest run: %v", err)
	}
	if res.Structured.Flushed {
		t.Error("Flushed must be false with a nil PipelineStage")
	}
	assertRecordCount(t, svc, scope, "s1", 2) // records durable regardless of flush
}

// TestIngestRun_HarborPayloadValidatesAgainstSchema pins the ST-2 failure mode:
// a struct mirroring Harbor's RunCompletionPayload (time.Time fields, all 13 keys,
// five-field entries — with kind/step/at present AND one entry omitting at)
// marshals to bytes that validate against the GENERATED input schema. If the
// mirror ever diverges from Harbor's shape, this breaks in CI (the drift gate).
func TestIngestRun_HarborPayloadValidatesAgainstSchema(t *testing.T) {
	// A local, self-contained mirror of Harbor's wire types (we never import
	// Harbor — cross-module internal/, and it stays a separate product).
	type harborEntry struct {
		Role    string     `json:"role"`
		Kind    string     `json:"kind"`
		Content string     `json:"content"`
		Step    int        `json:"step"`
		At      *time.Time `json:"at,omitempty"`
	}
	type harborPayload struct {
		FormatVersion   int           `json:"format_version"`
		TenantID        string        `json:"tenant_id"`
		UserID          string        `json:"user_id"`
		SessionID       string        `json:"session_id"`
		RunID           string        `json:"run_id"`
		AgentID         string        `json:"agent_id,omitempty"`
		Outcome         string        `json:"outcome"`
		StartedAt       time.Time     `json:"started_at"`
		CompletedAt     time.Time     `json:"completed_at"`
		DurationMS      int64         `json:"duration_ms"`
		StepCount       int           `json:"step_count"`
		ToolInvocations int           `json:"tool_invocations"`
		Conversation    []harborEntry `json:"conversation"`
	}

	at := time.Date(2026, 7, 6, 10, 2, 0, 0, time.UTC)
	payload := harborPayload{
		FormatVersion:   1,
		TenantID:        "acme",
		UserID:          "alice",
		SessionID:       "s1",
		RunID:           "run-abc",
		AgentID:         "agent-x",
		Outcome:         "goal",
		StartedAt:       time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC),
		CompletedAt:     time.Date(2026, 7, 6, 10, 5, 0, 0, time.UTC),
		DurationMS:      300000,
		StepCount:       2,
		ToolInvocations: 1,
		Conversation: []harborEntry{
			{Role: "user", Kind: "goal", Content: "help me plan", Step: 0}, // omits at (omitempty)
			{Role: "assistant", Kind: "tool", Content: "planning: ok", Step: 0, At: &at},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal Harbor payload: %v", err)
	}

	// Compile the committed generated schema and validate the marshaled bytes.
	schemaBytes, err := os.ReadFile("testdata/memory_ingest_run.input.schema.json")
	if err != nil {
		t.Fatalf("read generated schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	const id = "urn:stowage:memory_ingest_run.input"
	if err := c.AddResource(id, doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	sch, err := c.Compile(id)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("Harbor payload must validate against the generated schema (ST-2): %v\npayload: %s", err, raw)
	}
}

// ── read-back helpers ──────────────────────────────────────────────────────────

func listRecords(t *testing.T, svc *Services, scope identity.Scope, session string) []recordRow {
	t.Helper()
	recs, _, err := svc.Store.Records().ListBySession(context.Background(), scope, session, "", 100, "")
	if err != nil {
		t.Fatalf("ListBySession: %v", err)
	}
	out := make([]recordRow, len(recs))
	for i, r := range recs {
		out[i] = recordRow{TenantID: r.TenantID, UserID: r.UserID, Content: r.Content, Outcome: r.Outcome, OutcomeDetail: r.OutcomeDetail, OccurredAt: r.OccurredAt}
	}
	return out
}

func assertRecordCount(t *testing.T, svc *Services, scope identity.Scope, session string, want int) {
	t.Helper()
	if got := len(listRecords(t, svc, scope, session)); got != want {
		t.Fatalf("record count = %d, want %d", got, want)
	}
}

type recordRow struct {
	TenantID      string
	UserID        string
	Content       string
	Outcome       string
	OutcomeDetail string
	OccurredAt    int64
}

func mustParseMillis(t *testing.T, s string) int64 {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts.UnixMilli()
}
