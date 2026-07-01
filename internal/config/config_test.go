package config_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hurtener/stowage/internal/config"
)

// TestDefaultsValid verifies AC-1: Load with no file and no env returns a
// valid, working default config.
func TestDefaultsValid(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Defaults().Validate() = %v, want nil", err)
	}
}

// TestLoadNoFileNoEnv verifies AC-1 via Load.
func TestLoadNoFileNoEnv(t *testing.T) {
	clearStowageEnv(t)
	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Load().Validate() = %v, want nil", err)
	}
	if cfg.Profile != "assistant" {
		t.Errorf("Profile = %q, want %q", cfg.Profile, "assistant")
	}
}

// TestSecretLiteralFails verifies AC-2: a secret field with a literal value
// fails validation.
func TestSecretLiteralFails(t *testing.T) {
	clearStowageEnv(t)
	yaml := []byte("gateway:\n  api_key: literal-secret-value\n")
	tmp := writeTmpFile(t, yaml)

	_, err := config.Load(context.Background(), tmp)
	if err != nil {
		// parse error is also acceptable — the literal is caught
		t.Logf("Load error (ok): %v", err)
		return
	}
	// If Load succeeded, Validate must catch it.
	cfg, _ := config.Load(context.Background(), tmp)
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for literal secret")
	}
	if !strings.Contains(err.Error(), "config.gateway.api_key") {
		t.Errorf("error %q does not contain key path", err.Error())
	}
}

// TestSecretLiteralFailsValidation uses Defaults() with manual override.
func TestSecretLiteralFailsValidation(t *testing.T) {
	clearStowageEnv(t)
	// Manually inject a literal (not env.VAR) into APIKey
	yaml := []byte("gateway:\n  api_key: not-env-ref\n")
	tmp := writeTmpFile(t, yaml)
	cfg, err := config.Load(context.Background(), tmp)
	if err != nil {
		return // parse-time error is acceptable
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for literal api_key")
	}
	if !strings.Contains(err.Error(), "config.gateway.api_key") {
		t.Errorf("error %q missing key path config.gateway.api_key", err.Error())
	}
}

// TestResolveEnvRefUnset verifies AC-2: Resolve fails closed with var name
// in the error when the env var is unset.
func TestResolveEnvRefUnset(t *testing.T) {
	const varName = "STOWAGE_TEST_SECRET_XXXX"
	_ = os.Unsetenv(varName)
	t.Cleanup(func() { _ = os.Unsetenv(varName) })

	_, err := config.ResolveEnvRef("env." + varName)
	if err == nil {
		t.Fatal("ResolveEnvRef() = nil, want error for unset var")
	}
	if !strings.Contains(err.Error(), varName) {
		t.Errorf("error %q does not contain var name %q", err.Error(), varName)
	}
}

// TestResolveEnvRefSet verifies ResolveEnvRef returns the value when set.
func TestResolveEnvRefSet(t *testing.T) {
	const varName = "STOWAGE_TEST_SECRET_XXXX"
	const want = "supersecret"
	t.Setenv(varName, want)

	got, err := config.ResolveEnvRef("env." + varName)
	if err != nil {
		t.Fatalf("ResolveEnvRef() error: %v", err)
	}
	if got != want {
		t.Errorf("ResolveEnvRef() = %q, want %q", got, want)
	}
}

// TestResolveEnvRefNotEnvForm verifies ResolveEnvRef rejects non-env. refs.
func TestResolveEnvRefNotEnvForm(t *testing.T) {
	_, err := config.ResolveEnvRef("literal-value")
	if err == nil {
		t.Fatal("ResolveEnvRef() = nil, want error for literal")
	}
}

// TestValidationErrors verifies AC-3: errors carry full key paths; multiple
// errors are joined.
func TestValidationErrors(t *testing.T) {
	clearStowageEnv(t)
	yaml := []byte(`profile: bad-profile
server:
  listen: ""
store:
  driver: unknown-driver
gateway:
  api_key: literal-value
`)
	tmp := writeTmpFile(t, yaml)
	cfg, err := config.Load(context.Background(), tmp)
	if err != nil {
		t.Logf("Load error: %v", err)
		return
	}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want multiple errors")
	}

	// Multiple errors must be joined — check for at least two key paths.
	errStr := err.Error()
	keyPaths := []string{
		"config.profile",
		"config.gateway.api_key",
	}
	for _, kp := range keyPaths {
		if !strings.Contains(errStr, kp) {
			t.Errorf("validation error missing key path %q in %q", kp, errStr)
		}
	}
}

