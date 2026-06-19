package proactive

import (
	"context"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
)

// FuzzProactiveConfig drives arbitrary stored-governance JSON through Resolve — the
// prime decode surface (RFC §6d). Invariant: Resolve never panics and always
// returns a clamped, sane Config (threshold/budget in range, non-nil classes); a
// malformed payload fails safe to OFF, never to an enabled-with-garbage state.
func FuzzProactiveConfig(f *testing.F) {
	f.Add(`{"enabled":true,"threshold":0.5,"budget":2,"classes":{"expiring":true}}`)
	f.Add(`{"enabled":false}`)
	f.Add(`{"threshold":-9,"budget":99999}`)
	f.Add(`{not json`)
	f.Add(``)
	f.Add(`{"classes":null}`)
	f.Add(`[]`)

	def := Config{Enabled: true, Threshold: 0.5, Budget: 2, Classes: map[string]bool{ClassExpiring: true}}
	f.Fuzz(func(t *testing.T, raw string) {
		ss := &fakeScopeSettings{vals: map[string]string{settingKey: raw}}
		got, err := Resolve(context.Background(), ss, identity.Scope{Tenant: "t"}, def)
		if err != nil {
			t.Fatalf("Resolve errored on stored payload %q: %v", raw, err)
		}
		if got.Threshold < 0 {
			t.Errorf("threshold not floored: %v (input %q)", got.Threshold, raw)
		}
		if got.Budget < 0 || got.Budget > 20 {
			t.Errorf("budget out of range: %v (input %q)", got.Budget, raw)
		}
		if got.Classes == nil {
			t.Errorf("classes nil after Resolve (input %q)", raw)
		}
	})
}
