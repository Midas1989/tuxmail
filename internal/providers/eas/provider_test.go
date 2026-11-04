// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas/eastest"
	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

func newFake(t *testing.T) *eastest.Server {
	t.Helper()
	s := eastest.New()
	t.Cleanup(s.Close)
	return s
}

func newProvider(t *testing.T, s *eastest.Server, mutate func(*Config)) *Provider {
	t.Helper()
	cfg := Config{
		Client: ClientConfig{URL: s.EndpointURL(), Username: "alice", Password: "secret", DeviceID: "TEST" + strings.ReplaceAll(t.Name(), "/", ""),
			State: &MemoryState{}},
		WindowSize: 2, Heartbeat: 30 * time.Second, MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testMIME(id, subject, extra string) string {
	return "From: Bob <bob@example.com>\r\nTo: alice@example.com\r\nSubject: " + subject +
		"\r\nDate: Mon, 02 Jan 2026 10:00:00 +0000\r\nMessage-ID: <" + id + ">\r\n" + extra +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nHello " + subject + "\r\n"
}

func folderByRole(t *testing.T, fs []domain.Folder, r domain.FolderRole) domain.Folder {
	t.Helper()
	for _, f := range fs {
		if f.Role == r {
			return f
		}
	}
	t.Fatalf("no folder with role %q in %+v", r, fs)
	return domain.Folder{}
}

func TestNegotiateVersions(t *testing.T) {
	for _, c := range []struct{ server, max, want string }{
		{"2.5,12.0,12.1,14.0,14.1,16.0,16.1", "", "16.1"},
		{"2.5,12.0,12.1,14.0,14.1", "", "14.1"},
		{"2.5,12.0,12.1,14.0,14.1,16.0,16.1", "14.1", "14.1"},
		{"2.5,12.1", "", "12.1"},
		{"", "", DefaultVersion},
	} {
		if got := pickVersion(splitList(c.server), c.max); got != c.want {
			t.Errorf("pickVersion(%q, %q) = %s, want %s", c.server, c.max, got, c.want)
		}
	}
	s := newFake(t)
	s.Versions = "12.1,14.0,14.1"
	p := newProvider(t, s, nil)
	v, err := p.Client().Version(testCtx(t))
	if err != nil || v != "14.1" {
		t.Fatalf("version %q %v", v, err)
	}
}

func TestAuthFailure(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, func(c *Config) { c.Client.Password = "wrong" })
	if _, err := p.ListFolders(testCtx(t)); !errors.Is(err, ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

func TestOAuthBearer(t *testing.T) {
	s := newFake(t)
	s.Tokens["tok-1"] = true
	p := newProvider(t, s, func(c *Config) {
		c.Client.Password = ""
		c.Client.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "tok-1", nil })
	})
	if _, err := p.ListFolders(testCtx(t)); err != nil {
		t.Fatal(err)
	}
}

func TestProvisioning(t *testing.T) {
	for _, version := range []string{"14.1", "12.1", "16.1"} {
		t.Run(version, func(t *testing.T) {
			s := newFake(t)
			s.RequireProvision = true
			s.PolicySettings["MinDevicePasswordLength"] = "4"
			var got Policy
			p := newProvider(t, s, func(c *Config) {
				c.Client.ProtocolVersion = version
				c.Client.OnPolicy = func(pol Policy) { got = pol }
			})
			ctx := testCtx(t)
			fs, err := p.ListFolders(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(fs) == 0 || got.Key == "" || got.Key != s.PolicyKey() || got.Settings["MinDevicePasswordLength"] != "4" {
				t.Fatalf("policy %+v (server key %s)", got, s.PolicyKey())
			}
			// Policy refresh: the server invalidates the key, the client re-provisions transparently.
			s.InvalidatePolicy()
			if _, err := p.ListFolders(ctx); err != nil {
				t.Fatal(err)
			}
			if p.Client().CurrentPolicy().Key != s.PolicyKey() {
				t.Fatal("not re-provisioned")
			}
			if version == "14.1" && s.DeviceInformation()["Model"] == "" {
				t.Error("device information not sent during provisioning")
			}
		})
	}
}

func TestRemoteWipe(t *testing.T) {
	s := newFake(t)
	s.RequireProvision = true
	var wiped []bool
	p := newProvider(t, s, func(c *Config) {
		c.Client.OnRemoteWipe = func(_ context.Context, accountOnly bool) error {
			wiped = append(wiped, accountOnly)
			return nil
		}
	})
	ctx := testCtx(t)
	if _, err := p.ListFolders(ctx); err != nil {
		t.Fatal(err)
	}
	s.RequestWipe(true)
	_, err := p.ListFolders(ctx)
	if !errors.Is(err, ErrRemoteWiped) {
		t.Fatalf("expected ErrRemoteWiped, got %v", err)
	}
	if len(wiped) != 1 || !wiped[0] || !s.Wiped() {
		t.Fatalf("wipe handler %v, acknowledged %v", wiped, s.Wiped())
	}
}

func TestRemoteWipeWithoutHandlerIsNotAcknowledged(t *testing.T) {
	s := newFake(t)
	s.RequestWipe(false)
	p := newProvider(t, s, nil)
	if _, err := p.ListFolders(testCtx(t)); !errors.Is(err, ErrRemoteWiped) {
		t.Fatalf("got %v", err)
	}
	if s.Wiped() {
		t.Fatal("wipe acknowledged without local wipe")
	}
}

func TestRedirectAndRetryAfter(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, nil)
	ctx := testCtx(t)
	other := newFake(t)
	s.RedirectOnce(other.EndpointURL())
	if _, err := p.ListFolders(ctx); err != nil {
		t.Fatal(err)
	}
	if p.Client().URL() != other.EndpointURL() {
		t.Fatalf("URL after 451: %s", p.Client().URL())
	}
	other.Unavailable(2)
	if _, err := p.Client().FolderSync(ctx); err != nil {
		t.Fatalf("503 with Retry-After 0 must be retried: %v", err)
	}
	other.Unavailable(10)
	var he *HTTPError
	if _, err := p.Client().FolderSync(ctx); !errors.As(err, &he) || !he.Temporary() {
		t.Fatalf("expected temporary HTTPError, got %v", err)
	}
}

func TestBase64Query(t *testing.T) {
	q := encodeBase64Query("Sync", "14.1", "123", "alice", "DEV1", "TuxMail", [][2]string{{"SaveInSent", "T"}, {"ItemId", "5"}})
	if q == "" {
		t.Fatal("empty")
	}
	s := newFake(t)
	s.RequireProvision = true
	p := newProvider(t, s, func(c *Config) { c.Client.Base64Query = true })
	ctx := testCtx(t)
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inbox := folderByRole(t, fs, domain.RoleInbox)
	s.AddMail(eastest.Inbox, testMIME("b64@x", "b64", ""), false)
	d, _, err := p.Sync(ctx, inbox.RemoteID, "")
	if err != nil || len(d.Added) != 1 {
		t.Fatalf("sync %v %+v", err, d)
	}
}

func TestFolders(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, nil)
	ctx := testCtx(t)
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for r, name := range map[domain.FolderRole]string{domain.RoleInbox: "Inbox", domain.RoleSent: "Sent Items",
		domain.RoleDrafts: "Drafts", domain.RoleTrash: "Deleted Items", domain.RoleJunk: "Junk E-Mail"} {
		if f := folderByRole(t, fs, r); f.Name != name || f.Delimiter != "/" {
			t.Errorf("role %s: %+v", r, f)
		}
	}
	for _, f := range fs {
		if f.Name == "Calendar" || f.Name == "Contacts" || f.Name == "Outbox" {
			t.Errorf("non-mail folder listed: %+v", f)
		}
	}
	inbox := folderByRole(t, fs, domain.RoleInbox)
	sub, err := p.CreateFolder(ctx, inbox.RemoteID, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sub.RemoteID, inbox.RemoteID+"/") || CollectionID(sub.RemoteID) == "" {
		t.Fatalf("sub folder %+v", sub)
	}
	// Server-side change arrives incrementally.
	s.AddFolder("0", "Archive", 12)
	s.ResetCommands()
	fs, err = p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if folderByRole(t, fs, domain.RoleArchive).Name != "Archive" {
		t.Fatal("archive missing")
	}
	found := false
	for _, f := range fs {
		found = found || f.RemoteID == sub.RemoteID
	}
	if !found {
		t.Fatal("created folder lost after incremental FolderSync")
	}
	ren, err := p.RenameFolder(ctx, sub.RemoteID, "Projekte")
	if err != nil || ren.RemoteID != sub.RemoteID || ren.Name != "Projekte" {
		t.Fatalf("rename %+v %v", ren, err)
	}
	if s.FolderByName("Projekte") == "" {
		t.Fatal("rename not on server")
	}
	if _, err := p.RenameFolder(ctx, inbox.RemoteID, "x"); err == nil {
		t.Fatal("renaming the inbox must fail")
	}
	if err := p.DeleteFolder(ctx, sub.RemoteID); err != nil {
		t.Fatal(err)
	}
	if s.FolderByName("Projekte") != "" {
		t.Fatal("folder not deleted")
	}
	// A rebuilt provider with the same state store keeps the hierarchy key.
	st := p.cfg.Client.State
	p2 := newProvider(t, s, func(c *Config) { c.Client.State = st; c.Client.DeviceID = p.cfg.Client.DeviceID })
	s.ResetCommands()
	if _, err := p2.ListFolders(ctx); err != nil {
		t.Fatal(err)
	}
	if cmds := s.Commands(); len(cmds) != 1 || cmds[0] != "FolderSync" {
		t.Fatalf("commands %v", cmds)
	}
}

