package proactive_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/proactive"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore" // register driver
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "proactive_test.db")
	s, err := store.Open(context.Background(), config.StoreConfig{Driver: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// seedExpiring inserts an active memory expiring `inMs` from now (0 = no expiry).
func seedExpiring(t *testing.T, st store.Store, scope identity.Scope, now, inMs int64, content string) string {
	t.Helper()
	mem := store.Memory{
		ID: ulid.Make().String(), TenantID: scope.Tenant, UserID: scope.User,
		Kind: "fact", Content: content, Status: "active",
		Importance: 8, Confidence: 0.9, TrustSource: "user_stated", Stability: 5.0,
		CreatedAt: now, UpdatedAt: now,
	}
	if inMs > 0 {
		mem.ValidUntil = now + inMs
	}
	if err := st.Memories().Insert(context.Background(), scope, mem); err != nil {
		t.Fatalf("insert memory: %v", err)
	}
	return mem.ID
}

func expiringCfg(threshold float64, budget int) proactive.Config {
	return proactive.Config{Enabled: true, Threshold: threshold, Budget: budget,
		Classes: map[string]bool{proactive.ClassExpiring: true}}
}

func TestResolveOffer_EmitsAuditEvents(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()

	mkOffer := func(sess string) string {
		seedExpiring(t, st, scope, now, int64(time.Hour/time.Millisecond), "expiring note")
		offers, _, err := proactive.Evaluate(context.Background(), st, nil, scope, sess, "", expiringCfg(0.0, 5), now)
		if err != nil || len(offers) == 0 {
			t.Fatalf("setup offer: %v / %d", err, len(offers))
		}
		return offers[0].ID
	}

	// accept → suggestion.accepted event.
	acceptID := mkOffer("s-acc")
	if _, err := proactive.ResolveOffer(context.Background(), st, scope, acceptID, "accept", now); err != nil {
		t.Fatalf("accept: %v", err)
	}
	assertEvent(t, st, scope, acceptID, "suggestion.accepted")

	// dismiss → suggestion.dismissed event.
	dismissID := mkOffer("s-dis")
	if _, err := proactive.ResolveOffer(context.Background(), st, scope, dismissID, "dismiss", now); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	assertEvent(t, st, scope, dismissID, "suggestion.dismissed")

	// double-resolve returns the store error and emits nothing new.
	if _, err := proactive.ResolveOffer(context.Background(), st, scope, acceptID, "dismiss", now); err == nil {
		t.Error("double-resolve should error")
	}
}

func assertEvent(t *testing.T, st store.Store, scope identity.Scope, subjectID, wantType string) {
	t.Helper()
	evs, err := st.Events().ListBySubject(context.Background(), scope, subjectID, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range evs {
		if e.Type == wantType {
			return
		}
	}
	t.Fatalf("expected a %s event for %s, got %+v", wantType, subjectID, evs)
}

// seedNarratedEpisode creates a closed episode ended `agoMs` ago, attaches a
// narrative memory, and returns (episodeID, narrativeMemoryID).
func seedNarratedEpisode(t *testing.T, st store.Store, scope identity.Scope, now, agoMs int64) (string, string) {
	t.Helper()
	ctx := context.Background()
	narrID := seedExpiring(t, st, scope, now, 0, "narrative of the migration")
	epID := ulid.Make().String()
	ep := store.Episode{
		ID: epID, TenantID: scope.Tenant, UserID: scope.User, SessionID: "old-session",
		Title: "the March migration", Status: "closed", Outcome: "success",
		StartedAt: now - agoMs - 1000, EndedAt: now - agoMs, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Episodes().CreateEpisode(ctx, scope, ep); err != nil {
		t.Fatalf("create episode: %v", err)
	}
	if err := st.Episodes().SetEpisodeNarrative(ctx, scope, epID, narrID, ep.Title, now); err != nil {
		t.Fatalf("set narrative: %v", err)
	}
	return epID, narrID
}

// fakeSearcher is a NarrativeSearcher returning a fixed ranking (the similar rule).
type fakeSearcher struct {
	ids      []string
	scores   []float64
	degraded bool
	err      error
}

func (f *fakeSearcher) SimilarNarratives(_ context.Context, _ identity.Scope, _ string, _ int) ([]string, []float64, bool, error) {
	return f.ids, f.scores, f.degraded, f.err
}

func TestEvaluate_RecentEpisodeOffered(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	epID, narrID := seedNarratedEpisode(t, st, scope, now, int64(24*time.Hour/time.Millisecond)) // 1 day ago

	cfg := proactive.Config{Enabled: true, Threshold: 0.0, Budget: 5,
		Classes: map[string]bool{proactive.ClassRecentEpisode: true}}
	offers, degraded, err := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", cfg, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if degraded {
		t.Errorf("recent_episode is gateway-free; must not be degraded")
	}
	if len(offers) != 1 || offers[0].MemoryID != narrID || offers[0].EpisodeID != epID {
		t.Fatalf("expected 1 recent-episode offer (mem %s ep %s), got %+v", narrID, epID, offers)
	}
	if offers[0].TriggerKind != proactive.ClassRecentEpisode {
		t.Errorf("wrong trigger kind: %s", offers[0].TriggerKind)
	}
}

func TestEvaluate_RecentEpisodeOutsideWindow(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	seedNarratedEpisode(t, st, scope, now, int64(30*24*time.Hour/time.Millisecond)) // 30 days ago

	cfg := proactive.Config{Enabled: true, Threshold: 0.0, Budget: 5,
		Classes: map[string]bool{proactive.ClassRecentEpisode: true}}
	offers, _, err := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", cfg, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("an episode older than the 7-day window must not be offered, got %+v", offers)
	}
}

func TestEvaluate_SimilarEpisode(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	epID, narrID := seedNarratedEpisode(t, st, scope, now, int64(90*24*time.Hour/time.Millisecond)) // old, but similar rule ignores age

	cfg := proactive.Config{Enabled: true, Threshold: 0.0, Budget: 5,
		Classes: map[string]bool{proactive.ClassSimilarEpisode: true}}
	searcher := &fakeSearcher{ids: []string{epID}, scores: []float64{0.8}}
	offers, degraded, err := proactive.Evaluate(context.Background(), st, searcher, scope, "s1", "how do I migrate?", cfg, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if degraded {
		t.Errorf("searcher reported healthy; degraded should be false")
	}
	if len(offers) != 1 || offers[0].MemoryID != narrID || offers[0].TriggerKind != proactive.ClassSimilarEpisode {
		t.Fatalf("expected 1 similar-episode offer, got %+v", offers)
	}
}

func TestEvaluate_SimilarEpisodeDegraded(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	seedNarratedEpisode(t, st, scope, now, int64(24*time.Hour/time.Millisecond))

	cfg := proactive.Config{Enabled: true, Threshold: 0.0, Budget: 5,
		Classes: map[string]bool{proactive.ClassSimilarEpisode: true}}
	searcher := &fakeSearcher{degraded: true} // gateway down: no ids, degraded set
	offers, degraded, err := proactive.Evaluate(context.Background(), st, searcher, scope, "s1", "anything", cfg, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !degraded {
		t.Errorf("a degraded searcher must surface degraded=true")
	}
	if len(offers) != 0 {
		t.Errorf("degraded similar rule offers nothing, got %+v", offers)
	}
}

func TestEvaluate_ExpiringOffered(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	id := seedExpiring(t, st, scope, now, int64(time.Hour/time.Millisecond), "rotate the staging cert")

	offers, degraded, err := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", expiringCfg(0.01, 5), now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if degraded {
		t.Errorf("gateway-free expiring rule must not be degraded")
	}
	if len(offers) != 1 || offers[0].MemoryID != id || offers[0].TriggerKind != proactive.ClassExpiring {
		t.Fatalf("expected 1 expiring offer for %s, got %+v", id, offers)
	}
	if offers[0].Score <= 0 {
		t.Errorf("offer score must be positive, got %v", offers[0].Score)
	}
	// Persisted pending row.
	prior, err := st.Suggestions().ListBySession(context.Background(), scope, "s1", "pending", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prior) != 1 || prior[0].Status != "pending" {
		t.Fatalf("expected 1 pending suggestion persisted, got %+v", prior)
	}
}

func TestEvaluate_RequiresSession(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	seedExpiring(t, st, scope, now, int64(time.Hour/time.Millisecond), "note")

	_, _, err := proactive.Evaluate(context.Background(), st, nil, scope, "", "", expiringCfg(0.0, 5), now)
	if !errors.Is(err, proactive.ErrSessionRequired) {
		t.Fatalf("empty session must return ErrSessionRequired, got %v", err)
	}
}

func TestEvaluate_OfferCarriesContent(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	seedExpiring(t, st, scope, now, int64(time.Hour/time.Millisecond), "rotate the staging cert")

	offers, _, err := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", expiringCfg(0.0, 5), now)
	if err != nil || len(offers) != 1 {
		t.Fatalf("evaluate: %v / %d", err, len(offers))
	}
	if offers[0].Content != "rotate the staging cert" {
		t.Errorf("offer should carry the memory content inline, got %q", offers[0].Content)
	}
}

// TestEvaluate_FeedbackRecovers proves a class suppressed by OLD dismissals recovers
// once the feedback ages out of the trailing window.
func TestEvaluate_FeedbackRecovers(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	hour := int64(time.Hour / time.Millisecond)

	// Establish the neutral base score and a gate just under it.
	base, _, _ := func() ([]proactive.Offer, bool, error) {
		seedExpiring(t, st, scope, time.Now().UnixMilli(), hour, "n")
		return proactive.Evaluate(context.Background(), st, nil, scope, "s0", "", expiringCfg(0.0, 5), time.Now().UnixMilli())
	}()
	if len(base) == 0 {
		t.Fatal("setup: no base offer")
	}
	gate := base[0].Score * 0.9

	// Accumulate dismissals dated ~60 days ago (older than the 30-day feedback window).
	old := time.Now().UnixMilli() - int64(60*24*time.Hour/time.Millisecond)
	for i := 0; i < 14; i++ {
		seedExpiring(t, st, scope, old, hour, "old noise")
		os, _, _ := proactive.Evaluate(context.Background(), st, nil, scope, "sold", "", expiringCfg(0.0, 5), old)
		for _, o := range os {
			_, _ = proactive.ResolveOffer(context.Background(), st, scope, o.ID, "dismiss", old)
		}
	}

	// Now, today: the old dismissals are outside the window, so the class is NOT
	// suppressed — a fresh offer clears the gate.
	now := time.Now().UnixMilli()
	seedExpiring(t, st, scope, now, hour, "fresh")
	offers, _, _ := proactive.Evaluate(context.Background(), st, nil, scope, "snew", "", expiringCfg(gate, 5), now)
	if len(offers) == 0 {
		t.Fatalf("a class dismissed only long ago should recover and clear gate %.4f", gate)
	}
}

func TestEvaluate_OutsideWindowNotOffered(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	seedExpiring(t, st, scope, now, int64(10*24*time.Hour/time.Millisecond), "far future expiry") // >3d window
	seedExpiring(t, st, scope, now, 0, "no expiry at all")

	offers, _, err := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", expiringCfg(0.01, 5), now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("memories outside the expiry window must not be offered, got %+v", offers)
	}
}

func TestEvaluate_BudgetCap(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	for i := 0; i < 5; i++ {
		seedExpiring(t, st, scope, now, int64(time.Hour/time.Millisecond), "expiring note")
	}
	offers, _, err := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", expiringCfg(0.0, 2), now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("budget=2 must cap offers at 2, got %d", len(offers))
	}
}

func TestEvaluate_ThresholdGate(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	seedExpiring(t, st, scope, now, int64(time.Hour/time.Millisecond), "expiring note")

	// Final scores can exceed 1 (importance/use boosts compound), so the gate uses a
	// threshold comfortably above any single-offer score.
	offers, _, err := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", expiringCfg(100.0, 5), now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("a high threshold must gate out the offer, got %+v", offers)
	}
}