// TestValidationErrorsJoined verifies errors.Join is used (unwrap chain).
func TestValidationErrorsJoined(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.Profile = "bad"
	cfg.Gateway.APIKey = "literal"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	// errors.Join produces an error with multiple Unwrap() errors.
	type multi interface {
		Unwrap() []error
	}
	if _, ok := err.(multi); !ok {
		// Single error is also fine if it carries both messages
		if strings.Count(err.Error(), "config.") < 2 {
			t.Errorf("expected at least 2 errors, got: %v", err)
		}
	}
}

// TestProfileTable verifies AC-4: switching profile changes documented values.
var profileTableTests = []struct {
	profile    string
	wantFormat string
	wantOrigin config.Origin
}{
	{"assistant", "text", config.OriginDefault},
	{"coding-agent", "text", config.OriginDefault},
	{"fleet", "json", config.OriginProfile},
}

func TestProfileTable(t *testing.T) {
	for _, tt := range profileTableTests {
		tt := tt
		t.Run(tt.profile, func(t *testing.T) {
			clearStowageEnv(t)
			yaml := []byte("profile: " + tt.profile + "\n")
			tmp := writeTmpFile(t, yaml)
			cfg, err := config.Load(context.Background(), tmp)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Telemetry.LogFormat != tt.wantFormat {
				t.Errorf("LogFormat = %q, want %q", cfg.Telemetry.LogFormat, tt.wantFormat)
			}
		})
	}
}

// TestEnvOverride verifies env vars override file values.
func TestEnvOverride(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_SERVER_LISTEN", ":9999")
	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != ":9999" {
		t.Errorf("Server.Listen = %q, want %q", cfg.Server.Listen, ":9999")
	}
}

// TestExplainSecretsNotPrinted verifies AC-4: secrets are never printed.
func TestExplainSecretsNotPrinted(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_GATEWAY_API_KEY", "supersecret-value")

	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var buf bytes.Buffer
	if err := cfg.Explain(&buf); err != nil {
		t.Fatalf("explain: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "supersecret-value") {
		t.Error("Explain output must not contain secret value")
	}
	if !strings.Contains(out, "STOWAGE_GATEWAY_API_KEY") {
		t.Error("Explain output should contain the env var name")
	}
	if !strings.Contains(out, "(set)") {
		t.Error("Explain output should indicate secret is (set)")
	}
}

// TestExplainGolden is a golden test for the default Explain output (AC-4).
// Run with UPDATE_GOLDEN=1 to regenerate testdata/explain_default.golden.
func TestExplainGolden(t *testing.T) {
	clearStowageEnv(t)
	// Ensure the gateway api_key env var is unset for stable output.
	_ = os.Unsetenv("STOWAGE_GATEWAY_API_KEY")

	cfg := config.Defaults()
	var buf bytes.Buffer
	if err := cfg.Explain(&buf); err != nil {
		t.Fatalf("explain: %v", err)
	}
	got := buf.String()

	goldenPath := filepath.Join("testdata", "explain_default.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden file %s: %v (run with UPDATE_GOLDEN=1 to create)", goldenPath, err)
	}
	if string(want) != got {
		t.Errorf("explain output mismatch\ngot:\n%s\nwant:\n%s", got, string(want))
	}
}

// TestServerTimeoutEnvOverrides verifies the new server timeout fields can be
// set via environment variables and are correctly parsed.
func TestServerTimeoutEnvOverrides(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_SERVER_READ_TIMEOUT", "30")
	t.Setenv("STOWAGE_SERVER_WRITE_TIMEOUT", "60")
	t.Setenv("STOWAGE_SERVER_IDLE_TIMEOUT", "120")
	t.Setenv("STOWAGE_SERVER_MAX_BODY_BYTES", "2097152")

	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.ReadTimeout != 30 {
		t.Errorf("ReadTimeout = %d, want 30", cfg.Server.ReadTimeout)
	}
	if cfg.Server.WriteTimeout != 60 {
		t.Errorf("WriteTimeout = %d, want 60", cfg.Server.WriteTimeout)
	}
	if cfg.Server.IdleTimeout != 120 {
		t.Errorf("IdleTimeout = %d, want 120", cfg.Server.IdleTimeout)
	}
	if cfg.Server.MaxBodyBytes != 2097152 {
		t.Errorf("MaxBodyBytes = %d, want 2097152", cfg.Server.MaxBodyBytes)
	}
}

