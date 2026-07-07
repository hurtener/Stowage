// runsink.go — the surface-agnostic Harbor run-completion → verbatim-records
// converter core (phase ae12, D-153).
//
// Harbor's run-completion hook dispatches a pinned RunCompletionPayload
// (format_version 1) — the run's identity quadruple, run metadata, and an
// ordered two-role conversation transcript — to a named catalog tool. Stowage's
// memory_ingest_run tool (internal/mcpserver) is the thin caller; the actual
// payload→records adaptation lives HERE, in the core, so the conversion is
// surface-agnostic: a future HTTP surface would be a thin caller of this same
// function (deliberate MCP-only tiering this phase, D-153 §4). This file has NO
// mcpserver import (the surface mirrors Harbor's wire shape into RunCompletion
// and calls FromRunCompletion) — enforced by acceptance criterion 8.
//
// Fidelity (P1): every conversation entry becomes exactly one verbatim record,
// in order, both roles, nothing filtered — extraction and reconciliation decide
// relevance downstream (brief 04). Identity is NOT resolved here: TenantID/UserID
// on the returned Inputs carry the payload's own claim; the handler overrides the
// tenant with the verified credential scope and lets D-124's scope-authoritative
// Append bind the user dimension (the payload quad is only cross-checked at the
// handler, never scope-authoritative — D-153 §2).

package records

import (
	"errors"
	"fmt"
	"time"
)

// RunCompletionFormatVersion is the single Harbor RunCompletionPayload schema
// version this converter accepts. The pin is the drift gate (D-153 §1): a future
// Harbor v2 fails loudly here, it never misparses. Bumping it is a deliberate,
// reviewed change paired with a contract update.
const RunCompletionFormatVersion = 1

// ErrUnsupportedRunFormat is returned when a payload's format_version is not
// RunCompletionFormatVersion. It names the supported version (D-153 §1).
var ErrUnsupportedRunFormat = errors.New("records: unsupported run-completion format_version")

// ErrEmptyConversation is returned when a run-completion payload carries no
// conversation entries — an empty transcript is a caller bug, never a no-op
// ingest (the ACK would falsely imply a save).
var ErrEmptyConversation = errors.New("records: run-completion conversation must not be empty")

// ErrEmptyRunID is returned when a run-completion payload carries no run_id — it
// is the extraction buffer key (one run = one buffer), so it cannot be empty.
var ErrEmptyRunID = errors.New("records: run-completion run_id must not be empty")

// ErrBadRunTimestamp is returned when a non-empty RFC3339 timestamp
// (started_at / completed_at / an entry's at) fails to parse — a parse failure
// rejects the whole payload rather than silently stamping a zero time (P1).
var ErrBadRunTimestamp = errors.New("records: run-completion timestamp is not RFC3339")

// RunCompletionEntry mirrors Harbor's TranscriptEntry (format_version 1) — all
// five fields. Kind and Step are wire-validated for shape fidelity but dropped
// on the records mapping (append order preserves sequence; content is
// self-contained — D-153 §3). At is the per-entry assertion time when Harbor
// provides it (RFC3339; empty ⇒ fall back to the run's completed_at).
type RunCompletionEntry struct {
	Role    string // "user" | "assistant"
	Kind    string // goal/steering/tool/... — accepted, not enum-validated, not stored
	Content string // verbatim (P1)
	Step    int    // trajectory index — accepted, not stored
	At      string // RFC3339; per-entry assertion time when present
}

// RunCompletion is the core-side, surface-agnostic form of Harbor's pinned
// RunCompletionPayload (format_version 1). Timestamps are carried as their raw
// RFC3339 wire strings so the parse-and-reject logic (and its fuzz coverage)
// lives in this one core function rather than in each surface's adapter.
type RunCompletion struct {
	FormatVersion   int
	TenantID        string
	UserID          string
	SessionID       string
	RunID           string
	AgentID         string
	Outcome         string // goal/no_path/constraints_conflict/cancelled/deadline_exceeded/error
	StartedAt       string // RFC3339 (validated, dropped)
	CompletedAt     string // RFC3339 (the per-record OccurredAt fallback)
	DurationMS      int64  // validated-shape, dropped (run observability, not memory signal)
	StepCount       int    // dropped
	ToolInvocations int    // dropped
	Conversation    []RunCompletionEntry
}

