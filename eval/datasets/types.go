// Package datasets defines common shapes for eval dataset normalizers.
package datasets

import "time"

// Conversation holds a multi-session conversation for eval ingestion.
type Conversation struct {
	ID       string
	Sessions []Session
}

// Session is one session within a conversation.
type Session struct {
	ID    string
	Turns []Turn
}

// Turn is one message exchange within a session.
type Turn struct {
	Role      string // "user" | "assistant"
	Content   string
	Timestamp time.Time
}

// Question is a single eval question with expected answers.
type Question struct {
	ID       string
	Text     string
	ConvID   string // which Conversation this belongs to
	Category string
	// Date is the reference "now" the question is asked at (YYYY-MM-DD), when the
	// dataset supplies it (LongMemEval question_date). The reader needs it to anchor
	// relative-time questions ("how many days/months since X"); empty = not supplied.
	Date     string
	Expected Expected
}

// Expected holds the ground-truth for scoring.
type Expected struct {
	Answer      string   // normalized substring to match against retrieved content
	EvidenceIDs []string // record or memory IDs for recall@k (dataset-supplied)
}