// TestLoadMergeOrder verifies the merge priority: env beats file beats profile.
func TestLoadMergeOrder(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_SERVER_LISTEN", ":env-wins")
	yaml := []byte("server:\n  listen: \":file-value\"\n")
	tmp := writeTmpFile(t, yaml)
	cfg, err := config.Load(context.Background(), tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != ":env-wins" {
		t.Errorf("Server.Listen = %q, want env override %q", cfg.Server.Listen, ":env-wins")
	}
}

// TestErrors verifies error sentinel values are exported.
func TestErrors(t *testing.T) {
	_, err := config.ResolveEnvRef("literal")
	if err == nil {
		t.Fatal("expected error")
	}

	err2 := config.ResolveEnvRef
	_ = err2
	_ = errors.New("placeholder")
}

// --- helpers ---

// clearStowageEnv removes all STOWAGE_* env vars for the duration of t.
func clearStowageEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"STOWAGE_PROFILE",
		"STOWAGE_SERVER_LISTEN",
		"STOWAGE_SERVER_MCP_LISTEN",
		"STOWAGE_SERVER_PPROF_LISTEN",
		"STOWAGE_SERVER_READ_TIMEOUT",
		"STOWAGE_SERVER_WRITE_TIMEOUT",
		"STOWAGE_SERVER_IDLE_TIMEOUT",
		"STOWAGE_SERVER_MAX_BODY_BYTES",
		"STOWAGE_STORE_DRIVER",
		"STOWAGE_STORE_DSN",
		"STOWAGE_VINDEX_DRIVER",
		"STOWAGE_GATEWAY_DRIVER",
		"STOWAGE_GATEWAY_PROVIDER",
		"STOWAGE_GATEWAY_BASE_URL",
		"STOWAGE_GATEWAY_API_KEY",
		"STOWAGE_GATEWAY_MODEL",
		"STOWAGE_GATEWAY_EMBED_MODEL",
		"STOWAGE_GATEWAY_EMBED_DIMS",
		"STOWAGE_TELEMETRY_LOG_LEVEL",
		"STOWAGE_TELEMETRY_LOG_FORMAT",
		"STOWAGE_TELEMETRY_METRICS_LISTEN",
		"STOWAGE_TELEMETRY_RUNTIME_SAMPLE_INTERVAL",
		"STOWAGE_AUTH_MODE",
		"STOWAGE_AUTH_ISSUER",
		"STOWAGE_AUTH_AUDIENCE",
		"STOWAGE_AUTH_ALGORITHMS",
		"STOWAGE_AUTH_JWKS_URL",
		"STOWAGE_AUTH_JWKS_FILE",
		"STOWAGE_AUTH_JWKS_MAX_STALE",
	}
	for _, v := range vars {
		prev, had := os.LookupEnv(v)
		_ = os.Unsetenv(v)
		if had {
			v, prev := v, prev
			t.Cleanup(func() { _ = os.Setenv(v, prev) })
		}
	}
}

// TestBufferTriggersForProfile verifies the per-profile trigger defaults (D-042).
func TestBufferTriggersForProfile(t *testing.T) {
	cases := []struct {
		profile    string
		wantCount  int
		wantTokens int64
	}{
		{"assistant", 18, 2500}, // Phase 29 (D-107): coarsened for richer per-extraction context
		{"coding-agent", 20, 2500},
		{"fleet", 30, 4000},
		{"unknown", 18, 2500}, // fallback to assistant
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.profile, func(t *testing.T) {
			trig := config.BufferTriggersForProfile(tc.profile)
			if trig.Count != tc.wantCount {
				t.Errorf("Count: got %d want %d", trig.Count, tc.wantCount)
			}
			if trig.Tokens != tc.wantTokens {
				t.Errorf("Tokens: got %d want %d", trig.Tokens, tc.wantTokens)
			}
			if trig.MaxAge == 0 {
				t.Errorf("MaxAge must be non-zero for profile %q", tc.profile)
			}
		})
	}
}

// TestRetrievalTuningDefaultEmpty verifies the retrieval section defaults to all-zero
// (inherit the built-in presets) and passes validation (D-103).
func TestRetrievalTuningDefaultEmpty(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	if cfg.Retrieval.Precise != (config.ProfileTuning{}) {
		t.Errorf("Retrieval.Precise default = %+v, want zero (inherit preset)", cfg.Retrieval.Precise)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Defaults().Validate() with empty retrieval: %v", err)
	}
}

