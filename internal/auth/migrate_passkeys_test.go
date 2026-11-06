// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// Migration 0007 rebuilds the sessions table (new login_method value). Rows,
// ids, the cascade from users, the Web Push link of a session and the
// "subscription ends with the session" trigger must survive that.
func TestMigration0007KeepsSessionsAndPushLinks(t *testing.T) {
	ctx := context.Background()
	db, err := openDB(ctx, filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	var m7 migration
	for _, m := range ms {
		if m.version == 7 {
			m7 = m
			continue
		}
		if m.version > 7 {
			continue
		}
		if _, err := db.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("migration %s: %v", m.name, err)
		}
	}
	if m7.sql == "" {
		t.Fatal("migration 7 missing")
	}
	for _, q := range []string{
		`INSERT INTO users(id, username, dek_wrapped, created_at, updated_at) VALUES (1,'a',x'00',1,1),(2,'b',x'00',1,1)`,
		`INSERT INTO sessions(id, token_hash, user_id, kind, mfa, login_method, created_at, expires_at, last_seen_at)
			VALUES (10, x'01', 1, 'web', 1, 'oidc', 1, 2000000000, 1), (11, x'02', 1, 'device', 0, 'password', 1, 2000000000, 1),
			       (12, x'03', 2, 'web', 0, 'device_code', 1, 2000000000, 1)`,
		`INSERT INTO push_subscriptions(id, user_id, session_id, endpoint, p256dh, auth, created_at)
			VALUES (1, 1, 10, 'https://e/1', 'k', 'a', 1), (2, 1, NULL, 'https://e/2', 'k', 'a', 1), (3, 2, 12, 'https://e/3', 'k', 'a', 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, m7.sql); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 3 {
		t.Fatalf("sessions after migration: %d", n)
	}
	var method string
	var mfa bool
	db.QueryRow(`SELECT login_method, mfa FROM sessions WHERE id = 10`).Scan(&method, &mfa)
	if method != "oidc" || !mfa {
		t.Fatalf("session 10: %q %v", method, mfa)
	}
	links := map[int64]sql.NullInt64{}
	rows, _ := db.Query(`SELECT id, session_id FROM push_subscriptions`)
	for rows.Next() {
		var id int64
		var sid sql.NullInt64
		rows.Scan(&id, &sid)
		links[id] = sid
	}
	rows.Close()
	if links[1].Int64 != 10 || links[2].Valid || links[3].Int64 != 12 {
		t.Fatalf("push links lost: %+v", links)
	}
	// New value accepted, unknown ones still rejected.
	if _, err := db.Exec(`INSERT INTO sessions(token_hash, user_id, login_method, created_at, expires_at, last_seen_at) VALUES (x'04', 1, 'passkey', 1, 2, 1)`); err != nil {
		t.Fatalf("passkey session rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(token_hash, user_id, login_method, created_at, expires_at, last_seen_at) VALUES (x'05', 1, 'magic', 1, 2, 1)`); err == nil {
		t.Fatal("unknown login method accepted")
	}
	// Trigger: ending a session removes its push subscription.
	db.Exec(`DELETE FROM sessions WHERE id = 10`)
	db.QueryRow(`SELECT COUNT(*) FROM push_subscriptions WHERE id = 1`).Scan(&n)
	if n != 0 {
		t.Fatal("sessions_delete_push trigger lost")
	}
	// Foreign key from push_subscriptions and the cascade from users still work.
	db.Exec(`DELETE FROM users WHERE id = 2`)
	db.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions WHERE user_id = 2) + (SELECT COUNT(*) FROM push_subscriptions WHERE user_id = 2)`).Scan(&n)
	if n != 0 {
		t.Fatal("user deletion does not cascade to sessions")
	}
	var idx int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('sessions_user', 'sessions_expiry')`).Scan(&idx)
	if idx != 2 {
		t.Fatalf("session indexes: %d", idx)
	}
	// And the real migration path (fresh database) ends at the same version.
	s, _ := newTestService(t)
	var v int
	s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v)
	if v != len(ms) {
		t.Fatalf("schema version %d, want %d", v, len(ms))
	}
}
