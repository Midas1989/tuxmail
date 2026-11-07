// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Forward from the schema before: existing data stays, new columns have
// their defaults. Back with the down script: the tables are gone, the data is
// still there, and the older migration set opens the database again. Without
// the script an older program refuses the newer database.
func TestMigration0060ForwardAndBack(t *testing.T) {
	ctx := context.Background()
	all, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	var before []migration
	for _, m := range all {
		if m.version != 60 {
			before = append(before, m)
		}
	}
	if len(before) != len(all)-1 {
		t.Fatal("migration 60 not found")
	}
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	// Back to the old schema, first with the script: proves it runs on a real database.
	down, err := os.ReadFile(filepath.Join("migrations", "down", "0060_server_connection.sql"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(script string) {
		t.Helper()
		if _, err := s.DB().ExecContext(ctx, script); err != nil {
			t.Fatal(err)
		}
	}
	run(string(down))
	if err := s.applyMigrations(ctx, before); err != nil {
		t.Fatalf("old program on the reverted database: %v", err)
	}
	sigID, err := s.CreateSignatureOldSchema(ctx, "Alt")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('remote_cache', 'remote_cache_messages', 'remote_outbox', 'remote_outbox_uploads', 'idempotency_keys')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("tables left after down: %d %v", n, err)
	}
	// Forward with data in place.
	if err := s.applyMigrations(ctx, all); err != nil {
		t.Fatal(err)
	}
	sig, err := s.GetSignature(ctx, sigID)
	if err != nil || sig.Name != "Alt" || sig.UpdatedAt != 0 {
		t.Fatalf("signature after migrating: %+v %v", sig, err)
	}
	if err := s.AddRemoteOutbox(ctx, RemoteOutboxItem{ID: "x", Kind: "send", Method: "POST", Path: "/api/send", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// An older program refuses the newer database instead of damaging it.
	if err := s.applyMigrations(ctx, before); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("older program: %v", err)
	}
	// And back again, with the data of the newer version in the tables.
	run(string(down))
	if err := s.applyMigrations(ctx, before); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := s.DB().QueryRowContext(ctx, `SELECT name FROM signatures WHERE id = ?`, sigID).Scan(&name); err != nil || name != "Alt" {
		t.Fatalf("data lost: %q %v", name, err)
	}
}

// CreateSignatureOldSchema inserts as the program before migration 60 did.
func (s *Store) CreateSignatureOldSchema(ctx context.Context, name string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO signatures(name) VALUES (?)`, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func TestSignatureUpdatedAt(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.CreateSignature(ctx, domain.Signature{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := s.GetSignature(ctx, id)
	if sig.UpdatedAt == 0 {
		t.Fatal("no time on create")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE signatures SET updated_at = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSignature(ctx, domain.Signature{ID: id, Name: "B"}); err != nil {
		t.Fatal(err)
	}
	sig, _ = s.GetSignature(ctx, id)
	if sig.UpdatedAt <= 1 {
		t.Fatal("no time on update")
	}
}

func TestRemoteCacheIsBounded(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1000, 0)
	big := []byte(strings.Repeat("x", 10_000))
	for i := range 100 {
		if err := s.PutRemoteCache(ctx, RemoteCacheEntry{Key: "/api/k" + string(rune('A'+i)), Body: big}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// The one used last is kept although it is old.
	if _, ok, _ := s.GetRemoteCache(ctx, "/api/kA", now.Add(time.Hour)); !ok {
		t.Fatal("missing")
	}
	if _, err := s.PruneRemoteCache(ctx, 300_000); err != nil {
		t.Fatal(err)
	}
	size, _ := s.RemoteCacheSize(ctx)
	if size > 300_000 {
		t.Fatalf("size %d", size)
	}
	if _, ok, _ := s.GetRemoteCache(ctx, "/api/kA", now.Add(2*time.Hour)); !ok {
		t.Fatal("recently used entry removed")
	}
	if _, ok, _ := s.GetRemoteCache(ctx, "/api/kB", now.Add(2*time.Hour)); ok {
		t.Fatal("oldest entry kept")
	}
	if err := s.ClearRemoteCache(ctx); err != nil {
		t.Fatal(err)
	}
	if size, _ := s.RemoteCacheSize(ctx); size != 0 {
		t.Fatalf("after clear: %d", size)
	}
}

func TestRemoteMessagesSearch(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	if err := s.PutRemoteMessages(ctx, []RemoteCachedMessage{
		{ID: 1, AccountID: 1, FolderIDs: []int64{10}, Date: "2026-01-01", SummaryJSON: `{"id":1}`, SearchText: "rechnung 100% bezahlt"},
		{ID: 2, AccountID: 1, FolderIDs: []int64{11}, Date: "2026-01-02", SummaryJSON: `{"id":2}`, SearchText: "urlaub"},
	}, now); err != nil {
		t.Fatal(err)
	}
	got, total, err := s.ListRemoteMessages(ctx, RemoteMessageQuery{FolderID: 10})
	if err != nil || total != 1 || got[0] != `{"id":1}` {
		t.Fatalf("folder: %v %d %v", got, total, err)
	}
	// LIKE characters in the search are plain text.
	if _, total, _ := s.ListRemoteMessages(ctx, RemoteMessageQuery{Text: "%"}); total != 1 {
		t.Fatalf("percent: %d", total)
	}
	if _, total, _ := s.ListRemoteMessages(ctx, RemoteMessageQuery{Text: "_rlaub"}); total != 0 {
		t.Fatalf("underscore: %d", total)
	}
	// A later list answer does not forget the body text of an opened mail.
	if err := s.PutRemoteMessages(ctx, []RemoteCachedMessage{{ID: 1, AccountID: 1, SummaryJSON: `{"id":1,"x":1}`, SearchText: "kurz"}}, now); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := s.ListRemoteMessages(ctx, RemoteMessageQuery{Text: "bezahlt"}); total != 1 {
		t.Fatalf("text lost: %d", total)
	}
}

func TestIdempotencyStates(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	rec, err := s.BeginIdempotency(ctx, "k", "r", "h", now, time.Hour)
	if err != nil || rec.State != IdempotencyNew {
		t.Fatalf("%+v %v", rec, err)
	}
	if rec, _ := s.BeginIdempotency(ctx, "k", "r", "h", now, time.Hour); rec.State != IdempotencyPending {
		t.Fatalf("second: %+v", rec)
	}
	if rec, _ := s.BeginIdempotency(ctx, "k", "r", "other", now, time.Hour); rec.State != IdempotencyMismatch {
		t.Fatalf("other body: %+v", rec)
	}
	if err := s.FinishIdempotency(ctx, "k", 200, "application/json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.BeginIdempotency(ctx, "k", "r", "h", now, time.Hour); rec.State != IdempotencyDone || string(rec.Body) != "{}" {
		t.Fatalf("done: %+v", rec)
	}
	if err := s.ReleaseIdempotency(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.BeginIdempotency(ctx, "k", "r", "h", now, time.Hour); rec.State != IdempotencyNew {
		t.Fatalf("released: %+v", rec)
	}
	// Old records are removed.
	if rec, _ := s.BeginIdempotency(ctx, "k", "r", "h", now.Add(2*time.Hour), time.Hour); rec.State != IdempotencyNew {
		t.Fatalf("retention: %+v", rec)
	}
}
