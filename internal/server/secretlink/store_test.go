// SPDX-License-Identifier: AGPL-3.0-or-later

package secretlink

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	mig, err := os.ReadFile("../../auth/migrations/0010_secret_links.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`CREATE TABLE users (id INTEGER PRIMARY KEY, disabled INTEGER NOT NULL DEFAULT 0)`, `INSERT INTO users(id) VALUES (1), (2)`, string(mig)} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	return &Store{DB: db, Now: func() time.Time { return now }}, &now
}

func ct() []byte { return make([]byte, 40) }

func TestOpenOnce(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	l, err := s.Create(ctx, 1, ct(), time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(l.ID) {
		t.Fatalf("bad id %q", l.ID)
	}
	if _, err := s.Open(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, l.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("second open: %v", err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM secret_links`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("row must be deleted after the last view: %d %v", n, err)
	}
}

func TestOpenParallelNeverExceedsMaxViews(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	l, err := s.Create(ctx, 1, ct(), time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Open(ctx, l.ID); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 3 {
		t.Fatalf("%d successful opens, want exactly 3", ok)
	}
}

func TestExpiryAndInvalid(t *testing.T) {
	ctx := context.Background()
	s, now := testStore(t)
	l, err := s.Create(ctx, 1, ct(), time.Hour, 5)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(61 * time.Minute)
	if _, err := s.Open(ctx, l.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("expired: %v", err)
	}
	for _, id := range []string{"", "x", "../../etc/passwd", "AAAAAAAAAAAAAAAAAAAAA!"} {
		if _, err := s.Open(ctx, id); !errors.Is(err, ErrGone) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	for name, f := range map[string]func() error{
		"tiny":     func() error { _, err := s.Create(ctx, 1, make([]byte, 5), time.Hour, 1); return err },
		"huge":     func() error { _, err := s.Create(ctx, 1, make([]byte, MaxCiphertext+1), time.Hour, 1); return err },
		"ttl long": func() error { _, err := s.Create(ctx, 1, ct(), MaxTTL+time.Second, 1); return err },
		"ttl zero": func() error { _, err := s.Create(ctx, 1, ct(), 0, 1); return err },
		"views 0":  func() error { _, err := s.Create(ctx, 1, ct(), time.Hour, 0); return err },
		"views 11": func() error { _, err := s.Create(ctx, 1, ct(), time.Hour, MaxViews+1); return err },
	} {
		if err := f(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestOwnerOnlyAndQuota(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	l, err := s.Create(ctx, 1, ct(), time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, 2, l.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("other user deleted: %v", err)
	}
	if ls, _ := s.List(ctx, 2); len(ls) != 0 {
		t.Fatalf("other user lists %v", ls)
	}
	if ls, _ := s.List(ctx, 1); len(ls) != 1 || ls[0].ID != l.ID {
		t.Fatalf("list: %v", ls)
	}
	if err := s.Delete(ctx, 1, l.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxActivePerUser; i++ {
		if _, err := s.Create(ctx, 1, ct(), time.Hour, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(ctx, 1, ct(), time.Hour, 1); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota: %v", err)
	}
	if _, err := s.Create(ctx, 2, ct(), time.Hour, 1); err != nil {
		t.Fatalf("quota is per user: %v", err)
	}
}

// L-22: the links of a disabled user cannot be opened, and work again once the
// account is enabled.
func TestDisabledOwnerLinksNotOpenable(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	l, err := s.Create(ctx, 1, ct(), time.Hour, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE users SET disabled = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, l.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("open of a disabled owner's link: %v", err)
	}
	var views int
	if err := s.DB.QueryRow(`SELECT views FROM secret_links WHERE id = ?`, l.ID).Scan(&views); err != nil || views != 0 {
		t.Fatalf("a refused open must not count: %d %v", views, err)
	}
	if _, err := s.DB.Exec(`UPDATE users SET disabled = 0 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, l.ID); err != nil {
		t.Fatal(err)
	}
}
