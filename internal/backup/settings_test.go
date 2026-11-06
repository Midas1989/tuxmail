// SPDX-License-Identifier: AGPL-3.0-or-later

package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/backup"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/store"
)

func tablesOf(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func serverFixture(t *testing.T) (dataDir string) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	svc, err := auth.Open(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc.Close()
	return cfg.DataDir
}

func userFixture(t *testing.T, dataDir string, id int) string {
	t.Helper()
	dir := filepath.Join(dataDir, "users", fmt.Sprint(id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	return filepath.Join(dir, "tuxmail.db")
}

// A new table (a new migration) fails here until it is classified, in
// internal/backup/settings.go. Listed tables that no longer exist fail as well.
func TestSettingsTablesAreClassified(t *testing.T) {
	dataDir := serverFixture(t)
	for name, c := range map[string]struct {
		path    string
		classes map[string]backup.TableClass
	}{
		"tuxmail.db": {userFixture(t, dataDir, 1), backup.UserStoreTables()},
		"server.db":  {filepath.Join(dataDir, "server.db"), backup.ServerDBTables()},
	} {
		have := map[string]bool{}
		for _, table := range tablesOf(t, c.path) {
			have[table] = true
			if c.classes[table] == 0 {
				t.Errorf("%s: table %s is not classified: add it to the list in internal/backup/settings.go", name, table)
			}
		}
		for table := range c.classes {
			if !have[table] {
				t.Errorf("%s: classified table %s no longer exists", name, table)
			}
		}
	}
}

// At run time an unknown table stops the backup instead of traveling along.
func TestSettingsRefusesUnclassifiedTable(t *testing.T) {
	dataDir := serverFixture(t)
	path := userFixture(t, dataDir, 1)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE new_mail_data (body TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = backup.CreateSettings(context.Background(), dataDir, t.TempDir())
	if !errors.Is(err, backup.ErrUnclassifiedTable) {
		t.Fatal(err)
	}
}

// Snapshots of both kinds of database are consistent while the service writes:
// the writers commit pairs of rows, a copy must never hold half a pair.
func TestSettingsConsistentUnderWriteLoad(t *testing.T) {
	dataDir := serverFixture(t)
	userDB := userFixture(t, dataDir, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	var commits, failures atomic.Int64
	write := func(path, stmt string) {
		// The same connection settings as the service: WAL, busy timeout.
		db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_txlock=immediate")
		if err != nil {
			t.Error(err)
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer db.Close()
			for i := 0; ctx.Err() == nil; i++ {
				tx, err := db.Begin()
				if err != nil {
					failures.Add(1)
					continue
				}
				_, e1 := tx.Exec(stmt, fmt.Sprintf("a-%d", i))
				_, e2 := tx.Exec(stmt, fmt.Sprintf("b-%d", i))
				if e1 != nil || e2 != nil || tx.Commit() != nil {
					tx.Rollback()
					failures.Add(1)
					continue
				}
				commits.Add(1)
			}
		}()
	}
	write(userDB, `INSERT INTO spell_words(word, created_at) VALUES (?, 1)`)
	write(filepath.Join(dataDir, "server.db"), `INSERT INTO groups(name, created_at) VALUES (?, 1)`)

	for range 5 {
		before := commits.Load()
		out := t.TempDir()
		if _, err := backup.CreateSettings(context.Background(), dataDir, out); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(out, filepath.Base(dataDir))
		for path, table := range map[string]string{filepath.Join(root, "users", "1", "tuxmail.db"): "spell_words", filepath.Join(root, "server.db"): "groups"} {
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n%2 != 0 {
				t.Errorf("%s: half a transaction in the copy (%d rows)", table, n)
			}
			var ok string
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
				t.Errorf("%s: integrity %q %v", path, ok, err)
			}
			db.Close()
		}
		for commits.Load() == before && failures.Load() == 0 {
			// the writers go on during the copies
		}
	}
	cancel()
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d writes failed while the copies ran (%d committed)", failures.Load(), commits.Load())
	}
	if commits.Load() == 0 {
		t.Fatal("no write load")
	}
}