func TestMailSync(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, nil)
	ctx := testCtx(t)
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inbox := folderByRole(t, fs, domain.RoleInbox)
	id1 := s.AddMail(eastest.Inbox, testMIME("m1@x", "Hello", ""), false)
	id2 := s.AddMail(eastest.Inbox, testMIME("m2@x", "Re: Hello", "In-Reply-To: <m1@x>\r\nReferences: <m1@x>\r\n"), true)
	s.AddMail(eastest.Inbox, testMIME("m3@x", "Third", ""), false)

	d, st, err := p.Sync(ctx, inbox.RemoteID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 3 || d.FullResync {
		t.Fatalf("initial delta %+v", d)
	}
	var m2 domain.RemoteMessage
	for _, m := range d.Added {
		if m.Ref.ID == id2 {
			m2 = m
		}
	}
	if m2.Header.MessageID != "m2@x" || len(m2.Header.InReplyTo) != 1 || m2.Header.References[0] != "m1@x" ||
		m2.Header.Subject != "Re: Hello" || m2.Header.From[0].Email != "bob@example.com" || !hasFlag(m2.Flags, domain.FlagSeen) ||
		m2.Header.ServerThreadID == "" || m2.Size == 0 || m2.Ref.FolderRemoteID != inbox.RemoteID {
		t.Fatalf("message %+v", m2)
	}
	// No changes: empty delta, sync key still valid.
	d, st2, err := p.Sync(ctx, inbox.RemoteID, st)
	if err != nil || len(d.Added)+len(d.FlagsChanged)+len(d.Removed) != 0 {
		t.Fatalf("expected empty delta: %+v %v", d, err)
	}
	// Changes by another client.
	s.SetRead(eastest.Inbox, id1, true)
	s.SetCategories(eastest.Inbox, id2, "Work")
	s.DeleteItem(eastest.Inbox, id2)
	s.SetCategories(eastest.Inbox, id1, "Important")
	d, st3, err := p.Sync(ctx, inbox.RemoteID, st2)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.FlagsChanged) != 1 || d.FlagsChanged[0].Ref.ID != id1 || !hasFlag(d.FlagsChanged[0].Flags, domain.FlagSeen) ||
		!hasFlag(d.FlagsChanged[0].Flags, "Important") {
		t.Fatalf("flags %+v", d.FlagsChanged)
	}
	if len(d.Removed) != 1 || d.Removed[0].ID != id2 {
		t.Fatalf("removed %+v", d.Removed)
	}
	// An unknown state yields a full resync.
	d, _, err = p.Sync(ctx, inbox.RemoteID, "garbage")
	if err != nil || !d.FullResync || len(d.Added) != 2 {
		t.Fatalf("garbage state: %+v %v", d, err)
	}
	_ = st3
}

