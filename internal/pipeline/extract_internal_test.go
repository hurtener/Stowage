package pipeline

import "testing"

func TestFilterToActiveTopics(t *testing.T) {
	active := map[string]struct{}{"auth": {}, "deploy": {}}
	got := filterToActiveTopics([]string{"auth", "billing", "deploy", "auth"}, active)
	if len(got) != 2 || got[0] != "auth" || got[1] != "deploy" {
		t.Fatalf("hallucinated/dup keys not filtered: got %v want [auth deploy]", got)
	}
	if filterToActiveTopics(nil, active) != nil {
		t.Error("nil input should return nil")
	}
	if filterToActiveTopics([]string{"nope"}, active) != nil {
		t.Error("no matches should return nil")
	}
}
