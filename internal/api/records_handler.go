package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/records"
	"github.com/hurtener/stowage/internal/store"
)

// maxBatchSize is the maximum number of records allowed in a single ingest
// request. Oversized batches return 413.
const maxBatchSize = 512

// recordInput is the per-item wire format for POST /v1/records.
type recordInput struct {
	// TenantID is optional. If provided it MUST match the auth key's tenant or
	// the request is rejected with 403 (cross-tenant forgery prevention, P3).
	TenantID      string `json:"tenant_id"`
	ProjectID     string `json:"project_id"`
	UserID        string `json:"user_id"`
	SessionID     string `json:"session_id"`
	BranchID      string `json:"branch_id"`
	Role          string `json:"role"`
	Content       string `json:"content"`
	SourceAgent   string `json:"source_agent"`
	ResponseID    string `json:"response_id"`
	Outcome       string `json:"outcome"`
	OutcomeDetail string `json:"outcome_detail"`
	OccurredAt    int64  `json:"occurred_at"` // unix millis; 0 → now
	// BufferKey is an optional pipeline routing hint (Phase 06). When provided
	// the record accumulates in the named buffer instead of the default
	// (session_id + "/" + branch_id) derived key.
	BufferKey string `json:"buffer_key"`
}

// targetScopeInput is the optional pool-owner target scope for contribute-mode
// ingest (Phase 15, D-059). When set, the records are committed into the
// target user's scope, subject to an active contribute grant covering the caller.
type targetScopeInput struct {
	ProjectID string `json:"project_id"`
	UserID    string `json:"user_id"`
	SessionID string `json:"session_id"`
}

type ingestRequest struct {
	Records []recordInput `json:"records"`

	// TargetScope and ContributorUserID are the Phase 15 contribute-mode fields
	// (D-059). When TargetScope is set the records are written into the target
	// user's scope; the caller must have an active contribute grant in a group
	// they belong to (identified by ContributorUserID). Without a covering grant
	// the request is rejected 403.
	TargetScope       *targetScopeInput `json:"target_scope,omitempty"`
	ContributorUserID string            `json:"contributor_user_id,omitempty"`
}

type ingestResponse struct {
	IDs      []string `json:"ids"`
	Enqueued bool     `json:"enqueued"`
}