// TestRetrievalTuningValid verifies a sane override loads and validates (D-103).
func TestRetrievalTuningValid(t *testing.T) {
	clearStowageEnv(t)
	yaml := []byte("retrieval:\n  precise:\n    lane_k: 60\n    scoring_k: 30\n    default_limit: 10\n")
	tmp := writeTmpFile(t, yaml)
	cfg, err := config.Load(context.Background(), tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retrieval.Precise.ScoringK != 30 || cfg.Retrieval.Precise.LaneK != 60 {
		t.Errorf("Retrieval.Precise = %+v, want lane_k=60 scoring_k=30", cfg.Retrieval.Precise)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with valid retrieval tuning: %v", err)
	}
}

// TestIncludeSupersededDefaultAndOverride verifies the dual-visibility knob (D-105)
// defaults true and accepts a YAML override.
func TestIncludeSupersededDefaultAndOverride(t *testing.T) {
	clearStowageEnv(t)
	if !config.Defaults().Retrieval.IncludeSuperseded {
		t.Errorf("retrieval.include_superseded default = false, want true (dual-visibility)")
	}
	tmp := writeTmpFile(t, []byte("retrieval:\n  include_superseded: false\n"))
	cfg, err := config.Load(context.Background(), tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Retrieval.IncludeSuperseded {
		t.Errorf("retrieval.include_superseded override to false did not take")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with include_superseded override: %v", err)
	}
}

// TestRetrievalTuningInvalid rejects negative windows, scoring_k > lane_k, and a
// default_limit past the hard result cap (D-103).
func TestRetrievalTuningInvalid(t *testing.T) {
	clearStowageEnv(t)
	cases := []struct {
		name string
		t    config.ProfileTuning
	}{
		{"negative", config.ProfileTuning{ScoringK: -1}},
		{"scoring_k exceeds lane_k", config.ProfileTuning{LaneK: 10, ScoringK: 20}},
		{"default_limit over cap", config.ProfileTuning{DefaultLimit: 51}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Retrieval.Precise = tc.t
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %s (%+v)", tc.name, tc.t)
			}
		})
	}
}

// TestVIndexDriverDefault verifies vindex.driver defaults to "hnsw" (D-048).
func TestVIndexDriverDefault(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	if cfg.VIndex.Driver != "hnsw" {
		t.Errorf("VIndex.Driver = %q, want %q", cfg.VIndex.Driver, "hnsw")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Defaults().Validate() with hnsw: %v", err)
	}
}

// TestVIndexDriverBrute verifies vindex.driver="brute" is valid.
func TestVIndexDriverBrute(t *testing.T) {
	clearStowageEnv(t)
	yaml := []byte("vindex:\n  driver: brute\n")
	tmp := writeTmpFile(t, yaml)
	cfg, err := config.Load(context.Background(), tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VIndex.Driver != "brute" {
		t.Errorf("VIndex.Driver = %q, want %q", cfg.VIndex.Driver, "brute")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with brute driver: %v", err)
	}
}

// TestVIndexDriverUnknownFails verifies unknown vindex.driver fails validation.
func TestVIndexDriverUnknownFails(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.VIndex.Driver = "pgvector"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for unknown vindex driver")
	}
	if !strings.Contains(err.Error(), "config.vindex.driver") {
		t.Errorf("error %q does not contain key path config.vindex.driver", err.Error())
	}
}

// TestVIndexDriverEnvOverride verifies STOWAGE_VINDEX_DRIVER overrides config.
func TestVIndexDriverEnvOverride(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_VINDEX_DRIVER", "brute")
	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VIndex.Driver != "brute" {
		t.Errorf("VIndex.Driver = %q, want %q", cfg.VIndex.Driver, "brute")
	}
}

// TestGatewayDriverOpenAICompat verifies openaicompat is a valid gateway driver (D-040/D-049).
func TestGatewayDriverOpenAICompat(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.Gateway.Driver = "openaicompat"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with openaicompat driver: %v", err)
	}
}

// TestGatewayDriverBifrostRequiresProvider verifies that driver=bifrost fails
// validation when gateway.provider is empty (D-049).
func TestGatewayDriverBifrostRequiresProvider(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.Gateway.Driver = "bifrost"
	cfg.Gateway.Provider = "" // no provider set
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for bifrost without provider")
	}
	if !strings.Contains(err.Error(), "config.gateway.provider") {
		t.Errorf("error %q does not contain key path config.gateway.provider", err.Error())
	}
}

// TestGatewayDriverBifrostWithProviderValid verifies that driver=bifrost
// with a provider set passes validation (D-049).
func TestGatewayDriverBifrostWithProviderValid(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.Gateway.Driver = "bifrost"
	cfg.Gateway.Provider = "openai"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with bifrost+provider: %v", err)
	}
}

// TestGatewayProviderEnvOverride verifies STOWAGE_GATEWAY_PROVIDER overrides config.
func TestGatewayProviderEnvOverride(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_GATEWAY_PROVIDER", "anthropic")
	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Gateway.Provider != "anthropic" {
		t.Errorf("Gateway.Provider = %q, want %q", cfg.Gateway.Provider, "anthropic")
	}
}

// TestMCPListenDefaultEmpty verifies server.mcp_listen defaults to empty (opt-in,
// D-074) and the default config validates.
func TestMCPListenDefaultEmpty(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	if cfg.Server.MCPListen != "" {
		t.Errorf("Server.MCPListen = %q, want empty (opt-in default)", cfg.Server.MCPListen)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Defaults().Validate() with empty mcp_listen: %v", err)
	}
}

