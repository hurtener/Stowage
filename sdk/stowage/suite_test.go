// Package stowage_test contains the shared parity test suite (AC-1).
// Both http_test.go and embedded_test.go call RunSuite with their respective
// constructors; the same assertions prove both implementations are equivalent.
package stowage_test

import (
	"context"
	"testing"

	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// RunSuite runs the full Client conformance suite. t is the top-level test;
// newClient is called once per sub-test to get a fresh client (both HTTP and
// embedded suites pass the same client instance via a factory that returns it
// each time — lifecycle is managed by the caller).
//
// The suite covers:
//  1. Ingest: batch of records → ACK with IDs
//  2. Retrieve: ingest then retrieve → non-empty result
//  3. Topics: list → non-error response
//  4. Feedback: response_id signal → applied ≥ 0
//  5. Drilldown (citation): invalid citation → error, not panic
//  6. ResolveCitations: unknown citation → found:false, no error
//  7. Playbook: always returns stub in Phase 17
func RunSuite(t *testing.T, client stowage.Client) {
	t.Helper()
	ctx := context.Background()

	// ── 1. Ingest ──────────────────────────────────────────────────────────
	t.Run("ingest_basic", func(t *testing.T) {
		t.Parallel()
		resp, err := client.Ingest(ctx, stowage.IngestRequest{
			Records: []stowage.RecordInput{
				{Role: "user", Content: "The project started in 2024.", SessionID: "s1"},
				{Role: "assistant", Content: "I remember the kickoff was in Q1.", SessionID: "s1"},
			},
		})
		if err != nil {
			t.Fatalf("Ingest error: %v", err)
		}
		if len(resp.IDs) != 2 {
			t.Errorf("Ingest: want 2 IDs, got %d", len(resp.IDs))
		}
		for _, id := range resp.IDs {
			if id == "" {
				t.Error("Ingest: empty ID in response")
			}
		}
	})

	// ── 2. Retrieve ────────────────────────────────────────────────────────
	t.Run("retrieve_after_ingest", func(t *testing.T) {
		// Ingest first, then retrieve. Mock gateway means vector lane is
		// degraded (no embeddings), but lexical + structured lanes return results
		// after the pipeline processes them. We just assert the response shape.
		resp, err := client.Ingest(ctx, stowage.IngestRequest{
			Records: []stowage.RecordInput{
				{Role: "user", Content: "stowage retrieve parity check"},
			},
		})
		if err != nil {
			t.Fatalf("Ingest before retrieve: %v", err)
		}
		_ = resp // IDs used implicitly

		// Retrieve — mock gateway means degraded:true is acceptable.
		rResp, err := client.Retrieve(ctx, stowage.RetrieveRequest{
			Query: "stowage retrieve parity",
			Limit: 5,
		})
		if err != nil {
			t.Fatalf("Retrieve error: %v", err)
		}
		// Basic shape assertions: response always has a ResponseID and API field.
		if rResp.ResponseID == "" {
			t.Error("Retrieve: empty ResponseID")
		}
		if rResp.API != "v1" {
			t.Errorf("Retrieve: API want %q, got %q", "v1", rResp.API)
		}
		// Items may be empty if pipeline hasn't processed yet — that's fine.
		// Degraded is acceptable with mock gateway.
	})

	// ── 3. Topics ──────────────────────────────────────────────────────────
	t.Run("topics_list", func(t *testing.T) {
		t.Parallel()
		resp, err := client.Topics(ctx)
		if err != nil {
			t.Fatalf("Topics error: %v", err)
		}
		// Topics may be empty; just assert no error and Topics is non-nil.
		if resp.Topics == nil {
			t.Error("Topics: nil Topics slice")
		}
	})

	// ── 4. Feedback ────────────────────────────────────────────────────────
	t.Run("feedback_unknown_response_id", func(t *testing.T) {
		t.Parallel()
		// A response_id that has no injections should return Applied:0, no error.
		resp, err := client.Feedback(ctx, stowage.FeedbackRequest{
			ResponseID: "01JXXXXXXXXXXXXXXXXXXXXXXX",
			Signal:     "use",
		})
		if err != nil {
			t.Fatalf("Feedback error: %v", err)
		}
		if resp.Applied < 0 {
			t.Errorf("Feedback: Applied should be ≥ 0, got %d", resp.Applied)
		}
		if resp.Signal != "use" {
			t.Errorf("Feedback: Signal echoed wrong: got %q want %q", resp.Signal, "use")
		}
	})

	t.Run("feedback_validation_no_target", func(t *testing.T) {
		t.Parallel()
		_, err := client.Feedback(ctx, stowage.FeedbackRequest{Signal: "use"})
		if err == nil {
			t.Error("Feedback with no target: expected error, got nil")
		}
	})

	// ── 5. Drilldown (unknown citation) ────────────────────────────────────
	t.Run("drilldown_unknown_citation", func(t *testing.T) {
		t.Parallel()
		// An unknown citation should return an error (not found), not a panic.
		_, err := client.Drilldown(ctx, stowage.DrilldownRequest{
			Citation: "01JXXXXXXXXXXXXXXXXXXXXXXX",
		})
		if err == nil {
			t.Error("Drilldown with unknown citation: expected error, got nil")
		}
	})

	// ── 6. ResolveCitations ────────────────────────────────────────────────
	t.Run("resolve_citations_unknown", func(t *testing.T) {
		t.Parallel()
		resp, err := client.ResolveCitations(ctx, stowage.ResolveCitationsRequest{
			Citations: []string{"01JXXXXXXXXXXXXXXXXXXXXXXX"},
		})
		if err != nil {
			t.Fatalf("ResolveCitations error: %v", err)
		}
		if len(resp.Items) != 1 {
			t.Fatalf("ResolveCitations: want 1 item, got %d", len(resp.Items))
		}
		if resp.Items[0].Found {
			t.Error("ResolveCitations: unknown citation should have Found:false")
		}
	})

	// ── 7. Playbook ────────────────────────────────────────────────────────
	t.Run("playbook_stub", func(t *testing.T) {
		t.Parallel()
		resp, err := client.Playbook(ctx, stowage.PlaybookRequest{})
		if err != nil {
			t.Fatalf("Playbook error: %v", err)
		}
		if !resp.Stub {
			t.Error("Playbook: expected Stub:true in Phase 17")
		}
	})

	// ── 8. GetMemory (D-070) — unknown id errors identically on both impls ───
	t.Run("get_memory_unknown", func(t *testing.T) {
		t.Parallel()
		_, err := client.GetMemory(ctx, "01JXXXXXXXXXXXXXXXXXXXXXXX")
		if err == nil {
			t.Error("GetMemory unknown id: expected error, got nil")
		}
		// Empty id is a client-side validation error on both impls.
		if _, err := client.GetMemory(ctx, ""); err == nil {
			t.Error("GetMemory empty id: expected validation error, got nil")
		}
	})

	// ── 9. Rollback (D-070) — unknown id errors identically on both impls ────
	t.Run("rollback_unknown", func(t *testing.T) {
		t.Parallel()
		_, err := client.Rollback(ctx, stowage.RollbackRequest{MemoryID: "01JXXXXXXXXXXXXXXXXXXXXXXX"})
		if err == nil {
			t.Error("Rollback unknown id: expected error, got nil")
		}
		if _, err := client.Rollback(ctx, stowage.RollbackRequest{}); err == nil {
			t.Error("Rollback empty id: expected validation error, got nil")
		}
	})

	// ── 10. ResolveMemory (D-070) — unknown id + bad action parity ───────────
	t.Run("resolve_memory_unknown", func(t *testing.T) {
		t.Parallel()
		_, err := client.ResolveMemory(ctx, stowage.ResolveRequest{
			MemoryID: "01JXXXXXXXXXXXXXXXXXXXXXXX", Action: "confirm",
		})
		if err == nil {
			t.Error("ResolveMemory unknown id: expected error, got nil")
		}
		if _, err := client.ResolveMemory(ctx, stowage.ResolveRequest{
			MemoryID: "01JXXXXXXXXXXXXXXXXXXXXXXX", Action: "explode",
		}); err == nil {
			t.Error("ResolveMemory bad action: expected validation error, got nil")
		}
	})
}
