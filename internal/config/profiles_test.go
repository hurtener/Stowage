package config_test

import (
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
)

// TestReflectConfigForProfile pins the Phase 19 reflection gating (D-077):
// reflection is enabled only on the fleet profile; single-user profiles keep it
// off so zero-config single-user start does no reflection gateway calls.
func TestReflectConfigForProfile(t *testing.T) {
	cases := []struct {
		profile     string
		wantEnabled bool
	}{
		{"fleet", true},
		{"assistant", false},
		{"coding-agent", false},
		{"", false},        // fallback
		{"unknown", false}, // fallback
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			rc := config.ReflectConfigForProfile(tc.profile)
			if rc.Enabled != tc.wantEnabled {
				t.Errorf("profile %q: Enabled = %v, want %v", tc.profile, rc.Enabled, tc.wantEnabled)
			}
			// Tuning defaults are present and sane regardless of enablement.
			if rc.Interval <= 0 || rc.BatchSize <= 0 || rc.EpochEvery <= 0 {
				t.Errorf("profile %q: non-positive tuning %+v", tc.profile, rc)
			}
			if rc.Interval != 30*time.Minute {
				t.Errorf("profile %q: interval = %v, want 30m", tc.profile, rc.Interval)
			}
		})
	}
}