func TestEvaluate_OptOut(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	seedExpiring(t, st, scope, now, int64(time.Hour/time.Millisecond), "expiring note")

	cfg := expiringCfg(0.0, 5)
	cfg.Enabled = false
	offers, _, err := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", cfg, now)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("opt-out must offer nothing, got %+v", offers)
	}
}

func TestEvaluate_DedupeAcrossCalls(t *testing.T) {
	st := newStore(t)
	scope := identity.Scope{Tenant: "acme"}
	now := time.Now().UnixMilli()
	seedExpiring(t, st, scope, now, int64(time.Hour/time.Millisecond), "expiring note")

	first, _, _ := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", expiringCfg(0.0, 5), now)
	if len(first) != 1 {
		t.Fatalf("first call should offer 1, got %d", len(first))
	}
	second, _, _ := proactive.Evaluate(context.Background(), st, nil, scope, "s1", "", expiringCfg(0.0, 5), now)
	if len(second) != 0 {
		t.Fatalf("second call must dedupe the already-offered memory, got %+v", second)
	}
}

func TestEvaluate_FeedbackTuningSuppresses(t *testing.T) {
	now := time.Now().UnixMilli()
	hour := int64(time.Hour / time.Millisecond)

	// Control tenant: one clean offer reveals the class's neutral base score S.
	ctrl := newStore(t)
	cScope := identity.Scope{Tenant: "control"}
	seedExpiring(t, ctrl, cScope, now, hour, "expiring note")
	base, _, _ := proactive.Evaluate(context.Background(), ctrl, nil, cScope, "s1", "", expiringCfg(0.0, 5), now)
	if len(base) != 1 {
		t.Fatalf("control: expected 1 offer, got %d", len(base))
	}
	S := base[0].Score
	// A threshold just under the neutral base score: a neutral class clears it.
	gate := S * 0.9

	// Dismissed tenant: accumulate dismissals so the class multiplier decays to the
	// floor, then a fresh offer at `gate` is suppressed (base × small < gate).
	dis := newStore(t)
	dScope := identity.Scope{Tenant: "dismissive"}
	for i := 0; i < 14; i++ {
		seedExpiring(t, dis, dScope, now, hour, "annoying note")
		os, _, _ := proactive.Evaluate(context.Background(), dis, nil, dScope, "s1", "", expiringCfg(0.0, 5), now)
		for _, o := range os {
			if _, err := dis.Suggestions().Resolve(context.Background(), dScope, o.ID, "dismiss", now); err != nil {
				t.Fatalf("resolve: %v", err)
			}
		}
	}
	acc, dismissed, err := dis.Suggestions().CountByTrigger(context.Background(), dScope, proactive.ClassExpiring, 0)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if dismissed == 0 {
		t.Fatalf("expected accumulated dismissals, got acc=%d dis=%d", acc, dismissed)
	}

	// Fresh memory in a new session: the dismissed class is now suppressed at `gate`
	// even though a neutral class (the control) would clear the same gate.
	seedExpiring(t, dis, dScope, now, hour, "yet another")
	suppressed, _, _ := proactive.Evaluate(context.Background(), dis, nil, dScope, "s2", "", expiringCfg(gate, 5), now)
	if len(suppressed) != 0 {
		t.Fatalf("heavily-dismissed class should be suppressed at gate %.4f, got %+v", gate, suppressed)
	}
	// Sanity: the control (neutral) class clears the same gate.
	control2, _, _ := proactive.Evaluate(context.Background(), ctrl, nil, cScope, "s3", "", expiringCfg(gate, 5), now)
	if len(control2) != 1 {
		t.Fatalf("neutral class should clear gate %.4f, got %+v", gate, control2)
	}
}
