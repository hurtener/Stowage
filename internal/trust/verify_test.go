package trust

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

func cited(n int) []CitedMemory {
	out := make([]CitedMemory, n)
	for i := range out {
		out[i] = CitedMemory{ID: string(rune('a' + i)), Content: "memory content"}
	}
	return out
}

func TestVerify_Entailed(t *testing.T) {
	gw := &fakeGateway{json: `{"verdict":"entailed","confidence":0.9,"explanation":"supported"}`}
	v, err := Verify(context.Background(), gw, "the sky is blue", cited(2))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Verdict != VerdictEntailed || v.Confidence != 0.9 || v.Degraded {
		t.Fatalf("unexpected verdict: %+v", v)
	}
	if len(gw.last.Schema) == 0 {
		t.Error("Complete called without a schema (P5/D-040)")
	}
	if !strings.Contains(gw.last.Messages[0].Content, "the sky is blue") {
		t.Error("prompt missing the claim")
	}
}

func TestVerify_EmptyCitations(t *testing.T) {
	gw := &fakeGateway{json: `{"verdict":"entailed","confidence":1,"explanation":"x"}`}
	v, err := Verify(context.Background(), gw, "claim", nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Verdict != VerdictUnclear {
		t.Errorf("empty citations ⇒ unclear, got %q", v.Verdict)
	}
	if len(gw.last.Schema) != 0 {
		t.Error("gateway must not be called with no citations")
	}
}

func TestVerify_GatewayDownDegrades(t *testing.T) {
	gw := &fakeGateway{err: errors.New("unavailable")}
	v, err := Verify(context.Background(), gw, "claim", cited(1))
	if err != nil {
		t.Fatalf("gateway-down must not error: %v", err)
	}
	if v.Verdict != VerdictUnclear || !v.Degraded {
		t.Errorf("gateway down ⇒ unclear+degraded, got %+v", v)
	}
}

func TestVerify_NilGatewayDegrades(t *testing.T) {
	v, err := Verify(context.Background(), nil, "claim", cited(1))
	if err != nil || v.Verdict != VerdictUnclear || !v.Degraded {
		t.Errorf("nil gateway ⇒ unclear+degraded, got %+v err=%v", v, err)
	}
}

func TestVerify_UnknownVerdictCoercedToUnclear(t *testing.T) {
	gw := &fakeGateway{json: `{"verdict":"maybe","confidence":0.5,"explanation":"x"}`}
	v, _ := Verify(context.Background(), gw, "claim", cited(1))
	if v.Verdict != VerdictUnclear {
		t.Errorf("unknown verdict should coerce to unclear, got %q", v.Verdict)
	}
}
