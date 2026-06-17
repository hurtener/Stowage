package config

import "time"

// Profiles returns the named profile override maps. Each map contains
// dot-separated key paths whose values differ from Defaults() for that profile.
// Merge order: defaults < profile < file < env (D-034 knob guardrail).
//
// Profile knobs:
//   - telemetry.log_format: text (assistant/coding-agent), json (fleet)
//   - vindex.driver: "hnsw" in all profiles (D-048 owner directive; brute is the
//     exact-recall oracle and debug driver, selectable via STOWAGE_VINDEX_DRIVER
//     or config file override)
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
