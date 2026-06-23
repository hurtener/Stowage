package gateway

import "encoding/json"

// EmbedRequest carries input strings to embed.
// The model and dimensions are pinned at construction time from config.
type EmbedRequest struct {
	Inputs []string
}

// EmbedResponse carries the resulting float32 vectors and provider usage.
type EmbedResponse struct {
	Vectors [][]float32
	Usage   Usage
}

// CompleteRequest carries a structured completion request.
// Schema is REQUIRED — free-text completions are forbidden (CLAUDE.md §10).
type CompleteRequest struct {
	System      string
	Messages    []Message
	Schema      json.RawMessage // REQUIRED: JSON schema for the response object
	MaxTokens   int
	Temperature float32

	// Model optionally overrides the gateway's configured completion model for
	// this single call. Empty (the default) uses the configured model, so every
	// existing caller is unaffected. Used by the eval harness to answer with a
	// stronger reader model than the cheap extraction model (D-100).
	Model string

	// ReasoningEffort optionally requests provider reasoning / extended thinking
	// at the given effort: "none" | "minimal" | "low" | "medium" | "high". Empty
	// (the default) sends no reasoning parameter, so provider behavior is
	// unchanged for existing callers. Honored by providers that support it; others
	// ignore it (D-100).
	ReasoningEffort string
}

// CompleteResponse carries the validated JSON output and provider usage.
type CompleteResponse struct {
	JSON  json.RawMessage
	Usage Usage
}

// Message is a single turn in the conversation history.
type Message struct {
	Role    string // "user" | "assistant"
	Content string
}

// Usage records token consumption and estimated cost for a provider round-trip.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// RerankRequest carries query + documents to rerank.
type RerankRequest struct {
	Query     string
	Documents []string
	TopN      int // 0 = return all
}

// RerankResponse carries rerank results and provider usage.
type RerankResponse struct {
	Results []RerankResult
	Usage   RerankUsage
}

// RerankResult is one reranked document with its relevance score.
type RerankResult struct {
	Index int
	Score float64
}

// RerankUsage records rerank-specific cost (search units for Cohere).
type RerankUsage struct {
	SearchUnits int
	CostUSD     float64
}
