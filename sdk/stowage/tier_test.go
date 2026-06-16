package stowage_test

// tier_test.go — AC-2 (D-071): the single-user boundary is ENFORCED, not just
// documented. The SDK Client interface must expose every Tier-A control verb and
// must NOT expose any Tier-B (multi-user/admin) verb — grants/group/membership
// management or contribute-mode. A reflection check over the interface method set
// fails the build if a Tier-B verb ever leaks onto the embedded SDK (D-067).

import (
	"reflect"
	"strings"
	"testing"

	stowage "github.com/hurtener/stowage/sdk/stowage"
)

func TestClientTierBoundary(t *testing.T) {
	typ := reflect.TypeOf((*stowage.Client)(nil)).Elem()
	methods := make(map[string]bool, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		methods[typ.Method(i).Name] = true
	}

	// Tier-B verbs (multi-user/admin) must be ABSENT from the single-user SDK.
	forbidden := []string{"Group", "Grant", "Member", "Contribute"}
	for name := range methods {
		for _, f := range forbidden {
			if strings.Contains(name, f) {
				t.Errorf("Tier-B verb leaked onto the SDK Client: %s (contains %q) — multi-user verbs are {HTTP, MCP} only (D-071)", name, f)
			}
		}
	}

	// Tier-A verbs (single-user control) must be PRESENT.
	for _, want := range []string{
		"UpsertTopics", "DeleteTopic", "Flush",
		"ForkBranch", "MergeBranch", "DiscardBranch", "Assert",
	} {
		if !methods[want] {
			t.Errorf("Tier-A verb missing from the SDK Client: %s (must be reachable on {SDK, MCP, HTTP}; D-071)", want)
		}
	}
}
