// White-box tests for the stowage package. Because this file is in package
// stowage (not stowage_test) it has access to unexported types and fields.
// These tests exercise code paths that are impractical to reach from the
// outside: internal helper functions, deep Drilldown provenance paths,
// ResolveCitations with real injections, and trySend channel behaviour.
package stowage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// newWhiteboxClient boots an embeddedClient and returns it cast to its
// concrete type so tests can access internal fields (stack, scope, pipeline).
// The closer is registered with t.Cleanup.
func newWhiteboxClient(t *testing.T, tenantID string) *embeddedClient {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "wb.db")

	cfg := config.Config{}
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dbPath
	cfg.Gateway.Driver = "mock"
	cfg.VIndex.Driver = "hnsw"

	ctx, cancel := context.WithCancel(context.Background())

	client, closer, err := NewEmbedded(ctx, cfg, WithTenantID(tenantID))
	if err != nil {
		cancel()
		t.Fatalf("NewEmbedded: %v", err)
	}

	ec := client.(*embeddedClient)

	t.Cleanup(func() {
		cancel()
		shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = closer(shutCtx)
	})

	return ec
}

// ---- trySend ------------------------------------------------------------------

// TestTrySend_ChannelFull verifies the default (channel-full) branch in trySend.
func TestTrySend_ChannelFull(t *testing.T) {
	t.Parallel()
	ch := make(chan pipeline.Item, 1)
	ch <- pipeline.Item{RecordID: "fill"} // occupy the only slot
	if trySend(ch, pipeline.Item{RecordID: "overflow"}) {
		t.Error("trySend to full channel: expected false, got true")
	}
}

// TestTrySend_ClosedChannel verifies the panic-recovery path in trySend: a send
// on a closed channel panics; trySend must recover and return false.
func TestTrySend_ClosedChannel(t *testing.T) {
	t.Parallel()
	ch := make(chan pipeline.Item, 1)
	close(ch)
	if trySend(ch, pipeline.Item{RecordID: "x"}) {
		t.Error("trySend to closed channel: expected false (panic recovered), got true")
	}
}

// ---- Ingest -------------------------------------------------------------------

// TestEmbedded_Ingest_Empty verifies that Ingest returns an error when given
// an empty records slice.
func TestEmbedded_Ingest_Empty(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "ingest-empty-tenant")
	_, err := ec.Ingest(context.Background(), IngestRequest{Records: nil})
	if err == nil {
		t.Error("Ingest with empty records: expected error, got nil")
	}
}

// ---- Retrieve -----------------------------------------------------------------

// TestEmbedded_Retrieve_EmptyQuery verifies that Retrieve returns an error when
// the Query field is empty.
func TestEmbedded_Retrieve_EmptyQuery(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "retrieve-empty-tenant")
	_, err := ec.Retrieve(context.Background(), RetrieveRequest{Query: ""})
	if err == nil {
		t.Error("Retrieve with empty query: expected error, got nil")
	}
}

// TestEmbedded_Retrieve_WithItemsAndLanes inserts an active memory directly
// into the store and then calls Retrieve with IncludeLanes:true and Debug:true.
// This covers the per-item loop body, the IncludeLanes branch, and — when the
// retriever's scorer produces a Breakdown — the Debug branch.
func TestEmbedded_Retrieve_WithItemsAndLanes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "retrieve-lanes-tenant")

	// Insert an active memory directly so the FTS trigger populates
	// memories_fts; the retriever's lexical lane will find it.
	memID := ulid.Make().String()
	mem := store.Memory{
		ID:      memID,
		Kind:    "fact",
		Content: "whitebox lanes debug coverage content",
		Status:  "active",
	}
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, mem); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	resp, err := ec.Retrieve(ctx, RetrieveRequest{
		Query:        "whitebox lanes debug",
		Limit:        5,
		IncludeLanes: true,
		Debug:        true,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	// The response must be well-formed even if the item list is empty (pipeline
	// may not have processed yet in unit-test time). Just assert shape.
	if resp.API != "v1" {
		t.Errorf("Retrieve: API want v1, got %q", resp.API)
	}
}

// ---- Drilldown ----------------------------------------------------------------

// TestEmbedded_Drilldown_BothSet verifies that Drilldown returns an error when
// both MemoryID and Citation are set (mutual-exclusion validation).
func TestEmbedded_Drilldown_BothSet(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "drilldown-both-tenant")
	_, err := ec.Drilldown(context.Background(), DrilldownRequest{
		MemoryID: "01JXXXXXXXXXXXXXXXXXXXXXXX",
		Citation: "01JXXXXXXXXXXXXXXXXXXXXXXY",
	})
	if err == nil {
		t.Error("Drilldown with both MemoryID+Citation: expected error, got nil")
	}
}

