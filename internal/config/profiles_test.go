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

// TestEpisodeConfigForProfile pins the episode-sweep gating: episodic memory is
// enabled for assistant + fleet, off for coding-agent and unknown/fallback
// profiles; threading ships OFF in every profile (D-081 eval-gate).
func TestEpisodeConfigForProfile(t *testing.T) {
	cases := []struct {
		profile     string
		wantEnabled bool
	}{
		{"assistant", true},
		{"fleet", true},
		{"coding-agent", false},
		{"", false},        // fallback
		{"unknown", false}, // fallback
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			ec := config.EpisodeConfigForProfile(tc.profile)
			if ec.Enabled != tc.wantEnabled {
				t.Errorf("profile %q: Enabled = %v, want %v", tc.profile, ec.Enabled, tc.wantEnabled)
			}
			// Threading is OFF in every profile regardless of enablement (D-081).
			if ec.ThreadingEnabled {
				t.Errorf("profile %q: ThreadingEnabled = true, want false (D-081)", tc.profile)
			}
			// Tuning defaults are present and sane regardless of enablement.
			if ec.DetectInterval <= 0 || ec.NarrateInterval <= 0 || ec.IdleWindow <= 0 {
				t.Errorf("profile %q: non-positive tuning %+v", tc.profile, ec)
			}
			if ec.CausalMinConfidence <= 0 || ec.CausalMinConfidence > 1 {
				t.Errorf("profile %q: CausalMinConfidence = %v, want (0,1]", tc.profile, ec.CausalMinConfidence)
			}
		})
	}
}

// TestProactiveConfigForProfile pins the proactive governance defaults: enabled
// for assistant + fleet, off for coding-agent; the two default-enabled classes are
// the GATEWAY-FREE ones (recent_episode, expiring) so a zero-config start makes no
// proactive gateway calls (similar_episode is opt-in per scope, D-036/D-034).
func TestProactiveConfigForProfile(t *testing.T) {
	cases := []struct {
		profile       string
		wantEnabled   bool
		wantThreshold float64
		wantBudget    int
		wantClasses   int // count of default-enabled classes
	}{
		{"assistant", true, 0.45, 2, 2},
		{"fleet", true, 0.55, 1, 2},
		{"coding-agent", false, 0.60, 1, 0},
		{"", true, 0.45, 2, 2},        // fallback → assistant
		{"unknown", true, 0.45, 2, 2}, // fallback → assistant
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			pc := config.ProactiveConfigForProfile(tc.profile)
			if pc.Enabled != tc.wantEnabled {
				t.Errorf("profile %q: Enabled = %v, want %v", tc.profile, pc.Enabled, tc.wantEnabled)
			}
			if pc.Threshold != tc.wantThreshold {
				t.Errorf("profile %q: Threshold = %v, want %v", tc.profile, pc.Threshold, tc.wantThreshold)
			}
			if pc.Budget != tc.wantBudget {
				t.Errorf("profile %q: Budget = %d, want %d", tc.profile, pc.Budget, tc.wantBudget)
			}
			if len(pc.Classes) != tc.wantClasses {
				t.Errorf("profile %q: %d default classes, want %d (%v)", tc.profile, len(pc.Classes), tc.wantClasses, pc.Classes)
			}
			// The gateway-touching class must never be default-enabled (D-036/D-034).
			if pc.Classes["similar_episode"] {
				t.Errorf("profile %q: similar_episode is default-enabled — breaks the zero-config no-gateway-call invariant", tc.profile)
			}
		})
	}
}

// TestPlaybookBudgetForProfile pins the deterministic playbook token budgets
// (D-072); unknown/empty profiles fall back to the assistant budget.
func TestPlaybookBudgetForProfile(t *testing.T) {
	cases := []struct {
		profile    string
		wantBudget int
	}{
		{"assistant", 2000},
		{"coding-agent", 3000},
		{"fleet", 4000},
		{"", 2000},        // fallback → assistant
		{"unknown", 2000}, // fallback → assistant
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			if got := config.PlaybookBudgetForProfile(tc.profile); got != tc.wantBudget {
				t.Errorf("profile %q: budget = %d, want %d", tc.profile, got, tc.wantBudget)
			}
		})
	}
}
