package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

func bytesBufferString(s string) *bytes.Buffer { return bytes.NewBufferString(s) }

type suggestionsBody struct {
	Suggestions []struct {
		ID          string  `json:"id"`
		TriggerKind string  `json:"trigger_kind"`
		MemoryID    string  `json:"memory_id"`
		Title       string  `json:"title"`
		Score       float64 `json:"score"`
	} `json:"suggestions"`
	Degraded bool `json:"degraded"`
}

type resolveBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// seedExpiringMemory inserts an active memory expiring within the proactive window.
func seedExpiringMemory(t *testing.T, st store.Store, scope identity.Scope) string {
	t.Helper()
	now := time.Now().UnixMilli()
	id := ulid.Make().String()
	if err := st.Memories().Insert(context.Background(), scope, store.Memory{
		ID: id, Kind: "fact", Content: "rotate the staging cert", Status: "active",
		Importance: 8, Confidence: 0.9, TrustSource: "user_stated", Stability: 5.0,
		ValidUntil: now + int64(time.Hour/time.Millisecond), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	return id
}

func TestSuggestions_ListAndResolve(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-suggest"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	memID := seedExpiringMemory(t, st, identity.Scope{Tenant: tenant})

	// GET /v1/suggestions evaluates + offers (assistant profile: expiring enabled).
	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/suggestions?session_id=s1", nil, agentKey)
	if err != nil {
		t.Fatalf("GET suggestions: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body suggestionsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Suggestions) != 1 || body.Suggestions[0].MemoryID != memID {
		t.Fatalf("expected 1 offer for %s, got %+v", memID, body.Suggestions)
	}
	if body.Suggestions[0].TriggerKind != "expiring" {
		t.Errorf("wrong trigger kind: %s", body.Suggestions[0].TriggerKind)
	}
	offerID := body.Suggestions[0].ID

	// POST accept → 200, status accepted.
	r2, err := doRequest(t, http.MethodPost, ts.URL+"/v1/suggestions/"+offerID, jsonBody(t, map[string]string{"action": "accept"}), agentKey)
	if err != nil {
		t.Fatalf("POST resolve: %v", err)
	}
	defer drainClose(r2.Body)
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("resolve want 200, got %d", r2.StatusCode)
	}
	var rb resolveBody
	if err := json.NewDecoder(r2.Body).Decode(&rb); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if rb.ID != offerID || rb.Status != "accepted" {
		t.Fatalf("want accepted %s, got %+v", offerID, rb)
	}

	// Double-resolve → 404 (CAS: no longer pending).
	r3, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/suggestions/"+offerID, jsonBody(t, map[string]string{"action": "dismiss"}), agentKey)
	defer drainClose(r3.Body)
	if r3.StatusCode != http.StatusNotFound {
		t.Fatalf("double-resolve want 404, got %d", r3.StatusCode)
	}
}

func TestSuggestions_BadAction(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-suggest-bad"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	r, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/suggestions/"+ulid.Make().String(), jsonBody(t, map[string]string{"action": "frobnicate"}), agentKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad action want 400, got %d", r.StatusCode)
	}
}

func TestSuggestions_ResolveMissing(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-suggest-missing"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	r, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/suggestions/"+ulid.Make().String(), jsonBody(t, map[string]string{"action": "accept"}), agentKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("missing offer want 404, got %d", r.StatusCode)
	}
}

