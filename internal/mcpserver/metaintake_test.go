package mcpserver

// metaintake_test.go — Phase ae2 (D-137/D-138) coverage for the shared _meta
// intake seam: the tenant guard (absent/equal/mismatch/non-string), the
// metaElseArg precedence table, metaString extraction, and a concurrent-reuse
// proof under -race.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/identity"
)

// TestMetaIntake_TenantGuard covers the D-138 fail-closed guard: absent tenant
// is a no-op, an equal tenant is a no-op, a mismatched or non-string tenant
// rejects with identity.ErrTenantMismatch.
func TestMetaIntake_TenantGuard(t *testing.T) {
	tests := []struct {
		name       string
		meta       map[string]any
		credTenant string
		wantErr    bool
	}{
		{"no _meta at all (nil map)", nil, "acme", false},
		{"_meta present but no tenant key", map[string]any{"user": "u1"}, "acme", false},
		{"_meta.tenant absent", map[string]any{"user": "u1"}, "acme", false},
		{"_meta.tenant equal to credential", map[string]any{"tenant": "acme"}, "acme", false},
		{"_meta.tenant mismatched", map[string]any{"tenant": "other"}, "acme", true},
		{"_meta.tenant non-string", map[string]any{"tenant": 123}, "acme", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.meta != nil {
				ctx = server.WithRequestMeta(ctx, tt.meta)
			}
			_, err := readMetaIdentity(ctx, tt.credTenant)
			if (err != nil) != tt.wantErr {
				t.Fatalf("readMetaIdentity() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, identity.ErrTenantMismatch) {
				t.Errorf("error %v does not wrap identity.ErrTenantMismatch", err)
			}
		})
	}
}

// TestMetaIntake_TenantMismatchIsRedacted proves the rejected error's message
// leaks neither the injected nor the credential tenant value (D-138 redaction).
func TestMetaIntake_TenantMismatchIsRedacted(t *testing.T) {
	ctx := server.WithRequestMeta(context.Background(), map[string]any{"tenant": "attacker-tenant"})
	_, err := readMetaIdentity(ctx, "victim-tenant")
	if err == nil {
		t.Fatal("expected a mismatch error")
	}
	msg := err.Error()
	if strings.Contains(msg, "attacker-tenant") || strings.Contains(msg, "victim-tenant") {
		t.Errorf("error message %q leaks a tenant value", msg)
	}
}

// TestMetaIntake_Extraction covers clean extraction of user/session/agent_id/
// project, and defensive handling of a missing or non-string value (each
// falls back to "" rather than erroring — only tenant fails closed).
func TestMetaIntake_Extraction(t *testing.T) {
	ctx := server.WithRequestMeta(context.Background(), map[string]any{
		"user":     "u1",
		"session":  "s1",
		"agent_id": "a1",
		"project":  "p1",
	})
	mi, err := readMetaIdentity(ctx, "acme")
	if err != nil {
		t.Fatalf("readMetaIdentity: %v", err)
	}
	if mi.User != "u1" || mi.Session != "s1" || mi.Agent != "a1" || mi.Project != "p1" {
		t.Errorf("got %+v, want User=u1 Session=s1 Agent=a1 Project=p1", mi)
	}
}

// TestMetaIntake_ProjectExtraction covers ae2b's M1 addition in isolation:
// _meta.project present, absent, and non-string (present/absent/non-string,
// mirroring the existing user/session/agent_id cases).
func TestMetaIntake_ProjectExtraction(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want string
	}{
		{"present", map[string]any{"project": "p1"}, "p1"},
		{"absent", map[string]any{"user": "u1"}, ""},
		{"non-string", map[string]any{"project": 123}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := server.WithRequestMeta(context.Background(), tt.meta)
			mi, err := readMetaIdentity(ctx, "acme")
			if err != nil {
				t.Fatalf("readMetaIdentity: %v", err)
			}
			if mi.Project != tt.want {
				t.Errorf("mi.Project = %q, want %q", mi.Project, tt.want)
			}
		})
	}
}

func TestMetaIntake_ExtractionDefensive(t *testing.T) {
	ctx := server.WithRequestMeta(context.Background(), map[string]any{
		"user":     42,  // non-string
		"agent_id": nil, // nil value
		"project":  7,   // non-string
		// session key absent entirely
	})
	mi, err := readMetaIdentity(ctx, "acme")
	if err != nil {
		t.Fatalf("readMetaIdentity: %v", err)
	}
	if mi != (metaIdentity{}) {
		t.Errorf("got %+v, want zero value (all malformed/absent)", mi)
	}
}

func TestMetaIntake_NilMeta(t *testing.T) {
	mi, err := readMetaIdentity(context.Background(), "acme")
	if err != nil {
		t.Fatalf("readMetaIdentity: %v", err)
	}
	if mi != (metaIdentity{}) {
		t.Errorf("got %+v, want zero value when no _meta was sent", mi)
	}
}

// TestMetaElseArg_Precedence is the truth table for the documented precedence
// rule (AC-2): _meta wins when present; the arg is the fallback; metaElseArg
// with meta=="" reproduces today's arg-only behaviour exactly (AC-5).
func TestMetaElseArg_Precedence(t *testing.T) {
	tests := []struct {
		name string
		meta string
		arg  string
		want string
	}{
		{"both set: meta wins", "u_meta", "u_arg", "u_meta"},
		{"only meta set", "u_meta", "", "u_meta"},
		{"only arg set: arg is fallback", "", "u_arg", "u_arg"},
		{"neither set", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metaElseArg(tt.meta, tt.arg); got != tt.want {
				t.Errorf("metaElseArg(%q, %q) = %q, want %q", tt.meta, tt.arg, got, tt.want)
			}
		})
	}
}

// TestMetaString covers the defensive map read directly.
func TestMetaString(t *testing.T) {
	if got := metaString(nil, "user"); got != "" {
		t.Errorf("metaString(nil, ...) = %q, want empty", got)
	}
	m := map[string]any{"user": "u1", "bad": 7}
	if got := metaString(m, "missing"); got != "" {
		t.Errorf("metaString(missing key) = %q, want empty", got)
	}
	if got := metaString(m, "bad"); got != "" {
		t.Errorf("metaString(non-string value) = %q, want empty", got)
	}
	if got := metaString(m, "user"); got != "u1" {
		t.Errorf("metaString(user) = %q, want u1", got)
	}
}

// TestMetaIntake_ConcurrentReuse proves readMetaIdentity/metaElseArg/metaString
// are safe under concurrent use from N goroutines on independent contexts
// (-race), matching the handlers' existing statelessness (no receiver state,
// no package-level mutable state).
func TestMetaIntake_ConcurrentReuse(t *testing.T) {
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			ctx := server.WithRequestMeta(context.Background(), map[string]any{
				"user": "u", "session": "s", "agent_id": "a", "tenant": "acme",
			})
			mi, err := readMetaIdentity(ctx, "acme")
			if err != nil {
				t.Errorf("goroutine %d: readMetaIdentity: %v", i, err)
				return
			}
			if metaElseArg(mi.User, "fallback") != "u" {
				t.Errorf("goroutine %d: unexpected metaElseArg result", i)
			}
		}()
	}
	wg.Wait()
}
