// Package openaicompat contains the OpenAI-compatible wire format and the
// openaicompat gateway driver (D-040, D-049). No types or functions from
// this package may be imported outside internal/gateway/openaicompat —
// CLAUDE.md §13, P5.
package openaicompat

import "encoding/json"

// chatRequest is the wire body for POST {base}/chat/completions.
type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []wireMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    float32         `json:"temperature,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *jsonSchemaFormat `json:"json_schema,omitempty"`
}

type jsonSchemaFormat struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

// chatResponse is the wire body for the /chat/completions response.
type chatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Choices []chatChoice `json:"choices"`
	Usage   wireUsage    `json:"usage"`
	Error   *wireError   `json:"error,omitempty"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      wireMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// embedRequest is the wire body for POST {base}/embeddings.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// wireError is the error envelope some OpenAI-compatible providers (e.g.
// OpenRouter) return INSIDE an HTTP 200 body when an upstream call fails.
// Detected post-decode; never silently treated as an empty result.
type wireError struct {
	Code    any    `json:"code"` // number or string depending on provider
	Message string `json:"message"`
}

// embedResponse is the wire body for the /embeddings response.
type embedResponse struct {
	Object string      `json:"object"`
	Data   []embedData `json:"data"`
	Model  string      `json:"model"`
	Usage  wireUsage   `json:"usage"`
	Error  *wireError  `json:"error,omitempty"`
}

type embedData struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
