package bifrost

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"

	bf "github.com/maximhq/bifrost/core"
	bfschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
)

func init() {
	gateway.Register("bifrost", open)
}

const canaryInput = "stowage-probe-canary"

// bifrostClient is the slim sub-surface of *bf.Bifrost the Driver actually
// uses. Defining it explicitly lets tests inject a stub without spinning up
// bifrost's queue infrastructure / network / goroutine pool.
//
// Production wires *bf.Bifrost; tests inject a stubbed implementation via
// newDriverWithClient (see export_test.go).
type bifrostClient interface {
	ChatCompletionRequest(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostChatRequest) (*bfschemas.BifrostChatResponse, *bfschemas.BifrostError)
	EmbeddingRequest(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostEmbeddingRequest) (*bfschemas.BifrostEmbeddingResponse, *bfschemas.BifrostError)
	RerankRequest(ctx *bfschemas.BifrostContext, req *bfschemas.BifrostRerankRequest) (*bfschemas.BifrostRerankResponse, *bfschemas.BifrostError)
}

// Driver is the Bifrost-SDK-backed gateway.Gateway implementation (D-049).
//
// Concurrent-reuse (CLAUDE.md §5): the driver is stateless across calls. The
// embedded bifrostClient is internally synchronized (bifrost owns a queue pool
// and dispatches per-request goroutines). The closed flag is atomic.Bool for
// idempotent Close. Per-call state lives on the call stack / ctx.
type Driver struct {
	client   bifrostClient
	provider bfschemas.ModelProvider
	cfg      config.GatewayConfig
	log      *slog.Logger
	meter    gateway.Meter
	breaker  *gateway.CircuitBreaker
	bat      *gateway.Batcher
	cache    *gateway.EmbedCache

	closed atomic.Bool
}

// Compile-time assertion: *Driver implements gateway.Gateway.
var _ gateway.Gateway = (*Driver)(nil)

func open(
	ctx context.Context,
	cfg config.GatewayConfig,
	log *slog.Logger,
	prom *prometheus.Registry,
) (gateway.Gateway, error) {
	account, err := newAccount(cfg)
	if err != nil {
		return nil, err
	}

	bfCfg := bfschemas.BifrostConfig{
		Account: account,
	}
	inner, err := bf.Init(ctx, bfCfg)
	if err != nil {
		return nil, fmt.Errorf("bifrost: Init: %w", err)
	}
	return newDriverWithClient(inner, account.provider, cfg, log, prom), nil //nolint:contextcheck // batcher uses background ctx for long-lived worker goroutines
}

// newDriverWithClient constructs a Driver with an injected bifrostClient.
// Used by open (production) and tests (fake client).
func newDriverWithClient(
	client bifrostClient,
	provider bfschemas.ModelProvider,
	cfg config.GatewayConfig,
	log *slog.Logger,
	prom *prometheus.Registry,
) *Driver {
	d := &Driver{
		client:   client,
		provider: provider,
		cfg:      cfg,
		log:      log,
		meter:    gateway.NewPromMeter(log, prom),
		breaker:  gateway.NewCircuitBreaker(),
		cache:    gateway.NewEmbedCache(0),
	}
	d.bat = gateway.NewBatcher(d.embedBatch, d.meter, cfg.EmbedModel) //nolint:contextcheck // batcher uses background ctx for async dispatch
	return d
}

// Embed returns vectors for the inputs via the batcher+cache layer.
// Cache hits are returned immediately; all cache misses are dispatched
// concurrently to the batcher so they coalesce into provider batches.
func (d *Driver) Embed(ctx context.Context, req gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	if d.closed.Load() {
		return gateway.EmbedResponse{}, gateway.ErrGatewayUnavailable
	}

	vecs := make([][]float32, len(req.Inputs))

	type miss struct {
		idx   int
		input string
	}
	var misses []miss
	for i, input := range req.Inputs {
		if vec, ok := d.cache.Get(d.cfg.EmbedModel, input); ok {
			vecs[i] = vec
		} else {
			misses = append(misses, miss{i, input})
		}
	}
	if len(misses) == 0 {
		return gateway.EmbedResponse{Vectors: vecs}, nil
	}

	type result struct {
		idx int
		vec []float32
		err error
	}
	ch := make(chan result, len(misses))
	for _, m := range misses {
		go func(m miss) {
			vec, err := d.bat.Embed(ctx, m.input)
			ch <- result{idx: m.idx, vec: vec, err: err}
		}(m)
	}
	for range len(misses) {
		r := <-ch
		if r.err != nil {
			return gateway.EmbedResponse{}, fmt.Errorf("bifrost: embed: %w", r.err)
		}
		d.cache.Put(d.cfg.EmbedModel, req.Inputs[r.idx], r.vec)
		vecs[r.idx] = r.vec
	}

	return gateway.EmbedResponse{Vectors: vecs}, nil
}

