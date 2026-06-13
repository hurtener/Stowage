package sqlitestore_test

// Tests + fuzz target for the sqlite FTS5 MATCH argument sanitization (BUG-4,
// D-069). The invariant: a lexical query containing FTS operators or special
// characters must NEVER hard-error and silently drop the lexical lane — it must
// return a result set or a clean empty.

import (
	"context"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// ftsSeedCorpus is the operator/special-char corpus that crashed raw FTS5 MATCH
// before sanitization. Each must yield (result|empty), never an error.
var ftsSeedCorpus = []string{
	"",
	`"`,
	`""`,
	`unbalanced "quote`,
	`OR`,
	`AND`,
	`NOT`,
	`NEAR`,
	`a OR b`,
	`foo AND bar`,
	`term*`,
	`*`,
	`col:value`,
	`:`,
	`-`,
	`-minus`,
	`a - b`,
	`(`,
	`)`,
	`(unbalanced`,
	`^caret`,
	`a^b`,
	`{brace}`,
	`a+b`,
	`100%`,
	`email@example.com`,
	`path/to/file`,
	`你好 世界`,
	`café OR thé`,
	`   `,
	`!!!`,
	`"phrase query"`,
	`NEAR(a b, 5)`,
}

func TestFTSSpecialCharQueriesNeverError(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t-" + ulid.Make().String()}

	// Seed a normal memory so the lane has something to (potentially) match.
	insertMemoryP09(t, s, scope, "the quick brown fox jumps", "fact", nil, nil, []string{"how fast is the fox"})

	// Subtests are NOT parallel: the parent's deferred cleanup() closes the store,
	// which would race parallel subtests ("database is closed").
	for _, q := range ftsSeedCorpus {
		q := q
		t.Run("lexical:"+q, func(t *testing.T) {
			if _, err := s.Memories().LexicalSearch(ctx, scope, q, 10, store.Window{}, nil); err != nil {
				t.Fatalf("LexicalSearch(%q): unexpected error (lane aborted): %v", q, err)
			}
			if _, err := s.Memories().QuerySearch(ctx, scope, q, 10, store.Window{}); err != nil {
				t.Fatalf("QuerySearch(%q): unexpected error (lane aborted): %v", q, err)
			}
		})
	}
}

// TestFTSSanitizedQueriesStillMatch proves sanitization does not break ordinary
// queries: normal terms (even mixed with operators) still return the seeded hit.
func TestFTSSanitizedQueriesStillMatch(t *testing.T) {
	t.Parallel()
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := identity.Scope{Tenant: "t-" + ulid.Make().String()}

	insertMemoryP09(t, s, scope, "the quick brown fox jumps", "fact", nil, nil, nil)

	hits, err := s.Memories().LexicalSearch(ctx, scope, "quick fox", 10, store.Window{}, nil)
	if err != nil {
		t.Fatalf("LexicalSearch: %v", err)
	}
	if len(hits) == 0 {
		t.Error("expected a hit for 'quick fox'")
	}

	// Special characters around real terms are stripped, leaving the terms to
	// match (all present in the doc). Mirrors plainto_tsquery robustness.
	hits, err = s.Memories().LexicalSearch(ctx, scope, `"quick", fox!`, 10, store.Window{}, nil)
	if err != nil {
		t.Fatalf("LexicalSearch with special chars: %v", err)
	}
	if len(hits) == 0 {
		t.Error(`expected a hit for '"quick", fox!' (special chars sanitized to terms)`)
	}
}

// FuzzFTSQueryArg fuzzes the sqlite lexical lane against arbitrary query text.
// Invariant: the lane never returns an error (it sanitizes the FTS5 MATCH
// argument), regardless of operators/special characters in the input.
func FuzzFTSQueryArg(f *testing.F) {
	for _, q := range ftsSeedCorpus {
		f.Add(q)
	}
	f.Add("normal words here")
	f.Add("MiXeD cAsE AnD operators OR stuff")

	s, cleanup := newTestStore(f)
	f.Cleanup(cleanup)
	ctx := context.Background()
	scope := identity.Scope{Tenant: "fuzz-tenant"}
	insertMemoryP09Fuzz(f, s, scope, "seed content for the fuzz target", "fact")

	f.Fuzz(func(t *testing.T, query string) {
		if _, err := s.Memories().LexicalSearch(ctx, scope, query, 10, store.Window{}, nil); err != nil {
			t.Fatalf("LexicalSearch(%q): lane-aborting error: %v", query, err)
		}
		if _, err := s.Memories().QuerySearch(ctx, scope, query, 10, store.Window{}); err != nil {
			t.Fatalf("QuerySearch(%q): lane-aborting error: %v", query, err)
		}
	})
}

// insertMemoryP09Fuzz mirrors insertMemoryP09 but takes a testing.TB so it works
// from the fuzz seed setup (testing.F).
func insertMemoryP09Fuzz(tb testing.TB, s store.Store, scope identity.Scope, content, kind string) {
	tb.Helper()
	id := ulid.Make().String()
	ts := int64(1)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: kind, Content: content, Context: "ctx",
			Status: "active", Confidence: 0.9, TrustSource: "llm_extracted",
			Stability: 1.0, ContentHash: ulid.Make().String(),
			CreatedAt: ts, UpdatedAt: ts,
		},
		Queries: []string{"a seed anticipated query"},
		Events: []store.Event{
			{ID: ulid.Make().String(), Type: "memory.added", SubjectID: id, Payload: `{}`},
		},
	}
	if err := s.Memories().Commit(context.Background(), scope, cs); err != nil {
		tb.Fatalf("insertMemoryP09Fuzz: %v", err)
	}
}
