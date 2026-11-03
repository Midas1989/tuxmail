// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/project"
	imapprov "github.com/Midas1989/Mailclient/internal/providers/imap"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

var (
	_ Tagger       = (*project.Service)(nil)
	_ ProjectHooks = (*project.Service)(nil)
)

// ---- in-process IMAP server ------------------------------------------------

type testServer struct {
	t    *testing.T
	mem  *imapmemserver.Server
	user *imapmemserver.User
	mu   stdsync.Mutex
	srv  *imapserver.Server
	addr string
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}

func newTestServer(t *testing.T, mailboxes ...string) *testServer {
	t.Helper()
	ts := &testServer{t: t, mem: imapmemserver.New()}
	ts.user = imapmemserver.NewUser("alice", "secret")
	for _, mb := range append([]string{"INBOX"}, mailboxes...) {
		if err := ts.user.Create(mb, nil); err != nil {
			t.Fatal(err)
		}
	}
	ts.mem.AddUser(ts.user)
	ts.start("127.0.0.1:0")
	t.Cleanup(ts.stop)
	return ts
}

func (ts *testServer) start(addr string) {
	ts.t.Helper()
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return ts.mem.NewSession(), nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}, imap.CapESearch: {}, imap.CapIdle: {},
			imap.CapListExtended: {}, imap.CapListStatus: {}, imap.CapNamespace: {}, imap.CapSpecialUse: {},
			imap.CapChildren: {},
		},
		InsecureAuth: true,
		Logger:       discardLogger{},
	})
	var (
		ln  net.Listener
		err error
	)
	for i := 0; i < 100; i++ {
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		ts.t.Fatal(err)
	}
	ts.mu.Lock()
	ts.srv, ts.addr = srv, ln.Addr().String()
	ts.mu.Unlock()
	go srv.Serve(ln)
}

func (ts *testServer) stop() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.srv != nil {
		ts.srv.Close()
		ts.srv = nil
	}
}

func (ts *testServer) server() domain.ServerConfig {
	host, port, _ := net.SplitHostPort(ts.addr)
	var p int
	fmt.Sscan(port, &p)
	return domain.ServerConfig{Host: host, Port: p, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "alice"}
}