// TestEmbedded_Drilldown_MemoryID_EmptyJunctions calls Drilldown with a
// MemoryID that exists in the store but has no provenance rows. The expected
// result is an empty Spans slice (not an error) — covering the
// `len(junctions.Provenance) == 0` early-return path.
func TestEmbedded_Drilldown_MemoryID_EmptyJunctions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "drilldown-noprov-tenant")

	// Insert a memory with no provenance.
	memID := ulid.Make().String()
	mem := store.Memory{
		ID:      memID,
		Kind:    "fact",
		Content: "no provenance memory",
		Status:  "active",
	}
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, mem); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	resp, err := ec.Drilldown(ctx, DrilldownRequest{MemoryID: memID})
	if err != nil {
		t.Fatalf("Drilldown (no provenance): unexpected error: %v", err)
	}
	if resp.MemoryID != memID {
		t.Errorf("Drilldown: MemoryID want %q, got %q", memID, resp.MemoryID)
	}
	if len(resp.Spans) != 0 {
		t.Errorf("Drilldown (no provenance): want 0 spans, got %d", len(resp.Spans))
	}
}

// TestEmbedded_Drilldown_WithProvenance exercises the full Drilldown code path
// including safeExcerpt bounds-clamping with multiple span variants.
func TestEmbedded_Drilldown_WithProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "drilldown-prov-tenant")

	// Insert a verbatim record (11 chars: "hello world").
	recID := ulid.Make().String()
	recs := []store.Record{{
		ID:      recID,
		Role:    "user",
		Content: "hello world",
	}}
	if err := ec.stack.Store.Records().Append(ctx, ec.scope, recs); err != nil {
		t.Fatalf("Append record: %v", err)
	}

	// Insert a memory that will receive provenance rows.
	memID := ulid.Make().String()
	mem := store.Memory{
		ID:      memID,
		Kind:    "fact",
		Content: "hello world",
		Status:  "active",
	}
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, mem); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	// Add four provenance rows to exercise all safeExcerpt branches:
	//   row 1: normal span [0,5]         → "hello"         (base path)
	//   row 2: end past len [0,999]      → clamp e to n    (e > n branch)
	//   row 3: start past len [100,200]  → clamp s to n    (s > n branch)
	//   row 4: end before start [3,1]    → clamp e to s    (e < s branch)
	prows := []store.Provenance{
		{ID: ulid.Make().String(), MemoryID: memID, RecordID: recID, SpanStart: 0, SpanEnd: 5},
		{ID: ulid.Make().String(), MemoryID: memID, RecordID: recID, SpanStart: 0, SpanEnd: 999},
		{ID: ulid.Make().String(), MemoryID: memID, RecordID: recID, SpanStart: 100, SpanEnd: 200},
		{ID: ulid.Make().String(), MemoryID: memID, RecordID: recID, SpanStart: 3, SpanEnd: 1},
	}
	if err := ec.stack.Store.Memories().AddProvenance(ctx, ec.scope, prows); err != nil {
		t.Fatalf("AddProvenance: %v", err)
	}

	resp, err := ec.Drilldown(ctx, DrilldownRequest{MemoryID: memID})
	if err != nil {
		t.Fatalf("Drilldown with provenance: %v", err)
	}
	if resp.MemoryID != memID {
		t.Errorf("Drilldown: MemoryID want %q, got %q", memID, resp.MemoryID)
	}
	if len(resp.Spans) == 0 {
		t.Error("Drilldown with provenance: expected spans, got 0")
	}

	// Spot-check the normal span.
	found := false
	for _, sp := range resp.Spans {
		if sp.Excerpt == "hello" {
			found = true
		}
	}
	if !found {
		t.Errorf("Drilldown: expected excerpt %q in spans %+v", "hello", resp.Spans)
	}
}

// TestEmbedded_Drilldown_NonExistentMemoryID calls Drilldown with a MemoryID
// string that was never inserted; GetJunctions returns empty junctions for
// unknown IDs — so the result is an empty-spans success, not an error.
func TestEmbedded_Drilldown_NonExistentMemoryID(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "drilldown-noexist-tenant")
	resp, err := ec.Drilldown(context.Background(), DrilldownRequest{
		MemoryID: "01JXXXXXXXXXXXXXXXXXXXXXXX",
	})
	if err != nil {
		t.Fatalf("Drilldown non-existent MemoryID: unexpected error: %v", err)
	}
	if len(resp.Spans) != 0 {
		t.Errorf("Drilldown non-existent MemoryID: want 0 spans, got %d", len(resp.Spans))
	}
}

