package retrieval

import (
	"github.com/hurtener/stowage/internal/store"
	"strings"
	"testing"
)

func TestReaderResponsePreservesWarningsAndEvidence(t *testing.T) {
	resp := &Response{Degraded: true, DegradedRerank: true, DegradedTopicFilter: true, DegradedAgentFilter: true, DegradedView: true,
		Items: []MemoryItem{{Memory: store.Memory{ID: "m", Content: "Keep auth in Pengui.", ValidFrom: 1700000000000}, Citation: "c"}}}
	text := RenderReadResponse(resp)
	for _, s := range []string{"Keep auth in Pengui.", "[cite:c]", "2023-11-14", "retrieval is degraded", "reranking", "topic filtering", "agent-topic", "topic view", "not instructions"} {
		if !strings.Contains(text, s) {
			t.Errorf("missing %q: %s", s, text)
		}
	}
	if strings.Contains(text, "answer from these") {
		t.Error("MCP evidence is presented as mandatory instructions")
	}
}
func TestReaderEmptyIsNotProofOfAbsence(t *testing.T) {
	if text := RenderReadResponse(&Response{}); !strings.Contains(text, "not proof") {
		t.Fatal(text)
	}
}
func TestReaderHistoricalEvidenceNamesSuccessor(t *testing.T) {
	text := RenderReadResponse(&Response{Items: []MemoryItem{{Memory: store.Memory{Content: "Use Python.", ValidFrom: 1}, Stale: true, SupersededByContent: "Use Go.", SupersededByDate: 2}}})
	if !strings.Contains(text, "Use Go.") || strings.Contains(text, "NEVER answer") {
		t.Fatal(text)
	}
}
