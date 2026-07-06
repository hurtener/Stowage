package records

// runsink_test.go — the FromRunCompletion converter core (phase ae12, D-153):
// the mapping table, the at-else-completed_at rule, the reject paths, and a fuzz
// target over the prime decode surface (§11).

import (
	"errors"
	"testing"
	"time"
)

// mustMillis parses an RFC3339 string to unix millis for the expected values.
func mustMillis(t *testing.T, s string) int64 {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("mustMillis(%q): %v", s, err)
	}
	return ts.UnixMilli()
}

// validPayload returns a minimal well-formed format_version-1 RunCompletion the
// per-test cases mutate.
func validPayload() RunCompletion {
	return RunCompletion{
		FormatVersion: 1,
		TenantID:      "acme",
		UserID:        "alice",
		SessionID:     "s1",
		RunID:         "run-123",
		AgentID:       "agent-x",
		Outcome:       "goal",
		StartedAt:     "2026-07-06T10:00:00Z",
		CompletedAt:   "2026-07-06T10:05:00Z",
		DurationMS:    300000,
		StepCount:     2,
		Conversation: []RunCompletionEntry{
			{Role: "user", Kind: "goal", Content: "help me plan", Step: 0},
			{Role: "assistant", Kind: "tool", Content: "planning: ok", Step: 0, At: "2026-07-06T10:02:00Z"},
		},
	}
}

