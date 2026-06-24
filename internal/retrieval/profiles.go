package retrieval

// Profile controls the retrieval trade-off between precision and recall.
// Named presets encode the {laneK, scoringK, defaultLimit, enableRerank} tuple
// used during the retrieve call (D-034 knob guardrail).
type Profile struct {
	// LaneK is the number of candidates fetched per lane before RRF fusion.
	LaneK int
	// ScoringK is the scoring window (top-N fused IDs scored by utility function).
	ScoringK int
	// DefaultLimit is used when the caller does not specify a limit.
	DefaultLimit int
	// EnableRerank enables the cross-encoder rerank pass (Phase 12).
	// Only ProfilePrecise enables this; balanced and broad rely on RRF + Phase-10.
	EnableRerank bool
}

// The three named retrieval presets.
var (
	// ProfilePrecise favours depth over breadth: tight laneK, small limit, plus
	// cross-encoder reranking for maximum relevance (Phase 12).
	ProfilePrecise = Profile{LaneK: 30, ScoringK: 10, DefaultLimit: 5, EnableRerank: true}

	// ProfileBalanced is the default preset: wide enough for good recall,
	// tight enough for fast responses.
	ProfileBalanced = Profile{LaneK: 100, ScoringK: 20, DefaultLimit: 10}

	// ProfileBroad maximises recall at the cost of latency and noise.
	// Best for exploratory queries or when the relevant memory may be rare.
	ProfileBroad = Profile{LaneK: 200, ScoringK: 50, DefaultLimit: 20}
)

// ProfileOverride carries optional per-field overrides for a named profile (D-103).
// A zero field inherits the built-in preset value, so a partial override (e.g. only
// precise.ScoringK) is valid. EnableRerank is intentionally not overridable: reranking
// is a property of the precise profile's identity, wired via the gateway rerank model.
type ProfileOverride struct {
	LaneK        int
	ScoringK     int
	DefaultLimit int
}

// ApplyOverride returns base with each non-zero override field applied.
func ApplyOverride(base Profile, o ProfileOverride) Profile {
	if o.LaneK > 0 {
		base.LaneK = o.LaneK
	}
	if o.ScoringK > 0 {
		base.ScoringK = o.ScoringK
	}
	if o.DefaultLimit > 0 {
		base.DefaultLimit = o.DefaultLimit
	}
	return base
}

// BuildProfiles applies the three deployment overrides onto the built-in presets and
// returns the resolved set for Retriever.WithProfiles. An all-zero override set yields
// a map equal to the built-in presets (the tuned defaults), so wiring it is always safe.
func BuildProfiles(precise, balanced, broad ProfileOverride) map[string]Profile {
	return map[string]Profile{
		"precise":  ApplyOverride(ProfilePrecise, precise),
		"balanced": ApplyOverride(ProfileBalanced, balanced),
		"broad":    ApplyOverride(ProfileBroad, broad),
	}
}

// profileByName resolves the named preset. Returns (profile, ok) — ok is false
// for unknown names. The empty string maps to ProfileBalanced (default).
func profileByName(name string) (Profile, bool) {
	switch name {
	case "", "balanced":
		return ProfileBalanced, true
	case "precise":
		return ProfilePrecise, true
	case "broad":
		return ProfileBroad, true
	default:
		return Profile{}, false
	}
}
