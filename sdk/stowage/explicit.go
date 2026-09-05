package stowage

import (
	"context"
	"net/http"

	"github.com/hurtener/stowage/internal/reconcile"
)

// MemoryReceipt is the shared source-backed command receipt. Committed outcome
// and current eligibility are separate; an idempotent retry does not re-save.
type MemoryReceipt = reconcile.ExplicitReceipt

// RememberRequest preserves an exact quotation from an already durable user
// record. The host obtains its ID from Ingest, not from a model-authored claim.
// Identity fields are host context and cannot override verified JWT claims.
type RememberRequest struct {
	SourceRecordID string `json:"source_record_id"`
	Quote          string `json:"quote"`
	Kind           string `json:"kind,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
}

// CorrectRequest replaces an inspected memory with later user evidence. A stale
// revision fails closed. Old records are preserved; correction is not erasure.
type CorrectRequest struct {
	SourceRecordID   string `json:"source_record_id"`
	Quote            string `json:"quote"`
	MemoryID         string `json:"memory_id"`
	ExpectedRevision string `json:"expected_revision"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
	ProjectID        string `json:"project_id,omitempty"`
	UserID           string `json:"user_id,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
}

func (c *embeddedClient) Remember(ctx context.Context, req RememberRequest) (MemoryReceipt, error) {
	scope, err := c.callScope(req.ProjectID, req.UserID, "")
	if err != nil {
		return MemoryReceipt{}, err
	}
	scope.Session = req.SessionID
	if scope.Session == "" {
		scope.Session = c.scope.Session
	}
	r, err := reconcile.Remember(ctx, c.stack.Store, scope, reconcile.ExplicitRequest{SourceRecordID: req.SourceRecordID, Quote: req.Quote, Kind: req.Kind, IdempotencyKey: req.IdempotencyKey}, c.scopeInvalidator())
	if err != nil {
		return MemoryReceipt{}, err
	}
	return *r, nil
}
func (c *embeddedClient) Correct(ctx context.Context, req CorrectRequest) (MemoryReceipt, error) {
	scope, err := c.callScope(req.ProjectID, req.UserID, "")
	if err != nil {
		return MemoryReceipt{}, err
	}
	scope.Session = req.SessionID
	if scope.Session == "" {
		scope.Session = c.scope.Session
	}
	r, err := reconcile.Correct(ctx, c.stack.Store, scope, reconcile.ExplicitRequest{SourceRecordID: req.SourceRecordID, Quote: req.Quote, MemoryID: req.MemoryID, ExpectedRevision: req.ExpectedRevision, IdempotencyKey: req.IdempotencyKey}, c.scopeInvalidator())
	if err != nil {
		return MemoryReceipt{}, err
	}
	return *r, nil
}
func (c *httpClient) Remember(ctx context.Context, req RememberRequest) (MemoryReceipt, error) {
	var out MemoryReceipt
	err := c.do(ctx, http.MethodPost, "/v1/remember", req, &out)
	return out, err
}
func (c *httpClient) Correct(ctx context.Context, req CorrectRequest) (MemoryReceipt, error) {
	var out MemoryReceipt
	err := c.do(ctx, http.MethodPost, "/v1/correct", req, &out)
	return out, err
}