// handleIngest implements POST /v1/records.
//
// Flow (P2 fire-and-forget):
//  1. Decode + validate (400/413 on error).
//  2. For each item: stamp ULID, created_at, token_estimate → records.Record.
//  3. Bulk append to store (durable write).
//  4. Non-blocking enqueue to pipeline channel (drop + metric if full — record
//     already durable; Phase 14 re-enqueue sweep recovers).
//  5. ACK 202 {ids, enqueued}.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if !requireJSON(w, r) {
		return
	}

	var req ingestRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, errBody("decode: "+sanitizeDecodeErr(err)))
		return
	}

	if len(req.Records) == 0 {
		respondJSON(w, http.StatusBadRequest, errBody("records must not be empty"))
		return
	}
	if len(req.Records) > maxBatchSize {
		respondJSON(w, http.StatusRequestEntityTooLarge,
			errBody(fmt.Sprintf("batch too large: max %d items", maxBatchSize)))
		return
	}

	authKey := keyFromContext(r.Context())

	// Contribute-mode (Phase 15, D-059): when target_scope is set the records
	// are committed into the pool owner's scope. Caller must hold an active
	// contribute grant (403 otherwise). Cross-tenant contribute is never allowed.
	targetTenantID := authKey.TenantID // always the auth key's tenant
	targetProjectID := ""
	targetUserID := ""
	targetSessionID := ""
	contributeMode := req.TargetScope != nil

	if contributeMode {
		if s.grantsSvc == nil {
			respondJSON(w, http.StatusServiceUnavailable, errBody("grants service not available"))
			return
		}
		targetProjectID = req.TargetScope.ProjectID
		targetUserID = req.TargetScope.UserID
		targetSessionID = req.TargetScope.SessionID
		callerScope := identity.Scope{Tenant: authKey.TenantID}
		targetScope := identity.Scope{
			Tenant:  targetTenantID,
			Project: targetProjectID,
			User:    targetUserID,
			Session: targetSessionID,
		}
		if err := s.grantsSvc.CheckContributeGrant(r.Context(), callerScope, targetScope, req.ContributorUserID); err != nil {
			if errors.Is(err, grants.ErrNotCovered) || errors.Is(err, grants.ErrCrossTenantGrant) {
				respondJSON(w, http.StatusForbidden, errBody("no active contribute grant for target scope"))
				return
			}
			respondJSON(w, http.StatusInternalServerError, errBody("contribute check: "+err.Error()))
			return
		}
	}

	// Stamp and validate all items up-front so we don't partially commit.
	// stampedItem preserves the per-record buffer_key routing hint (Phase 06).
	type stampedItem struct {
		rec       records.Record
		bufferKey string
	}
	stamped := make([]stampedItem, 0, len(req.Records))
	for i, item := range req.Records {
		if item.TenantID != "" && item.TenantID != authKey.TenantID {
			respondJSON(w, http.StatusForbidden,
				errBody(fmt.Sprintf("item[%d]: tenant scope forgery", i)))
			return
		}
		// In contribute mode, scope fields are overridden with the target scope.
		// The pool-owner's scope is used so memories land in the right tenant/project/user pool.
		recProjectID := item.ProjectID
		recUserID := item.UserID
		recSessionID := item.SessionID
		if contributeMode {
			if targetProjectID != "" {
				recProjectID = targetProjectID
			}
			if targetUserID != "" {
				recUserID = targetUserID
			}
			if targetSessionID != "" {
				recSessionID = targetSessionID
			}
		}
		rec, err := records.New(records.Input{
			TenantID:      authKey.TenantID,
			ProjectID:     recProjectID,
			UserID:        recUserID,
			SessionID:     recSessionID,
			BranchID:      item.BranchID,
			Role:          item.Role,
			Content:       item.Content,
			SourceAgent:   item.SourceAgent,
			ResponseID:    item.ResponseID,
			Outcome:       item.Outcome,
			OutcomeDetail: item.OutcomeDetail,
			OccurredAt:    item.OccurredAt,
		})
		if err != nil {
			respondJSON(w, http.StatusBadRequest,
				errBody(fmt.Sprintf("item[%d]: %v", i, err)))
			return
		}
		stamped = append(stamped, stampedItem{rec: *rec, bufferKey: item.BufferKey})
	}

	// Build store records.
	storeRecs := make([]store.Record, len(stamped))
	for i, si := range stamped {
		rec := si.rec
		storeRecs[i] = store.Record{
			ID:            rec.ID,
			TenantID:      rec.TenantID,
			ProjectID:     rec.ProjectID,
			UserID:        rec.UserID,
			SessionID:     rec.SessionID,
			BranchID:      rec.BranchID,
			Role:          rec.Role,
			Content:       rec.Content,
			SourceAgent:   rec.SourceAgent,
			ResponseID:    rec.ResponseID,
			Outcome:       rec.Outcome,
			OutcomeDetail: rec.OutcomeDetail,
			TokenEstimate: rec.TokenEstimate,
			OccurredAt:    rec.OccurredAt,
			CreatedAt:     rec.CreatedAt,
		}
	}

	scope := identity.Scope{Tenant: authKey.TenantID}
	if err := s.st.Records().Append(r.Context(), scope, storeRecs); err != nil {
		s.log.ErrorContext(r.Context(), "api: ingest: append failed", "err", err)
		respondJSON(w, http.StatusInternalServerError, errBody("store error"))
		return
	}

	s.ingestTotal.Add(float64(len(stamped)))

	// Non-blocking enqueue: pipeline Items are sent to the buffer stage.
	// If the channel is full, the enqueue is silently dropped and counted —
	// the records are already durable; the Phase 14 re-enqueue sweep recovers.
	allEnqueued := true
	for _, si := range stamped {
		select {
		case s.ingestSink <- pipeline.Item{
			RecordID:  si.rec.ID,
			TenantID:  authKey.TenantID,
			BufferKey: si.bufferKey,
			SessionID: si.rec.SessionID,
			BranchID:  si.rec.BranchID,
		}:
		default:
			s.pipelineDrops.Add(1)
			allEnqueued = false
		}
	}

	ids := make([]string, len(stamped))
	for i, si := range stamped {
		ids[i] = si.rec.ID
	}

	respondJSON(w, http.StatusAccepted, ingestResponse{
		IDs:      ids,
		Enqueued: allEnqueued,
	})
}

// sanitizeDecodeErr strips potentially sensitive content from JSON decode errors
// before including them in the response body.
func sanitizeDecodeErr(err error) string {
	// Use a short prefix of the error message only.
	msg := err.Error()
	if errors.Is(err, &json.SyntaxError{}) || len(msg) > 120 {
		return "malformed JSON"
	}
	return msg
}
