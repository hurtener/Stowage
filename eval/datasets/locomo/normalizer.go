// Package locomo normalizes the LoCoMo dataset to the common eval/datasets shape.
//
// LoCoMo wire format (github.com/snap-research/locomo data/locomo10.json):
//
//	{
//	  "conversation_id": {
//	    "conversation": [
//	      {"speaker": "...", "text": "...", "timestamp": "..."},
//	      ...
//	    ],
//	    "question_answer_pairs": {
//	      "q_id": {
//	        "question": "...",
//	        "answer": "...",
//	        "category": "...",
//	        "evidence": [...]
//	      }
//	    }
//	  }
//	}
package locomo

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/hurtener/stowage/eval/datasets"
)

// rawDoc is the top-level LoCoMo JSON object (map from conv ID to rawConv).
type rawDoc map[string]rawConv

type rawConv struct {
	Conversation        []rawTurn        `json:"conversation"`
	QuestionAnswerPairs map[string]rawQA `json:"question_answer_pairs"`
}

type rawTurn struct {
	Speaker   string `json:"speaker"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
}

type rawQA struct {
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Category string   `json:"category"`
	Evidence []string `json:"evidence"`
}

// Normalize reads a LoCoMo JSON object from r and returns (conversations, questions).
func Normalize(r io.Reader) ([]datasets.Conversation, []datasets.Question, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, fmt.Errorf("locomo: read: %w", err)
	}
	var doc rawDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("locomo: parse: %w", err)
	}
	return normalize(doc)
}

func normalize(doc rawDoc) ([]datasets.Conversation, []datasets.Question, error) {
	convs := make([]datasets.Conversation, 0, len(doc))
	qs := make([]datasets.Question, 0)

	for convID, rawC := range doc {
		conv := datasets.Conversation{
			ID:       "loc-" + convID,
			Sessions: normalizeSessions(convID, rawC.Conversation),
		}
		convs = append(convs, conv)

		for qID, qa := range rawC.QuestionAnswerPairs {
			qs = append(qs, datasets.Question{
				ID:       "loc-" + convID + "-" + qID,
				Text:     qa.Question,
				ConvID:   "loc-" + convID,
				Category: qa.Category,
				Expected: datasets.Expected{
					Answer:      qa.Answer,
					EvidenceIDs: qa.Evidence,
				},
			})
		}
	}
	return convs, qs, nil
}

// normalizeSessions splits a flat LoCoMo conversation into sessions by
// detecting day boundaries in the timestamps. Each day = one session.
func normalizeSessions(convID string, turns []rawTurn) []datasets.Session {
	if len(turns) == 0 {
		return nil
	}

	var sessions []datasets.Session
	var cur datasets.Session
	var lastDay string

	for i, t := range turns {
		day := dayOf(t.Timestamp)
		if i == 0 || day != lastDay {
			if i > 0 && len(cur.Turns) > 0 {
				sessions = append(sessions, cur)
			}
			cur = datasets.Session{
				ID: fmt.Sprintf("loc-%s-d%d", convID, len(sessions)),
			}
			lastDay = day
		}
		role := "user"
		if t.Speaker == "Assistant" || t.Speaker == "assistant" {
			role = "assistant"
		}
		ts := parseTimestamp(t.Timestamp)
		cur.Turns = append(cur.Turns, datasets.Turn{
			Role:      role,
			Content:   t.Text,
			Timestamp: ts,
		})
	}
	if len(cur.Turns) > 0 {
		sessions = append(sessions, cur)
	}
	return sessions
}

func dayOf(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

func parseTimestamp(ts string) time.Time {
	// Try multiple formats.
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"01/02/2006",
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t
		}
	}
	return time.Time{}
}
