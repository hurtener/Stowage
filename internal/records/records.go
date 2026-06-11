// Package records defines the verbatim Record domain type and its constructor.
//
// A Record is the immutable fidelity unit (RFC P1, §5.1). New stamps a ULID,
// created_at, and a len/4 token estimate, then validates the input. No heavier
// work happens here — extraction, embedding, and reconciliation are pipeline
// stages (P2).
package records

import (
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// ValidRoles is the set of allowed role values (mirrors the store schema).
var ValidRoles = map[string]bool{
	"user":      true,
	"assistant": true,
	"tool":      true,
}

// ValidOutcomes is the set of allowed outcome values (mirrors the store schema).
var ValidOutcomes = map[string]bool{
	"":        true,
	"success": true,
	"failure": true,
}

// ErrInvalidRole is returned when the role field is not user|assistant|tool.
var ErrInvalidRole = errors.New("records: invalid role")

// ErrEmptyContent is returned when the content field is empty.
var ErrEmptyContent = errors.New("records: content must not be empty")

// ErrInvalidOutcome is returned when the outcome field is not a valid value.
var ErrInvalidOutcome = errors.New("records: invalid outcome")

// ErrTenantRequired is returned when the tenant ID is empty.
var ErrTenantRequired = errors.New("records: tenant ID is required")

// Record is the verbatim fidelity domain type (RFC P1, D-006).
// All timestamps are unix milliseconds (D-037).
type Record struct {
	ID            string
	TenantID      string
	ProjectID     string
	UserID        string
	SessionID     string
	BranchID      string
	Role          string // "user" | "assistant" | "tool"
	Content       string
	SourceAgent   string
	ResponseID    string
	Outcome       string // "" | "success" | "failure"
	OutcomeDetail string
	TokenEstimate int64
	OccurredAt    int64 // unix millis
	CreatedAt     int64 // unix millis
}

// Input carries the caller-supplied fields for a new record.
// TenantID is required; all others are optional except Role and Content.
type Input struct {
	TenantID      string // required; must match auth key tenant (enforced by handler)
	ProjectID     string
	UserID        string
	SessionID     string
	BranchID      string
	Role          string // "user" | "assistant" | "tool" — required
	Content       string // required; must not be empty
	SourceAgent   string
	ResponseID    string
	Outcome       string // "" | "success" | "failure"
	OutcomeDetail string
	OccurredAt    int64 // unix millis; 0 → time.Now()
}

// New validates in and returns a stamped Record.
//
// Stamped fields: ID (ULID), CreatedAt (now), OccurredAt (in.OccurredAt or now),
// TokenEstimate (len(in.Content)/4 heuristic — D-024 day-one signal, revisit
// with eval data if the heuristic proves too crude).
func New(in Input) (*Record, error) {
	if in.TenantID == "" {
		return nil, ErrTenantRequired
	}
	if !ValidRoles[in.Role] {
		return nil, fmt.Errorf("%w: %q (want user|assistant|tool)", ErrInvalidRole, in.Role)
	}
	if in.Content == "" {
		return nil, ErrEmptyContent
	}
	if !ValidOutcomes[in.Outcome] {
		return nil, fmt.Errorf("%w: %q (want ''|success|failure)", ErrInvalidOutcome, in.Outcome)
	}

	now := time.Now().UnixMilli()
	occurredAt := in.OccurredAt
	if occurredAt == 0 {
		occurredAt = now
	}

	return &Record{
		ID:            ulid.Make().String(),
		TenantID:      in.TenantID,
		ProjectID:     in.ProjectID,
		UserID:        in.UserID,
		SessionID:     in.SessionID,
		BranchID:      in.BranchID,
		Role:          in.Role,
		Content:       in.Content,
		SourceAgent:   in.SourceAgent,
		ResponseID:    in.ResponseID,
		Outcome:       in.Outcome,
		OutcomeDetail: in.OutcomeDetail,
		TokenEstimate: int64(len(in.Content)) / 4,
		OccurredAt:    occurredAt,
		CreatedAt:     now,
	}, nil
}
