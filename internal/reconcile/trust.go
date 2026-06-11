package reconcile

import (
	"math"

	"github.com/hurtener/stowage/internal/store"
)

// sourceMultiplierMap maps trust_source values to their contribution weight in
// the trust gate formula. Higher values mean the memory is harder to supersede
// without human review (D-044).
//
//	user_stated    — explicit user assertion; highest protection.
//	agreed_upon    — user confirmed agent claim.
//	agent_suggested — agent proposed, not confirmed.
//	llm_extracted  — extracted by the pipeline without user confirmation.
var sourceMultiplierMap = map[string]float64{
	"user_stated":     2.0,
	"agreed_upon":     1.5,
	"agent_suggested": 1.0,
	"llm_extracted":   0.7,
}

// defaultSourceMultiplier is used when trust_source does not appear in
// sourceMultiplierMap (e.g. legacy rows with an empty value).
const defaultSourceMultiplier = 1.0

// Trust gate thresholds for supersede/update operations on TARGET memories (D-044).
// A score below trustGateWarn means the decision is applied silently.
// A score in [trustGateWarn, trustGatePark) applies the decision and emits
// a reconcile.warned audit event.
// A score ≥ trustGatePark parks the incoming memory as pending_confirmation;
// the target stays active until a human resolves the conflict (Phase 15).
const (
	trustGateWarn = 1.0 // [1.0, 3.0) → apply + reconcile.warned event
	trustGatePark = 3.0 // ≥ 3.0 → park new memory as pending_confirmation
)

// contradictionBoostImportanceFloor is the minimum importance applied to a
// superseding memory (D-044). Corrections must outrank what they correct
// immediately; importance = max(candidate.Importance, contradictionBoostImportanceFloor).
const contradictionBoostImportanceFloor = 4

// contradictionBoostStabilityDelta is the stability addend applied to a
// superseding memory (D-044). 1.0 is the normalised time-constant unit,
// corresponding to approximately 45 days in milliseconds
// (45 × 24 × 60 × 60 × 1000 = 3 888 000 000 ms). This ensures a correction
// decays more slowly than the memory it replaced.
const contradictionBoostStabilityDelta = 1.0

// TrustLevel is the outcome of evaluating a target memory against the trust
// gate thresholds defined above.
type TrustLevel int

const (
	TrustLevelLow    TrustLevel = iota // score < trustGateWarn  → apply
	TrustLevelMedium                   // trustGateWarn ≤ score < trustGatePark → apply + warn
	TrustLevelHigh                     // score ≥ trustGatePark  → park new memory
)

// targetTrustScore computes the trust score for a TARGET memory.
//
// Formula (D-044, brief 02):
//
//	trust = (0.5 + log1p(use + 2·save)) · source_multiplier · (importance/3)
//
// use and save are the target's utility counters (UseCount, SaveCount).
// trustSource is the target's TrustSource field.
// importance is the target's Importance field (1–5).
//
// This score gates supersede/update decisions: a high-trust target cannot be
// silently overwritten.
func targetTrustScore(use, save int64, trustSource string, importance int) float64 {
	sm, ok := sourceMultiplierMap[trustSource]
	if !ok {
		sm = defaultSourceMultiplier
	}
	return (0.5 + math.Log1p(float64(use)+2*float64(save))) * sm * (float64(importance) / 3.0)
}

// targetTrustLevel returns the TrustLevel for the given memory by evaluating
// its trust score against the gate thresholds.
func targetTrustLevel(m store.Memory) TrustLevel {
	score := targetTrustScore(m.UseCount, m.SaveCount, m.TrustSource, m.Importance)
	switch {
	case score >= trustGatePark:
		return TrustLevelHigh
	case score >= trustGateWarn:
		return TrustLevelMedium
	default:
		return TrustLevelLow
	}
}

// applyContradictionBoost elevates the importance and stability of a
// superseding memory so it immediately outranks the memory it corrected (D-044).
//
//	importance = max(candidateImportance, contradictionBoostImportanceFloor)
//	stability += contradictionBoostStabilityDelta
func applyContradictionBoost(m *store.Memory, candidateImportance int) {
	if candidateImportance > contradictionBoostImportanceFloor {
		m.Importance = candidateImportance
	} else {
		m.Importance = contradictionBoostImportanceFloor
	}
	m.Stability += contradictionBoostStabilityDelta
}
