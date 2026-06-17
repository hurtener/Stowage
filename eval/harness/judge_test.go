package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/gateway"
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
	sys, user := BuildReaderPrompt("How many mugs did the user buy?",
		[]string{"User spent $60 on coffee mugs.", "  The mugs cost $12 each.  "})
	if !strings.Contains(sys, "ONLY the provided memory context") {
		t.Errorf("reader system prompt missing context-only instruction: %q", sys)
	}
	wantUser := "Context:\n[1] User spent $60 on coffee mugs.\n[2] The mugs cost $12 each.\n\nQuestion: How many mugs did the user buy?"
	if user != wantUser {
		t.Errorf("reader user prompt mismatch:\n got: %q\nwant: %q", user, wantUser)
	}
}

// TestReaderPrompt_NoContext renders an explicit no-memories block.
func TestReaderPrompt_NoContext(t *testing.T) {
	_, user := BuildReaderPrompt("Q?", nil)
	if !strings.Contains(user, "(no memories retrieved)") {
		t.Errorf("empty-context reader prompt should note no memories: %q", user)
	}
}

// TestJudgePrompt_Golden pins the judge prompt assembly (deterministic).
func TestJudgePrompt_Golden(t *testing.T) {
	sys, user := BuildJudgePrompt("How long did it take?", "over a year", "more than a year")
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