// embedBatch is the raw provider call used by the Batcher. It translates
// a slice of input strings into a BifrostEmbeddingRequest, dispatches it
// through the SDK, and translates the response back to Stowage types.
func (d *Driver) embedBatch(ctx context.Context, inputs []string) ([][]float32, gateway.Usage, error) {
	if err := d.breaker.Allow(); err != nil {
		return nil, gateway.Usage{}, err
	}

	bfReq := translateEmbedRequest(d.provider, d.cfg.EmbedModel, inputs)
	bctx := bfschemas.NewBifrostContext(ctx, bfschemas.NoDeadline)
	resp, berr := d.client.EmbeddingRequest(bctx, bfReq)
	if berr != nil {
		d.breaker.Failure()
		return nil, gateway.Usage{}, translateBifrostError(berr, "EmbeddingRequest")
	}
	d.breaker.Success()

	vecs, usage, err := translateEmbedResponse(resp, len(inputs))
	if err != nil {
		return nil, gateway.Usage{}, fmt.Errorf("bifrost: embed: %w", err)
	}
	return vecs, usage, nil
}

// Complete performs a JSON-schema-constrained chat completion. It validates
// the model's JSON response against req.Schema and retries once on validation
// failure with the error appended to the messages (CLAUDE.md §10).
func (d *Driver) Complete(ctx context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	if d.closed.Load() {
		return gateway.CompleteResponse{}, gateway.ErrGatewayUnavailable
	}
	if len(req.Schema) == 0 {
		return gateway.CompleteResponse{}, fmt.Errorf("bifrost: Complete: Schema is required")
	}

	sch, err := gateway.CompileSchema(req.Schema)
	if err != nil {
		return gateway.CompleteResponse{}, fmt.Errorf("bifrost: %w", err)
	}

	resp, usage, err := d.doComplete(ctx, req)
	if err != nil {
		return gateway.CompleteResponse{}, err
	}

	valErr := gateway.ValidateJSON(sch, resp.JSON)
	if valErr == nil {
		d.meter.Record(ctx, "complete", d.cfg.Model, usage)
		return resp, nil
	}

	// Retry once with validation error appended (seam policy).
	d.log.LogAttrs(ctx, slog.LevelWarn, "bifrost.complete: schema validation failed, retrying",
		slog.String("error", valErr.Error()),
	)
	retryReq := req
	retryReq.Messages = append(retryReq.Messages, gateway.Message{
		Role:    "user",
		Content: fmt.Sprintf("Your previous response failed JSON schema validation: %s. Please respond with valid JSON matching the schema.", valErr),
	})
	resp2, usage2, err2 := d.doComplete(ctx, retryReq)
	if err2 != nil {
		return gateway.CompleteResponse{}, err2
	}
	d.meter.Record(ctx, "complete", d.cfg.Model, combineUsage(usage, usage2))
	if valErr2 := gateway.ValidateJSON(sch, resp2.JSON); valErr2 != nil {
		return gateway.CompleteResponse{}, valErr2
	}
	return resp2, nil
}

func combineUsage(a, b gateway.Usage) gateway.Usage {
	return gateway.Usage{
		InputTokens:  a.InputTokens + b.InputTokens,
		OutputTokens: a.OutputTokens + b.OutputTokens,
		CostUSD:      a.CostUSD + b.CostUSD,
	}
}

// doComplete sends a single chat completion request without schema retry.
func (d *Driver) doComplete(ctx context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, gateway.Usage, error) {
	if err := d.breaker.Allow(); err != nil {
		return gateway.CompleteResponse{}, gateway.Usage{}, err
	}

	bfReq := translateChatRequest(d.provider, d.cfg.Model, req)
	bctx := bfschemas.NewBifrostContext(ctx, bfschemas.NoDeadline)
	resp, berr := d.client.ChatCompletionRequest(bctx, bfReq)
	if berr != nil {
		d.breaker.Failure()
		return gateway.CompleteResponse{}, gateway.Usage{}, translateBifrostError(berr, "ChatCompletionRequest")
	}
	d.breaker.Success()

	out, usage, err := translateChatResponse(resp)
	if err != nil {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("bifrost: complete: %w", err)
	}
	return out, usage, nil
}

