package trust

import (
	"context"
	"testing"
)

// FuzzVerifyVerdict fuzzes the verify JSON-verdict unmarshal + coercion surface (§11,
// phase-25 plan). It drives arbitrary gateway-response bytes through Verify (via a
// scripted fake gateway) and asserts the invariant: a successful Verify always yields
// one of the three legal verdicts — never a third state — and never panics. A decode
// failure is an acceptable (clean) error.
func FuzzVerifyVerdict(f *testing.F) {
	f.Add(`{"verdict":"entailed","confidence":0.9,"explanation":"x"}`)
	f.Add(`{"verdict":"not_entailed","confidence":0.2,"explanation":""}`)
	f.Add(`{"verdict":"maybe","confidence":0.5,"explanation":"x"}`)
	f.Add(`{"verdict":"","confidence":3,"explanation":"x"}`)
	f.Add(`{}`)
	f.Add(`not json`)

	cm := []CitedMemory{{ID: "m1", Content: "some memory"}}
	f.Fuzz(func(t *testing.T, respJSON string) {
		gw := &fakeGateway{json: respJSON}
		v, err := Verify(context.Background(), gw, "a claim", cm)
		if err != nil {
			return // malformed gateway JSON — a clean decode error is acceptable
		}
		switch v.Verdict {
		case VerdictEntailed, VerdictNotEntailed, VerdictUnclear:
			// legal
		default:
			t.Fatalf("Verify produced an illegal verdict %q from %q", v.Verdict, respJSON)
		}
	})
}
