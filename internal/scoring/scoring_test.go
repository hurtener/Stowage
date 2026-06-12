package scoring_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/scoring"
)

// ── AC-1: Pure-function lint ──────────────────────────────────────────────────

// TestScoringPackageImports asserts that internal/scoring imports neither
// time, math/rand, os, nor any store or gateway package. This enforces the
// pure-function contract required by AC-1 and the phase-10 spec.
//
// Implementation: parses each non-test .go file in the scoring package using
// go/parser.ParseFile (scanning imports only) rather than the deprecated
// parser.ParseDir, which does not consider build tags.
func TestScoringPackageImports(t *testing.T) {
	t.Parallel()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(filename)

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", pkgDir, err)
	}

	forbidden := []string{
		"time",
		"math/rand",
		"os",
		"github.com/hurtener/stowage/internal/store",
		"github.com/hurtener/stowage/internal/gateway",
	}

	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".go" {
			continue
		}
		// Skip test files — they may legitimately import restricted packages.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fullPath := filepath.Join(pkgDir, name)
		file, err := parser.ParseFile(fset, fullPath, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path := ""
			if imp.Path != nil {
				path = imp.Path.Value
				if len(path) >= 2 {
					path = path[1 : len(path)-1]
				}
			}
			for _, bad := range forbidden {
				if path == bad {
					t.Errorf("scoring package imports forbidden package %q in %s", bad, name)
				}
			}
		}
	}
}

// Compile-time check that AST types imported above are referenced.
var _ = ast.File{}

// ── Helper to build a zero-value Inputs with only needed fields overridden ──

func zeroInputs() scoring.Inputs {
	return scoring.Inputs{
		Memory: scoring.MemoryFacts{
			TrustSource: "agent_suggested",
			Stability:   1.0,
		},
		FusedScore: 1.0,
		Now:        1_000_000_000_000, // arbitrary but stable
	}
}

// ── AC-1: Factor table tests ──────────────────────────────────────────────────

func TestUseBoostMonotone(t *testing.T) {
	t.Parallel()
	// More use → higher useBoost → non-lower score (all else equal).
	prev := -1.0
	for use := int64(0); use <= 10; use++ {
		in := zeroInputs()
		in.Memory.UseCount = use
		in.Memory.InjectCount = 20 // fix inject so precision doesn't improve
		score, _ := scoring.Score(in)
		if score < prev {
			t.Errorf("use=%d: score %.6f < prev %.6f (not monotone)", use, score, prev)
		}
		prev = score
	}
}

func TestSaveBoostMonotone(t *testing.T) {
	t.Parallel()
	prev := -1.0
	for save := int64(0); save <= 10; save++ {
		in := zeroInputs()
		in.Memory.SaveCount = save
		in.Memory.UseCount = 0
		in.Memory.InjectCount = 20
		score, _ := scoring.Score(in)
		if score < prev {
			t.Errorf("save=%d: score %.6f < prev %.6f (not monotone)", save, score, prev)
		}
		prev = score
	}
}

func TestNoisePenaltyMonotone(t *testing.T) {
	t.Parallel()
	// More noise → never higher score.
	prev := math.MaxFloat64
	for noise := int64(0); noise <= 20; noise++ {
		in := zeroInputs()
		in.Memory.NoiseCount = noise
		score, _ := scoring.Score(in)
		if score > prev+1e-12 {
			t.Errorf("noise=%d: score %.6f > prev %.6f (not monotone decreasing)", noise, score, prev)
		}
		prev = score
	}
}

func TestNoisePenaltyFloor(t *testing.T) {
	t.Parallel()
	// Even with extreme noise, the penalty factor should not go below 0.40.
	in := zeroInputs()
	in.Memory.NoiseCount = 10000
	_, bd := scoring.Score(in)
	const floor = 0.40
	if bd.NoisePenalty < floor-1e-12 {
		t.Errorf("noise penalty floor violated: got %.6f want >= %.6f", bd.NoisePenalty, floor)
	}
}

func TestPrecisionFactorZombieRange(t *testing.T) {
	t.Parallel()
	// inject>0, use=0 → precision should be at minimum (0.5).
	in := zeroInputs()
	in.Memory.InjectCount = 10
	in.Memory.UseCount = 0
	_, bd := scoring.Score(in)
	if math.Abs(bd.PrecisionFactor-0.5) > 1e-12 {
		t.Errorf("zombie precision: got %.6f want 0.5", bd.PrecisionFactor)
	}
}

