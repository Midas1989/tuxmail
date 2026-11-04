// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas/eastest"
	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
	"github.com/Midas1989/Mailclient/locales"
)

func easMIME(mid, subject, extra string) string {
	return "From: Bob <bob@example.com>\r\nTo: alice@example.com\r\nSubject: " + subject +
		"\r\nDate: " + time.Now().Add(-time.Hour).Format(time.RFC1123Z) + "\r\nMessage-ID: <" + mid + ">\r\n" + extra +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nHello " + subject + "\r\n"
}

func TestEASAccountThroughApp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv := eastest.New()
	defer srv.Close()
	srv.RequireProvision = true
	srv.MaxPingWait = 200 * time.Millisecond
	srv.AddMail(eastest.Inbox, easMIME("a1@x", "Budget", ""), false)
	srv.AddMail(eastest.Inbox, easMIME("a2@x", "Re: Budget", "In-Reply-To: <a1@x>\r\nReferences: <a1@x>\r\n"), false)
	srv.AddItem(eastest.Calendar, "Calendar", wbxml.S("Calendar:Subject", "Review"),
		wbxml.S("Calendar:StartTime", "20261110T090000Z"), wbxml.S("Calendar:EndTime", "20261110T100000Z"))

	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "tuxmail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, err := blob.New(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Locales: locales.FS,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC,
		Sync: &app.SyncOptions{Configure: func(c *tsync.Config) {
			c.PollInterval, c.FolderListInterval, c.Debounce = time.Minute, time.Minute, 20*time.Millisecond
			c.MinBackoff, c.MaxBackoff = 20*time.Millisecond, 200*time.Millisecond
		}},
		Send: &app.SendOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Start()

	acc, err := a.CreateAccount(ctx, app.NewAccountInput{Kind: "eas", Email: "alice@example.com", Name: "Alice",
		Password: "secret", EASURL: srv.EndpointURL(), Incoming: &app.ServerConfig{Username: "alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Kind != domain.KindEAS || acc.Extra["eas_url"] != srv.EndpointURL() || acc.Incoming.Host == "" || acc.Outgoing != nil {
		t.Fatalf("account %+v", acc)
	}
	if _, err := a.CreateAccount(ctx, app.NewAccountInput{Kind: "eas", Email: "x@example.com", EASURL: "ftp://x"}); err == nil {
		t.Fatal("invalid URL accepted")
	}

	var conv app.Conversation
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		page, err := a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "account", AccountID: acc.ID}, Limit: 50})
		if err == nil {
			for _, c := range page.Items {
				items, _ := a.ListConversationItems(ctx, c.ID, app.ItemsQuery{})
				if len(items) == 2 {
					conv = c
				}
			}
		}
		if conv.ID != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conv.ID == 0 {
		t.Fatal("EAS mails were not synced into one conversation")
	}

	// Sending goes through EAS SendMail.
	var identity int64
	ids, _ := a.ListIdentities(ctx)
	for _, i := range ids {
		if i.AccountID == acc.ID {
			identity = i.ID
		}
	}
	if _, err := a.Send(ctx, app.OutgoingMessage{Channel: "email", AccountID: acc.ID, IdentityID: identity, To: []app.Address{{Email: "bob@example.com"}},
		Cc: []app.Address{}, Bcc: []app.Address{}, Subject: "Hi", Text: "Hello"}); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) && len(srv.SentMails()) == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if len(srv.SentMails()) != 1 {
		t.Fatal("mail not sent through EAS")
	}

	// Calendars are synchronized by the app's timer.
	for time.Now().Before(deadline) {
		if cals, _ := st.ListAccountCalendars(ctx, acc.ID); len(cals) > 0 {
			if objs, _ := st.ListCalObjects(ctx, cals[0].ID); len(objs) > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	found := false
	cals, _ := st.ListAccountCalendars(ctx, acc.ID)
	for _, c := range cals {
		objs, _ := st.ListCalObjects(ctx, c.ID)
		for _, o := range objs {
			found = found || o.Summary == "Review"
		}
	}
	if !found {
		t.Fatal("EAS calendar not synchronized")
	}

	servers, err := a.EASVacationServers(ctx)
	if err != nil || servers[acc.ID] == nil || !servers[acc.ID].Supported() {
		t.Fatalf("vacation servers %v %v", servers, err)
	}
}
