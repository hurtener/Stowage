package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// RunConfig configures a CI eval run.
type RunConfig struct {
	// FixturesDir is the path to eval/ci-fixtures/.
	FixturesDir string
	// DisableLane: when non-empty, any item the named lane contributed to
	// surfacing is filtered out of the scored results (presence semantics —
	// discard results that relied on the broken lane). This is the gate-bite
	// harness hook (AC-3): it proves the LANE FILTER alone bites the gate.
	//
	// Note (D-067 Wave-A checkpoint): the limit cap (CapLimitToOne) is now
	// DECOUPLED from DisableLane. Previously DisableLane silently also capped the
	// retrieve limit at 1, so a "degradation" could come from fetching fewer
	// results rather than from the missing lane — the test could not prove the
	// FILTER bit. The gate-bite test now sets DisableLane WITHOUT the cap, so any
	// observed degradation is attributable to the lane filter alone.
	//
	// Note (D-069): this hook originally used "only-lane" filter semantics and
	// named the "lexical" lane. That only ever bit because the sqlite FTS lane
	// hard-errored on the fixture queries (every one ends in "?", BUG-4), so the
	// degraded run returned nothing. Once BUG-4 was fixed the lexical/queries
	// lanes work and the answers are robustly multi-lane; the hook now uses
	// presence semantics and the test disables EACH production lane to keep the
	// gate honestly biting on a lexical/queries regression too.
	DisableLane string
	// CapLimitToOne, when true, caps the retrieve limit at 1 (an orthogonal
	// quality degradation). Kept as an explicit, opt-in knob so it cannot be
	// confused with the DisableLane filter degradation.
	CapLimitToOne bool
	// RetrieveLimit is the number of results to fetch per question. Default 5.
	RetrieveLimit int
	// EnableRerank, when true, issues retrieve requests with the "precise"
	// profile so the cross-encoder rerank pass runs (D-075). Default OFF — the
	// deterministic mock CI run must NOT rerank (it would call a model and shift
	// scores); full mode turns it ON, paired with the harness retriever's
	// WithRerankModel wiring in NewTestServer.
	EnableRerank bool
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
//
// Fix (bbd134d diagnosis): the previous approach wrote one combined mock-script
// file covering all 8 conversations and then flushed each buffer sequentially.
// The global file-offset counter in the mock driver became misaligned when any
// intervening gateway.Complete call (e.g. a reconcile decision call) consumed a
// script entry meant for a later conversation's extraction, causing conversations
// 2-8 to receive wrong scripts and produce zero memories.
//
// The fix uses PushScript (in-process queue) instead of the lazy file.  The
// in-process queue takes priority over the file (see mock.Driver.Complete).
// Each conversation's extraction script is pushed immediately before its buffer
// flush, and RunCI waits for at least one new active memory after each flush
// before pushing the next script.  This ensures (a) the queue holds exactly one
// entry when the extraction Complete call fires, and (b) the in-process queue is
// drained before the next conversation's entry is pushed.
func (r *Runner) RunCI(ctx context.Context) (*RunResult, error) {
	fixtures, err := LoadCIFixtures(r.cfg.FixturesDir)
	if err != nil {
		return nil, fmt.Errorf("load fixtures: %w", err)
	}

	// Ingest all conversations and collect their record IDs.
	// All ingestion is done upfront so record IDs are available for placeholder
	// rendering before any flush fires.
	allRecordIDs := make(map[string][]string) // convID → slice of IDs in ingest order
	for i := range fixtures.Conversations {
		conv := &fixtures.Conversations[i]
		ids, err := r.ingestConversation(ctx, conv)
		if err != nil {
			return nil, fmt.Errorf("ingest %s: %w", conv.ID, err)
		}
		allRecordIDs[conv.ID] = ids
	}

	// Process each conversation: push its extraction script into the in-process
	// mock queue, flush the buffer, then wait for at least one new memory before
	// moving on.  The per-conversation wait serves a dual purpose:
	//   1. It confirms the extraction Complete call has fired and returned (the
	//      queue entry was consumed), so the queue is empty before the next push.
	//   2. It acts as a fixture integrity check: if a conversation produces zero
	//      new active memories the wait times out and RunCI returns a fatal error.
	prevMemCount := r.srv.ActiveMemoryCount(ctx)
	for _, conv := range fixtures.Conversations {
		ids := allRecordIDs[conv.ID]
		rendered := RenderMockScript(conv.MockScriptTemplate, ids)

		var entries []json.RawMessage
		if err := json.Unmarshal(rendered, &entries); err != nil {
			return nil, fmt.Errorf("parse rendered script for %s: %w", conv.ID, err)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("fixture integrity: empty mock script for %s", conv.ID)
		}

		// Push this conversation's extraction response into the in-process queue.
		// The extraction stage's next Complete() call will pop it (priority over file).
		r.srv.PushExtractionScript(entries[0])

		// Flush this conversation's buffer to trigger the extraction stage.
		if err := r.flushBuffer(ctx, conv.ID); err != nil {
			return nil, fmt.Errorf("flush %s: %w", conv.ID, err)
		}

		// Fixture integrity check: wait until at least one new active memory appears.
		// If the conversation produced zero memories (wrong script, validation drop,
		// or pipeline bug), WaitForMemories times out → fatal error.
		if err := r.srv.WaitForMemories(ctx, prevMemCount+1); err != nil {
			return nil, fmt.Errorf(
				"fixture integrity: conversation %s committed 0 active memories — "+
					"verify mock script provenance record IDs match flushed records: %w",
				conv.ID, err,
			)
		}
		prevMemCount = r.srv.ActiveMemoryCount(ctx)
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
		Profile      string `json:"profile,omitempty"`
	}
	type retrieveResp struct {
		Items []retrieveItem `json:"items"`
	}

	// The precise profile (EnableRerank) is what runs the cross-encoder rerank
	// pass; the harness retriever is wired with WithRerankModel only in full mode
	// (D-075). CI/mock leaves this empty → balanced profile, no rerank.
	profile := ""
	if r.cfg.EnableRerank {
		profile = "precise"
	}

	// The limit cap is an explicit, opt-in degradation, decoupled from the lane
	// filter (D-067 Wave-A checkpoint) so the gate-bite test proves the FILTER
	// bites, not the cap.
	limit := r.cfg.RetrieveLimit
	if r.cfg.CapLimitToOne && limit > 1 {
		limit = 1
	}

	start := time.Now()
	status, body, err := r.srv.DoJSON(ctx, "POST", "/v1/retrieve", retrieveReq{
		Query:        q.Text,
		Limit:        limit,
		IncludeLanes: true,
		Profile:      profile,
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
		// Simulate the named lane being down: discard any result that the disabled
		// lane contributed to surfacing (presence semantics, not "only-lane"). This
		// degrades retrieval regardless of multi-lane redundancy — see RunConfig
		// for why "only-lane" semantics stopped biting once BUG-4 was fixed (D-069).
		if containsLane(item.Lanes, disableLane) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func containsLane(lanes []string, lane string) bool {
	for _, l := range lanes {
		if l == lane {
			return true
		}
	}
	return false
}
