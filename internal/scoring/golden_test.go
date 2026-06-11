package scoring_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/hurtener/stowage/internal/scoring"
)

// TestGoldenBreakdown pins the exact Breakdown JSON for a fixed, hand-computed
// set of inputs. Any change to the scoring formula or constant values will break
// this test. Update the golden string and the explanatory comment below whenever
// a deliberate formula change is made (record the deviation in docs/decisions.md).
//
// Inputs chosen to produce exact IEEE-754 double values where possible:
//
//	use=3, save=0, inject=3, noise=0
//	→ useBoost = 1 + log2(4) = 3.0            (exact: log2(4)=2.0)
//	→ precision = 0.5 + 3/3 = 1.5             (exact)
//	→ explorationBonus = 1.0 (inject >= 3)     (exact)
//
//	importance=2, trustSource="agent_suggested"
//	→ importanceMult = 0.8 + 0.2 = 1.0        (exact)
//	→ trustMult = 1.0                          (exact)
//
//	Now == LastAccessedAt, ActivityTurns=0
//	→ decayFactor = 1.0                        (exact)
//
//	SameSession=false, HubSignals=0
//	→ cooldown = 1.0, hubDampening = 1.0       (exact)
//
//	FusedScore = 1.0, QueryWindow = nil
//	→ scopeAffinity=1.0, temporalBoost=1.0     (exact)
//
//	finalScore = 1.0 × 3.0 × 1.0 × 1.5 × 1.0 × 1.0 × 1.0 × 1.0 × 1.0 × 1.0 × 1.0 × 1.0
//	           = 4.5                           (exact: 3.0×1.5)
func TestGoldenBreakdown(t *testing.T) {
	t.Parallel()

	const ts = int64(1_700_000_000_000) // arbitrary fixed timestamp

	in := scoring.Inputs{
		Memory: scoring.MemoryFacts{
			UseCount:       3,
			SaveCount:      0,
			InjectCount:    3,
			NoiseCount:     0,
			Importance:     2,
			TrustSource:    "agent_suggested",
			Stability:      1.0,
			CreatedAt:      ts,
			LastAccessedAt: ts,
		},
		FusedScore:    1.0,
		Now:           ts,
		ActivityTurns: 0,
		QueryWindow:   nil,
		SameSession:   false,
		HubSignals:    0,
	}

	score, bd := scoring.Score(in)

	// Verify the key exact values before the JSON comparison.
	checkClose(t, "use_boost", bd.UseBoost, 3.0)
	checkClose(t, "noise_penalty", bd.NoisePenalty, 1.0)
	checkClose(t, "precision_factor", bd.PrecisionFactor, 1.5)
	checkClose(t, "exploration_bonus", bd.ExplorationBonus, 1.0)
	checkClose(t, "decay_factor", bd.DecayFactor, 1.0)
	checkClose(t, "trust_multiplier", bd.TrustMultiplier, 1.0)
	checkClose(t, "scope_affinity", bd.ScopeAffinity, 1.0)
	checkClose(t, "temporal_boost", bd.TemporalBoost, 1.0)
	checkClose(t, "hub_dampening", bd.HubDampening, 1.0)
	checkClose(t, "cooldown", bd.Cooldown, 1.0)
	checkClose(t, "importance_mult", bd.ImportanceMult, 1.0)
	checkClose(t, "final_score", score, 4.5)

	// Golden JSON pin — byte-for-byte comparison to detect formula drift.
	const wantJSON = `{"use_boost":3,"noise_penalty":1,"precision_factor":1.5,"exploration_bonus":1,"decay_factor":1,"trust_multiplier":1,"scope_affinity":1,"temporal_boost":1,"hub_dampening":1,"cooldown":1,"importance_mult":1,"final_score":4.5}`

	gotBytes, err := json.Marshal(bd)
	if err != nil {
		t.Fatalf("json.Marshal(Breakdown): %v", err)
	}
	got := string(gotBytes)
	if got != wantJSON {
		t.Errorf("golden breakdown mismatch:\ngot:  %s\nwant: %s", got, wantJSON)
	}
}

func checkClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("%s: got %.10f want %.10f (diff=%.2e)", name, got, want, math.Abs(got-want))
	}
}
