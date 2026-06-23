// Package topics manages extraction magnets (topics), virtual default packs
// (D-043), and pack composition (D-099). The public Service type is the single
// access point used by the extraction stage and the topics API handler.
package topics

import "strings"

// Pack-sentinel, pack-name, and composition constants.
const (
	// PackOff is a sentinel topic key. When a scope has an active topic whose
	// Key == PackOff, packs are suppressed entirely: extraction short-circuits
	// without a gateway call (AC-2). The opt-out mechanism (D-043); it dominates
	// over enabled packs and explicit topics (D-099).
	PackOff = "pack:off"

	// packOnPrefix is the sentinel-key prefix that enables a compiled-in pack at
	// a scope (D-099): an active topic with Key == "pack:on:<name>" adds the pack
	// "pack:<name>" to the scope's composed topic set. Mirrors PackOff.
	packOnPrefix = "pack:on:"

	// MaxActiveTopics caps the composed topic set (explicit ∪ enabled packs) so
	// the extraction prompt and per-flush cost stay bounded (D-099). Explicit
	// topics are retained; pack entries drop by enable order; drops are never
	// silent. An internal recall/cost guardrail like the D-090 cosine floor — not
	// a config knob (D-034 untouched).
	MaxActiveTopics = 32

	// PackPreferences is the default topic pack for assistant profiles.
	PackPreferences = "pack:preferences"

	// PackAgentLearnings is the default topic pack for coding-agent and fleet
	// profiles.
	PackAgentLearnings = "pack:agent-learnings"

	// Curated composable packs (D-099) — enabled per-scope via pack:on:<name>.
	PackProject    = "pack:project"
	PackIncidents  = "pack:incidents"
	PackProduct    = "pack:product"
	PackPeople     = "pack:people"
	PackCompliance = "pack:compliance"
	PackResearch   = "pack:research"
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

// projectTopics — pack:project (D-099). Project-level context distinct from the
// broader agent-learnings: the durable "how this project is shaped and run".
var projectTopics = []packEntry{
	{Key: "project-glossary", Description: "Domain terms, acronyms, and entity names specific to this project and what they mean"},
	{Key: "ownership-and-contacts", Description: "Who owns which component, service, or area, and how to reach them"},
	{Key: "environments-and-endpoints", Description: "Environments (dev/staging/prod), their URLs/endpoints, and how they differ"},
	{Key: "runbooks-and-procedures", Description: "Operational procedures, deploy/release steps, and how to perform recurring tasks"},
	{Key: "project-conventions", Description: "Naming, structure, configuration, and workflow conventions established in this project"},
	{Key: "design-rationale", Description: "Why the project is structured or configured the way it is — the reasoning behind setup choices"},
}

// incidentsTopics — pack:incidents (D-099). A gotcha/failure_mode generator for
// reliability work.
var incidentsTopics = []packEntry{
	{Key: "incidents-and-outages", Description: "Past incidents and outages: what broke, the impact, and the timeline"},
	{Key: "root-causes", Description: "Identified root causes of failures and the conditions that triggered them"},
	{Key: "postmortem-lessons", Description: "Lessons learned and action items from postmortems worth applying next time"},
	{Key: "oncall-footguns", Description: "On-call traps, fragile operations, and known-dangerous actions to avoid"},
	{Key: "mitigations-and-workarounds", Description: "Mitigations, workarounds, and recovery steps that resolved past issues"},
}

// productTopics — pack:product (D-099). Product decisions and the "why" behind them.
var productTopics = []packEntry{
	{Key: "product-decisions", Description: "Product decisions made and the rationale behind them"},
	{Key: "requirement-rationale", Description: "Why specific requirements or scope choices were made"},
	{Key: "user-research-findings", Description: "Findings from user research, interviews, and feedback that shaped the product"},
	{Key: "roadmap-rationale", Description: "Roadmap priorities and the reasoning for sequencing or deferring work"},
	{Key: "success-metrics", Description: "Product goals, success metrics, and what outcomes are being optimized for"},
}

// peopleTopics — pack:people (D-099). CRM-like memory of who's who.
var peopleTopics = []packEntry{
	{Key: "team-members-and-roles", Description: "People on the team, their roles, and areas of responsibility"},
	{Key: "stakeholders", Description: "Key stakeholders, their interests, and what they care about"},
	{Key: "working-relationships", Description: "How specific people prefer to collaborate, communicate, or be involved"},
	{Key: "expertise-and-contacts", Description: "Who to ask about a given topic or system"},
}

// complianceTopics — pack:compliance (D-099). High-importance, safety-relevant rules.
var complianceTopics = []packEntry{
	{Key: "hard-rules-and-prohibitions", Description: "Absolute rules and prohibited actions — things that must never be done"},
	{Key: "approval-requirements", Description: "Actions that require approval, sign-off, or escalation before proceeding"},
	{Key: "data-handling-and-redaction", Description: "Rules for handling sensitive data, privacy, retention, and redaction"},
	{Key: "regulatory-obligations", Description: "Regulatory, legal, or contractual obligations that constrain how work is done"},
}

// researchTopics — pack:research (D-099). Pairs with the trust/verify + citations
// subsystem.
var researchTopics = []packEntry{
	{Key: "sources-and-references", Description: "Cited sources, references, and where a claim or fact came from"},
	{Key: "claims-and-findings", Description: "Substantive claims, findings, and conclusions established during research"},
	{Key: "open-questions", Description: "Unresolved questions, uncertainties, and things still to investigate"},
	{Key: "assumptions", Description: "Working assumptions being relied on that may need validation"},
}

// packRegistry maps every compiled-in pack name to its entries (D-099). It is the
// single source of truth for which packs exist; pack:on:<name> resolution and the
// profile default lists both index into it. Read-only after package init.
var packRegistry = map[string][]packEntry{
	PackPreferences:    preferencesTopics,
	PackAgentLearnings: agentLearningsTopics,
	PackProject:        projectTopics,
	PackIncidents:      incidentsTopics,
	PackProduct:        productTopics,
	PackPeople:         peopleTopics,
	PackCompliance:     complianceTopics,
	PackResearch:       researchTopics,
}

// defaultPacksForProfile returns the ordered list of default pack names for the
// profile, applied only when a scope has expressed no intent (D-099, amending the
// single-pack D-043). One element today; the list keeps the door open to
// multi-default profiles without another algorithm change.
func defaultPacksForProfile(profile string) []string {
	switch profile {
	case "coding-agent", "fleet":
		return []string{PackAgentLearnings}
	default: // "assistant" and fallback
		return []string{PackPreferences}
	}
}

// packEntriesByName returns the entries for a pack name, or (nil, false) if the
// name is not a registered pack.
func packEntriesByName(name string) ([]packEntry, bool) {
	entries, ok := packRegistry[name]
	return entries, ok
}

// packNameFromOnSentinel maps a "pack:on:<name>" topic key to its registered pack
// name (e.g. "pack:on:project" → "pack:project"). Returns ok=false when key is not
// a pack:on sentinel, has an empty name, or names a pack that does not exist (an
// unknown pack is ignored by the caller, not treated as an explicit topic).
func packNameFromOnSentinel(key string) (string, bool) {
	short, ok := strings.CutPrefix(key, packOnPrefix)
	if !ok || short == "" {
		return "", false
	}
	name := "pack:" + short
	if _, exists := packRegistry[name]; !exists {
		return "", false
	}
	return name, true
}