func TestPrecisionFactorClamped(t *testing.T) {
	t.Parallel()
	// use >> inject → precision capped at 1.5.
	in := zeroInputs()
	in.Memory.InjectCount = 1
	in.Memory.UseCount = 100
	_, bd := scoring.Score(in)
	if bd.PrecisionFactor > 1.5+1e-12 {
		t.Errorf("precision factor not clamped: got %.6f want <= 1.5", bd.PrecisionFactor)
	}
}

func TestPrecisionFactorUnused(t *testing.T) {
	t.Parallel()
	// inject == 0 → precision == 1.5 (benefit of the doubt).
	in := zeroInputs()
	in.Memory.InjectCount = 0
	in.Memory.UseCount = 0
	_, bd := scoring.Score(in)
	if math.Abs(bd.PrecisionFactor-1.5) > 1e-12 {
		t.Errorf("unused precision: got %.6f want 1.5", bd.PrecisionFactor)
	}
}

func TestExplorationBonusApplied(t *testing.T) {
	t.Parallel()
	// inject < 3 → exploration bonus 1.3.
	for inject := int64(0); inject < 3; inject++ {
		in := zeroInputs()
		in.Memory.InjectCount = inject
		_, bd := scoring.Score(in)
		if math.Abs(bd.ExplorationBonus-1.3) > 1e-12 {
			t.Errorf("inject=%d: exploration bonus %.6f want 1.3", inject, bd.ExplorationBonus)
		}
	}
}

func TestExplorationBonusNotApplied(t *testing.T) {
	t.Parallel()
	// inject >= 3 → no exploration bonus (1.0).
	for inject := int64(3); inject <= 10; inject++ {
		in := zeroInputs()
		in.Memory.InjectCount = inject
		_, bd := scoring.Score(in)
		if math.Abs(bd.ExplorationBonus-1.0) > 1e-12 {
			t.Errorf("inject=%d: exploration bonus %.6f want 1.0", inject, bd.ExplorationBonus)
		}
	}
}

func TestDecayNoElapsed(t *testing.T) {
	t.Parallel()
	// When Now == LastAccessedAt and ActivityTurns == 0, decay == 1.0.
	in := zeroInputs()
	in.Memory.LastAccessedAt = in.Now
	in.ActivityTurns = 0
	_, bd := scoring.Score(in)
	if math.Abs(bd.DecayFactor-1.0) > 1e-12 {
		t.Errorf("zero-elapsed decay: got %.6f want 1.0", bd.DecayFactor)
	}
}

func TestDecayFloorDefault(t *testing.T) {
	t.Parallel()
	// With extreme elapsed time, decay floors at 0.10 for non-user_stated.
	in := zeroInputs()
	in.Memory.TrustSource = "llm_extracted"
	in.Memory.LastAccessedAt = 1
	in.Now = 1 + int64(1000*msPerDayTest) // 1000 days
	in.Memory.Stability = 1.0
	_, bd := scoring.Score(in)
	const floor = 0.10
	if bd.DecayFactor < floor-1e-9 {
		t.Errorf("default decay floor violated: got %.6f want >= %.6f", bd.DecayFactor, floor)
	}
}

func TestDecayFloorUserStated(t *testing.T) {
	t.Parallel()
	// user_stated floor is 0.50, even with extreme elapsed time.
	in := zeroInputs()
	in.Memory.TrustSource = "user_stated"
	in.Memory.LastAccessedAt = 1
	in.Now = 1 + int64(1000*msPerDayTest) // 1000 days
	in.Memory.Stability = 1.0
	_, bd := scoring.Score(in)
	const floor = 0.50
	if bd.DecayFactor < floor-1e-9 {
		t.Errorf("user_stated decay floor violated: got %.6f want >= %.6f", bd.DecayFactor, floor)
	}
}

// msPerDayTest mirrors the package constant for test use.
const msPerDayTest = 86_400_000.0

func TestTrustMultipliers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		source string
		want   float64
	}{
		{"user_stated", 1.25},
		{"agreed_upon", 1.15},
		{"agent_suggested", 1.00},
		{"llm_extracted", 0.95},
		{"", 1.00},         // unknown → neutral
		{"invented", 1.00}, // unknown → neutral
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.source, func(t *testing.T) {
			t.Parallel()
			in := zeroInputs()
			in.Memory.TrustSource = tc.source
			_, bd := scoring.Score(in)
			if math.Abs(bd.TrustMultiplier-tc.want) > 1e-12 {
				t.Errorf("trust=%q: got %.4f want %.4f", tc.source, bd.TrustMultiplier, tc.want)
			}
		})
	}
}

