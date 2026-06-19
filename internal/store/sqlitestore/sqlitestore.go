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
	"sync"
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
// ctx is checked by the writer before executing fn (W2).
type writeOp struct {
	fn  func(*sql.Tx) error
	res chan error
	ctx context.Context
}

// sqliteStore implements store.Store backed by modernc.org/sqlite.
type sqliteStore struct {
	rdb       *sql.DB       // read pool
	writeCh   chan writeOp  // bounded write channel
	done      chan struct{} // closed when writer exits
	quit      chan struct{} // closed by Close to fence new ops (W1)
	closeOnce sync.Once     // ensures Close logic runs exactly once (W1)
	execMu    sync.RWMutex  // read-held during the send window; write-held by Close (W1)
	log       *slog.Logger
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
		quit:    make(chan struct{}),
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
			// W2: honour ctx cancellation before executing.
			if op.ctx.Err() != nil {
				_ = tx.Rollback()
				batchErr = true
				break
			}
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
// W2: checks op.ctx.Err() immediately before executing each op's fn;
// if the context is already cancelled the fn is skipped and ctx.Err() is returned.
func (s *sqliteStore) execBatch(wdb *sql.DB, batch []writeOp) {
	for _, op := range batch {
		if err := op.ctx.Err(); err != nil {
			op.res <- err
			continue
		}
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
//
// Returns store.ErrClosed if Close has been called.
// Returns ctx.Err() if the context is cancelled before the op is sent.
//
// Remaining window (W2): if ctx is cancelled after the op has been sent but
// before the writer goroutine starts executing it, the writer checks op.ctx.Err()
// and returns ctx.Err() without executing fn. If cancellation races the start of
// execution (fn has begun running), the op may commit successfully before this
// call returns ctx.Err() — callers must be idempotent.
func (s *sqliteStore) exec(ctx context.Context, fn func(*sql.Tx) error) error {
	// W1: hold the read lock only for the send window, not while blocking on result.
	// Close() takes the write lock after closing quit, so no send races a close.
	s.execMu.RLock()
	select {
	case <-s.quit:
		s.execMu.RUnlock()
		return store.ErrClosed
	default:
	}
	op := writeOp{fn: fn, res: make(chan error, 1), ctx: ctx}
	select {
	case s.writeCh <- op:
		s.execMu.RUnlock() // release before blocking on result
	case <-s.quit:
		s.execMu.RUnlock()
		return store.ErrClosed
	case <-ctx.Done():
		s.execMu.RUnlock()
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

// splitStatements splits a SQL script into individual statements.
//
// It handles two SQLite-specific complications:
//   - Single-line comments (-- ...) may contain semicolons that must not be
//     treated as statement terminators.
//   - Trigger bodies (BEGIN ... END) contain inner statements separated by
//     semicolons; those semicolons must be preserved inside the trigger.
func splitStatements(sqlText string) []string {
	// Strip single-line comments line by line to avoid ';' inside them
	// being mistaken for statement terminators.
	var stripped strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		stripped.WriteString(line)
		stripped.WriteByte('\n')
	}

	// Split on ';', tracking BEGIN/END depth so that semicolons inside
	// SQLite trigger bodies are preserved rather than treated as terminators.
	text := stripped.String()
	var stmts []string
	var cur strings.Builder
	depth := 0

	for _, part := range strings.Split(text, ";") {
		upper := strings.ToUpper(part)
		depth += sqlWordCount(upper, "BEGIN") - sqlWordCount(upper, "END")
		if depth < 0 {
			depth = 0 // safety — shouldn't happen in well-formed SQL
		}
		cur.WriteString(part)
		if depth == 0 {
			if s := strings.TrimSpace(cur.String()); s != "" {
				stmts = append(stmts, cur.String())
			}
			cur.Reset()
		} else {
			cur.WriteByte(';') // preserve inner semicolons inside BEGIN...END
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		stmts = append(stmts, cur.String())
	}
	return stmts
}

// sqlWordCount counts whole-word occurrences of word in text.
// text and word must already be uppercased.
func sqlWordCount(text, word string) int {
	count := 0
	start := 0
	for {
		idx := strings.Index(text[start:], word)
		if idx == -1 {
			break
		}
		abs := start + idx
		before := abs == 0 || !sqlWordChar(text[abs-1])
		after := abs+len(word) >= len(text) || !sqlWordChar(text[abs+len(word)])
		if before && after {
			count++
		}
		start = abs + len(word)
	}
	return count
}

// sqlWordChar reports whether b is an identifier character (letter, digit, _).
func sqlWordChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') || b == '_'
}

// Close signals the store closed, drains in-flight writes, and releases resources.
//
// W1 protocol:
//  1. close(s.quit)    — future exec calls see ErrClosed immediately.
//  2. s.execMu.Lock()  — wait for any exec call that is mid-send to finish.
//  3. close(s.writeCh) — signal the writer goroutine to exit after draining.
//  4. s.execMu.Unlock()
//
// After step 2 no new ops can be sent (quit is closed and no sender holds the
// read lock any more), so closing writeCh is safe.
func (s *sqliteStore) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		close(s.quit)    // fence new sends
		s.execMu.Lock()  // wait for all in-flight sends to complete
		close(s.writeCh) // drain signal for writerLoop
		s.execMu.Unlock()
	})
	select {
	case <-s.done:
	case <-ctx.Done():
		s.log.WarnContext(ctx, "sqlitestore: close timed out waiting for writer drain")
	}
	return s.rdb.Close()
}

// Sub-store accessors.

func (s *sqliteStore) Records() store.RecordStore       { return &recordStore{s} }
func (s *sqliteStore) Memories() store.MemoryStore      { return &memoryStore{s} }
func (s *sqliteStore) Topics() store.TopicStore         { return &topicStore{s} }
func (s *sqliteStore) Buffers() store.BufferStore       { return &bufferStore{s} }
func (s *sqliteStore) Keys() auth.Keyring               { return &keyStore{s} }
func (s *sqliteStore) Events() store.EventStore         { return &eventStore{s} }
func (s *sqliteStore) Branches() store.BranchStore      { return &branchStore{s} }
func (s *sqliteStore) Episodes() store.EpisodeStore     { return &episodeStore{s} }
func (s *sqliteStore) Ops() store.OpsStore              { return &opsStore{s} }
func (s *sqliteStore) Vectors() store.VectorStore       { return &vectorStore{s} }
func (s *sqliteStore) Injections() store.InjectionStore { return &injectionStore{s} }
func (s *sqliteStore) Grants() store.GrantStore         { return &grantStore{s} }

func (s *sqliteStore) Suggestions() store.SuggestionStore      { return &suggestionStore{s} }
func (s *sqliteStore) ScopeSettings() store.ScopeSettingsStore { return &scopeSettingsStore{s} }

// nullStr converts empty string to nil (for nullable TEXT columns).
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// Tenants returns distinct tenant IDs from the memories table.
// Used by lifecycle sweeps to enumerate tenants for per-scope passes (D-057).
func (s *sqliteStore) Tenants(ctx context.Context) ([]string, error) {
	// Union memories AND records: the reflection sweep (Phase 19, D-077) operates
	// on outcome-tagged RECORDS, which exist for a fresh scope before any memory —
	// a memories-only listing would make such scopes invisible to reflection. The
	// memory-operating sweeps (decay/dedupe/rollup/confirm) no-op on a record-only
	// tenant.
	rows, err := s.rdb.QueryContext(ctx,
		`SELECT tenant_id FROM memories UNION SELECT tenant_id FROM records ORDER BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: tenants: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("sqlitestore: scan tenant: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AppliedMigrations returns versions from schema_migrations ascending; empty
// (no error) when the table does not exist yet.
func (s *sqliteStore) AppliedMigrations(ctx context.Context) ([]string, error) {
	rows, err := s.rdb.QueryContext(ctx,
		`SELECT version FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("sqlitestore: applied migrations: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("sqlitestore: scan migration: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
