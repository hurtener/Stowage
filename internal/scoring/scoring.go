// Package scoring implements the utility scoring function for retrieval ranking
// (Phase 10, RFC §5.2, §4.2 step 4).
//
// The Score function is PURE: no I/O, no clock reads, no randomness. Every
// input is an explicit parameter; side-effect-free construction is verified by
// TestScoringPackageImports.
//
// # Trust multipliers (D-050)
//
// The multipliers here are scoring weights: they affect the rank a memory
// receives in a retrieval response. They are DISTINCT from the supersede-gate
// trust computed in internal/reconcile (trust.go), which governs whether a new
// candidate can silently overwrite a high-value existing memory. Different jobs:
//   - Scoring trust: biases retrieval rank toward information the system (or
//     user) has confirmed.
//   - Supersede trust: gates destructive mutation to protect high-value memories
//     from being silently overwritten by low-signal candidates.
//
// Both are documented here to prevent accidental merging of the two concerns.
package scoring

import "math"

// ─── Constants ───────────────────────────────────────────────────────────────
// All constants are named with doc comments (D-034 knob guardrail). These are
// profile-internal starting values; Phase 13 eval re-tunes with real data.

const (
	// saveWeightInUseBoost is the extra multiplier applied to save_count inside
	// the use-boost formula: useBoost = 1 + log2(1 + use + saveWeight*save).
	// Save counts double because a user explicitly saving a memory is a stronger
	// utility signal than a passive use.
	saveWeightInUseBoost float64 = 2.0

	// noisePenaltyCoeff scales the noise counter inside the noise-penalty formula:
	// noisePenalty = 1 / (1 + coeff * noise). Higher coefficient → sharper drop.
	noisePenaltyCoeff float64 = 0.15

	// noisePenaltyFloor is the minimum value of the noise-penalty factor. Even
	// very noisy memories are not zeroed out (may still carry useful signal).
	noisePenaltyFloor float64 = 0.40

	// precisionFactorMin is the lower bound of the precision-factor ramp.
	// A memory with many injections but zero uses gets this penalty factor.
	precisionFactorMin float64 = 0.50

	// precisionFactorMax is the upper bound of the precision-factor ramp.
	// A memory with a perfect use/inject ratio gets this bonus factor.
	precisionFactorMax float64 = 1.50

	// precisionFactorUnused is the precision factor applied when inject_count == 0.
	// The memory has not been tested yet, so we grant it the benefit of the doubt
	// (no zombie evidence). Equals precisionFactorMax.
	precisionFactorUnused float64 = 1.50

	// explorationBonusInjectThresh is the inject_count below which the
	// exploration bonus is applied. New memories get a chance to compete.
	explorationBonusInjectThresh int64 = 3

	// explorationBonusMultiplier is the bonus applied to memories with very few
	// injections. Ensures brand-new memories are not buried by established ones.
	explorationBonusMultiplier float64 = 1.30

	// decayBlendAlpha is the weight given to activity-turns in the Δ blend.
	// Δ = α·turns + (1−α)·days. Fixes the predecessor's dormant-project blind
	// spot: wall-clock decay ensures memories age even in inactive scopes.
	decayBlendAlpha float64 = 0.60

	// msPerDay converts unix-millisecond deltas to days.
	msPerDay float64 = 86_400_000.0

	// decayFloorDefault is the minimum decay factor for memories whose
	// trust_source is not "user_stated". Even ancient, proven memories are
	// retrieved at this minimum fraction of their peak score.
	decayFloorDefault float64 = 0.10

	// decayFloorUserStated is the minimum decay factor for memories marked
	// trust_source = "user_stated". User-stated facts are treated as durable
	// preferences and decay much more slowly.
	decayFloorUserStated float64 = 0.50

	// trustMultUserStated is the scoring multiplier for user_stated trust.
	// See package doc for the distinction from supersede-gate trust (D-050).
	trustMultUserStated float64 = 1.25

	// trustMultAgreedUpon is the scoring multiplier for agreed_upon trust.
	trustMultAgreedUpon float64 = 1.15

	// trustMultAgentSuggested is the scoring multiplier for agent_suggested trust.
	trustMultAgentSuggested float64 = 1.00

	// trustMultLLMExtracted is the scoring multiplier for llm_extracted trust.
	trustMultLLMExtracted float64 = 0.95

	// scopeAffinitySession is the multiplier applied when the retrieving session
	// matches the memory's origin session.
	scopeAffinitySession float64 = 1.30

	// scopeAffinityProject is the multiplier applied when the same project matches
	// but the session differs.
	scopeAffinityProject float64 = 1.15

	// temporalBoostMax is the maximum multiplier from temporal-proximity boosting.
	// When a query window is provided and the memory falls exactly at the window
	// centre, this multiplier is applied (RFC §4.2.5, D-036).
	temporalBoostMax float64 = 1.20

	// hubDampeningThreshold is the number of distinct query clusters that must
	// have returned a memory before it is considered a "hub" (generic content).
	hubDampeningThreshold int = 4

	// hubDampeningMultiplier is applied to hub memories. Generic memories that
	// appear in many unrelated contexts are penalised for low specificity.
	hubDampeningMultiplier float64 = 0.80

	// cooldownWindowMs is the suppression window for write-echo cooldown.
	// A memory created less than this many milliseconds ago is suppressed for
	// retrievals in the same session that extracted it (anti-echo).
	cooldownWindowMs int64 = 30 * 60 * 1000 // 30 minutes

	// cooldownMultiplier is the score factor applied during write-echo cooldown.
	// 0.1 effectively removes the memory from consideration for its origin session
	// while it is still fresh, without fully zeroing it out.
	cooldownMultiplier float64 = 0.10

	// importanceBase is the intercept of the importance multiplier:
	// importanceMult = importanceBase + importancePerUnit * importance.
	// At importance=0 the multiplier is 0.8 (slight down-weight for unimportant
	// memories); at importance=5 it is 1.3 (significant up-weight).
	importanceBase float64 = 0.80

	// importancePerUnit is the per-point increment of the importance multiplier.
	importancePerUnit float64 = 0.10
)

