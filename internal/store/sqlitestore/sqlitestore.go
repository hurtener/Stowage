// Package sqlitestore provides the SQLite Store driver for Stowage.
//
// It uses modernc.org/sqlite (pure-Go, CGo-free) and a dedicated writer
// goroutine to serialise all mutations, avoiding SQLITE_BUSY under concurrent
// writers (a documented failure mode of the Python predecessor; D-009).
//
// PRAGMAs set at open time: journal_mode=WAL, busy_timeout=5000,
// foreign_keys=ON, synchronous=NORMAL.
// The read pool uses a second *sql.DB with MaxOpenConns bounded.
package sqlitestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	// register the modernc pure-Go driver under the name "sqlite"
	_ "modernc.org/sqlite"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/store/migrations"
)

const (
	writerChanSize = 1024
	maxReadConns   = 8
	batchSize      = 32 // max ops per micro-batch transaction
)

// writeOp is a single write operation sent to the writer goroutine.
type writeOp struct {
	fn  func(*sql.Tx) error
	res chan error
}

// sqliteStore implements store.Store backed by modernc.org/sqlite.
type sqliteStore struct {
	rdb     *sql.DB       // read pool
	writeCh chan writeOp  // bounded write channel
	done    chan struct{} // closed when writer exits
	log     *slog.Logger
}

// Open opens a SQLite store at dsn and starts the writer goroutine.
func Open(ctx context.Context, cfg config.StoreConfig) (store.Store, error) {
	dsn := cfg.DSN
	if dsn == "" {
		dsn = "./data/stowage.db"
	}

	// Writer connection — single open connection, WAL + PRAGMAs.
	wdb, err := openDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: open writer: %w", err)
	}
	wdb.SetMaxOpenConns(1)

	// Read pool — separate connection.
	rdb, err := openDB(dsn)
	if err != nil {
		_ = wdb.Close()
		return nil, fmt.Errorf("sqlitestore: open reader: %w", err)
	}
	rdb.SetMaxOpenConns(maxReadConns)

	s := &sqliteStore{
		rdb:     rdb,
		writeCh: make(chan writeOp, writerChanSize),
		done:    make(chan struct{}),
		log:     slog.Default().With("driver", "sqlite"),
	}

	go s.writerLoop(wdb)

	return s, nil
}

