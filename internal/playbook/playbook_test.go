package playbook_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/playbook"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register driver
)

func newTestStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "playbook_test.db")
	s, err := store.Open(context.Background(), config.StoreConfig{Driver: "sqlite", DSN: dsn})
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

// insertMem inserts a deterministic active memory. LastAccessedAt is left 0 so
// scoring's decay term is 1.0 regardless of wall-clock time → the assembled
// playbook is byte-identical run-to-run (the determinism property under test).
func insertMem(t *testing.T, st store.Store, scope identity.Scope, id, kind, content string, use, save, noise int64) {
	t.Helper()
	m := store.Memory{
		ID: id, Kind: kind, Content: content, Status: "active",
		Importance: 3, Confidence: 0.9, TrustSource: "llm_extracted", Stability: 1.0,
		UseCount: use, SaveCount: save, NoiseCount: noise,
		CreatedAt: 1_000_000, UpdatedAt: 1_000_000, // fixed → stable
	}
	if err := st.Memories().Insert(context.Background(), scope, m); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func tenant(id string) identity.Scope { return identity.Scope{Tenant: id} }

// TestAssembleDeterministic proves AC-2: identical input ⇒ byte-identical output.
func TestAssembleDeterministic(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenant("det")
	ctx := context.Background()

	// ULIDs ascending so the tiebreak is observable.
	insertMem(t, st, scope, "01AAAAAAAAAAAAAAAAAAAAAAAA", "strategy", "Always write tests first.", 10, 5, 0)
	insertMem(t, st, scope, "01BBBBBBBBBBBBBBBBBBBBBBBB", "strategy", "Prefer composition.", 3, 0, 1)
	insertMem(t, st, scope, "01CCCCCCCCCCCCCCCCCCCCCCCC", "failure_mode", "Do not panic across the API boundary.", 4, 1, 0)
	insertMem(t, st, scope, "01DDDDDDDDDDDDDDDDDDDDDDDD", "gotcha", "SQLite needs CGO_ENABLED=0 via modernc.", 2, 0, 0)
	insertMem(t, st, scope, "01EEEEEEEEEEEEEEEEEEEEEEEE", "fact", "Irrelevant fact.", 9, 9, 0) // wrong kind — excluded

	var first string
	for i := 0; i < 5; i++ {
		pb, err := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 2000})
		if err != nil {
			t.Fatalf("assemble: %v", err)
		}
		b, _ := json.Marshal(pb)
		if i == 0 {
			first = string(b)
			continue
		}
		if string(b) != first {
			t.Fatalf("non-deterministic output on run %d:\n%s\n!=\n%s", i, b, first)
		}
	}

	// Sanity: sections ordered strategy → failure_mode → gotcha; fact excluded.
	pb, _ := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 2000})
	gotKinds := make([]string, len(pb.Sections))
	for i, s := range pb.Sections {
		gotKinds[i] = s.Kind
	}
	want := []string{"strategy", "failure_mode", "gotcha"}
	if strings.Join(gotKinds, ",") != strings.Join(want, ",") {
		t.Errorf("section order = %v, want %v", gotKinds, want)
	}
	// Within the strategy section, the higher-utility memory ranks first.
	strategySec := pb.Sections[0]
	if strategySec.Items[0].MemoryID != "01AAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("strategy[0] = %s, want the high-use memory", strategySec.Items[0].MemoryID)
	}
	if strategySec.Items[0].Score <= strategySec.Items[1].Score {
		t.Errorf("strategy not ranked by score desc: %v", strategySec.Items)
	}
}

