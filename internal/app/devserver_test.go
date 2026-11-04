// SPDX-License-Identifier: AGPL-3.0-or-later

package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
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

// liveFixture is an App with sync and send against the in-memory dev
// server (real IMAP/SMTP), with one account for devserver.AnnaAddr.
type liveFixture struct {
	a   *app.App
	srv *devserver.Server
	acc app.Account
}

func newLiveFixture(t *testing.T) *liveFixture {
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
	a, err := app.New(ctx, app.Options{Store: st, Blobs: blobs, DataDir: dir, Mode: app.ModeServer,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Sync: &app.SyncOptions{}, Send: &app.SendOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.Start()
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

func (f *liveFixture) waitMessage(t *testing.T, subject string) app.MessageSummary {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		page, err := f.a.ListMessages(context.Background(), app.MessageQuery{Scope: app.MessageScope{Kind: "unified", Role: "inbox"}, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Items {
			if m.Subject == subject {
				return m
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("message %q not synced", subject)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Send checks must stop interactive sends with warnings (KONZEPT 8.13)
// until the user confirms them.
func TestLiveSendChecksNeedConfirmation(t *testing.T) {
	f := newLiveFixture(t)
	ctx := context.Background()
	orig := f.waitMessage(t, "Mittagessen morgen?")
	ids := must[[]app.Identity](t)(f.a.ListIdentities(ctx))
	msg := app.OutgoingMessage{Channel: "email", IdentityID: ids[0].ID, To: []app.Address{{Email: devserver.BenAddr}},
		Subject: "Re: Mittagessen morgen?", Text: "Gerne, anbei der Tischplan.", InReplyToMessageID: orig.ID}
	res := must[app.SendResult](t)(f.a.Send(ctx, msg))
	if !res.Blocked || len(res.Findings) == 0 || res.Findings[0].Check != "forgotten_attachment" || res.Findings[0].Localized["de"] == "" {
		t.Fatalf("expected a forgotten-attachment block, got %+v", res)
	}
	if got := len(f.srv.Deliveries()); got != 0 {
		t.Fatalf("%d mails left before confirmation", got)
	}
	msg.ConfirmChecks = []string{"forgotten_attachment"}
	res = must[app.SendResult](t)(f.a.Send(ctx, msg))
	if res.Blocked {
		t.Fatalf("still blocked after confirmation: %+v", res)
	}
	d := f.srv.Deliveries()
	if len(d) != 1 || len(d[0].Local) != 1 || d[0].Local[0] != devserver.BenAddr {
		t.Fatalf("deliveries %+v", d)
	}
	got, err := f.srv.Messages(devserver.BenAddr, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	last := got[len(got)-1]
	if last.InReplyTo != "<"+devserver.SeedLunchID+">" || !strings.Contains(last.References, devserver.SeedLunchID) {
		t.Errorf("threading headers: %+v", last)
	}
}

// A new mail must announce itself to the person-view conversation of its
// sender (chat view grouped by person), not only to its thread.
func TestLiveNewMailUpdatesPersonView(t *testing.T) {
	f := newLiveFixture(t)
	ctx := context.Background()
	convs := must[app.Page[app.Conversation]](t)(f.a.ListConversations(ctx, app.ConversationQuery{Scope: app.ConversationScope{Kind: "all"}, Grouping: "person", Limit: 50}))
	var maria int64
	for _, c := range convs.Items {
		if c.Title == "Maria Sommer" {
			maria = c.ID
		}
	}
	if maria == 0 {
		t.Fatalf("no person conversation for Maria: %+v", convs.Items)
	}
	events, cancel := f.a.Events().Subscribe(ctx, 256)
	defer cancel()
	if _, err := f.srv.Send(devserver.SendRequest{From: "Maria Sommer <maria.sommer@example.org>", To: []string{devserver.AnnaAddr},
		Subject: "Re: Angebot Website-Relaunch", Text: "Die Auftragsbestätigung kommt morgen.", InReplyTo: "<" + devserver.SeedMariaReplyID + ">"}); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(15 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == app.EventConversationUpdated && ev.ConversationID == maria {
				return
			}
		case <-timeout:
			t.Fatal("no conversation.updated for the person view")
		}
	}
}

func TestSaveSignature(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sig := must[app.Signature](t)(f.a.SaveSignature(ctx, app.Signature{Name: "Kurz", Plain: "Anna <Berger>\nTel. 123"}))
	if sig.ID == 0 || sig.HTML != "Anna &lt;Berger&gt;<br>Tel. 123" {
		t.Fatalf("saved %+v", sig)
	}
	sig.Name = "Kurz 2"
	if s := must[app.Signature](t)(f.a.SaveSignature(ctx, sig)); s.Name != "Kurz 2" {
		t.Errorf("update %+v", s)
	}
	if _, err := f.a.SaveSignature(ctx, app.Signature{Name: " "}); code(err) != app.CodeInvalidInput {
		t.Errorf("empty name: %v", err)
	}
	if err := f.a.DeleteSignature(ctx, sig.ID); err != nil {
		t.Fatal(err)
	}
}
