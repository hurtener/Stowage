package stowage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpClient implements Client via the Stowage HTTP API.
// It is safe for concurrent use after construction.
type httpClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// Option configures a Client constructed by NewHTTP or NewEmbedded.
type Option func(*options)

type options struct {
	tenantID   string        // embedded: default tenant scope
	httpClient *http.Client  // HTTP: custom transport
	timeout    time.Duration // HTTP: per-request timeout (default 30s)
}

func defaultOptions() *options {
	return &options{
		timeout: 30 * time.Second,
	}
}

// WithHTTPClient sets a custom *http.Client for the HTTP constructor.
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.httpClient = c }
}

// WithTimeout sets the per-request timeout for the HTTP client (default: 30s).
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithTenantID sets the default tenant scope for the embedded client. All
// operations run in this tenant's scope. Required for NewEmbedded.
func WithTenantID(id string) Option {
	return func(o *options) { o.tenantID = id }
}

// NewHTTP returns a Client that communicates with a Stowage HTTP server at
// baseURL using apiKey for Bearer authentication. The tenant scope is derived
// from the API key on the server side.
//
// baseURL must not have a trailing slash (e.g. "http://localhost:7150").
func NewHTTP(baseURL, apiKey string, opts ...Option) Client {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	hc := o.httpClient
	if hc == nil {
		hc = &http.Client{Timeout: o.timeout}
	}

	return &httpClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    hc,
	}
}

// do executes an authenticated HTTP request and decodes the JSON response.
// Retries are not implemented here; callers can wrap the client for retry.
func (c *httpClient) do(ctx context.Context, method, path string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("sdk: marshal: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("sdk: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sdk: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sdk: %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(raw))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("sdk: %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

// Ingest implements Client.
func (c *httpClient) Ingest(ctx context.Context, req IngestRequest) (IngestResponse, error) {
	// Wrap as the API's ingestRequest format.
	type apiRecord struct {
		ProjectID     string `json:"project_id,omitempty"`
		UserID        string `json:"user_id,omitempty"`
		SessionID     string `json:"session_id,omitempty"`
		BranchID      string `json:"branch_id,omitempty"`
		Role          string `json:"role,omitempty"`
		Content       string `json:"content"`
		SourceAgent   string `json:"source_agent,omitempty"`
		ResponseID    string `json:"response_id,omitempty"`
		Outcome       string `json:"outcome,omitempty"`
		OutcomeDetail string `json:"outcome_detail,omitempty"`
		OccurredAt    int64  `json:"occurred_at,omitempty"`
		BufferKey     string `json:"buffer_key,omitempty"`
	}
	type apiReq struct {
		Records []apiRecord `json:"records"`
	}
	apiRecords := make([]apiRecord, len(req.Records))
	for i, r := range req.Records {
		apiRecords[i] = apiRecord(r)
	}

	var resp IngestResponse
	if err := c.do(ctx, http.MethodPost, "/v1/records", apiReq{Records: apiRecords}, &resp); err != nil {
		return IngestResponse{}, err
	}
	return resp, nil
}

// Retrieve implements Client.
func (c *httpClient) Retrieve(ctx context.Context, req RetrieveRequest) (RetrieveResponse, error) {
	var resp RetrieveResponse
	if err := c.do(ctx, http.MethodPost, "/v1/retrieve", req, &resp); err != nil {
		return RetrieveResponse{}, err
	}
	return resp, nil
}

// Drilldown implements Client.
func (c *httpClient) Drilldown(ctx context.Context, req DrilldownRequest) (DrilldownResponse, error) {
	var resp DrilldownResponse
	if err := c.do(ctx, http.MethodPost, "/v1/drilldown", req, &resp); err != nil {
		return DrilldownResponse{}, err
	}
	return resp, nil
}

// Feedback implements Client.
func (c *httpClient) Feedback(ctx context.Context, req FeedbackRequest) (FeedbackResponse, error) {
	var resp FeedbackResponse
	if err := c.do(ctx, http.MethodPost, "/v1/feedback", req, &resp); err != nil {
		return FeedbackResponse{}, err
	}
	return resp, nil
}

// ResolveCitations implements Client.
func (c *httpClient) ResolveCitations(ctx context.Context, req ResolveCitationsRequest) (ResolveCitationsResponse, error) {
	var resp ResolveCitationsResponse
	if err := c.do(ctx, http.MethodPost, "/v1/citations/resolve", req, &resp); err != nil {
		return ResolveCitationsResponse{}, err
	}
	return resp, nil
}

// Topics implements Client.
func (c *httpClient) Topics(ctx context.Context) (TopicsResponse, error) {
	var raw struct {
		Topics []TopicView `json:"topics"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/topics", nil, &raw); err != nil {
		return TopicsResponse{}, err
	}
	return TopicsResponse{Topics: raw.Topics}, nil
}

// Playbook implements Client. Returns a stub in Phase 17.
func (c *httpClient) Playbook(_ context.Context, _ PlaybookRequest) (PlaybookResponse, error) {
	return PlaybookResponse{Entries: []any{}, Stub: true}, nil
}

// ErrPlaybookStub is returned by Playbook implementations while the
// full playbook assembly is pending a future phase.
var ErrPlaybookStub = errors.New("sdk: playbook is a stub in Phase 17; full assembly lands later")
