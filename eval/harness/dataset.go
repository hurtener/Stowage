package harness

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hurtener/stowage/eval/datasets"

	// Blank-import the dataset subpackages so their registry init() runs for any
	// harness consumer (the full-mode runner, the CLI, the wiring tests). Adding a
	// benchmark is a new subpackage + its import here — no central switch (D-096).
	_ "github.com/hurtener/stowage/eval/datasets/locomo"
	_ "github.com/hurtener/stowage/eval/datasets/longmemeval"
)

// RunDatasetOpts tunes a single RunDataset call.
type RunDatasetOpts struct {
	// Limit caps the number of questions scored (deterministic by question ID).
	// <= 0 runs every question.
	Limit int
	// Judge, when true, runs the reader+judge over each question's retrieved
	// context (needs a live gateway). Off → deterministic answer_context_hit only.
	Judge bool
	// Settle is the final hard quiescence barrier before scoring (scoring a
	// partially-ingested store invalidates the run). Default 20m.
	Settle time.Duration
	// PerConvSettle paces ingestion: settle each conversation before the next so
	// the bounded pipeline never sees the whole haystack at once. 0 → skip.
	PerConvSettle time.Duration
	// EmbedSettle is a short pause after the final quiescence barrier to let the
	// async embedder finish indexing committed memories before scoring — quiescence
	// gates on unprocessed records + stable memory count, NOT on pending embeddings,
	// so the vector lane can be under-populated without it. 0 → skip (the CI wiring
	// test scores via lexical/anticipated lanes and needs no embeddings).
	EmbedSettle time.Duration
	// BeforeFlush, when set, is invoked after a conversation is ingested and
	// before its buffer flush, with the conversation's record IDs. The CI/mock path
	// sets it to push a deterministic extraction script (auto-flush is suppressed in
	// mock mode, so the deliberate flush must see the records — hence the pre-flush
	// WaitForBuffered barrier runs only here). Full mode leaves it nil: the real
	// gateway extracts and the Count/MaxAge auto-flush triggers drain the buffer, so
	// a fixed-count buffer barrier would deadlock on a >trigger-count conversation.
	BeforeFlush func(convID string, recordIDs []string) error
}

// DatasetResult is the outcome of a RunDataset call.
type DatasetResult struct {
	Results     []QuestionResult
	Scores      Scores
	Verdicts    []string // judge verdicts (judged mode only), in scored order
	JudgeErrors int      // questions whose judge call errored (judged mode only)
}

// maxConsecutiveJudgeErrors aborts a judged run when the gateway/model is clearly
// broken — otherwise AnswerQuality would be silently computed over only the subset
// that happened to succeed (a misleading release-gate number). Mirrors the prior
// TestFullMode fail-fast.
const maxConsecutiveJudgeErrors = 5

