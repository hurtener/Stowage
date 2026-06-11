package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hurtener/stowage/internal/identity"
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
}

type ingestRequest struct {
	Records []recordInput `json:"records"`
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

	// Stamp and validate all items up-front so we don't partially commit.
	stamped := make([]records.Record, 0, len(req.Records))
	for i, item := range req.Records {
		if item.TenantID != "" && item.TenantID != authKey.TenantID {
			respondJSON(w, http.StatusForbidden,
				errBody(fmt.Sprintf("item[%d]: tenant scope forgery", i)))
			return
		}
		rec, err := records.New(records.Input{
			TenantID:      authKey.TenantID,
			ProjectID:     item.ProjectID,
			UserID:        item.UserID,
			SessionID:     item.SessionID,
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
		stamped = append(stamped, *rec)
	}

	// Build store records from domain records.
	storeRecs := make([]store.Record, len(stamped))
	for i, rec := range stamped {
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

	// Non-blocking enqueue: record IDs are sent to the pipeline channel.
	// If the channel is full, the enqueue is silently dropped and counted —
	// the records are already durable; the Phase 14 re-enqueue sweep recovers.
	allEnqueued := true
	for _, rec := range stamped {
		select {
		case s.pipeline <- rec.ID:
		default:
			s.pipelineDrops.Add(1)
			allEnqueued = false
		}
	}

	ids := make([]string, len(stamped))
	for i, rec := range stamped {
		ids[i] = rec.ID
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