func TestScopeAffinitySameSession(t *testing.T) {
	t.Parallel()
	in := zeroInputs()
	in.SameSession = true
	_, bd := scoring.Score(in)
	if math.Abs(bd.ScopeAffinity-1.3) > 1e-12 {
		t.Errorf("same-session affinity: got %.4f want 1.3", bd.ScopeAffinity)
	}
}

func TestScopeAffinityDifferentSession(t *testing.T) {
	t.Parallel()
	in := zeroInputs()
	in.SameSession = false
	_, bd := scoring.Score(in)
	if math.Abs(bd.ScopeAffinity-1.0) > 1e-12 {
		t.Errorf("different-session affinity: got %.4f want 1.0", bd.ScopeAffinity)
	}
}

func TestTemporalBoostNilWindow(t *testing.T) {
	t.Parallel()
	in := zeroInputs()
	in.QueryWindow = nil
	_, bd := scoring.Score(in)
	if math.Abs(bd.TemporalBoost-1.0) > 1e-12 {
		t.Errorf("nil window temporal boost: got %.4f want 1.0", bd.TemporalBoost)
	}
}

func TestTemporalBoostInsideCenteredWindow(t *testing.T) {
	t.Parallel()
	// Memory created at exact center of window → full 1.2 boost.
	center := int64(1_000_000_000_000)
	radius := int64(5_000_000)
	in := zeroInputs()
	in.Memory.CreatedAt = center
	in.QueryWindow = &scoring.Window{From: center - radius, Until: center + radius}
	_, bd := scoring.Score(in)
	if math.Abs(bd.TemporalBoost-1.2) > 1e-9 {
		t.Errorf("centered temporal boost: got %.6f want 1.2", bd.TemporalBoost)
	}
}

func TestTemporalBoostOutsideWindow(t *testing.T) {
	t.Parallel()
	// Memory outside window → no boost (1.0), not penalty.
	center := int64(1_000_000_000_000)
	radius := int64(5_000_000)
	in := zeroInputs()
	in.Memory.CreatedAt = center + 2*radius // well outside
	in.QueryWindow = &scoring.Window{From: center - radius, Until: center + radius}
	_, bd := scoring.Score(in)
	if math.Abs(bd.TemporalBoost-1.0) > 1e-9 {
		t.Errorf("outside-window temporal boost: got %.6f want 1.0", bd.TemporalBoost)
	}
}

func TestTemporalBoostFromOnly(t *testing.T) {
	t.Parallel()
	// Only From set: memory at or after From gets 1.2 boost.
	from := int64(1_000_000_000_000)
	in := zeroInputs()
	in.Memory.CreatedAt = from + 1
	in.QueryWindow = &scoring.Window{From: from}
	_, bd := scoring.Score(in)
	if math.Abs(bd.TemporalBoost-1.2) > 1e-9 {
		t.Errorf("from-only temporal boost: got %.6f want 1.2", bd.TemporalBoost)
	}
}

func TestHubDampeningThreshold(t *testing.T) {
	t.Parallel()
	// Below threshold: no dampening.
	for signals := 0; signals < 4; signals++ {
		in := zeroInputs()
		in.HubSignals = signals
		_, bd := scoring.Score(in)
		if math.Abs(bd.HubDampening-1.0) > 1e-12 {
			t.Errorf("hub signals=%d: dampening %.4f want 1.0", signals, bd.HubDampening)
		}
	}
	// At and above threshold: 0.8.
	for signals := 4; signals <= 8; signals++ {
		in := zeroInputs()
		in.HubSignals = signals
		_, bd := scoring.Score(in)
		if math.Abs(bd.HubDampening-0.8) > 1e-12 {
			t.Errorf("hub signals=%d: dampening %.4f want 0.8", signals, bd.HubDampening)
		}
	}
}

func TestCooldownSameSessionFresh(t *testing.T) {
	t.Parallel()
	// SameSession + memory created 5 min ago → cooldown 0.1.
	now := int64(1_000_000_000_000)
	in := zeroInputs()
	in.Now = now
	in.Memory.CreatedAt = now - 5*60*1000 // 5 minutes ago
	in.SameSession = true
	_, bd := scoring.Score(in)
	if math.Abs(bd.Cooldown-0.1) > 1e-12 {
		t.Errorf("same-session fresh cooldown: got %.4f want 0.1", bd.Cooldown)
	}
}