// RunDataset is the dataset-agnostic public-benchmark runner (D-096): it ingests
// the conversations the question slice needs, settles the pipeline, scores each
// question (and optionally judges it), and returns the results. LongMemEval,
// longmemeval_s, and LoCoMo all flow through this single path — the runner is the
// one core, the dataset is a parameter (the registry resolves name → normalize).
//
// The full ingest→settle→retrieve→judge loop needs a live gateway (operator-run);
// the CI wiring test drives it with the mock gateway via BeforeFlush + a scripted
// extraction, so the dataset→runner wiring is proven without a paid call.
func RunDataset(ctx context.Context, srv *TestServer, runner *Runner, convs []datasets.Conversation, questions []datasets.Question, opts RunDatasetOpts) (*DatasetResult, error) {
	// Deterministic question slice.
	sort.Slice(questions, func(i, j int) bool { return questions[i].ID < questions[j].ID })
	if opts.Limit > 0 && len(questions) > opts.Limit {
		questions = questions[:opts.Limit]
	}

	// Only the conversations the selected questions reference are ingested.
	need := map[string]bool{}
	for _, q := range questions {
		need[q.ConvID] = true
	}
	convByID := map[string]datasets.Conversation{}
	for _, c := range convs {
		convByID[c.ID] = c
	}

	for id := range need {
		c, ok := convByID[id]
		if !ok {
			return nil, fmt.Errorf("question references unknown conversation %s", id)
		}
		fix := toFixture(c)
		ids, err := runner.ingestConversation(ctx, &fix)
		if err != nil {
			return nil, fmt.Errorf("ingest %s: %w", id, err)
		}
		if opts.BeforeFlush != nil {
			// Mock/CI path: auto-flush is suppressed, so the deliberate flush must see
			// the records — wait out the async ingest→buffer append first (D-096). Only
			// here: in full mode the auto-flush triggers drain the buffer, so a
			// fixed-count barrier would never reach len(ids) on a large conversation.
			if err := srv.WaitForBuffered(ctx, fix.ID, len(ids)); err != nil {
				return nil, fmt.Errorf("buffer %s: %w", id, err)
			}
			if err := opts.BeforeFlush(id, ids); err != nil {
				return nil, fmt.Errorf("before-flush %s: %w", id, err)
			}
		}
		if err := runner.flushBuffer(ctx, fix.ID); err != nil {
			return nil, fmt.Errorf("flush %s: %w", id, err)
		}
		if opts.PerConvSettle > 0 {
			if err := srv.WaitForQuiescence(ctx, opts.PerConvSettle); err != nil {
				// Non-fatal: the re-enqueue sweep recovers; the final barrier still gates.
				_ = err
			}
		}
	}

	settle := opts.Settle
	if settle <= 0 {
		settle = 20 * time.Minute
	}
	if err := srv.WaitForQuiescence(ctx, settle); err != nil {
		return nil, fmt.Errorf("pipeline did not settle — run would be invalid: %w", err)
	}
	// Let the async embedder catch up before scoring (quiescence does not gate on
	// pending embeddings — see EmbedSettle).
	if opts.EmbedSettle > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.EmbedSettle):
		}
	}

	results := make([]QuestionResult, 0, len(questions))
	var verdicts []string
	judgeErrors := 0
	consecJudgeErr := 0
	for _, q := range questions {
		qr, err := runner.scoreQuestion(ctx, toQuestionFixture(q))
		if err != nil {
			return nil, fmt.Errorf("score %s: %w", q.ID, err)
		}
		if opts.Judge {
			jr, jerr := JudgeQuestion(ctx, srv.Gateway(), q.Text, q.Expected.Answer, qr.Items)
			if jerr != nil {
				judgeErrors++
				consecJudgeErr++
				// Fail loud: a broken gateway must not yield an AnswerQuality computed
				// over only the lucky subset (restores the prior fail-fast).
				if consecJudgeErr >= maxConsecutiveJudgeErrors {
					return nil, fmt.Errorf("judged-QA aborted after %d consecutive judge errors: %w", consecJudgeErr, jerr)
				}
			} else {
				consecJudgeErr = 0
				qr.ReaderAnswer = jr.Answer
				qr.JudgeVerdict = jr.Verdict
				qr.JudgeJustification = jr.Justification
				verdicts = append(verdicts, jr.Verdict)
			}
		}
		results = append(results, qr)
	}

	scores := ComputeScores(results)
	if opts.Judge && len(verdicts) > 0 {
		quality, n := JudgedQuality(verdicts)
		scores.AnswerQuality = &quality
		scores.JudgedCount = n
	}
	return &DatasetResult{Results: results, Scores: scores, Verdicts: verdicts, JudgeErrors: judgeErrors}, nil
}

// toFixture converts a normalized dataset conversation into the ingestion fixture.
func toFixture(c datasets.Conversation) ConvFixture {
	fix := ConvFixture{ID: c.ID}
	for _, s := range c.Sessions {
		sf := SessionFixture{ID: s.ID}
		for _, turn := range s.Turns {
			sf.Turns = append(sf.Turns, TurnFixture{Role: turn.Role, Content: turn.Content})
		}
		fix.Sessions = append(fix.Sessions, sf)
	}
	return fix
}

// toQuestionFixture maps a normalized dataset question to the scoring fixture.
func toQuestionFixture(q datasets.Question) QuestionFixture {
	qf := QuestionFixture{ID: q.ID, Text: q.Text, ConvID: q.ConvID, Category: q.Category}
	qf.Expected.Answer = q.Expected.Answer
	return qf
}
