package retrieval

import (
	"context"
	"log/slog"
	"sort"

	"github.com/hurtener/stowage/internal/gateway"
)

const (
	// rerankSlice is the number of top candidates sent to the rerank model.
	rerankSlice = 24
	// rerankDocBudget is the maximum number of documents per rerank call.
	rerankDocBudget = 32
	// rerankBlendRerank is the weight of the reranked cross-encoder score.
	rerankBlendRerank = 0.6
	// rerankBlendScore is the weight of the Phase-10 utility score.
	rerankBlendScore = 0.4
)

// rerankPass applies cross-encoder reranking to the top candidates when the
// profile enables it. On failure it logs, records degradedRerank=true, and
// returns items in their original Phase-10 order (graceful degradation, D-052).
//
// Returns (degraded, reordered items).
func rerankPass(
	ctx context.Context,
	gw gateway.Gateway,
	rerankModel string,
	query string,
	items []MemoryItem,
	log *slog.Logger,
) (rerankDegraded bool, out []MemoryItem) {
	if gw == nil || len(items) == 0 {
		return true, items
	}

	// Slice the top candidates for reranking; keep the rest untouched.
	sliceLen := rerankSlice
	if sliceLen > rerankDocBudget {
		sliceLen = rerankDocBudget
	}
	if len(items) < sliceLen {
		sliceLen = len(items)
	}

	candidates := items[:sliceLen]
	rest := items[sliceLen:]

	docs := make([]string, len(candidates))
	for i, c := range candidates {
		docs[i] = c.Memory.Content
	}

	resp, err := gw.Rerank(ctx, gateway.RerankRequest{
		Query:     query,
		Documents: docs,
		TopN:      sliceLen,
	})
	if err != nil {
		log.WarnContext(ctx, "retrieval: rerank failed — degraded", "model", rerankModel, "err", err)
		return true, items // Phase-10 order preserved
	}

	// Build a lookup: rerank result index → cross-encoder score.
	rerankScoreByIdx := make(map[int]float64, len(resp.Results))
	var maxRerankScore float64
	for _, r := range resp.Results {
		if r.Score > maxRerankScore {
			maxRerankScore = r.Score
		}
		rerankScoreByIdx[r.Index] = r.Score
	}

	// Normalise Phase-10 scores across the candidate slice.
	var maxPhase10Score float64
	for _, c := range candidates {
		if c.Score > maxPhase10Score {
			maxPhase10Score = c.Score
		}
	}

	// Blend: finalScore = rerankBlendRerank * rerankNorm + rerankBlendScore * phase10Norm
	type blended struct {
		item  MemoryItem
		score float64
	}
	bl := make([]blended, len(candidates))
	for i, c := range candidates {
		var rerankNorm float64
		if maxRerankScore > 0 {
			rerankNorm = rerankScoreByIdx[i] / maxRerankScore
		}
		var phase10Norm float64
		if maxPhase10Score > 0 {
			phase10Norm = c.Score / maxPhase10Score
		}
		bl[i] = blended{
			item:  c,
			score: rerankBlendRerank*rerankNorm + rerankBlendScore*phase10Norm,
		}
	}

	// Sort blended candidates descending, stable on tie by memory ID.
	sort.SliceStable(bl, func(i, j int) bool {
		if bl[i].score != bl[j].score {
			return bl[i].score > bl[j].score
		}
		return bl[i].item.Memory.ID < bl[j].item.Memory.ID
	})

	// Reassemble: reranked candidates + untouched tail.
	out = make([]MemoryItem, 0, len(items))
	for _, b := range bl {
		out = append(out, b.item)
	}
	out = append(out, rest...)
	return false, out
}
