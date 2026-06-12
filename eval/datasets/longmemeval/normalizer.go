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
	"time"

	"github.com/hurtener/stowage/eval/datasets"
)

// rawItem is one row in the LongMemEval JSON array.
type rawItem struct {
	QuestionID       string      `json:"question_id"`
	Question         string      `json:"question"`
	Answer           any         `json:"answer"` // string OR number in the real dataset
	QuestionType     string      `json:"question_type"`
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
			// Parse date for this session if available.
			var base time.Time
			if si < len(item.HaystackDates) {
				if t, err := time.Parse("2006-01-02", item.HaystackDates[si]); err == nil {
					base = t
				}
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

		q := datasets.Question{
			ID:       item.QuestionID,
			Text:     item.Question,
			ConvID:   convID,
			Category: item.QuestionType,
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
