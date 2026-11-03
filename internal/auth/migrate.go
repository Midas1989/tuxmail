// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

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

// ErrSchemaTooNew is returned when server.db was written by a newer TuxMail.
var ErrSchemaTooNew = errors.New("auth: server database schema is newer than this program")

type migration struct {
	version int
	name    string
	sql     string
}

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
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("auth: bad migration file name %q", name)
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
			return nil, fmt.Errorf("auth: migrations not contiguous at %q", m.name)
		}
	}
	return ms, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	ms, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("auth: create schema_version: %w", err)
	}
	for _, m := range ms {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var cur sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&cur); err != nil {
			tx.Rollback()
			return err
		}
		if int(cur.Int64) >= m.version {
			tx.Rollback()
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("auth: migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	var cur sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&cur); err != nil {
		return err
	}
	if int(cur.Int64) > len(ms) {
		return fmt.Errorf("%w (database version %d, supported %d)", ErrSchemaTooNew, cur.Int64, len(ms))
	}
	return nil
}
