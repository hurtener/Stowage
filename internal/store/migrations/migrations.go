// Package migrations embeds the SQL migration files for all drivers.
// Each driver imports this package and uses the appropriate FS.
package migrations

import "embed"

// SQLite holds the embedded SQLite migration files.
//
//go:embed sqlite
var SQLite embed.FS

// Postgres holds the embedded PostgreSQL migration files.
//
//go:embed postgres
var Postgres embed.FS
