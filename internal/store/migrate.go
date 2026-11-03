// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrSchemaTooNew is returned when the database was written by a newer TuxMail version.
var ErrSchemaTooNew = errors.New("store: database schema is newer than this program")

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the embedded migrations. File names must look like
// "0001_description.sql"; versions must be unique and start at 1 without gaps.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("store: bad migration file name %q", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("store: bad migration version in %q", name)
		}
		body, err := fs.ReadFile(fsys, path.Join("migrations", name))
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migrations not contiguous at %q (want version %d)", m.name, i+1)
		}
	}
	return ms, nil
}

func (s *Store) migrate(ctx context.Context) error {
	ms, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}
	return s.applyMigrations(ctx, ms)
}

func (s *Store) applyMigrations(ctx context.Context, ms []migration) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_version: %w", err)
	}
	for _, m := range ms {
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			// Re-read the version inside the write transaction so that two
			// processes opening the same database cannot apply a migration twice.
			cur, err := schemaVersion(ctx, tx)
			if err != nil {
				return err
			}
			if cur >= m.version {
				return nil
			}
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_version(version, applied_at) VALUES (?, ?)`,
				m.version, time.Now().Unix())
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
	}
	cur, err := schemaVersion(ctx, s.db)
	if err != nil {
		return err
	}
	if cur > len(ms) {
		return fmt.Errorf("%w (database version %d, supported %d)", ErrSchemaTooNew, cur, len(ms))
	}
	return nil
}

func schemaVersion(ctx context.Context, q querier) (int, error) {
	var v sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

// SchemaVersion returns the currently applied schema version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return schemaVersion(ctx, s.db)
}