// Rerank reorders documents by relevance to the query using the configured
// rerank model. Uses the circuit breaker for resilience. On error the caller
// is expected to degrade gracefully (never fatal to retrieval).
func (d *Driver) Rerank(ctx context.Context, req gateway.RerankRequest) (gateway.RerankResponse, error) {
	if d.closed.Load() {
		return gateway.RerankResponse{}, gateway.ErrGatewayUnavailable
	}
	if err := d.breaker.Allow(); err != nil {
		return gateway.RerankResponse{}, err
	}

	docs := make([]bfschemas.RerankDocument, len(req.Documents))
	for i, doc := range req.Documents {
		docs[i] = bfschemas.RerankDocument{Text: doc}
	}

	bfReq := &bfschemas.BifrostRerankRequest{
		Provider:  d.provider,
		Model:     d.cfg.RerankModel,
		Query:     req.Query,
		Documents: docs,
	}
	if req.TopN > 0 {
		topN := req.TopN
		bfReq.Params = &bfschemas.RerankParameters{TopN: &topN}
	}

	bctx := bfschemas.NewBifrostContext(ctx, bfschemas.NoDeadline)
	resp, berr := d.client.RerankRequest(bctx, bfReq)
	if berr != nil {
		d.breaker.Failure()
		return gateway.RerankResponse{}, translateBifrostError(berr, "RerankRequest")
	}
	d.breaker.Success()

	results := make([]gateway.RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = gateway.RerankResult{Index: r.Index, Score: r.RelevanceScore}
	}

	// Bifrost maps search_units to PromptTokens for rerank providers.
	var searchUnits int
	if resp.Usage != nil {
		searchUnits = resp.Usage.PromptTokens
	}

	usage := gateway.RerankUsage{SearchUnits: searchUnits}
	d.meter.RecordRerank(ctx, d.cfg.RerankModel, usage)
	return gateway.RerankResponse{Results: results, Usage: usage}, nil
}

// Probe validates the provider is reachable and the embedding model returns
// the configured dimensions. Fails closed on any error or dims mismatch.
func (d *Driver) Probe(ctx context.Context) error {
	if d.closed.Load() {
		return fmt.Errorf("%w: driver is closed", gateway.ErrProbeFailed)
	}

	bfReq := translateEmbedRequest(d.provider, d.cfg.EmbedModel, []string{canaryInput})
	bctx := bfschemas.NewBifrostContext(ctx, bfschemas.NoDeadline)
	resp, berr := d.client.EmbeddingRequest(bctx, bfReq)
	if berr != nil {
		return fmt.Errorf("%w: embed call failed: %s", gateway.ErrProbeFailed, berr.GetErrorString())
	}

	vecs, _, err := translateEmbedResponse(resp, 1)
	if err != nil {
		return fmt.Errorf("%w: %w", gateway.ErrProbeFailed, err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return fmt.Errorf("%w: empty vector returned", gateway.ErrProbeFailed)
	}
	if d.cfg.EmbedDims > 0 && len(vecs[0]) != d.cfg.EmbedDims {
		return fmt.Errorf("%w: expected %d dims, got %d", gateway.ErrProbeFailed, d.cfg.EmbedDims, len(vecs[0]))
	}
	return nil
}

// Close flushes pending batches and shuts down the underlying bifrost instance.
// Bifrost owns goroutines for the queue/dispatcher — Shutdown() joins the
// per-provider worker pools. Idempotent: subsequent calls return nil.
func (d *Driver) Close(_ context.Context) error {
	if !d.closed.CompareAndSwap(false, true) {
		return nil
	}
	d.bat.Close()
	// Probe the client for a Shutdown method (the *bf.Bifrost teardown path:
	// joins provider worker pools; returns nothing). If the injected test
	// fake implements neither shape, this is a no-op.
	switch c := d.client.(type) {
	case interface{ Cleanup() error }:
		return c.Cleanup()
	case interface{ Shutdown() }:
		c.Shutdown()
	}
	return nil
}

// translateBifrostError converts a *bfschemas.BifrostError into a Stowage
// gateway error. Status codes 429 and ≥500 map to retryable errors;
// auth failures map to a descriptive non-retryable error.
func translateBifrostError(berr *bfschemas.BifrostError, op string) error {
	if berr == nil {
		return nil
	}
	msg := berr.GetErrorString()
	if berr.StatusCode != nil {
		code := *berr.StatusCode
		if code == 429 || code >= 500 {
			return fmt.Errorf("bifrost: %s: HTTP %d: %s", op, code, msg)
		}
		if code == 401 || code == 403 {
			return fmt.Errorf("bifrost: %s: authentication error (HTTP %d): %s", op, code, msg)
		}
		return fmt.Errorf("bifrost: %s: HTTP %d: %s", op, code, msg)
	}
	return fmt.Errorf("bifrost: %s: %s", op, msg)
}

// translateEmbedRequest builds a BifrostEmbeddingRequest for the given inputs.
func translateEmbedRequest(provider bfschemas.ModelProvider, model string, inputs []string) *bfschemas.BifrostEmbeddingRequest {
	var input *bfschemas.EmbeddingInput
	if len(inputs) == 1 {
		s := inputs[0]
		input = &bfschemas.EmbeddingInput{Text: &s}
	} else {
		input = &bfschemas.EmbeddingInput{Texts: inputs}
	}
	return &bfschemas.BifrostEmbeddingRequest{
		Provider: provider,
		Model:    model,
		Input:    input,
	}
}

// translateEmbedResponse converts a BifrostEmbeddingResponse to [][]float32.
// Returns an error if any expected index is missing or out of range.
func translateEmbedResponse(resp *bfschemas.BifrostEmbeddingResponse, expectedCount int) ([][]float32, gateway.Usage, error) {
	if resp == nil {
		return nil, gateway.Usage{}, fmt.Errorf("nil embedding response")
	}
	vecs := make([][]float32, expectedCount)
	for _, item := range resp.Data {
		if item.Index < 0 || item.Index >= expectedCount {
			return nil, gateway.Usage{}, fmt.Errorf("embedding response index %d out of range (expected %d)", item.Index, expectedCount)
		}
		// Convert []float64 → []float32 (bifrost normalises to float64)
		f64 := item.Embedding.EmbeddingArray
		if len(f64) == 0 {
			return nil, gateway.Usage{}, fmt.Errorf("embedding at index %d is empty", item.Index)
		}
		f32 := make([]float32, len(f64))
		for i, v := range f64 {
			f32[i] = float32(v)
		}
		vecs[item.Index] = f32
	}
	var usage gateway.Usage
	if resp.Usage != nil {
		usage.InputTokens = resp.Usage.PromptTokens
	}
	return vecs, usage, nil
}

// translateChatRequest builds a BifrostChatRequest from a gateway.CompleteRequest.
// Structured output is requested via ResponseFormat where the provider supports it.
func translateChatRequest(provider bfschemas.ModelProvider, model string, req gateway.CompleteRequest) *bfschemas.BifrostChatRequest {
	msgs := make([]bfschemas.ChatMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		s := req.System
		msgs = append(msgs, bfschemas.ChatMessage{
			Role:    bfschemas.ChatMessageRoleSystem,
			Content: &bfschemas.ChatMessageContent{ContentStr: &s},
		})
	}
	for _, m := range req.Messages {
		role := bfschemas.ChatMessageRole(m.Role)
		content := m.Content
		msgs = append(msgs, bfschemas.ChatMessage{
			Role:    role,
			Content: &bfschemas.ChatMessageContent{ContentStr: &content},
		})
	}

	var maxTokens *int
	if req.MaxTokens > 0 {
		maxTokens = &req.MaxTokens
	}

	var temp *float64
	if req.Temperature != 0 {
		t := float64(req.Temperature)
		temp = &t
	}

	// Build the JSON schema response format for providers that support it.
	var responseFormat *interface{}
	if len(req.Schema) > 0 {
		rf := buildJSONSchemaResponseFormat(req.Schema)
		responseFormat = &rf
	}

	params := &bfschemas.ChatParameters{
		MaxCompletionTokens: maxTokens,
		Temperature:         temp,
		ResponseFormat:      responseFormat,
	}

	return &bfschemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input:    msgs,
		Params:   params,
	}
}

