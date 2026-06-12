package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CIFixtures holds all CI fixture data loaded from eval/ci-fixtures/.
type CIFixtures struct {
	Conversations []ConvFixture
	Questions     []QuestionFixture
}

// ConvFixture is one conversation from eval/ci-fixtures/conversations/.
type ConvFixture struct {
	ID       string           `json:"id"`
	Category string           `json:"category"`
	Sessions []SessionFixture `json:"sessions"`
	// MockScriptTemplate is the raw JSON array template (with {{.RN}} placeholders).
	// Loaded from eval/ci-fixtures/mock-scripts/{conv-id}.json
	MockScriptTemplate []byte
}

// SessionFixture is one session within a ConvFixture.
type SessionFixture struct {
	ID    string        `json:"id"`
	Turns []TurnFixture `json:"turns"`
}

// TurnFixture is one turn within a SessionFixture.
type TurnFixture struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// QuestionFixture is one CI eval question.
type QuestionFixture struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	ConvID   string `json:"conv_id"`
	Category string `json:"category"`
	Expected struct {
		Answer string `json:"answer"`
	} `json:"expected"`
}

// LoadCIFixtures loads all CI fixture data from the given dir.
// dir should be the path to eval/ci-fixtures/.
func LoadCIFixtures(dir string) (*CIFixtures, error) {
	// Load questions.
	qData, err := os.ReadFile(filepath.Join(dir, "questions.json")) //nolint:gosec // test-fixture path from test harness
	if err != nil {
		return nil, fmt.Errorf("load questions: %w", err)
	}
	var questions []QuestionFixture
	if err := json.Unmarshal(qData, &questions); err != nil {
		return nil, fmt.Errorf("parse questions: %w", err)
	}

	// Load conversations.
	convDir := filepath.Join(dir, "conversations")
	entries, err := os.ReadDir(convDir)
	if err != nil {
		return nil, fmt.Errorf("read conversations dir: %w", err)
	}

	var convs []ConvFixture
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(convDir, entry.Name())) //nolint:gosec // test-fixture path from test harness
		if err != nil {
			return nil, fmt.Errorf("read conversation %s: %w", entry.Name(), err)
		}
		var conv ConvFixture
		if err := json.Unmarshal(data, &conv); err != nil {
			return nil, fmt.Errorf("parse conversation %s: %w", entry.Name(), err)
		}
		// Load mock script template.
		scriptPath := filepath.Join(dir, "mock-scripts", entry.Name())
		script, err := os.ReadFile(scriptPath) //nolint:gosec // test-fixture path from test harness
		if err != nil {
			return nil, fmt.Errorf("read mock script %s: %w", entry.Name(), err)
		}
		conv.MockScriptTemplate = script
		convs = append(convs, conv)
	}

	// Sort conversations by ID for determinism.
	sort.Slice(convs, func(i, j int) bool {
		return convs[i].ID < convs[j].ID
	})

	return &CIFixtures{
		Conversations: convs,
		Questions:     questions,
	}, nil
}

// RenderMockScript substitutes {{.RN}} placeholders with actual record IDs.
// ids is a slice of all record IDs in order (R1=ids[0], R2=ids[1], ...).
func RenderMockScript(template []byte, ids []string) []byte {
	result := string(template)
	for i, id := range ids {
		placeholder := fmt.Sprintf("{{.R%d}}", i+1)
		result = strings.ReplaceAll(result, placeholder, id)
	}
	return []byte(result)
}
