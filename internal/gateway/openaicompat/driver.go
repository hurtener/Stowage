package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/prometheus/client_golang/prometheus"
)

func init() {
	gateway.Register("openaicompat", open)
}

const (
	maxRetries  = 3
	retryBaseMS = 500
	retryJitter = 200
	httpTimeout = 60 * time.Second
	canaryInput = "stowage-probe-canary"
)

// Driver is the openaicompat gateway driver (OpenAI-compatible wire, D-040).
type Driver struct {
	cfg     config.GatewayConfig
	apiKey  string
	baseURL string
	client  *http.Client
	log     *slog.Logger
	meter   gateway.Meter
	breaker *gateway.CircuitBreaker
	bat     *gateway.Batcher
	cache   *gateway.EmbedCache
}

func open(
	ctx context.Context,
	cfg config.GatewayConfig,
	log *slog.Logger,
	prom *prometheus.Registry,
) (gateway.Gateway, error) {
	apiKey, err := config.ResolveEnvRef(cfg.APIKey)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: %w", err)
	}

	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("openaicompat: base_url is required")
	}

	d := &Driver{
		cfg:     cfg,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: httpTimeout},
		log:     log,
		meter:   gateway.NewPromMeter(log, prom),
		breaker: gateway.NewCircuitBreaker(),
		cache:   gateway.NewEmbedCache(0),
	}
	d.bat = gateway.NewBatcher(d.embedBatch, d.meter, cfg.EmbedModel) //nolint:contextcheck // batcher uses background ctx for async dispatch; caller contexts may be cancelled
	return d, nil
}

// Embed returns vectors for the inputs via the batcher+cache layer.
// Cache hits are returned immediately. All cache misses are dispatched
// concurrently to the batcher so they coalesce into provider batches.
func (d *Driver) Embed(ctx context.Context, req gateway.EmbedRequest) (gateway.EmbedResponse, error) {
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

	// Send all cache misses to the batcher concurrently so they coalesce.
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
			return gateway.EmbedResponse{}, fmt.Errorf("openaicompat: embed: %w", r.err)
		}
		d.cache.Put(d.cfg.EmbedModel, req.Inputs[r.idx], r.vec)
		vecs[r.idx] = r.vec
	}

	return gateway.EmbedResponse{Vectors: vecs}, nil
}

// embedBatch is the raw provider call used by the Batcher.
func (d *Driver) embedBatch(ctx context.Context, inputs []string) ([][]float32, gateway.Usage, error) {
	body := embedRequest{
		Model: d.cfg.EmbedModel,
		Input: inputs,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, gateway.Usage{}, fmt.Errorf("openaicompat: marshal embed request: %w", err)
	}

	var resp embedResponse
	if err := d.doWithRetry(ctx, "embed", d.baseURL+"/embeddings", b, &resp); err != nil {
		return nil, gateway.Usage{}, err
	}

	if resp.Error != nil {
		return nil, gateway.Usage{}, fmt.Errorf("openaicompat: embed: provider error envelope (code %v): %s", resp.Error.Code, resp.Error.Message)
	}

	vecs := make([][]float32, len(resp.Data))
	for _, item := range resp.Data {
		if item.Index < 0 || item.Index >= len(vecs) {
			return nil, gateway.Usage{}, fmt.Errorf("openaicompat: embed response index %d out of range", item.Index)
		}
		vecs[item.Index] = item.Embedding
	}

	usage := gateway.Usage{
		InputTokens: resp.Usage.PromptTokens,
	}
	return vecs, usage, nil
}

// Complete performs a JSON-schema-constrained chat completion. It validates the
// model's JSON response against req.Schema and retries once on validation failure
// with the error appended to the messages (AC-2, CLAUDE.md §10).
func (d *Driver) Complete(ctx context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	if len(req.Schema) == 0 {
		return gateway.CompleteResponse{}, fmt.Errorf("openaicompat: Complete: Schema is required")
	}

	sch, err := gateway.CompileSchema(req.Schema)
	if err != nil {
		return gateway.CompleteResponse{}, fmt.Errorf("openaicompat: %w", err)
	}

	resp, usage, err := d.doComplete(ctx, req)
	if err != nil {
		return gateway.CompleteResponse{}, err
	}

	if valErr := gateway.ValidateJSON(sch, resp.JSON); valErr == nil {
		d.meter.Record(ctx, "complete", d.cfg.Model, usage)
		return resp, nil
	} else {
		// Retry once with validation error appended (AC-2).
		d.log.LogAttrs(ctx, slog.LevelWarn, "openaicompat.complete: schema validation failed, retrying",
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
			// valErr2 is already a wrapped ErrSchemaValidation; return it directly.
			return gateway.CompleteResponse{}, valErr2
		}
		return resp2, nil
	}
}

func combineUsage(a, b gateway.Usage) gateway.Usage {
	return gateway.Usage{
		InputTokens:  a.InputTokens + b.InputTokens,
		OutputTokens: a.OutputTokens + b.OutputTokens,
		CostUSD:      a.CostUSD + b.CostUSD,
	}
}

