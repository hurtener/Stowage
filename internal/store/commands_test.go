package store

import "testing"

func TestMemoryRevisionTracksSemanticStateNotObservationCounters(t *testing.T) {
	base := Memory{ID: "m", TenantID: "t", ProjectID: "p", UserID: "u", SessionID: "s", Content: "Use Go.", Kind: "decision", Status: "active", UpdatedAt: 100}
	revision := MemoryRevision(base)
	if len(revision) != 64 || revision != MemoryRevision(base) {
		t.Fatal("revision is not a deterministic SHA-256 value")
	}
	observed := base
	observed.UseCount++
	observed.InjectCount++
	observed.MatchCount++
	if MemoryRevision(observed) != revision {
		t.Fatal("retrieving a memory invalidated its semantic revision")
	}
	for _, mutate := range []func(*Memory){
		func(m *Memory) { m.Content = "Use Rust." },
		func(m *Memory) { m.Status = "superseded" },
		func(m *Memory) { m.UserID = "other" },
		func(m *Memory) { m.SupersededByID = "new" },
		func(m *Memory) { m.PrivacyZone = "personal" },
		func(m *Memory) { m.ValidUntil = 200 },
	} {
		changed := base
		mutate(&changed)
		if MemoryRevision(changed) == revision {
			t.Fatal("semantic mutation was not detected")
		}
	}
}
