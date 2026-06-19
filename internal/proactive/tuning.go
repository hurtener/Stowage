package proactive

// tuning.go — feedback-driven per-trigger-class confidence (RFC §6d "accept/dismiss
// tunes per-trigger confidence … triggers that annoy decay; triggers that help gain
// stability"). Deterministic and bounded; no opaque ML.

// minClassMultiplier is the floor a fully-dismissed class decays to. It must be small
// enough that a class the scope keeps dismissing falls below any reasonable threshold,
// but non-zero so a recovering class can climb back with accepts.
const minClassMultiplier = 0.2

// classMultiplier maps a trigger class's accept/dismiss history (within a scope) to a
// confidence multiplier in [minClassMultiplier, 1] applied to that class's candidate
// scores. With no history it is 1 (neutral — new classes get a fair chance). As
// dismissals dominate, it decays toward the floor; accepts pull it back up. Monotonic
// in both inputs, deterministic.
//
// Formula: (accepted + 1) / (accepted + dismissed + 1), clamped to the floor. The +1
// Laplace smoothing keeps a single dismissal from nuking a brand-new class.
func classMultiplier(accepted, dismissed int) float64 {
	if accepted < 0 {
		accepted = 0
	}
	if dismissed < 0 {
		dismissed = 0
	}
	m := float64(accepted+1) / float64(accepted+dismissed+1)
	if m < minClassMultiplier {
		return minClassMultiplier
	}
	if m > 1 {
		return 1
	}
	return m
}
