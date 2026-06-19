package records_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/records"
	"github.com/hurtener/stowage/internal/tokenize"
)

func TestNew_Valid(t *testing.T) {
	t.Parallel()
	in := records.Input{
		TenantID: "tenant-1",
		Role:     "user",
		Content:  "hello world",
	}
	rec, err := records.New(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.ID == "" {
		t.Error("ID must not be empty")
	}
	if rec.CreatedAt == 0 {
		t.Error("CreatedAt must be stamped")
	}
	if rec.OccurredAt == 0 {
		t.Error("OccurredAt must be stamped when not provided")
	}
	if want := int64(tokenize.Estimate("hello world")); rec.TokenEstimate != want {
		t.Errorf("TokenEstimate: got %d want %d", rec.TokenEstimate, want)
	}
	if rec.TenantID != "tenant-1" {
		t.Errorf("TenantID: got %q want tenant-1", rec.TenantID)
	}
}

func TestNew_OccurredAtPassthrough(t *testing.T) {
	t.Parallel()
	ts := int64(1700000000000)
	in := records.Input{TenantID: "t", Role: "assistant", Content: "hi", OccurredAt: ts}
	rec, err := records.New(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.OccurredAt != ts {
		t.Errorf("OccurredAt: got %d want %d", rec.OccurredAt, ts)
	}
}

func TestNew_Roles(t *testing.T) {
	t.Parallel()
	validRoles := []string{"user", "assistant", "tool"}
	for _, role := range validRoles {
		role := role
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			in := records.Input{TenantID: "t", Role: role, Content: "x"}
			if _, err := records.New(in); err != nil {
				t.Errorf("role %q rejected unexpectedly: %v", role, err)
			}
		})
	}
}

func TestNew_InvalidRole(t *testing.T) {
	t.Parallel()
	in := records.Input{TenantID: "t", Role: "invalid", Content: "x"}
	_, err := records.New(in)
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
	if !isError(err, records.ErrInvalidRole) {
		t.Errorf("wrong error: got %v want ErrInvalidRole", err)
	}
}

func TestNew_EmptyContent(t *testing.T) {
	t.Parallel()
	in := records.Input{TenantID: "t", Role: "user", Content: ""}
	_, err := records.New(in)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !isError(err, records.ErrEmptyContent) {
		t.Errorf("wrong error: got %v want ErrEmptyContent", err)
	}
}

func TestNew_Outcomes(t *testing.T) {
	t.Parallel()
	validOutcomes := []string{"", "success", "failure"}
	for _, outcome := range validOutcomes {
		outcome := outcome
		t.Run("outcome_"+outcome, func(t *testing.T) {
			t.Parallel()
			in := records.Input{TenantID: "t", Role: "user", Content: "x", Outcome: outcome}
			if _, err := records.New(in); err != nil {
				t.Errorf("outcome %q rejected unexpectedly: %v", outcome, err)
			}
		})
	}
}

func TestNew_InvalidOutcome(t *testing.T) {
	t.Parallel()
	in := records.Input{TenantID: "t", Role: "user", Content: "x", Outcome: "neutral"}
	_, err := records.New(in)
	if err == nil {
		t.Fatal("expected error for invalid outcome")
	}
	if !isError(err, records.ErrInvalidOutcome) {
		t.Errorf("wrong error: got %v want ErrInvalidOutcome", err)
	}
}

func TestNew_TenantRequired(t *testing.T) {
	t.Parallel()
	in := records.Input{Role: "user", Content: "x"}
	_, err := records.New(in)
	if err == nil {
		t.Fatal("expected error for empty tenant")
	}
	if !isError(err, records.ErrTenantRequired) {
		t.Errorf("wrong error: got %v want ErrTenantRequired", err)
	}
}

func TestNew_IDsUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		rec, err := records.New(records.Input{TenantID: "t", Role: "user", Content: "x"})
		if err != nil {
			t.Fatalf("New[%d]: %v", i, err)
		}
		if seen[rec.ID] {
			t.Errorf("duplicate ID at iteration %d: %q", i, rec.ID)
		}
		seen[rec.ID] = true
	}
}

func TestNew_TokenEstimateLongContent(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("x", 400)
	rec, err := records.New(records.Input{TenantID: "t", Role: "user", Content: content})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := int64(400) / 4
	if rec.TokenEstimate != want {
		t.Errorf("TokenEstimate: got %d want %d", rec.TokenEstimate, want)
	}
}

func TestNew_OptionalFields(t *testing.T) {
	t.Parallel()
	in := records.Input{
		TenantID:      "t",
		ProjectID:     "proj",
		UserID:        "user",
		SessionID:     "sess",
		BranchID:      "branch",
		Role:          "tool",
		Content:       "result",
		SourceAgent:   "agent-1",
		ResponseID:    "resp-1",
		Outcome:       "success",
		OutcomeDetail: "all good",
	}
	rec, err := records.New(in)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if rec.ProjectID != "proj" {
		t.Errorf("ProjectID: got %q want proj", rec.ProjectID)
	}
	if rec.SessionID != "sess" {
		t.Errorf("SessionID: got %q want sess", rec.SessionID)
	}
	if rec.Outcome != "success" {
		t.Errorf("Outcome: got %q want success", rec.Outcome)
	}
}

// isError checks whether err wraps target.
func isError(err, target error) bool {
	return errors.Is(err, target)
}
