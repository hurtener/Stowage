package topics

import "testing"

// TestPackRegistry_Complete asserts every registered pack has a non-empty entry
// set with unique, non-empty keys (D-099) — packs are the extraction magnets, so a
// malformed pack would silently weaken the gate.
func TestPackRegistry_Complete(t *testing.T) {
	if len(packRegistry) < 8 {
		t.Fatalf("want at least 8 registered packs, got %d", len(packRegistry))
	}
	for name, entries := range packRegistry {
		if len(entries) == 0 {
			t.Errorf("pack %q has no entries", name)
		}
		seen := map[string]bool{}
		for _, e := range entries {
			if e.Key == "" {
				t.Errorf("pack %q has an entry with an empty key", name)
			}
			if e.Description == "" {
				t.Errorf("pack %q entry %q has an empty description", name, e.Key)
			}
			if seen[e.Key] {
				t.Errorf("pack %q has a duplicate key %q", name, e.Key)
			}
			seen[e.Key] = true
		}
	}
}

func TestPackNameFromOnSentinel(t *testing.T) {
	cases := []struct {
		key      string
		wantName string
		wantOK   bool
	}{
		{"pack:on:project", PackProject, true},
		{"pack:on:incidents", PackIncidents, true},
		{"pack:on:research", PackResearch, true},
		{"pack:on:preferences", PackPreferences, true},
		{"pack:on:does-not-exist", "", false}, // unknown pack
		{"pack:on:", "", false},               // empty name
		{"pack:off", "", false},               // not a pack:on sentinel
		{"my-topic", "", false},               // ordinary explicit topic
		{"pack:project", "", false},           // pack name, not the on-sentinel
	}
	for _, c := range cases {
		name, ok := packNameFromOnSentinel(c.key)
		if ok != c.wantOK || name != c.wantName {
			t.Errorf("packNameFromOnSentinel(%q) = (%q,%v), want (%q,%v)",
				c.key, name, ok, c.wantName, c.wantOK)
		}
	}
}

func TestDefaultPacksForProfile(t *testing.T) {
	cases := map[string][]string{
		"assistant":    {PackPreferences},
		"coding-agent": {PackAgentLearnings},
		"fleet":        {PackAgentLearnings},
		"unknown":      {PackPreferences}, // fallback
	}
	for profile, want := range cases {
		got := defaultPacksForProfile(profile)
		if len(got) != len(want) || got[0] != want[0] {
			t.Errorf("defaultPacksForProfile(%q) = %v, want %v", profile, got, want)
		}
	}
}
