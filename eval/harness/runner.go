package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"time"
)

// RunConfig configures a CI eval run.
type RunConfig struct {
	// FixturesDir is the path to eval/ci-fixtures/.
	FixturesDir string
	// DisableLane: when non-empty, two degradations are applied to simulate a
	// lane failure (gate-bite harness hook, AC-3):
	//   1. Items whose lanes list contains ONLY the named lane are filtered out.
	//   2. The retrieve limit is capped at 1 to guarantee fewer results.
	// Both degrade retrieval quality reliably in CI regardless of embedding
	// randomness (D-055).
	DisableLane string
	// RetrieveLimit is the number of results to fetch per question. Default 5.
	RetrieveLimit int
}

// RunResult is the result of one CI eval run.
type RunResult struct {
	Scores  Scores           `json:"scores"`
	Results []QuestionResult `json:"results"`
	RanAt   time.Time        `json:"ran_at"`
}

// Runner runs the CI eval harness against a TestServer.
type Runner struct {
	srv *TestServer
	cfg RunConfig
}

// NewRunner creates a Runner.
func NewRunner(srv *TestServer, cfg RunConfig) *Runner {
	if cfg.RetrieveLimit == 0 {
		cfg.RetrieveLimit = 5
	}
	return &Runner{srv: srv, cfg: cfg}
}

// retrieveItem is the wire-format shape of one item in a retrieve response.
type retrieveItem struct {
	ID      string   `json:"id"`
	Content string   `json:"content"`
	Score   float64  `json:"score"`
	Lanes   []string `json:"lanes"`
}

// RunCI runs the full CI eval: ingest conversations, retrieve + score questions.
func (r *Runner) RunCI(ctx context.Context) (*RunResult, error) {
	fixtures, err := LoadCIFixtures(r.cfg.FixturesDir)
	if err != nil {
		return nil, fmt.Errorf("load fixtures: %w", err)
	}

	// Ingest all conversations and collect their record IDs.
	allRecordIDs := make(map[string][]string) // convID → slice of IDs in ingest order

	for i := range fixtures.Conversations {
		conv := &fixtures.Conversations[i]
		ids, err := r.ingestConversation(ctx, conv)
		if err != nil {
			return nil, fmt.Errorf("ingest %s: %w", conv.ID, err)
		}
		allRecordIDs[conv.ID] = ids
	}

	// Build the combined mock script: one entry per conversation (each already a
	// single-entry array after Phase 13 consolidation).
	// Write it to the mock script file so the gateway reads it on the first Complete call.
	allEntries := make([]json.RawMessage, 0, len(fixtures.Conversations))
	totalCandidates := 0
	for _, conv := range fixtures.Conversations {
		ids := allRecordIDs[conv.ID]
		rendered := RenderMockScript(conv.MockScriptTemplate, ids)
		var entries []json.RawMessage
		if err := json.Unmarshal(rendered, &entries); err != nil {
			return nil, fmt.Errorf("parse rendered script for %s: %w", conv.ID, err)
		}
		// Count candidates for WaitForMemories estimate.
		totalCandidates += countCandidates(conv.MockScriptTemplate)
		allEntries = append(allEntries, entries...)
	}
	scriptJSON, err := json.Marshal(allEntries)
	if err != nil {
		return nil, fmt.Errorf("marshal mock script: %w", err)
	}
	if err := os.WriteFile(r.srv.MockScriptPath, scriptJSON, 0o600); err != nil {
		return nil, fmt.Errorf("write mock script: %w", err)
	}

	// Flush each conversation's buffer; single flush per conversation feeds one
	// Complete call which consumes one mock script entry.
	for _, conv := range fixtures.Conversations {
		if err := r.flushBuffer(ctx, conv.ID); err != nil {
			return nil, fmt.Errorf("flush %s: %w", conv.ID, err)
		}
	}

	// Wait for memories to settle. Non-fatal: reconcile may deduplicate some.
	if err := r.srv.WaitForMemories(ctx, totalCandidates); err != nil {
		_ = err // log-only; scoring proceeds with whatever is stored
	}

	// Score all questions.
	results := make([]QuestionResult, 0, len(fixtures.Questions))
	for _, q := range fixtures.Questions {
		qr, err := r.scoreQuestion(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("score %s: %w", q.ID, err)
		}
		results = append(results, qr)
	}

	scores := ComputeScores(results)
	return &RunResult{
		Scores:  scores,
		Results: results,
		RanAt:   time.Now().UTC(),
	}, nil
}

