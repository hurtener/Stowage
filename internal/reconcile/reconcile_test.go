package reconcile_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register driver
)

// --- test helpers -----------------------------------------------------------

func newTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	cfg := config.StoreConfig{Driver: "sqlite", DSN: dsn}
	s, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		_ = s.Close(context.Background())
		t.Fatalf("migrate: %v", err)
	}
	return s, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	}
}

// stubGateway is a minimal gateway.Gateway for tests.
type stubGateway struct {
	responses []gateway.CompleteResponse
	errs      []error
	calls     int
}

func (g *stubGateway) Complete(_ context.Context, _ gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	i := g.calls
	g.calls++
	if i < len(g.errs) && g.errs[i] != nil {
		return gateway.CompleteResponse{}, g.errs[i]
	}
	if i < len(g.responses) {
		return g.responses[i], nil
	}
	return gateway.CompleteResponse{JSON: json.RawMessage(`{"action":"add","reason":"new information"}`)}, nil
}

func (g *stubGateway) Embed(_ context.Context, _ gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (g *stubGateway) Rerank(_ context.Context, _ gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}
func (g *stubGateway) Probe(_ context.Context) error { return nil }
func (g *stubGateway) Close(_ context.Context) error { return nil }

func newCandidate(kind, content string, importance int, confidence float64, entities ...string) pipeline.Candidate {
	ents := entities
	if len(ents) == 0 {
		ents = []string{"entity-" + kind}
	}
	return pipeline.Candidate{
		Kind:       kind,
		Content:    content,
		Importance: importance,
		Confidence: confidence,
		Entities:   ents,
		Keywords:   []string{"kw-" + kind},
		// No provenance: avoids FK constraint violation (records table is empty in tests).
	}
}

func tenantScope(t string) identity.Scope { return identity.Scope{Tenant: t} }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 4}))
}

func runStage(t *testing.T, mem store.MemoryStore, ops store.OpsStore, evts store.EventStore, gw gateway.Gateway, batch pipeline.CandidateBatch) {
	t.Helper()
	ch := make(chan pipeline.CandidateBatch, 1)
	ch <- batch
	close(ch)
	stage := reconcile.New(mem, ops, evts, gw, discardLogger(), ch)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stage.Start(ctx)
	stage.Drain(ctx)
}

// stubScopeInvalidator is a minimal ScopeInvalidator for tests.
type stubScopeInvalidator struct {
	count int
}

func (s *stubScopeInvalidator) InvalidateScope(_ identity.Scope) {
	s.count++
}

func runStageWithInvalidator(t *testing.T, mem store.MemoryStore, ops store.OpsStore, evts store.EventStore, gw gateway.Gateway, inv reconcile.ScopeInvalidator, batch pipeline.CandidateBatch) {
	t.Helper()
	ch := make(chan pipeline.CandidateBatch, 1)
	ch <- batch
	close(ch)
	stage := reconcile.New(mem, ops, evts, gw, discardLogger(), ch)
	stage.SetScopeInvalidator(inv)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stage.Start(ctx)
	stage.Drain(ctx)
}

// insertTestMemory inserts a memory with junction rows via Commit.
// The memory ID must be pre-set in mem.ID.
func insertTestMemory(t *testing.T, st store.Store, scope identity.Scope, mem store.Memory, entities, keywords []string) {
	t.Helper()
	cs := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   mem,
		Entities: entities,
		Keywords: keywords,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("insertTestMemory: %v", err)
	}
}

