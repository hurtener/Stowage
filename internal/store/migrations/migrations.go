// Package migrations embeds the SQL migration files for all drivers.
// Each driver imports this package and uses the appropriate FS.
package migrations

import (
	"embed"
	"sort"
	"strings"
)

// SQLite holds the embedded SQLite migration files.
//
//go:embed sqlite
var SQLite embed.FS

// Postgres holds the embedded PostgreSQL migration files.
//
//go:embed postgres
var Postgres embed.FS

// Known returns the sorted migration names (without .sql) embedded for the
// given driver ("sqlite" or "postgres"). Used by `stowage migrate --status`.
func Known(driver string) []string {
	var fsys embed.FS
	switch driver {
	case "postgres":
		fsys = Postgres
	default:
		fsys = SQLite
		driver = "sqlite"
	}
	entries, err := fsys.ReadDir(driver)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := strings.TrimSuffix(e.Name(), ".sql")
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
