package config

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// Explain writes every effective configuration value, its origin, and — for
// secret fields — the env-var status (set|UNSET) without printing the value.
// (AC-4; secrets are never printed per CLAUDE.md §7.)
func (c *Config) Explain(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, key := range allKeys {
		val := c.displayValue(key)
		origin := string(c.prov[key])
		_, _ = fmt.Fprintf(tw, "%s\t= %s\t[%s]\n", key, val, origin)
	}
	return tw.Flush()
}

// displayValue returns the value to show in Explain output.
// Secret fields are redacted to "env.VAR_NAME (set|UNSET)".
func (c *Config) displayValue(key string) string {
	raw := c.getByPath(key)
	if !secretKeyPaths[key] {
		return raw
	}
	// An optional secret left empty (e.g. trace.signing_key default) is simply unset —
	// show it as empty, not redacted (there is nothing to hide).
	if raw == "" {
		return ""
	}
	// Secret: show the env-ref and whether the env var is currently set.
	if !strings.HasPrefix(raw, "env.") {
		// Should not happen after Validate, but be safe.
		return "[REDACTED]"
	}
	varName := strings.TrimPrefix(raw, "env.")
	_, ok := os.LookupEnv(varName)
	status := "UNSET"
	if ok {
		status = "set"
	}
	return fmt.Sprintf("%s (%s)", raw, status)
}
