// Package gain defines the Stowage gain-harness scenario format (Phase 13 skeleton).
//
// A gain scenario measures whether memory improves task completion over a
// baseline (no-memory) run. The full Harbor-fleet loop is Phase 20; this
// package ships the format and three seed scenarios exercisable in CI mode.
package gain

import (
	"encoding/json"
	"fmt"
	"os"
)

// Scenario is one gain-measurement scenario.
type Scenario struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Category    string `json:"category"` // "preference", "multi_session", "update"
	// Turns are the conversation turns to ingest before the evaluation question.
	Turns []Turn `json:"turns"`
	// EvalQuestion is the question asked after ingestion.
	EvalQuestion string `json:"eval_question"`
	// ExpectedAnswer is the substring expected in the retrieved context.
	ExpectedAnswer string `json:"expected_answer"`
}

// Turn is one message in a scenario conversation.
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LoadScenario loads a scenario from a JSON file.
func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("load scenario %s: %w", path, err)
	}
	var s Scenario
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse scenario %s: %w", path, err)
	}
	return &s, nil
}