func TestInvalidSyncKeyFullResync(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, nil)
	ctx := testCtx(t)
	fs, _ := p.ListFolders(ctx)
	inbox := folderByRole(t, fs, domain.RoleInbox)
	s.AddMail(eastest.Inbox, testMIME("a@x", "A", ""), false)
	st := (&collState{Key: "999", Items: map[string]itemFlags{}}).encode()
	d, _, err := p.Sync(ctx, inbox.RemoteID, st)
	if err != nil || !d.FullResync || len(d.Added) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestSetFlagsMoveDeleteFetch(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, nil)
	ctx := testCtx(t)
	fs, _ := p.ListFolders(ctx)
	inbox := folderByRole(t, fs, domain.RoleInbox)
	trash := folderByRole(t, fs, domain.RoleTrash)
	id1 := s.AddMail(eastest.Inbox, testMIME("f1@x", "One", ""), false)
	id2 := s.AddMail(eastest.Inbox, testMIME("f2@x", "Two", ""), false)
	_, st, err := p.Sync(ctx, inbox.RemoteID, "")
	if err != nil {
		t.Fatal(err)
	}
	ref1 := domain.RemoteRef{FolderRemoteID: inbox.RemoteID, ID: id1}
	ref2 := domain.RemoteRef{FolderRemoteID: inbox.RemoteID, ID: id2}
	if err := p.SetFlags(ctx, []domain.RemoteRef{ref1}, []domain.Flag{domain.FlagSeen, domain.FlagFlagged, "Work", domain.FlagAnswered}, nil); err != nil {
		t.Fatal(err)
	}
	it := s.Item(eastest.Inbox, id1)
	if !it.Read || !it.Flagged || len(it.Categories) != 1 || it.Categories[0] != "Work" {
		t.Fatalf("server item %+v", it)
	}
	if err := p.SetFlags(ctx, []domain.RemoteRef{ref1}, []domain.Flag{"Later"}, []domain.Flag{domain.FlagFlagged, "Work"}); err != nil {
		t.Fatal(err)
	}
	if it = s.Item(eastest.Inbox, id1); it.Flagged || len(it.Categories) != 1 || it.Categories[0] != "Later" {
		t.Fatalf("server item %+v", it)
	}
	// Own changes are not echoed, and the engine's (older) state still works.
	d, st, err := p.Sync(ctx, inbox.RemoteID, st)
	if err != nil || len(d.FlagsChanged)+len(d.Added) != 0 {
		t.Fatalf("echo %+v %v", d, err)
	}
	raw, err := p.FetchRaw(ctx, ref2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(raw)
	if !strings.Contains(string(b), "Hello Two") {
		t.Fatalf("raw %q", b)
	}
	if _, err := p.FetchRaw(ctx, domain.RemoteRef{FolderRemoteID: inbox.RemoteID, ID: "nope"}); !errors.Is(err, ErrItemNotFound) {
		t.Fatalf("missing item: %v", err)
	}
	moved, err := p.Move(ctx, []domain.RemoteRef{ref2}, trash.RemoteID)
	if err != nil || len(moved) != 1 || moved[0].ID == "" || moved[0].FolderRemoteID != trash.RemoteID {
		t.Fatalf("move %+v %v", moved, err)
	}
	if s.Item(eastest.Deleted, moved[0].ID) == nil {
		t.Fatal("not moved on server")
	}
	if err := p.Delete(ctx, []domain.RemoteRef{ref1}); err != nil {
		t.Fatal(err)
	}
	if s.Item(eastest.Inbox, id1) != nil || s.Item(eastest.Deleted, id1) != nil {
		t.Fatal("delete must be permanent")
	}
	d, _, err = p.Sync(ctx, inbox.RemoteID, st)
	if err != nil || len(d.Removed)+len(d.Added) != 0 {
		t.Fatalf("after delete %+v %v", d, err)
	}
	// Deleting an already deleted item is not an error.
	if err := p.Delete(ctx, []domain.RemoteRef{ref1}); err != nil {
		t.Fatal(err)
	}
}