// FromRunCompletion converts a Harbor run-completion payload (format_version 1)
// into ingest inputs: one records.Input per conversation entry, in order (D-153
// §3). It rejects (fail loud, never a partial or silent-zero result):
//
//   - format_version != RunCompletionFormatVersion (the drift gate)
//   - an empty run_id (the buffer key)
//   - an empty conversation (a caller bug)
//   - any non-empty started_at / completed_at / entry.at that is not RFC3339
//
// Per-entry mapping (the records.Input fields):
//
//	role                          -> Role                (both roles, verbatim order)
//	content                       -> Content             (verbatim, P1)
//	at when set else completed_at -> OccurredAt          (per-entry assertion time when given)
//	session_id                    -> SessionID           (writes stay session-stamped, D-150)
//	agent_id                      -> SourceAgent         (metadata, never an isolation key)
//	outcome                       -> Outcome/OutcomeDetail (see projectRunOutcome; D-024 signal)
//	tenant_id / user_id           -> TenantID / UserID   (payload claim; handler is authoritative — D-124/D-153)
//	kind, step                    -> (dropped)           (append order preserves sequence)
//	started_at, duration_ms, step_count, tool_invocations, format_version -> (validated, dropped)
//
// TenantID/UserID are stamped from the payload for D-124's fill-empty rule; the
// handler overrides TenantID with the verified scope tenant before records.New,
// and Append lets the verified scope user win when set (jwt mode) or the payload
// user fill an empty scope dimension (keyring mode) — see the handler and D-153 §2.
// Role/content validity is NOT checked here; records.New (called by the handler
// per input) is the single validation gate for those (avoids a second, drifting
// copy of the role/content rules).
func FromRunCompletion(p RunCompletion) ([]Input, error) {
	if p.FormatVersion != RunCompletionFormatVersion {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedRunFormat, p.FormatVersion, RunCompletionFormatVersion)
	}
	if p.RunID == "" {
		return nil, ErrEmptyRunID
	}
	if len(p.Conversation) == 0 {
		return nil, ErrEmptyConversation
	}

	// started_at is dropped from the record shape but still validated when
	// present — the pinned wire contract's fidelity check (the golden
	// marshal-validate test relies on it, and a malformed run timestamp is a
	// caller bug we surface, never swallow).
	if _, err := parseRunTime(p.StartedAt); err != nil {
		return nil, fmt.Errorf("started_at: %w", err)
	}
	completedMillis, err := parseRunTime(p.CompletedAt)
	if err != nil {
		return nil, fmt.Errorf("completed_at: %w", err)
	}

	outcome, outcomeDetail := projectRunOutcome(p.Outcome)

	out := make([]Input, 0, len(p.Conversation))
	for i, e := range p.Conversation {
		occurredAt := completedMillis
		if e.At != "" {
			atMillis, aerr := parseRunTime(e.At)
			if aerr != nil {
				return nil, fmt.Errorf("conversation[%d].at: %w", i, aerr)
			}
			occurredAt = atMillis
		}
		out = append(out, Input{
			TenantID:      p.TenantID, // handler overrides with the verified scope tenant (D-124/D-153)
			UserID:        p.UserID,   // fills the record's user dimension only when the scope leaves it empty (D-124)
			SessionID:     p.SessionID,
			Role:          e.Role,
			Content:       e.Content,
			SourceAgent:   p.AgentID,
			Outcome:       outcome,
			OutcomeDetail: outcomeDetail,
			OccurredAt:    occurredAt,
		})
	}
	return out, nil
}

// parseRunTime parses an RFC3339 run timestamp into unix millis. An empty string
// returns (0, nil) — the caller treats 0 as "unset" (records.New then stamps
// now()); a non-empty unparseable value returns ErrBadRunTimestamp so the whole
// payload is rejected rather than silently stamped with a zero time (P1).
// time.RFC3339 accepts a fractional-second input (Harbor marshals RFC3339Nano),
// so goal/step timestamps with sub-second precision parse cleanly.
func parseRunTime(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrBadRunTimestamp, err)
	}
	return t.UnixMilli(), nil
}

// projectRunOutcome maps Harbor's run outcome vocabulary
// (goal/no_path/constraints_conflict/cancelled/deadline_exceeded/error) onto the
// records outcome axis. The records table's outcome column is CHECK-constrained
// to {”, 'success', 'failure'} (day-one schema) and the Phase-19 reflection
// sweep keys off exactly that success/failure axis, so the Harbor outcome cannot
// be stored verbatim in `outcome`. It is projected — only "goal" is a success,
// every other terminal outcome is a failure — and the PRECISE run outcome is
// preserved verbatim in the free-text `outcome_detail` column (no CHECK), so the
// D-024 day-one signal is captured losslessly and stays queryable. An empty
// outcome maps to the empty (untagged) record. This split is a documented plan
// deviation (the plan mapping table's `outcome -> Outcome` row): the column
// choice is dictated by the schema constraint, the signal is not lost.
func projectRunOutcome(o string) (outcome, detail string) {
	switch o {
	case "":
		return "", ""
	case "goal":
		return "success", o
	default:
		return "failure", o
	}
}
