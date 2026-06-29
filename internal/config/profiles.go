package config

import "time"

// Profiles returns the named profile override maps. Each map contains
// dot-separated key paths whose values differ from Defaults() for that profile.
// Merge order: defaults < profile < file < env (D-034 knob guardrail).
//
// Profile knobs:
//   - telemetry.log_format: text (assistant/coding-agent), json (fleet)
//   - telemetry.runtime_sample_interval: 0/off (assistant/coding-agent), 60s (fleet)
//   - server.pprof_listen: "" (disabled) in every profile for security; enable
//     per-deployment via STOWAGE_SERVER_PPROF_LISTEN or a config file override
//   - vindex.driver: "hnsw" in all profiles (D-048 owner directive; brute is the
//     exact-recall oracle and debug driver, selectable via STOWAGE_VINDEX_DRIVER
//     or config file override)
func Profiles() map[string]map[string]string {
	return map[string]map[string]string{
		"assistant":    {},
		"coding-agent": {},
		"fleet": {
			"telemetry.log_format":              "json",
			"telemetry.runtime_sample_interval": "60",
		},
	}
}

// BufferTriggers holds the buffer-flush trigger thresholds for a profile.
// These are profile-internal constants — not operator-tunable top-level config
// knobs (D-034 knob guardrail). The eval harness re-tunes them later (D-035).
// Resolves OQ-3 (D-042).
type BufferTriggers struct {
	Count  int
	Tokens int64
	MaxAge time.Duration
}

// BufferTriggersForProfile returns the trigger thresholds for the named profile.
// Unknown profiles fall back to "assistant" defaults.
//
// Trigger defaults (D-042):
//
//	| Trigger  | assistant | coding-agent | fleet |
//	|----------|-----------|--------------|-------|
//	| count    |        12 |           20 |    30 |
//	| tokens   |      1500 |         2500 |  4000 |
//	| max age  |      90 s |        180 s |  120s |
func BufferTriggersForProfile(profile string) BufferTriggers {
	switch profile {
	case "coding-agent":
		return BufferTriggers{Count: 20, Tokens: 2500, MaxAge: 180 * time.Second}
	case "fleet":
		return BufferTriggers{Count: 30, Tokens: 4000, MaxAge: 120 * time.Second}
	default: // "assistant" and fallback
		// Coarsened in Phase 29 (D-107): the old 12/1500/90s window flushed every few
		// turns, so extraction often saw too little conversation to retain disambiguating
		// context (the dropped "each way" qualifier). A wider window yields fewer, richer,
		// better-contextualized memories without changing the fire-and-forget contract (P2).
		return BufferTriggers{Count: 18, Tokens: 2500, MaxAge: 180 * time.Second}
	}
}

// ReflectConfig holds the per-profile reflection-sweep tuning (Phase 19, D-077).
// Like BufferTriggers and PlaybookBudget above, these are profile-internal
// constants — NOT operator-tunable top-level config knobs — tuned per profile and
// re-tuned by the eval harness (D-035). Reflection is the fleet-learning loop, so
// it is ENABLED only on the fleet profile by default; single-user profiles
// (assistant, coding-agent) keep it off so zero-config single-user start does no
// reflection gateway calls (D-034 zero-config invariant preserved).
type ReflectConfig struct {
	Enabled    bool          // wire the reflection sweep at all
	Interval   time.Duration // sweep cadence (jittered)
	BatchSize  int           // max outcome-tagged records per scope per sweep
	EpochEvery int           // every Nth interval re-reflects the wider trailing window
}

// ReflectConfigForProfile returns the reflection-sweep tuning for the named
// profile. Unknown profiles fall back to "assistant" (off).
//
//	| profile      | enabled | interval | batch | epoch_every |
//	|--------------|---------|----------|-------|-------------|
//	| assistant    |   off   |   30m    |  200  |      8      |
//	| coding-agent |   off   |   30m    |  200  |      8      |
//	| fleet        |   ON    |   30m    |  200  |      8      |
func ReflectConfigForProfile(profile string) ReflectConfig {
	rc := ReflectConfig{Enabled: false, Interval: 30 * time.Minute, BatchSize: 200, EpochEvery: 8}
	if profile == "fleet" {
		rc.Enabled = true
	}
	return rc
}

// EpisodeConfig holds the per-profile episode-sweep tuning (Phase 22, D-079).
// Profile-internal (like ReflectConfig/BufferTriggers — not top-level config
// knobs). Episodes are enabled where episodic memory helps; off by zero-config
// default elsewhere.
type EpisodeConfig struct {
	Enabled         bool
	DetectInterval  time.Duration
	NarrateInterval time.Duration
	IdleWindow      time.Duration // a session idle this long is "closed"
	GapSplit        time.Duration // intra-session gap that splits an episode; 0 = off (v1)
	// CausalMinConfidence gates which inferred led_to edges persist during narration
	// (Phase 24, D-083). Profile-internal (like the intervals above — NOT a top-level
	// operator knob); the eval harness re-tunes it with real data (D-035).
	CausalMinConfidence float64

	// Episode threading (Phase 24b, D-081). Profile-internal; OFF BY DEFAULT —
	// enablement is gated on an episodic-eval win (D-035). The eval re-tunes the
	// overlap/window thresholds before any profile turns it on.
	ThreadingEnabled bool
	ThreadInterval   time.Duration
	ThreadMinOverlap float64
	ThreadWindow     time.Duration
	ThreadBatchSize  int
}

