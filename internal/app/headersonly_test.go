// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/devserver"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

func oldMail(date time.Time, mid, subject, body string) []byte {
	return []byte("From: Ben Krause <ben@tuxmail.test>\r\nTo: Anna Berger <anna@tuxmail.test>\r\nSubject: " + subject +
		"\r\nDate: " + date.Format(time.RFC1123Z) + "\r\nMessage-ID: <" + mid + ">\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

func newDesktopFixture(t *testing.T, mode string, days int, seed func(*devserver.Server)) *liveFixture {
	t.Helper()
	ctx := context.Background()
	srv, err := devserver.Start(devserver.Config{Users: devserver.DefaultUsers()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	if err := srv.Seed(time.Now()); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(srv)
	}
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Mode: mode,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Sync: &app.SyncOptions{}, Send: &app.SendOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.Start()
	if days > 0 {
		if _, err := a.SetMailStorageSettings(ctx, app.MailStorageSettings{HeadersOnlyOlderThanDays: days}); err != nil {
			t.Fatal(err)
		}
	}
	server := func(addr string) *app.ServerConfig {
		h, p, _ := net.SplitHostPort(addr)
		port, _ := strconv.Atoi(p)
		return &app.ServerConfig{Host: h, Port: port, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: devserver.AnnaAddr}
	}
	acc, err := a.CreateAccount(ctx, app.NewAccountInput{Kind: "imap", Email: devserver.AnnaAddr, Name: "Anna Berger",
		Password: devserver.DefaultPassword, Incoming: server(srv.IMAPAddr()), Outgoing: server(srv.SMTPAddr())})
	if err != nil {
		t.Fatal(err)
	}
	f := &liveFixture{a: a, srv: srv, acc: acc}
	f.waitMessage(t, "Mittagessen morgen?")
	return f
}

func (f *liveFixture) waitPreview(t *testing.T, subject string) app.MessageSummary {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		m := f.waitMessage(t, subject)
		if m.Preview != "" {
			return m
		}
		if time.Now().After(deadline) {
			t.Fatalf("no preview for %q", subject)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func seenOnDevserver(t *testing.T, srv *devserver.Server, mid string) bool {
	t.Helper()
	msgs, err := srv.Messages(devserver.AnnaAddr, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.Contains(m.MessageID, mid) {
			return slices.Contains(m.Flags, `\Seen`)
		}
	}
	t.Fatalf("%s not on the server", mid)
	return false
}

// Older mails are kept with header and preview, opened with their content,
// found by header, preview and on the server; offline they say so.
func TestHeadersOnlyDesktop(t *testing.T) {
	old := time.Now().AddDate(0, 0, -200)
	long := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 80)
	f := newDesktopFixture(t, app.ModeDesktop, 30, func(s *devserver.Server) {
		for _, m := range []struct{ mid, subj, body string }{
			{"alt1@tuxmail.test", "Altes Angebot", "Vorschauwort zuerst. " + long + " Tiefenwort ganz unten."},
			{"alt2@tuxmail.test", "Alte Rechnung", "Rechnungsvorschau bitte prüfen. " + long + " Geheimnummer 4711."},
		} {
			if _, err := s.Append(devserver.AnnaAddr, "INBOX", oldMail(old, m.mid, m.subj, m.body), nil, old); err != nil {
				t.Fatal(err)
			}
		}
	})
	ctx := context.Background()
	st := must[app.MailStorageSettings](t)(f.a.GetMailStorageSettings(ctx))
	if st.HeadersOnlyOlderThanDays != 30 || !st.Available {
		t.Errorf("settings = %+v", st)
	}
	a1 := f.waitPreview(t, "Altes Angebot")
	a2 := f.waitPreview(t, "Alte Rechnung")
	fresh := f.waitMessage(t, "Mittagessen morgen?")
	if !a1.HeadersOnly || !a2.HeadersOnly || !strings.Contains(a1.Preview, "Vorschauwort") {
		t.Fatalf("older mails: %+v / %+v", a1, a2)
	}
	deadline := time.Now().Add(15 * time.Second)
	for fresh.HeadersOnly && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		fresh = f.waitMessage(t, "Mittagessen morgen?")
	}
	if fresh.HeadersOnly {
		t.Error("a mail inside the day count stays header-only")
	}

	// Search: by preview locally (marked), by the content on the server.
	res := must[app.SearchResult](t)(f.a.Search(ctx, app.SearchQuery{Text: "Rechnungsvorschau", Location: "auto", Sort: "relevance"}))
	if len(res.Hits) != 1 || res.Hits[0].MessageID != a2.ID || !res.Hits[0].HeadersOnly || res.Hits[0].ServerMatch {
		t.Errorf("preview search = %+v", res.Hits)
	}
	res = must[app.SearchResult](t)(f.a.Search(ctx, app.SearchQuery{Text: "Geheimnummer", Location: "auto", Sort: "relevance"}))
	if len(res.Hits) != 1 || res.Hits[0].MessageID != a2.ID || !res.Hits[0].ServerMatch || !res.Hits[0].HeadersOnly {
		t.Errorf("server search = %+v", res.Hits)
	}
	res = must[app.SearchResult](t)(f.a.Search(ctx, app.SearchQuery{Text: "Geheimnummer", Location: "local", Sort: "relevance"}))
	if len(res.Hits) != 0 {
		t.Errorf("local-only search reached the server: %+v", res.Hits)
	}

	// Opening loads the content, without marking it read on the server.
	d := must[app.MessageDetail](t)(f.a.GetMessage(ctx, a1.ID))
	if d.HeadersOnly || d.ContentUnavailable || !strings.Contains(d.Text, "Tiefenwort") {
		t.Errorf("opened: headersOnly=%v unavailable=%v text=%.60q", d.HeadersOnly, d.ContentUnavailable, d.Text)
	}
	if seenOnDevserver(t, f.srv, "alt1@tuxmail.test") {
		t.Error("loading the content set \\Seen")
	}

	// Without the server: header and preview, "content only online".
	f.srv.Close()
	d = must[app.MessageDetail](t)(f.a.GetMessage(ctx, a2.ID))
	if !d.ContentUnavailable || !d.HeadersOnly || !strings.Contains(d.Text, "Rechnungsvorschau") || d.Subject != "Alte Rechnung" {
		t.Errorf("offline: %+v", d)
	}

	// The account's own day count.
	s := must[app.AccountSettings](t)(f.a.GetAccountSettings(ctx, f.acc.ID))
	if !s.HeadersOnlyAvailable || s.HeadersOnlyOlderThanDays != -1 || s.BodyPolicy != "recent" {
		t.Errorf("account settings = %+v", s)
	}
	seven := 7
	s = must[app.AccountSettings](t)(f.a.UpdateAccount(ctx, f.acc.ID, app.AccountUpdate{HeadersOnlyOlderThanDays: &seven}))
	if s.HeadersOnlyOlderThanDays != 7 {
		t.Errorf("after update = %d", s.HeadersOnlyOlderThanDays)
	}
	bad := -5
	if _, err := f.a.UpdateAccount(ctx, f.acc.ID, app.AccountUpdate{HeadersOnlyOlderThanDays: &bad}); err == nil {
		t.Error("invalid day count accepted")
	}
	reset := -1
	s = must[app.AccountSettings](t)(f.a.UpdateAccount(ctx, f.acc.ID, app.AccountUpdate{HeadersOnlyOlderThanDays: &reset}))
	if s.HeadersOnlyOlderThanDays != -1 {
		t.Errorf("after reset = %d", s.HeadersOnlyOlderThanDays)
	}
	if _, err := f.a.SetMailStorageSettings(ctx, app.MailStorageSettings{HeadersOnlyOlderThanDays: -2}); err == nil {
		t.Error("invalid global day count accepted")
	}
	if st := must[app.MailStorageSettings](t)(f.a.SetMailStorageSettings(ctx, app.MailStorageSettings{HeadersOnlyOlderThanDays: -1})); st.HeadersOnlyOlderThanDays != -1 {
		t.Errorf("reset to the default = %+v", st)
	}
}

// The server keeps every mail completely: the day count does not exist there.
func TestHeadersOnlyNotOnServer(t *testing.T) {
	f := newDesktopFixture(t, app.ModeServer, 0, nil)
	ctx := context.Background()
	if st := must[app.MailStorageSettings](t)(f.a.GetMailStorageSettings(ctx)); st.HeadersOnlyOlderThanDays != -1 || st.Available {
		t.Errorf("server settings = %+v", st)
	}
	_, err := f.a.SetMailStorageSettings(ctx, app.MailStorageSettings{HeadersOnlyOlderThanDays: 30})
	var ae *app.Error
	if !errors.As(err, &ae) || ae.Status != 403 {
		t.Errorf("server accepted the day count: %v", err)
	}
	s := must[app.AccountSettings](t)(f.a.GetAccountSettings(ctx, f.acc.ID))
	if s.HeadersOnlyAvailable {
		t.Error("offered on the server")
	}
	n := 30
	if _, err := f.a.UpdateAccount(ctx, f.acc.ID, app.AccountUpdate{HeadersOnlyOlderThanDays: &n}); err == nil {
		t.Error("account day count accepted on the server")
	}
}

// Before switching to "all" or a larger day count the interface asks with an
// estimate; the download can be paused.
func TestHeadersOnlyEstimateAndPause(t *testing.T) {
	old := time.Now().AddDate(0, 0, -200)
	f := newDesktopFixture(t, app.ModeDesktop, 30, func(s *devserver.Server) {
		for i, subj := range []string{"Alt eins", "Alt zwei"} {
			if _, err := s.Append(devserver.AnnaAddr, "INBOX", oldMail(old, "est"+strconv.Itoa(i)+"@tuxmail.test", subj, "Inhalt "+subj), nil, old); err != nil {
				t.Fatal(err)
			}
		}
	})
	ctx := context.Background()
	f.waitMessage(t, "Alt zwei")
	// The estimate counts what is not downloaded yet, so the background
	// download must not shrink it between the calls below: pause it and wait
	// until a download that was already running has finished.
	must[app.MailStorageSettings](t)(f.a.SetMailStorageSettings(ctx, app.MailStorageSettings{HeadersOnlyOlderThanDays: 30, DownloadPaused: true}))
	for last, stable := -1, 0; stable < 5; {
		time.Sleep(100 * time.Millisecond)
		p := must[app.MailStorageEstimate](t)(f.a.EstimateMailStorage(ctx, 0)).Messages
		if p == last {
			stable++
		} else {
			last, stable = p, 0
		}
	}
	est := must[app.MailStorageEstimate](t)(f.a.EstimateMailStorage(ctx, 0))
	if est.Messages < 2 || est.Bytes <= 0 {
		t.Errorf("estimate for all = %+v", est)
	}
	if e30 := must[app.MailStorageEstimate](t)(f.a.EstimateMailStorage(ctx, 30)); e30.Messages >= est.Messages {
		t.Errorf("estimate for 30 days %+v not smaller than for all %+v", e30, est)
	}
	// The accounts' own day counts count: "all" on the account, 30 days globally.
	zero := 0
	must[app.AccountSettings](t)(f.a.UpdateAccount(ctx, f.acc.ID, app.AccountUpdate{HeadersOnlyOlderThanDays: &zero}))
	if own := must[app.MailStorageEstimate](t)(f.a.EstimateMailStorage(ctx, 30)); own.Messages != est.Messages {
		t.Errorf("estimate with the account's own count %+v, want %+v", own, est)
	}
	reset := -1
	must[app.AccountSettings](t)(f.a.UpdateAccount(ctx, f.acc.ID, app.AccountUpdate{HeadersOnlyOlderThanDays: &reset}))
	if _, err := f.a.EstimateMailStorage(ctx, -5); err == nil {
		t.Error("invalid estimate accepted")
	}
	st := must[app.MailStorageSettings](t)(f.a.SetMailStorageSettings(ctx, app.MailStorageSettings{HeadersOnlyOlderThanDays: 0, DownloadPaused: true}))
	if !st.DownloadPaused || st.Pending < 2 {
		t.Errorf("paused = %+v", st)
	}
	time.Sleep(500 * time.Millisecond)
	if st = must[app.MailStorageSettings](t)(f.a.GetMailStorageSettings(ctx)); st.Pending < 2 {
		t.Errorf("downloaded while paused: %+v", st)
	}
	st = must[app.MailStorageSettings](t)(f.a.SetMailStorageSettings(ctx, app.MailStorageSettings{HeadersOnlyOlderThanDays: 0}))
	deadline := time.Now().Add(15 * time.Second)
	for st.Pending > 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		st = must[app.MailStorageSettings](t)(f.a.GetMailStorageSettings(ctx))
	}
	if st.Pending != 0 || st.DownloadPaused {
		t.Errorf("after resuming: %+v", st)
	}
}
