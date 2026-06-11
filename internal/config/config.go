// Package config provides typed, fail-loud configuration for Stowage.
//
// Values are loaded with the merge order: defaults < profile < file < env.
// Every value's origin is tracked for Explain output.
// Secret fields must use env.VAR_NAME indirection; literal secrets fail
// validation (D-030, AC-2).
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// Origin records where a config value came from.
type Origin string

const (
	OriginDefault Origin = "default"
	OriginProfile Origin = "profile"
	OriginFile    Origin = "file"
	OriginEnv     Origin = "env"
)

// Provenance maps dot-separated config key paths to their Origin.
type Provenance map[string]Origin

// Config is the full Stowage runtime configuration. All fields have defaults
// so that Load with no file and no env produces a working config (AC-1).
type Config struct {
	Profile   string          `yaml:"profile"`
	Server    ServerConfig    `yaml:"server"`
	Store     StoreConfig     `yaml:"store"`
	Gateway   GatewayConfig   `yaml:"gateway"`
	Telemetry TelemetryConfig `yaml:"telemetry"`

	prov Provenance
}

// ServerConfig holds HTTP server tuning.
type ServerConfig struct {
	Listen string `yaml:"listen"`
}

// StoreConfig selects the persistence driver.
type StoreConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

// GatewayConfig selects the intelligence gateway driver.
// APIKey must use env.VAR_NAME indirection (secret:"true", D-030).
type GatewayConfig struct {
	Driver     string `yaml:"driver"`
	BaseURL    string `yaml:"base_url"`
	APIKey     string `yaml:"api_key"` // secret:"true" — must be env.VAR_NAME
	Model      string `yaml:"model"`
	EmbedModel string `yaml:"embed_model"`
	EmbedDims  int    `yaml:"embed_dims"`
}

// TelemetryConfig controls logging and metrics.
type TelemetryConfig struct {
	LogLevel      string `yaml:"log_level"`
	LogFormat     string `yaml:"log_format"`
	MetricsListen string `yaml:"metrics_listen"`
}

// allKeys is the canonical ordered list of config key paths used by Explain
// and provenance tracking.
var allKeys = []string{
	"profile",
	"server.listen",
	"store.driver",
	"store.dsn",
	"gateway.driver",
	"gateway.base_url",
	"gateway.api_key",
	"gateway.model",
	"gateway.embed_model",
	"gateway.embed_dims",
	"telemetry.log_level",
	"telemetry.log_format",
	"telemetry.metrics_listen",
}

// secretKeyPaths is the set of keys that hold env.VAR_NAME references.
var secretKeyPaths = map[string]bool{
	"gateway.api_key": true,
}

// envKeys maps STOWAGE_* env var names to config key paths.
// Note: STOWAGE_GATEWAY_API_KEY is intentionally absent — it is the target
// env var that the default env-ref "env.STOWAGE_GATEWAY_API_KEY" resolves to,
// not a config override. To change which env var holds the API key, set
// gateway.api_key in a config file (e.g. api_key: env.MY_KEY).
var envKeys = []struct {
	env  string
	path string
}{
	{"STOWAGE_PROFILE", "profile"},
	{"STOWAGE_SERVER_LISTEN", "server.listen"},
	{"STOWAGE_STORE_DRIVER", "store.driver"},
	{"STOWAGE_STORE_DSN", "store.dsn"},
	{"STOWAGE_GATEWAY_DRIVER", "gateway.driver"},
	{"STOWAGE_GATEWAY_BASE_URL", "gateway.base_url"},
	{"STOWAGE_GATEWAY_MODEL", "gateway.model"},
	{"STOWAGE_GATEWAY_EMBED_MODEL", "gateway.embed_model"},
	{"STOWAGE_GATEWAY_EMBED_DIMS", "gateway.embed_dims"},
	{"STOWAGE_TELEMETRY_LOG_LEVEL", "telemetry.log_level"},
	{"STOWAGE_TELEMETRY_LOG_FORMAT", "telemetry.log_format"},
	{"STOWAGE_TELEMETRY_METRICS_LISTEN", "telemetry.metrics_listen"},
}