// TestAssembleAppendBias proves AC-2's append-bias clause: adding a strictly
// lower-ranked memory leaves the existing prefix unchanged.
func TestAssembleAppendBias(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenant("append")
	ctx := context.Background()

	insertMem(t, st, scope, "01AAAAAAAAAAAAAAAAAAAAAAAA", "strategy", "High value.", 20, 10, 0)
	insertMem(t, st, scope, "01BBBBBBBBBBBBBBBBBBBBBBBB", "strategy", "Medium value.", 8, 2, 0)

	before, err := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 2000})
	if err != nil {
		t.Fatalf("assemble before: %v", err)
	}
	beforeIDs := itemIDs(before)

	// Add a strictly lower-ranked memory (zero use, some noise).
	insertMem(t, st, scope, "01ZZZZZZZZZZZZZZZZZZZZZZZZ", "strategy", "Low value, noisy.", 0, 0, 5)

	after, err := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 2000})
	if err != nil {
		t.Fatalf("assemble after: %v", err)
	}
	afterIDs := itemIDs(after)

	// The existing prefix must be preserved; the new item appends at the end.
	if len(afterIDs) != len(beforeIDs)+1 {
		t.Fatalf("expected one appended item: before=%v after=%v", beforeIDs, afterIDs)
	}
	for i := range beforeIDs {
		if afterIDs[i] != beforeIDs[i] {
			t.Errorf("prefix changed at %d: before=%v after=%v", i, beforeIDs, afterIDs)
		}
	}
	if afterIDs[len(afterIDs)-1] != "01ZZZZZZZZZZZZZZZZZZZZZZZZ" {
		t.Errorf("new lower-ranked item not appended last: %v", afterIDs)
	}
}

// TestAssembleBudgetNeverExceeded proves AC-4b: the packer respects TokenBudget.
func TestAssembleBudgetNeverExceeded(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenant("budget")
	ctx := context.Background()

	// Each content is ~40 chars ⇒ ~10 tokens. With a 12-token budget only one
	// item can be packed.
	body := strings.Repeat("abcd ", 8) // 40 chars
	insertMem(t, st, scope, "01AAAAAAAAAAAAAAAAAAAAAAAA", "strategy", body, 10, 0, 0)
	insertMem(t, st, scope, "01BBBBBBBBBBBBBBBBBBBBBBBB", "strategy", body, 5, 0, 0)
	insertMem(t, st, scope, "01CCCCCCCCCCCCCCCCCCCCCCCC", "strategy", body, 1, 0, 0)

	pb, err := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 12})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if pb.Budget.TokensUsed > 12 {
		t.Errorf("budget exceeded: used=%d budget=12", pb.Budget.TokensUsed)
	}
	if pb.Budget.ItemsPacked != 1 {
		t.Errorf("packed %d items, want 1 under a 12-token budget", pb.Budget.ItemsPacked)
	}
	if pb.Budget.ItemsTotal != 3 {
		t.Errorf("ItemsTotal = %d, want 3", pb.Budget.ItemsTotal)
	}
	// The highest-utility item must be the one packed.
	if pb.Sections[0].Items[0].MemoryID != "01AAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("packed the wrong item: %s", pb.Sections[0].Items[0].MemoryID)
	}
}

// TestAssembleSingleItemOverBudget pins the AC-4b edge the prompt called out: a
// single item whose token cost exceeds the WHOLE budget is skipped (not packed),
// so the budget is never exceeded and nothing is emitted (Wave-C checkpoint NIT).
func TestAssembleSingleItemOverBudget(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenant("overbudget")
	ctx := context.Background()

	// ~200 chars ⇒ ~50 tokens, far above the 5-token budget.
	big := strings.Repeat("abcd ", 40)
	insertMem(t, st, scope, "01AAAAAAAAAAAAAAAAAAAAAAAA", "strategy", big, 10, 0, 0)

	pb, err := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 5})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if pb.Budget.ItemsPacked != 0 {
		t.Errorf("ItemsPacked = %d, want 0 (single item exceeds the whole budget)", pb.Budget.ItemsPacked)
	}
	if pb.Budget.TokensUsed != 0 {
		t.Errorf("TokensUsed = %d, want 0", pb.Budget.TokensUsed)
	}
	if pb.Budget.TokensUsed > 5 {
		t.Errorf("budget exceeded: used=%d budget=5", pb.Budget.TokensUsed)
	}
}

// TestAssembleEmptyScope proves AC-4b's empty clause + the default-budget path.
func TestAssembleEmptyScope(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	pb, err := playbook.Assemble(ctx, st, tenant("empty"), playbook.Options{}) // budget <= 0 → default
	if err != nil {
		t.Fatalf("assemble empty: %v", err)
	}
	if len(pb.Sections) != 0 {
		t.Errorf("empty scope returned %d sections, want 0", len(pb.Sections))
	}
	if pb.Budget.TokenBudget != playbook.DefaultTokenBudget {
		t.Errorf("default budget not applied: got %d want %d", pb.Budget.TokenBudget, playbook.DefaultTokenBudget)
	}
	if pb.Budget.ItemsPacked != 0 || pb.Budget.ItemsTotal != 0 {
		t.Errorf("empty scope budget info nonzero: %+v", pb.Budget)
	}
}