func TestChangeBeforeSyncUsesPersistedKey(t *testing.T) {
	s := newFake(t)
	state := &MemoryState{}
	mk := func() *Provider {
		return newProvider(t, s, func(c *Config) { c.Client.State = state; c.Client.DeviceID = "PERSIST1" })
	}
	ctx := testCtx(t)
	p := mk()
	fs, _ := p.ListFolders(ctx)
	inbox := folderByRole(t, fs, domain.RoleInbox)
	id := s.AddMail(eastest.Inbox, testMIME("p@x", "P", ""), false)
	_, st, err := p.Sync(ctx, inbox.RemoteID, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	// Restart: an operation runs before the first folder sync.
	p = mk()
	if err := p.SetFlags(ctx, []domain.RemoteRef{{FolderRemoteID: inbox.RemoteID, ID: id}}, []domain.Flag{domain.FlagSeen}, nil); err != nil {
		t.Fatal(err)
	}
	if !s.Item(eastest.Inbox, id).Read {
		t.Fatal("flag not set")
	}
	d, _, err := p.Sync(ctx, inbox.RemoteID, st)
	if err != nil || d.FullResync || len(d.Added) != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestSendTransport(t *testing.T) {
	for _, version := range []string{"14.1", "12.1"} {
		t.Run(version, func(t *testing.T) {
			s := newFake(t)
			p := newProvider(t, s, func(c *Config) { c.Client.ProtocolVersion = version })
			tr := NewTransport(p.Client())
			raw := []byte("From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Hi\r\n\r\nBody\r\n")
			err := tr.Send(testCtx(t), domain.OutgoingMessage{From: domain.Address{Email: "alice@example.com"},
				Recipients: []string{"bob@example.com", "secret@example.com"}, Raw: raw})
			if err != nil {
				t.Fatal(err)
			}
			sent := s.SentMails()
			if len(sent) != 1 || !sent[0].SaveInSent || !strings.HasPrefix(string(sent[0].MIME), "Bcc: secret@example.com\r\n") {
				t.Fatalf("sent %+v", sent)
			}
			if len(s.Items(eastest.Sent)) != 1 {
				t.Fatal("not saved in Sent Items")
			}
			if err := tr.Reply(testCtx(t), domain.OutgoingMessage{Raw: raw}, domain.RemoteRef{FolderRemoteID: "1", ID: "42"}); err != nil {
				t.Fatal(err)
			}
			if sent = s.SentMails(); sent[1].Cmd != "SmartReply" || sent[1].Source != "42" {
				t.Fatalf("smart reply %+v", sent[1])
			}
		})
	}
}

func TestWatchPing(t *testing.T) {
	s := newFake(t)
	s.MaxPingWait = 300 * time.Millisecond
	s.MinHeartbeat = 60
	p := newProvider(t, s, nil)
	ctx := testCtx(t)
	fs, _ := p.ListFolders(ctx)
	inbox := folderByRole(t, fs, domain.RoleInbox)
	if _, _, err := p.Sync(ctx, inbox.RemoteID, ""); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(ctx)
	ch, err := p.Watch(wctx, inbox.RemoteID)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond) // heartbeat negotiation + an expired ping
	s.AddMail(eastest.Inbox, testMIME("w@x", "W", ""), false)
	select {
	case h := <-ch:
		if h.FolderRemoteID != inbox.RemoteID {
			t.Fatalf("hint %+v", h)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change hint")
	}
	if p.hub.heartbeat != 60*time.Second {
		t.Errorf("heartbeat not negotiated: %s", p.hub.heartbeat)
	}
	cancel()
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel not closed")
	}
}

func TestAppendDraft(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, func(c *Config) { c.Client.ProtocolVersion = "16.1" })
	ctx := testCtx(t)
	fs, _ := p.ListFolders(ctx)
	drafts := folderByRole(t, fs, domain.RoleDrafts)
	raw := "From: alice@example.com\r\nTo: Bob <bob@example.com>\r\nBcc: c@example.com\r\nSubject: Draft\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=X\r\n\r\n--X\r\nContent-Type: text/plain\r\n\r\nText\r\n" +
		"--X\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n\r\nPDF\r\n--X--\r\n"
	ref, err := p.Append(ctx, drafts.RemoteID, strings.NewReader(raw), nil)
	if err != nil || ref.ID == "" {
		t.Fatalf("append %+v %v", ref, err)
	}
	it := s.Item(eastest.Drafts, ref.ID)
	if it == nil || it.Data.TextOf("Email:Subject") != "Draft" || it.Data.TextOf("Email2:Bcc") == "" ||
		it.Data.Path("AirSyncBase:Attachments/AirSyncBase:Add").TextOf("AirSyncBase:DisplayName") != "a.pdf" {
		t.Fatalf("draft %+v", it)
	}
	inbox := folderByRole(t, fs, domain.RoleInbox)
	if _, err := p.Append(ctx, inbox.RemoteID, strings.NewReader(raw), nil); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("append to inbox: %v", err)
	}
	p14 := newProvider(t, s, func(c *Config) { c.Client.ProtocolVersion = "14.1"; c.Client.DeviceID = "OTHER" })
	if _, err := p14.Append(ctx, drafts.RemoteID, strings.NewReader(raw), nil); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("append with 14.1: %v", err)
	}
}

