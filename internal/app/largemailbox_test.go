// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/devserver"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// A large mailbox (German Dovecot-like folders, 2500 mails in the INBOX)
// must show its newest mails within seconds of adding the account, long
// before the initial sync is complete ("Keine Mails" bug: the list stayed
// empty while the folder tree already showed server counts), in the
// classic list and in the hybrid/chat conversation list. Sync progress is
// reported in the account status.
func TestLiveLargeMailboxFillsProgressively(t *testing.T) {
	// TUXMAIL_LARGE_N=2500 (or 10000) for the measurements in the report;
	// the default keeps go test ./... fast.
	n := 1200
	if testing.Short() {
		n = 400
	}
	if v, err := strconv.Atoi(os.Getenv("TUXMAIL_LARGE_N")); err == nil && v > 0 {
		n = v
	}
	ctx := context.Background()
	srv, err := devserver.Start(devserver.Config{Users: devserver.DefaultUsers()[:1], Folders: devserver.GermanFolders, SpecialUse: devserver.GermanSpecialUse})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	now := time.Now()
	if err := srv.SeedBulk(devserver.AnnaAddr, "INBOX", n, now, 1); err != nil {
		t.Fatal(err)
	}
	for i, f := range []string{"Archiv", "Archiv/2024", "Archive", "Gesendet", "Papierkorb"} {
		if err := srv.SeedBulk(devserver.AnnaAddr, f, n/10, now, uint64(i+2)); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if keep := os.Getenv("TUXMAIL_KEEP_DIR"); keep != "" { // for EXPLAIN QUERY PLAN checks
		dir = keep
	}
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Mode: app.ModeServer,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Sync: &app.SyncOptions{}, Send: &app.SendOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.Start()
	h, p, _ := net.SplitHostPort(srv.IMAPAddr())
	port, _ := strconv.Atoi(p)
	start := time.Now()
	acc, err := a.CreateAccount(ctx, app.NewAccountInput{Kind: "imap", Email: devserver.AnnaAddr, Name: "Anna Berger",
		Password: devserver.DefaultPassword, Incoming: &app.ServerConfig{Host: h, Port: port, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: devserver.AnnaAddr}})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Since(start)

	var firstClassic, firstHybrid, complete time.Duration
	var sawProgress bool
	var firstSubject string
	deadline := time.Now().Add(5 * time.Minute)
	for complete == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("not complete after 5 min (classic %v, hybrid %v)", firstClassic, firstHybrid)
		}
		page, err := a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "inbox"}, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if firstClassic == 0 && len(page.Items) > 0 {
			firstClassic = time.Since(start)
			firstSubject = page.Items[0].Subject + page.Items[0].Date
		}
		if firstHybrid == 0 {
			convs, err := a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "unified", Role: "inbox"}, Grouping: "thread", Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			if len(convs.Items) > 0 {
				firstHybrid = time.Since(start)
			}
		}
		sts, err := a.SyncStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sts {
			if s.AccountID == acc.ID && s.Total > 0 && s.Done > 0 && s.Done < s.Total {
				sawProgress = true
			}
		}
		if page.Total >= n {
			complete = time.Since(start)
			// Newest first: the very first visible row was the newest mail.
			if got := page.Items[0].Subject + page.Items[0].Date; got != firstSubject {
				t.Errorf("first visible mail %q is not the newest %q", firstSubject, got)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("n=%d: account created %v, first classic rows %v, first hybrid rows %v, inbox complete %v", n, created, firstClassic, firstHybrid, complete)

	// Open-mail latency (click -> content): first open (body from the store
	// or downloaded on demand) and reopen (render cache); list page query.
	page, err := a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "inbox"}, Offset: n / 2, Limit: 50})
	if err != nil || len(page.Items) == 0 {
		t.Fatal(err)
	}
	t0 := time.Now()
	if _, err := a.GetMessage(ctx, page.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	first := time.Since(t0)
	t0 = time.Now()
	if _, err := a.GetMessage(ctx, page.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	again := time.Since(t0)
	t0 = time.Now()
	for i := 0; i < 10; i++ {
		if _, err := a.ListMessages(ctx, app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "inbox"}, Limit: 50}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("open old mail (body on demand) %v, reopen %v, list page %v", first, again, time.Since(t0)/10)

	// Hybrid rows name the counterpart (the sender), never only "Du": all
	// generated mails come from other people.
	t0 = time.Now()
	convs, err := a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "unified", Role: "inbox"}, Grouping: "thread", Limit: 50})
	t.Logf("hybrid list after the sync (indexes all new mail) %v", time.Since(t0))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range convs.Items {
		if len(c.ParticipantIDs) == 0 {
			t.Errorf("conversation %q has no participants", c.Title)
			break
		}
	}
	if firstClassic > 10*time.Second || firstHybrid > 10*time.Second {
		t.Errorf("first rows too late: classic %v, hybrid %v", firstClassic, firstHybrid)
	}
	if !sawProgress && n >= 1000 {
		t.Error("no sync progress reported in the account status")
	}
}
