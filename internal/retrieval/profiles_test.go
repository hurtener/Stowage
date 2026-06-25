package retrieval

import "testing"

// TestApplyOverride checks that only non-zero override fields replace the preset value
// (D-103): a zero field inherits, so a partial override is valid.
func TestApplyOverride(t *testing.T) {
	t.Parallel()
	base := Profile{LaneK: 30, ScoringK: 10, DefaultLimit: 5, EnableRerank: true}

	tests := []struct {
		name string
		ov   ProfileOverride
		want Profile
	}{
		{"empty inherits all", ProfileOverride{}, base},
		{"scoring_k only", ProfileOverride{ScoringK: 30}, Profile{LaneK: 30, ScoringK: 30, DefaultLimit: 5, EnableRerank: true}},
		{"all three", ProfileOverride{LaneK: 60, ScoringK: 40, DefaultLimit: 12}, Profile{LaneK: 60, ScoringK: 40, DefaultLimit: 12, EnableRerank: true}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ApplyOverride(base, tt.ov); got != tt.want {
				t.Errorf("ApplyOverride(%+v) = %+v, want %+v", tt.ov, got, tt.want)
			}
		})
	}
}

// TestApplyOverrideNeverTouchesRerank confirms EnableRerank is not operator-settable via
// the override path — it stays a property of the profile identity (only precise reranks).
func TestApplyOverrideNeverTouchesRerank(t *testing.T) {
	t.Parallel()
	if got := ApplyOverride(ProfileBalanced, ProfileOverride{ScoringK: 99}); got.EnableRerank {
		t.Errorf("balanced override unexpectedly enabled rerank: %+v", got)
	}
	if got := ApplyOverride(ProfilePrecise, ProfileOverride{}); !got.EnableRerank {
		t.Errorf("precise override unexpectedly disabled rerank: %+v", got)
	}
}

// TestBuildProfilesAllEmptyEqualsPresets is the safety invariant: wiring an all-empty
// config retrieval section reproduces the built-in presets exactly, so WithProfiles is
// always safe to call from boot.
func TestBuildProfilesAllEmptyEqualsPresets(t *testing.T) {
	t.Parallel()
	got := BuildProfiles(ProfileOverride{}, ProfileOverride{}, ProfileOverride{})
	want := map[string]Profile{
		"precise":  ProfilePrecise,
		"balanced": ProfileBalanced,
		"broad":    ProfileBroad,
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("BuildProfiles[%q] = %+v, want %+v", name, got[name], w)
		}
	}
}
