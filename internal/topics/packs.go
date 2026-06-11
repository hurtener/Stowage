// Package topics manages extraction magnets (topics) and virtual default packs
// (D-043). The public Service type is the single access point used by the
// extraction stage and the topics API handler.
package topics

// Pack-sentinel and pack-name constants.
const (
	// PackOff is a sentinel topic key. When a scope has an active topic whose
	// Key == PackOff, the virtual default pack is suppressed: if there are no
	// other active explicit topics the extraction stage short-circuits without
	// a gateway call (AC-2). This is the opt-out mechanism documented in the
	// phase-07 plan under "Deviations".
	PackOff = "pack:off"

	// PackPreferences is the default topic-pack key for assistant profiles.
	PackPreferences = "pack:preferences"

	// PackAgentLearnings is the default topic-pack key for coding-agent and
	// fleet profiles.
	PackAgentLearnings = "pack:agent-learnings"
)

// packEntry is one topic within a compiled-in default pack.
type packEntry struct {
	Key         string
	Description string
}

// preferencesTopics lists the topics in the pack:preferences virtual pack.
// Applied at prompt-build time when an assistant-profile scope has no explicit
// topics (D-043). Compiled-in; never persisted.
var preferencesTopics = []packEntry{
	{
		Key:         "user-communication-style",
		Description: "How the user prefers to be addressed — tone, verbosity, level of detail",
	},
	{
		Key:         "user-background",
		Description: "The user's domain expertise, occupation, and background that shapes explanations",
	},
	{
		Key:         "user-preferences",
		Description: "Explicit preferences about tools, methods, formats, frameworks, or workflows",
	},
	{
		Key:         "user-personal-facts",
		Description: "Durable personal facts the user has shared (name, location, goals, relationships)",
	},
}

// agentLearningsTopics lists the topics in the pack:agent-learnings virtual pack.
// Applied at prompt-build time when a coding-agent or fleet profile scope has
// no explicit topics (D-043). Compiled-in; never persisted.
var agentLearningsTopics = []packEntry{
	{
		Key:         "technical-decisions",
		Description: "Architecture choices, technology selections, and design decisions made in this project",
	},
	{
		Key:         "code-patterns",
		Description: "Established coding patterns, conventions, naming rules, or approaches in use",
	},
	{
		Key:         "gotchas-and-pitfalls",
		Description: "Known issues, edge cases, footguns, or things to avoid in this codebase or environment",
	},
	{
		Key:         "task-progress",
		Description: "In-progress tasks, TODO items, open work items, and their current status",
	},
	{
		Key:         "project-context",
		Description: "Project-specific context, setup instructions, configuration, and conventions",
	},
}

// defaultPackForProfile returns the compiled-in pack name and entries for the
// given profile. Falls back to pack:preferences for unknown profiles.
func defaultPackForProfile(profile string) (packName string, entries []packEntry) {
	switch profile {
	case "coding-agent", "fleet":
		return PackAgentLearnings, agentLearningsTopics
	default: // "assistant" and fallback
		return PackPreferences, preferencesTopics
	}
}
