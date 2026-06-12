package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hurtener/stowage/internal/gateway"
)

// rerankWireRequest is the Cohere-shape rerank wire body (POST {base}/rerank).
type rerankWireRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
}

type rerankWireResponse struct {
	Results []rerankWireResult `json:"results"`
	Usage   rerankWireUsage    `json:"usage,omitempty"`
	Error   *wireError         `json:"error,omitempty"`
}

type rerankWireResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type rerankWireUsage struct {
	SearchUnits int `json:"search_units"`
}

// Rerank sends a Cohere-shape rerank request to the configured base URL.
// On provider-envelope error it returns a descriptive error; on HTTP failure
// it uses the same jittered-retry + circuit-breaker path as other endpoints.
func (d *Driver) Rerank(ctx context.Context, req gateway.RerankRequest) (gateway.RerankResponse, error) {
	body := rerankWireRequest{
		Model:     d.cfg.RerankModel,
		Query:     req.Query,
		Documents: req.Documents,
		TopN:      req.TopN,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return gateway.RerankResponse{}, fmt.Errorf("openaicompat: rerank: marshal: %w", err)
	}

	var resp rerankWireResponse
	if err := d.doWithRetry(ctx, "rerank", d.baseURL+"/rerank", b, &resp); err != nil {
		return gateway.RerankResponse{}, err
	}
	if resp.Error != nil {
		return gateway.RerankResponse{}, fmt.Errorf("openaicompat: rerank: provider error envelope (code %v): %s", resp.Error.Code, resp.Error.Message)
	}

	results := make([]gateway.RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = gateway.RerankResult{Index: r.Index, Score: r.RelevanceScore}
	}
	usage := gateway.RerankUsage{SearchUnits: resp.Usage.SearchUnits}
	d.meter.RecordRerank(ctx, d.cfg.RerankModel, usage)
	return gateway.RerankResponse{Results: results, Usage: usage}, nil
}