// ─── Types ───────────────────────────────────────────────────────────────────

// MemoryFacts carries the memory-specific inputs to the scoring function.
// These are sourced from the store.Memory projection returned by GetMany.
type MemoryFacts struct {
	// Six utility counters (RFC §5.2, D-008).
	MatchCount  int64
	InjectCount int64
	UseCount    int64
	SaveCount   int64
	FailCount   int64
	NoiseCount  int64

	// Importance is the 0–10 editorial weight assigned at extraction time.
	Importance int

	// Confidence is the extraction confidence (0–1); not used in the current
	// formula but carried in MemoryFacts for completeness and future use.
	Confidence float64

	// TrustSource is one of: "user_stated", "agreed_upon", "agent_suggested",
	// "llm_extracted". Controls both the scoring trust multiplier and the decay
	// floor (see package-level constants).
	TrustSource string

	// Stability is the initial time-constant for decay. Grown by proven utility
	// via effectiveStability = stability × (1 + log2(1 + use + 2·save)).
	Stability float64

	// CreatedAt and LastAccessedAt are unix milliseconds. LastAccessedAt = 0
	// means the memory has never been accessed; in that case the day-normalised
	// component of Δ is treated as zero (recently-created assumption).
	CreatedAt      int64
	LastAccessedAt int64

	// SessionID is the session from which this memory was originally extracted.
	// Used for write-echo cooldown detection: compared against Inputs.SameSession.
	SessionID string
}

// Window is an optional time-range filter carried by the query.
// Timestamps are unix milliseconds; zero means the bound is absent.
type Window struct {
	From  int64 // inclusive lower bound; 0 = no lower bound
	Until int64 // inclusive upper bound; 0 = no upper bound
}

// Inputs is the full input set for Score. All timing and activity signals are
// supplied by the retrieval layer; no clock or I/O calls happen inside Score.
type Inputs struct {
	// Memory carries the facts from the store projection.
	Memory MemoryFacts

	// FusedScore is the RRF fused score from the retrieval lanes.
	FusedScore float64

	// Now is the current unix timestamp in milliseconds, supplied by the caller.
	Now int64

	// ActivityTurns is the count of records in the scope created after
	// memory.LastAccessedAt. It is a batched approximation: the same value is
	// reused for all items in one retrieve call to avoid per-item queries.
	// Approximation is documented in internal/retrieval/retrieval.go.
	ActivityTurns int64

	// QueryWindow, when non-nil, enables temporal-proximity boosting for
	// memories whose created_at falls inside or near the window.
	QueryWindow *Window

	// SameSession is true when the retrieving session matches the memory's
	// origin session (memory.SessionID). Set by the retrieval layer from
	// the Request.SessionID field. Used for write-echo cooldown.
	SameSession bool

	// HubSignals is the count of distinct query clusters that have returned
	// this memory in the recent window. Tracked by the hub LRU in
	// internal/retrieval. Values >= hubDampeningThreshold trigger dampening.
	HubSignals int
}

