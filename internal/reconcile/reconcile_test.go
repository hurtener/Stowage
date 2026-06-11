package reconcile_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
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
		{"update", reconcile.DecisionOutput{Action: "update", TargetIDs: []string{"id1"}}},
		{"supersede", reconcile.DecisionOutput{Action: "supersede", TargetIDs: []string{"id1"}}},
		{"merge", reconcile.DecisionOutput{Action: "merge", TargetIDs: []string{"id1", "id2"}}},
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
		{"update no target", reconcile.DecisionOutput{Action: "update"}},
		{"supersede no target", reconcile.DecisionOutput{Action: "supersede"}},
		{"merge one target", reconcile.DecisionOutput{Action: "merge", TargetIDs: []string{"id1"}}},
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
// JSON prior field containing the full pre-mutation memory snapshot.
// Phase 15 rollback will consume exactly this payload.
func TestReversibilityPriorState(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-prior-" + t.Name())
	entity := "prior-state-entity"

	targetContent := "Go uses goroutines for concurrency"
	target := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: targetContent,
		Status: "active", Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(targetContent)),
		CreatedAt: time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
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

	// The memory.superseded event must carry the prior content in its Payload.
	evts := eventsByType(t, st, scope, "memory.superseded")
	if len(evts) == 0 {
		t.Fatal("reversibility: no memory.superseded event found")
	}
	payload := evts[0].Payload
	if !strings.Contains(payload, targetContent) {
		t.Errorf("reversibility: prior-state payload does not contain original content %q\npayload: %s",
			targetContent, payload)
	}
	// The payload must be parseable as JSON with a "content" field.
	var priorMap map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &priorMap); err != nil {
		t.Errorf("reversibility: payload is not valid JSON: %v\npayload: %s", err, payload)
	}
	if _, ok := priorMap["content"]; !ok {
		t.Errorf("reversibility: prior-state JSON missing 'content' field: %s", payload)
	}
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

	// Insert a low-trust target so the trust gate does not park.
	// (use=0, save=0, llm_extracted, importance=3 → score ≈ 0.35 < 1.0)
	targetContent := "Go has one goroutine model"
	targetMem := store.Memory{
		ID: ulid.Make().String(), Kind: "fact", Content: targetContent,
		Status: "active", Confidence: 0.7, TrustSource: "llm_extracted",
		Importance: 3, Stability: 1.0,
		ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(targetContent)),
		CreatedAt:   time.Now().UnixMilli(), UpdatedAt: time.Now().UnixMilli(),
	}
	insertTestMemory(t, st, scope, targetMem, []string{entity}, []string{"kw-update"})

	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"update","target_ids":["` + targetMem.ID + `"],"reason":"refinement"}`)},
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
	if updated.Content != "Go supports millions of goroutines" {
		t.Errorf("update: content = %q, want updated value", updated.Content)
	}
	evts := eventsByType(t, st, scope, "memory.updated")
	if len(evts) == 0 {
		t.Error("update: no memory.updated event found")
	}
	// Prior-state payload must contain original content.
	if !strings.Contains(evts[0].Payload, targetContent) {
		t.Errorf("update: prior-state payload missing original content %q", targetContent)
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

	gw := &stubGateway{
		responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"merge","target_ids":["` + mem1.ID + `","` + mem2.ID + `"],"reason":"consolidation"}`)},
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

	got := reconcile.BuildUserPrompt(c, neighbors)
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
