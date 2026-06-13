package config

import "testing"

// TestFillZeroDefaults_PopulatesGatewayLanes proves the embedded defaults layer
// fills the gateway model / embedding dims / rerank model so the in-process
// vector + rerank lanes are populated identically to the server (D-069, AC-2).
func TestFillZeroDefaults_PopulatesGatewayLanes(t *testing.T) {
	t.Parallel()

	d := Defaults()

	// Minimal embedded-style config: only the store is set.
	c := Config{}
	c.Store.Driver = "sqlite"
	c.Store.DSN = "/tmp/x.db"
	c.FillZeroDefaults()

	if c.Gateway.EmbedDims != d.Gateway.EmbedDims {
		t.Errorf("EmbedDims = %d, want %d (server default)", c.Gateway.EmbedDims, d.Gateway.EmbedDims)
	}
	if c.Gateway.EmbedDims == 0 {
		t.Error("EmbedDims is 0 — vector lane would be a silent no-op (the BUG this fixes)")
	}
	if c.Gateway.EmbedModel != d.Gateway.EmbedModel {
		t.Errorf("EmbedModel = %q, want %q", c.Gateway.EmbedModel, d.Gateway.EmbedModel)
	}
	if c.Gateway.Model != d.Gateway.Model {
		t.Errorf("Model = %q, want %q", c.Gateway.Model, d.Gateway.Model)
	}
	if c.Gateway.RerankModel != d.Gateway.RerankModel {
		t.Errorf("RerankModel = %q, want %q", c.Gateway.RerankModel, d.Gateway.RerankModel)
	}
	if c.Gateway.Driver != d.Gateway.Driver {
		t.Errorf("Driver = %q, want %q", c.Gateway.Driver, d.Gateway.Driver)
	}
	if c.Gateway.APIKey != d.Gateway.APIKey {
		t.Errorf("APIKey = %q, want %q (env-ref default)", c.Gateway.APIKey, d.Gateway.APIKey)
	}

	// Profile/telemetry/server must be filled so Validate passes.
	if c.Profile != d.Profile {
		t.Errorf("Profile = %q, want %q", c.Profile, d.Profile)
	}
	if c.Telemetry.LogLevel != d.Telemetry.LogLevel {
		t.Errorf("LogLevel = %q, want %q", c.Telemetry.LogLevel, d.Telemetry.LogLevel)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate after FillZeroDefaults: unexpected error: %v", err)
	}
}

// TestFillZeroDefaults_PreservesExplicit proves caller-set fields survive.
func TestFillZeroDefaults_PreservesExplicit(t *testing.T) {
	t.Parallel()

	c := Config{}
	c.Store.Driver = "sqlite"
	c.Store.DSN = "/tmp/explicit.db"
	c.Gateway.EmbedDims = 256
	c.Profile = "coding-agent"
	c.FillZeroDefaults()

	if c.Gateway.EmbedDims != 256 {
		t.Errorf("EmbedDims = %d, want 256 (explicit value clobbered)", c.Gateway.EmbedDims)
	}
	if c.Store.DSN != "/tmp/explicit.db" {
		t.Errorf("DSN = %q, want explicit value", c.Store.DSN)
	}
	if c.Profile != "coding-agent" {
		t.Errorf("Profile = %q, want coding-agent", c.Profile)
	}
}

// TestFillZeroDefaults_ProfileLogFormat proves the embedded defaults layer
// resolves the profile-specific telemetry.log_format (fleet → json), mirroring
// config.Load's defaults < profile merge so the embedded fleet matches the
// server fleet (D-067 Wave-A checkpoint).
func TestFillZeroDefaults_ProfileLogFormat(t *testing.T) {
	t.Parallel()

	// fleet profile, log_format unset → must resolve to the profile override.
	fleet := Config{Profile: "fleet"}
	fleet.Store.Driver = "sqlite"
	fleet.Store.DSN = "/tmp/fleet.db"
	fleet.FillZeroDefaults()
	if fleet.Telemetry.LogFormat != "json" {
		t.Errorf("fleet log_format = %q, want json (profile override; embedded must match server)",
			fleet.Telemetry.LogFormat)
	}

	// assistant profile → flat default text.
	asst := Config{Profile: "assistant"}
	asst.Store.Driver = "sqlite"
	asst.Store.DSN = "/tmp/asst.db"
	asst.FillZeroDefaults()
	if asst.Telemetry.LogFormat != "text" {
		t.Errorf("assistant log_format = %q, want text", asst.Telemetry.LogFormat)
	}

	// Explicit caller value survives the profile override (file/env > profile).
	explicit := Config{Profile: "fleet"}
	explicit.Store.Driver = "sqlite"
	explicit.Store.DSN = "/tmp/exp.db"
	explicit.Telemetry.LogFormat = "text"
	explicit.FillZeroDefaults()
	if explicit.Telemetry.LogFormat != "text" {
		t.Errorf("explicit log_format = %q, want text (caller value clobbered by profile)",
			explicit.Telemetry.LogFormat)
	}
}
