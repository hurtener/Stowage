package config

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
