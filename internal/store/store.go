// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store implements TuxMail's SQLite persistence layer (KONZEPT chapter 5).
//
// The database runs in WAL mode with foreign keys enabled. All timestamps are
// stored as Unix seconds (UTC); a zero time.Time is stored as NULL where the
// column is nullable.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store is a handle to the TuxMail database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database at path and applies pending migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{
		"foreign_keys(1)",
		"journal_mode(WAL)",
		"busy_timeout(10000)",
		"synchronous(NORMAL)",
	} {
		q.Add("_pragma", p)
	}
	// Write transactions take the RESERVED lock up front; this avoids
	// SQLITE_BUSY on lock upgrades when several goroutines write concurrently.
	q.Set("_txlock", "immediate")
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("store: database path must not contain '?': %q", path)
	}
	dsn := path + "?" + q.Encode()
	// SQLite creates the database (and its -wal/-shm files, which inherit
	// the mode) with 0644 minus umask; the database holds all mail and
	// wrapped keys, so it is created owner-only (AUDIT-C).
	if path != "" && !strings.HasPrefix(path, ":memory:") && !strings.HasPrefix(path, "file:") {
		if f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
			f.Close()
		}
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for packages that need ad-hoc queries
// (e.g. calendar or contact repositories living elsewhere).
func (s *Store) DB() *sql.DB { return s.db }

// withTx runs fn inside a transaction and commits if fn returns nil.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// querier is implemented by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanner is implemented by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func unixOrNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Unix()
}

func timeFromNull(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0).UTC()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// notFound maps sql.ErrNoRows to ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// checkAffected returns ErrNotFound if res affected no rows.
func checkAffected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
