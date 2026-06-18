package causal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/gateway"
)

// fakeGateway returns a scripted Complete JSON (or error). Only Complete is used.
type fakeGateway struct {
	json string
	err  error
	last gateway.CompleteRequest
}

func (f *fakeGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (f *fakeGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	f.last = req
	if f.err != nil {
		return gateway.CompleteResponse{}, f.err
	}
	return gateway.CompleteResponse{JSON: json.RawMessage(f.json)}, nil
}
func (f *fakeGateway) Probe(context.Context) error { return nil }
func (f *fakeGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}
func (f *fakeGateway) Close(context.Context) error { return nil }

func cands(n int) []Candidate {
	out := make([]Candidate, n)
	for i := 0; i < n; i++ {
		out[i] = Candidate{ID: string(rune('A' + i)), Kind: "decision", Content: "d"}
	}
	return out
}

func TestInfer_ScriptedProposals(t *testing.T) {
	gw := &fakeGateway{json: `{"links":[{"from_idx":0,"to_idx":1,"confidence":0.9,"reason":"x"}]}`}
	props, err := Infer(context.Background(), gw, "narrative text", cands(3))
	if err != nil {
		t.Fatalf("Infer: %v", err)
	}
	if len(props) != 1 || props[0].FromIdx != 0 || props[0].ToIdx != 1 || props[0].Confidence != 0.9 {
		t.Fatalf("unexpected proposals: %+v", props)
	}
	// Schema is required (P5/D-040): the request must carry it.
	if len(gw.last.Schema) == 0 {
		t.Error("Complete called without a schema")
	}
	if !strings.Contains(gw.last.Messages[0].Content, "narrative text") {
		t.Error("prompt missing narrative")
	}
}

func TestInfer_FewerThanTwoCandidates(t *testing.T) {
	gw := &fakeGateway{json: `{"links":[]}`}
	props, err := Infer(context.Background(), gw, "n", cands(1))
	if err != nil || props != nil {
		t.Errorf("want no call / nil props, got props=%+v err=%v", props, err)
	}
	if len(gw.last.Schema) != 0 {
		t.Error("gateway should not be called with <2 candidates")
	}
}

func TestInfer_GatewayError(t *testing.T) {
	gw := &fakeGateway{err: errors.New("boom")}
	if _, err := Infer(context.Background(), gw, "n", cands(2)); err == nil {
		t.Error("expected gateway error to propagate")
	}
}

func TestInfer_NilGateway(t *testing.T) {
	if _, err := Infer(context.Background(), nil, "n", cands(2)); err == nil {
		t.Error("expected error on nil gateway")
	}
}

func TestGateProposals(t *testing.T) {
	props := []ProposedLink{
		{FromIdx: 0, ToIdx: 1, Confidence: 0.9},   // keep
		{FromIdx: 1, ToIdx: 1, Confidence: 0.95},  // drop: self
		{FromIdx: 0, ToIdx: 5, Confidence: 0.95},  // drop: out of range (n=3)
		{FromIdx: 0, ToIdx: 1, Confidence: 0.8},   // drop: dup pair
		{FromIdx: 2, ToIdx: 0, Confidence: 0.4},   // drop: below threshold
		{FromIdx: 2, ToIdx: 1, Confidence: 0.7},   // keep
		{FromIdx: -1, ToIdx: 0, Confidence: 0.99}, // drop: negative
	}
	got := GateProposals(props, 3, 0.6)
	if len(got) != 2 {
		t.Fatalf("want 2 survivors, got %d: %+v", len(got), got)
	}
	if got[0].FromIdx != 0 || got[0].ToIdx != 1 || got[1].FromIdx != 2 || got[1].ToIdx != 1 {
		t.Errorf("unexpected survivors: %+v", got)
	}
}
