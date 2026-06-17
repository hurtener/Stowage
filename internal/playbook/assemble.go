package playbook

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/scoring"
	"github.com/hurtener/stowage/internal/store"
)

// Assemble renders a deterministic, sectioned, budget-packed playbook for a
// scope. It is LLM-free (CLAUDE.md §6): it reads the active strategy /
// failure_mode / building-block memories via the store seam, ranks them with the
// pure internal/scoring functions, greedily packs them under the token budget,
// and attaches provenance for drill-down. No gateway call, no retriever.
//
// The algorithm (RFC §6a.3):
//  1. list active memories of the playbook kinds in scope (store.ListByKinds);
//  2. score each by utility counters + decay (scoring.Score with a unit fused
//     base — pure, no I/O);
//  3. greedily pack by score descending (stable ULID tiebreak) until the token
//     budget is reached, so packing is reproducible and budget is never exceeded;
//  4. group packed items into sections by kind (playbook-kind order), ranked
//     within each section by score desc / ULID asc — append-biased so a re-fetch
//     after adding a lower-ranked memory keeps the existing prefix stable;
//  5. attach provenance refs to the packed items (P1).
//
// An empty scope (no matching memories) returns an empty, non-error playbook.
func Assemble(ctx context.Context, st store.Store, scope identity.Scope, opts Options) (*Playbook, error) {
	budget := opts.TokenBudget
	if budget <= 0 {
		budget = DefaultTokenBudget
	}

	// Session-affinity: narrow the scope to a single session when requested.
	if opts.SessionID != "" {
		scope.Session = opts.SessionID
	}

	mems, err := st.Memories().ListByKinds(ctx, scope, playbookKinds)
	if err != nil {
		return nil, fmt.Errorf("playbook: list by kinds: %w", err)
	}

	now := time.Now().UnixMilli()

	// Score every candidate. A unit fused base means the final score reflects the
	// utility factors (use/save/noise/precision/decay/trust/importance) only —
	// the same signals retrieval trusts (brief 02), with no query-relevance term.
	type scored struct {
		mem   store.Memory
		score float64
	}
	cands := make([]scored, 0, len(mems))
	for _, m := range mems {
		s, _ := scoring.Score(scoring.Inputs{
			Memory:     factsFromMemory(m),
			FusedScore: 1.0,
			Now:        now,
		})
		cands = append(cands, scored{mem: m, score: s})
	}

	// Stable global ranking for the greedy packer: score desc, ULID asc tiebreak.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].mem.ID < cands[j].mem.ID
	})

	// Greedy budget pack: include an item when its estimated content tokens fit
	// in the remaining budget; skip-and-continue so smaller lower-ranked items
	// can still fill the tail. The budget is never exceeded (AC-4b).
	packed := make(map[string]float64, len(cands))
	tokensUsed := 0
	for _, c := range cands {
		cost := estimateTokens(c.mem.Content)
		if tokensUsed+cost > budget {
			continue
		}
		tokensUsed += cost
		packed[c.mem.ID] = c.score
	}

	// Group packed items into sections by kind (playbook-kind order).
	byKind := make(map[string][]Item, len(playbookKinds))
	for _, m := range mems {
		score, ok := packed[m.ID]
		if !ok {
			continue
		}
		item := Item{MemoryID: m.ID, Kind: m.Kind, Content: m.Content, Score: score}
		if prov := provenanceFor(ctx, st, scope, m.ID); len(prov) > 0 {
			item.Provenance = prov
		}
		byKind[m.Kind] = append(byKind[m.Kind], item)
	}

	sections := make([]Section, 0, len(playbookKinds))
	for _, kind := range playbookKinds {
		items := byKind[kind]
		if len(items) == 0 {
			continue
		}
		// Append-biased intra-section order: score desc, ULID asc tiebreak.
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Score != items[j].Score {
				return items[i].Score > items[j].Score
			}
			return items[i].MemoryID < items[j].MemoryID
		})
		sections = append(sections, Section{Title: kindTitles[kind], Kind: kind, Items: items})
	}

	return &Playbook{
		Sections: sections,
		Budget: BudgetInfo{
			TokenBudget: budget,
			TokensUsed:  tokensUsed,
			ItemsTotal:  len(mems),
			ItemsPacked: len(packed),
		},
	}, nil
}

// provenanceFor returns the provenance refs for a packed memory, in a stable
// order (record_id, span_start, span_end) so the assembled playbook is
// byte-identical run-to-run. Provenance is best-effort: a junction read error is
// swallowed (the item still renders, just without drill-down refs) — the
// playbook is a read view and must not fail because one item lacks junctions.
func provenanceFor(ctx context.Context, st store.Store, scope identity.Scope, memoryID string) []ProvenanceRef {
	j, err := st.Memories().GetJunctions(ctx, scope, memoryID)
	if err != nil {
		return nil
	}
	refs := toProvRefs(j.Provenance)
	sort.SliceStable(refs, func(i, k int) bool {
		if refs[i].RecordID != refs[k].RecordID {
			return refs[i].RecordID < refs[k].RecordID
		}
		if refs[i].SpanStart != refs[k].SpanStart {
			return refs[i].SpanStart < refs[k].SpanStart
		}
		return refs[i].SpanEnd < refs[k].SpanEnd
	})
	return refs
}

// factsFromMemory projects a store.Memory onto the scoring inputs. Pure mapping;
// no clock read (Now is supplied by Assemble).
func factsFromMemory(m store.Memory) scoring.MemoryFacts {
	return scoring.MemoryFacts{
		MatchCount:     m.MatchCount,
		InjectCount:    m.InjectCount,
		UseCount:       m.UseCount,
		SaveCount:      m.SaveCount,
		FailCount:      m.FailCount,
		NoiseCount:     m.NoiseCount,
		Importance:     m.Importance,
		Confidence:     m.Confidence,
		TrustSource:    m.TrustSource,
		Stability:      m.Stability,
		CreatedAt:      m.CreatedAt,
		LastAccessedAt: m.LastAccessedAt,
		SessionID:      m.SessionID,
	}
}
