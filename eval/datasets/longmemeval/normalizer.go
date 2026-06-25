// Package longmemeval normalizes the LongMemEval dataset to the common
// eval/datasets shape (Conversation + Question).
//
// LongMemEval wire format (HuggingFace xiaowu0162/longmemeval-cleaned):
//
//	[{
//	  "question_id": "...",
//	  "question": "...",
//	  "answer": "...",
//	  "question_type": "...",
//	  "haystack_dates": ["2024-01-01", ...],
//	  "haystack_sessions": [[{"role":"user","content":"..."},...],...],
//	  "evidence_list": ["session_idx:turn_idx", ...]
//	}]
//
// Normalizer produces one Conversation per haystack_sessions array,
// grouped by question_id. Questions reference the conversation by ID.
package longmemeval

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hurtener/stowage/eval/datasets"
)

// rawItem is one row in the LongMemEval JSON array.
type rawItem struct {
	QuestionID       string      `json:"question_id"`
	Question         string      `json:"question"`
	Answer           any         `json:"answer"` // string OR number in the real dataset
	QuestionType     string      `json:"question_type"`
	QuestionDate     string      `json:"question_date"` // the reference "now" the question is asked at
	HaystackDates    []string    `json:"haystack_dates"`
	HaystackSessions [][]rawTurn `json:"haystack_sessions"`
	EvidenceList     []string    `json:"evidence_list"`
}

type rawTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Normalize reads a LongMemEval JSON array from r and returns the
// (conversations, questions) pair for the harness.
func Normalize(r io.Reader) ([]datasets.Conversation, []datasets.Question, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, fmt.Errorf("longmemeval: read: %w", err)
	}
	var items []rawItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, nil, fmt.Errorf("longmemeval: parse: %w", err)
	}
	return normalize(items)
}

func normalize(items []rawItem) ([]datasets.Conversation, []datasets.Question, error) {
	convs := make([]datasets.Conversation, 0, len(items))
	qs := make([]datasets.Question, 0, len(items))

	for _, item := range items {
		convID := "lme-" + item.QuestionID
		conv := datasets.Conversation{
			ID:       convID,
			Sessions: make([]datasets.Session, 0, len(item.HaystackSessions)),
		}
		for si, sess := range item.HaystackSessions {
			session := datasets.Session{
				ID:    fmt.Sprintf("%s-s%d", convID, si),
				Turns: make([]datasets.Turn, 0, len(sess)),
			}
			// Parse the real session timestamp if available (the dataset uses
			// "2023/04/10 (Mon) 17:50" — minute granularity, which preserves true
			// intervals for temporal-reasoning questions and within-day ordering for
			// same-day corrections, D-109). Fall back to an ordered synthetic date.
			var base time.Time
			if si < len(item.HaystackDates) {
				base = parseHaystackDate(item.HaystackDates[si])
			}
			if base.IsZero() {
				base = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, si)
			}
			for ti, turn := range sess {
				session.Turns = append(session.Turns, datasets.Turn{
					Role:      turn.Role,
					Content:   turn.Content,
					Timestamp: base.Add(time.Duration(ti) * time.Minute),
				})
			}
			conv.Sessions = append(conv.Sessions, session)
		}
		convs = append(convs, conv)

		// Normalize the question date to YYYY-MM-DD (the reader's daily granularity, matching
		// the "| When:" memory dates). The dataset uses "2023/05/20 (Sat) 02:21"; fall back to
		// the raw trimmed string if it doesn't parse, or empty when absent.
		qDate := ""
		if item.QuestionDate != "" {
			if t := parseHaystackDate(item.QuestionDate); !t.IsZero() {
				qDate = t.UTC().Format("2006-01-02")
			} else {
				qDate = strings.TrimSpace(item.QuestionDate)
			}
		}
		q := datasets.Question{
			ID:       item.QuestionID,
			Text:     item.Question,
			ConvID:   convID,
			Category: item.QuestionType,
			Date:     qDate,
			Expected: datasets.Expected{
				Answer:      stringifyAnswer(item.Answer),
				EvidenceIDs: item.EvidenceList,
			},
		}
		qs = append(qs, q)
	}
	return convs, qs, nil
}

// stringifyAnswer renders the dataset's answer field (string or number in
// the wild — found on first real-dataset contact) as a string.
func stringifyAnswer(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// haystackDateFormats are the layouts tried, in order, against a LongMemEval
// haystack_dates entry. The dataset uses "2023/04/10 (Mon) 17:50" (minute
// granularity); the others are defensive fallbacks (D-109).
var haystackDateFormats = []string{
	"2006/01/02 (Mon) 15:04",
	"2006/01/02 (Mon) 15:04:05",
	"2006-01-02 15:04",
	"2006/01/02",
	"2006-01-02",
}

// parseHaystackDate parses a LongMemEval session timestamp, returning the zero
// time if no known layout matches (caller falls back to a synthetic ordered date).
func parseHaystackDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, f := range haystackDateFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