func TestProactiveConfig_GetAndPut(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-gov"
	_, adminKey := mustCreateAdminKey(t, st, tenant)

	// GET returns the profile default (assistant: enabled).
	resp, err := doRequest(t, http.MethodGet, ts.URL+"/v1/admin/proactive", nil, adminKey)
	if err != nil {
		t.Fatalf("GET proactive: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var cfg struct {
		Enabled   bool            `json:"enabled"`
		Threshold float64         `json:"threshold"`
		Budget    int             `json:"budget"`
		Classes   map[string]bool `json:"classes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.Enabled {
		t.Errorf("assistant profile default should be enabled, got %+v", cfg)
	}

	// PUT an override (opt-out) and confirm it echoes back disabled.
	put := map[string]any{"enabled": false, "threshold": 0.7, "budget": 1, "classes": map[string]bool{"expiring": true}}
	r2, err := doRequest(t, http.MethodPut, ts.URL+"/v1/admin/proactive", jsonBody(t, put), adminKey)
	if err != nil {
		t.Fatalf("PUT proactive: %v", err)
	}
	defer drainClose(r2.Body)
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("PUT want 200, got %d", r2.StatusCode)
	}
	var cfg2 struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r2.Body).Decode(&cfg2); err != nil {
		t.Fatalf("decode put: %v", err)
	}
	if cfg2.Enabled {
		t.Errorf("override should disable, got enabled")
	}

	// GET again reflects the stored override.
	r3, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/admin/proactive", nil, adminKey)
	defer drainClose(r3.Body)
	var cfg3 struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r3.Body).Decode(&cfg3)
	if cfg3.Enabled {
		t.Errorf("stored override not reflected on GET")
	}
}

func TestSuggestions_EmptyWhenNoMemories(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-suggest-empty"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	resp, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/suggestions?session_id=s1", nil, agentKey)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body suggestionsBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Suggestions) != 0 {
		t.Fatalf("no memories → no offers, got %+v", body.Suggestions)
	}
}

func TestSuggestions_ResolveMalformedBody(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-suggest-malformed"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	r, _ := doRequest(t, http.MethodPost, ts.URL+"/v1/suggestions/"+ulid.Make().String(), bytesBufferString("{not json"), agentKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body want 400, got %d", r.StatusCode)
	}
}

func TestProactiveConfig_PutMalformedBody(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-gov-malformed"
	_, adminKey := mustCreateAdminKey(t, st, tenant)

	r, _ := doRequest(t, http.MethodPut, ts.URL+"/v1/admin/proactive", bytesBufferString("{not json"), adminKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body want 400, got %d", r.StatusCode)
	}
}

func TestSuggestions_DedupeAcrossGets(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-suggest-dedupe"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedExpiringMemory(t, st, identity.Scope{Tenant: tenant})

	get := func() suggestionsBody {
		resp, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/suggestions?session_id=s1", nil, agentKey)
		defer drainClose(resp.Body)
		var b suggestionsBody
		_ = json.NewDecoder(resp.Body).Decode(&b)
		return b
	}
	if n := len(get().Suggestions); n != 1 {
		t.Fatalf("first GET should offer 1, got %d", n)
	}
	if n := len(get().Suggestions); n != 0 {
		t.Fatalf("second GET must dedupe the already-offered memory, got %d", n)
	}
}

func TestProactiveConfig_UserScoped(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-gov-user"
	_, adminKey := mustCreateAdminKey(t, st, tenant)

	// Disable proactive for user=alice only.
	put := map[string]any{"enabled": false, "threshold": 0.5, "budget": 1, "classes": map[string]bool{}}
	r, _ := doRequest(t, http.MethodPut, ts.URL+"/v1/admin/proactive?user=alice", jsonBody(t, put), adminKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("PUT user-scoped want 200, got %d", r.StatusCode)
	}
	// The tenant-level config is untouched (still the profile default, enabled).
	r2, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/admin/proactive", nil, adminKey)
	defer drainClose(r2.Body)
	var tenantCfg struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r2.Body).Decode(&tenantCfg)
	if !tenantCfg.Enabled {
		t.Errorf("tenant-level governance should be unaffected by a user-scoped override")
	}
	// alice's scope reads the disabled override.
	r3, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/admin/proactive?user=alice", nil, adminKey)
	defer drainClose(r3.Body)
	var aliceCfg struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r3.Body).Decode(&aliceCfg)
	if aliceCfg.Enabled {
		t.Errorf("alice's user-scoped override should be disabled")
	}
}

func TestSuggestions_MissingSessionIs400(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-suggest-nosess"
	_, agentKey := mustCreateAgentKey(t, st, tenant)
	seedExpiringMemory(t, st, identity.Scope{Tenant: tenant})

	r, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/suggestions", nil, agentKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing session_id want 400, got %d", r.StatusCode)
	}
}

func TestProactiveConfig_PartialPatchPreserves(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-gov-patch"
	_, adminKey := mustCreateAdminKey(t, st, tenant)

	// PUT only threshold; enabled/budget/classes must survive (assistant default).
	r, _ := doRequest(t, http.MethodPut, ts.URL+"/v1/admin/proactive", jsonBody(t, map[string]any{"threshold": 0.8}), adminKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("partial PUT want 200, got %d", r.StatusCode)
	}
	var cfg struct {
		Enabled   bool            `json:"enabled"`
		Threshold float64         `json:"threshold"`
		Budget    int             `json:"budget"`
		Classes   map[string]bool `json:"classes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.Enabled || cfg.Budget != 2 || cfg.Threshold != 0.8 || !cfg.Classes["expiring"] {
		t.Fatalf("partial patch wiped fields: %+v", cfg)
	}
}

func TestProactiveConfig_RequiresAdmin(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	tenant := "tenant-gov-auth"
	_, agentKey := mustCreateAgentKey(t, st, tenant)

	r, _ := doRequest(t, http.MethodGet, ts.URL+"/v1/admin/proactive", nil, agentKey)
	defer drainClose(r.Body)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("agent key on admin route want 403, got %d", r.StatusCode)
	}
}
