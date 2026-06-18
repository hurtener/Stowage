package stowage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
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
		return mapHTTPError(method, path, resp.StatusCode, raw)
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("sdk: %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

// mapHTTPError converts a non-2xx HTTP response into an error that is
// errors.Is-matchable to the same sentinels the embedded client surfaces, so
// both Client impls behave identically (D-070 Wave-B checkpoint). A 409 carries
// the reconcile stable conflict code (e.g. "already_rolled_back") and maps back
// to a *reconcile.ConflictError — errors.Is matches by code, so
// errors.Is(err, reconcile.ErrAlreadyRolledBack) succeeds across the wire. A 404
// maps to store.ErrNotFound. Everything else is an opaque transport error.
func mapHTTPError(method, path string, status int, raw []byte) error {
	switch status {
	case http.StatusConflict:
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.Unmarshal(raw, &body); err == nil && body.Code != "" {
			return &reconcile.ConflictError{Code: body.Code, Msg: body.Error}
		}
	case http.StatusNotFound:
		return fmt.Errorf("sdk: %s %s: HTTP 404: %s: %w", method, path, string(raw), store.ErrNotFound)
	}
	return fmt.Errorf("sdk: %s %s: HTTP %d: %s", method, path, status, string(raw))
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

// Playbook implements Client via GET /v1/playbook (D-072). SessionID, when set,
// is passed as the ?session_id= query param for session-affinity.
func (c *httpClient) Playbook(ctx context.Context, req PlaybookRequest) (PlaybookResponse, error) {
	path := "/v1/playbook"
	if req.SessionID != "" {
		path += "?session_id=" + url.QueryEscape(req.SessionID)
	}
	var resp PlaybookResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return PlaybookResponse{}, err
	}
	return resp, nil
}

// Episodes implements Client via GET /v1/episodes (D-080).
func (c *httpClient) Episodes(ctx context.Context, req EpisodesRequest) (EpisodesResponse, error) {
	v := url.Values{}
	if req.ID != "" {
		v.Set("id", req.ID)
	}
	if req.Limit > 0 {
		v.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Cursor != "" {
		v.Set("cursor", req.Cursor)
	}
	if req.SessionID != "" {
		v.Set("session_id", req.SessionID)
	}
	if req.From > 0 {
		v.Set("from", strconv.FormatInt(req.From, 10))
	}
	if req.Until > 0 {
		v.Set("until", strconv.FormatInt(req.Until, 10))
	}
	if req.SimilarTo != "" {
		v.Set("similar_to", req.SimilarTo)
	}
	if req.K > 0 {
		v.Set("k", strconv.Itoa(req.K))
	}
	path := "/v1/episodes"
	if enc := v.Encode(); enc != "" {
		path += "?" + enc
	}
	var resp EpisodesResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return EpisodesResponse{}, err
	}
	return resp, nil
}

// GetMemory implements Client via GET /v1/memories/{id} (D-070).
func (c *httpClient) GetMemory(ctx context.Context, id string) (GetMemoryResponse, error) {
	if id == "" {
		return GetMemoryResponse{}, errors.New("sdk: get_memory: id must not be empty")
	}
	var resp GetMemoryResponse
	if err := c.do(ctx, http.MethodGet, "/v1/memories/"+url.PathEscape(id), nil, &resp); err != nil {
		return GetMemoryResponse{}, err
	}
	return resp, nil
}

// Rollback implements Client via POST /v1/memories/{id}/rollback (D-064/D-070).
func (c *httpClient) Rollback(ctx context.Context, req RollbackRequest) (Memory, error) {
	if req.MemoryID == "" {
		return Memory{}, errors.New("sdk: rollback: memory_id must not be empty")
	}
	var resp Memory
	if err := c.do(ctx, http.MethodPost, "/v1/memories/"+url.PathEscape(req.MemoryID)+"/rollback", nil, &resp); err != nil {
		return Memory{}, err
	}
	return resp, nil
}

// ResolveMemory implements Client via PATCH /v1/memories/{id} (D-065/D-070).
func (c *httpClient) ResolveMemory(ctx context.Context, req ResolveRequest) (ResolveResponse, error) {
	if req.MemoryID == "" {
		return ResolveResponse{}, errors.New("sdk: resolve_memory: memory_id must not be empty")
	}
	if req.Action != "confirm" && req.Action != "reject" {
		return ResolveResponse{}, errors.New("sdk: resolve_memory: action must be confirm or reject")
	}
	var resp ResolveResponse
	if err := c.do(ctx, http.MethodPatch, "/v1/memories/"+url.PathEscape(req.MemoryID), req, &resp); err != nil {
		return ResolveResponse{}, err
	}
	return resp, nil
}

// UpsertTopics implements Client via PUT /v1/topics (D-043/D-071).
func (c *httpClient) UpsertTopics(ctx context.Context, req UpsertTopicsRequest) (UpsertTopicsResponse, error) {
	if len(req.Topics) == 0 {
		return UpsertTopicsResponse{}, errors.New("sdk: upsert_topics: topics must not be empty")
	}
	// The HTTP handler accepts a bare JSON array of topic objects.
	var resp UpsertTopicsResponse
	if err := c.do(ctx, http.MethodPut, "/v1/topics", req.Topics, &resp); err != nil {
		return UpsertTopicsResponse{}, err
	}
	return resp, nil
}

// DeleteTopic implements Client via DELETE /v1/topics/{key} (D-043/D-071).
func (c *httpClient) DeleteTopic(ctx context.Context, key string) (DeleteTopicResponse, error) {
	if key == "" {
		return DeleteTopicResponse{}, errors.New("sdk: delete_topic: key must not be empty")
	}
	var resp DeleteTopicResponse
	if err := c.do(ctx, http.MethodDelete, "/v1/topics/"+url.PathEscape(key), nil, &resp); err != nil {
		return DeleteTopicResponse{}, err
	}
	return resp, nil
}

// Flush implements Client via POST /v1/buffers/{key}/flush (D-071).
func (c *httpClient) Flush(ctx context.Context, req FlushRequest) (FlushResponse, error) {
	if req.Key == "" {
		return FlushResponse{}, errors.New("sdk: flush: key must not be empty")
	}
	body := struct {
		Trigger string `json:"trigger"`
	}{Trigger: req.Trigger}
	var resp FlushResponse
	if err := c.do(ctx, http.MethodPost, "/v1/buffers/"+url.PathEscape(req.Key)+"/flush", body, &resp); err != nil {
		return FlushResponse{}, err
	}
	return resp, nil
}

// branchHTTPRequest mirrors the POST /v1/branches wire format.
type branchHTTPRequest struct {
	Action         string `json:"action"`
	SessionID      string `json:"session_id,omitempty"`
	BranchID       string `json:"branch_id,omitempty"`
	ParentBranchID string `json:"parent_branch_id,omitempty"`
}

// ForkBranch implements Client via POST /v1/branches (D-029/D-071).
func (c *httpClient) ForkBranch(ctx context.Context, req ForkBranchRequest) (ForkBranchResponse, error) {
	var resp ForkBranchResponse
	if err := c.do(ctx, http.MethodPost, "/v1/branches", branchHTTPRequest{
		Action: "fork", SessionID: req.SessionID, ParentBranchID: req.ParentBranchID,
	}, &resp); err != nil {
		return ForkBranchResponse{}, err
	}
	return resp, nil
}

// MergeBranch implements Client via POST /v1/branches (D-029/D-071).
func (c *httpClient) MergeBranch(ctx context.Context, branchID string) (BranchResponse, error) {
	if branchID == "" {
		return BranchResponse{}, errors.New("sdk: merge_branch: branch_id must not be empty")
	}
	if err := c.do(ctx, http.MethodPost, "/v1/branches", branchHTTPRequest{
		Action: "merge", BranchID: branchID,
	}, nil); err != nil {
		return BranchResponse{}, err
	}
	return BranchResponse{BranchID: branchID, Status: "merged"}, nil
}

// DiscardBranch implements Client via POST /v1/branches (D-029/D-071).
func (c *httpClient) DiscardBranch(ctx context.Context, branchID string) (BranchResponse, error) {
	if branchID == "" {
		return BranchResponse{}, errors.New("sdk: discard_branch: branch_id must not be empty")
	}
	if err := c.do(ctx, http.MethodPost, "/v1/branches", branchHTTPRequest{
		Action: "discard", BranchID: branchID,
	}, nil); err != nil {
		return BranchResponse{}, err
	}
	return BranchResponse{BranchID: branchID, Status: "discarded"}, nil
}

// Assert implements Client. memory_assert is a Tier-A verb reachable on
// {SDK, MCP} only — the HTTP surface deliberately routes all writes through the
// ingest pipeline and exposes no direct-assert route (D-071). Over the HTTP
// transport this returns ErrAssertHTTPUnsupported; use an embedded client (or
// the MCP memory_assert tool) for direct asserts.
func (c *httpClient) Assert(_ context.Context, _ AssertRequest) (AssertResponse, error) {
	return AssertResponse{}, ErrAssertHTTPUnsupported
}

// ErrAssertHTTPUnsupported is returned by the HTTP client's Assert: memory_assert
// is intentionally not exposed over HTTP (Tier A = {SDK, MCP} only; D-071).
var ErrAssertHTTPUnsupported = errors.New("sdk: Assert is not available over HTTP; memory_assert is embedded/MCP-only (D-071)")
