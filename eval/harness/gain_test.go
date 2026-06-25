package harness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hurtener/stowage/eval/gain"
	"github.com/hurtener/stowage/internal/gateway"
)

// seqGateway returns queued JSON responses in order, recording each request so the
// test can assert schema-constrained calls. Deterministic; no live model.
type seqGateway struct {
	responses []json.RawMessage
	idx       int
	reqs      []gateway.CompleteRequest
}

func (g *seqGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	g.reqs = append(g.reqs, req)
	out := json.RawMessage(`{}`)
	if g.idx < len(g.responses) {
		out = g.responses[g.idx]
		g.idx++
	}
	return gateway.CompleteResponse{JSON: out}, nil
}
func (g *seqGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (g *seqGateway) Probe(context.Context) error { return nil }
func (g *seqGateway) Close(context.Context) error { return nil }
func (g *seqGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

func TestGainQuality(t *testing.T) {
	cases := []struct {
		verdict string
		want    float64
	}{
		{"correct", 1.0}, {"partial", 0.5}, {"incorrect", 0.0}, {"garbled", 0.0}, {"", 0.0},
	}
	for _, tc := range cases {
		if got := quality(tc.verdict); got != tc.want {
			t.Errorf("quality(%q) = %v, want %v", tc.verdict, got, tc.want)
		}
	}
}

func TestAggregateGain(t *testing.T) {
	res := []GainResult{
		{Gain: 1.0, QualityOn: 1.0, QualityOff: 0.0},
		{Gain: 0.0, QualityOn: 0.5, QualityOff: 0.5},
		{Gain: -0.5, QualityOn: 0.0, QualityOff: 0.5},
	}
	s := AggregateGain(res)
	if s.Total != 3 || s.NonNegative != 2 {
		t.Errorf("Total/NonNegative = %d/%d, want 3/2", s.Total, s.NonNegative)
	}
	if g := s.MeanGain; g < 0.166 || g > 0.167 { // (1+0-0.5)/3 = 0.1666…
		t.Errorf("MeanGain = %v, want ~0.1667", g)
	}
	if AggregateGain(nil).Total != 0 {
		t.Error("empty AggregateGain should be zero-valued")
	}
}

func TestScenarioToFixture(t *testing.T) {
	sc := gain.Scenario{ID: "gain-x", Category: "preference", Turns: []gain.Turn{
		{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"},
	}}
	fix := scenarioToFixture(sc)
	if fix.ID != "gain-x" || fix.Category != "preference" || len(fix.Sessions) != 1 || len(fix.Sessions[0].Turns) != 2 {
		t.Errorf("unexpected fixture: %+v", fix)
	}
	if fix.Sessions[0].Turns[0].Content != "a" {
		t.Errorf("turn content not preserved: %+v", fix.Sessions[0].Turns)
	}
}

// TestJudgeOnOff_PositiveGain proves the on-vs-off delta: memory-off reader is wrong
// (judge incorrect → 0), memory-on reader is right (judge correct → 1) ⇒ gain = 1.
// The sequence is reader-off, judge-off, reader-on, judge-on. Also asserts every
// Complete call is schema-constrained (§10).
func TestJudgeOnOff_PositiveGain(t *testing.T) {
	gw := &seqGateway{responses: []json.RawMessage{
		json.RawMessage(`{"answer":"I don't know"}`),                                // reader OFF
		json.RawMessage(`{"verdict":"incorrect","justification":"no answer"}`),      // judge OFF
		json.RawMessage(`{"answer":"TypeScript"}`),                                  // reader ON
		json.RawMessage(`{"verdict":"correct","justification":"matches the gold"}`), // judge ON
	}}
	gr, err := judgeOnOff(context.Background(), gw, "gain-pref-01", "preference",
		"What language does the user prefer?", "TypeScript", []string{"User prefers TypeScript."})
	if err != nil {
		t.Fatalf("judgeOnOff: %v", err)
	}
	if gr.QualityOff != 0.0 || gr.QualityOn != 1.0 || gr.Gain != 1.0 {
		t.Errorf("expected gain 1.0 (on=1,off=0), got on=%v off=%v gain=%v", gr.QualityOn, gr.QualityOff, gr.Gain)
	}
	if len(gw.reqs) != 4 {
		t.Fatalf("expected 4 Complete calls, got %d", len(gw.reqs))
	}
	for i, req := range gw.reqs {
		if len(req.Schema) == 0 {
			t.Errorf("Complete call %d not schema-constrained (§10)", i)
		}
	}
}

// TestRunGainScenario_Wiring exercises the full ingest→settle→retrieve→judge wiring
// deterministically: a real sqlite server (mock gateway, no extraction script → 0
// memories, fast quiescence) drives ingest/retrieve, and a seqGateway answers the
// reader+judge. Proves RunGainScenario completes and returns a populated GainResult
// without a live model (parity with the adapt loop-wiring test).
func TestRunGainScenario_Wiring(t *testing.T) {
	srv := NewTestServer(t, "gain-wire")
	runner := NewRunner(srv, RunConfig{})
	gw := &seqGateway{responses: []json.RawMessage{
		json.RawMessage(`{"answer":"I don't know"}`),
		json.RawMessage(`{"verdict":"incorrect","justification":"no context"}`),
		json.RawMessage(`{"answer":"React"}`),
		json.RawMessage(`{"verdict":"correct","justification":"ok"}`),
	}}
	sc := &gain.Scenario{
		ID: "gain-wire-01", Category: "multi_session",
		Turns:          []gain.Turn{{Role: "user", Content: "I use React for the dashboard."}},
		EvalQuestion:   "what frontend?",
		ExpectedAnswer: "React",
	}
	// 90s settle (not 30s): under CI's -race + coverage instrumentation the mock pipeline
	// runs ~2-3x slower and a 30s quiescence barrier flakes with a single record still in
	// flight. Generous headroom keeps the wiring assertion while still failing fast on a hang.
	gr, err := RunGainScenario(context.Background(), srv, runner, gw, sc, 90*time.Second)
	if err != nil {
		t.Fatalf("RunGainScenario: %v", err)
	}
	if gr.ScenarioID != "gain-wire-01" || gr.VerdictOff == "" || gr.VerdictOn == "" {
		t.Errorf("GainResult not populated: %+v", gr)
	}
	if gr.Gain != gr.QualityOn-gr.QualityOff {
		t.Errorf("gain inconsistent: %v != %v-%v", gr.Gain, gr.QualityOn, gr.QualityOff)
	}
}

// TestJudgeOnOff_NoGain: memory makes no difference (both correct) ⇒ gain 0.
func TestJudgeOnOff_NoGain(t *testing.T) {
	gw := &seqGateway{responses: []json.RawMessage{
		json.RawMessage(`{"answer":"TypeScript"}`),
		json.RawMessage(`{"verdict":"correct","justification":"ok"}`),
		json.RawMessage(`{"answer":"TypeScript"}`),
		json.RawMessage(`{"verdict":"correct","justification":"ok"}`),
	}}
	gr, err := judgeOnOff(context.Background(), gw, "x", "c", "q?", "TypeScript", []string{"ctx"})
	if err != nil {
		t.Fatalf("judgeOnOff: %v", err)
	}
	if gr.Gain != 0.0 {
		t.Errorf("expected gain 0 when both correct, got %v", gr.Gain)
	}
}