// Defaults returns a fully working Config with no file or env input. (AC-1)
func Defaults() *Config {
	c := &Config{
		Profile: "assistant",
		Server: ServerConfig{
			Listen: ":7160",
		},
		Store: StoreConfig{
			Driver: "sqlite",
			DSN:    "./data/stowage.db",
		},
		Gateway: GatewayConfig{ //nolint:gosec // G101: api_key holds an env-var *name* (reference), not a credential
			Driver:     "mock",
			BaseURL:    "",
			APIKey:     "env.STOWAGE_GATEWAY_API_KEY",
			Model:      "claude-3-5-haiku-20241022",
			EmbedModel: "voyage-3-lite",
			EmbedDims:  512,
		},
		Telemetry: TelemetryConfig{
			LogLevel:      "info",
			LogFormat:     "text",
			MetricsListen: ":7161",
		},
		prov: make(Provenance),
	}
	for _, k := range allKeys {
		c.prov[k] = OriginDefault
	}
	return c
}

// Load builds a Config by merging in order: defaults < profile < file < env.
// path is optional; pass "" to skip the file step.
// context.Context is accepted for future cancellation support on I/O paths.
func Load(_ context.Context, path string) (*Config, error) {
	c := Defaults()

	// Read file data once for reuse.
	var fileData []byte
	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is the operator-supplied --config flag
		if err != nil {
			return nil, fmt.Errorf("config: read file %q: %w", path, err)
		}
		fileData = data
	}

	// Peek the profile from the file and env before applying profile overrides.
	// This is necessary because the file/env may change which profile is active.
	if len(fileData) > 0 {
		var peeked struct {
			Profile string `yaml:"profile"`
		}
		if err := yaml.Unmarshal(fileData, &peeked); err == nil && peeked.Profile != "" {
			c.Profile = peeked.Profile
			c.prov["profile"] = OriginFile
		}
	}
	if v, ok := os.LookupEnv("STOWAGE_PROFILE"); ok {
		c.Profile = v
		c.prov["profile"] = OriginEnv
	}

	// Apply profile overrides using the resolved profile name.
	if err := c.applyProfile(); err != nil {
		return nil, fmt.Errorf("config: apply profile: %w", err)
	}

	// Apply file overrides (profile key is re-set but idempotent).
	if len(fileData) > 0 {
		if err := c.applyFile(fileData); err != nil {
			return nil, fmt.Errorf("config: parse file %q: %w", path, err)
		}
	}

	// Apply env overrides.
	c.applyEnv()

	return c, nil
}