// Breakdown carries every factor computed by Score, indexed by name, for
// debug mode and golden tests. All values are the factors as multiplied;
// FinalScore == FusedScore × product-of-all-factors.
type Breakdown struct {
	UseBoost         float64 `json:"use_boost"`
	NoisePenalty     float64 `json:"noise_penalty"`
	PrecisionFactor  float64 `json:"precision_factor"`
	ExplorationBonus float64 `json:"exploration_bonus"`
	DecayFactor      float64 `json:"decay_factor"`
	TrustMultiplier  float64 `json:"trust_multiplier"`
	ScopeAffinity    float64 `json:"scope_affinity"`
	TemporalBoost    float64 `json:"temporal_boost"`
	HubDampening     float64 `json:"hub_dampening"`
	Cooldown         float64 `json:"cooldown"`
	ImportanceMult   float64 `json:"importance_mult"`
	FinalScore       float64 `json:"final_score"`
}

// ─── Score ───────────────────────────────────────────────────────────────────

// Score computes the utility-adjusted retrieval score for a single memory.
// It is a pure function: deterministic, no I/O, no global state. All timing
// and activity signals are supplied explicitly via in.
//
// The returned (score, Breakdown) pair carries both the final score and a
// per-factor breakdown suitable for debug responses and golden tests.
func Score(in Inputs) (float64, Breakdown) {
	use := in.Memory.UseCount
	save := in.Memory.SaveCount
	inject := in.Memory.InjectCount
	noise := in.Memory.NoiseCount

	// ── 1. Use boost ─────────────────────────────────────────────────────────
	// Logarithmic reward for proven utility. Save is double-weighted because
	// an explicit save is a stronger signal than a passive use.
	useBoost := 1.0 + math.Log2(1.0+float64(use)+saveWeightInUseBoost*float64(save))

	// ── 2. Noise penalty ─────────────────────────────────────────────────────
	// Each noise increment degrades score; floored at noisePenaltyFloor.
	noisePenalty := 1.0 / (1.0 + noisePenaltyCoeff*float64(noise))
	if noisePenalty < noisePenaltyFloor {
		noisePenalty = noisePenaltyFloor
	}

	// ── 3. Precision factor ───────────────────────────────────────────────────
	// Ramp from 0.5 (zombie: many injections, zero use) to 1.5 (fully used).
	// When inject == 0 the memory is untested; grant the benefit of the doubt.
	var precisionFactor float64
	if inject == 0 {
		precisionFactor = precisionFactorUnused
	} else {
		precisionFactor = 0.5 + float64(use)/float64(inject)
		if precisionFactor < precisionFactorMin {
			precisionFactor = precisionFactorMin
		} else if precisionFactor > precisionFactorMax {
			precisionFactor = precisionFactorMax
		}
	}

	// ── 4. Exploration bonus ──────────────────────────────────────────────────
	// New memories (low inject) get a temporary lift so they are not buried by
	// established memories before they have a chance to accumulate utility.
	explorationBonus := 1.0
	if inject < explorationBonusInjectThresh {
		explorationBonus = explorationBonusMultiplier
	}

	// ── 5. Decay ──────────────────────────────────────────────────────────────
	// Δ blends scope-activity turns and wall-clock days to handle the
	// dormant-project blind spot of purely turn-based decay (D-008).
	stability := in.Memory.Stability
	if stability <= 0 {
		stability = 1.0 // defensive: unset stability treated as unit value
	}
	effectiveStability := stability * (1.0 + math.Log2(1.0+float64(use)+saveWeightInUseBoost*float64(save)))

	turnsNorm := float64(in.ActivityTurns)
	var daysNorm float64
	if in.Memory.LastAccessedAt > 0 {
		elapsed := float64(in.Now - in.Memory.LastAccessedAt)
		if elapsed > 0 {
			daysNorm = elapsed / msPerDay
		}
	}
	delta := decayBlendAlpha*turnsNorm + (1-decayBlendAlpha)*daysNorm

	decayFactor := 1.0
	if effectiveStability > 0 && delta > 0 {
		decayFactor = math.Exp(-delta / effectiveStability)
	}

	// Apply trust-source-dependent decay floor.
	decayFloor := decayFloorDefault
	if in.Memory.TrustSource == "user_stated" {
		decayFloor = decayFloorUserStated
	}
	if decayFactor < decayFloor {
		decayFactor = decayFloor
	}

	// ── 6. Trust multiplier ───────────────────────────────────────────────────
	// Scoring weight reflecting source reliability (D-050). Distinct from the
	// supersede-gate trust in internal/reconcile (different job: rank vs protect).
	trustMult := trustMultiplierFor(in.Memory.TrustSource)

	// ── 7. Scope affinity ─────────────────────────────────────────────────────
	scopeAff := scopeAffinityFactor(in)

	// ── 8. Temporal proximity ─────────────────────────────────────────────────
	tempBoost := temporalBoostFactor(in)

	// ── 9. Hub dampening ──────────────────────────────────────────────────────
	hubDampening := 1.0
	if in.HubSignals >= hubDampeningThreshold {
		hubDampening = hubDampeningMultiplier
	}

	// ── 10. Write-echo cooldown ───────────────────────────────────────────────
	// Suppress memories that were just extracted in this session so the agent
	// does not retrieve back verbatim what it just injected (anti-echo loop).
	cooldown := 1.0
	if in.SameSession {
		age := in.Now - in.Memory.CreatedAt
		if age >= 0 && age < cooldownWindowMs {
			cooldown = cooldownMultiplier
		}
	}

	// ── 11. Importance ────────────────────────────────────────────────────────
	importanceMult := importanceBase + importancePerUnit*float64(in.Memory.Importance)

	// ── Final score ───────────────────────────────────────────────────────────
	finalScore := in.FusedScore *
		useBoost *
		noisePenalty *
		precisionFactor *
		explorationBonus *
		decayFactor *
		trustMult *
		scopeAff *
		tempBoost *
		hubDampening *
		cooldown *
		importanceMult

	bd := Breakdown{
		UseBoost:         useBoost,
		NoisePenalty:     noisePenalty,
		PrecisionFactor:  precisionFactor,
		ExplorationBonus: explorationBonus,
		DecayFactor:      decayFactor,
		TrustMultiplier:  trustMult,
		ScopeAffinity:    scopeAff,
		TemporalBoost:    tempBoost,
		HubDampening:     hubDampening,
		Cooldown:         cooldown,
		ImportanceMult:   importanceMult,
		FinalScore:       finalScore,
	}
	return finalScore, bd
}