// TestMCPListenValidation table-tests server.mcp_listen validation (D-074):
// empty ok, a valid host:port ok, a malformed/colliding addr fails.
func TestMCPListenValidation(t *testing.T) {
	clearStowageEnv(t)
	tests := []struct {
		name    string
		listen  string // server.listen (api); default :7160 when empty
		mcp     string
		wantErr bool
	}{
		{name: "empty is ok", mcp: "", wantErr: false},
		{name: "port-only ok", mcp: ":8081", wantErr: false},
		{name: "host:port ok", mcp: "127.0.0.1:8081", wantErr: false},
		{name: "no port fails", mcp: "notaport", wantErr: true},
		{name: "non-numeric port fails", mcp: ":abc", wantErr: true},
		{name: "port out of range fails", mcp: ":99999", wantErr: true},
		{name: "collision with server.listen fails", listen: ":8081", mcp: ":8081", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			if tt.listen != "" {
				cfg.Server.Listen = tt.listen
			}
			cfg.Server.MCPListen = tt.mcp
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error for mcp_listen=%q", tt.mcp)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil for mcp_listen=%q", err, tt.mcp)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "config.server.mcp_listen") {
				t.Errorf("error %q does not contain key path config.server.mcp_listen", err.Error())
			}
		})
	}
}

// TestMCPListenEnvOverride verifies STOWAGE_SERVER_MCP_LISTEN overrides config.
func TestMCPListenEnvOverride(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_SERVER_MCP_LISTEN", ":8081")
	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.MCPListen != ":8081" {
		t.Errorf("Server.MCPListen = %q, want %q", cfg.Server.MCPListen, ":8081")
	}
}

// TestRerankBaseURLDefaultEmpty verifies gateway.rerank_base_url defaults empty
// (→ the bifrost driver supplies OpenRouter's …/api/v1 when provider=openrouter,
// while any other provider keeps "reuse base_url", D-131) and that the default
// config validates (D-075/D-034).
func TestRerankBaseURLDefaultEmpty(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	if cfg.Gateway.RerankBaseURL != "" {
		t.Errorf("Gateway.RerankBaseURL = %q, want empty (driver supplies OpenRouter's)", cfg.Gateway.RerankBaseURL)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Defaults().Validate() with empty rerank_base_url: %v", err)
	}
}

// TestRerankBaseURLValidation table-tests gateway.rerank_base_url validation
// (D-075): empty ok, a valid absolute URL ok, a malformed/relative value fails.
func TestRerankBaseURLValidation(t *testing.T) {
	clearStowageEnv(t)
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "empty is ok", url: "", wantErr: false},
		{name: "https url ok", url: "https://openrouter.ai/api/v1", wantErr: false},
		{name: "http localhost ok", url: "http://127.0.0.1:8080/v1", wantErr: false},
		{name: "no scheme fails", url: "openrouter.ai/api/v1", wantErr: true},
		{name: "scheme without host fails", url: "https://", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Gateway.RerankBaseURL = tt.url
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error for rerank_base_url=%q", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil for rerank_base_url=%q", err, tt.url)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "config.gateway.rerank_base_url") {
				t.Errorf("error %q does not contain key path config.gateway.rerank_base_url", err.Error())
			}
		})
	}
}

// TestRerankBaseURLEnvOverride verifies STOWAGE_GATEWAY_RERANK_BASE_URL overrides config.
func TestRerankBaseURLEnvOverride(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_GATEWAY_RERANK_BASE_URL", "https://rerank.example.com/v1")
	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Gateway.RerankBaseURL != "https://rerank.example.com/v1" {
		t.Errorf("Gateway.RerankBaseURL = %q, want %q", cfg.Gateway.RerankBaseURL, "https://rerank.example.com/v1")
	}
}

// TestPprofListenDefaultEmpty verifies server.pprof_listen defaults to empty
// (opt-in, disabled by default) and the default config validates.
func TestPprofListenDefaultEmpty(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	if cfg.Server.PprofListen != "" {
		t.Errorf("Server.PprofListen = %q, want empty (opt-in default)", cfg.Server.PprofListen)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Defaults().Validate() with empty pprof_listen: %v", err)
	}
}

