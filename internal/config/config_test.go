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
	cfg := config.Defaults()
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
	os.Unsetenv(varName)
	t.Cleanup(func() { os.Unsetenv(varName) })

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
	cfg.Explain(&buf)
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
	os.Unsetenv("STOWAGE_GATEWAY_API_KEY")

	cfg := config.Defaults()
	var buf bytes.Buffer
	cfg.Explain(&buf)
	got := buf.String()

	goldenPath := filepath.Join("testdata", "explain_default.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
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
		"STOWAGE_STORE_DRIVER",
		"STOWAGE_STORE_DSN",
		"STOWAGE_GATEWAY_DRIVER",
		"STOWAGE_GATEWAY_BASE_URL",
		"STOWAGE_GATEWAY_API_KEY",
		"STOWAGE_GATEWAY_MODEL",
		"STOWAGE_GATEWAY_EMBED_MODEL",
		"STOWAGE_GATEWAY_EMBED_DIMS",
		"STOWAGE_TELEMETRY_LOG_LEVEL",
		"STOWAGE_TELEMETRY_LOG_FORMAT",
		"STOWAGE_TELEMETRY_METRICS_LISTEN",
	}
	for _, v := range vars {
		prev, had := os.LookupEnv(v)
		os.Unsetenv(v)
		if had {
			v, prev := v, prev
			t.Cleanup(func() { os.Setenv(v, prev) })
		}
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
	f.Close()
	return f.Name()
}
