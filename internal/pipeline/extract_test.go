package pipeline_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	mockdrv "github.com/hurtener/stowage/internal/gateway/mock"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"

	_ "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newMockGateway(t *testing.T) (gateway.Gateway, *mockdrv.Driver) {
	t.Helper()
	cfg := config.GatewayConfig{Driver: "mock", Model: "test-model", EmbedDims: 4}
	gw, err := gateway.Open(context.Background(), cfg, noopLog(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open gateway: %v", err)
	}
	return gw, gw.(*mockdrv.Driver)
}

// makeRecord inserts a store.Record and returns its ID.
func makeRecord(t *testing.T, st store.Store, tenantID, content string) string {
	t.Helper()
	id := ulid.Make().String()
	rec := store.Record{
		ID:            id,
		TenantID:      tenantID,
		Role:          "user",
		Content:       content,
		TokenEstimate: int64(len(content) / 4),
		OccurredAt:    time.Now().UnixMilli(),
		CreatedAt:     time.Now().UnixMilli(),
	}
	scope := identity.Scope{Tenant: tenantID}
	if err := st.Records().Append(context.Background(), scope, []store.Record{rec}); err != nil {
		t.Fatalf("append record: %v", err)
	}
	return id
}

// newExtractStageAndChan creates an ExtractStage with its own ingest channel.
func newExtractStageAndChan(
	st store.Store,
	gw gateway.Gateway,
	svc *topics.Service,
	profile string,
) (*pipeline.ExtractStage, chan pipeline.FlushedBuffer) {
	in := make(chan pipeline.FlushedBuffer, 16)
	s := pipeline.NewExtractStage(st, gw, svc, noopLog(), profile, in)
	return s, in
}

// makeFlushedBuffer builds a FlushedBuffer value for testing.
func makeFlushedBuffer(tenant string, recordIDs []string, skipPromotion bool) pipeline.FlushedBuffer {
	return pipeline.FlushedBuffer{
		Scope:         identity.Scope{Tenant: tenant},
		Key:           "test-sess/test-br",
		BranchID:      "test-br",
		RecordIDs:     recordIDs,
		TokenEstimate: 100,
		Trigger:       pipeline.TriggerExplicit,
		SkipPromotion: skipPromotion,
	}
}

