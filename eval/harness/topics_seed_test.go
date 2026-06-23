package harness

import (
	"context"
	"encoding/json"
	"testing"
)

// TestSeedEvalTopics drives the topic-seeding path against the mock harness (no
// paid call): it PUTs the LongMemEval magnet set and confirms GET /v1/topics
// returns exactly those keys as explicit topics (the default pack is suppressed,
// D-099). Proves the full-mode seeding wiring without a live gateway.
func TestSeedEvalTopics(t *testing.T) {
	srv := NewTestServer(t, "eval-seed")
	ctx := context.Background()

	if err := SeedEvalTopics(ctx, srv); err != nil {
		t.Fatalf("SeedEvalTopics: %v", err)
	}

	status, body, err := srv.DoJSON(ctx, "GET", "/v1/topics", nil)
	if err != nil {
		t.Fatalf("GET /v1/topics: %v", err)
	}
	if status != 200 {
		t.Fatalf("GET /v1/topics: got %d: %s", status, body)
	}
	var resp struct {
		Topics []struct {
			Key    string `json:"key"`
			Source string `json:"source"`
		} `json:"topics"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode topics: %v", err)
	}

	got := map[string]string{}
	for _, tp := range resp.Topics {
		got[tp.Key] = tp.Source
	}
	if len(got) != len(LongMemEvalTopics) {
		t.Errorf("want %d seeded topics, got %d", len(LongMemEvalTopics), len(got))
	}
	for _, want := range LongMemEvalTopics {
		src, ok := got[want.Key]
		if !ok {
			t.Errorf("seeded topic %q missing from GET /v1/topics", want.Key)
			continue
		}
		if src != "explicit" {
			t.Errorf("topic %q: source=%q, want explicit", want.Key, src)
		}
	}
}