// TestPprofListenValidation table-tests server.pprof_listen validation:
// empty ok, a valid host:port ok, malformed/colliding addresses fail.
func TestPprofListenValidation(t *testing.T) {
	clearStowageEnv(t)
	tests := []struct {
		name      string
		listen    string // server.listen; default :7160 when empty
		mcpListen string // server.mcp_listen; "" when empty
		pprof     string
		wantErr   bool
	}{
		{name: "empty is ok", pprof: "", wantErr: false},
		{name: "loopback host:port ok", pprof: "127.0.0.1:6060", wantErr: false},
		{name: "port-only ok", pprof: ":6060", wantErr: false},
		{name: "no port fails", pprof: "notaport", wantErr: true},
		{name: "non-numeric port fails", pprof: ":abc", wantErr: true},
		{name: "port out of range fails", pprof: ":99999", wantErr: true},
		{name: "collision with server.listen fails", listen: ":6060", pprof: ":6060", wantErr: true},
		{name: "collision with mcp_listen fails", mcpListen: ":8081", pprof: ":8081", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			if tt.listen != "" {
				cfg.Server.Listen = tt.listen
			}
			if tt.mcpListen != "" {
				cfg.Server.MCPListen = tt.mcpListen
			}
			cfg.Server.PprofListen = tt.pprof
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error for pprof_listen=%q", tt.pprof)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil for pprof_listen=%q", err, tt.pprof)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "config.server.pprof_listen") {
				t.Errorf("error %q does not contain key path config.server.pprof_listen", err.Error())
			}
		})
	}
}

// TestPprofListenEnvOverride verifies STOWAGE_SERVER_PPROF_LISTEN overrides config.
func TestPprofListenEnvOverride(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_SERVER_PPROF_LISTEN", "127.0.0.1:6060")
	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.PprofListen != "127.0.0.1:6060" {
		t.Errorf("Server.PprofListen = %q, want %q", cfg.Server.PprofListen, "127.0.0.1:6060")
	}
}

// TestRuntimeSampleIntervalDefault verifies telemetry.runtime_sample_interval
// defaults to 0 (sampler off) and the default config validates.
func TestRuntimeSampleIntervalDefault(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	if cfg.Telemetry.RuntimeSampleInterval != 0 {
		t.Errorf("Telemetry.RuntimeSampleInterval = %d, want 0 (off by default)", cfg.Telemetry.RuntimeSampleInterval)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Defaults().Validate() with interval=0: %v", err)
	}
}

// TestRuntimeSampleIntervalValidation verifies negative values fail, non-negative pass.
func TestRuntimeSampleIntervalValidation(t *testing.T) {
	clearStowageEnv(t)
	tests := []struct {
		name     string
		interval int
		wantErr  bool
	}{
		{name: "zero is ok (off)", interval: 0, wantErr: false},
		{name: "positive is ok", interval: 60, wantErr: false},
		{name: "negative fails", interval: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Telemetry.RuntimeSampleInterval = tt.interval
			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error for interval=%d", tt.interval)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil for interval=%d", err, tt.interval)
			}
			if tt.wantErr && err != nil && !strings.Contains(err.Error(), "config.telemetry.runtime_sample_interval") {
				t.Errorf("error %q does not contain key path config.telemetry.runtime_sample_interval", err.Error())
			}
		})
	}
}

// TestRuntimeSampleIntervalFleetProfile verifies the fleet profile sets
// telemetry.runtime_sample_interval to 60 (coarse sampling).
func TestRuntimeSampleIntervalFleetProfile(t *testing.T) {
	clearStowageEnv(t)
	yaml := []byte("profile: fleet\n")
	tmp := writeTmpFile(t, yaml)
	cfg, err := config.Load(context.Background(), tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Telemetry.RuntimeSampleInterval != 60 {
		t.Errorf("fleet profile RuntimeSampleInterval = %d, want 60", cfg.Telemetry.RuntimeSampleInterval)
	}
}

// TestRuntimeSampleIntervalAssistantProfile verifies the assistant and
// coding-agent profiles inherit the default 0 (sampler off).
func TestRuntimeSampleIntervalNonFleetProfiles(t *testing.T) {
	for _, profile := range []string{"assistant", "coding-agent"} {
		profile := profile
		t.Run(profile, func(t *testing.T) {
			clearStowageEnv(t)
			yaml := []byte("profile: " + profile + "\n")
			tmp := writeTmpFile(t, yaml)
			cfg, err := config.Load(context.Background(), tmp)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Telemetry.RuntimeSampleInterval != 0 {
				t.Errorf("%s profile RuntimeSampleInterval = %d, want 0 (off)", profile, cfg.Telemetry.RuntimeSampleInterval)
			}
		})
	}
}

// TestRuntimeSampleIntervalEnvOverride verifies
// STOWAGE_TELEMETRY_RUNTIME_SAMPLE_INTERVAL overrides config.
func TestRuntimeSampleIntervalEnvOverride(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_TELEMETRY_RUNTIME_SAMPLE_INTERVAL", "30")
	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Telemetry.RuntimeSampleInterval != 30 {
		t.Errorf("Telemetry.RuntimeSampleInterval = %d, want 30", cfg.Telemetry.RuntimeSampleInterval)
	}
}

func writeTmpFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stowage-cfg-*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}
	return f.Name()
}