// ext is another mail client working on the same mailbox.
func (ts *testServer) ext(t *testing.T) *imapprov.Provider {
	t.Helper()
	p, err := imapprov.New(imapprov.Config{Server: ts.server(), Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// ---- test environment ------------------------------------------------------

type flaky struct {
	domain.MailProvider
	moveFails  *atomic.Int32 // temporary failures of Move
	flagsBlock *atomic.Bool  // permanent failure of SetFlags with \Flagged
}

func (f *flaky) Move(ctx context.Context, refs []domain.RemoteRef, to string) ([]domain.RemoteRef, error) {
	if f.moveFails.Add(-1) >= 0 {
		return nil, errors.New("NO [UNAVAILABLE] try again later")
	}
	return f.MailProvider.Move(ctx, refs, to)
}

func (f *flaky) SetFlags(ctx context.Context, refs []domain.RemoteRef, add, remove []domain.Flag) error {
	if f.flagsBlock.Load() {
		for _, fl := range add {
			if fl == domain.FlagFlagged {
				return Permanent(errors.New("NO flag not permitted"))
			}
		}
	}
	return f.MailProvider.SetFlags(ctx, refs, add, remove)
}

type env struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	srv   *testServer
	mgr   *Manager
	acc   int64
	proj  *project.Service
	fails atomic.Int32
	block atomic.Bool

	evMu stdsync.Mutex
	evs  []events.Event
}

func newEnv(t *testing.T, srv *testServer, mutate func(*Config)) *env {
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
	sec := secrets.NewMemory()
	if err := sec.Set("acc-secret", []byte("secret")); err != nil {
		t.Fatal(err)
	}
	server := srv.server()
	acc, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Alice", Incoming: &server,
		SecretRef: "acc-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	e := &env{t: t, ctx: ctx, st: st, srv: srv, acc: acc}
	sub := bus.Subscribe(ctx, events.SubscribeOptions{Name: "test", Buffer: 4096})
	go func() {
		for ev := range sub.C {
			e.evMu.Lock()
			e.evs = append(e.evs, ev)
			e.evMu.Unlock()
		}
	}()
	e.proj = project.New(st, bus, project.Options{})
	reg := NewRegistry()
	reg.Register(domain.KindIMAP, func(ctx context.Context, a domain.Account, c Credentials, o ProviderOptions) (domain.MailProvider, error) {
		p, err := IMAPFactory(ctx, a, c, o)
		if err != nil {
			return nil, err
		}
		return &flaky{MailProvider: p, moveFails: &e.fails, flagsBlock: &e.block}, nil
	})
	cfg := Config{
		Store: st, Blobs: blobs, Bus: bus, Secrets: sec, Providers: reg,
		ProviderOptions: ProviderOptions{WatchMinBackoff: 20 * time.Millisecond, WatchMaxBackoff: 200 * time.Millisecond},
		Projects:        e.proj, Tags: e.proj,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: time.Minute, FolderListInterval: time.Minute, Debounce: 20 * time.Millisecond,
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
		OpBackoff:        func(int) time.Duration { return 0 },
		MaintenanceDelay: time.Hour, OrphanMaxAge: time.Millisecond,
		IngestBatch: 2, BackfillBatch: 2,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e.mgr, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.mgr.Stop)
	return e
}

func (e *env) eventually(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	e.t.Fatalf("timeout waiting for %s", what)
}

func (e *env) events(typ events.Type) []events.Event {
	e.evMu.Lock()
	defer e.evMu.Unlock()
	var out []events.Event
	for _, ev := range e.evs {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func (e *env) msg(mid string) store.Message {
	e.t.Helper()
	m, err := e.st.FindMessageByMessageID(e.ctx, e.acc, mid)
	if err != nil {
		e.t.Fatalf("message %s: %v", mid, err)
	}
	return m
}

func (e *env) has(mid string) bool {
	_, err := e.st.FindMessageByMessageID(e.ctx, e.acc, mid)
	return err == nil
}

func (e *env) folder(remote string) domain.Folder {
	e.t.Helper()
	f, err := e.st.FolderByRemoteID(e.ctx, e.acc, remote)
	if err != nil {
		e.t.Fatalf("folder %s: %v", remote, err)
	}
	return f
}

func (e *env) inFolder(mid, remote string) bool {
	m, err := e.st.FindMessageByMessageID(e.ctx, e.acc, mid)
	if err != nil {
		return false
	}
	f, err := e.st.FolderByRemoteID(e.ctx, e.acc, remote)
	if err != nil {
		return false
	}
	locs, _ := e.st.Locations(e.ctx, m.ID)
	for _, l := range locs {
		if l.FolderID == f.ID && !isPlaceholder(l.RemoteRef) {
			return true
		}
	}
	return false
}

func (e *env) idle() bool {
	st, ok := e.mgr.Status(e.acc)
	return ok && st.State == StateIdle
}

// serverMessage returns ref and flags of a message on the server.
func serverMessage(ctx context.Context, p *imapprov.Provider, folder, mid string) (domain.RemoteRef, []domain.Flag, bool) {
	d, _, err := p.Sync(ctx, folder, "")
	if err != nil {
		return domain.RemoteRef{}, nil, false
	}
	for _, m := range d.Added {
		if m.Header.MessageID == mid {
			return m.Ref, m.Flags, true
		}
	}
	return domain.RemoteRef{}, nil, false
}

func hasFlag(fs []domain.Flag, f domain.Flag) bool {
	for _, x := range fs {
		if strings.EqualFold(string(x), string(f)) {
			return true
		}
	}
	return false
}

func mail(mid, from, to, subject string, date time.Time, extra, body string) string {
	return "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + subject + "\r\nDate: " + date.Format(time.RFC1123Z) +
		"\r\nMessage-ID: <" + mid + ">\r\n" + extra + "MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		body + "\r\n"
}

func appendMail(t *testing.T, ctx context.Context, p *imapprov.Provider, folder, raw string, flags ...domain.Flag) {
	t.Helper()
	if _, err := p.Append(ctx, folder, strings.NewReader(raw), flags); err != nil {
		t.Fatal(err)
	}
}

// ---- tests -------------------------------------------------------------------

func TestEndToEnd(t *testing.T) {
	srv := newTestServer(t, "Sent", "Archive", "Trash")
	ctx := context.Background()
	ext := srv.ext(t)
	base := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	carol, alice := "Carol <carol@example.org>", "Alice <alice@example.org>"
	appendMail(t, ctx, ext, "INBOX", mail("m1@x", carol, alice, "Budget 2027", base, "", "Here are the numbers."), domain.FlagSeen)
	appendMail(t, ctx, ext, "INBOX", mail("m2@x", carol, alice, "Re: Budget 2027", base.Add(time.Hour),
		"In-Reply-To: <m1@x>\r\nReferences: <m1@x>\r\n", "One more thing."))
	appendMail(t, ctx, ext, "Sent", mail("m3@x", alice, carol, "Re: Budget 2027", base.Add(2*time.Hour),
		"In-Reply-To: <m2@x>\r\nReferences: <m1@x> <m2@x>\r\n", "Thanks!"), domain.FlagSeen)
	appendMail(t, ctx, ext, "INBOX", mail("m4@x", "Dave <dave@example.org>", alice, "Fwd: numbers", base.Add(3*time.Hour), "",
		"FYI\r\n\r\n---------- Forwarded message ---------\r\nFrom: Carol <carol@example.org>\r\nDate: "+
			base.Format(time.RFC1123Z)+"\r\nSubject: Budget 2027\r\nTo: Alice <alice@example.org>\r\n\r\nHere are the numbers."))
	appendMail(t, ctx, ext, "INBOX", mail("m5@x", "Eve <eve@example.org>", alice, "Lunch", base.Add(4*time.Hour), "",
		"Pizza on friday? zanzibarword"))

	e := newEnv(t, srv, nil)

	// Initial sync: messages, folders with roles, conversations, bodies.
	e.eventually("initial sync", func() bool {
		for _, id := range []string{"m1@x", "m2@x", "m3@x", "m4@x", "m5@x"} {
			if !e.has(id) {
				return false
			}
		}
		return true
	})
	if f := e.folder("Sent"); f.Role != domain.RoleSent {
		t.Errorf("Sent role = %q", f.Role)
	}
	e.eventually("thread incl. inline forward", func() bool {
		c := e.msg("m1@x").ConversationID
		return c != 0 && e.msg("m2@x").ConversationID == c && e.msg("m3@x").ConversationID == c &&
			e.msg("m4@x").ConversationID == c
	})
	thread := e.msg("m1@x").ConversationID
	if e.msg("m5@x").ConversationID == thread || e.msg("m5@x").ConversationID == 0 {
		t.Errorf("unrelated mail joined the thread")
	}
	e.eventually("bodies and search", func() bool {
		ids, err := e.st.SearchMessages(e.ctx, "zanzibarword", 10)
		return err == nil && len(ids) == 1 && ids[0] == e.msg("m5@x").ID
	})
	if m := e.msg("m5@x"); m.Snippet == "" || m.BlobHash == "" {
		t.Errorf("content not stored: %+v", m)
	}
	conv, err := e.st.GetConversation(e.ctx, thread)
	if err != nil || conv.UnreadCount != 2 || conv.Title == "" {
		t.Errorf("conversation = %+v %v", conv, err)
	}
	if inbox := e.folder("INBOX"); inbox.Total != 4 || inbox.Unread != 3 {
		t.Errorf("inbox counts = %d/%d", inbox.Total, inbox.Unread)
	}
	if n := len(e.events(events.MailReceived)); n != 0 {
		t.Errorf("initial sync published %d mail.received", n)
	}
	e.eventually("idle", e.idle)
	if len(e.events(EventAccountStatus)) == 0 {
		t.Error("no account.status events")
	}

	// IDLE: new mail arrives without polling and emits mail.received.
	appendMail(t, ctx, ext, "INBOX", mail("m6@x", carol, alice, "Re: Budget 2027", base.Add(5*time.Hour),
		"In-Reply-To: <m2@x>\r\n", "Final numbers attached."))
	e.eventually("mail.received via IDLE", func() bool {
		for _, ev := range e.events(events.MailReceived) {
			if ev.Mail != nil && ev.Mail.MessageID == "m6@x" && strings.Contains(ev.Mail.Body, "Final numbers") &&
				ev.Mail.From.Email == "carol@example.org" && ev.Mail.FolderRole == "inbox" {
				return true
			}
		}
		return false
	})
	if e.msg("m6@x").ConversationID != thread {
		t.Error("new reply not threaded")
	}

	// Move via Actions (first attempt fails temporarily): local first,
	// then on the server; conversation kept.
	e.fails.Store(1)
	archive := e.folder("Archive")
	if err := e.mgr.Actions().Move(e.ctx, []int64{e.msg("m2@x").ID}, archive.ID); err != nil {
		t.Fatal(err)
	}
	locs, _ := e.st.Locations(e.ctx, e.msg("m2@x").ID)
	if len(locs) != 1 || locs[0].FolderID != archive.ID {
		t.Fatalf("move not applied locally: %+v", locs)
	}
	e.eventually("move on server", func() bool {
		_, _, ok := serverMessage(e.ctx, ext, "Archive", "m2@x")
		return ok && e.inFolder("m2@x", "Archive")
	})
	if _, _, ok := serverMessage(e.ctx, ext, "INBOX", "m2@x"); ok {
		t.Error("message still in INBOX on server")
	}
	ops, _ := e.st.ListOps(e.ctx, store.OpDone)
	retried := false
	for _, op := range ops {
		retried = retried || (op.Kind == OpMove && op.Attempts == 2)
	}
	if !retried {
		t.Errorf("move op was not retried: %+v", ops)
	}
	if e.msg("m2@x").ConversationID != thread {
		t.Error("move changed the conversation")
	}
	if len(e.events(events.MailMoved)) == 0 {
		t.Error("no mail.moved event")
	}

	// External move on the server: orphaned, then reattached, same row.
	before := e.msg("m1@x")
	ref, _, ok := serverMessage(e.ctx, ext, "INBOX", "m1@x")
	if !ok {
		t.Fatal("m1 not on server")
	}
	if _, err := ext.Move(e.ctx, []domain.RemoteRef{ref}, "Archive"); err != nil {
		t.Fatal(err)
	}
	e.mgr.SyncNow(e.acc)
	e.eventually("external move reattached", func() bool { return e.inFolder("m1@x", "Archive") && !e.inFolder("m1@x", "INBOX") })
	after := e.msg("m1@x")
	if after.ID != before.ID || after.ConversationID != thread || after.State != store.StateLive {
		t.Errorf("external move lost identity: %+v -> %+v", before, after)
	}

	// Flags: local -> server and server -> local.
	m5 := e.msg("m5@x")
	if err := e.mgr.Actions().SetFlags(e.ctx, []int64{m5.ID}, []domain.Flag{domain.FlagFlagged}, nil); err != nil {
		t.Fatal(err)
	}
	if e.msg("m5@x").Flags&store.FlagFlagged == 0 {
		t.Fatal("flag not applied locally")
	}
	e.eventually("flag on server", func() bool {
		_, fl, ok := serverMessage(e.ctx, ext, "INBOX", "m5@x")
		return ok && hasFlag(fl, domain.FlagFlagged)
	})
	ref5, _, _ := serverMessage(e.ctx, ext, "INBOX", "m5@x")
	if err := ext.SetFlags(e.ctx, []domain.RemoteRef{ref5}, []domain.Flag{domain.FlagSeen}, nil); err != nil {
		t.Fatal(err)
	}
	e.eventually("server flag synced", func() bool { return e.msg("m5@x").Flags&store.FlagSeen != 0 })

	// Permanent failure: the local change is reverted and op.failed published.
	e.block.Store(true)
	m6 := e.msg("m6@x")
	if err := e.mgr.Actions().SetFlags(e.ctx, []int64{m6.ID}, []domain.Flag{domain.FlagFlagged}, nil); err != nil {
		t.Fatal(err)
	}
	e.eventually("revert", func() bool {
		return len(e.events(EventOpFailed)) == 1 && e.msg("m6@x").Flags&store.FlagFlagged == 0
	})
	e.block.Store(false)

	// Tags are pushed as IMAP keywords.
	if _, err := e.proj.Tag(e.ctx, "Projekt", store.TagSourceUser, store.Item{Kind: store.ItemMessage, ID: m5.ID}); err != nil {
		t.Fatal(err)
	}
	e.eventually("keyword on server", func() bool {
		_, fl, ok := serverMessage(e.ctx, ext, "INBOX", "m5@x")
		pending, _ := e.st.PendingTagSync(e.ctx, e.acc, 10)
		return ok && hasFlag(fl, "Projekt") && len(pending) == 0
	})

	// Workflow backend.
	wm := e.mgr.MailActions()
	if err := wm.Archive(e.ctx, workflow.ItemRef{AccountID: e.acc, Key: "m5@x"}); err != nil {
		t.Fatal(err)
	}
	e.eventually("workflow archive", func() bool { return e.inFolder("m5@x", "Archive") })
	if err := wm.Forward(e.ctx, workflow.ItemRef{AccountID: e.acc, Key: "m5@x"}, workflow.ForwardRequest{}); !errors.Is(err, ErrNotImplemented) || !workflow.IsPermanent(err) {
		t.Errorf("forward = %v", err)
	}

	// Offline: changes are applied locally and replayed after reconnect.
	srv.stop()
	if err := e.mgr.Actions().MarkRead(e.ctx, []int64{m6.ID}, true); err != nil {
		t.Fatal(err)
	}
	e.eventually("error state", func() bool {
		st, _ := e.mgr.Status(e.acc)
		return st.State == StateError && st.LastError != "" && !st.NextRetry.IsZero()
	})
	srv.start(srv.addr)
	appendMail(t, ctx, srv.ext(t), "INBOX", mail("m7@x", "Eve <eve@example.org>", alice, "Back online", time.Now(), "", "hello again"))
	e.eventually("reconnected", func() bool {
		_, fl, ok := serverMessage(e.ctx, srv.ext(t), "INBOX", "m6@x")
		return ok && hasFlag(fl, domain.FlagSeen) && e.has("m7@x")
	})

	// Append (draft) shows up locally at once and on the server later.
	drafts := e.folder("Trash") // any folder will do
	id, err := e.mgr.Actions().Append(e.ctx, drafts.ID, []byte(mail("d1@x", alice, carol, "Re: Lunch", time.Now(),
		"In-Reply-To: <m5@x>\r\n", "Sure.")), []domain.Flag{domain.FlagSeen})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.msg("d1@x"); got.ID != id || got.ConversationID != e.msg("m5@x").ConversationID {
		t.Errorf("appended draft not threaded: %+v", got)
	}
	e.eventually("append on server", func() bool { return e.inFolder("d1@x", "Trash") })

	// Permanent delete and garbage collection keep relations.
	if err := e.mgr.Actions().DeletePermanently(e.ctx, []int64{id}); err != nil {
		t.Fatal(err)
	}
	e.eventually("deleted on server", func() bool {
		_, _, ok := serverMessage(e.ctx, srv.ext(t), "Trash", "d1@x")
		ops, _ := e.st.ListOps(e.ctx, store.OpPending)
		return !ok && len(ops) == 0
	})
	time.Sleep(5 * time.Millisecond)
	rep, err := e.mgr.CollectGarbage(e.ctx)
	if err != nil || rep.Deleted != 1 || rep.Unthreaded != 1 {
		t.Errorf("gc = %+v %v", rep, err)
	}
	if rels, _ := e.st.RelationsByKey(e.ctx, "d1@x", 0); len(rels) == 0 {
		t.Error("relations lost by gc")
	}
}

func TestFolderActionsAndAccounts(t *testing.T) {
	srv := newTestServer(t, "Archive")
	ctx := context.Background()
	ext := srv.ext(t)
	appendMail(t, ctx, ext, "INBOX", mail("a1@x", "Bob <bob@example.org>", "Alice <alice@example.org>", "Hi", time.Now(), "", "hi"))
	e := newEnv(t, srv, func(c *Config) { c.Bodies = BodiesOnDemand })
	e.eventually("synced", func() bool { return e.has("a1@x") && e.idle() })
	if m := e.msg("a1@x"); m.BlobHash != "" {
		t.Error("on-demand policy downloaded a body")
	}
	raw, err := e.mgr.FetchBody(e.ctx, e.msg("a1@x").ID)
	if err != nil || !strings.Contains(string(raw), "Subject: Hi") || e.msg("a1@x").BlobHash == "" {
		t.Fatalf("FetchBody: %v", err)
	}

	// Folder create / rename / delete.
	inbox := e.folder("INBOX")
	fid, err := e.mgr.Actions().CreateFolder(e.ctx, e.acc, 0, "Projects")
	if err != nil {
		t.Fatal(err)
	}
	e.eventually("folder created on server", func() bool {
		fs, _ := ext.ListFolders(e.ctx)
		for _, f := range fs {
			if f.RemoteID == "Projects" {
				return true
			}
		}
		return false
	})
	if err := e.mgr.Actions().RenameFolder(e.ctx, fid, "Clients"); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.st.GetFolder(e.ctx, fid); f.RemoteID != "Clients" {
		t.Errorf("rename not local: %+v", f)
	}
	e.eventually("folder renamed on server", func() bool {
		fs, _ := ext.ListFolders(e.ctx)
		for _, f := range fs {
			if f.RemoteID == "Clients" {
				return true
			}
		}
		return false
	})
	if err := e.mgr.Actions().Copy(e.ctx, []int64{e.msg("a1@x").ID}, fid); err != nil {
		t.Fatal(err)
	}
	e.eventually("copied", func() bool { return e.inFolder("a1@x", "Clients") && e.inFolder("a1@x", "INBOX") })
	if err := e.mgr.Actions().DeleteFolder(e.ctx, fid); err != nil {
		t.Fatal(err)
	}
	e.eventually("folder deleted on server", func() bool {
		fs, _ := ext.ListFolders(e.ctx)
		for _, f := range fs {
			if f.RemoteID == "Clients" {
				return false
			}
		}
		ops, _ := e.st.ListOps(e.ctx, store.OpPending)
		return len(ops) == 0
	})
	if _, err := e.st.GetFolder(e.ctx, fid); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("folder still local: %v", err)
	}
	if !e.inFolder("a1@x", "INBOX") || inbox.ID != e.folder("INBOX").ID {
		t.Error("inbox affected")
	}

	// A second account on the same mailbox: same keys, one conversation.
	server := srv.server()
	acc2, err := e.st.CreateAccount(e.ctx, domain.Account{Kind: domain.KindIMAP, DisplayName: "Alias", Incoming: &server,
		SecretRef: "acc-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.AddAccount(e.ctx, acc2); err != nil {
		t.Fatal(err)
	}
	e.eventually("second account synced", func() bool {
		m, err := e.st.FindMessageByMessageID(e.ctx, acc2, "a1@x")
		return err == nil && m.ConversationID != 0 && m.ConversationID == e.msg("a1@x").ConversationID
	})
	if len(e.mgr.Statuses()) != 2 {
		t.Errorf("statuses = %+v", e.mgr.Statuses())
	}
	if err := e.mgr.SetAccountEnabled(e.ctx, acc2, false); err != nil {
		t.Fatal(err)
	}
	if st, ok := e.mgr.Status(acc2); ok || st.State != StateStopped {
		t.Errorf("disabled account still running: %+v", st)
	}
	// Relations are keyed by message key, so the shared conversation
	// survives one account's rows going away.
	if _, err := e.mgr.CollectGarbage(e.ctx); err != nil {
		t.Fatal(err)
	}
}