// trustMultiplierFor returns the scoring trust multiplier for a trust source.
// Unknown or empty sources are treated as agent_suggested (neutral).
func trustMultiplierFor(trustSource string) float64 {
	switch trustSource {
	case "user_stated":
		return trustMultUserStated
	case "agreed_upon":
		return trustMultAgreedUpon
	case "agent_suggested":
		return trustMultAgentSuggested
	case "llm_extracted":
		return trustMultLLMExtracted
	default:
		return trustMultAgentSuggested
	}
}

// scopeAffinityFactor returns the scope-affinity multiplier.
// SameSession is set by the retrieval layer based on Request.SessionID.
// SameProject (SameSession=false but same project context) would require
// project-level tracking, which is not yet wired (Phase 10 wires session only).
func scopeAffinityFactor(in Inputs) float64 {
	if in.SameSession {
		return scopeAffinitySession
	}
	// Project affinity: same project, different session. Retrieval currently
	// does not supply a project-match signal, so this path is inactive in v1.
	// The constant is defined for completeness; Phase 11 wires project matching.
	_ = scopeAffinityProject
	return 1.0
}

// temporalBoostFactor returns the temporal-proximity boost (max 1.2×).
// When the query carries a window, memories whose created_at falls inside
// or near the window receive a proportional boost. Memories outside the window
// receive 1.0 (no boost, not a penalty — lane filters handle exclusion).
func temporalBoostFactor(in Inputs) float64 {
	if in.QueryWindow == nil {
		return 1.0
	}
	from := in.QueryWindow.From
	until := in.QueryWindow.Until

	var closeness float64
	switch {
	case from > 0 && until > 0:
		// Full window: compute closeness to centre.
		center := float64(from+until) / 2.0
		radius := float64(until-from) / 2.0
		if radius <= 0 {
			return 1.0
		}
		dist := math.Abs(float64(in.Memory.CreatedAt) - center)
		c := 1.0 - dist/radius
		if c < 0 {
			c = 0
		}
		closeness = c
	case from > 0:
		if in.Memory.CreatedAt >= from {
			closeness = 1.0
		}
	case until > 0:
		if in.Memory.CreatedAt <= until {
			closeness = 1.0
		}
	default:
		return 1.0
	}
	return 1.0 + (temporalBoostMax-1.0)*closeness
}
