// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/providers/eas"
	"github.com/Midas1989/Mailclient/internal/providers/eas/eastest"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

type easEnv struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	srv *eastest.Server
	mgr *Manager
	acc int64
}

func newEASEnv(t *testing.T, prepare func(*eastest.Server)) *easEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	t.Cleanup(cancel)
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
	srv := eastest.New()
	srv.MaxPingWait = 200 * time.Millisecond
	srv.RequireProvision = true
	t.Cleanup(srv.Close)
	if prepare != nil {
		prepare(srv)
	}
	sec := secrets.NewMemory()
	if err := sec.Set("eas-secret", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	acc, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindEAS, DisplayName: "Exchange",
		Incoming:  &domain.ServerConfig{Host: u.Hostname(), Port: port, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "alice"},
		SecretRef: "eas-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	e := &easEnv{t: t, ctx: ctx, st: st, srv: srv, acc: acc}
	reg := NewRegistry()
	RegisterEAS(reg, EASOptions{State: EASStoreState{Store: st}, Heartbeat: time.Minute,
		OnRemoteWipe: func(ctx context.Context, a domain.Account, _ bool) error { return WipeEASAccountData(ctx, st, a.ID) }})
	e.mgr, err = New(Config{
		Store: st, Blobs: blobs, Bus: events.New(), Secrets: sec, Providers: reg,
		ProviderOptions: ProviderOptions{WatchMinBackoff: 20 * time.Millisecond, WatchMaxBackoff: 200 * time.Millisecond},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval:    time.Minute, FolderListInterval: time.Minute, Debounce: 20 * time.Millisecond,
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
		OpBackoff:        func(int) time.Duration { return 0 },
		MaintenanceDelay: time.Hour, IngestBatch: 2, BackfillBatch: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.mgr.Stop)
	return e
}

func (e *easEnv) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("timeout waiting for %s", what)
}

func (e *easEnv) msg(mid string) (store.Message, bool) {
	m, err := e.st.FindMessageByMessageID(e.ctx, e.acc, mid)
	return m, err == nil
}

func easMail(mid, subject, extra string) string {
	return "From: Bob <bob@example.com>\r\nTo: alice@example.com\r\nSubject: " + subject +
		"\r\nDate: " + time.Now().Add(-time.Hour).Format(time.RFC1123Z) + "\r\nMessage-ID: <" + mid + ">\r\n" + extra +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nHello " + subject + "\r\n"
}

