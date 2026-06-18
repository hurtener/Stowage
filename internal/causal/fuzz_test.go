package causal

import (
	"encoding/json"
	"testing"
)

// FuzzCausalProposals fuzzes the inference decode + GateProposals gate — the
// JSON-unmarshal/index-mapping surface (§11, phase-24 plan). It feeds arbitrary
// bytes through the same `{"links":[...]}` unmarshal the gateway response takes, then
// asserts GateProposals' invariant: every survivor has distinct in-range indices and
// confidence ≥ threshold, with no duplicate (from,to) pair. A panic or a violated
// invariant fails the fuzz.
func FuzzCausalProposals(f *testing.F) {
	f.Add([]byte(`{"links":[{"from_idx":0,"to_idx":1,"confidence":0.9,"reason":"x"}]}`))
	f.Add([]byte(`{"links":[{"from_idx":-1,"to_idx":99,"confidence":2,"reason":""}]}`))
	f.Add([]byte(`{"links":[{"from_idx":1,"to_idx":1,"confidence":0.5}]}`))
	f.Add([]byte(`{"links":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"links":[{"from_idx":0,"to_idx":1,"confidence":0.7},{"from_idx":0,"to_idx":1,"confidence":0.8}]}`))

	const n = 5
	const threshold = 0.6
	f.Fuzz(func(t *testing.T, data []byte) {
		var out inferOut
		if err := json.Unmarshal(data, &out); err != nil {
			return // malformed input — a clean decode error is acceptable
		}
		gated := GateProposals(out.Links, n, threshold)
		seen := map[[2]int]bool{}
		for _, p := range gated {
			if p.FromIdx < 0 || p.ToIdx < 0 || p.FromIdx >= n || p.ToIdx >= n {
				t.Fatalf("GateProposals returned an out-of-range index: %+v (n=%d)", p, n)
			}
			if p.FromIdx == p.ToIdx {
				t.Fatalf("GateProposals returned a self-edge: %+v", p)
			}
			if p.Confidence < threshold {
				t.Fatalf("GateProposals returned a below-threshold edge: %+v (thr=%v)", p, threshold)
			}
			key := [2]int{p.FromIdx, p.ToIdx}
			if seen[key] {
				t.Fatalf("GateProposals returned a duplicate (from,to) pair: %+v", p)
			}
			seen[key] = true
		}
	})
}
