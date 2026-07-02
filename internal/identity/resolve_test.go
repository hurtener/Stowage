package identity

// resolve_test.go — Phase ae8 (D-148) coverage for identity.ResolveReadScope:
// the full source-precedence matrix, the D-137 resolution rule (pin/assert),
// the three sentinel branches, the tenant-never-empty P3 property, the
// compatible-args-only byte-identical regression, the D-150 session-never-on-
// Scope row, and a concurrent-reuse proof under -race.

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

// TestResolveReadScope_PrecedenceMatrix (AC-1/AC-2) is the golden matrix over
// every source-precedence combination: JWT-only, _meta-only, args-only,
// mixed, and conflicting. Every branch is a deterministic row.
func TestResolveReadScope_PrecedenceMatrix(t *testing.T) {
	tests := []struct {
		name      string
		src       IdentitySources
		opts      ResolveOptions
		wantUser  string
		wantProj  string
		wantAgent string
		wantSess  string
		wantErr   error
	}{
		{
			name:     "JWT-only: claim user/session resolve, no cred pin",
			src:      IdentitySources{Tenant: "acme", ClaimTenant: "acme", ClaimUser: "u1", ClaimSession: "s1"},
			wantUser: "u1", wantSess: "s1",
		},
		{
			name:     "_meta-only: meta user/session/project/agent resolve",
			src:      IdentitySources{Tenant: "acme", MetaUser: "u1", MetaSession: "s1", MetaProject: "p1", MetaAgent: "a1"},
			wantUser: "u1", wantSess: "s1", wantProj: "p1", wantAgent: "a1",
		},
		{
			name:     "args-only: arg user/session/project resolve (today's behaviour)",
			src:      IdentitySources{Tenant: "acme", ArgUser: "u1", ArgSession: "s1", ArgProject: "p1"},
			wantUser: "u1", wantSess: "s1", wantProj: "p1",
		},
		{
			name:     "mixed: meta project + arg user (no meta user) — arg is the fallback",
			src:      IdentitySources{Tenant: "acme", MetaProject: "p1", ArgUser: "u1"},
			wantUser: "u1", wantProj: "p1",
		},
		{
			name:     "mixed: claim session + meta user + arg project",
			src:      IdentitySources{Tenant: "acme", ClaimSession: "s1", MetaUser: "u1", ArgProject: "p1"},
			wantUser: "u1", wantSess: "s1", wantProj: "p1",
		},
		{
			// AC-1's required row: both the arg AND _meta are present for the
			// SAME dimension — _meta wins (the sub-order ae2 already ships via
			// metaElseArg; NOT covered by the args-only row above).
			name:     "arg-and-_meta-both-present: _meta wins over the arg (ae2 metaElseArg sub-order)",
			src:      IdentitySources{Tenant: "acme", MetaUser: "B", ArgUser: "A"},
			wantUser: "B",
		},
		{
			name:     "claim-over-_meta: verified JWT claim wins over _meta for the same dimension",
			src:      IdentitySources{Tenant: "acme", ClaimUser: "C", MetaUser: "B", ArgUser: "A"},
			wantUser: "C",
		},
		{
			name:     "claim-over-_meta-over-arg for session too",
			src:      IdentitySources{Tenant: "acme", ClaimSession: "C", MetaSession: "B", ArgSession: "A"},
			wantSess: "C",
		},
		{
			name:    "conflicting: claim tenant disagrees with credential tenant -> ErrTenantMismatch",
			src:     IdentitySources{Tenant: "acme", ClaimTenant: "attacker"},
			wantErr: ErrTenantMismatch,
		},
		{
			name:    "conflicting: meta tenant disagrees with credential tenant -> ErrTenantMismatch",
			src:     IdentitySources{Tenant: "acme", MetaTenant: "attacker"},
			wantErr: ErrTenantMismatch,
		},
		{
			name:     "conflicting: meta tenant EQUAL to credential tenant is a no-op",
			src:      IdentitySources{Tenant: "acme", MetaTenant: "acme", MetaUser: "u1"},
			wantUser: "u1",
		},
		{
			name:    "conflicting: cred-pinned user disagrees with arg, no multiplexing -> ErrUserConflict",
			src:     IdentitySources{Tenant: "acme", CredUser: "alice", ArgUser: "bob"},
			wantErr: ErrUserConflict,
		},
		{
			name:     "cred-pinned user matches asserted -> no conflict",
			src:      IdentitySources{Tenant: "acme", CredUser: "alice", ArgUser: "alice"},
			wantUser: "alice",
		},
		{
			name:     "cred-pinned user, no assertion at all -> the pin wins",
			src:      IdentitySources{Tenant: "acme", CredUser: "alice"},
			wantUser: "alice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope, sess, err := ResolveReadScope(tt.src, tt.opts)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ResolveReadScope() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveReadScope() unexpected error: %v", err)
			}
			if scope.Tenant != tt.src.Tenant {
				t.Errorf("Tenant = %q, want %q", scope.Tenant, tt.src.Tenant)
			}
			if scope.User != tt.wantUser {
				t.Errorf("User = %q, want %q", scope.User, tt.wantUser)
			}
			if scope.Project != tt.wantProj {
				t.Errorf("Project = %q, want %q", scope.Project, tt.wantProj)
			}
			if scope.Agent != tt.wantAgent {
				t.Errorf("Agent = %q, want %q", scope.Agent, tt.wantAgent)
			}
			if sess != tt.wantSess {
				t.Errorf("session = %q, want %q", sess, tt.wantSess)
			}
			if scope.Session != "" {
				t.Errorf("Scope.Session = %q, want empty (D-150)", scope.Session)
			}
		})
	}
}

