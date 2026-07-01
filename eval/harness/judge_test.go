package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/retrieval"
)

// fakeGateway is a deterministic in-test gateway.Gateway: Complete pops the next
// queued JSON response and records each request so tests can assert the call was
// schema-constrained (RFC §10). No network, no model — CI-safe.
type fakeGateway struct {
	responses []json.RawMessage
	idx       int
	requests  []gateway.CompleteRequest
}

func (f *fakeGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	f.requests = append(f.requests, req)
	var out json.RawMessage
	if f.idx < len(f.responses) {
		out = f.responses[f.idx]
		f.idx++
	} else {
		out = json.RawMessage(`{}`)
	}
	return gateway.CompleteResponse{JSON: out}, nil
}

func (f *fakeGateway) Embed(_ context.Context, _ gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (f *fakeGateway) Probe(_ context.Context) error { return nil }
func (f *fakeGateway) Close(_ context.Context) error { return nil }
func (f *fakeGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

// TestReaderPrompt_Golden pins the reader prompt assembly (deterministic).
func TestReaderPrompt_Golden(t *testing.T) {
	sys, user := BuildReaderPrompt("How many mugs did the user buy?", "",
		[]retrieval.RenderItem{{Content: "User spent $60 on coffee mugs."}, {Content: "  The mugs cost $12 each.  "}})
	if !strings.Contains(sys, "ONLY the retrieved context") {
		t.Errorf("reader system prompt missing context-only instruction: %q", sys)
	}
	if !strings.Contains(sys, "ABSTAIN") {
		t.Errorf("reader system prompt missing abstention instruction: %q", sys)
	}
	wantUser := "CURRENT memories (answer from these):\n[1] User spent $60 on coffee mugs.\n[2] The mugs cost $12 each.\n\nQuestion: How many mugs did the user buy?"
	if user != wantUser {
		t.Errorf("reader user prompt mismatch:\n got: %q\nwant: %q", user, wantUser)
	}
}

// TestReaderPrompt_QuestionDateAndShapeRules pins the AMB-parity reader upgrades: the
// Question Date is rendered when supplied (the temporal anchor) and the per-question-shape
// guidance (counting/preference/comparative/date-diff) is present in the system prompt.
func TestReaderPrompt_QuestionDateAndShapeRules(t *testing.T) {
	sys, user := BuildReaderPrompt("How many days since my last museum visit?", "2023-06-01",
		[]retrieval.RenderItem{{Content: "Visited the Science Museum. | When: 2023-05-15"}})
	if !strings.Contains(user, "Question Date: 2023-06-01") {
		t.Errorf("reader user prompt missing Question Date anchor: %q", user)
	}
	for _, want := range []string{"Counting", "Recommendation", "Comparative", "Date-difference"} {
		if !strings.Contains(sys, want) {
			t.Errorf("reader system prompt missing %q shape rule", want)
		}
	}
	// Empty date → no Question Date line (back-compat for callers without a date).
	if _, u2 := BuildReaderPrompt("Q?", "", nil); strings.Contains(u2, "Question Date:") {
		t.Errorf("empty date must not render a Question Date line: %q", u2)
	}
}

// TestJudgePrompt_PerCategoryLeniency pins the per-category rubric (LongMemEval-standard):
// temporal off-by-one, knowledge-update updated-wins, preference recall-is-enough; and that
// an empty/other category yields only the generic rubric.
func TestJudgePrompt_PerCategoryLeniency(t *testing.T) {
	cases := map[string]string{
		"temporal-reasoning":        "off-by-one",
		"knowledge-update":          "updated/current value",
		"single-session-preference": "need not state every point",
	}
	for cat, want := range cases {
		if sys, _ := BuildJudgePrompt(cat, "q", "g", "a"); !strings.Contains(sys, want) {
			t.Errorf("judge prompt for %q missing leniency %q: %q", cat, want, sys)
		}
	}
	if sys, _ := BuildJudgePrompt("multi-session", "q", "g", "a"); strings.Contains(sys, "off-by-one") {
		t.Errorf("non-listed category must not get temporal leniency: %q", sys)
	}
}

// TestReaderPrompt_NoContext renders an explicit no-memories block.
func TestReaderPrompt_NoContext(t *testing.T) {
	_, user := BuildReaderPrompt("Q?", "", nil)
	if !strings.Contains(user, "(no current memories retrieved)") {
		t.Errorf("empty-context reader prompt should note no memories: %q", user)
	}
}

// TestReaderPrompt_SupersededSection puts [OUTDATED]-marked items in a separate
// SUPERSEDED section (history only), not inline among current memories (D-105).
func TestReaderPrompt_SupersededSection(t *testing.T) {
	_, user := BuildReaderPrompt("How long is the commute?", "",
		[]retrieval.RenderItem{
			{Content: "Commute is 45 minutes each way."},
			{Content: "Commute is 30 minutes.", Stale: true},
		})
	if !strings.Contains(user, "CURRENT memories") || !strings.Contains(user, "SUPERSEDED memories") {
		t.Errorf("prompt missing current/superseded sections: %q", user)
	}
	// The current value is in the CURRENT block; the stale value is below the SUPERSEDED header.
	cur := user[:strings.Index(user, "SUPERSEDED memories")]
	if !strings.Contains(cur, "45 minutes each way") {
		t.Errorf("current value not in CURRENT section: %q", cur)
	}
	if strings.Contains(cur, "30 minutes") {
		t.Errorf("superseded value leaked into CURRENT section: %q", cur)
	}
	if strings.Contains(user, "[OUTDATED") {
		t.Errorf("inline [OUTDATED marker should be stripped once sectioned: %q", user)
	}
}

// TestJudgePrompt_Golden pins the judge prompt assembly (deterministic).
func TestJudgePrompt_Golden(t *testing.T) {
	sys, user := BuildJudgePrompt("", "How long did it take?", "over a year", "more than a year")
	if !strings.Contains(sys, "SEMANTIC equivalence") {
		t.Errorf("judge system prompt missing semantic-equivalence instruction: %q", sys)
	}
	wantUser := "Question: How long did it take?\n\nGold answer: over a year\n\nCandidate answer: more than a year"
	if user != wantUser {
		t.Errorf("judge user prompt mismatch:\n got: %q\nwant: %q", user, wantUser)
	}
}

// TestJudgeQuestion_SchemaConstrained drives the reader+judge through the fake
// gateway and asserts: (a) the result is decoded from the validated JSON, and
// (b) BOTH Complete calls carried a non-nil Schema (the §10 invariant the smoke
// also checks structurally).
func TestJudgeQuestion_SchemaConstrained(t *testing.T) {
	fg := &fakeGateway{responses: []json.RawMessage{
		json.RawMessage(`{"answer":"12 dollars"}`),
		json.RawMessage(`{"verdict":"correct","justification":"$12 matches the gold."}`),
	}}
	res, err := JudgeQuestion(context.Background(), fg, "How much per mug?", "$12",
		[]string{"User spent $60 on 5 coffee mugs."})
	if err != nil {
		t.Fatalf("JudgeQuestion: %v", err)
	}
	if res.Answer != "12 dollars" || res.Verdict != "correct" {
		t.Errorf("unexpected result: %+v", res)
	}
	if len(fg.requests) != 2 {
		t.Fatalf("expected 2 Complete calls, got %d", len(fg.requests))
	}
	for i, req := range fg.requests {
		if len(req.Schema) == 0 {
			t.Errorf("Complete call %d was not schema-constrained (empty Schema) — violates §10", i)
		}
	}
}

// TestJudgeQuestionWithItems_PartitionsStale proves the wave-0 fix: the judged
// path threads TYPED render items through to BuildReaderPrompt, so a stale
// companion item lands in a separate SUPERSEDED section — not inline in
// CURRENT with a leaked "[OUTDATED" marker, and not silently treated as
// current (which JudgeQuestionWith's []string wrap would do — the pre-fix
// regression the two adversarial reviews flagged). We assert on the actual
// reader prompt the fake gateway received, not just on BuildReaderPrompt in
// isolation, so this pins the full call path (dataset.go/gain.go's entry
// point) rather than only the render core ae3 already covers.
func TestJudgeQuestionWithItems_PartitionsStale(t *testing.T) {
	fg := &fakeGateway{responses: []json.RawMessage{
		json.RawMessage(`{"answer":"45 minutes"}`),
		json.RawMessage(`{"verdict":"correct","justification":"matches current value"}`),
	}}
	items := []retrieval.RenderItem{
		{Content: "Commute is 45 minutes each way."},
		{Content: "Commute is 30 minutes.", Stale: true, SupersededByContent: "Commute is 45 minutes each way.", SupersededByDate: 1684108800000},
	}
	res, err := JudgeQuestionWithItems(context.Background(), fg, ReaderOpts{}, "", "How long is the commute?", "", "45 minutes", items)
	if err != nil {
		t.Fatalf("JudgeQuestionWithItems: %v", err)
	}
	if res.Answer != "45 minutes" || res.Verdict != "correct" {
		t.Errorf("unexpected result: %+v", res)
	}
	if len(fg.requests) != 2 {
		t.Fatalf("expected 2 Complete calls, got %d", len(fg.requests))
	}
	readerUser := fg.requests[0].Messages[0].Content
	if !strings.Contains(readerUser, "SUPERSEDED memories") {
		t.Errorf("reader prompt missing a SUPERSEDED section for the stale item: %q", readerUser)
	}
	cur := readerUser[:strings.Index(readerUser, "SUPERSEDED memories")]
	if !strings.Contains(cur, "45 minutes each way") {
		t.Errorf("current value not in CURRENT section: %q", cur)
	}
	if strings.Contains(cur, "30 minutes.") {
		t.Errorf("stale value leaked into CURRENT section: %q", cur)
	}
	if strings.Contains(readerUser, "[OUTDATED") {
		t.Errorf("raw [OUTDATED marker leaked into the reader prompt instead of a sectioned split: %q", readerUser)
	}
}

// TestJudgeQuestionWith_TreatsEveryContextAsCurrent documents the []string
// wrapper's known scope (renderItemsFromContexts, judge.go): every context is
// treated as current, since a plain string carries no Stale bit. This is
// correct ONLY for genuinely-all-current callers (adapt.go's playbook
// context, the gain memory-OFF condition); the judged/gain-ON paths were
// moved OFF this wrapper onto JudgeQuestionWithItems by the wave-0 fix so a
// stale companion is never silently folded into CURRENT.
func TestJudgeQuestionWith_TreatsEveryContextAsCurrent(t *testing.T) {
	fg := &fakeGateway{responses: []json.RawMessage{
		json.RawMessage(`{"answer":"30 minutes"}`),
		json.RawMessage(`{"verdict":"incorrect","justification":"stale value"}`),
	}}
	_, err := JudgeQuestionWith(context.Background(), fg, ReaderOpts{}, "", "How long is the commute?", "", "45 minutes",
		[]string{"Commute is 45 minutes each way.", "Commute is 30 minutes."})
	if err != nil {
		t.Fatalf("JudgeQuestionWith: %v", err)
	}
	readerUser := fg.requests[0].Messages[0].Content
	if strings.Contains(readerUser, "SUPERSEDED memories") {
		t.Errorf("the []string wrapper has no Stale bit to partition on — it must not emit a SUPERSEDED section: %q", readerUser)
	}
}

// TestJudgeQuestion_VerdictNormalized maps unknown/garbled verdicts to incorrect.
func TestJudgeQuestion_VerdictNormalized(t *testing.T) {
	fg := &fakeGateway{responses: []json.RawMessage{
		json.RawMessage(`{"answer":"unsure"}`),
		json.RawMessage(`{"verdict":"DEFINITELY NOT","justification":"nope"}`),
	}}
	res, err := JudgeQuestion(context.Background(), fg, "Q?", "gold", []string{"ctx"})
	if err != nil {
		t.Fatalf("JudgeQuestion: %v", err)
	}
	if res.Verdict != "incorrect" {
		t.Errorf("unrecognized verdict should normalize to incorrect, got %q", res.Verdict)
	}
}

// TestJudgedQuality covers the (correct + ½·partial)/N aggregate.
func TestJudgedQuality(t *testing.T) {
	cases := []struct {
		name     string
		verdicts []string
		want     float64
		wantN    int
	}{
		{"empty", nil, 0, 0},
		{"all correct", []string{"correct", "correct"}, 1.0, 2},
		{"all incorrect", []string{"incorrect", "incorrect"}, 0.0, 2},
		{"mixed with partial", []string{"correct", "partial", "incorrect", "partial"}, 0.5, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, n := JudgedQuality(tc.verdicts)
			if q != tc.want || n != tc.wantN {
				t.Errorf("JudgedQuality(%v) = (%.3f, %d), want (%.3f, %d)", tc.verdicts, q, n, tc.want, tc.wantN)
			}
		})
	}
}