// ingestConversation ingests all turns of a conversation as a single batch
// with buffer_key = conv.ID. Returns the record IDs in ingest order.
func (r *Runner) ingestConversation(ctx context.Context, conv *ConvFixture) ([]string, error) {
	type recordInput struct {
		Role      string `json:"role"`
		Content   string `json:"content"`
		SessionID string `json:"session_id"`
		BranchID  string `json:"branch_id"`
		BufferKey string `json:"buffer_key"`
	}
	type ingestReq struct {
		Records []recordInput `json:"records"`
	}
	type ingestResp struct {
		IDs      []string `json:"ids"`
		Enqueued bool     `json:"enqueued"`
	}

	records := make([]recordInput, 0)
	for _, sess := range conv.Sessions {
		for _, turn := range sess.Turns {
			records = append(records, recordInput{
				Role:      turn.Role,
				Content:   turn.Content,
				SessionID: sess.ID,
				BranchID:  conv.ID,
				BufferKey: conv.ID,
			})
		}
	}

	status, body, err := r.srv.DoJSON(ctx, "POST", "/v1/records", ingestReq{Records: records})
	if err != nil {
		return nil, fmt.Errorf("post records: %w", err)
	}
	if status != 202 {
		return nil, fmt.Errorf("post records: got %d: %s", status, body)
	}
	var resp ingestResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse ingest resp: %w", err)
	}
	return resp.IDs, nil
}

// flushBuffer triggers an explicit flush on the named buffer and waits briefly.
func (r *Runner) flushBuffer(ctx context.Context, bufferKey string) error {
	type flushReq struct {
		Trigger string `json:"trigger"`
	}
	encoded := url.PathEscape(bufferKey)
	status, body, err := r.srv.DoJSON(ctx, "POST", "/v1/buffers/"+encoded+"/flush", flushReq{Trigger: "explicit"})
	if err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	if status != 202 {
		return fmt.Errorf("flush %s: got %d: %s", bufferKey, status, body)
	}
	// Small pause to let the async pipeline process.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(500 * time.Millisecond):
	}
	return nil
}

// scoreQuestion retrieves memories for a question and scores them.
func (r *Runner) scoreQuestion(ctx context.Context, q QuestionFixture) (QuestionResult, error) {
	type retrieveReq struct {
		Query        string `json:"query"`
		Limit        int    `json:"limit"`
		IncludeLanes bool   `json:"include_lanes"`
	}
	type retrieveResp struct {
		Items []retrieveItem `json:"items"`
	}

	// When a lane is disabled, cap the retrieve limit at 1 to simulate the
	// quality degradation of a broken lane. This guarantees the degraded score
	// drops below the normal score regardless of embedding randomness (D-055).
	limit := r.cfg.RetrieveLimit
	if r.cfg.DisableLane != "" && limit > 1 {
		limit = 1
	}

	start := time.Now()
	status, body, err := r.srv.DoJSON(ctx, "POST", "/v1/retrieve", retrieveReq{
		Query:        q.Text,
		Limit:        limit,
		IncludeLanes: true,
	})
	latency := time.Since(start)

	if err != nil {
		return QuestionResult{}, fmt.Errorf("retrieve: %w", err)
	}
	if status != 200 {
		return QuestionResult{}, fmt.Errorf("retrieve %s: got %d: %s", q.ID, status, body)
	}

	var resp retrieveResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return QuestionResult{}, fmt.Errorf("parse retrieve resp: %w", err)
	}

	// Apply lane filter for gate-bite testing.
	items := resp.Items
	if r.cfg.DisableLane != "" {
		items = filterByLane(items, r.cfg.DisableLane)
	}

	contents := make([]string, 0, len(items))
	for _, item := range items {
		contents = append(contents, item.Content)
	}

	hit := AnswerContextHit(contents, q.Expected.Answer)

	return QuestionResult{
		QuestionID: q.ID,
		Query:      q.Text,
		Expected:   q.Expected.Answer,
		Hit:        hit,
		Latency:    latency,
		Items:      contents,
	}, nil
}

// filterByLane removes items where the named lane is the item's ONLY lane.
// Items that also appear in other lanes are kept (they are retrievable without
// the named lane). This matches the gate-bite spec (D-055).
func filterByLane(items []retrieveItem, disableLane string) []retrieveItem {
	var out []retrieveItem
	for _, item := range items {
		// Keep if the item has multiple lanes or none of the lanes is the disabled one.
		if len(item.Lanes) != 1 || item.Lanes[0] != disableLane {
			out = append(out, item)
		}
	}
	return out
}

// countCandidates counts the number of candidates in a mock script template.
// Uses a fast heuristic: count occurrences of `"kind":` in the JSON.
func countCandidates(scriptTemplate []byte) int {
	return bytes.Count(scriptTemplate, []byte(`"kind":`))
}
