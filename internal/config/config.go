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
	"net"
	"net/url"
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

// MCPConfig configures the MCP server surface (Phase 16, D-020).
type MCPConfig struct {
	// StdioTenant is the fixed tenant ID used in stdio mode.
	// Defaults to "default". Set STOWAGE_MCP_TENANT to override.
	StdioTenant string `yaml:"stdio_tenant"`
}

// Config is the full Stowage runtime configuration. All fields have defaults
// so that Load with no file and no env produces a working config (AC-1).
type Config struct {
	Profile   string          `yaml:"profile"`
	Server    ServerConfig    `yaml:"server"`
	Store     StoreConfig     `yaml:"store"`
	VIndex    VIndexConfig    `yaml:"vindex"`
	Gateway   GatewayConfig   `yaml:"gateway"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	MCP       MCPConfig       `yaml:"mcp"`
	Trace     TraceConfig     `yaml:"trace"`
	Retrieval RetrievalConfig `yaml:"retrieval"`

	prov Provenance
}

// RetrievalConfig tunes the named retrieval profiles (D-103). Each profile's candidate
// windows are operator-tunable; an omitted/zero field inherits the built-in preset, so
// a partial override (e.g. only precise.scoring_k) is valid and the all-empty default
// reproduces the shipped presets exactly. Reranking is not configured here — it is a
// property of the precise profile, wired via gateway.rerank_model.
type RetrievalConfig struct {
	Precise  ProfileTuning `yaml:"precise"`
	Balanced ProfileTuning `yaml:"balanced"`
	Broad    ProfileTuning `yaml:"broad"`
}

// ProfileTuning overrides one retrieval profile's candidate windows. A zero field
// inherits the built-in preset value.
//
//	LaneK        — candidates fetched per lane before RRF fusion.
//	ScoringK     — fused candidates scored/reranked (the cap on memories reaching the
//	               reader; the per-request limit is floored up into this window).
//	DefaultLimit — final result count when the caller omits limit.
type ProfileTuning struct {
	LaneK        int `yaml:"lane_k"`
	ScoringK     int `yaml:"scoring_k"`
	DefaultLimit int `yaml:"default_limit"`
}

// TraceConfig holds reasoning-trace export tuning (Phase 26, §6c, D-086).
type TraceConfig struct {
	// SigningKey, when set, MUST be an env.VAR_NAME reference (D-030) to a
	// base64-encoded 32-byte ed25519 seed used to sign exported trace bundles.
	// Empty (the default) ⇒ bundles are returned unsigned (signed:false). The seed
	// itself never appears in config or logs.
	SigningKey string `yaml:"signing_key"`
}

// ServerConfig holds HTTP server tuning.
type ServerConfig struct {
	Listen string `yaml:"listen"`
	// MCPListen, when non-empty (e.g. ":8081"), makes `stowage serve` co-mount
	// the MCP-over-HTTP surface on this second listener over the SAME
	// boot.Stack + StartPipeline as the HTTP API — one result cache, one
	// pipeline, no cross-process staleness (D-073/D-074). Empty (the default)
	// keeps `stowage serve` single-surface (HTTP API only), binding exactly one
	// port — the zero-config shape is unchanged. Two listeners (not a
	// path-prefixed single port) because MCP streams and must not inherit the
	// REST WriteTimeout/middleware.
	MCPListen    string `yaml:"mcp_listen"`     // default "" (opt-in)
	ReadTimeout  int    `yaml:"read_timeout"`   // seconds; default 10
	WriteTimeout int    `yaml:"write_timeout"`  // seconds; default 20
	IdleTimeout  int    `yaml:"idle_timeout"`   // seconds; default 60
	MaxBodyBytes int64  `yaml:"max_body_bytes"` // default 1 MiB
}

// StoreConfig selects the persistence driver.
type StoreConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

// VIndexConfig selects the vector-index driver (Phase 09b, D-048).
// Driver must be "hnsw" (default, pure-Go ANN) or "brute" (exact-recall oracle).
// The knob guardrail (D-034) requires this key in every profile and a tuned default.
type VIndexConfig struct {
	Driver string `yaml:"driver"` // "hnsw" (default) | "brute"
}

// GatewayConfig selects the intelligence gateway driver.
// APIKey must use env.VAR_NAME indirection (secret:"true", D-030).
type GatewayConfig struct {
	Driver      string `yaml:"driver"`
	Provider    string `yaml:"provider"` // required iff driver=bifrost; one of the Bifrost SDK's supported providers (D-049)
	BaseURL     string `yaml:"base_url"`
	APIKey      string `yaml:"api_key"` // secret:"true" — must be env.VAR_NAME
	Model       string `yaml:"model"`
	EmbedModel  string `yaml:"embed_model"`
	EmbedDims   int    `yaml:"embed_dims"`
	RerankModel string `yaml:"rerank_model"` // cross-encoder model for the precise-profile rerank pass (Phase 12)
	// RerankBaseURL optionally overrides base_url as the host the bifrost driver's
	// auto-wired Cohere-shape rerank provider POSTs to, for the rare case rerank
	// lives on a different host than embed/complete (D-075). Empty → use base_url.
	RerankBaseURL string `yaml:"rerank_base_url"`
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
	"server.mcp_listen",
	"server.read_timeout",
	"server.write_timeout",
	"server.idle_timeout",
	"server.max_body_bytes",
	"store.driver",
	"store.dsn",
	"vindex.driver",
	"gateway.driver",
	"gateway.provider",
	"gateway.base_url",
	"gateway.api_key",
	"gateway.model",
	"gateway.embed_model",
	"gateway.embed_dims",
	"gateway.rerank_model",
	"gateway.rerank_base_url",
	"telemetry.log_level",
	"telemetry.log_format",
	"telemetry.metrics_listen",
	"mcp.stdio_tenant",
	"trace.signing_key",
	"retrieval.precise.lane_k",
	"retrieval.precise.scoring_k",
	"retrieval.precise.default_limit",
	"retrieval.balanced.lane_k",
	"retrieval.balanced.scoring_k",
	"retrieval.balanced.default_limit",
	"retrieval.broad.lane_k",
	"retrieval.broad.scoring_k",
	"retrieval.broad.default_limit",
}

// secretKeyPaths is the set of keys that hold env.VAR_NAME references.
var secretKeyPaths = map[string]bool{
	"gateway.api_key":   true,
	"trace.signing_key": true,
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
	{"STOWAGE_SERVER_MCP_LISTEN", "server.mcp_listen"},
	{"STOWAGE_SERVER_READ_TIMEOUT", "server.read_timeout"},
	{"STOWAGE_SERVER_WRITE_TIMEOUT", "server.write_timeout"},
	{"STOWAGE_SERVER_IDLE_TIMEOUT", "server.idle_timeout"},
	{"STOWAGE_SERVER_MAX_BODY_BYTES", "server.max_body_bytes"},
	{"STOWAGE_STORE_DRIVER", "store.driver"},
	{"STOWAGE_STORE_DSN", "store.dsn"},
	{"STOWAGE_VINDEX_DRIVER", "vindex.driver"},
	{"STOWAGE_GATEWAY_DRIVER", "gateway.driver"},
	{"STOWAGE_GATEWAY_PROVIDER", "gateway.provider"},
	{"STOWAGE_GATEWAY_BASE_URL", "gateway.base_url"},
	{"STOWAGE_GATEWAY_MODEL", "gateway.model"},
	{"STOWAGE_GATEWAY_EMBED_MODEL", "gateway.embed_model"},
	{"STOWAGE_GATEWAY_EMBED_DIMS", "gateway.embed_dims"},
	{"STOWAGE_GATEWAY_RERANK_MODEL", "gateway.rerank_model"},
	{"STOWAGE_GATEWAY_RERANK_BASE_URL", "gateway.rerank_base_url"},
	{"STOWAGE_TELEMETRY_LOG_LEVEL", "telemetry.log_level"},
	{"STOWAGE_TELEMETRY_LOG_FORMAT", "telemetry.log_format"},
	{"STOWAGE_TELEMETRY_METRICS_LISTEN", "telemetry.metrics_listen"},
	{"STOWAGE_MCP_TENANT", "mcp.stdio_tenant"},
}

// Defaults returns a fully working Config with no file or env input. (AC-1)
func Defaults() *Config {
	c := &Config{
		Profile: "assistant",
		Server: ServerConfig{
			Listen:       ":7160",
			MCPListen:    "", // opt-in: empty keeps `stowage serve` single-surface (D-074)
			ReadTimeout:  10,
			WriteTimeout: 20,
			IdleTimeout:  60,
			MaxBodyBytes: 1 << 20, // 1 MiB
		},
		Store: StoreConfig{
			Driver: "sqlite",
			DSN:    "./data/stowage.db",
		},
		VIndex: VIndexConfig{
			Driver: "hnsw", // default: HNSW (D-048; owner directive — 100k brute ceiling too low)
		},
		Gateway: GatewayConfig{ //nolint:gosec // G101: api_key holds an env-var *name* (reference), not a credential
			Driver:        "mock",
			BaseURL:       "",
			APIKey:        "env.STOWAGE_GATEWAY_API_KEY",
			Model:         "claude-3-5-haiku-20241022",
			EmbedModel:    "voyage-3-lite",
			EmbedDims:     512,
			RerankModel:   "cohere/rerank-4-fast",
			RerankBaseURL: "", // empty → reuse base_url for the auto-wired rerank provider (D-075)
		},
		Telemetry: TelemetryConfig{
			LogLevel:      "info",
			LogFormat:     "text",
			MetricsListen: ":7161",
		},
		MCP: MCPConfig{
			StdioTenant: "default",
		},
		prov: make(Provenance),
	}
	for _, k := range allKeys {
		c.prov[k] = OriginDefault
	}
	return c
}

// FillZeroDefaults sets every zero-valued field of c to the corresponding value
// from Defaults(), preserving any field the caller explicitly set. It is the
// defaults layer the embedded SDK path (sdk/stowage.NewEmbedded) applies before
// Validate so the in-process stack validates and populates the same retrieval
// lanes (gateway model / embedding dims / rerank model) as the server, which
// gets these via Load (D-069, parity-lens Pattern P3 / BUG-3).
//
// Note: Store.DSN is also filled from Defaults() here; embedded callers that
// require an explicit DSN must check it before calling FillZeroDefaults.
func (c *Config) FillZeroDefaults() {
	d := Defaults()

	if c.Profile == "" {
		c.Profile = d.Profile
	}

	if c.Server.Listen == "" {
		c.Server.Listen = d.Server.Listen
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = d.Server.ReadTimeout
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = d.Server.WriteTimeout
	}
	if c.Server.IdleTimeout == 0 {
		c.Server.IdleTimeout = d.Server.IdleTimeout
	}
	if c.Server.MaxBodyBytes == 0 {
		c.Server.MaxBodyBytes = d.Server.MaxBodyBytes
	}

	if c.Store.Driver == "" {
		c.Store.Driver = d.Store.Driver
	}
	if c.Store.DSN == "" {
		c.Store.DSN = d.Store.DSN
	}

	if c.VIndex.Driver == "" {
		c.VIndex.Driver = d.VIndex.Driver
	}

	if c.Gateway.Driver == "" {
		c.Gateway.Driver = d.Gateway.Driver
	}
	if c.Gateway.APIKey == "" {
		c.Gateway.APIKey = d.Gateway.APIKey
	}
	if c.Gateway.Model == "" {
		c.Gateway.Model = d.Gateway.Model
	}
	if c.Gateway.EmbedModel == "" {
		c.Gateway.EmbedModel = d.Gateway.EmbedModel
	}
	if c.Gateway.EmbedDims == 0 {
		c.Gateway.EmbedDims = d.Gateway.EmbedDims
	}
	if c.Gateway.RerankModel == "" {
		c.Gateway.RerankModel = d.Gateway.RerankModel
	}

	if c.Telemetry.LogLevel == "" {
		c.Telemetry.LogLevel = d.Telemetry.LogLevel
	}
	if c.Telemetry.LogFormat == "" {
		c.Telemetry.LogFormat = d.Telemetry.LogFormat
		// Mirror config.Load's defaults < profile merge so the embedded fleet
		// matches the server fleet (D-067 lens). config.Load resolves the
		// profile-specific log_format (fleet → json); FillZeroDefaults — the
		// embedded path's defaults layer — must apply the same profile override
		// for a zero field, not the flat Defaults() value. telemetry.log_format is
		// the only field-level profile override today (see Profiles()).
		if pf, ok := Profiles()[c.Profile]["telemetry.log_format"]; ok {
			c.Telemetry.LogFormat = pf
		}
	}
	if c.Telemetry.MetricsListen == "" {
		c.Telemetry.MetricsListen = d.Telemetry.MetricsListen
	}

	if c.MCP.StdioTenant == "" {
		c.MCP.StdioTenant = d.MCP.StdioTenant
	}
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

	// server.mcp_listen is opt-in (default ""). When set it must be a valid
	// host:port, and must not collide with the HTTP API listener — co-mount uses
	// TWO listeners over one stack (D-074).
	if c.Server.MCPListen != "" {
		if _, port, err := net.SplitHostPort(c.Server.MCPListen); err != nil {
			errs = append(errs, fmt.Errorf("config.server.mcp_listen: invalid host:port %q: %w", c.Server.MCPListen, err))
		} else if p, perr := strconv.Atoi(port); perr != nil || p < 1 || p > 65535 {
			errs = append(errs, fmt.Errorf("config.server.mcp_listen: invalid port %q (want 1-65535)", port))
		}
		if c.Server.MCPListen == c.Server.Listen {
			errs = append(errs, fmt.Errorf("config.server.mcp_listen: must differ from server.listen %q (co-mount binds two separate ports)", c.Server.Listen))
		}
	}

	validStoreDrivers := map[string]bool{"sqlite": true, "postgres": true}
	if !validStoreDrivers[c.Store.Driver] {
		errs = append(errs, fmt.Errorf("config.store.driver: unknown driver %q", c.Store.Driver))
	}

	validVIndexDrivers := map[string]bool{"hnsw": true, "brute": true}
	if !validVIndexDrivers[c.VIndex.Driver] {
		errs = append(errs, fmt.Errorf("config.vindex.driver: unknown driver %q (valid: hnsw, brute)", c.VIndex.Driver))
	}

	// driver enum: mock (default), bifrost (SDK — all providers), openaicompat (OpenAI-compatible HTTP, D-040)
	validGWDrivers := map[string]bool{"mock": true, "bifrost": true, "openaicompat": true}
	if !validGWDrivers[c.Gateway.Driver] {
		errs = append(errs, fmt.Errorf("config.gateway.driver: unknown driver %q (valid: mock, bifrost, openaicompat)", c.Gateway.Driver))
	}

	// gateway.provider is required iff driver=bifrost (D-049).
	if c.Gateway.Driver == "bifrost" && c.Gateway.Provider == "" {
		errs = append(errs, errors.New("config.gateway.provider: required when gateway.driver=bifrost; set to a supported provider (e.g. openai, anthropic, gemini)"))
	}

	// Secret fields must use env.VAR indirection (D-030).
	if c.Gateway.APIKey != "" && !strings.HasPrefix(c.Gateway.APIKey, "env.") {
		errs = append(errs, errors.New("config.gateway.api_key: must use env.VAR indirection"))
	}
	// trace.signing_key is optional (empty → unsigned); when set it is a secret and
	// must use env.VAR indirection (D-030/D-086).
	if c.Trace.SigningKey != "" && !strings.HasPrefix(c.Trace.SigningKey, "env.") {
		errs = append(errs, errors.New("config.trace.signing_key: must use env.VAR indirection"))
	}

	// gateway.rerank_base_url is optional (default empty → reuse base_url); when
	// set it must be an absolute URL with scheme + host (D-075/D-034).
	if c.Gateway.RerankBaseURL != "" {
		if u, perr := url.Parse(c.Gateway.RerankBaseURL); perr != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("config.gateway.rerank_base_url: invalid URL %q (want e.g. https://openrouter.ai/api/v1)", c.Gateway.RerankBaseURL))
		}
	}

	validLogLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLogLevels[c.Telemetry.LogLevel] {
		errs = append(errs, fmt.Errorf("config.telemetry.log_level: unknown level %q", c.Telemetry.LogLevel))
	}

	validLogFormats := map[string]bool{"text": true, "json": true}
	if !validLogFormats[c.Telemetry.LogFormat] {
		errs = append(errs, fmt.Errorf("config.telemetry.log_format: unknown format %q", c.Telemetry.LogFormat))
	}

	// retrieval profile tuning (D-103): an omitted field inherits the preset; a set
	// field must be non-negative, scoring_k must not exceed lane_k, and default_limit
	// must stay within the hard result cap (50). Ordered slice → deterministic errors.
	for _, pt := range []struct {
		name string
		t    ProfileTuning
	}{
		{"precise", c.Retrieval.Precise},
		{"balanced", c.Retrieval.Balanced},
		{"broad", c.Retrieval.Broad},
	} {
		if pt.t.LaneK < 0 || pt.t.ScoringK < 0 || pt.t.DefaultLimit < 0 {
			errs = append(errs, fmt.Errorf("config.retrieval.%s: lane_k/scoring_k/default_limit must be >= 0", pt.name))
		}
		if pt.t.DefaultLimit > 50 {
			errs = append(errs, fmt.Errorf("config.retrieval.%s.default_limit: %d exceeds the hard result cap (50)", pt.name, pt.t.DefaultLimit))
		}
		if pt.t.ScoringK > 0 && pt.t.LaneK > 0 && pt.t.ScoringK > pt.t.LaneK {
			errs = append(errs, fmt.Errorf("config.retrieval.%s: scoring_k (%d) must not exceed lane_k (%d)", pt.name, pt.t.ScoringK, pt.t.LaneK))
		}
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
	case "server.mcp_listen":
		return c.Server.MCPListen
	case "server.read_timeout":
		return strconv.Itoa(c.Server.ReadTimeout)
	case "server.write_timeout":
		return strconv.Itoa(c.Server.WriteTimeout)
	case "server.idle_timeout":
		return strconv.Itoa(c.Server.IdleTimeout)
	case "server.max_body_bytes":
		return strconv.FormatInt(c.Server.MaxBodyBytes, 10)
	case "store.driver":
		return c.Store.Driver
	case "store.dsn":
		return c.Store.DSN
	case "vindex.driver":
		return c.VIndex.Driver
	case "gateway.driver":
		return c.Gateway.Driver
	case "gateway.provider":
		return c.Gateway.Provider
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
	case "gateway.rerank_model":
		return c.Gateway.RerankModel
	case "gateway.rerank_base_url":
		return c.Gateway.RerankBaseURL
	case "telemetry.log_level":
		return c.Telemetry.LogLevel
	case "telemetry.log_format":
		return c.Telemetry.LogFormat
	case "telemetry.metrics_listen":
		return c.Telemetry.MetricsListen
	case "mcp.stdio_tenant":
		return c.MCP.StdioTenant
	case "trace.signing_key":
		return c.Trace.SigningKey
	case "retrieval.precise.lane_k":
		return strconv.Itoa(c.Retrieval.Precise.LaneK)
	case "retrieval.precise.scoring_k":
		return strconv.Itoa(c.Retrieval.Precise.ScoringK)
	case "retrieval.precise.default_limit":
		return strconv.Itoa(c.Retrieval.Precise.DefaultLimit)
	case "retrieval.balanced.lane_k":
		return strconv.Itoa(c.Retrieval.Balanced.LaneK)
	case "retrieval.balanced.scoring_k":
		return strconv.Itoa(c.Retrieval.Balanced.ScoringK)
	case "retrieval.balanced.default_limit":
		return strconv.Itoa(c.Retrieval.Balanced.DefaultLimit)
	case "retrieval.broad.lane_k":
		return strconv.Itoa(c.Retrieval.Broad.LaneK)
	case "retrieval.broad.scoring_k":
		return strconv.Itoa(c.Retrieval.Broad.ScoringK)
	case "retrieval.broad.default_limit":
		return strconv.Itoa(c.Retrieval.Broad.DefaultLimit)
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
	case "server.mcp_listen":
		c.Server.MCPListen = value
	case "server.read_timeout":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config.%s: %w", path, err)
		}
		c.Server.ReadTimeout = n
	case "server.write_timeout":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config.%s: %w", path, err)
		}
		c.Server.WriteTimeout = n
	case "server.idle_timeout":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config.%s: %w", path, err)
		}
		c.Server.IdleTimeout = n
	case "server.max_body_bytes":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("config.%s: %w", path, err)
		}
		c.Server.MaxBodyBytes = n
	case "store.driver":
		c.Store.Driver = value
	case "store.dsn":
		c.Store.DSN = value
	case "vindex.driver":
		c.VIndex.Driver = value
	case "gateway.driver":
		c.Gateway.Driver = value
	case "gateway.provider":
		c.Gateway.Provider = value
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
	case "gateway.rerank_model":
		c.Gateway.RerankModel = value
	case "gateway.rerank_base_url":
		c.Gateway.RerankBaseURL = value
	case "telemetry.log_level":
		c.Telemetry.LogLevel = value
	case "telemetry.log_format":
		c.Telemetry.LogFormat = value
	case "telemetry.metrics_listen":
		c.Telemetry.MetricsListen = value
	case "mcp.stdio_tenant":
		c.MCP.StdioTenant = value
	case "trace.signing_key":
		c.Trace.SigningKey = value
	case "retrieval.precise.lane_k", "retrieval.precise.scoring_k", "retrieval.precise.default_limit",
		"retrieval.balanced.lane_k", "retrieval.balanced.scoring_k", "retrieval.balanced.default_limit",
		"retrieval.broad.lane_k", "retrieval.broad.scoring_k", "retrieval.broad.default_limit":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("config.%s: %w", path, err)
		}
		switch path {
		case "retrieval.precise.lane_k":
			c.Retrieval.Precise.LaneK = n
		case "retrieval.precise.scoring_k":
			c.Retrieval.Precise.ScoringK = n
		case "retrieval.precise.default_limit":
			c.Retrieval.Precise.DefaultLimit = n
		case "retrieval.balanced.lane_k":
			c.Retrieval.Balanced.LaneK = n
		case "retrieval.balanced.scoring_k":
			c.Retrieval.Balanced.ScoringK = n
		case "retrieval.balanced.default_limit":
			c.Retrieval.Balanced.DefaultLimit = n
		case "retrieval.broad.lane_k":
			c.Retrieval.Broad.LaneK = n
		case "retrieval.broad.scoring_k":
			c.Retrieval.Broad.ScoringK = n
		case "retrieval.broad.default_limit":
			c.Retrieval.Broad.DefaultLimit = n
		}
	default:
		return fmt.Errorf("config: unknown key path %q", path)
	}
	return nil
}
