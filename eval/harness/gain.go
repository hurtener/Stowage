package harness

import (
	"context"
	"fmt"
	"time"

	"github.com/hurtener/stowage/eval/datasets"
	"github.com/hurtener/stowage/eval/gain"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/retrieval"
)

// gain harness (Phase 20b, D-078): measures whether memory improves task
// completion. Each scenario is answered by the Phase-20 reader+judge twice — once
// with retrieved memory context (memory-ON) and once with none (memory-OFF) — and
// gain = quality(on) − quality(off). The reader is the stand-in agent loop (Harbor
// is a separate codebase). All model access is the schema-constrained gateway seam
// (§10/P5). The full ingest→settle→retrieve→judge loop needs a live gateway
// (operator-run); the scoring/aggregation here is pure and CI-tested.

// GainResult is one scenario's memory-on-vs-off measurement.
type GainResult struct {
	ScenarioID string  `json:"scenario_id"`
	Category   string  `json:"category"`
	Question   string  `json:"question"`
	Expected   string  `json:"expected"`
	AnswerOff  string  `json:"answer_off"`
	AnswerOn   string  `json:"answer_on"`
	VerdictOff string  `json:"verdict_off"`
	VerdictOn  string  `json:"verdict_on"`
	QualityOff float64 `json:"quality_off"`
	QualityOn  float64 `json:"quality_on"`
	Gain       float64 `json:"gain"`
}

// quality maps a judge verdict to a [0,1] score: correct=1, partial=½, else 0.
func quality(verdict string) float64 {
	switch verdict {
	case "correct":
		return 1.0
	case "partial":
		return 0.5
	default:
		return 0.0
	}
}

// GainSummary is the aggregate over a set of GainResults.
type GainSummary struct {
	Total          int     `json:"total"`
	NonNegative    int     `json:"non_negative"`
	MeanGain       float64 `json:"mean_gain"`
	MeanQualityOn  float64 `json:"mean_quality_on"`
	MeanQualityOff float64 `json:"mean_quality_off"`
}

// AggregateGain computes the aggregate gain over per-scenario results. Mean gain ≥ 0
// is the operator-run release gate (RFC §12).
func AggregateGain(results []GainResult) GainSummary {
	if len(results) == 0 {
		return GainSummary{}
	}
	var sumGain, sumOn, sumOff float64
	nonNeg := 0
	for _, r := range results {
		sumGain += r.Gain
		sumOn += r.QualityOn
		sumOff += r.QualityOff
		if r.Gain >= 0 {
			nonNeg++
		}
	}
	n := float64(len(results))
	return GainSummary{
		Total:          len(results),
		NonNegative:    nonNeg,
		MeanGain:       sumGain / n,
		MeanQualityOn:  sumOn / n,
		MeanQualityOff: sumOff / n,
	}
}

// scenarioToFixture converts a gain.Scenario's turns into a single-session
// ConvFixture for ingestion.
func scenarioToFixture(sc gain.Scenario) ConvFixture {
	sf := SessionFixture{ID: sc.ID + "-s1"}
	for _, t := range sc.Turns {
		sf.Turns = append(sf.Turns, TurnFixture{Role: t.Role, Content: t.Content})
	}
	return ConvFixture{ID: sc.ID, Category: sc.Category, Sessions: []SessionFixture{sf}}
}

// judgeOnOff runs the reader+judge for the memory-OFF (no context) and memory-ON
// (provided items) conditions and assembles the GainResult. Pure of ingestion —
// CI-testable with a fakeGateway. onItems carries the typed CURRENT/SUPERSEDED
// partition the runner already built (wave-0 fix, D-141) so a stale companion
// in the memory-ON context doesn't leak into the reader's CURRENT section.
func judgeOnOff(ctx context.Context, gw gateway.Gateway, scenarioID, category, question, expected string, onItems []retrieval.RenderItem) (GainResult, error) {
	off, err := JudgeQuestion(ctx, gw, question, expected, nil)
	if err != nil {
		return GainResult{}, fmt.Errorf("judge memory-off: %w", err)
	}
	// category is intentionally "" here, matching JudgeQuestion's zero-opts
	// wrapper above (JudgeQuestion always hardcodes category="" — see judge.go)
	// so this fix changes ONLY the CURRENT/SUPERSEDED partitioning, not the
	// per-category judge leniency behavior.
	on, err := JudgeQuestionWithItems(ctx, gw, ReaderOpts{}, "", question, "", expected, onItems)
	if err != nil {
		return GainResult{}, fmt.Errorf("judge memory-on: %w", err)
	}
	qOff, qOn := quality(off.Verdict), quality(on.Verdict)
	return GainResult{
		ScenarioID: scenarioID, Category: category, Question: question, Expected: expected,
		AnswerOff: off.Answer, AnswerOn: on.Answer,
		VerdictOff: off.Verdict, VerdictOn: on.Verdict,
		QualityOff: qOff, QualityOn: qOn, Gain: qOn - qOff,
	}, nil
}

