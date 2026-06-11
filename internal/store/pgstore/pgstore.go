// Package pgstore provides the PostgreSQL Store driver for Stowage.
//
// It uses github.com/jackc/pgx/v5/pgxpool. Tests are gated on the
// STOWAGE_TEST_PG_DSN environment variable and skipped when unset.
//
// AdvisoryLock uses pg_advisory_lock (blocking) / pg_advisory_unlock.
package pgstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/store/migrations"
)

// pgStore implements store.Store backed by PostgreSQL via pgx/v5.
type pgStore struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// Open opens a PostgreSQL store at the given DSN.
func Open(ctx context.Context, cfg config.StoreConfig) (store.Store, error) {
	dsn := cfg.DSN
	if dsn == "" {
		return nil, fmt.Errorf("pgstore: DSN is required")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}
	s := &pgStore{
		pool: pool,
		log:  slog.Default().With("driver", "postgres"),
	}
	return s, nil
}

// Migrate applies pending migrations (idempotent; checksum-guarded).
func (s *pgStore) Migrate(ctx context.Context) error {
	files, err := listMigrationFiles(migrations.Postgres, "postgres")
	if err != nil {
		return fmt.Errorf("pgstore: list migrations: %w", err)
	}
	for _, mf := range files {
		if err := s.applyMigration(ctx, mf.version, mf.sql); err != nil {
			return err
		}
	}
	return nil
}

type migrationFile struct {
	version string
	sql     string
}

func listMigrationFiles(fsys fs.FS, dir string) ([]migrationFile, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		data, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migrationFile{
			version: strings.TrimSuffix(e.Name(), ".sql"),
			sql:     string(data),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func checksum(sql string) string {
	h := sha256.Sum256([]byte(sql))
	return fmt.Sprintf("%x", h)
}

func (s *pgStore) applyMigration(ctx context.Context, version, sqlText string) error {
	want := checksum(sqlText)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgstore: begin migration tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Bootstrap schema_migrations.
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT   NOT NULL PRIMARY KEY,
		checksum   TEXT   NOT NULL,
		applied_at BIGINT NOT NULL
	)`); err != nil {
		return fmt.Errorf("pgstore: bootstrap schema_migrations: %w", err)
	}

	var got string
	err = tx.QueryRow(ctx,
		`SELECT checksum FROM schema_migrations WHERE version = $1`, version,
	).Scan(&got)
	if err == nil {
		if got != want {
			return fmt.Errorf("%w: version %q", store.ErrChecksumMismatch, version)
		}
		return tx.Commit(ctx) // already applied
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("pgstore: check migration %q: %w", version, err)
	}

	// Apply each statement.
	for _, stmt := range splitStatements(sqlText) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("pgstore: exec migration %q: %w", version, err)
		}
	}

	now := time.Now().UnixMilli()
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations(version, checksum, applied_at) VALUES($1,$2,$3)`,
		version, want, now,
	); err != nil {
		return fmt.Errorf("pgstore: record migration %q: %w", version, err)
	}
	return tx.Commit(ctx)
}

// splitStatements splits a migration file into executable statements.
// Line comments are stripped first so a ';' inside a comment never splits a
// statement (found in CI: a comment containing ';' produced a bogus
// fragment). Our postgres migrations contain no ';' inside literals or
// function bodies; if that changes, this splitter must grow with them.
func splitStatements(sql string) []string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	var out []string
	for _, stmt := range strings.Split(b.String(), ";") {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// Close shuts down the connection pool.
func (s *pgStore) Close(_ context.Context) error {
	s.pool.Close()
	return nil
}

// Sub-store accessors.

func (s *pgStore) Records() store.RecordStore  { return &recordStore{s} }
func (s *pgStore) Memories() store.MemoryStore { return &memoryStore{s} }
func (s *pgStore) Topics() store.TopicStore    { return &topicStore{s} }
func (s *pgStore) Buffers() store.BufferStore  { return &bufferStore{s} }
func (s *pgStore) Keys() auth.Keyring          { return &keyStore{s} }
func (s *pgStore) Events() store.EventStore    { return &eventStore{s} }
func (s *pgStore) Branches() store.BranchStore { return &branchStore{s} }
func (s *pgStore) Ops() store.OpsStore         { return &opsStore{s} }
func (s *pgStore) Vectors() store.VectorStore  { return &vectorStore{s} }

// nullStr converts empty string to nil.
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// AppliedMigrations returns versions from schema_migrations ascending; empty
// (no error) when the table does not exist yet.
func (s *pgStore) AppliedMigrations(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT version FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, nil
		}
		return nil, fmt.Errorf("pgstore: applied migrations: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("pgstore: scan migration: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