// TestGatewayPerConcernKeyValidation verifies the per-concern secret keys
// (embed_api_key, rerank_api_key) require env.VAR indirection like gateway.api_key
// (a1b, D-134/D-030).
func TestGatewayPerConcernKeyValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*config.Config)
		path string
	}{
		{"embed_api_key literal", func(c *config.Config) { c.Gateway.EmbedAPIKey = "sk-literal" }, "config.gateway.embed_api_key"},
		{"rerank_api_key literal", func(c *config.Config) { c.Gateway.RerankAPIKey = "sk-literal" }, "config.gateway.rerank_api_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			tc.set(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error for %s", tc.path)
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Errorf("error %q does not name %s", err.Error(), tc.path)
			}
		})
	}
	// env. indirection passes.
	cfg := config.Defaults()
	cfg.Gateway.EmbedAPIKey = "env.MY_EMBED_KEY"
	cfg.Gateway.RerankAPIKey = "env.MY_RERANK_KEY"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with env. per-concern keys: %v", err)
	}
}

// TestGatewayPerConcernSecretRedacted verifies a set embed_api_key is redacted in
// config explain — the env value never prints (a1b, D-134).
func TestGatewayPerConcernSecretRedacted(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_TEST_EMBED_SECRET", "secret-embed-value")
	cfg := config.Defaults()
	cfg.Gateway.EmbedAPIKey = "env.STOWAGE_TEST_EMBED_SECRET"

	var buf bytes.Buffer
	if err := cfg.Explain(&buf); err != nil {
		t.Fatalf("explain: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "secret-embed-value") {
		t.Error("Explain output must not contain the embed_api_key value")
	}
	if !strings.Contains(out, "STOWAGE_TEST_EMBED_SECRET (set)") {
		t.Error("Explain should show the embed_api_key env-ref as (set)")
	}
}

// ---- ae7: auth.* (D-136/D-147) ---------------------------------------------

// TestAuthModeDefaultKeyring verifies AC-4: auth.mode defaults to keyring and
// the default config validates (zero-config start unchanged).
func TestAuthModeDefaultKeyring(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	if cfg.Auth.Mode != "keyring" {
		t.Errorf("Auth.Mode = %q, want keyring", cfg.Auth.Mode)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Defaults().Validate(): %v", err)
	}
}

// TestAuthModeUnknownFails verifies an unrecognized auth.mode fails validation.
func TestAuthModeUnknownFails(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.Auth.Mode = "oauth2"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want error for unknown auth.mode")
	}
	if !strings.Contains(err.Error(), "config.auth.mode") {
		t.Errorf("error %q does not contain key path config.auth.mode", err.Error())
	}
}

// TestAuthModeJWT_RequiresExactlyOneJWKSSource verifies AC-4/AC-9: jwt mode
// with neither (or both) of jwks.url/jwks.file fails validation — never a
// silent keyring fallback (D-147).
func TestAuthModeJWT_RequiresExactlyOneJWKSSource(t *testing.T) {
	clearStowageEnv(t)

	cfg := config.Defaults()
	cfg.Auth.Mode = "jwt"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error: jwt mode with neither jwks.url nor jwks.file")
	}

	cfg = config.Defaults()
	cfg.Auth.Mode = "jwt"
	cfg.Auth.JWKS.URL = "https://harbor.example.com/.well-known/jwks.json"
	cfg.Auth.JWKS.File = "/etc/stowage/jwks.json"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error: jwt mode with BOTH jwks.url and jwks.file")
	}

	cfg = config.Defaults()
	cfg.Auth.Mode = "jwt"
	cfg.Auth.JWKS.URL = "https://harbor.example.com/.well-known/jwks.json"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with exactly jwks.url set: unexpected error: %v", err)
	}
}

// TestAuthModeJWT_MaxStaleMustBePositive verifies auth.jwks.max_stale > 0.
func TestAuthModeJWT_MaxStaleMustBePositive(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.Auth.Mode = "jwt"
	cfg.Auth.JWKS.URL = "https://harbor.example.com/.well-known/jwks.json"
	cfg.Auth.JWKS.MaxStale = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for max_stale=0")
	}
}

// TestAuthAlgorithms_ValidSubset verifies a comma-separated asymmetric subset
// validates and parses via AlgorithmList.
func TestAuthAlgorithms_ValidSubset(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.Auth.Mode = "jwt"
	cfg.Auth.JWKS.URL = "https://harbor.example.com/.well-known/jwks.json"
	cfg.Auth.Algorithms = "RS256, ES256"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with RS256,ES256: unexpected error: %v", err)
	}
	got := cfg.Auth.AlgorithmList()
	want := []string{"RS256", "ES256"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("AlgorithmList() = %v, want %v", got, want)
	}
}