// TestAssembleSessionAffinity proves Options.SessionID narrows the scope.
func TestAssembleSessionAffinity(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	base := tenant("sess")

	// Two memories in distinct sessions (session is a scope field on insert).
	s1 := identity.Scope{Tenant: "sess", Session: "s1"}
	s2 := identity.Scope{Tenant: "sess", Session: "s2"}
	insertMem(t, st, s1, "01AAAAAAAAAAAAAAAAAAAAAAAA", "strategy", "Session one strategy.", 5, 0, 0)
	insertMem(t, st, s2, "01BBBBBBBBBBBBBBBBBBBBBBBB", "strategy", "Session two strategy.", 5, 0, 0)

	pb, err := playbook.Assemble(ctx, st, base, playbook.Options{SessionID: "s1", TokenBudget: 2000})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	ids := itemIDs(pb)
	if len(ids) != 1 || ids[0] != "01AAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("session affinity wrong: got %v want [01AAAA...]", ids)
	}
}

// TestAssembleAttachesProvenance proves packed items carry sorted provenance
// refs for P1 drill-down (covers provenanceFor + toProvRefs).
func TestAssembleAttachesProvenance(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenant("prov")
	ctx := context.Background()

	const memID = "01AAAAAAAAAAAAAAAAAAAAAAAA"
	insertMem(t, st, scope, memID, "strategy", "Provenanced strategy.", 5, 0, 0)

	// Provenance has a FK to records — append the verbatim records first.
	if err := st.Records().Append(ctx, scope, []store.Record{
		{ID: "recA", Role: "user", Content: "alpha record", OccurredAt: 1, CreatedAt: 1},
		{ID: "recB", Role: "user", Content: "bravo record", OccurredAt: 1, CreatedAt: 1},
	}); err != nil {
		t.Fatalf("append records: %v", err)
	}

	// Two provenance rows inserted out of order; assembly must return them sorted.
	if err := st.Memories().AddProvenance(ctx, scope, []store.Provenance{
		{ID: "p2", MemoryID: memID, RecordID: "recB", SpanStart: 0, SpanEnd: 5, TenantID: scope.Tenant, CreatedAt: 1},
		{ID: "p1", MemoryID: memID, RecordID: "recA", SpanStart: 3, SpanEnd: 9, TenantID: scope.Tenant, CreatedAt: 1},
	}); err != nil {
		t.Fatalf("AddProvenance: %v", err)
	}

	pb, err := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 2000})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	item := pb.Sections[0].Items[0]
	if len(item.Provenance) != 2 {
		t.Fatalf("expected 2 provenance refs, got %+v", item.Provenance)
	}
	if item.Provenance[0].RecordID != "recA" || item.Provenance[1].RecordID != "recB" {
		t.Errorf("provenance not sorted by record_id: %+v", item.Provenance)
	}
}

// TestAssembleTinyContent exercises the estimateTokens floor (len<4 → 1 token).
func TestAssembleTinyContent(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenant("tiny")
	ctx := context.Background()
	insertMem(t, st, scope, "01AAAAAAAAAAAAAAAAAAAAAAAA", "strategy", "x", 1, 0, 0)

	pb, err := playbook.Assemble(ctx, st, scope, playbook.Options{TokenBudget: 2000})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if pb.Budget.TokensUsed != 1 {
		t.Errorf("tiny content tokens = %d, want 1", pb.Budget.TokensUsed)
	}
}

// TestKinds covers the exported Kinds accessor (the single source of truth).
func TestKinds(t *testing.T) {
	k := playbook.Kinds()
	want := []string{"strategy", "failure_mode", "decision", "gotcha", "pattern"}
	if strings.Join(k, ",") != strings.Join(want, ",") {
		t.Errorf("Kinds() = %v, want %v", k, want)
	}
	// Mutating the returned slice must not affect the package's view.
	k[0] = "mutated"
	if playbook.Kinds()[0] != "strategy" {
		t.Error("Kinds() returned a non-defensive copy")
	}
}

func itemIDs(pb *playbook.Playbook) []string {
	var out []string
	for _, s := range pb.Sections {
		for _, it := range s.Items {
			out = append(out, it.MemoryID)
		}
	}
	return out
}