// eventsByType returns all events of the given type in scope.
func eventsByType(t *testing.T, st store.Store, scope identity.Scope, typ string) []store.Event {
	t.Helper()
	evts, _, err := st.Events().List(context.Background(), scope, 200, "")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []store.Event
	for _, e := range evts {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// --- AC unit-level tests (prefilter functions) --------------------------------

func TestNormalizeContent(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"  hello   world  ", "hello world"},
		{"Go\tis\nfast", "Go is fast"},
		{"Python", "Python"}, // case preserved: "Python" ≠ "python"; case changes meaning
		{"python", "python"},
		{"", ""},
		{"  ", ""},
		{"multiple   spaces", "multiple spaces"},
	}
	for _, tc := range cases {
		got := reconcile.NormalizeContent(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeContent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestContentHashDeterministic(t *testing.T) {
	h1 := reconcile.ContentHash("Go is fast")
	h2 := reconcile.ContentHash("Go is fast")
	if h1 != h2 {
		t.Errorf("hash not deterministic: %q != %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("hash length: got %d want 64", len(h1))
	}
	// Case sensitivity: "Python" and "python" must hash differently because
	// case changes meaning (e.g. the Python language vs a python snake).
	hp := reconcile.ContentHash("Python")
	hpl := reconcile.ContentHash("python")
	if hp == hpl {
		t.Error("case sensitive: Python and python should produce different hashes")
	}
}

func TestBigramJaccard(t *testing.T) {
	if j := reconcile.BigramJaccard("hello", "hello"); j != 1.0 {
		t.Errorf("identical strings: got %v want 1.0", j)
	}
	if j := reconcile.BigramJaccard("", ""); j != 1.0 {
		t.Errorf("empty strings: got %v want 1.0", j)
	}
	if j := reconcile.BigramJaccard("hello", ""); j != 0.0 {
		t.Errorf("one empty: got %v want 0.0", j)
	}
	if j := reconcile.BigramJaccard("abc", "xyz"); j != 0.0 {
		t.Errorf("disjoint: got %v want 0.0", j)
	}
	j := reconcile.BigramJaccard("abcde", "abcxy")
	if j <= 0 || j >= 1 {
		t.Errorf("partial overlap: got %v want (0,1)", j)
	}
	// Near-dup threshold test: highly similar strings must exceed 0.85.
	j2 := reconcile.BigramJaccard("Python was created by Guido", "Python was created by Guido in")
	if j2 < reconcile.ExportNearDupThreshold {
		t.Errorf("near-dup pair: Jaccard = %.3f, want ≥ %.2f", j2, reconcile.ExportNearDupThreshold)
	}
}

// TestNumeralsDiverge covers the D-104 numeric-correction guard: a numeric correction
// that is lexically a near-dup must be flagged as divergent so it routes to the LLM
// (supersede) instead of being auto-discarded.
func TestNumeralsDiverge(t *testing.T) {
	cases := []struct {
		name, a, b string
		want       bool
	}{
		{"same numerals", "120 stars to reach gold", "120 stars to reach gold", false},
		{"no numerals", "the user likes tea", "the user likes coffee", false},
		{"stars correction", "You need 120 stars for gold level", "You need 125 stars for gold level", true},
		{"months correction", "Fitbit Charge 3 used for 9 months", "Fitbit Charge 3 used for 6 months", true},
		{"thousands separator equal", "raised $5,850 total", "raised $5850 total", false},
		{"reorder same set", "2 cats and 3 dogs", "3 dogs and 2 cats", false},
		{"extra numeral", "I have 2 cats", "I have 2 cats and 1 dog", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconcile.NumeralsDiverge(tc.a, tc.b); got != tc.want {
				t.Errorf("NumeralsDiverge(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
	// Invariant: a numeric correction is BOTH a lexical near-dup AND numeral-divergent,
	// so the guard fires exactly where the auto-discard would otherwise swallow it.
	a, b := "You need 120 stars for gold level", "You need 125 stars for gold level"
	if reconcile.BigramJaccard(a, b) < reconcile.ExportNearDupThreshold {
		t.Fatalf("precondition: star correction is not a near-dup (Jaccard %.3f)", reconcile.BigramJaccard(a, b))
	}
	if !reconcile.NumeralsDiverge(a, b) {
		t.Fatalf("star correction must be numeral-divergent so the guard routes it to the LLM")
	}
}

// TestCandidateAssertionOrdering proves the D-106 winner-determinism fix: within a flush,
// candidates are processed oldest-asserted first (by latest source-record ULID = turn
// order), so the newer value supersedes the older — never the reverse. Records are ULIDs;
// here we use lexically-ordered stand-ins.
func TestCandidateAssertionOrdering(t *testing.T) {
	older := pipeline.Candidate{Content: "6 months", Provenance: []pipeline.ProvSpan{{RecordID: "01A"}, {RecordID: "01B"}}}
	newer := pipeline.Candidate{Content: "9 months", Provenance: []pipeline.ProvSpan{{RecordID: "01C"}}}

	// Key = latest (max) record among provenance.
	if k := reconcile.ExportCandidateAssertionKey(older); k != "01B" {
		t.Errorf("older key = %q, want 01B (latest of its records)", k)
	}
	if k := reconcile.ExportCandidateAssertionKey(newer); k != "01C" {
		t.Errorf("newer key = %q, want 01C", k)
	}

	// Even when the LLM emits newest-first, the stable sort puts the older assertion first
	// so it commits before the newer one supersedes it.
	cands := []pipeline.Candidate{newer, older}
	sort.SliceStable(cands, func(i, j int) bool {
		return reconcile.ExportCandidateAssertionKey(cands[i]) < reconcile.ExportCandidateAssertionKey(cands[j])
	})
	if cands[0].Content != "6 months" || cands[1].Content != "9 months" {
		t.Fatalf("ordering = [%q,%q], want [6 months, 9 months]", cands[0].Content, cands[1].Content)
	}
}

// --- AC-4: Trust gate formula ------------------------------------------------

// TestTrustGateFormula verifies the three trust levels against known memory
// configurations. The spec formula is:
//
//	trust = (0.5 + log1p(use + 2·save)) · source_multiplier · (importance/3)
func TestTrustGateFormula(t *testing.T) {
	cases := []struct {
		name      string
		mem       store.Memory
		wantLevel reconcile.TrustLevel
	}{
		{
			name: "low: use=0 save=0 llm_extracted importance=3",
			mem:  store.Memory{UseCount: 0, SaveCount: 0, TrustSource: "llm_extracted", Importance: 3},
			// score = (0.5 + log1p(0)) * 0.7 * 1.0 = 0.35 < 1.0
			wantLevel: reconcile.ExportTrustLevelLow,
		},
		{
			name: "medium: use=5 save=2 llm_extracted importance=3",
			mem:  store.Memory{UseCount: 5, SaveCount: 2, TrustSource: "llm_extracted", Importance: 3},
			// score = (0.5 + log1p(9)) * 0.7 * 1.0 ≈ 2.80 * 0.7 ≈ 1.96 ∈ [1,3)
			wantLevel: reconcile.ExportTrustLevelMedium,
		},
		{
			name: "high: use=10 save=5 user_stated importance=5",
			mem:  store.Memory{UseCount: 10, SaveCount: 5, TrustSource: "user_stated", Importance: 5},
			// score = (0.5 + log1p(20)) * 2.0 * (5/3) ≈ 3.545 * 3.333 ≈ 11.8 ≥ 3.0
			wantLevel: reconcile.ExportTrustLevelHigh,
		},
		{
			// Covers the defaultSourceMultiplier path in targetTrustScore.
			name: "unknown trust_source uses default multiplier",
			mem:  store.Memory{UseCount: 0, SaveCount: 0, TrustSource: "unknown_source", Importance: 3},
			// score = (0.5 + log1p(0)) * 1.0 * 1.0 = 0.5 < 1.0
			wantLevel: reconcile.ExportTrustLevelLow,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reconcile.ExportTargetTrustLevel(tc.mem)
			if got != tc.wantLevel {
				t.Errorf("targetTrustLevel = %d, want %d", got, tc.wantLevel)
			}
		})
	}
}

// --- Decision validation -----------------------------------------------------

func TestValidateDecision(t *testing.T) {
	validCases := []struct {
		name string
		d    reconcile.DecisionOutput
	}{
		{"add", reconcile.DecisionOutput{Action: "add"}},
		{"discard", reconcile.DecisionOutput{Action: "discard"}},
		{"park", reconcile.DecisionOutput{Action: "park"}},
		// M5: update and merge require non-empty content.
		{"update", reconcile.DecisionOutput{Action: "update", Content: "refined content", TargetIDs: []string{"id1"}}},
		{"supersede", reconcile.DecisionOutput{Action: "supersede", TargetIDs: []string{"id1"}}},
		{"merge", reconcile.DecisionOutput{Action: "merge", Content: "merged content", TargetIDs: []string{"id1", "id2"}}},
		// m10: duplicate target_ids are silently deduplicated.
		{"merge-dedup", reconcile.DecisionOutput{Action: "merge", Content: "merged content", TargetIDs: []string{"id1", "id2", "id1"}}},
	}
	for _, tc := range validCases {
		if err := reconcile.ExportValidateDecision(tc.d); err != nil {
			t.Errorf("valid case %q: unexpected error: %v", tc.name, err)
		}
	}

	errorCases := []struct {
		name string
		d    reconcile.DecisionOutput
	}{
		{"unknown action", reconcile.DecisionOutput{Action: "explode"}},
		{"update no target", reconcile.DecisionOutput{Action: "update", Content: "c"}},
		{"update no content", reconcile.DecisionOutput{Action: "update", TargetIDs: []string{"id1"}}},
		{"supersede no target", reconcile.DecisionOutput{Action: "supersede"}},
		{"merge one target", reconcile.DecisionOutput{Action: "merge", Content: "c", TargetIDs: []string{"id1"}}},
		{"merge no content", reconcile.DecisionOutput{Action: "merge", TargetIDs: []string{"id1", "id2"}}},
	}
	for _, tc := range errorCases {
		if err := reconcile.ExportValidateDecision(tc.d); err == nil {
			t.Errorf("error case %q: expected error, got nil", tc.name)
		}
	}
}

// --- AC-2: Fast-add path (zero neighbors → no gateway call) ------------------

func TestStageFastAdd(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-fastadd-" + t.Name())

	gw := &stubGateway{} // should NOT be called
	batch := pipeline.CandidateBatch{
		Scope:      scope,
		BufferKey:  "buf-fastadd",
		Candidates: []pipeline.Candidate{newCandidate("fact", "Go is a compiled language", 4, 0.9)},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	mems, _, err := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(mems) != 1 {
		t.Errorf("fast-add: got %d active memories want 1", len(mems))
	}
	if len(mems) > 0 && mems[0].Content != "Go is a compiled language" {
		t.Errorf("content: got %q", mems[0].Content)
	}
	if gw.calls != 0 {
		t.Errorf("fast-add: gateway called %d times; want 0 (no neighbors → no LLM)", gw.calls)
	}
}

// --- AC-1: Exact-dedup bumps match_count + no second memory ------------------

func TestStageExactDuplicate(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-dup-" + t.Name())

	content := "Go is a compiled language"
	normalized := reconcile.NormalizeContent(content)
	hash := reconcile.ContentHash(normalized)

	mem := store.Memory{
		ID: "existing-1", Kind: "fact", Content: normalized,
		Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: hash,
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	if err := st.Memories().Insert(ctx, scope, mem); err != nil {
		t.Fatalf("Insert existing: %v", err)
	}

	gw := &stubGateway{} // should not be called
	batch := pipeline.CandidateBatch{
		Scope:      scope,
		Candidates: []pipeline.Candidate{newCandidate("fact", content, 4, 0.9)},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	mems, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if len(mems) != 1 {
		t.Errorf("exact dup: got %d active memories want 1 (original only)", len(mems))
	}
	if gw.calls != 0 {
		t.Errorf("gateway called %d times on exact dup; want 0", gw.calls)
	}

	// AC-1: match_count must be incremented (spec: "both bump match_count").
	updated, err := st.Memories().Get(ctx, scope, "existing-1")
	if err != nil {
		t.Fatalf("Get after dup: %v", err)
	}
	if updated.MatchCount != 1 {
		t.Errorf("exact dup: match_count = %d, want 1", updated.MatchCount)
	}

	// reconcile.dedup_exact event must be emitted.
	evts := eventsByType(t, st, scope, "reconcile.dedup_exact")
	if len(evts) == 0 {
		t.Error("exact dup: no reconcile.dedup_exact event found")
	}
}

// --- AC-1: Near-dup pre-filter bumps match_count + emits dedup_near event ----

func TestStageNearDupPreFilter(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-neardup-" + t.Name())

	// C1 and C2 differ by a few characters at the end but share the same
	// entity so FindNeighbors returns C1 when C2 is ingested.
	// BigramJaccard("python was created by guido", "python was created by guido in") ≈ 0.897 ≥ 0.85
	c1Content := "Python was created by Guido"
	c2Content := "Python was created by Guido in"
	entity := "near-dup-entity-" + t.Name()

	// Verify the near-dup invariant holds before the actual test assertion.
	j := reconcile.BigramJaccard(
		reconcile.NormalizeContent(c1Content),
		reconcile.NormalizeContent(c2Content),
	)
	if j < reconcile.ExportNearDupThreshold {
		t.Fatalf("test precondition failed: Jaccard %.3f < threshold %.2f", j, reconcile.ExportNearDupThreshold)
	}

	targetMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: c1Content,
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(c1Content)),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, targetMem, []string{entity}, []string{"kw-near"})

	gw := &stubGateway{} // must not be called (near-dup fires before LLM)
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			newCandidate("fact", c2Content, 4, 0.9, entity),
		},
	}
	// Rebuild candidate with correct keywords too.
	batch.Candidates[0].Keywords = []string{"kw-near"}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// No new memory should have been created.
	mems, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if len(mems) != 1 {
		t.Errorf("near-dup: got %d active memories want 1 (original only)", len(mems))
	}

	// Gateway must not have been called.
	if gw.calls != 0 {
		t.Errorf("near-dup: gateway called %d times; want 0", gw.calls)
	}

	// AC-1: match_count must be incremented.
	updated, err := st.Memories().Get(ctx, scope, targetMem.ID)
	if err != nil {
		t.Fatalf("Get after near-dup: %v", err)
	}
	if updated.MatchCount != 1 {
		t.Errorf("near-dup: match_count = %d, want 1", updated.MatchCount)
	}

	// reconcile.dedup_near event must be emitted.
	evts := eventsByType(t, st, scope, "reconcile.dedup_near")
	if len(evts) == 0 {
		t.Error("near-dup: no reconcile.dedup_near event found")
	}
}

// --- AC-3: Non-neighbor target_id degrades to add ----------------------------

// TestStageNonNeighborTargetDegrades verifies that a decision targeting a
// memory ID not in the shown neighbors is rejected and demoted to add.
// The model must never touch a memory it was not shown (D-045 safety net).
func TestStageNonNeighborTargetDegrades(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-nonneighbor-" + t.Name())
	entity := "non-neighbor-entity-" + t.Name()

	// Insert a neighbor so FindNeighbors is non-empty and the LLM is called.
	neighborMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: "A known fact about Go",
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash("A known fact about Go"),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, neighborMem, []string{entity}, []string{"kw-nonneighbor"})

	// Script gateway to supersede a non-existent (unseen) memory ID.
	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"supersede","target_ids":["does-not-exist"],"reason":"contradiction"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "A different fact about Go", Importance: 3, Confidence: 0.8,
				Entities: []string{entity}, Keywords: []string{"kw-nonneighbor"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// The neighbor must NOT have been superseded.
	neighbor, err := st.Memories().Get(ctx, scope, neighborMem.ID)
	if err != nil {
		t.Fatalf("Get neighbor: %v", err)
	}
	if neighbor.Status != "active" {
		t.Errorf("non-neighbor target: neighbor status = %q, want active (must not be touched)", neighbor.Status)
	}

	// A new memory should have been added (degraded to add).
	mems, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if len(mems) != 2 {
		t.Errorf("non-neighbor target: got %d active memories, want 2 (original + new add)", len(mems))
	}
}

// --- AC-4: Trust gate matrix -------------------------------------------------

// TestTrustGateMatrix exercises all three trust levels:
//   - Low  (score < 1.0): supersede applied silently.
//   - Medium (1.0 ≤ score < 3.0): supersede applied + reconcile.warned event.
//   - High (score ≥ 3.0): new memory parked; target stays active.
func TestTrustGateMatrix(t *testing.T) {
	cases := []struct {
		name       string
		target     store.Memory // pre-inserted target
		wantParked bool         // true → new memory pending_confirmation, target active
		wantWarn   bool         // true → reconcile.warned event emitted
	}{
		{
			name: "low trust: applied silently",
			target: store.Memory{
				// score ≈ 0.35 < 1.0 (use=0,save=0,llm_extracted,importance=3)
				UseCount: 0, SaveCount: 0, TrustSource: "llm_extracted", Importance: 3,
			},
			wantParked: false,
			wantWarn:   false,
		},
		{
			name: "medium trust: applied with warning",
			target: store.Memory{
				// score ≈ 1.96 ∈ [1.0,3.0) (use=5,save=2,llm_extracted,importance=3)
				UseCount: 5, SaveCount: 2, TrustSource: "llm_extracted", Importance: 3,
			},
			wantParked: false,
			wantWarn:   true,
		},
		{
			name: "high trust: new memory parked",
			target: store.Memory{
				// score ≈ 11.8 ≥ 3.0 (use=10,save=5,user_stated,importance=5)
				UseCount: 10, SaveCount: 5, TrustSource: "user_stated", Importance: 5,
			},
			wantParked: true,
			wantWarn:   false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			st, cleanup := newTestStore(t)
			defer cleanup()
			ctx := context.Background()
			scope := tenantScope("t-trust-" + t.Name())
			entity := "trust-matrix-entity"

			// Fill required fields for the target memory.
			tc.target.ID = ulid.Make().String()
			tc.target.Kind = "fact"
			tc.target.Content = "Python uses dynamic typing"
			tc.target.Status = "active"
			tc.target.Confidence = 0.9
			tc.target.Stability = 1.0
			tc.target.ContentHash = reconcile.ContentHash(reconcile.NormalizeContent(tc.target.Content))
			tc.target.CreatedAt = time.Now().UnixMilli()
			tc.target.UpdatedAt = time.Now().UnixMilli()

			insertTestMemory(t, st, scope, tc.target, []string{entity}, []string{"kw-trust"})

			// Script gateway to supersede the target.
			gw := &stubGateway{
				responses: []gateway.CompleteResponse{
					{JSON: json.RawMessage(`{"action":"supersede","target_ids":["` + tc.target.ID + `"],"reason":"contradiction"}`)},
				},
			}

			// Candidate with different content (not near-dup) but same entity.
			batch := pipeline.CandidateBatch{
				Scope: scope,
				Candidates: []pipeline.Candidate{
					{Kind: "fact", Content: "JavaScript uses static typing",
						Importance: 3, Confidence: 0.85,
						Entities: []string{entity}, Keywords: []string{"kw-trust"}},
				},
			}
			runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

			target, err := st.Memories().Get(ctx, scope, tc.target.ID)
			if err != nil {
				t.Fatalf("Get target: %v", err)
			}

			if tc.wantParked {
				// High trust: target must remain active; new memory parks.
				if target.Status != "active" {
					t.Errorf("high trust: target status = %q, want active", target.Status)
				}
				pending, _, _ := st.Memories().ListByStatus(ctx, scope, "pending_confirmation", 10, "")
				if len(pending) != 1 {
					t.Errorf("high trust: got %d pending_confirmation, want 1", len(pending))
				}
				if len(pending) > 0 && pending[0].SupersedesID != tc.target.ID {
					t.Errorf("high trust: parked.supersedes_id = %q, want %q", pending[0].SupersedesID, tc.target.ID)
				}
			} else {
				// Low/medium: target must be superseded; new memory is active.
				if target.Status != "superseded" {
					t.Errorf("low/medium trust: target status = %q, want superseded", target.Status)
				}
				active, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
				if len(active) != 1 {
					t.Errorf("low/medium trust: got %d active memories, want 1 (new superseder)", len(active))
				}
			}

			warnEvts := eventsByType(t, st, scope, "reconcile.warned")
			if tc.wantWarn && len(warnEvts) == 0 {
				t.Error("medium trust: no reconcile.warned event found; want one")
			}
			if !tc.wantWarn && len(warnEvts) > 0 {
				t.Errorf("low/high trust: unexpected reconcile.warned event (got %d)", len(warnEvts))
			}
		})
	}
}

// --- AC-5: Reversibility — prior-state in events -----------------------------

// TestReversibilityPriorState verifies that update/supersede events embed a
// complete JSON prior-state snapshot in the event Payload.
// Phase 15 rollback will consume this payload verbatim, so it must contain
// all scalar fields AND junction rows (M6, m8).
func TestReversibilityPriorState(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-prior-" + t.Name())
	entity := "prior-state-entity"

	// m8: pre-load non-zero counters, non-default trust_source, non-default
	// stability, and a validity window so the snapshot must preserve them.
	// importance=1, use=7, save=3, user_stated → trust score ≈ 2.09 (MEDIUM);
	// supersede is still applied (not parked) but a reconcile.warned event fires.
	targetContent := "Go uses goroutines for concurrency"
	validFrom := time.Now().Add(-24 * time.Hour).UnixMilli()
	validUntil := time.Now().Add(30 * 24 * time.Hour).UnixMilli()
	target := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: targetContent,
		Status: "active", Confidence: 0.8, TrustSource: "user_stated",
		Importance: 1, Stability: 2.5,
		UseCount: 7, SaveCount: 3,
		ValidFrom: validFrom, ValidUntil: validUntil,
		ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(targetContent)),
		CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, target, []string{entity}, []string{"kw-prior"})

	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"supersede","target_ids":["` + target.ID + `"],"reason":"new info"}`)},
		},
	}

	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Go uses channels for communication",
				Importance: 3, Confidence: 0.85,
				Entities: []string{entity}, Keywords: []string{"kw-prior"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// The memory.superseded event must carry the full prior-state in its Payload.
	evts := eventsByType(t, st, scope, "memory.superseded")
	if len(evts) == 0 {
		t.Fatal("reversibility: no memory.superseded event found")
	}
	payload := evts[0].Payload
	if !strings.Contains(payload, targetContent) {
		t.Errorf("reversibility: prior-state payload does not contain original content %q\npayload: %s",
			targetContent, payload)
	}

	// Parse and verify every significant field of the prior-state snapshot.
	var prior map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &prior); err != nil {
		t.Fatalf("reversibility: payload is not valid JSON: %v\npayload: %s", err, payload)
	}

	assertField := func(key string, want interface{}) {
		t.Helper()
		v, ok := prior[key]
		if !ok {
			t.Errorf("prior-state JSON missing %q field: %s", key, payload)
			return
		}
		// JSON numbers come back as float64.
		switch w := want.(type) {
		case int:
			if v.(float64) != float64(w) {
				t.Errorf("prior-state[%q] = %v, want %d", key, v, w)
			}
		case int64:
			if v.(float64) != float64(w) {
				t.Errorf("prior-state[%q] = %v, want %d", key, v, w)
			}
		case float64:
			if v.(float64) != w {
				t.Errorf("prior-state[%q] = %v, want %g", key, v, w)
			}
		case string:
			if v.(string) != w {
				t.Errorf("prior-state[%q] = %q, want %q", key, v, w)
			}
		}
	}

	assertField("content", targetContent)
	assertField("trust_source", "user_stated")
	assertField("use_count", int64(7))
	assertField("save_count", int64(3))
	assertField("stability", 2.5)
	assertField("valid_from", float64(validFrom))
	assertField("valid_until", float64(validUntil))
	assertField("importance", int(1))
	assertField("confidence", 0.8)
	assertField("status", "active")
}

// --- AC-6: Contradiction boost -----------------------------------------------

// TestContradictionBoost verifies that a supersede commit assigns
// importance ≥ contradictionBoostImportanceFloor (4) and increments
// stability by contradictionBoostStabilityDelta to the new memory.
func TestContradictionBoost(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-boost-" + t.Name())
	entity := "boost-entity"

	// Low-trust target so the supersede is applied (not parked).
	targetContent := "Python is interpreted"
	target := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: targetContent,
		Status: "active", Confidence: 0.7, TrustSource: "llm_extracted",
		Importance: 2, Stability: 1.0,
		ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(targetContent)),
		CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, target, []string{entity}, []string{"kw-boost"})

	// Candidate with low importance (1) — contradiction boost must raise it to ≥ 4.
	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"supersede","target_ids":["` + target.ID + `"],"reason":"correction"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Python can also be compiled",
				Importance: 1, Confidence: 0.85,
				Entities: []string{entity}, Keywords: []string{"kw-boost"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// Find the new (superseding) memory.
	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("contradiction boost: got %d active memories, want 1 (superseder)", len(active))
	}
	newMem := active[0]

	if newMem.Importance < reconcile.ExportContradictionBoostImportanceFloor {
		t.Errorf("contradiction boost: importance = %d, want ≥ %d",
			newMem.Importance, reconcile.ExportContradictionBoostImportanceFloor)
	}
	// Base stability is 1.0 (candidateToMemory); boost adds 1.0 → expect 2.0.
	wantStability := 1.0 + reconcile.ExportContradictionBoostStabilityDelta
	if newMem.Stability != wantStability {
		t.Errorf("contradiction boost: stability = %g, want %g", newMem.Stability, wantStability)
	}
}

// --- AC-6b: Contradiction boost when candidate importance > floor -------------

// TestContradictionBoostHighImportance verifies that when the candidate's
// importance already exceeds the floor (4), the candidate's own importance is
// used rather than the floor. Covers the candidateImportance > floor branch.
func TestContradictionBoostHighImportance(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-boost-hi-" + t.Name())
	entity := "boost-hi-entity"

	// Low-trust target so the supersede is applied (not parked).
	targetContent := "Rust is slow"
	target := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: targetContent,
		Status: "active", Confidence: 0.7, TrustSource: "llm_extracted",
		Importance: 2, Stability: 1.0,
		ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(targetContent)),
		CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, target, []string{entity}, []string{"kw-boost-hi"})

	// Candidate importance=5 > floor(4): new memory keeps importance=5.
	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"supersede","target_ids":["` + target.ID + `"],"reason":"correction"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Rust has zero-cost abstractions",
				Importance: 5, Confidence: 0.95,
				Entities: []string{entity}, Keywords: []string{"kw-boost-hi"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("contradiction boost (high): got %d active memories, want 1", len(active))
	}
	if active[0].Importance != 5 {
		t.Errorf("contradiction boost (high): importance = %d, want 5 (candidate's own importance)", active[0].Importance)
	}
}

// --- AC-7: Links written with source='reconciler' ----------------------------

func TestLinks(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-links-" + t.Name())
	entity := "links-entity"

	// Insert a neighbor so the LLM is called and we can reference its ID in links.
	neighborContent := "Go has goroutines"
	neighborMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: neighborContent,
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(neighborContent)),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, neighborMem, []string{entity}, []string{"kw-links"})

	// Script decision: add new memory + supports link to neighbor.
	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"add","reason":"new fact","links":[{"target_id":"` + neighborMem.ID + `","type":"supports"}]}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Go also has channels",
				Importance: 3, Confidence: 0.85,
				Entities: []string{entity}, Keywords: []string{"kw-links"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// Verify the supports link was written with source='reconciler'.
	links, err := st.Memories().ListLinks(ctx, scope, "", neighborMem.ID)
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("links: got %d links to neighbor, want 1", len(links))
	}
	l := links[0]
	if l.Type != "supports" {
		t.Errorf("link type = %q, want supports", l.Type)
	}
	if l.Source != "reconciler" {
		t.Errorf("link source = %q, want reconciler", l.Source)
	}
}

// --- AC-8: Commit atomicity via FaultHook ------------------------------------

// TestCommitAtomicity verifies that a mid-commit fault leaves no partial rows.
// The FaultHook fires after the memory row is inserted but before junctions and
// events are written; the transaction must roll back completely.
func TestCommitAtomicity(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-atomic-" + t.Name())

	fault := errors.New("injected mid-commit fault")
	memID := ulid.Make().String()
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: memID, Kind: "fact", Content: "atomic fact",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: reconcile.ContentHash("atomic fact"),
			CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
		},
		Entities: []string{"atomic-entity"},
		Keywords: []string{"atomic-kw"},
		Events: []store.Event{
			{ID: ulid.Make().String(), Type: "memory.added", SubjectID: memID,
				Payload: "{}", CreatedAt: time.Now().UnixMilli()},
		},
		FaultHook: func() error { return fault },
	}

	err := st.Memories().Commit(ctx, scope, cs)
	if err == nil {
		t.Fatal("atomicity: expected error from FaultHook, got nil")
	}

	// No memory row should exist.
	_, getErr := st.Memories().Get(ctx, scope, memID)
	if !errors.Is(getErr, store.ErrNotFound) {
		t.Errorf("atomicity: memory row persisted after fault; want ErrNotFound, got %v", getErr)
	}

	// No event rows should exist.
	evts := eventsByType(t, st, scope, "memory.added")
	if len(evts) != 0 {
		t.Errorf("atomicity: %d event rows found after fault; want 0", len(evts))
	}
}

// --- LLM-decided park --------------------------------------------------------

func TestStageLLMDecidedPark(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-llmpark-" + t.Name())
	entity := "llmpark-entity"

	// Insert a neighbor so the gateway is called.
	neighborContent := "Some existing fact"
	neighborMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: neighborContent,
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(neighborContent)),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, neighborMem, []string{entity}, []string{"kw-park"})

	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"park","reason":"uncertain claim"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Some uncertain claim",
				Importance: 3, Confidence: 0.6,
				Entities: []string{entity}, Keywords: []string{"kw-park"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	pending, _, err := st.Memories().ListByStatus(ctx, scope, "pending_confirmation", 10, "")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("LLM park: got %d pending_confirmation, want 1", len(pending))
	}
}

// --- LLM-decided discard ----------------------------------------------------

func TestStageLLMDecidedDiscard(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-llmdiscard-" + t.Name())
	entity := "llmdiscard-entity"

	// Insert a neighbor so the gateway is called.
	neighborContent := "Known fact already in store"
	neighborMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: neighborContent,
		Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(neighborContent)),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, neighborMem, []string{entity}, []string{"kw-discard"})

	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"discard","reason":"redundant noise"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Some noisy candidate",
				Importance: 2, Confidence: 0.5,
				Entities: []string{entity}, Keywords: []string{"kw-discard"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// Only the original neighbor should remain; no new memory.
	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("LLM discard: got %d active memories, want 1 (original only)", len(active))
	}
}

// --- LLM-decided update -----------------------------------------------------

func TestStageLLMDecidedUpdate(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-llmupdate-" + t.Name())
	entity := "llmupdate-entity"

	// m8: Insert a target with non-zero counters and non-default trust_source.
	// use=1, save=0, user_stated, importance=3 → score ≈ 2.386 (MEDIUM);
	// update is applied (not parked) + reconcile.warned event fires.
	targetContent := "Go has one goroutine model"
	targetMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: targetContent,
		Status: "active", Confidence: 0.7, TrustSource: "user_stated",
		Importance: 3, Stability: 2.5, UseCount: 1,
		ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(targetContent)),
		CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, targetMem, []string{entity}, []string{"kw-update"})

	// M5: decision must include content; otherwise validateDecision fails → degrade to add.
	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"update","target_ids":["` + targetMem.ID + `"],"content":"Go supports millions of goroutines","reason":"refinement"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Go supports millions of goroutines",
				Importance: 3, Confidence: 0.9,
				Entities: []string{entity}, Keywords: []string{"kw-update"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	updated, err := st.Memories().Get(context.Background(), scope, targetMem.ID)
	if err != nil {
		t.Fatalf("Get updated: %v", err)
	}
	// C1/M5: content must be from decision.Content.
	if updated.Content != "Go supports millions of goroutines" {
		t.Errorf("update: content = %q, want updated value", updated.Content)
	}
	// m8/C1: counters, trust_source, stability, importance MUST be unchanged.
	if updated.UseCount != targetMem.UseCount {
		t.Errorf("update: UseCount = %d, want %d (must not be overwritten)", updated.UseCount, targetMem.UseCount)
	}
	if updated.TrustSource != targetMem.TrustSource {
		t.Errorf("update: TrustSource = %q, want %q (must not be overwritten)", updated.TrustSource, targetMem.TrustSource)
	}
	if updated.Stability != targetMem.Stability {
		t.Errorf("update: Stability = %g, want %g (must not be overwritten)", updated.Stability, targetMem.Stability)
	}
	if updated.Importance != targetMem.Importance {
		t.Errorf("update: Importance = %d, want %d (must not be overwritten)", updated.Importance, targetMem.Importance)
	}

	evts := eventsByType(t, st, scope, "memory.updated")
	if len(evts) == 0 {
		t.Error("update: no memory.updated event found")
	}
	// Prior-state payload must contain original content (M6).
	if len(evts) > 0 && !strings.Contains(evts[0].Payload, targetContent) {
		t.Errorf("update: prior-state payload missing original content %q", targetContent)
	}
	// MEDIUM trust → reconcile.warned event must be present.
	warnEvts := eventsByType(t, st, scope, "reconcile.warned")
	if len(warnEvts) == 0 {
		t.Error("update: expected reconcile.warned event for medium-trust target; got none")
	}
}

// --- LLM-decided merge -------------------------------------------------------

func TestStageLLMDecidedMerge(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-llmmerge-" + t.Name())
	entity := "llmmerge-entity"

	// Insert two neighbors to be merged.
	mem1Content := "Go uses goroutines"
	mem2Content := "Go uses channels"
	mem1 := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: mem1Content,
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(mem1Content)),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	mem2 := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: mem2Content,
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(mem2Content)),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, mem1, []string{entity}, []string{"kw-merge"})
	insertTestMemory(t, st, scope, mem2, []string{entity}, []string{"kw-merge"})

	// M5: merge decision must include content; otherwise validateDecision fails → degrade to add.
	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"merge","target_ids":["` + mem1.ID + `","` + mem2.ID + `"],"content":"Go uses goroutines and channels for concurrency","reason":"consolidation"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Go uses goroutines and channels for concurrency",
				Importance: 4, Confidence: 0.9,
				Entities: []string{entity}, Keywords: []string{"kw-merge"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// Both sources should be superseded.
	m1, err := st.Memories().Get(context.Background(), scope, mem1.ID)
	if err != nil {
		t.Fatalf("Get mem1: %v", err)
	}
	m2, err := st.Memories().Get(context.Background(), scope, mem2.ID)
	if err != nil {
		t.Fatalf("Get mem2: %v", err)
	}
	if m1.Status != "superseded" {
		t.Errorf("merge: mem1 status = %q, want superseded", m1.Status)
	}
	if m2.Status != "superseded" {
		t.Errorf("merge: mem2 status = %q, want superseded", m2.Status)
	}

	// New merged memory should be active.
	active, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if len(active) != 1 {
		t.Errorf("merge: got %d active memories, want 1 (merged memory)", len(active))
	}
}

// --- Misc: empty content discarded -------------------------------------------

func TestStageEmptyContentDiscarded(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-empty-" + t.Name())

	gw := &stubGateway{}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "   ", Importance: 3, Confidence: 0.8, Provenance: []pipeline.ProvSpan{{RecordID: "r1", SpanEnd: 1}}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	active, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if len(active) != 0 {
		t.Errorf("empty content: got %d active memories want 0", len(active))
	}
}

// --- Misc: gateway error → dead letter (no panic) ----------------------------

func TestStageGatewayError(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-gwerr-" + t.Name())
	entity := "gwerr-entity"

	// Insert a neighbor so FindNeighbors is non-empty and the gateway IS called.
	neighborMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: "Go uses goroutines",
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash("Go uses goroutines"),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, neighborMem, []string{entity}, []string{"kw-gwerr"})

	gw := &stubGateway{
		errs: []error{errors.New("provider unavailable")},
	}
	batch := pipeline.CandidateBatch{
		Scope:     scope,
		BufferKey: "buf-err",
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Go uses channels",
				Importance: 4, Confidence: 0.9,
				Entities: []string{entity}, Keywords: []string{"kw-gwerr"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)
	// Gateway error is dead-lettered; no panic is the primary assertion.
	if gw.calls != 1 {
		t.Errorf("gateway error test: calls = %d, want 1", gw.calls)
	}
}

// --- Misc: discard with explicit target_id covers firstTargetID non-empty ----

// TestStageLLMDiscardWithTargetID verifies that when the LLM returns a discard
// decision with an explicit target_id, the committed discard event carries that
// target_id as its SubjectID. This exercises firstTargetID with a non-empty list.
func TestStageLLMDiscardWithTargetID(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-llmdiscard-tid-" + t.Name())
	entity := "discard-tid-entity"

	// Insert a neighbor so FindNeighbors is non-empty and the gateway IS called.
	neighborContent := "Existing knowledge"
	neighborMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: neighborContent,
		Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(neighborContent)),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, neighborMem, []string{entity}, []string{"kw-discard-tid"})

	// Decision: discard with explicit target_id.
	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"discard","target_ids":["` + neighborMem.ID + `"],"reason":"redundant"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			{Kind: "fact", Content: "Some redundant candidate",
				Importance: 2, Confidence: 0.7,
				Entities: []string{entity}, Keywords: []string{"kw-discard-tid"}},
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// The memory.discarded event must carry the neighbor's ID as SubjectID.
	evts := eventsByType(t, st, scope, "memory.discarded")
	if len(evts) == 0 {
		t.Fatal("discard: no memory.discarded event found")
	}
	if evts[0].SubjectID != neighborMem.ID {
		t.Errorf("discard: event SubjectID = %q, want %q (from target_ids[0])",
			evts[0].SubjectID, neighborMem.ID)
	}
}

// --- Unit: BuildUserPrompt with no neighbors covers the "None found" branch --

func TestBuildUserPromptNoNeighbors(t *testing.T) {
	c := pipeline.Candidate{
		Kind:       "fact",
		Content:    "standalone fact",
		Importance: 3,
		Confidence: 0.9,
	}
	got := reconcile.BuildUserPrompt(c, nil, reconcile.ReconcileContext{})
	if !strings.Contains(got, "None found") {
		t.Errorf("BuildUserPrompt with no neighbors: expected 'None found', got:\n%s", got)
	}
}

// --- Golden prompt tests -----------------------------------------------------

func TestGoldenSystemPrompt(t *testing.T) {
	got := reconcile.BuildSystemPrompt()
	if len(got) == 0 {
		t.Fatal("BuildSystemPrompt returned empty string")
	}

	goldenPath := filepath.Join("testdata", "decision_system.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden: %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Skipf("golden file missing (%s); run with UPDATE_GOLDEN=1 to create", goldenPath)
	}
	if string(want) != got {
		t.Errorf("system prompt differs from golden\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestGoldenUserPrompt(t *testing.T) {
	c := pipeline.Candidate{
		Kind:       "fact",
		Content:    "Go uses goroutines for concurrency",
		Context:    "concurrent programming discussion",
		Entities:   []string{"Go", "goroutines"},
		Keywords:   []string{"concurrency", "programming"},
		Importance: 4,
		Confidence: 0.85,
	}
	neighbors := []store.Memory{
		{
			ID:         "mem-001",
			Kind:       "fact",
			Content:    "Go has channels for communication",
			Context:    "concurrency model",
			Status:     "active",
			Confidence: 0.8,
			Importance: 3,
		},
	}

	got := reconcile.BuildUserPrompt(c, neighbors, reconcile.ReconcileContext{})
	if len(got) == 0 {
		t.Fatal("BuildUserPrompt returned empty string")
	}

	goldenPath := filepath.Join("testdata", "decision_user.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden: %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Skipf("golden file missing (%s); run with UPDATE_GOLDEN=1 to create", goldenPath)
	}
	if string(want) != got {
		t.Errorf("user prompt differs from golden\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestBuildUserPrompt_ConversationContext proves D-108: when ReconcileContext carries the
// candidate's and neighbors' source turns, BuildUserPrompt appends an "Original conversation
// context" section; with the zero value it does not, and the system prompt carries the
// correction-vs-distinct-fact rule.
func TestBuildUserPrompt_ConversationContext(t *testing.T) {
	c := pipeline.Candidate{Kind: "fact", Content: "commute is 45 minutes each way", Importance: 3, Confidence: 0.9}
	neighbors := []store.Memory{{ID: "mem-30", Kind: "fact", Content: "commute is about 30 minutes", Status: "active", Confidence: 0.8, Importance: 3}}

	// Without context: no section.
	plain := reconcile.BuildUserPrompt(c, neighbors, reconcile.ReconcileContext{})
	if strings.Contains(plain, "Original conversation context") {
		t.Errorf("empty context should render no conversation section")
	}

	// With context: section present with both the candidate's and neighbor's turns.
	rc := reconcile.ReconcileContext{
		CandidateTurns: []store.Record{{Role: "user", Content: "my audiobook commute is 45 minutes each way"}},
		NeighborTurns:  map[string][]store.Record{"mem-30": {{Role: "user", Content: "my drive to the office is about 30 minutes"}}},
	}
	withCtx := reconcile.BuildUserPrompt(c, neighbors, rc)
	for _, want := range []string{"Original conversation context", "audiobook commute is 45 minutes each way", "drive to the office is about 30 minutes"} {
		if !strings.Contains(withCtx, want) {
			t.Errorf("context prompt missing %q", want)
		}
	}
	// System prompt instructs correction-vs-distinct-fact disambiguation.
	if !strings.Contains(reconcile.BuildSystemPrompt(), "DIFFERENT fact that merely shares words") {
		t.Errorf("system prompt missing the D-108 disambiguation rule")
	}
}

// --- M3: Merge trust gate matrix ---------------------------------------------

// TestMergeTrustGateMatrix verifies that ActionMerge respects the trust gate
// on every merge target:
//   - All Low trust  → merge applied (targets superseded, new memory active).
//   - Any Medium (no High) → merge applied + reconcile.warned event.
//   - Any High trust → candidate parked; NO target is touched.
func TestMergeTrustGateMatrix(t *testing.T) {
	cases := []struct {
		name       string
		mem1Trust  store.Memory // first target trust params
		mem2Trust  store.Memory // second target trust params
		wantParked bool         // true → candidate pending_confirmation, targets active
		wantWarn   bool         // true → reconcile.warned event emitted
	}{
		{
			name: "all low: merge applied silently",
			// Both use=0,save=0,llm_extracted,importance=3 → score≈0.35 < 1.0
			mem1Trust:  store.Memory{UseCount: 0, SaveCount: 0, TrustSource: "llm_extracted", Importance: 3},
			mem2Trust:  store.Memory{UseCount: 0, SaveCount: 0, TrustSource: "llm_extracted", Importance: 3},
			wantParked: false, wantWarn: false,
		},
		{
			name: "one medium: merge applied with warning",
			// mem1: use=5,save=2,llm_extracted,importance=3 → score≈1.96 (MEDIUM)
			// mem2: use=0,save=0,llm_extracted,importance=3 → score≈0.35 (LOW)
			mem1Trust:  store.Memory{UseCount: 5, SaveCount: 2, TrustSource: "llm_extracted", Importance: 3},
			mem2Trust:  store.Memory{UseCount: 0, SaveCount: 0, TrustSource: "llm_extracted", Importance: 3},
			wantParked: false, wantWarn: true,
		},
		{
			name: "one high: candidate parked",
			// mem1: use=10,save=5,user_stated,importance=5 → score≈11.8 (HIGH)
			// mem2: use=0,save=0,llm_extracted,importance=3 → score≈0.35 (LOW)
			mem1Trust:  store.Memory{UseCount: 10, SaveCount: 5, TrustSource: "user_stated", Importance: 5},
			mem2Trust:  store.Memory{UseCount: 0, SaveCount: 0, TrustSource: "llm_extracted", Importance: 3},
			wantParked: true, wantWarn: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			st, cleanup := newTestStore(t)
			defer cleanup()
			ctx := context.Background()
			scope := tenantScope("t-mtrust-" + t.Name())
			entity := "merge-trust-entity"

			// Build and insert the two targets.
			mem1Content := "Go uses goroutines"
			mem2Content := "Go uses channels"

			mem1 := store.Memory{
				ID: ulid.Make().String(), Kind: "fact", Content: mem1Content,
				Status: "active", Confidence: 0.8,
				TrustSource: tc.mem1Trust.TrustSource,
				Importance:  tc.mem1Trust.Importance,
				UseCount:    tc.mem1Trust.UseCount,
				SaveCount:   tc.mem1Trust.SaveCount,
				Stability:   1.0,
				ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(mem1Content)),
				CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
			}
			mem2 := store.Memory{
				ID: ulid.Make().String(), Kind: "fact", Content: mem2Content,
				Status: "active", Confidence: 0.8,
				TrustSource: tc.mem2Trust.TrustSource,
				Importance:  tc.mem2Trust.Importance,
				UseCount:    tc.mem2Trust.UseCount,
				SaveCount:   tc.mem2Trust.SaveCount,
				Stability:   1.0,
				ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(mem2Content)),
				CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
			}
			insertTestMemory(t, st, scope, mem1, []string{entity}, []string{"kw-mtrust"})
			insertTestMemory(t, st, scope, mem2, []string{entity}, []string{"kw-mtrust"})

			gw := &stubGateway{
				responses: []gateway.CompleteResponse{
					{JSON: json.RawMessage(`{"action":"merge","target_ids":["` + mem1.ID + `","` + mem2.ID + `"],"content":"Go uses goroutines and channels","reason":"consolidation"}`)},
				},
			}
			batch := pipeline.CandidateBatch{
				Scope: scope,
				Candidates: []pipeline.Candidate{
					{Kind: "fact", Content: "Go uses goroutines and channels for concurrency",
						Importance: 4, Confidence: 0.9,
						Entities: []string{entity}, Keywords: []string{"kw-mtrust"}},
				},
			}
			runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

			m1After, err := st.Memories().Get(ctx, scope, mem1.ID)
			if err != nil {
				t.Fatalf("Get mem1: %v", err)
			}
			m2After, err := st.Memories().Get(ctx, scope, mem2.ID)
			if err != nil {
				t.Fatalf("Get mem2: %v", err)
			}

			if tc.wantParked {
				// High-trust: both targets must remain active; candidate is parked.
				if m1After.Status != "active" {
					t.Errorf("high trust: mem1 status = %q, want active", m1After.Status)
				}
				if m2After.Status != "active" {
					t.Errorf("high trust: mem2 status = %q, want active", m2After.Status)
				}
				pending, _, _ := st.Memories().ListByStatus(ctx, scope, "pending_confirmation", 10, "")
				if len(pending) != 1 {
					t.Errorf("high trust: got %d pending_confirmation, want 1", len(pending))
				}
			} else {
				// Low/medium: both targets superseded; exactly one new active memory.
				if m1After.Status != "superseded" {
					t.Errorf("merge: mem1 status = %q, want superseded", m1After.Status)
				}
				if m2After.Status != "superseded" {
					t.Errorf("merge: mem2 status = %q, want superseded", m2After.Status)
				}
				active, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
				if len(active) != 1 {
					t.Errorf("merge: got %d active memories, want 1 (merged)", len(active))
				}
			}

			warnEvts := eventsByType(t, st, scope, "reconcile.warned")
			if tc.wantWarn && len(warnEvts) == 0 {
				t.Error("merge medium trust: no reconcile.warned event; want one")
			}
			if !tc.wantWarn && len(warnEvts) > 0 {
				t.Errorf("merge low/high trust: unexpected reconcile.warned events (got %d)", len(warnEvts))
			}
		})
	}
}

// TestCheckParkedDuplicate verifies AC-6: when a candidate's content hash
// matches a pending_confirmation memory, the reconcile stage discards the
// incoming candidate (no second parked row), bumps the parked memory's
// match_count, and emits a memory.reconfirmed event.
func TestCheckParkedDuplicate(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-parked-dup-" + t.Name())

	// Insert a pending_confirmation memory directly with the right content hash.
	content := "parked duplicate test content"
	normalized := reconcile.NormalizeContent(content)
	hash := reconcile.ContentHash(normalized)
	parkedMem := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     normalized,
		Status:      "pending_confirmation",
		Importance:  3,
		Confidence:  0.7,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: hash,
		CreatedAt:   time.Now().UnixMilli(),
		UpdatedAt:   time.Now().UnixMilli(),
	}
	if err := st.Memories().Insert(ctx, scope, parkedMem); err != nil {
		t.Fatalf("Insert parked memory: %v", err)
	}
	initialMatchCount := parkedMem.MatchCount

	// Run reconcile with the same candidate content (no neighbors so no LLM call).
	gw := &stubGateway{}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			newCandidate("fact", content, 3, 0.7),
		},
	}
	runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

	// The parked memory should still be pending_confirmation (not promoted).
	pending, _, err := st.Memories().ListByStatus(ctx, scope, "pending_confirmation", 10, "")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("pending_confirmation count: got %d want 1 (no second parked row)", len(pending))
	}

	// No new active memory should have been added.
	active, _, err := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
	if err != nil {
		t.Fatalf("ListByStatus active: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("active count: got %d want 0 (candidate should be discarded)", len(active))
	}

	// The parked memory's match_count should be incremented.
	updated, getErr := st.Memories().Get(ctx, scope, parkedMem.ID)
	if getErr != nil {
		t.Fatalf("Get parked: %v", getErr)
	}
	if updated.MatchCount <= initialMatchCount {
		t.Errorf("match_count: got %d want > %d (should be bumped)", updated.MatchCount, initialMatchCount)
	}

	// A memory.reconfirmed event should have been emitted.
	reconfirmedEvts := eventsByType(t, st, scope, "memory.reconfirmed")
	if len(reconfirmedEvts) == 0 {
		t.Error("memory.reconfirmed event: got 0 want >= 1")
	}

	// The gateway should NOT have been called (parked-dup check short-circuits before FindNeighbors).
	if gw.calls != 0 {
		t.Errorf("gateway calls: got %d want 0 (parked-dup check should short-circuit)", gw.calls)
	}
}

// TestSetScopeInvalidator verifies that wiring a ScopeInvalidator causes it
// to be called after a content-adding commit (fast-add path, D-053).
func TestSetScopeInvalidator(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-inv-" + t.Name())

	// Fast-add: no neighbors, gateway returns "add".
	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"add","reason":"new fact"}`)},
		},
	}
	batch := pipeline.CandidateBatch{
		Scope: scope,
		Candidates: []pipeline.Candidate{
			newCandidate("fact", "scope invalidator test", 3, 0.8),
		},
	}
	inv := &stubScopeInvalidator{}
	runStageWithInvalidator(t, st.Memories(), st.Ops(), st.Events(), gw, inv, batch)

	if inv.count == 0 {
		t.Error("ScopeInvalidator.InvalidateScope: not called after fast-add; want >= 1 call")
	}
}
