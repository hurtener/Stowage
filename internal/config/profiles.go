package config

import "time"

// Profiles returns the named profile override maps. Each map contains
// dot-separated key paths whose values differ from Defaults() for that profile.
// Merge order: defaults < profile < file < env (D-034 knob guardrail).
//
// Currently the only profile-tunable is telemetry.log_format:
//   - "assistant"   → text  (human-readable; matches the default)
//   - "coding-agent"→ text  (same as assistant; reserved for future knobs)
//   - "fleet"       → json  (structured; suited for log aggregators)
func Profiles() map[string]map[string]string {
	return map[string]map[string]string{
		"assistant":    {},
		"coding-agent": {},
		"fleet": {
			"telemetry.log_format": "json",
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
		return BufferTriggers{Count: 12, Tokens: 1500, MaxAge: 90 * time.Second}
	}
}