// EpisodeConfigForProfile returns the episode-sweep tuning for the named profile.
// Enabled for assistant + fleet (episodic memory is useful for both); off for
// coding-agent and unknown profiles. Profile-internal (not a top-level knob).
func EpisodeConfigForProfile(profile string) EpisodeConfig {
	ec := EpisodeConfig{
		Enabled: false, DetectInterval: 15 * time.Minute, NarrateInterval: 15 * time.Minute,
		IdleWindow: 30 * time.Minute, GapSplit: 0, CausalMinConfidence: 0.6,
		// Threading ships OFF in every profile (D-081 eval-gate); thresholds are the
		// conservative defaults the eval will re-tune before enablement.
		ThreadingEnabled: false, ThreadInterval: 30 * time.Minute, ThreadMinOverlap: 0.3,
		ThreadWindow: 30 * 24 * time.Hour, ThreadBatchSize: 50,
	}
	if profile == "assistant" || profile == "fleet" {
		ec.Enabled = true
	}
	return ec
}

// ProactiveConfig holds the per-profile proactive-engine governance defaults
// (Phase 27, D-087). Profile-internal (like BufferTriggers/EpisodeConfig — NOT a
// top-level operator knob); the per-scope stored "proactive" setting overrides it
// at runtime (RFC §6d), and the eval harness re-tunes the defaults (D-035).
//
// Proactive offers are PUSHED context, so the bar is deliberately high: "silence
// over spam". Enabled where episodic memory helps (assistant, fleet) and OFF for
// coding-agent (a coding agent drives its own context and dislikes interruptions)
// and unknown profiles — preserving the zero-config no-surprise-gateway-calls
// invariant (D-034): the gateway-touching similar_episode class is off by default
// in every profile; the two gateway-free classes carry the default experience.
type ProactiveConfig struct {
	Enabled   bool
	Threshold float64
	Budget    int
	Classes   map[string]bool
}

// ProactiveConfigForProfile returns the proactive governance defaults for the named
// profile. The default-enabled classes are the two GATEWAY-FREE ones
// (recent_episode, expiring); similar_episode is opt-in per scope so a zero-config
// start makes no proactive gateway calls (D-036/D-034). Unknown profiles fall back
// to "assistant".
//
//	| profile      | enabled | threshold | budget | default classes               |
//	|--------------|---------|-----------|--------|-------------------------------|
//	| assistant    |   ON    |    0.45   |   2    | recent_episode, expiring      |
//	| fleet        |   ON    |    0.55   |   1    | recent_episode, expiring      |
//	| coding-agent |   off   |    0.60   |   1    | (none)                        |
func ProactiveConfigForProfile(profile string) ProactiveConfig {
	gatewayFree := func() map[string]bool {
		return map[string]bool{"recent_episode": true, "expiring": true}
	}
	switch profile {
	case "fleet":
		return ProactiveConfig{Enabled: true, Threshold: 0.55, Budget: 1, Classes: gatewayFree()}
	case "coding-agent":
		return ProactiveConfig{Enabled: false, Threshold: 0.60, Budget: 1, Classes: map[string]bool{}}
	default: // "assistant" and fallback
		return ProactiveConfig{Enabled: true, Threshold: 0.45, Budget: 2, Classes: gatewayFree()}
	}
}

// PlaybookBudgetForProfile returns the deterministic playbook token budget for
// the named profile (D-072). Like the buffer triggers above, this is a
// profile-internal constant — NOT an operator-tunable top-level config knob
// (D-034 knob guardrail). It bounds how much of a scope's strategy/failure_mode/
// building-block memory the assembled playbook packs; the eval harness re-tunes
// it later with real data (D-035). Unknown profiles fall back to "assistant".
//
// Budget defaults (token estimate, ≈4 chars/token):
//
//	| assistant | coding-agent | fleet |
//	|-----------|--------------|-------|
//	|      2000 |         3000 |  4000 |
//
// The coding-agent budget is larger because coding playbooks (strategies +
// gotchas) are denser and benefit from a fuller injected context; fleet is
// larger still for multi-agent supervisory contexts.
func PlaybookBudgetForProfile(profile string) int {
	switch profile {
	case "coding-agent":
		return 3000
	case "fleet":
		return 4000
	default: // "assistant" and fallback
		return 2000
	}
}
