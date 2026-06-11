package pipeline_test

import (
	"encoding/json"
	"testing"

	"github.com/hurtener/stowage/internal/pipeline"
)

// FuzzCandidateValidation exercises the candidate JSON parsing + validation
// path with arbitrary inputs. The invariant: no input must cause a panic.
func FuzzCandidateValidation(f *testing.F) {
	// Seed corpus: valid candidate list.
	f.Add(`{"candidates":[{"kind":"fact","content":"test","context":"ctx","entities":[],"keywords":[],"anticipated_queries":["q1","q2","q3"],"importance":3,"confidence":0.8,"provenance":[{"record_id":"r1","span_start":0,"span_end":4}]}]}`)
	// Empty candidates list.
	f.Add(`{"candidates":[]}`)
	// Invalid kind.
	f.Add(`{"candidates":[{"kind":"strategy","content":"x","context":"","entities":[],"keywords":[],"anticipated_queries":[],"importance":1,"confidence":0.5,"provenance":[{"record_id":"r1","span_start":0,"span_end":0}]}]}`)
	// Missing provenance.
	f.Add(`{"candidates":[{"kind":"fact","content":"x","context":"","entities":[],"keywords":[],"anticipated_queries":[],"importance":1,"confidence":0.5,"provenance":[]}]}`)
	// Negative spans.
	f.Add(`{"candidates":[{"kind":"fact","content":"x","context":"","entities":[],"keywords":[],"anticipated_queries":["q"],"importance":1,"confidence":0.5,"provenance":[{"record_id":"r1","span_start":-99,"span_end":-1}]}]}`)
	// Out-of-bounds spans.
	f.Add(`{"candidates":[{"kind":"fact","content":"x","context":"","entities":[],"keywords":[],"anticipated_queries":["q"],"importance":1,"confidence":0.5,"provenance":[{"record_id":"r1","span_start":0,"span_end":999999}]}]}`)
	// Malformed JSON.
	f.Add(`not json`)
	// Empty.
	f.Add(``)
	// Null.
	f.Add(`null`)

	recordSet := map[string]bool{"r1": true, "r2": true}
	recordContents := map[string]string{"r1": "hello world", "r2": "another record"}

	f.Fuzz(func(t *testing.T, input string) {
		var list pipeline.CandidateList
		if err := json.Unmarshal([]byte(input), &list); err != nil {
			return // invalid JSON: parsing failure is acceptable
		}
		// ValidateCandidates must not panic on any input.
		valid, dropped := pipeline.ValidateCandidates(list.Candidates, recordSet, recordContents)
		// Invariant: produced + dropped == total input.
		if len(valid)+dropped != len(list.Candidates) {
			t.Errorf("invariant violated: valid=%d dropped=%d total=%d",
				len(valid), dropped, len(list.Candidates))
		}
	})
}
