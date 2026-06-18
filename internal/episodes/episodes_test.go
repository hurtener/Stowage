package episodes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/store"
)

func rec(id string, occ int64, outcome string) store.Record {
	return store.Record{ID: id, Role: "tool", Content: "did " + id, OccurredAt: occ, Outcome: outcome}
}

func TestDetectEpisodes_OneSessionOneEpisode(t *testing.T) {
	recs := []store.Record{rec("a", 100, ""), rec("b", 200, "success")}
	got := DetectEpisodes(recs, 0) // gap split disabled
	if len(got) != 1 {
		t.Fatalf("expected 1 episode, got %d", len(got))
	}
	if got[0].StartedAt != 100 || got[0].EndedAt != 200 || got[0].Outcome != "success" || len(got[0].RecordIDs) != 2 {
		t.Errorf("episode wrong: %+v", got[0])
	}
}

func TestDetectEpisodes_GapSplit(t *testing.T) {
	// gap between b(200) and c(10000) is 9800 > gapMs(1000) → split into 2.
	recs := []store.Record{rec("a", 100, ""), rec("b", 200, "failure"), rec("c", 10000, "success")}
	got := DetectEpisodes(recs, 1000)
	if len(got) != 2 {
		t.Fatalf("expected 2 episodes after gap split, got %d", len(got))
	}
	if got[0].Outcome != "failure" || len(got[0].RecordIDs) != 2 {
		t.Errorf("episode 0 wrong: %+v", got[0])
	}
	if got[1].Outcome != "success" || got[1].StartedAt != 10000 || len(got[1].RecordIDs) != 1 {
		t.Errorf("episode 1 wrong: %+v", got[1])
	}
}

func TestDetectEpisodes_Empty(t *testing.T) {
	if got := DetectEpisodes(nil, 1000); got != nil {
		t.Errorf("empty records should yield nil, got %+v", got)
	}
}

// fakeGateway returns a scripted narrative + records the request for §10 assertion.
type fakeGateway struct {
	resp json.RawMessage
	reqs []gateway.CompleteRequest
	err  error
}

func (f *fakeGateway) Complete(_ context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return gateway.CompleteResponse{}, f.err
	}
	return gateway.CompleteResponse{JSON: f.resp}, nil
}
func (f *fakeGateway) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (f *fakeGateway) Probe(context.Context) error { return nil }
func (f *fakeGateway) Close(context.Context) error { return nil }
func (f *fakeGateway) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

func TestBuildNarrativePrompt_Golden(t *testing.T) {
	sys, user := BuildNarrativePrompt([]store.Record{{ID: "r1", Role: "tool", Content: "  deployed v2  ", OccurredAt: 1}})
	if !strings.Contains(sys, "narrative memory") {
		t.Errorf("system prompt missing framing: %q", sys)
	}
	want := "Episode records:\n[record r1] role: tool\ndeployed v2\n"
	if user != want {
		t.Errorf("user prompt mismatch:\n got: %q\nwant: %q", user, want)
	}
}

func TestNarrate_SchemaConstrained(t *testing.T) {
	fg := &fakeGateway{resp: json.RawMessage(`{"title":"March deploy","narrative":"Deployed v2 under a lock; succeeded."}`)}
	n, err := Narrate(context.Background(), fg, []store.Record{rec("r1", 1, "success")})
	if err != nil {
		t.Fatalf("Narrate: %v", err)
	}
	if n.Title != "March deploy" || !strings.Contains(n.Narrative, "Deployed v2") {
		t.Errorf("narrative wrong: %+v", n)
	}
	if len(fg.reqs) != 1 || len(fg.reqs[0].Schema) == 0 {
		t.Error("narrate Complete call was not schema-constrained (§10)")
	}
}

func TestNarrate_EmptyRecords(t *testing.T) {
	if _, err := Narrate(context.Background(), &fakeGateway{}, nil); err == nil {
		t.Error("expected error on empty records")
	}
}
