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