func TestFromRunCompletion_MappingTable(t *testing.T) {
	p := validPayload()
	inputs, err := FromRunCompletion(p)
	if err != nil {
		t.Fatalf("FromRunCompletion: %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("want 2 inputs, got %d", len(inputs))
	}

	// Entry 0: no `at` → OccurredAt falls back to completed_at.
	got0 := inputs[0]
	if got0.Role != "user" || got0.Content != "help me plan" {
		t.Errorf("entry0 role/content wrong: %+v", got0)
	}
	if got0.OccurredAt != mustMillis(t, p.CompletedAt) {
		t.Errorf("entry0 OccurredAt = %d, want completed_at %d", got0.OccurredAt, mustMillis(t, p.CompletedAt))
	}
	// Entry 1: `at` set → OccurredAt is the per-entry assertion time.
	got1 := inputs[1]
	if got1.OccurredAt != mustMillis(t, "2026-07-06T10:02:00Z") {
		t.Errorf("entry1 OccurredAt = %d, want at %d", got1.OccurredAt, mustMillis(t, "2026-07-06T10:02:00Z"))
	}

	// Shared stamps on every record.
	for i, in := range inputs {
		if in.SessionID != "s1" {
			t.Errorf("entry%d SessionID = %q, want s1", i, in.SessionID)
		}
		if in.SourceAgent != "agent-x" {
			t.Errorf("entry%d SourceAgent = %q, want agent-x", i, in.SourceAgent)
		}
		if in.TenantID != "acme" || in.UserID != "alice" {
			t.Errorf("entry%d tenant/user = %q/%q, want acme/alice", i, in.TenantID, in.UserID)
		}
		// goal → success on the constrained `outcome` axis, verbatim in detail.
		if in.Outcome != "success" || in.OutcomeDetail != "goal" {
			t.Errorf("entry%d outcome/detail = %q/%q, want success/goal", i, in.Outcome, in.OutcomeDetail)
		}
	}
}

func TestFromRunCompletion_OutcomeProjection(t *testing.T) {
	cases := []struct {
		in         string
		wantOut    string
		wantDetail string
	}{
		{"goal", "success", "goal"},
		{"no_path", "failure", "no_path"},
		{"constraints_conflict", "failure", "constraints_conflict"},
		{"cancelled", "failure", "cancelled"},
		{"deadline_exceeded", "failure", "deadline_exceeded"},
		{"error", "failure", "error"},
		{"", "", ""},
		{"some_future_outcome", "failure", "some_future_outcome"},
	}
	for _, tc := range cases {
		out, detail := projectRunOutcome(tc.in)
		if out != tc.wantOut || detail != tc.wantDetail {
			t.Errorf("projectRunOutcome(%q) = %q/%q, want %q/%q", tc.in, out, detail, tc.wantOut, tc.wantDetail)
		}
		// ValidOutcomes must accept whatever we project onto the `outcome` column.
		if !ValidOutcomes[out] {
			t.Errorf("projectRunOutcome(%q) produced outcome %q not in ValidOutcomes", tc.in, out)
		}
	}
}

func TestFromRunCompletion_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*RunCompletion)
		wantErr error
	}{
		{"format_version 2", func(p *RunCompletion) { p.FormatVersion = 2 }, ErrUnsupportedRunFormat},
		{"format_version 0", func(p *RunCompletion) { p.FormatVersion = 0 }, ErrUnsupportedRunFormat},
		{"empty run_id", func(p *RunCompletion) { p.RunID = "" }, ErrEmptyRunID},
		{"empty conversation", func(p *RunCompletion) { p.Conversation = nil }, ErrEmptyConversation},
		{"bad completed_at", func(p *RunCompletion) { p.CompletedAt = "not-a-time" }, ErrBadRunTimestamp},
		{"bad started_at", func(p *RunCompletion) { p.StartedAt = "13 o'clock" }, ErrBadRunTimestamp},
		{"bad entry at", func(p *RunCompletion) { p.Conversation[1].At = "soon" }, ErrBadRunTimestamp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validPayload()
			tc.mutate(&p)
			_, err := FromRunCompletion(p)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestFromRunCompletion_EmptyTimestampsFallBack asserts empty started/completed
// are tolerated (parse-when-non-empty), leaving OccurredAt 0 so records.New
// stamps now() — never a rejected happy path over an omitted optional time.
func TestFromRunCompletion_EmptyTimestampsFallBack(t *testing.T) {
	p := validPayload()
	p.StartedAt = ""
	p.CompletedAt = ""
	p.Conversation = []RunCompletionEntry{{Role: "user", Content: "x"}}
	inputs, err := FromRunCompletion(p)
	if err != nil {
		t.Fatalf("FromRunCompletion: %v", err)
	}
	if inputs[0].OccurredAt != 0 {
		t.Errorf("OccurredAt = %d, want 0 (records.New stamps now())", inputs[0].OccurredAt)
	}
}

// FuzzFromRunCompletion drives the prime decode surface (§11): no panic, and on
// success exactly one Input per conversation entry (D-153 §3 in-order 1:1).
func FuzzFromRunCompletion(f *testing.F) {
	f.Add(1, "run-1", "2026-07-06T10:00:00Z", "2026-07-06T10:05:00Z", 3, "user", "hi", "goal", "2026-07-06T10:02:00Z")
	f.Add(2, "", "bad", "", 0, "assistant", "", "error", "")
	f.Add(1, "r", "", "", 5, "", "content", "no_path", "nope")

	f.Fuzz(func(t *testing.T, fv int, runID, startedAt, completedAt string, count int, role, content, outcome, at string) {
		n := count % 8
		if n < 0 {
			n = -n
		}
		conv := make([]RunCompletionEntry, n)
		for i := range conv {
			conv[i] = RunCompletionEntry{Role: role, Kind: "k", Content: content, Step: i, At: at}
		}
		p := RunCompletion{
			FormatVersion: fv,
			RunID:         runID,
			Outcome:       outcome,
			StartedAt:     startedAt,
			CompletedAt:   completedAt,
			Conversation:  conv,
		}
		inputs, err := FromRunCompletion(p)
		if err != nil {
			return // a rejection is a valid outcome; the invariant is no panic.
		}
		if len(inputs) != len(conv) {
			t.Fatalf("len(inputs)=%d != len(conversation)=%d on success", len(inputs), len(conv))
		}
	})
}
