package traces

import (
	"encoding/json"
	"testing"
)

// FuzzTraceVerify fuzzes the signature-verify decode path (§11): arbitrary bundle
// JSON must never panic and Verify must always return a bool (true only for a bundle
// whose signature genuinely checks out). Third-party audit tooling feeds untrusted
// bytes here, so it must be crash-proof.
func FuzzTraceVerify(f *testing.F) {
	f.Add([]byte(`{"trace":{"response_id":"r"},"signed":true,"algorithm":"ed25519","public_key":"AAAA","signature":"AAAA"}`))
	f.Add([]byte(`{"trace":{"response_id":"r"},"signed":false}`))
	f.Add([]byte(`{"signed":true,"algorithm":"hmac"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))

	f.Fuzz(func(t *testing.T, data []byte) {
		var b Bundle
		if json.Unmarshal(data, &b) != nil {
			return // malformed bundle — a clean decode error is acceptable
		}
		_ = Verify(b) // must not panic; result is a bool by construction
	})
}