func TestCooldownSameSessionOld(t *testing.T) {
	t.Parallel()
	// SameSession + memory created 2 hours ago → no cooldown (1.0).
	now := int64(1_000_000_000_000)
	in := zeroInputs()
	in.Now = now
	in.Memory.CreatedAt = now - 120*60*1000 // 2 hours ago
	in.SameSession = true
	_, bd := scoring.Score(in)
	if math.Abs(bd.Cooldown-1.0) > 1e-12 {
		t.Errorf("same-session old cooldown: got %.4f want 1.0", bd.Cooldown)
	}
}

func TestCooldownDifferentSession(t *testing.T) {
	t.Parallel()
	// Different session: no cooldown regardless of age.
	now := int64(1_000_000_000_000)
	in := zeroInputs()
	in.Now = now
	in.Memory.CreatedAt = now - 60*1000 // 1 minute ago — very fresh
	in.SameSession = false
	_, bd := scoring.Score(in)
	if math.Abs(bd.Cooldown-1.0) > 1e-12 {
		t.Errorf("different-session cooldown: got %.4f want 1.0", bd.Cooldown)
	}
}

func TestImportanceMult(t *testing.T) {
	t.Parallel()
	cases := []struct {
		importance int
		want       float64
	}{
		{0, 0.8},
		{1, 0.9},
		{5, 1.3},
		{10, 1.8},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("", func(t *testing.T) {
			t.Parallel()
			in := zeroInputs()
			in.Memory.Importance = tc.importance
			_, bd := scoring.Score(in)
			if math.Abs(bd.ImportanceMult-tc.want) > 1e-9 {
				t.Errorf("importance=%d: got %.4f want %.4f", tc.importance, bd.ImportanceMult, tc.want)
			}
		})
	}
}

// ── AC-3: Property tests ──────────────────────────────────────────────────────

// TestPropertyMoreUseNeverLower verifies that increasing use_count never
// decreases the score when inject_count is held constant (all else equal).
// Note: the exploration bonus transitions at inject<3, so this test holds
// inject fixed at values that don't straddle the threshold.
func TestPropertyMoreUseNeverLower(t *testing.T) {
	t.Parallel()
	// inject fixed above threshold: only useBoost and precisionFactor vary.
	seeds := []struct{ save, inject, noise int64 }{
		{0, 5, 0},  // inject=5 (above threshold), no noise
		{2, 10, 1}, // save present, high inject, some noise
		{1, 20, 2}, // high inject, multiple saves
		{0, 0, 0},  // inject=0: precisionFactor fixed at 1.5, exploration 1.3; only useBoost varies
	}
	for _, s := range seeds {
		prev := -1.0
		for use := int64(0); use <= 20; use++ {
			in := zeroInputs()
			in.Memory.UseCount = use
			in.Memory.SaveCount = s.save
			in.Memory.InjectCount = s.inject // FIXED inject
			in.Memory.NoiseCount = s.noise
			score, _ := scoring.Score(in)
			if score < prev-1e-12 {
				t.Errorf("more use violated: save=%d inject=%d noise=%d use=%d score=%.6f < prev=%.6f",
					s.save, s.inject, s.noise, use, score, prev)
			}
			prev = score
		}
	}
}

// TestPropertyMoreNoiseNeverHigher verifies that increasing noise_count never
// increases the score (all other factors equal).
func TestPropertyMoreNoiseNeverHigher(t *testing.T) {
	t.Parallel()
	seeds := []struct{ use, save, inject int64 }{
		{0, 0, 0},
		{3, 1, 5},
		{0, 0, 3},
	}
	for _, s := range seeds {
		prev := math.MaxFloat64
		for noise := int64(0); noise <= 30; noise++ {
			in := zeroInputs()
			in.Memory.UseCount = s.use
			in.Memory.SaveCount = s.save
			in.Memory.InjectCount = s.inject
			in.Memory.NoiseCount = noise
			score, _ := scoring.Score(in)
			if score > prev+1e-12 {
				t.Errorf("more noise violated: use=%d save=%d inject=%d noise=%d score=%.6f > prev=%.6f",
					s.use, s.save, s.inject, noise, score, prev)
			}
			prev = score
		}
	}
}