// buildJSONSchemaResponseFormat constructs the response_format object for
// structured output (JSON schema constraint). Shape mirrors OpenAI's
// response_format with type:"json_schema".
func buildJSONSchemaResponseFormat(schema json.RawMessage) interface{} {
	return map[string]interface{}{
		"type": "json_schema",
		"json_schema": map[string]interface{}{
			"name":   "response",
			"schema": schema,
			"strict": true,
		},
	}
}

// translateChatResponse converts a BifrostChatResponse to gateway types.
// Returns ErrTruncated when finish_reason is "length".
func translateChatResponse(resp *bfschemas.BifrostChatResponse) (gateway.CompleteResponse, gateway.Usage, error) {
	if resp == nil {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("nil chat response")
	}
	if len(resp.Choices) == 0 {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("no choices in response")
	}

	choice := resp.Choices[0]
	if choice.FinishReason != nil && *choice.FinishReason == string(bfschemas.BifrostFinishReasonLength) {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("bifrost: %w", gateway.ErrTruncated)
	}

	if choice.ChatNonStreamResponseChoice == nil || choice.Message == nil {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("no message in response choice")
	}
	msg := choice.Message
	if msg.Content == nil || msg.Content.ContentStr == nil {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("nil content in response message")
	}

	usage := extractUsage(resp.Usage)
	return gateway.CompleteResponse{JSON: json.RawMessage(*msg.Content.ContentStr)}, usage, nil
}

// extractUsage converts a *BifrostLLMUsage to gateway.Usage.
func extractUsage(u *bfschemas.BifrostLLMUsage) gateway.Usage {
	if u == nil {
		return gateway.Usage{}
	}
	var costUSD float64
	if u.Cost != nil {
		costUSD = u.Cost.TotalCost
	}
	return gateway.Usage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		CostUSD:      costUSD,
	}
}