// RunGainScenario ingests the scenario, settles the pipeline, retrieves for the
// eval question, and measures memory-on vs memory-off gain. Needs a live gateway
// (operator-run via gainmode_test.go).
func RunGainScenario(ctx context.Context, srv *TestServer, runner *Runner, gw gateway.Gateway, sc *gain.Scenario, settle time.Duration) (GainResult, error) {
	fix := scenarioToFixture(*sc)
	if _, err := runner.ingestConversation(ctx, &fix); err != nil {
		return GainResult{}, fmt.Errorf("ingest %s: %w", sc.ID, err)
	}
	if err := runner.flushBuffer(ctx, fix.ID); err != nil {
		return GainResult{}, fmt.Errorf("flush %s: %w", sc.ID, err)
	}
	if err := srv.WaitForQuiescence(ctx, settle); err != nil {
		return GainResult{}, fmt.Errorf("settle %s: %w", sc.ID, err)
	}

	qr, err := runner.scoreQuestion(ctx, QuestionFixture{
		ID: sc.ID, Text: sc.EvalQuestion, Category: sc.Category,
		Expected: struct {
			Answer string `json:"answer"`
		}{Answer: sc.ExpectedAnswer},
	})
	if err != nil {
		return GainResult{}, fmt.Errorf("retrieve %s: %w", sc.ID, err)
	}
	return judgeOnOff(ctx, gw, sc.ID, sc.Category, sc.EvalQuestion, sc.ExpectedAnswer, qr.RenderItems)
}

// RunGainOverDataset measures memory-on-vs-off gain for ONE public-benchmark
// question (D-096): it ingests the conversation the question references, settles
// the pipeline, retrieves the question's context, and judges memory-ON (with that
// context) vs memory-OFF (none). This makes the gain metric runnable over a real
// dataset (longmemeval_s, locomo) rather than only hand-authored scenarios, reusing
// the same judgeOnOff primitive as RunGainScenario. Needs a live gateway
// (operator-run); the pure scoring (judgeOnOff/AggregateGain) is CI-tested.
func RunGainOverDataset(ctx context.Context, srv *TestServer, runner *Runner, gw gateway.Gateway, conv datasets.Conversation, q datasets.Question, settle time.Duration) (GainResult, error) {
	fix := toFixture(conv)
	if _, err := runner.ingestConversation(ctx, &fix); err != nil {
		return GainResult{}, fmt.Errorf("ingest %s: %w", conv.ID, err)
	}
	// Operator-run (full mode) only: the deliberate flush plus the full-mode
	// Count/MaxAge auto-flush triggers extract the buffered records, and the
	// WaitForQuiescence barrier below gates scoring — so this needs no pre-flush
	// WaitForBuffered (which is a mock-mode concern; see RunDataset, D-096).
	if err := runner.flushBuffer(ctx, fix.ID); err != nil {
		return GainResult{}, fmt.Errorf("flush %s: %w", conv.ID, err)
	}
	if err := srv.WaitForQuiescence(ctx, settle); err != nil {
		return GainResult{}, fmt.Errorf("settle %s: %w", conv.ID, err)
	}
	qr, err := runner.scoreQuestion(ctx, toQuestionFixture(q))
	if err != nil {
		return GainResult{}, fmt.Errorf("retrieve %s: %w", q.ID, err)
	}
	return judgeOnOff(ctx, gw, q.ID, q.Category, q.Text, q.Expected.Answer, qr.RenderItems)
}
