package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hurtener/stowage/eval/gain"
	"github.com/hurtener/stowage/internal/gateway"
)

// adaptFakeGateway routes Complete by role (reflection / reader / judge) off the
// system prompt, so RunAdaptScenario can be exercised deterministically in CI: the
// reflection call returns a strategy referencing a real record; the reader returns a
// fixed answer; the judge returns correct.
type adaptFakeGateway struct{ readerCalls, judgeCalls, reflectCalls int }

var adaptRecRe = regexp.MustCompile(`\[record (\S+)\]`)

func (g *adaptFakeGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	user := ""
	if len(req.Messages) > 0 {
		user = req.Messages[0].Content
	}
	switch {
	case strings.Contains(req.System, "Reflector"):
		g.reflectCalls++
		rid := ""
		if m := adaptRecRe.FindStringSubmatch(user); len(m) == 2 {
			rid = m[1]
		}
		resp := fmt.Sprintf(`{"reflections":[{"kind":"strategy","content":"Acquire the migration lock before running the migration to avoid deadlocks.",`+
			`"context":"","entities":["migration"],"keywords":["lock","deadlock"],"anticipated_queries":["avoid deadlock"],`+
			`"importance":5,"confidence":0.9,"provenance":[{"record_id":%q,"span_start":0,"span_end":1}]}]}`, rid)
		return gateway.CompleteResponse{JSON: json.RawMessage(resp)}, nil
	case strings.Contains(req.System, "ONLY the provided memory context"):
		g.readerCalls++
		return gateway.CompleteResponse{JSON: json.RawMessage(`{"answer":"acquire a lock before running the migration"}`)}, nil
	case strings.Contains(req.System, "grading a candidate answer"):
		g.judgeCalls++
		return gateway.CompleteResponse{JSON: json.RawMessage(`{"verdict":"correct","justification":"matches"}`)}, nil
	default:
		return gateway.CompleteResponse{JSON: json.RawMessage(`{}`)}, nil
	}
}
func (g *adaptFakeGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (g *adaptFakeGateway) Probe(context.Context) error { return nil }
func (g *adaptFakeGateway) Close(context.Context) error { return nil }
func (g *adaptFakeGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

// TestRunAdaptScenario_LoopWiring proves the online-adaptation loop: each task
// reflects → commits → the playbook grows → the reader answers with it → judged.
// Deterministic (fake gateway + real sqlite store via NewTestServer).
func TestRunAdaptScenario_LoopWiring(t *testing.T) {
	srv := NewTestServer(t, "adapt-ci")
	gw := &adaptFakeGateway{}
	sc := &gain.AdaptScenario{
		ID: "adapt-ci-01",
		Tasks: []gain.AdaptTask{
			{Turns: []gain.Turn{{Role: "tool", Content: "migration deadlocked"}}, Outcome: "failure",
				EvalQuestion: "how to avoid deadlock?", ExpectedAnswer: "acquire a lock before running the migration"},
			{Turns: []gain.Turn{{Role: "tool", Content: "migration under lock succeeded"}}, Outcome: "success",
				EvalQuestion: "how to avoid deadlock?", ExpectedAnswer: "acquire a lock before running the migration"},
		},
	}
	res, err := RunAdaptScenario(context.Background(), srv, gw, sc, 2000)
	if err != nil {
		t.Fatalf("RunAdaptScenario: %v", err)
	}
	if len(res.Tasks) != 2 {
		t.Fatalf("expected 2 task results, got %d", len(res.Tasks))
	}
	// The playbook must have grown by task 2 (task 1's reflection committed a strategy).
	if res.Tasks[1].PlaybookItems < 1 {
		t.Errorf("playbook did not grow from reflection: task2 items=%d", res.Tasks[1].PlaybookItems)
	}
	if res.Tasks[1].PlaybookItems < res.Tasks[0].PlaybookItems {
		t.Errorf("playbook shrank across tasks: %d → %d", res.Tasks[0].PlaybookItems, res.Tasks[1].PlaybookItems)
	}
	// Each task judged (verdict present); reflection ran at least once.
	for _, tr := range res.Tasks {
		if tr.Verdict == "" {
			t.Errorf("task %d not judged", tr.TaskIndex)
		}
	}
	if gw.reflectCalls == 0 || gw.judgeCalls != 2 {
		t.Errorf("expected reflect>0 and 2 judge calls, got reflect=%d judge=%d", gw.reflectCalls, gw.judgeCalls)
	}
	if res.Delta < 0 {
		t.Errorf("unexpected negative delta in wiring test: %v", res.Delta)
	}
}