// TestPropertyDecayFloorAlwaysRespected runs 1000 random-seeded scenarios and
// verifies the decay floor is never violated.
func TestPropertyDecayFloorAlwaysRespected(t *testing.T) {
	t.Parallel()
	// Fixed-seed loop: deterministic, not truly random (no math/rand import in
	// scoring; we use a simple counter here in the test).
	for i := 0; i < 1000; i++ {
		in := zeroInputs()
		in.Memory.Stability = float64(1 + i%20)
		in.Memory.UseCount = int64(i % 7)
		in.Memory.LastAccessedAt = 1
		in.Now = 1 + int64((i+1))*100_000_000 // increasing elapsed
		in.ActivityTurns = int64(i % 30)

		trust := "llm_extracted"
		floor := 0.10
		if i%3 == 0 {
			trust = "user_stated"
			floor = 0.50
		}
		in.Memory.TrustSource = trust

		_, bd := scoring.Score(in)
		if bd.DecayFactor < floor-1e-9 {
			t.Errorf("i=%d trust=%s: decay %.6f < floor %.2f", i, trust, bd.DecayFactor, floor)
		}
	}
}

// ── AC-4: Zombie vs fresh ─────────────────────────────────────────────────────

// TestZombieVsFresh verifies that a high-inject/zero-use memory (zombie) ranks
// below a low-inject/high-use memory (fresh), holding all other facts constant.
// This is the brief-02 signature test.
func TestZombieVsFresh(t *testing.T) {
	t.Parallel()
	base := zeroInputs()
	base.Memory.Stability = 1.0
	base.Memory.TrustSource = "llm_extracted"
	base.Memory.LastAccessedAt = base.Now

	zombie := base
	zombie.Memory.InjectCount = 10
	zombie.Memory.UseCount = 0
	zombie.Memory.SaveCount = 0

	fresh := base
	fresh.Memory.InjectCount = 2
	fresh.Memory.UseCount = 5
	fresh.Memory.SaveCount = 1

	zombieScore, _ := scoring.Score(zombie)
	freshScore, _ := scoring.Score(fresh)

	if zombieScore >= freshScore {
		t.Errorf("zombie (inject=10,use=0) score %.6f >= fresh (inject=2,use=5,save=1) score %.6f; expected zombie < fresh",
			zombieScore, freshScore)
	}
}

// ── AC-5: Cooldown is per-session ─────────────────────────────────────────────

// TestCooldownSuppressesOnlyOriginSession verifies that the write-echo cooldown
// applies for SameSession=true but not for SameSession=false, with identical
// memory facts and timing.
func TestCooldownSuppressesOnlyOriginSession(t *testing.T) {
	t.Parallel()
	now := int64(1_000_000_000_000)
	created := now - 5*60*1000 // 5 min ago (inside 30-min window)

	inOrigin := zeroInputs()
	inOrigin.Now = now
	inOrigin.Memory.CreatedAt = created
	inOrigin.SameSession = true

	inOther := inOrigin
	inOther.SameSession = false

	originScore, bdOrigin := scoring.Score(inOrigin)
	otherScore, bdOther := scoring.Score(inOther)

	if math.Abs(bdOrigin.Cooldown-0.1) > 1e-12 {
		t.Errorf("origin session cooldown: got %.4f want 0.1", bdOrigin.Cooldown)
	}
	if math.Abs(bdOther.Cooldown-1.0) > 1e-12 {
		t.Errorf("other session cooldown: got %.4f want 1.0", bdOther.Cooldown)
	}
	if originScore >= otherScore {
		t.Errorf("origin score %.6f >= other score %.6f; cooldown not applied", originScore, otherScore)
	}
}

// ── AC-6: Hub dampening ───────────────────────────────────────────────────────

// TestHubDampeningAppliesAt4(t) is covered by TestHubDampeningThreshold above.

// ── DecayFactor + DecayFloorFor (Phase 14 exports) ───────────────────────────