// ---- Feedback -----------------------------------------------------------------

// TestEmbedded_Feedback_EmptySignal verifies that Feedback returns an error
// when the Signal field is empty (first validation gate).
func TestEmbedded_Feedback_EmptySignal(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "feedback-signal-tenant")
	_, err := ec.Feedback(context.Background(), FeedbackRequest{
		ResponseID: "01JXXXXXXXXXXXXXXXXXXXXXXX",
		Signal:     "",
	})
	if err == nil {
		t.Error("Feedback with empty signal: expected error, got nil")
	}
}

// TestEmbedded_Feedback_Citation_WrongSignal verifies that Feedback returns an
// error when a citation-level target is used with a non-wrong_citation signal.
func TestEmbedded_Feedback_Citation_WrongSignal(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "feedback-cit-sig-tenant")
	_, err := ec.Feedback(context.Background(), FeedbackRequest{
		Citation: "01JXXXXXXXXXXXXXXXXXXXXXXX",
		Signal:   "use", // only "wrong_citation" is valid for citation targets
	})
	if err == nil {
		t.Error("Feedback citation + non-wrong_citation signal: expected error, got nil")
	}
}

// TestEmbedded_Feedback_Citation_NotFound verifies that Feedback with Citation
// and Signal "wrong_citation" propagates the not-found error from
// MarkWrongCitation when the injection does not exist.
func TestEmbedded_Feedback_Citation_NotFound(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "feedback-cit-nf-tenant")
	_, err := ec.Feedback(context.Background(), FeedbackRequest{
		Citation: "01JXXXXXXXXXXXXXXXXXXXXXXX",
		Signal:   "wrong_citation",
	})
	if err == nil {
		t.Error("Feedback with unknown citation: expected error from MarkWrongCitation, got nil")
	}
}

// TestEmbedded_Feedback_MemoryID verifies that Feedback with a MemoryID target
// calls ApplyFeedback. ApplyFeedback is a no-op (not ErrNotFound) for unknown
// memory IDs, so this should succeed with Applied:1.
func TestEmbedded_Feedback_MemoryID(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "feedback-memid-tenant")
	resp, err := ec.Feedback(context.Background(), FeedbackRequest{
		MemoryID: "01JXXXXXXXXXXXXXXXXXXXXXXX",
		Signal:   "save",
	})
	if err != nil {
		t.Fatalf("Feedback with MemoryID: unexpected error: %v", err)
	}
	if resp.Applied != 1 {
		t.Errorf("Feedback MemoryID: Applied want 1, got %d", resp.Applied)
	}
	if resp.Signal != "save" {
		t.Errorf("Feedback MemoryID: Signal want %q, got %q", "save", resp.Signal)
	}
}

// TestEmbedded_Feedback_Citation_Success verifies the happy path of the
// Citation branch: an existing injection is marked wrong_citation successfully,
// returning Applied:1 (covers the return statement after MarkWrongCitation).
func TestEmbedded_Feedback_Citation_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "feedback-cit-ok-tenant")

	// Insert the memory first (injections.memory_id is a FK to memories.id).
	memID := ulid.Make().String()
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, store.Memory{
		ID: memID, Kind: "fact", Content: "feedback citation success test", Status: "active",
	}); err != nil {
		t.Fatalf("Insert memory for FK: %v", err)
	}

	// Now insert the injection.
	citID := ulid.Make().String()
	if err := ec.stack.Store.Injections().Append(ctx, ec.scope, []store.Injection{
		{ID: citID, TenantID: ec.scope.Tenant, MemoryID: memID, Rank: 1, Score: 0.5},
	}); err != nil {
		t.Fatalf("Append injection: %v", err)
	}

	resp, err := ec.Feedback(ctx, FeedbackRequest{
		Citation: citID,
		Signal:   "wrong_citation",
	})
	if err != nil {
		t.Fatalf("Feedback Citation success: %v", err)
	}
	if resp.Applied != 1 {
		t.Errorf("Feedback Citation success: Applied want 1, got %d", resp.Applied)
	}
}