func TestEASAccountSync(t *testing.T) {
	var id1 string
	e := newEASEnv(t, func(s *eastest.Server) {
		id1 = s.AddMail(eastest.Inbox, easMail("t1@x", "Plan", ""), false)
		s.AddMail(eastest.Inbox, easMail("t2@x", "Re: Plan", "In-Reply-To: <t1@x>\r\nReferences: <t1@x>\r\n"), true)
		s.AddMail(eastest.Sent, easMail("t3@x", "Re: Plan", "In-Reply-To: <t2@x>\r\nReferences: <t1@x> <t2@x>\r\n"), true)
	})
	e.eventually("initial sync", func() bool {
		_, a := e.msg("t1@x")
		_, b := e.msg("t2@x")
		_, c := e.msg("t3@x")
		return a && b && c
	})
	m1, _ := e.msg("t1@x")
	m2, _ := e.msg("t2@x")
	m3, _ := e.msg("t3@x")
	if m1.ConversationID == 0 || m1.ConversationID != m2.ConversationID || m2.ConversationID != m3.ConversationID {
		t.Fatalf("conversation ids %d %d %d", m1.ConversationID, m2.ConversationID, m3.ConversationID)
	}
	if m2.Flags&store.FlagSeen == 0 || m1.Flags&store.FlagSeen != 0 {
		t.Fatalf("flags %v %v", m1.Flags, m2.Flags)
	}
	inbox, err := e.st.FolderByRemoteID(e.ctx, e.acc, eastest.Inbox)
	if err != nil || inbox.Role != domain.RoleInbox {
		t.Fatalf("inbox %+v %v", inbox, err)
	}
	// The body was downloaded (recent mail) via ItemOperations.
	e.eventually("body", func() bool {
		m, _ := e.msg("t1@x")
		return m.BlobHash != ""
	})

	// Push: a new mail on the server wakes the Ping watcher.
	e.srv.AddMail(eastest.Inbox, easMail("t4@x", "New", ""), false)
	e.eventually("pushed mail", func() bool { _, ok := e.msg("t4@x"); return ok })

	// Local flag change reaches the server.
	if err := e.mgr.Actions().MarkRead(e.ctx, []int64{m1.ID}, true); err != nil {
		t.Fatal(err)
	}
	e.eventually("read on server", func() bool { it := e.srv.Item(eastest.Inbox, id1); return it != nil && it.Read })

	// Server-side change by another client.
	e.srv.SetRead(eastest.Inbox, id1, false)
	e.mgr.SyncNow(e.acc)
	e.eventually("unread locally", func() bool { m, _ := e.msg("t1@x"); return m.Flags&store.FlagSeen == 0 })

	// Moving to the trash folder.
	trash, err := e.st.FolderByRemoteID(e.ctx, e.acc, eastest.Deleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.Actions().Move(e.ctx, []int64{m1.ID}, trash.ID); err != nil {
		t.Fatal(err)
	}
	e.eventually("moved on server", func() bool { return e.srv.Item(eastest.Inbox, id1) == nil && len(e.srv.Items(eastest.Deleted)) == 1 })

	// The account state survives: policy key and hierarchy are persisted.
	var raw string
	if ok, err := e.st.GetAppSetting(e.ctx, easStatePrefix+"alice|"+EASDeviceID(mustAccount(t, e)), &raw); err != nil || !ok {
		t.Fatalf("state not persisted: %v %v", ok, err)
	}
}

func mustAccount(t *testing.T, e *easEnv) domain.Account {
	a, err := e.st.GetAccount(e.ctx, e.acc)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEASRemoteWipe(t *testing.T) {
	e := newEASEnv(t, func(s *eastest.Server) { s.AddMail(eastest.Inbox, easMail("w1@x", "W", ""), false) })
	e.eventually("initial sync", func() bool { _, ok := e.msg("w1@x"); return ok })
	e.srv.RequestWipe(true)
	e.mgr.SyncNow(e.acc)
	e.eventually("wipe", func() bool { return e.srv.Wiped() })
	e.eventually("local data removed", func() bool {
		fs, _ := e.st.ListFolders(e.ctx, e.acc)
		a := mustAccount(t, e)
		return len(fs) == 0 && !a.Enabled
	})
}

func TestEASConfigHelpers(t *testing.T) {
	acc := domain.Account{ID: 7, Kind: domain.KindEAS, Incoming: &domain.ServerConfig{Host: "mail.example.com", Port: 443,
		TLS: domain.TLSImplicit, Username: "Alice@Example.com"}}
	u, err := EASURL(acc)
	if err != nil || u != "https://mail.example.com/Microsoft-Server-ActiveSync" {
		t.Fatalf("%s %v", u, err)
	}
	id := EASDeviceID(acc)
	if len(id) != 23 || id != EASDeviceID(acc) {
		t.Fatalf("device id %q", id)
	}
	acc.Extra = map[string]string{ExtraEASURL: "https://x/eas"}
	if u, _ := EASURL(acc); u != "https://x/eas" {
		t.Fatal(u)
	}
	if !DefaultRegistry().Supports(domain.KindEAS) {
		t.Fatal("eas not registered")
	}
	if !IsPermanent(easErr(eas.ErrNotSupported)) || IsPermanent(easErr(&eas.HTTPError{StatusCode: 503})) {
		t.Fatal("error mapping")
	}
}
