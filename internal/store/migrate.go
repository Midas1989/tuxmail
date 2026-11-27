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
// "0001_description.sql"; versions must be unique. Gaps are allowed so that
// migrations developed on parallel branches can be merged in any order; each
// version is applied exactly once regardless of the order it arrives in.
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
	for i := 1; i < len(ms); i++ {
		if ms[i].version == ms[i-1].version {
			return nil, fmt.Errorf("store: duplicate migration version %d (%q, %q)", ms[i].version, ms[i-1].name, ms[i].name)
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
	if _, err := s.q(ctx).ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_version: %w", err)
	}
	for _, m := range ms {
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			// Check inside the write transaction so that two processes opening
			// the same database cannot apply a migration twice. Versions are
			// checked individually (not against the maximum) so that a lower
			// version merged later is still applied.
			var applied bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_version WHERE version = ?)`,
				m.version).Scan(&applied); err != nil {
				return err
			}
			if applied {
				return nil
			}
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version, applied_at) VALUES (?, ?)`,
				m.version, time.Now().Unix())
			return err
		})
		if err != nil {
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
	}
	// Any applied version this program does not know means a newer program
	// wrote the database.
	known := make(map[int]bool, len(ms))
	maxKnown := 0
	for _, m := range ms {
		known[m.version] = true
		maxKnown = max(maxKnown, m.version)
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT version FROM schema_version ORDER BY version`)
	if err != nil {
		return err
	}
	applied, err := collect(rows, func(sc scanner) (int, error) {
		var v int
		return v, sc.Scan(&v)
	})
	if err != nil {
		return err
	}
	for _, v := range applied {
		if !known[v] {
			return fmt.Errorf("%w (database has version %d, supported up to %d)", ErrSchemaTooNew, v, maxKnown)
		}
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