func TestMiscCommands(t *testing.T) {
	s := newFake(t)
	s.SetGAL(eastest.GALEntry{Name: "Carol Example", Email: "carol@example.com"})
	p := newProvider(t, s, nil)
	c := p.Client()
	ctx := testCtx(t)
	fs, _ := p.ListFolders(ctx)
	inbox := folderByRole(t, fs, domain.RoleInbox)
	s.AddMail(eastest.Inbox, testMIME("s@x", "Quarterly report", ""), false)
	s.AddMail(eastest.Inbox, testMIME("t@x", "Lunch", ""), false)

	res, err := c.SearchMailbox(ctx, "quarterly", "", 10)
	if err != nil || len(res) != 1 || res[0].Properties.TextOf("Email:Subject") != "Quarterly report" {
		t.Fatalf("search %+v %v", res, err)
	}
	gal, err := c.SearchGAL(ctx, "carol", 10)
	if err != nil || len(gal) != 1 || gal[0].EmailAddress != "carol@example.com" {
		t.Fatalf("gal %+v %v", gal, err)
	}
	rr, err := c.ResolveRecipients(ctx, "carol")
	if err != nil || len(rr) != 1 || rr[0].EmailAddress != "carol@example.com" {
		t.Fatalf("resolve %+v %v", rr, err)
	}
	calID, err := c.MeetingResponse(ctx, MeetingAccept, eastest.Inbox, "77", "")
	if err != nil || calID != "cal-77" || s.MeetingResponses()[0] != "77:1" {
		t.Fatalf("meeting %q %v", calID, err)
	}
	// GetItemEstimate after the initial key.
	r, err := c.SyncCollection(ctx, CollectionID(inbox.RemoteID), "0", SyncOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := c.GetItemEstimate(ctx, CollectionID(inbox.RemoteID), r.SyncKey, SyncOptions{Class: "Email", FilterType: "0"})
	if err != nil || n != 2 {
		t.Fatalf("estimate %d %v", n, err)
	}
	if _, err := c.GetItemEstimate(ctx, CollectionID(inbox.RemoteID), "55", SyncOptions{}); !errors.Is(err, ErrInvalidSyncKey) {
		t.Fatalf("estimate bad key: %v", err)
	}
	// OOF roundtrip.
	start := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
	if err := c.SetOof(ctx, Oof{State: OofTimed, Start: start, End: start.Add(72 * time.Hour), InternalText: "Away",
		ExternalText: "Out of office", ExternalEnabled: true}); err != nil {
		t.Fatal(err)
	}
	o, err := c.GetOof(ctx)
	if err != nil || o.State != OofTimed || o.InternalText != "Away" || o.ExternalText != "Out of office" ||
		!o.ExternalEnabled || o.ExternalKnownOnly || !o.Start.Equal(start) {
		t.Fatalf("oof %+v %v", o, err)
	}
	if err := c.SendDeviceInformation(ctx); err != nil || s.DeviceInformation()["FriendlyName"] != "TuxMail" {
		t.Fatalf("device info %v %v", err, s.DeviceInformation())
	}
	primary, addrs, err := c.UserInformation(ctx)
	if err != nil || primary != "alice@example.com" || len(addrs) != 1 {
		t.Fatalf("user info %q %v %v", primary, addrs, err)
	}
}

func TestMailStateRoundtrip(t *testing.T) {
	s := &collState{Key: "12", Items: map[string]itemFlags{"1": {Mask: bitRead}, "a/b": {Mask: 3, Categories: []string{"X", "Y Z"}}}}
	back, err := decodeCollState(s.encode())
	if err != nil || back.Key != "12" || back.Items["a/b"].Categories[1] != "Y Z" || back.Items["1"].Mask != bitRead {
		t.Fatalf("%+v %v", back, err)
	}
	if CollectionID("1/2%2F3") != "2/3" || escapeID("2/3") != "2%2F3" {
		t.Fatal("escaping")
	}
}

func TestEmailParsing(t *testing.T) {
	d := wbxml.E("AirSync:ApplicationData",
		wbxml.S("Email:From", `"Müller, Hans" <hans@example.com>`), wbxml.S("Email:To", "a@x; b@y"),
		wbxml.S("Email:DateReceived", "2026-01-02T10:00:00.000Z"), wbxml.S("Email:Read", "0"),
		wbxml.E("Email:Flag", wbxml.S("Email:Status", "2")), wbxml.S("Email2:LastVerbExecuted", "3"),
		wbxml.E("AirSyncBase:Attachments", wbxml.E("AirSyncBase:Attachment", wbxml.S("AirSyncBase:DisplayName", "x.pdf"))),
		wbxml.E("AirSyncBase:Body", wbxml.S("AirSyncBase:Type", "4"), wbxml.S("AirSyncBase:Data",
			"Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\nMessage-ID: <x@y>\r\nThread-Index: AQHX\r\nReferences: <a@b>\r\n <c@d>\r\n")))
	f := mergeFlags(itemFlags{}, d)
	rm := emailMessage("1", "9", d, f, defaultCategoryToKeyword)
	if rm.Header.Subject != "Grüße" || rm.Header.MessageID != "x@y" || len(rm.Header.References) != 2 ||
		rm.Header.From[0].Name != "Müller, Hans" || len(rm.Header.To) != 2 || !rm.HasAttachments ||
		!hasFlag(rm.Flags, domain.FlagFlagged) || !hasFlag(rm.Flags, domain.FlagForwarded) || hasFlag(rm.Flags, domain.FlagSeen) ||
		rm.Header.ThreadIndex != "AQHX" {
		t.Fatalf("%+v", rm)
	}
	// A Change with only Read keeps the flag.
	f = mergeFlags(f, wbxml.E("AirSync:ApplicationData", wbxml.S("Email:Read", "1")))
	if f.Mask&bitFlagged == 0 || f.Mask&bitRead == 0 {
		t.Fatalf("mask %b", f.Mask)
	}
	if got := withBcc([]byte("To: a@x\r\nCc: B <b@y>\r\n\r\nx"), []string{"a@x", "B@y", "c@z"}); !strings.HasPrefix(string(got), "Bcc: c@z\r\n") {
		t.Fatalf("bcc %q", got)
	}
}

func TestPagingWindowSize(t *testing.T) {
	s := newFake(t)
	p := newProvider(t, s, func(c *Config) { c.WindowSize = 3 })
	ctx := testCtx(t)
	fs, _ := p.ListFolders(ctx)
	inbox := folderByRole(t, fs, domain.RoleInbox)
	for i := 0; i < 10; i++ {
		s.AddMail(eastest.Inbox, testMIME(fmt.Sprintf("p%d@x", i), fmt.Sprint(i), ""), false)
	}
	s.ResetCommands()
	d, _, err := p.Sync(ctx, inbox.RemoteID, "")
	if err != nil || len(d.Added) != 10 {
		t.Fatalf("%d %v", len(d.Added), err)
	}
	if n := len(s.Commands()); n != 5 { // initial + 4 pages
		t.Fatalf("expected 5 Sync requests, got %d", n)
	}
}