// TestEmbedded_Feedback_ResponseID_WithInjections verifies the ResponseID
// branch with real injections: inserts a memory + injection tied to a specific
// ResponseID, then calls Feedback with that ResponseID and signal="use".
// This exercises the loop body (seen-dedup, ApplyFeedback call, applied counter).
func TestEmbedded_Feedback_ResponseID_WithInjections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "feedback-respid-tenant")

	// Insert a memory so ApplyFeedback has something to update.
	memID := ulid.Make().String()
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, store.Memory{
		ID: memID, Kind: "fact", Content: "feedback response id test", Status: "active",
	}); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	responseID := ulid.Make().String()
	// Insert two injections for the same memory+response (dedup test).
	inj1 := store.Injection{
		ID: ulid.Make().String(), TenantID: ec.scope.Tenant,
		MemoryID: memID, ResponseID: responseID, Rank: 1, Score: 0.9,
	}
	inj2 := store.Injection{
		ID: ulid.Make().String(), TenantID: ec.scope.Tenant,
		MemoryID: memID, ResponseID: responseID, Rank: 2, Score: 0.8,
	}
	if err := ec.stack.Store.Injections().Append(ctx, ec.scope, []store.Injection{inj1, inj2}); err != nil {
		t.Fatalf("Append injections: %v", err)
	}

	resp, err := ec.Feedback(ctx, FeedbackRequest{
		ResponseID: responseID,
		Signal:     "use",
	})
	if err != nil {
		t.Fatalf("Feedback ResponseID with injections: %v", err)
	}
	// Two injections share the same MemoryID; the seen-dedup should result in
	// exactly 1 ApplyFeedback call → Applied:1.
	if resp.Applied != 1 {
		t.Errorf("Feedback ResponseID: Applied want 1, got %d (dedup should collapse 2→1)", resp.Applied)
	}
	if resp.Signal != "use" {
		t.Errorf("Feedback ResponseID: Signal want %q, got %q", "use", resp.Signal)
	}
}

// ---- ResolveCitations ---------------------------------------------------------

// TestEmbedded_ResolveCitations_Empty verifies that ResolveCitations returns an
// error when the Citations slice is empty.
func TestEmbedded_ResolveCitations_Empty(t *testing.T) {
	t.Parallel()
	ec := newWhiteboxClient(t, "resolve-empty-tenant")
	_, err := ec.ResolveCitations(context.Background(), ResolveCitationsRequest{
		Citations: nil,
	})
	if err == nil {
		t.Error("ResolveCitations with empty citations: expected error, got nil")
	}
}

// TestEmbedded_ResolveCitations_ValidInjection exercises the full resolve path:
// inject a memory + injection directly into the store, then call ResolveCitations
// and verify Found:true with a non-nil Memory in the result.
func TestEmbedded_ResolveCitations_ValidInjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "resolve-valid-tenant")

	// Insert the memory.
	memID := ulid.Make().String()
	mem := store.Memory{
		ID:      memID,
		Kind:    "fact",
		Content: "resolve citations whitebox content",
		Status:  "active",
	}
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, mem); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	// Insert the injection (citation handle).
	citID := ulid.Make().String()
	inj := store.Injection{
		ID:       citID,
		TenantID: ec.scope.Tenant,
		MemoryID: memID,
		Rank:     1,
		Score:    0.9,
		Lane:     "lexical",
	}
	if err := ec.stack.Store.Injections().Append(ctx, ec.scope, []store.Injection{inj}); err != nil {
		t.Fatalf("Append injection: %v", err)
	}

	resp, err := ec.ResolveCitations(ctx, ResolveCitationsRequest{
		Citations: []string{citID},
	})
	if err != nil {
		t.Fatalf("ResolveCitations: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("ResolveCitations: want 1 item, got %d", len(resp.Items))
	}
	item := resp.Items[0]
	if !item.Found {
		t.Errorf("ResolveCitations: want Found:true, got Found:false")
	}
	if item.Memory == nil {
		t.Error("ResolveCitations: want non-nil Memory, got nil")
	}
	if item.Memory != nil && item.Memory.ID != memID {
		t.Errorf("ResolveCitations: Memory.ID want %q, got %q", memID, item.Memory.ID)
	}
	// Lane field should be parsed from the CSV "lexical".
	if len(item.Lanes) == 0 {
		t.Error("ResolveCitations: expected Lanes to be populated from Lane CSV")
	}
}