// waitEvents polls EventStore until at least one event of the given type is
// found, or the timeout elapses.
func waitEvents(
	t *testing.T,
	st store.Store,
	scope identity.Scope,
	evType string,
	timeout time.Duration,
) []store.Event {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		evs, _, err := st.Events().List(context.Background(), scope, 50, "")
		if err != nil {
			t.Fatalf("Events.List: %v", err)
		}
		var found []store.Event
		for _, ev := range evs {
			if ev.Type == evType {
				found = append(found, ev)
			}
		}
		if len(found) > 0 {
			return found
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// collectBatches drains up to n batches from ch with a timeout.
func collectBatches(
	t *testing.T,
	ch <-chan pipeline.CandidateBatch,
	n int,
	timeout time.Duration,
) []pipeline.CandidateBatch {
	t.Helper()
	var out []pipeline.CandidateBatch
	deadline := time.After(timeout)
	for len(out) < n {
		select {
		case b, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, b)
		case <-deadline:
			return out
		}
	}
	return out
}

// candidateJSON builds a valid candidate JSON object for the given record ID.
func candidateJSON(recordID, content string) []byte {
	c := map[string]interface{}{
		"kind":     "preference",
		"content":  content,
		"context":  "test context",
		"entities": []string{"user"},
		"keywords": []string{"preference"},
		"anticipated_queries": []string{
			"what does the user prefer",
			"user preference for communication",
			"how to respond to user",
		},
		"importance": 3,
		"confidence": 0.85,
		"provenance": []map[string]interface{}{
			{"record_id": recordID, "span_start": 0, "span_end": len(content)},
		},
	}
	b, _ := json.Marshal(map[string]interface{}{"candidates": []interface{}{c}})
	return b
}

// ── AC-1: Prompt golden tests ─────────────────────────────────────────────────

// TestPromptGoldenPack asserts that pack topics + fixture records produce a
// byte-exact prompt (AC-1, one per pack).
func TestPromptGoldenPack(t *testing.T) {
	t.Parallel()

	topicLines := []string{
		"user-communication-style: How the user prefers to be addressed — tone, verbosity, level of detail",
		"user-preferences: Explicit preferences about tools, methods, formats, frameworks, or workflows",
	}
	records := []store.Record{
		{
			ID:      "rec-AAAAAAAAAAAAAAAAAAAAAAAAAAA1",
			Role:    "user",
			Content: "Please keep your answers short and to the point.",
		},
		{
			ID:      "rec-AAAAAAAAAAAAAAAAAAAAAAAAAAA2",
			Role:    "assistant",
			Content: "Understood. I will be concise.",
		},
	}

	result := pipeline.BuildPrompt(topicLines, records, 8000)
	got, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	goldenPath := filepath.Join("testdata", "extract_prompt_pack.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to create)", goldenPath, err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Errorf("PromptResult JSON mismatch (pack):\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestPromptGoldenExplicit asserts that explicit topics + fixture records
// produce a byte-exact prompt (AC-1, explicit-topics case).
func TestPromptGoldenExplicit(t *testing.T) {
	t.Parallel()

	topicLines := []string{
		"go-style: Go code style conventions agreed in this project",
	}
	records := []store.Record{
		{
			ID:      "rec-BBBBBBBBBBBBBBBBBBBBBBBBBBB1",
			Role:    "user",
			Content: "We decided to use gofmt and golangci-lint with the default settings.",
		},
	}

	result := pipeline.BuildPrompt(topicLines, records, 8000)
	got, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	goldenPath := filepath.Join("testdata", "extract_prompt_explicit.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=1 to create)", goldenPath, err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Errorf("PromptResult JSON mismatch (explicit):\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// ── AC-2: Topic gating ────────────────────────────────────────────────────────

// TestTopicGating_PackOff_ShortCircuit asserts that a scope with a pack:off
// sentinel and no other active topics skips the gateway call and emits
// extraction.skipped (AC-2).
func TestTopicGating_PackOff_ShortCircuit(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	gw, mock := newMockGateway(t)
	scope := identity.Scope{Tenant: "t-packoff-gate"}

	// Insert pack:off sentinel.
	now := time.Now().UnixMilli()
	if err := st.Topics().Upsert(context.Background(), scope, store.Topic{
		ID:        ulid.Make().String(),
		TenantID:  scope.Tenant,
		Key:       topics.PackOff,
		Status:    "active",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert pack:off: %v", err)
	}

	svc := topics.New(st.Topics(), noopLog(), "assistant")

	// Push a fail-script: if the gateway IS called, test fails via dead-letter.
	mock.PushScript(mockdrv.Script{Err: errors.New("should not call gateway")})

	stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
	stage.Start(context.Background())

	recID := makeRecord(t, st, scope.Tenant, "test content")
	in <- makeFlushedBuffer(scope.Tenant, []string{recID}, false)

	// Close + drain.
	close(in)
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stage.Drain(drainCtx)

	// Assert: extraction.skipped event present.
	skipped := waitEvents(t, st, scope, "extraction.skipped", 2*time.Second)
	if len(skipped) == 0 {
		t.Error("want extraction.skipped event, got none")
	}

	// Assert: no dead letter (gateway was not called).
	dls, err := st.Ops().ListDeadLetters(context.Background(), "extract", 10)
	if err != nil {
		t.Fatalf("ListDeadLetters: %v", err)
	}
	if len(dls) != 0 {
		t.Errorf("unexpected dead letters: %v", dls)
	}
}

// ── AC-3: Preference fragments ────────────────────────────────────────────────

// TestPreferenceFragments asserts that an assistant-profile scope with no
// explicit topics extracts preference candidates from a fixture conversation
// using the virtual pack:preferences pack (AC-3).
func TestPreferenceFragments(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	gw, mock := newMockGateway(t)
	scope := identity.Scope{Tenant: "t-pref-frags"}

	// No explicit topics → pack:preferences virtual pack applies.
	svc := topics.New(st.Topics(), noopLog(), "assistant")

	recID := makeRecord(t, st, scope.Tenant, "I prefer short, direct answers without preamble.")

	// Script the mock to return a preference candidate.
	script := candidateJSON(recID, "User prefers short, direct answers without preamble.")
	mock.PushScript(mockdrv.Script{JSON: script})

	stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
	stage.Start(context.Background())

	in <- makeFlushedBuffer(scope.Tenant, []string{recID}, false)
	close(in)
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stage.Drain(drainCtx)

	batches := collectBatches(t, stage.Downstream(), 1, 2*time.Second)
	if len(batches) == 0 {
		t.Fatal("want CandidateBatch, got none")
	}
	b := batches[0]
	if len(b.Candidates) == 0 {
		t.Fatal("want ≥1 candidates, got none")
	}
	c := b.Candidates[0]
	if c.Kind != "preference" {
		t.Errorf("want Kind=preference, got %q", c.Kind)
	}
	if len(c.AnticipatedQueries) == 0 {
		t.Error("want ≥1 anticipated_queries, got none")
	}
	if len(c.Provenance) == 0 {
		t.Error("want ≥1 provenance span, got none")
	}
}

// ── AC-4: Per-candidate validation ───────────────────────────────────────────

// TestCandidateValidation_TableTest covers the per-candidate validation path
// with a table of valid and invalid inputs (AC-4). The batch survives a bad
// candidate; only the bad one is dropped.
func TestCandidateValidation_TableTest(t *testing.T) {
	t.Parallel()

	recordSet := map[string]bool{"r1": true, "r2": true}
	recordContents := map[string]string{
		"r1": "hello world this is a test record with some content in it",
		"r2": "another record here",
	}

	goodCandidate := pipeline.Candidate{
		Kind:               "fact",
		Content:            "The project uses Go 1.26.",
		Context:            "Stated in the conversation.",
		Entities:           []string{"Go"},
		Keywords:           []string{"Go", "1.26"},
		AnticipatedQueries: []string{"what Go version", "Go version project", "version requirement"},
		Importance:         3,
		Confidence:         0.9,
		Provenance:         []pipeline.ProvSpan{{RecordID: "r1", SpanStart: 0, SpanEnd: 10}},
	}

	cases := []struct {
		name      string
		candidate pipeline.Candidate
		wantValid bool
	}{
		{"valid", goodCandidate, true},
		{
			"empty content",
			func() pipeline.Candidate {
				c := goodCandidate
				c.Content = "   "
				return c
			}(),
			false,
		},
		{
			"unknown kind",
			func() pipeline.Candidate {
				c := goodCandidate
				c.Kind = "strategy" // Phase 19 only
				return c
			}(),
			false,
		},
		{
			"importance too low",
			func() pipeline.Candidate {
				c := goodCandidate
				c.Importance = 0
				return c
			}(),
			false,
		},
		{
			"importance too high",
			func() pipeline.Candidate {
				c := goodCandidate
				c.Importance = 6
				return c
			}(),
			false,
		},
		{
			"confidence < 0",
			func() pipeline.Candidate {
				c := goodCandidate
				c.Confidence = -0.1
				return c
			}(),
			false,
		},
		{
			"confidence > 1",
			func() pipeline.Candidate {
				c := goodCandidate
				c.Confidence = 1.1
				return c
			}(),
			false,
		},
		{
			"no provenance",
			func() pipeline.Candidate {
				c := goodCandidate
				c.Provenance = nil
				return c
			}(),
			false,
		},
		{
			"foreign record_id",
			func() pipeline.Candidate {
				c := goodCandidate
				c.Provenance = []pipeline.ProvSpan{{RecordID: "foreign-id", SpanStart: 0, SpanEnd: 5}}
				return c
			}(),
			false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			valid, dropped := pipeline.ValidateCandidates(
				[]pipeline.Candidate{tc.candidate},
				recordSet,
				recordContents,
			)
			if tc.wantValid {
				if len(valid) != 1 || dropped != 0 {
					t.Errorf("want 1 valid 0 dropped, got %d valid %d dropped", len(valid), dropped)
				}
			} else {
				if len(valid) != 0 || dropped != 1 {
					t.Errorf("want 0 valid 1 dropped, got %d valid %d dropped", len(valid), dropped)
				}
			}
		})
	}
}

// TestCandidateValidation_SpanClamping asserts that out-of-range spans are
// clamped to the record content length rather than being dropped.
func TestCandidateValidation_SpanClamping(t *testing.T) {
	t.Parallel()

	content := "hello"
	recordSet := map[string]bool{"r1": true}
	recordContents := map[string]string{"r1": content}

	c := pipeline.Candidate{
		Kind:               "fact",
		Content:            "Some fact.",
		Context:            "",
		Entities:           []string{},
		Keywords:           []string{},
		AnticipatedQueries: []string{"q1", "q2", "q3"},
		Importance:         2,
		Confidence:         0.5,
		Provenance: []pipeline.ProvSpan{
			{RecordID: "r1", SpanStart: -5, SpanEnd: 999},
		},
	}

	valid, dropped := pipeline.ValidateCandidates([]pipeline.Candidate{c}, recordSet, recordContents)
	if dropped != 0 {
		t.Errorf("out-of-range spans should be clamped, not dropped; got dropped=%d", dropped)
	}
	if len(valid) != 1 {
		t.Fatalf("want 1 valid, got %d", len(valid))
	}
	p := valid[0].Provenance[0]
	if p.SpanStart != 0 {
		t.Errorf("SpanStart: want 0 (clamped from -5), got %d", p.SpanStart)
	}
	if p.SpanEnd != len(content) {
		t.Errorf("SpanEnd: want %d (clamped from 999), got %d", len(content), p.SpanEnd)
	}
}

// TestCandidateValidation_BatchSurvivesBadCandidate asserts that the batch
// survives when one candidate is invalid (AC-4 key property).
func TestCandidateValidation_BatchSurvivesBadCandidate(t *testing.T) {
	t.Parallel()

	recordSet := map[string]bool{"r1": true}
	recordContents := map[string]string{"r1": "some content"}

	good := pipeline.Candidate{
		Kind:               "fact",
		Content:            "Valid fact.",
		Context:            "",
		Entities:           []string{},
		Keywords:           []string{},
		AnticipatedQueries: []string{"q", "q2", "q3"},
		Importance:         3,
		Confidence:         0.8,
		Provenance:         []pipeline.ProvSpan{{RecordID: "r1", SpanStart: 0, SpanEnd: 4}},
	}
	bad := pipeline.Candidate{
		Kind:    "fact",
		Content: "", // empty → drop
	}

	valid, dropped := pipeline.ValidateCandidates([]pipeline.Candidate{good, bad}, recordSet, recordContents)
	if len(valid) != 1 {
		t.Errorf("want 1 valid, got %d", len(valid))
	}
	if dropped != 1 {
		t.Errorf("want 1 dropped, got %d", dropped)
	}
}

// ── AC-5: SkipPromotion ───────────────────────────────────────────────────────

// TestSkipPromotion asserts that SkipPromotion flushes emit no gateway call
// and produce an extraction.skipped event (AC-5).
func TestSkipPromotion(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	gw, mock := newMockGateway(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")

	// If gateway IS called, we'd get an error (fail-script).
	mock.PushScript(mockdrv.Script{Err: errors.New("gateway should not be called for SkipPromotion")})

	scope := identity.Scope{Tenant: "t-skip-promo"}
	recID := makeRecord(t, st, scope.Tenant, "branch-discard content")

	stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
	stage.Start(context.Background())

	// SkipPromotion = true (branch_discard trigger).
	fb := makeFlushedBuffer(scope.Tenant, []string{recID}, true)
	in <- fb

	close(in)
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stage.Drain(drainCtx)

	// Assert: extraction.skipped event.
	skipped := waitEvents(t, st, scope, "extraction.skipped", 2*time.Second)
	if len(skipped) == 0 {
		t.Error("want extraction.skipped event, got none")
	}
	if len(skipped) > 0 {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(skipped[0].Payload), &payload); err == nil {
			if payload["reason"] != "skip_promotion" {
				t.Errorf("want reason=skip_promotion, got %v", payload["reason"])
			}
		}
	}

	// Assert: no dead letter (no gateway call).
	dls, err := st.Ops().ListDeadLetters(context.Background(), "extract", 10)
	if err != nil {
		t.Fatalf("ListDeadLetters: %v", err)
	}
	if len(dls) != 0 {
		t.Errorf("unexpected dead letters (gateway must not be called): %v", dls)
	}
}

// ── AC-6: Gateway failure → dead letter ──────────────────────────────────────

// TestGatewayFailure_DeadLetter asserts that a terminal gateway failure
// dead-letters with the flush descriptor and emits an event; records are
// durable throughout (AC-6).
func TestGatewayFailure_DeadLetter(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	gw, mock := newMockGateway(t)
	scope := identity.Scope{Tenant: "t-gw-fail"}

	// No explicit topics → virtual pack applies.
	svc := topics.New(st.Topics(), noopLog(), "assistant")

	// Script terminal gateway error.
	mock.PushScript(mockdrv.Script{Err: errors.New("simulated gateway failure")})

	recID := makeRecord(t, st, scope.Tenant, "content for extraction")

	stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
	stage.Start(context.Background())

	in <- makeFlushedBuffer(scope.Tenant, []string{recID}, false)
	close(in)
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stage.Drain(drainCtx)

	// Assert: dead letter exists.
	dls, err := st.Ops().ListDeadLetters(context.Background(), "extract", 10)
	if err != nil {
		t.Fatalf("ListDeadLetters: %v", err)
	}
	if len(dls) == 0 {
		t.Fatal("want dead letter after gateway failure, got none")
	}
	if dls[0].Stage != "extract" {
		t.Errorf("dead letter Stage: want extract, got %q", dls[0].Stage)
	}

	// Assert: original record still in store (P1 — no data loss).
	rec, err := st.Records().Get(context.Background(), scope, recID)
	if err != nil {
		t.Fatalf("record lost after dead-letter: %v", err)
	}
	if rec.ID != recID {
		t.Errorf("record ID mismatch: want %q, got %q", recID, rec.ID)
	}

	// Assert: extraction.failed event.
	failed := waitEvents(t, st, scope, "extraction.failed", 2*time.Second)
	if len(failed) == 0 {
		t.Error("want extraction.failed event, got none")
	}
}

// ── AC-7: Scope stamping (P3) ─────────────────────────────────────────────────

// TestScopeStamping asserts that the CandidateBatch carries the flush's scope
// and branch regardless of model output (P3, AC-7).
func TestScopeStamping(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	gw, mock := newMockGateway(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")

	tenant := "t-scope-stamp"
	branchID := "branch-XYZ"
	scope := identity.Scope{Tenant: tenant}

	recID := makeRecord(t, st, tenant, "The user prefers Go for systems programming.")
	script := candidateJSON(recID, "User prefers Go for systems programming.")
	mock.PushScript(mockdrv.Script{JSON: script})

	stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
	stage.Start(context.Background())

	fb := pipeline.FlushedBuffer{
		Scope:         scope,
		Key:           "sess/branch-XYZ",
		BranchID:      branchID,
		RecordIDs:     []string{recID},
		TokenEstimate: 10,
		Trigger:       pipeline.TriggerExplicit,
		SkipPromotion: false,
	}
	in <- fb

	close(in)
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stage.Drain(drainCtx)

	batches := collectBatches(t, stage.Downstream(), 1, 2*time.Second)
	if len(batches) == 0 {
		t.Fatal("want CandidateBatch, got none")
	}
	b := batches[0]

	// P3: scope must come from the flush, not the model.
	if b.Scope.Tenant != tenant {
		t.Errorf("Scope.Tenant: want %q, got %q", tenant, b.Scope.Tenant)
	}
	if b.BranchID != branchID {
		t.Errorf("BranchID: want %q, got %q", branchID, b.BranchID)
	}
}

// ── AC-8: extraction.completed event counts ───────────────────────────────────

// TestExtractionCompleted_EventCounts asserts that extraction.completed events
// carry produced, dropped, and truncated counts (AC-8).
func TestExtractionCompleted_EventCounts(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	gw, mock := newMockGateway(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")

	tenant := "t-ev-counts"
	scope := identity.Scope{Tenant: tenant}

	recID := makeRecord(t, st, tenant, "I always use tabs for indentation.")
	// Script: one valid candidate + one invalid (foreign record_id → dropped).
	scriptJSON, _ := json.Marshal(map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"kind":                "preference",
				"content":             "User uses tabs for indentation.",
				"context":             "",
				"entities":            []string{},
				"keywords":            []string{"tabs"},
				"anticipated_queries": []string{"indentation style", "tabs vs spaces", "user indentation"},
				"importance":          2,
				"confidence":          0.9,
				"provenance": []map[string]interface{}{
					{"record_id": recID, "span_start": 0, "span_end": 10},
				},
			},
			map[string]interface{}{
				"kind":                "fact",
				"content":             "Will be dropped due to foreign record_id.",
				"context":             "",
				"entities":            []string{},
				"keywords":            []string{},
				"anticipated_queries": []string{"q1", "q2", "q3"},
				"importance":          1,
				"confidence":          0.5,
				"provenance": []map[string]interface{}{
					{"record_id": "foreign-record-not-in-flush", "span_start": 0, "span_end": 5},
				},
			},
		},
	})
	mock.PushScript(mockdrv.Script{JSON: scriptJSON})

	stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
	stage.Start(context.Background())

	in <- makeFlushedBuffer(tenant, []string{recID}, false)
	close(in)
	drainCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stage.Drain(drainCtx)

	evs := waitEvents(t, st, scope, "extraction.completed", 2*time.Second)
	if len(evs) == 0 {
		t.Fatal("want extraction.completed event, got none")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(evs[0].Payload), &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}

	produced, _ := payload["produced"].(float64)
	dropped, _ := payload["dropped"].(float64)
	if int(produced) != 1 {
		t.Errorf("produced: want 1, got %v", produced)
	}
	if int(dropped) != 1 {
		t.Errorf("dropped: want 1, got %v", dropped)
	}
	_, hasTruncated := payload["truncated"]
	if !hasTruncated {
		t.Error("extraction.completed event must carry 'truncated' count")
	}
}

// ── Truncation ────────────────────────────────────────────────────────────────

// TestPromptTruncation asserts that the transcript is clamped when it exceeds
// the token budget, oldest records are dropped, and Truncated is true.
func TestPromptTruncation(t *testing.T) {
	t.Parallel()

	// r1 block ≈ 67 chars → ~16 tokens; r2 block ≈ 28 chars → ~7 tokens.
	// Budget 10: r1+r2=23 > 10 → drop r1 (16); 7 ≤ 10 → keep r2.
	budget := 10
	records := []store.Record{
		{ID: "r1", Role: "user", Content: "This is old content that should be dropped."},
		{ID: "r2", Role: "user", Content: "New."},
	}

	result := pipeline.BuildPrompt([]string{"topic: desc"}, records, budget)
	if !result.Truncated {
		t.Error("want Truncated=true when transcript exceeds budget")
	}
	if len(result.UserContent) == 0 {
		t.Error("UserContent must not be empty when there are records")
	}
	// Oldest record (r1) should be dropped; newest (r2) should survive.
	if !bytes.Contains([]byte(result.UserContent), []byte("r2")) {
		t.Error("newest record r2 should survive truncation")
	}
	if bytes.Contains([]byte(result.UserContent), []byte("r1")) {
		t.Error("oldest record r1 should be truncated")
	}
}

// ── Race test ─────────────────────────────────────────────────────────────────

// TestExtractStage_Race is the extract-stage concurrent-reuse test.
// Many flushes are pushed concurrently across tenants; no data races.
func TestExtractStage_Race(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	gw, mock := newMockGateway(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")

	// Return a valid single-candidate response for all calls.
	// Push many scripts so each worker call gets one.
	const nFlushes = 20
	for i := 0; i < nFlushes; i++ {
		id := ulid.Make().String()
		_ = id // scripts are per-call; we use a catch-all response
		mock.PushScript(mockdrv.Script{JSON: json.RawMessage(`{"candidates":[]}`)})
	}

	stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
	stage.Start(context.Background())

	for i := 0; i < nFlushes; i++ {
		tenant := "race-t-" + ulid.Make().String()
		recID := makeRecord(t, st, tenant, "race content")
		in <- makeFlushedBuffer(tenant, []string{recID}, false)
	}
	close(in)

	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stage.Drain(drainCtx)
}

// TestValidateCandidatesNormalizesKeywordCase pins the lowercase contract
// between extraction junctions and the structured lane's lowercase tokens
// (the Phase 13 eval gate caught capitalized keywords never matching).
func TestValidateCandidatesNormalizesKeywordCase(t *testing.T) {
	t.Parallel()
	recordSet := map[string]bool{"r1": true}
	contents := map[string]string{"r1": "some content here"}
	in := []pipeline.Candidate{{
		Kind: "fact", Content: "user codes in Python", Context: "x",
		Entities: []string{"Python", " VS Code "}, Keywords: []string{"Programming", "favorite Language"},
		AnticipatedQueries: []string{"a", "b", "c"}, Importance: 3, Confidence: 0.9,
		Provenance: []pipeline.ProvSpan{{RecordID: "r1", SpanStart: 0, SpanEnd: 4}},
	}}
	valid, dropped := pipeline.ValidateCandidates(in, recordSet, contents)
	if dropped != 0 || len(valid) != 1 {
		t.Fatalf("unexpected drop: %d valid=%d", dropped, len(valid))
	}
	wantKw := []string{"programming", "favorite language"}
	for i, k := range valid[0].Keywords {
		if k != wantKw[i] {
			t.Errorf("keyword %d: got %q want %q", i, k, wantKw[i])
		}
	}
	if valid[0].Entities[0] != "python" || valid[0].Entities[1] != "vs code" {
		t.Errorf("entities not normalized: %v", valid[0].Entities)
	}
}

// ── MarkProcessed wiring (2026-06-12 sanity-check finding) ────────────────────

// TestExtract_MarksRecordsProcessed pins the re-enqueue convergence contract:
// a successfully delivered extraction marks its records processed, while a
// gateway failure leaves them unprocessed so the re-enqueue sweep retries.
// Before this wiring MarkProcessed had no production caller and every record
// re-extracted forever.
func TestExtract_MarksRecordsProcessed(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	gw, mock := newMockGateway(t)
	svc := topics.New(st.Topics(), noopLog(), "assistant")
	tenant := "t-mark-processed"

	unprocessed := func() map[string]bool {
		recs, err := st.Records().ListUnprocessed(context.Background(), time.Now().Add(time.Second).UnixMilli(), 100)
		if err != nil {
			t.Fatalf("ListUnprocessed: %v", err)
		}
		out := make(map[string]bool, len(recs))
		for _, r := range recs {
			out[r.ID] = true
		}
		return out
	}

	okID := makeRecord(t, st, tenant, "The user prefers Go for systems programming.")
	failID := makeRecord(t, st, tenant, "The user lives in Madrid.")

	mock.PushScript(mockdrv.Script{JSON: candidateJSON(okID, "User prefers Go.")})
	mock.PushScript(mockdrv.Script{Err: errors.New("simulated gateway failure")})

	stage, in := newExtractStageAndChan(st, gw, svc, "assistant")
	stage.Start(context.Background())

	in <- makeFlushedBuffer(tenant, []string{okID}, false)
	in <- makeFlushedBuffer(tenant, []string{failID}, false)
	close(in)
	drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stage.Drain(drainCtx)
	collectBatches(t, stage.Downstream(), 1, 2*time.Second)

	up := unprocessed()
	if up[okID] {
		t.Errorf("delivered extraction: record %s still unprocessed — re-enqueue would loop forever", okID)
	}
	if !up[failID] {
		t.Errorf("failed extraction: record %s marked processed — re-enqueue can no longer retry it", failID)
	}
}
