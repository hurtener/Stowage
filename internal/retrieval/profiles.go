package retrieval

// Profile controls the retrieval trade-off between precision and recall.
// Named presets encode the {laneK, scoringK, defaultLimit} triple
// used during the retrieve call (D-034 knob guardrail).
type Profile struct {
	// LaneK is the number of candidates fetched per lane before RRF fusion.
	LaneK int
	// ScoringK is the scoring window (top-N fused IDs scored by utility function).
	ScoringK int
	// DefaultLimit is used when the caller does not specify a limit.
	DefaultLimit int
}

// The three named retrieval presets.
var (
	// ProfilePrecise favours depth over breadth: tight laneK, small limit.
	// Best for focused queries where the top answer is highly likely to be exact.
	ProfilePrecise = Profile{LaneK: 30, ScoringK: 10, DefaultLimit: 5}

	// ProfileBalanced is the default preset: wide enough for good recall,
	// tight enough for fast responses.
	ProfileBalanced = Profile{LaneK: 100, ScoringK: 20, DefaultLimit: 10}

	// ProfileBroad maximises recall at the cost of latency and noise.
	// Best for exploratory queries or when the relevant memory may be rare.
	ProfileBroad = Profile{LaneK: 200, ScoringK: 50, DefaultLimit: 20}
)

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