// TestResolveReadScope_Multiplexing (AC-2/AC-5) covers the assertable branch:
// a disagreeing user assertion is accepted under identity.multiplexing=true
// OR the per-credential CanAssertUser capability, and rejected by default.
func TestResolveReadScope_Multiplexing(t *testing.T) {
	base := IdentitySources{Tenant: "acme", CredUser: "alice", ArgUser: "bob"}

	t.Run("default (no mux, no CanAssertUser): rejected", func(t *testing.T) {
		_, _, err := ResolveReadScope(base, ResolveOptions{})
		if !errors.Is(err, ErrUserConflict) {
			t.Fatalf("error = %v, want ErrUserConflict", err)
		}
	})

	t.Run("identity.multiplexing=true: accepted", func(t *testing.T) {
		scope, _, err := ResolveReadScope(base, ResolveOptions{Multiplexing: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if scope.User != "bob" {
			t.Errorf("User = %q, want %q (the assertion should win under multiplexing)", scope.User, "bob")
		}
	})

	t.Run("per-credential CanAssertUser=true: accepted", func(t *testing.T) {
		src := base
		src.CanAssertUser = true
		scope, _, err := ResolveReadScope(src, ResolveOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if scope.User != "bob" {
			t.Errorf("User = %q, want %q", scope.User, "bob")
		}
	})
}

// TestResolveReadScope_StrictPosture (AC-3) covers the presence gate: strict
// posture refuses a read with no user AND no agent, before any store call;
// compatible posture resolves the same input to the tenant-wide scope
// unchanged.
func TestResolveReadScope_StrictPosture(t *testing.T) {
	tenantOnly := IdentitySources{Tenant: "acme"}

	t.Run("strict + no user + no agent -> ErrIdentityRequired", func(t *testing.T) {
		_, _, err := ResolveReadScope(tenantOnly, ResolveOptions{Posture: PostureStrict})
		if !errors.Is(err, ErrIdentityRequired) {
			t.Fatalf("error = %v, want ErrIdentityRequired", err)
		}
	})

	t.Run("compatible + no user + no agent -> tenant-wide scope, no error", func(t *testing.T) {
		scope, _, err := ResolveReadScope(tenantOnly, ResolveOptions{Posture: PostureCompatible})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if scope.User != "" || scope.Agent != "" || scope.Tenant != "acme" {
			t.Errorf("scope = %+v, want tenant-wide {Tenant: acme}", scope)
		}
	})

	t.Run("strict + agent present + no user -> allowed (agent satisfies the presence gate)", func(t *testing.T) {
		src := IdentitySources{Tenant: "acme", MetaAgent: "agent-1"}
		scope, _, err := ResolveReadScope(src, ResolveOptions{Posture: PostureStrict})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if scope.Agent != "agent-1" {
			t.Errorf("Agent = %q, want agent-1", scope.Agent)
		}
	})

	t.Run("strict + user present + no agent -> allowed", func(t *testing.T) {
		src := IdentitySources{Tenant: "acme", ArgUser: "u1"}
		scope, _, err := ResolveReadScope(src, ResolveOptions{Posture: PostureStrict})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if scope.User != "u1" {
			t.Errorf("User = %q, want u1", scope.User)
		}
	})
}

// TestResolveReadScope_SessionNeverOnScope (D-150) proves a session-bearing
// source (including under strict posture) NEVER lands on the returned
// Scope.Session — cross-session recall is preserved on every posture.
func TestResolveReadScope_SessionNeverOnScope(t *testing.T) {
	for _, posture := range []ReadPosture{PostureCompatible, PostureStrict} {
		src := IdentitySources{Tenant: "acme", ClaimUser: "u1", ClaimSession: "sess-123", MetaSession: "sess-should-not-win", ArgSession: "sess-arg"}
		scope, sess, err := ResolveReadScope(src, ResolveOptions{Posture: posture})
		if err != nil {
			t.Fatalf("posture %v: unexpected error: %v", posture, err)
		}
		if scope.Session != "" {
			t.Errorf("posture %v: Scope.Session = %q, want empty (D-150) — a session-bearing token must never narrow a read to one session", posture, scope.Session)
		}
		if sess != "sess-123" {
			t.Errorf("posture %v: effective session = %q, want sess-123 (claim precedence)", posture, sess)
		}
	}
}

// TestResolveReadScope_CompatibleArgsOnly_ByteIdentical (AC-6) proves that with
// no JWT/_meta sources at all — the pre-ae7/pre-ae2 world — the resolved scope
// is byte-identical to the pre-ae8 args-only construction
// (identity.Scope{Tenant, Project: arg, User: arg}).
func TestResolveReadScope_CompatibleArgsOnly_ByteIdentical(t *testing.T) {
	src := IdentitySources{Tenant: "acme", ArgUser: "u1", ArgProject: "p1"}
	scope, sess, err := ResolveReadScope(src, ResolveOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Scope{Tenant: "acme", Project: "p1", User: "u1"}
	if scope != want {
		t.Errorf("scope = %+v, want %+v", scope, want)
	}
	if sess != "" {
		t.Errorf("session = %q, want empty", sess)
	}

	// Omitted args entirely -> tenant-wide, exactly today's zero-arg behaviour.
	scope2, _, err2 := ResolveReadScope(IdentitySources{Tenant: "acme"}, ResolveOptions{})
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if scope2 != (Scope{Tenant: "acme"}) {
		t.Errorf("scope = %+v, want {Tenant: acme}", scope2)
	}
}

// TestResolveReadScope_Sentinels (AC-2) exercises the three sentinel error
// branches directly.
func TestResolveReadScope_Sentinels(t *testing.T) {
	t.Run("ErrTenantMismatch", func(t *testing.T) {
		_, _, err := ResolveReadScope(IdentitySources{Tenant: "acme", MetaTenant: "other"}, ResolveOptions{})
		if !errors.Is(err, ErrTenantMismatch) {
			t.Fatalf("error = %v, want ErrTenantMismatch", err)
		}
	})
	t.Run("ErrUserConflict", func(t *testing.T) {
		_, _, err := ResolveReadScope(IdentitySources{Tenant: "acme", CredUser: "alice", MetaUser: "mallory"}, ResolveOptions{})
		if !errors.Is(err, ErrUserConflict) {
			t.Fatalf("error = %v, want ErrUserConflict", err)
		}
	})
	t.Run("ErrIdentityRequired", func(t *testing.T) {
		_, _, err := ResolveReadScope(IdentitySources{Tenant: "acme"}, ResolveOptions{Posture: PostureStrict})
		if !errors.Is(err, ErrIdentityRequired) {
			t.Fatalf("error = %v, want ErrIdentityRequired", err)
		}
	})
}

// TestResolveReadScope_EmptyTenant proves an empty-Tenant source fails
// Validate() (P3: no tenant, no read) rather than silently resolving.
func TestResolveReadScope_EmptyTenant(t *testing.T) {
	_, _, err := ResolveReadScope(IdentitySources{}, ResolveOptions{})
	if !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("error = %v, want a wrapped ErrInvalidScope", err)
	}
}

// TestResolveReadScope_TenantNeverEmptyProperty (AC-4, P3) is a property test
// over random source combinations: on every nil-error return, Tenant != "".
func TestResolveReadScope_TenantNeverEmptyProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	tenants := []string{"", "acme", "other"}
	users := []string{"", "alice", "bob"}
	sample := func() string {
		return []string{"", "x", "y"}[rng.Intn(3)]
	}
	for i := 0; i < 500; i++ {
		src := IdentitySources{
			Tenant:        tenants[rng.Intn(len(tenants))],
			CredUser:      users[rng.Intn(len(users))],
			CanAssertUser: rng.Intn(2) == 0,
			ClaimTenant:   tenants[rng.Intn(len(tenants))],
			ClaimUser:     users[rng.Intn(len(users))],
			ClaimSession:  sample(),
			MetaTenant:    tenants[rng.Intn(len(tenants))],
			MetaUser:      users[rng.Intn(len(users))],
			MetaSession:   sample(),
			MetaAgent:     sample(),
			MetaProject:   sample(),
			ArgUser:       users[rng.Intn(len(users))],
			ArgSession:    sample(),
			ArgProject:    sample(),
		}
		opts := ResolveOptions{
			Posture:      ReadPosture(rng.Intn(2)),
			Multiplexing: rng.Intn(2) == 0,
		}
		scope, sess, err := ResolveReadScope(src, opts)
		if err == nil {
			if scope.Tenant == "" {
				t.Fatalf("case %d: nil error but empty Tenant; src=%+v opts=%+v", i, src, opts)
			}
			if scope.Session != "" {
				t.Fatalf("case %d: nil error but non-empty Scope.Session=%q (D-150); src=%+v", i, scope.Session, src)
			}
			_ = sess
		}
	}
}

// TestResolveReadScope_ConcurrentReuse (§5) proves ResolveReadScope is safe
// for concurrent use from N goroutines on shared input under -race — the pure
// -function claim.
func TestResolveReadScope_ConcurrentReuse(t *testing.T) {
	const n = 64
	src := IdentitySources{Tenant: "acme", ClaimUser: "u1", MetaUser: "u2", ArgUser: "u3", ClaimSession: "s1"}
	opts := ResolveOptions{Posture: PostureCompatible}
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			scope, sess, err := ResolveReadScope(src, opts)
			if err != nil {
				errs[i] = err
				return
			}
			if scope.User != "u1" || sess != "s1" || scope.Session != "" {
				errs[i] = fmt.Errorf("goroutine %d: unexpected result scope=%+v sess=%q", i, scope, sess)
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

// TestParsePosture covers the config-value mapping helper.
func TestParsePosture(t *testing.T) {
	tests := []struct {
		in   string
		want ReadPosture
	}{
		{"strict", PostureStrict},
		{"compatible", PostureCompatible},
		{"", PostureCompatible},
		{"bogus", PostureCompatible},
	}
	for _, tt := range tests {
		if got := ParsePosture(tt.in); got != tt.want {
			t.Errorf("ParsePosture(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
