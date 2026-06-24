package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hurtener/stowage/internal/gateway"
)

// judged-QA mode (Phase 20, D-076): a reader LLM answers an eval question from
// Stowage's retrieved context, then an LLM judge grades that answer against the
// gold answer semantically. Both calls go through the gateway seam (P5 — no
// provider SDK under eval/) and are JSON-schema-constrained (RFC §10 / D-040 —
// gateway.Complete REQUIRES a schema and returns validated JSON; unmarshaling
// that validated resp.JSON is the standard caller idiom, not free-text parsing).
//
// This file carries NO build tag: the prompt assembly and the reader/judge wiring
// are exercised in CI against the deterministic mock gateway (judge_test.go). Only
// the live, paid run is operator-gated (fullmode_test.go, STOWAGE_EVAL_JUDGE=1).

// Reasoning headroom: thinking models count reasoning tokens against the output
// budget, so a tight cap truncates the JSON and dead-letters the call (the
// 2026-06-12 lesson — REPORT.md item 4). These mirror the extract/reconcile fix.
const (
	readerMaxTokens = 8192
	judgeMaxTokens  = 2048
)

// readerSchema constrains the reader to a single concise answer string.
var readerSchema = json.RawMessage(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "ReaderAnswer",
  "type": "object",
  "required": ["answer"],
  "additionalProperties": false,
  "properties": {
    "answer": {
      "type": "string",
      "description": "A concise answer to the question using ONLY the provided context, or an explicit statement that the context is insufficient."
    }
  }
}`)

// judgeSchema constrains the judge to a verdict enum + a short justification.
var judgeSchema = json.RawMessage(`{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "JudgeVerdict",
  "type": "object",
  "required": ["verdict", "justification"],
  "additionalProperties": false,
  "properties": {
    "verdict": {
      "type": "string",
      "enum": ["correct", "partial", "incorrect"],
      "description": "correct = semantically equivalent to the gold answer; partial = captures some but not all of it; incorrect = wrong, missing, or unsupported."
    },
    "justification": {
      "type": "string",
      "description": "One sentence explaining the verdict."
    }
  }
}`)

// readerOut / judgeOut decode the validated gateway responses.
type readerOut struct {
	Answer string `json:"answer"`
}

type judgeOut struct {
	Verdict       string `json:"verdict"`
	Justification string `json:"justification"`
}

// JudgedResult is the outcome of one reader + judge round for a question.
type JudgedResult struct {
	Answer        string
	Verdict       string // "correct" | "partial" | "incorrect"
	Justification string
}

// BuildReaderPrompt assembles the (system, user) prompt for the reader. Pure and
// deterministic — golden-tested. The context blocks are the retrieved memory
// contents (already scored), joined with stable numbering.
func BuildReaderPrompt(question string, contexts []string) (system, user string) {
	system = "You are answering a question using ONLY the memory context retrieved below. " +
		"Rules: (1) Use ONLY the retrieved context — never rely on outside knowledge, prior " +
		"training, or assumptions; do not answer anything that is not supported by the context. " +
		"(2) You MAY do arithmetic, counting, or temporal reasoning OVER the context when the " +
		"question requires it. (3) If the retrieved context does not contain enough information " +
		"to answer, ABSTAIN: state explicitly that the provided context is insufficient — do not " +
		"guess. Answer concisely and directly."

	var b strings.Builder
	b.WriteString("Context:\n")
	if len(contexts) == 0 {
		b.WriteString("(no memories retrieved)\n")
	}
	for i, c := range contexts {
		fmt.Fprintf(&b, "[%d] %s\n", i+1, strings.TrimSpace(c))
	}
	b.WriteString("\nQuestion: ")
	b.WriteString(strings.TrimSpace(question))
	return system, b.String()
}

// BuildJudgePrompt assembles the (system, user) prompt for the judge. Pure and
// deterministic — golden-tested.
func BuildJudgePrompt(question, gold, answer string) (system, user string) {
	system = "You are grading a candidate answer against a gold answer for a memory-QA " +
		"benchmark. Judge SEMANTIC equivalence, not string overlap: a paraphrase, a different " +
		"number format (\"five\" vs \"5\"), or a correct value reached by reasoning all count as " +
		"correct. If the gold answer is an abstention (e.g. \"the information provided is not " +
		"enough\"), a candidate that also abstains is correct. Return correct, partial, or incorrect."

	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n\nGold answer: ")
	b.WriteString(strings.TrimSpace(gold))
	b.WriteString("\n\nCandidate answer: ")
	b.WriteString(strings.TrimSpace(answer))
	return system, b.String()
}

// ReaderOpts overrides the reader/judge model and reasoning effort for one run
// (D-100). The zero value reproduces the legacy behavior: reader and judge use the
// gateway's configured completion model with no reasoning parameter. The eval
// harness sets Model to a stronger reader model (e.g. anthropic/claude-sonnet-4.6,
// distinct from the cheap extraction model) and ReasoningEffort (e.g. "medium").
type ReaderOpts struct {
	// Model overrides the completion model for the READER call. Empty = the
	// gateway's configured model.
	Model string
	// JudgeModel overrides the completion model for the JUDGE call. Empty = Model
	// (so the judge follows the reader unless explicitly varied — the cost/quality
	// sweep varies reader and judge independently).
	JudgeModel string
	// ReasoningEffort requests provider extended thinking for the READER call
	// ("none"|"minimal"|"low"|"medium"|"high"). Empty = none.
	ReasoningEffort string
	// JudgeReasoningEffort requests reasoning for the JUDGE call. Empty = none (a
	// short classification rarely needs it).
	JudgeReasoningEffort string
}

// judgeModel returns the model the judge call should use (JudgeModel, falling back
// to Model when unset).
func (o ReaderOpts) judgeModel() string {
	if o.JudgeModel != "" {
		return o.JudgeModel
	}
	return o.Model
}

// readerBudget returns the reader's output-token budget. With reasoning enabled the
// model spends tokens thinking before the answer, so the budget is widened to avoid
// truncating the JSON (the 2026-06-12 lesson, generalized to extended thinking).
func readerBudget(opts ReaderOpts) int {
	if opts.ReasoningEffort != "" {
		return 16000
	}
	return readerMaxTokens
}

// JudgeQuestion runs the reader then the judge using the gateway's configured model
// with no reasoning (the CI/mock and gain/adapt callers). It is the zero-opts
// wrapper over JudgeQuestionWith.
func JudgeQuestion(ctx context.Context, gw gateway.Gateway, question, gold string, contexts []string) (JudgedResult, error) {
	return JudgeQuestionWith(ctx, gw, ReaderOpts{}, question, gold, contexts)
}

// JudgeQuestionWith runs the reader then the judge with per-call model / reasoning
// overrides (D-100, D-076). The reader answers ONLY from the retrieved context and
// may abstain; the judge grades that answer against the gold answer semantically.
// Both calls go through the gateway seam (P5) and are JSON-schema-constrained.
func JudgeQuestionWith(ctx context.Context, gw gateway.Gateway, opts ReaderOpts, question, gold string, contexts []string) (JudgedResult, error) {
	rSys, rUser := BuildReaderPrompt(question, contexts)
	rResp, err := gw.Complete(ctx, gateway.CompleteRequest{
		System:          rSys,
		Messages:        []gateway.Message{{Role: "user", Content: rUser}},
		Schema:          readerSchema,
		MaxTokens:       readerBudget(opts),
		Temperature:     0.0,
		Model:           opts.Model,
		ReasoningEffort: opts.ReasoningEffort,
	})
	if err != nil {
		return JudgedResult{}, fmt.Errorf("reader complete: %w", err)
	}
	var ro readerOut
	if err := json.Unmarshal(rResp.JSON, &ro); err != nil {
		return JudgedResult{}, fmt.Errorf("reader decode: %w", err)
	}

	jSys, jUser := BuildJudgePrompt(question, gold, ro.Answer)
	jResp, err := gw.Complete(ctx, gateway.CompleteRequest{
		System:          jSys,
		Messages:        []gateway.Message{{Role: "user", Content: jUser}},
		Schema:          judgeSchema,
		MaxTokens:       judgeMaxTokens,
		Temperature:     0.0,
		Model:           opts.judgeModel(),
		ReasoningEffort: opts.JudgeReasoningEffort,
	})
	if err != nil {
		return JudgedResult{}, fmt.Errorf("judge complete: %w", err)
	}
	var jo judgeOut
	if err := json.Unmarshal(jResp.JSON, &jo); err != nil {
		return JudgedResult{}, fmt.Errorf("judge decode: %w", err)
	}

	return JudgedResult{
		Answer:        ro.Answer,
		Verdict:       normalizeVerdict(jo.Verdict),
		Justification: jo.Justification,
	}, nil
}

// normalizeVerdict canonicalizes the judge verdict; anything unrecognized is
// treated as incorrect (conservative).
func normalizeVerdict(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "correct":
		return "correct"
	case "partial":
		return "partial"
	default:
		return "incorrect"
	}
}