// TestDecayFactorMatchesScore verifies that scoring.DecayFactor returns the same
// decay component as the DecayFactor in the Score Breakdown (AC: pure-function
// consistency, Phase 14 export).
func TestDecayFactorMatchesScore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		trustSource    string
		stability      float64
		lastAccessedAt int64
		nowMs          int64
		activityTurns  int64
	}{
		{
			name:           "zero elapsed",
			trustSource:    "llm_extracted",
			stability:      1.0,
			lastAccessedAt: 1_000_000_000_000,
			nowMs:          1_000_000_000_000,
			activityTurns:  0,
		},
		{
			name:           "old memory llm_extracted",
			trustSource:    "llm_extracted",
			stability:      1.0,
			lastAccessedAt: 1,
			nowMs:          1 + int64(100*msPerDayTest),
			activityTurns:  0,
		},
		{
			name:           "old memory user_stated",
			trustSource:    "user_stated",
			stability:      1.0,
			lastAccessedAt: 1,
			nowMs:          1 + int64(100*msPerDayTest),
			activityTurns:  0,
		},
		{
			name:           "with activity turns",
			trustSource:    "agent_suggested",
			stability:      5.0,
			lastAccessedAt: 1_000_000_000_000,
			nowMs:          1_000_000_000_000 + int64(10*msPerDayTest),
			activityTurns:  20,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := scoring.MemoryFacts{
				TrustSource:    tc.trustSource,
				Stability:      tc.stability,
				LastAccessedAt: tc.lastAccessedAt,
			}
			// Get the decay factor from the standalone function.
			df := scoring.DecayFactor(facts, tc.nowMs, tc.activityTurns)

			// Get the decay factor from Score's breakdown (zero fused score → breakdown still valid).
			in := scoring.Inputs{
				Memory:        facts,
				FusedScore:    1.0,
				Now:           tc.nowMs,
				ActivityTurns: tc.activityTurns,
			}
			_, bd := scoring.Score(in)

			if math.Abs(df-bd.DecayFactor) > 1e-12 {
				t.Errorf("DecayFactor=%v != Score.Breakdown.DecayFactor=%v", df, bd.DecayFactor)
			}
		})
	}
}

func TestDecayFactorZeroStabilityDefaults(t *testing.T) {
	t.Parallel()
	// Stability <= 0 should default to 1.0 (defensive).
	facts := scoring.MemoryFacts{
		TrustSource:    "llm_extracted",
		Stability:      0, // zero stability
		LastAccessedAt: 1,
	}
	df := scoring.DecayFactor(facts, 1+int64(1*msPerDayTest), 0)
	// Should still be >= decayFloorDefault (0.10), not NaN, not negative.
	if df < 0.10-1e-9 {
		t.Errorf("zero-stability decay factor %.6f < floor 0.10", df)
	}
}

func TestDecayFloorFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		trustSource string
		wantFloor   float64
	}{
		{"user_stated", 0.50},
		{"llm_extracted", 0.10},
		{"agent_suggested", 0.10},
		{"agreed_upon", 0.10},
		{"", 0.10},
		{"unknown", 0.10},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.trustSource, func(t *testing.T) {
			t.Parallel()
			got := scoring.DecayFloorFor(tc.trustSource)
			if math.Abs(got-tc.wantFloor) > 1e-12 {
				t.Errorf("DecayFloorFor(%q)=%.2f, want %.2f", tc.trustSource, got, tc.wantFloor)
			}
		})
	}
}

// ── FinalScore decomposition ──────────────────────────────────────────────────

// TestFinalScoreProductCheck verifies that FinalScore == FusedScore × product
// of all factors in Breakdown.
func TestFinalScoreProductCheck(t *testing.T) {
	t.Parallel()
	now := int64(1_000_000_000_000)
	in := scoring.Inputs{
		Memory: scoring.MemoryFacts{
			UseCount:       3,
			SaveCount:      1,
			InjectCount:    5,
			NoiseCount:     2,
			Importance:     4,
			TrustSource:    "agreed_upon",
			Stability:      10.0,
			CreatedAt:      now - 24*60*60*1000,
			LastAccessedAt: now - 60*60*1000,
		},
		FusedScore:    0.08,
		Now:           now,
		ActivityTurns: 5,
		QueryWindow:   &scoring.Window{From: now - 2*24*60*60*1000, Until: now},
		SameSession:   false,
		HubSignals:    2,
	}
	score, bd := scoring.Score(in)

	product := in.FusedScore *
		bd.UseBoost *
		bd.NoisePenalty *
		bd.PrecisionFactor *
		bd.ExplorationBonus *
		bd.DecayFactor *
		bd.TrustMultiplier *
		bd.ScopeAffinity *
		bd.TemporalBoost *
		bd.HubDampening *
		bd.Cooldown *
		bd.ImportanceMult

	if math.Abs(score-product) > 1e-12 {
		t.Errorf("score decomposition mismatch: score=%.10f product=%.10f diff=%.2e",
			score, product, math.Abs(score-product))
	}
	if math.Abs(bd.FinalScore-score) > 1e-12 {
		t.Errorf("Breakdown.FinalScore=%.10f != returned score=%.10f", bd.FinalScore, score)
	}
}