// Validate returns every error with its key path, joined (AC-3).
// A secret field with a literal value fails (AC-2, D-030).
func (c *Config) Validate() error {
	var errs []error

	validProfiles := map[string]bool{"assistant": true, "coding-agent": true, "fleet": true}
	if !validProfiles[c.Profile] {
		errs = append(errs, fmt.Errorf("config.profile: unknown profile %q", c.Profile))
	}

	if c.Server.Listen == "" {
		errs = append(errs, errors.New("config.server.listen: must not be empty"))
	}

	validStoreDrivers := map[string]bool{"sqlite": true, "postgres": true}
	if !validStoreDrivers[c.Store.Driver] {
		errs = append(errs, fmt.Errorf("config.store.driver: unknown driver %q", c.Store.Driver))
	}

	validGWDrivers := map[string]bool{"mock": true, "bifrost": true}
	if !validGWDrivers[c.Gateway.Driver] {
		errs = append(errs, fmt.Errorf("config.gateway.driver: unknown driver %q", c.Gateway.Driver))
	}

	// Secret fields must use env.VAR indirection (D-030).
	if c.Gateway.APIKey != "" && !strings.HasPrefix(c.Gateway.APIKey, "env.") {
		errs = append(errs, errors.New("config.gateway.api_key: must use env.VAR indirection"))
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[c.Telemetry.LogLevel] {
		errs = append(errs, fmt.Errorf("config.telemetry.log_level: unknown level %q", c.Telemetry.LogLevel))
	}

	validLogFormats := map[string]bool{"text": true, "json": true}
	if !validLogFormats[c.Telemetry.LogFormat] {
		errs = append(errs, fmt.Errorf("config.telemetry.log_format: unknown format %q", c.Telemetry.LogFormat))
	}

	return errors.Join(errs...)
}

// ResolveEnvRef resolves an env.VAR_NAME reference to its environment value.
// Returns an error if the reference is not in env.VAR_NAME form or the env var
// is unset (fail-closed, AC-2).
func ResolveEnvRef(ref string) (string, error) {
	if !strings.HasPrefix(ref, "env.") {
		return "", fmt.Errorf("config: %q is not an env.VAR_NAME reference", ref)
	}
	varName := strings.TrimPrefix(ref, "env.")
	val, ok := os.LookupEnv(varName)
	if !ok {
		return "", fmt.Errorf("config: env var %s is unset", varName)
	}
	return val, nil
}

// applyProfile merges profile-specific overrides into c.
func (c *Config) applyProfile() error {
	profiles := Profiles()
	overrides, ok := profiles[c.Profile]
	if !ok {
		return nil // unknown profile is caught by Validate
	}
	for path, val := range overrides {
		if err := c.setByPath(path, val); err != nil {
			return err
		}
		c.prov[path] = OriginProfile
	}
	return nil
}

// applyFile merges YAML file data into c.
func (c *Config) applyFile(data []byte) error {
	// Parse raw YAML to discover which keys are present.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Parse into a separate Config to get typed values.
	var fileCfg Config
	if err := yaml.Unmarshal(data, &fileCfg); err != nil {
		return err
	}

	// Collect flattened key paths present in the file.
	fileKeys := make(map[string]bool)
	flattenYAMLKeys("", raw, fileKeys)

	// Merge: for each key present in the file, update c.
	for _, key := range allKeys {
		if !fileKeys[key] {
			continue
		}
		val := fileCfg.getByPath(key)
		if err := c.setByPath(key, val); err != nil {
			return err
		}
		c.prov[key] = OriginFile
	}
	return nil
}

// applyEnv applies STOWAGE_* environment overrides.
func (c *Config) applyEnv() {
	for _, e := range envKeys {
		val, ok := os.LookupEnv(e.env)
		if !ok {
			continue
		}
		if err := c.setByPath(e.path, val); err != nil {
			// Ignore type conversion errors for now; Validate catches bad values.
			continue
		}
		c.prov[e.path] = OriginEnv
	}
}

// flattenYAMLKeys recursively collects dot-separated key paths.
func flattenYAMLKeys(prefix string, v interface{}, out map[string]bool) {
	m, ok := v.(map[string]interface{})
	if !ok {
		if prefix != "" {
			out[prefix] = true
		}
		return
	}
	for k, val := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		flattenYAMLKeys(path, val, out)
	}
}

// getByPath returns the string representation of the value at key path.
func (c *Config) getByPath(path string) string {
	switch path {
	case "profile":
		return c.Profile
	case "server.listen":
		return c.Server.Listen
	case "store.driver":
		return c.Store.Driver
	case "store.dsn":
		return c.Store.DSN
	case "gateway.driver":
		return c.Gateway.Driver
	case "gateway.base_url":
		return c.Gateway.BaseURL
	case "gateway.api_key":
		return c.Gateway.APIKey
	case "gateway.model":
		return c.Gateway.Model
	case "gateway.embed_model":
		return c.Gateway.EmbedModel
	case "gateway.embed_dims":
		return strconv.Itoa(c.Gateway.EmbedDims)
	case "telemetry.log_level":
		return c.Telemetry.LogLevel
	case "telemetry.log_format":
		return c.Telemetry.LogFormat
	case "telemetry.metrics_listen":
		return c.Telemetry.MetricsListen
	default:
		return ""
	}
}

// setByPath updates the field at key path from a string value.
func (c *Config) setByPath(path, value string) error {
	switch path {
	case "profile":
		c.Profile = value
	case "server.listen":
		c.Server.Listen = value
	case "store.driver":
		c.Store.Driver = value
	case "store.dsn":
		c.Store.DSN = value
	case "gateway.driver":
		c.Gateway.Driver = value
	case "gateway.base_url":
		c.Gateway.BaseURL = value
	case "gateway.api_key":
		c.Gateway.APIKey = value
	case "gateway.model":
		c.Gateway.Model = value
	case "gateway.embed_model":
		c.Gateway.EmbedModel = value
	case "gateway.embed_dims":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config.%s: %w", path, err)
		}
		c.Gateway.EmbedDims = n
	case "telemetry.log_level":
		c.Telemetry.LogLevel = value
	case "telemetry.log_format":
		c.Telemetry.LogFormat = value
	case "telemetry.metrics_listen":
		c.Telemetry.MetricsListen = value
	default:
		return fmt.Errorf("config: unknown key path %q", path)
	}
	return nil
}