func openDB(dsn string) (*sql.DB, error) {
	// Append pragmas to the DSN as URI query parameters.
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	uri := dsn + sep + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// writerLoop is the single-writer goroutine. It micro-batches ops into one
// transaction; on commit failure it retries each op individually.
func (s *sqliteStore) writerLoop(wdb *sql.DB) {
	defer func() {
		_ = wdb.Close()
		close(s.done)
	}()

	for {
		// Block until at least one op arrives (or channel is closed).
		first, ok := <-s.writeCh
		if !ok {
			return
		}

		// Collect up to batchSize ops (non-blocking after the first).
		batch := []writeOp{first}
	drain:
		for len(batch) < batchSize {
			select {
			case op, ok := <-s.writeCh:
				if !ok {
					break drain
				}
				batch = append(batch, op)
			default:
				break drain
			}
		}

		if len(batch) == 1 {
			// Fast path: single op.
			s.execBatch(wdb, batch)
			continue
		}

		// Try to commit the whole batch in one transaction.
		tx, err := wdb.Begin()
		if err != nil {
			// Begin itself failed — fall back to individual ops.
			s.execBatch(wdb, batch)
			continue
		}

		batchErr := false
		for _, op := range batch {
			if err := op.fn(tx); err != nil {
				_ = tx.Rollback()
				batchErr = true
				break
			}
		}

		if batchErr {
			// Batch failed — retry individually.
			s.execBatch(wdb, batch)
			continue
		}

		if err := tx.Commit(); err != nil {
			// Commit failed — retry individually.
			s.execBatch(wdb, batch)
			continue
		}

		// Success — signal all ops.
		for _, op := range batch {
			op.res <- nil
		}
	}
}

// execBatch executes each op individually.
func (s *sqliteStore) execBatch(wdb *sql.DB, batch []writeOp) {
	for _, op := range batch {
		tx, err := wdb.Begin()
		if err != nil {
			op.res <- fmt.Errorf("sqlitestore: begin: %w", err)
			continue
		}
		if err := op.fn(tx); err != nil {
			_ = tx.Rollback()
			op.res <- err
			continue
		}
		if err := tx.Commit(); err != nil {
			op.res <- fmt.Errorf("sqlitestore: commit: %w", err)
			continue
		}
		op.res <- nil
	}
}

// exec sends a write operation to the writer goroutine.
func (s *sqliteStore) exec(ctx context.Context, fn func(*sql.Tx) error) error {
	op := writeOp{fn: fn, res: make(chan error, 1)}
	select {
	case s.writeCh <- op:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-op.res:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Migrate applies pending migrations (idempotent; checksum-guarded).
func (s *sqliteStore) Migrate(ctx context.Context) error {
	files, err := listMigrationFiles(migrations.SQLite, "sqlite")
	if err != nil {
		return fmt.Errorf("sqlitestore: list migrations: %w", err)
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

func (s *sqliteStore) applyMigration(ctx context.Context, version, sqlText string) error {
	want := checksum(sqlText)

	return s.exec(ctx, func(tx *sql.Tx) error {
		// Ensure schema_migrations exists (bootstrap).
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT    NOT NULL PRIMARY KEY,
			checksum   TEXT    NOT NULL,
			applied_at INTEGER NOT NULL
		)`)
		if err != nil {
			return fmt.Errorf("sqlitestore: bootstrap schema_migrations: %w", err)
		}

		var got string
		err = tx.QueryRow(`SELECT checksum FROM schema_migrations WHERE version = ?`, version).Scan(&got)
		if err == nil {
			// Already applied — verify checksum.
			if got != want {
				return fmt.Errorf("%w: version %q", store.ErrChecksumMismatch, version)
			}
			return nil // idempotent
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlitestore: check migration %q: %w", version, err)
		}

		// Apply the migration — execute each statement split by semicolons.
		stmts := splitStatements(sqlText)
		for _, stmt := range stmts {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("sqlitestore: exec migration %q: %w", version, err)
			}
		}

		now := time.Now().UnixMilli()
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations(version, checksum, applied_at) VALUES(?,?,?)`,
			version, want, now,
		); err != nil {
			return fmt.Errorf("sqlitestore: record migration %q: %w", version, err)
		}
		return nil
	})
}

// splitStatements splits a SQL script on semicolons, preserving empty strings
// so blank lines are skipped by the caller.
func splitStatements(sql string) []string {
	return strings.Split(sql, ";")
}

// Close drains the write channel, stops the writer, and closes the read pool.
func (s *sqliteStore) Close(ctx context.Context) error {
	close(s.writeCh)
	select {
	case <-s.done:
	case <-ctx.Done():
		s.log.WarnContext(ctx, "sqlitestore: close timed out waiting for writer drain")
	}
	return s.rdb.Close()
}

// Sub-store accessors.

func (s *sqliteStore) Records() store.RecordStore  { return &recordStore{s} }
func (s *sqliteStore) Memories() store.MemoryStore { return &memoryStore{s} }
func (s *sqliteStore) Topics() store.TopicStore    { return &topicStore{s} }
func (s *sqliteStore) Buffers() store.BufferStore  { return &bufferStore{s} }
func (s *sqliteStore) Keys() auth.Keyring          { return &keyStore{s} }
func (s *sqliteStore) Events() store.EventStore    { return &eventStore{s} }
func (s *sqliteStore) Ops() store.OpsStore         { return &opsStore{s} }

// nullStr converts empty string to nil (for nullable TEXT columns).
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