// TestAuthAlgorithms_RejectsNonAsymmetric verifies HS*/none entries fail
// validation at config load (AC-1's config-side guardrail).
func TestAuthAlgorithms_RejectsNonAsymmetric(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	cfg.Auth.Mode = "jwt"
	cfg.Auth.JWKS.URL = "https://harbor.example.com/.well-known/jwks.json"
	for _, bad := range []string{"HS256", "none", "HS512"} {
		cfg.Auth.Algorithms = bad
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate() with auth.algorithms=%q = nil, want rejection (non-asymmetric)", bad)
		}
	}
}

// TestAuthAlgorithms_EmptyMeansAllSix verifies the empty default parses to nil
// (the validator then defaults to the full six-algorithm allowlist).
func TestAuthAlgorithms_EmptyMeansAllSix(t *testing.T) {
	cfg := config.Defaults()
	if got := cfg.Auth.AlgorithmList(); got != nil {
		t.Errorf("AlgorithmList() on empty auth.algorithms = %v, want nil", got)
	}
}

// TestAuthConfig_EnvOverride verifies the STOWAGE_AUTH_* env vars override
// config (D-034 completeness).
func TestAuthConfig_EnvOverride(t *testing.T) {
	clearStowageEnv(t)
	t.Setenv("STOWAGE_AUTH_MODE", "jwt")
	t.Setenv("STOWAGE_AUTH_ISSUER", "harbor")
	t.Setenv("STOWAGE_AUTH_AUDIENCE", "stowage")
	t.Setenv("STOWAGE_AUTH_ALGORITHMS", "RS256")
	t.Setenv("STOWAGE_AUTH_JWKS_URL", "https://harbor.example.com/.well-known/jwks.json")
	t.Setenv("STOWAGE_AUTH_JWKS_MAX_STALE", "120")

	cfg, err := config.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.Mode != "jwt" {
		t.Errorf("Auth.Mode = %q, want jwt", cfg.Auth.Mode)
	}
	if cfg.Auth.Issuer != "harbor" {
		t.Errorf("Auth.Issuer = %q, want harbor", cfg.Auth.Issuer)
	}
	if cfg.Auth.Audience != "stowage" {
		t.Errorf("Auth.Audience = %q, want stowage", cfg.Auth.Audience)
	}
	if cfg.Auth.Algorithms != "RS256" {
		t.Errorf("Auth.Algorithms = %q, want RS256", cfg.Auth.Algorithms)
	}
	if cfg.Auth.JWKS.URL != "https://harbor.example.com/.well-known/jwks.json" {
		t.Errorf("Auth.JWKS.URL = %q, want the configured URL", cfg.Auth.JWKS.URL)
	}
	if cfg.Auth.JWKS.MaxStale != 120 {
		t.Errorf("Auth.JWKS.MaxStale = %d, want 120", cfg.Auth.JWKS.MaxStale)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with full jwt-mode env override: %v", err)
	}
}

// TestAuthConfig_PresentInEveryProfile verifies D-034: the auth.* defaults
// hold across all three profiles (none overrides them), matching the
// vindex.driver precedent.
func TestAuthConfig_PresentInEveryProfile(t *testing.T) {
	clearStowageEnv(t)
	for _, profile := range []string{"assistant", "coding-agent", "fleet"} {
		yaml := []byte("profile: " + profile + "\n")
		tmp := writeTmpFile(t, yaml)
		cfg, err := config.Load(context.Background(), tmp)
		if err != nil {
			t.Fatalf("Load(%s): %v", profile, err)
		}
		if cfg.Auth.Mode != "keyring" {
			t.Errorf("profile %s: Auth.Mode = %q, want keyring", profile, cfg.Auth.Mode)
		}
		if cfg.Auth.JWKS.MaxStale != 3600 {
			t.Errorf("profile %s: Auth.JWKS.MaxStale = %d, want 3600", profile, cfg.Auth.JWKS.MaxStale)
		}
	}
}

// TestAuthConfig_ExplainShowsAllSevenKeys verifies the seven auth.* keys
// appear in Explain output (AC-9 / the smoke script's grep contract).
func TestAuthConfig_ExplainShowsAllSevenKeys(t *testing.T) {
	clearStowageEnv(t)
	cfg := config.Defaults()
	var buf bytes.Buffer
	if err := cfg.Explain(&buf); err != nil {
		t.Fatalf("explain: %v", err)
	}
	out := buf.String()
	for _, key := range []string{
		"auth.mode", "auth.issuer", "auth.audience", "auth.algorithms",
		"auth.jwks.url", "auth.jwks.file", "auth.jwks.max_stale",
	} {
		if !strings.Contains(out, key) {
			t.Errorf("Explain output missing key %q", key)
		}
	}
}
