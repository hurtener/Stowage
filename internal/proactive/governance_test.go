package proactive

import (
	"context"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
)

// fakeScopeSettings is a minimal in-memory ScopeSettingsStore for governance tests.
type fakeScopeSettings struct {
	vals map[string]string
	err  error
}

func (f *fakeScopeSettings) Get(_ context.Context, _ identity.Scope, key string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	v, ok := f.vals[key]
	return v, ok, nil
}
func (f *fakeScopeSettings) Set(_ context.Context, _ identity.Scope, key, value string, _ int64) error {
	if f.vals == nil {
		f.vals = map[string]string{}
	}
	f.vals[key] = value
	return nil
}
func (f *fakeScopeSettings) List(_ context.Context, _ identity.Scope) (map[string]string, error) {
	return f.vals, nil
}
func (f *fakeScopeSettings) Delete(_ context.Context, _ identity.Scope, key string) error {
	delete(f.vals, key)
	return nil
}

func defaultCfg() Config {
	return Config{Enabled: true, Threshold: 0.5, Budget: 2, Classes: map[string]bool{ClassExpiring: true}}
}

func TestResolve_ProfileDefaultWhenAbsent(t *testing.T) {
	ss := &fakeScopeSettings{}
	got, err := Resolve(context.Background(), ss, identity.Scope{Tenant: "t"}, defaultCfg())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.Enabled || got.Threshold != 0.5 || got.Budget != 2 {
		t.Fatalf("expected profile default, got %+v", got)
	}
}

func TestResolve_OverrideWins(t *testing.T) {
	ss := &fakeScopeSettings{vals: map[string]string{
		"proactive": `{"enabled":true,"threshold":0.9,"budget":1,"classes":{"recent_episode":true}}`,
	}}
	got, err := Resolve(context.Background(), ss, identity.Scope{Tenant: "t"}, defaultCfg())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Threshold != 0.9 || got.Budget != 1 || !got.classEnabled(ClassRecentEpisode) || got.classEnabled(ClassExpiring) {
		t.Fatalf("override not applied: %+v", got)
	}
}

func TestResolve_MalformedFailsSafeOff(t *testing.T) {
	ss := &fakeScopeSettings{vals: map[string]string{"proactive": `{not json`}}
	got, err := Resolve(context.Background(), ss, identity.Scope{Tenant: "t"}, defaultCfg())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Enabled {
		t.Fatalf("malformed governance must fail safe to OFF, got %+v", got)
	}
}

func TestResolve_OptOut(t *testing.T) {
	ss := &fakeScopeSettings{vals: map[string]string{"proactive": `{"enabled":false}`}}
	got, _ := Resolve(context.Background(), ss, identity.Scope{Tenant: "t"}, defaultCfg())
	if got.Enabled {
		t.Fatalf("opt-out must disable, got %+v", got)
	}
}

func TestClamp_BudgetCeilingAndFloors(t *testing.T) {
	c := Config{Threshold: -1, Budget: 1000, Classes: nil}.clamp()
	if c.Threshold != 0 {
		t.Errorf("negative threshold not floored: %v", c.Threshold)
	}
	if c.Budget != 20 {
		t.Errorf("budget not capped at 20: %v", c.Budget)
	}
	if c.Classes == nil {
		t.Errorf("nil classes not normalised")
	}
}

func TestMarshalConfig_Roundtrip(t *testing.T) {
	in := Config{Enabled: true, Threshold: 0.42, Budget: 3, Classes: map[string]bool{ClassExpiring: true}}
	raw, err := MarshalConfig(in)
	if err != nil {
		t.Fatalf("MarshalConfig: %v", err)
	}
	ss := &fakeScopeSettings{vals: map[string]string{"proactive": raw}}
	got, _ := Resolve(context.Background(), ss, identity.Scope{Tenant: "t"}, defaultCfg())
	if got.Threshold != 0.42 || got.Budget != 3 || !got.classEnabled(ClassExpiring) {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestClassMultiplier(t *testing.T) {
	cases := []struct {
		acc, dis int
		wantMin  float64
		wantMax  float64
	}{
		{0, 0, 1.0, 1.0},  // no history → neutral
		{0, 10, 0.2, 0.2}, // heavily dismissed → floor
		{10, 0, 0.9, 1.0}, // heavily accepted → ~1
		{1, 1, 0.6, 0.7},  // mixed → middle
	}
	for _, c := range cases {
		got := classMultiplier(c.acc, c.dis)
		if got < c.wantMin || got > c.wantMax {
			t.Errorf("classMultiplier(%d,%d)=%v, want [%v,%v]", c.acc, c.dis, got, c.wantMin, c.wantMax)
		}
	}
	// Monotonic: more dismissals never raises the multiplier.
	prev := 1.1
	for d := 0; d <= 20; d++ {
		m := classMultiplier(2, d)
		if m > prev {
			t.Fatalf("not monotonic in dismissals at d=%d: %v > %v", d, m, prev)
		}
		prev = m
	}
}