// doComplete sends a single completion request (no schema retry here).
func (d *Driver) doComplete(ctx context.Context, req gateway.CompleteRequest) (gateway.CompleteResponse, gateway.Usage, error) {
	msgs := make([]wireMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, wireMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, wireMessage{Role: m.Role, Content: m.Content})
	}

	// Per-request model override (D-100): empty Model uses the configured model.
	model := d.cfg.Model
	if req.Model != "" {
		model = req.Model
	}
	body := chatRequest{
		Model:       model,
		Messages:    msgs,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		ResponseFormat: &responseFormat{
			Type: "json_schema",
			JSONSchema: &jsonSchemaFormat{
				Name:   "response",
				Schema: req.Schema,
				Strict: true,
			},
		},
		ReasoningEffort: req.ReasoningEffort, // omitted when empty (D-100)
	}

	b, err := json.Marshal(body)
	if err != nil {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("openaicompat: marshal chat request: %w", err)
	}

	var resp chatResponse
	if err := d.doWithRetry(ctx, "complete", d.baseURL+"/chat/completions", b, &resp); err != nil {
		return gateway.CompleteResponse{}, gateway.Usage{}, err
	}

	if resp.Error != nil {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("openaicompat: complete: provider error envelope (code %v): %s", resp.Error.Code, resp.Error.Message)
	}

	if len(resp.Choices) == 0 {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("openaicompat: no choices in response")
	}

	if resp.Choices[0].FinishReason == "length" {
		return gateway.CompleteResponse{}, gateway.Usage{}, fmt.Errorf("openaicompat: %w", gateway.ErrTruncated)
	}

	usage := gateway.Usage{
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}
	return gateway.CompleteResponse{JSON: json.RawMessage(resp.Choices[0].Message.Content)}, usage, nil
}

// Probe embeds one canary string and verifies len(vec) == cfg.EmbedDims (AC-7).
func (d *Driver) Probe(ctx context.Context) error {
	vecs, _, err := d.embedBatch(ctx, []string{canaryInput})
	if err != nil {
		return fmt.Errorf("%w: embed call failed: %w", gateway.ErrProbeFailed, err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return fmt.Errorf("%w: empty vector returned", gateway.ErrProbeFailed)
	}
	if len(vecs[0]) != d.cfg.EmbedDims {
		return fmt.Errorf("%w: expected %d dims, got %d", gateway.ErrProbeFailed, d.cfg.EmbedDims, len(vecs[0]))
	}
	return nil
}

// Close shuts down the batcher and waits for in-flight dispatches.
func (d *Driver) Close(_ context.Context) error {
	d.bat.Close()
	return nil
}

// doWithRetry executes a POST request with jittered exponential backoff and
// circuit-breaker integration. Retries on 429, 5xx, and network timeouts.
// Never retries on other 4xx (AC-6).
func (d *Driver) doWithRetry(ctx context.Context, op, url string, body []byte, out any) error {
	var lastErr error
	for attempt := range maxRetries {
		if err := d.breaker.Allow(); err != nil {
			return err
		}

		err := d.doOnce(ctx, url, body, out)
		if err == nil {
			d.breaker.Success()
			return nil
		}

		// Classify the error.
		var httpErr *httpError
		if isRetryable(err, &httpErr) {
			d.breaker.Failure()
			lastErr = err
			if attempt < maxRetries-1 {
				sleep := jitteredBackoff(attempt)
				d.log.LogAttrs(ctx, slog.LevelDebug, "openaicompat: retrying",
					slog.String("op", op),
					slog.Int("attempt", attempt+1),
					slog.Duration("backoff", sleep),
					slog.String("error", err.Error()),
				)
				select {
				case <-time.After(sleep):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			continue
		}

		// Non-retryable error (4xx excluding 429).
		d.breaker.Failure()
		return err
	}
	return lastErr
}

func (d *Driver) doOnce(ctx context.Context, url string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("openaicompat: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.apiKey)

	resp, err := d.client.Do(req)
	if err != nil {
		return err // may be a net.Error with Timeout
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("openaicompat: read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return &httpError{
			StatusCode: resp.StatusCode,
			Body:       respBody,
		}
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("openaicompat: decode response: %w", err)
	}
	return nil
}

// httpError represents a non-2xx HTTP response.
type httpError struct {
	StatusCode int
	Body       []byte
}

func (e *httpError) Error() string {
	return fmt.Sprintf("openaicompat: HTTP %d: %s", e.StatusCode, truncate(string(e.Body), 256))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// isRetryable reports whether err is a retryable error and populates *httpErr
// if it is an HTTP error.
func isRetryable(err error, httpErr **httpError) bool {
	// Network timeout.
	var netErr net.Error
	if ok := asNetError(err, &netErr); ok && netErr.Timeout() {
		return true
	}
	// HTTP 429 or 5xx.
	var he *httpError
	if asHTTPError(err, &he) {
		*httpErr = he
		return he.StatusCode == http.StatusTooManyRequests || he.StatusCode >= 500
	}
	return false
}

func asNetError(err error, target *net.Error) bool {
	if ne, ok := err.(net.Error); ok { //nolint:errorlint
		*target = ne
		return true
	}
	return false
}

func asHTTPError(err error, target **httpError) bool {
	if he, ok := err.(*httpError); ok { //nolint:errorlint
		*target = he
		return true
	}
	return false
}

// jitteredBackoff returns a backoff duration for the given attempt (0-indexed).
func jitteredBackoff(attempt int) time.Duration {
	base := retryBaseMS * (1 << attempt)
	jitter := rand.IntN(retryJitter) //nolint:gosec // G404: jitter for retry backoff, not security-sensitive
	return time.Duration(base+jitter) * time.Millisecond
}