// TestEmbedded_ResolveCitations_MixedFound exercises the case where some
// citations are found and some are not, verifying per-item Found flags.
func TestEmbedded_ResolveCitations_MixedFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "resolve-mixed-tenant")

	// One real injection.
	memID := ulid.Make().String()
	mem := store.Memory{
		ID:      memID,
		Kind:    "fact",
		Content: "mixed resolve test",
		Status:  "active",
	}
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, mem); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}
	citID := ulid.Make().String()
	inj := store.Injection{
		ID:       citID,
		TenantID: ec.scope.Tenant,
		MemoryID: memID,
		Rank:     1,
		Score:    0.8,
	}
	if err := ec.stack.Store.Injections().Append(ctx, ec.scope, []store.Injection{inj}); err != nil {
		t.Fatalf("Append injection: %v", err)
	}

	unknownCit := "01JXXXXXXXXXXXXXXXXXXXXXXX"
	resp, err := ec.ResolveCitations(ctx, ResolveCitationsRequest{
		Citations: []string{citID, unknownCit},
	})
	if err != nil {
		t.Fatalf("ResolveCitations mixed: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("ResolveCitations mixed: want 2 items, got %d", len(resp.Items))
	}

	// Order matches citation order.
	if !resp.Items[0].Found {
		t.Errorf("ResolveCitations mixed: item[0] (known) should be Found:true")
	}
	if resp.Items[1].Found {
		t.Errorf("ResolveCitations mixed: item[1] (unknown) should be Found:false")
	}
}

// ---- safeExcerpt (via Drilldown) ---------------------------------------------

// TestSafeExcerpt_NegativeStart verifies the `s < 0` clamping branch by
// inserting a provenance row with SpanStart=-1, which SQLite stores as-is.
func TestSafeExcerpt_NegativeStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "excerpt-neg-tenant")

	recID := ulid.Make().String()
	if err := ec.stack.Store.Records().Append(ctx, ec.scope, []store.Record{
		{ID: recID, Role: "user", Content: "hello"},
	}); err != nil {
		t.Fatalf("Append record: %v", err)
	}

	memID := ulid.Make().String()
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, store.Memory{
		ID: memID, Kind: "fact", Content: "hello", Status: "active",
	}); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	// SpanStart=-1: safeExcerpt must clamp to 0.
	if err := ec.stack.Store.Memories().AddProvenance(ctx, ec.scope, []store.Provenance{
		{ID: ulid.Make().String(), MemoryID: memID, RecordID: recID, SpanStart: -1, SpanEnd: 3},
	}); err != nil {
		t.Fatalf("AddProvenance: %v", err)
	}

	resp, err := ec.Drilldown(ctx, DrilldownRequest{MemoryID: memID})
	if err != nil {
		t.Fatalf("Drilldown (neg start): %v", err)
	}
	if len(resp.Spans) == 0 {
		t.Fatal("expected at least one span")
	}
	if resp.Spans[0].Excerpt != "hel" {
		t.Errorf("excerpt after clamping SpanStart=-1: want %q, got %q", "hel", resp.Spans[0].Excerpt)
	}
}

// TestSafeExcerpt_EndBeforeStart verifies the `e < s` clamping branch by
// inserting a provenance row with SpanEnd < SpanStart.
func TestSafeExcerpt_EndBeforeStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ec := newWhiteboxClient(t, "excerpt-ebs-tenant")

	recID := ulid.Make().String()
	if err := ec.stack.Store.Records().Append(ctx, ec.scope, []store.Record{
		{ID: recID, Role: "user", Content: "hello world"},
	}); err != nil {
		t.Fatalf("Append record: %v", err)
	}

	memID := ulid.Make().String()
	if err := ec.stack.Store.Memories().Insert(ctx, ec.scope, store.Memory{
		ID: memID, Kind: "fact", Content: "hello world", Status: "active",
	}); err != nil {
		t.Fatalf("Insert memory: %v", err)
	}

	// SpanEnd=1 < SpanStart=5: safeExcerpt clamps e to s, yielding empty excerpt.
	if err := ec.stack.Store.Memories().AddProvenance(ctx, ec.scope, []store.Provenance{
		{ID: ulid.Make().String(), MemoryID: memID, RecordID: recID, SpanStart: 5, SpanEnd: 1},
	}); err != nil {
		t.Fatalf("AddProvenance: %v", err)
	}

	resp, err := ec.Drilldown(ctx, DrilldownRequest{MemoryID: memID})
	if err != nil {
		t.Fatalf("Drilldown (e<s): %v", err)
	}
	if len(resp.Spans) == 0 {
		t.Fatal("expected at least one span")
	}
	// After clamping e = s = 5: content[5:5] = "".
	if resp.Spans[0].Excerpt != "" {
		t.Errorf("excerpt after e<s clamp: want empty string, got %q", resp.Spans[0].Excerpt)
	}
}

// ---- identity.Scope helper check --------------------------------------------

// TestScope_Tenant just confirms the identity import compiles cleanly.
// It prevents the import from being flagged as unused if other tests are skipped.
func TestScope_Tenant(_ *testing.T) {
	_ = identity.Scope{Tenant: "compile-check"}
}
