// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// ---- in-process server -----------------------------------------------------

type testServer struct {
	t     *testing.T
	mem   *imapmemserver.Server
	user  *imapmemserver.User
	caps  imap.CapSet
	addr  string
	mu    sync.Mutex
	srv   *imapserver.Server
	oauth atomic.Bool
}

var (
	capsFull = imap.CapSet{
		imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}, imap.CapMove: {}, imap.CapESearch: {},
		imap.CapListExtended: {}, imap.CapListStatus: {}, imap.CapNamespace: {},
		imap.CapSpecialUse: {}, imap.CapChildren: {},
	}
	capsMinimal = imap.CapSet{imap.CapIMAP4rev1: {}}
)

type discardLogger struct{}

func (discardLogger) Printf(string, ...interface{}) {}

func newTestServer(t *testing.T, caps imap.CapSet) *testServer {
	t.Helper()
	ts := &testServer{t: t, mem: imapmemserver.New(), caps: caps}
	ts.user = imapmemserver.NewUser("alice", "secret")
	if err := ts.user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
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
			sess := ts.mem.NewSession()
			if ts.oauth.Load() {
				return &oauthSession{SessionIMAP4rev2: sess.(imapserver.SessionIMAP4rev2)}, nil, nil
			}
			return sess, nil, nil
		},
		Caps:         ts.caps,
		InsecureAuth: true,
		Logger:       discardLogger{},
	})
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ { // the port may linger briefly after a restart
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

func (ts *testServer) restart() {
	ts.stop()
	ts.start(ts.addr)
}

func (ts *testServer) config() Config {
	host, port, _ := net.SplitHostPort(ts.addr)
	var p int
	fmt.Sscan(port, &p)
	return Config{
		Server:     domain.ServerConfig{Host: host, Port: p, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "alice"},
		Password:   "secret",
		MinBackoff: 20 * time.Millisecond,
		MaxBackoff: 200 * time.Millisecond,
	}
}

func (ts *testServer) provider(t *testing.T) *Provider {
	t.Helper()
	return ts.providerWith(t, ts.config())
}

func (ts *testServer) providerWith(t *testing.T, cfg Config) *Provider {
	t.Helper()
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// oauthSession adds XOAUTH2/OAUTHBEARER to the memory server.
type oauthSession struct {
	imapserver.SessionIMAP4rev2
}

const goodToken = "ya29.token"

func (s *oauthSession) AuthenticateMechanisms() []string { return []string{"XOAUTH2", "OAUTHBEARER"} }

func (s *oauthSession) Authenticate(mech string) (sasl.Server, error) {
	login := func(user, token string) error {
		if user != "alice" || token != goodToken {
			return imapserver.ErrAuthFailed
		}
		return s.Login("alice", "secret")
	}
	switch mech {
	case "XOAUTH2":
		return saslServerFunc(func(resp []byte) ([]byte, bool, error) {
			var user, token string
			for _, part := range strings.Split(string(resp), "\x01") {
				if v, ok := strings.CutPrefix(part, "user="); ok {
					user = v
				}
				if v, ok := strings.CutPrefix(part, "auth=Bearer "); ok {
					token = v
				}
			}
			return nil, true, login(user, token)
		}), nil
	case "OAUTHBEARER":
		return sasl.NewOAuthBearerServer(func(o sasl.OAuthBearerOptions) *sasl.OAuthBearerError {
			if login(o.Username, o.Token) != nil {
				return &sasl.OAuthBearerError{Status: "invalid_token"}
			}
			return nil
		}), nil
	}
	return nil, errors.New("unsupported mechanism")
}

type saslServerFunc func([]byte) ([]byte, bool, error)

func (f saslServerFunc) Next(resp []byte) ([]byte, bool, error) {
	if resp == nil {
		return []byte{}, false, nil
	}
	return f(resp)
}

// ---- helpers ---------------------------------------------------------------

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

func testMessage(n int, extra string) string {
	return fmt.Sprintf("From: Alice <alice@example.org>\r\n"+
		"To: Bob <bob@example.org>\r\n"+
		"Subject: Message %d\r\n"+
		"Date: Mon, 02 Nov 2026 10:%02d:00 +0100\r\n"+
		"Message-ID: <msg%d@Example.ORG>\r\n"+
		"%s"+
		"Content-Type: text/plain\r\n\r\n"+
		"Body %d\r\n", n, n, n, extra, n)
}

const multipartMsg = "From: carol@example.org\r\n" +
	"To: alice@example.org\r\n" +
	"Subject: Report\r\n" +
	"Message-ID: <report@example.org>\r\n" +
	"In-Reply-To: <msg1@example.org>\r\n" +
	"References: <root@example.org>\r\n <msg1@example.org>\r\n" +
	"Thread-Index: AdlXyz123==\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=XX\r\n\r\n" +
	"--XX\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
	"--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"r.pdf\"\r\n\r\nPDFDATA\r\n" +
	"--XX--\r\n"

func appendMsg(t *testing.T, p *Provider, folder, raw string, flags ...domain.Flag) domain.RemoteRef {
	t.Helper()
	r, err := p.Append(ctx(t), folder, strings.NewReader(raw), flags)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if r.ID == "" {
		t.Fatalf("append returned no UID")
	}
	return r
}

func ids(refs []domain.RemoteRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

func addedIDs(msgs []domain.RemoteMessage) []string {
	var refs []domain.RemoteRef
	for _, m := range msgs {
		refs = append(refs, m.Ref)
	}
	return ids(refs)
}

func hasFlag(flags []domain.Flag, f domain.Flag) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

// ---- tests -----------------------------------------------------------------

func TestConnectAndCapabilities(t *testing.T) {
	ts := newTestServer(t, capsFull)
	p := ts.provider(t)
	if got := p.Capabilities(); got != (domain.MailCaps{}) {
		t.Errorf("caps before connect = %+v", got)
	}
	if err := p.Connect(ctx(t)); err != nil {
		t.Fatal(err)
	}
	caps := p.Capabilities()
	if !caps.Move || !caps.Idle || caps.CondStore || caps.ObjectID {
		t.Errorf("caps = %+v", caps)
	}

	cfg := ts.config()
	cfg.Password = "wrong"
	if err := ts.providerWith(t, cfg).Connect(ctx(t)); err == nil {
		t.Error("expected login failure")
	}
}

func TestOAuth(t *testing.T) {
	ts := newTestServer(t, capsFull)
	ts.oauth.Store(true)
	for _, mech := range []string{"", "XOAUTH2", "OAUTHBEARER"} {
		cfg := ts.config()
		cfg.Server.Auth = domain.AuthOAuth2
		cfg.OAuthMechanism = mech
		cfg.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return goodToken, nil })
		p := ts.providerWith(t, cfg)
		if _, err := p.ListFolders(ctx(t)); err != nil {
			t.Errorf("mech %q: %v", mech, err)
		}
	}
	cfg := ts.config()
	cfg.Server.Auth = domain.AuthOAuth2
	cfg.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "bad", nil })
	if err := ts.providerWith(t, cfg).Connect(ctx(t)); err == nil {
		t.Error("expected OAuth failure")
	}
	if _, err := New(Config{Server: domain.ServerConfig{Host: "x", Auth: domain.AuthOAuth2}}); err == nil {
		t.Error("expected error without token source")
	}
}

func TestFolderOperations(t *testing.T) {
	for name, caps := range map[string]imap.CapSet{"full": capsFull, "minimal": capsMinimal} {
		t.Run(name, func(t *testing.T) {
			ts := newTestServer(t, caps)
			p := ts.provider(t)
			c := ctx(t)
			for _, n := range []string{"Gesendet", "Entwürfe", "Papierkorb", "Spam", "Archiv", "Projects"} {
				f, err := p.CreateFolder(c, "", n)
				if err != nil {
					t.Fatalf("create %s: %v", n, err)
				}
				if f.RemoteID != n || f.Name != n {
					t.Errorf("created folder = %+v", f)
				}
			}
			sub, err := p.CreateFolder(c, "Projects", "2026")
			if err != nil {
				t.Fatal(err)
			}
			if sub.RemoteID != "Projects/2026" || sub.Name != "2026" || sub.Delimiter != "/" {
				t.Errorf("sub folder = %+v", sub)
			}
			if _, err := p.CreateFolder(c, "", "a/b"); err == nil {
				t.Error("expected error for name containing the delimiter")
			}

			folders, err := p.ListFolders(c)
			if err != nil {
				t.Fatal(err)
			}
			roles := map[string]domain.FolderRole{}
			for _, f := range folders {
				roles[f.RemoteID] = f.Role
			}
			want := map[string]domain.FolderRole{
				"INBOX": domain.RoleInbox, "Gesendet": domain.RoleSent, "Entwürfe": domain.RoleDrafts,
				"Papierkorb": domain.RoleTrash, "Spam": domain.RoleJunk, "Archiv": domain.RoleArchive,
				"Projects": domain.RoleNone, "Projects/2026": domain.RoleNone,
			}
			for k, v := range want {
				if r, ok := roles[k]; !ok || r != v {
					t.Errorf("folder %q: role %q (present %v), want %q", k, r, ok, v)
				}
			}

			r, err := p.RenameFolder(c, "Projects/2026", "2027")
			if err != nil {
				t.Fatal(err)
			}
			if r.RemoteID != "Projects/2027" {
				t.Errorf("renamed = %+v", r)
			}
			// select it, then delete it while selected
			if _, _, err := p.Sync(c, "Projects/2027", ""); err != nil {
				t.Fatal(err)
			}
			if err := p.DeleteFolder(c, "Projects/2027"); err != nil {
				t.Fatal(err)
			}
			if err := p.DeleteFolder(c, "INBOX"); err == nil {
				t.Error("deleting INBOX must fail")
			}
			folders, _ = p.ListFolders(c)
			for _, f := range folders {
				if strings.HasPrefix(f.RemoteID, "Projects/") {
					t.Errorf("folder still listed: %+v", f)
				}
			}
		})
	}
}

func TestListFoldersStatus(t *testing.T) {
	ts := newTestServer(t, capsFull)
	p := ts.provider(t)
	appendMsg(t, p, "INBOX", testMessage(1, ""))
	appendMsg(t, p, "INBOX", testMessage(2, ""), domain.FlagSeen)
	folders, err := p.ListFolders(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].Total != 2 || folders[0].Unread != 1 {
		t.Errorf("folders = %+v", folders)
	}
}

func TestSyncDeltas(t *testing.T) {
	for name, caps := range map[string]imap.CapSet{"full": capsFull, "minimal": capsMinimal} {
		t.Run(name, func(t *testing.T) {
			ts := newTestServer(t, caps)
			p := ts.provider(t)
			other := ts.provider(t) // simulates another client
			c := ctx(t)

			r1 := appendMsg(t, other, "INBOX", testMessage(1, ""))
			r2 := appendMsg(t, other, "INBOX", testMessage(2, ""), domain.FlagSeen, "$label1")
			r3 := appendMsg(t, other, "INBOX", multipartMsg)

			delta, st, err := p.Sync(c, "INBOX", "")
			if err != nil {
				t.Fatal(err)
			}
			if delta.FullResync || len(delta.Added) != 3 || len(delta.Removed) != 0 || len(delta.FlagsChanged) != 0 {
				t.Fatalf("initial delta = %+v", delta)
			}
			if !strings.HasPrefix(string(st), statePrefix) {
				t.Errorf("state = %q", st)
			}
			m1, m3 := delta.Added[0], delta.Added[2]
			if m1.Ref != r1 || m1.Header.MessageID != "msg1@example.org" || m1.Header.Subject != "Message 1" {
				t.Errorf("m1 = %+v", m1)
			}
			if len(m1.Header.From) != 1 || m1.Header.From[0] != (domain.Address{Name: "Alice", Email: "alice@example.org"}) {
				t.Errorf("from = %+v", m1.Header.From)
			}
			if m1.Header.Date.IsZero() || m1.Size == 0 || m1.ReceivedAt.IsZero() || m1.HasAttachments {
				t.Errorf("m1 meta = %+v", m1)
			}
			if !hasFlag(delta.Added[1].Flags, domain.FlagSeen) || !hasFlag(delta.Added[1].Flags, "$label1") {
				t.Errorf("m2 flags = %v", delta.Added[1].Flags)
			}
			if m3.Ref != r3 || !m3.HasAttachments || m3.Header.ThreadIndex != "AdlXyz123==" ||
				strings.Join(m3.Header.References, ",") != "root@example.org,msg1@example.org" ||
				strings.Join(m3.Header.InReplyTo, ",") != "msg1@example.org" {
				t.Errorf("m3 = %+v", m3.Header)
			}

			// no changes
			delta, st2, err := p.Sync(c, "INBOX", st)
			if err != nil {
				t.Fatal(err)
			}
			if len(delta.Added)+len(delta.Removed)+len(delta.FlagsChanged) != 0 || delta.FullResync {
				t.Fatalf("idle delta = %+v", delta)
			}

			// external changes: flag r1, remove $label1 from r2, delete r3, add r4
			if err := other.SetFlags(c, []domain.RemoteRef{r1}, []domain.Flag{domain.FlagSeen, domain.FlagFlagged}, nil); err != nil {
				t.Fatal(err)
			}
			if err := other.SetFlags(c, []domain.RemoteRef{r2}, nil, []domain.Flag{"$label1"}); err != nil {
				t.Fatal(err)
			}
			if err := other.Delete(c, []domain.RemoteRef{r3}); err != nil {
				t.Fatal(err)
			}
			r4 := appendMsg(t, other, "INBOX", testMessage(4, "References: <msg1@example.org>\r\n"))

			delta, st3, err := p.Sync(c, "INBOX", st2)
			if err != nil {
				t.Fatal(err)
			}
			if got := addedIDs(delta.Added); strings.Join(got, ",") != r4.ID {
				t.Errorf("added = %v, want %s", got, r4.ID)
			}
			if got := ids(delta.Removed); strings.Join(got, ",") != r3.ID {
				t.Errorf("removed = %v, want %s", got, r3.ID)
			}
			if got := addedIDs(delta.FlagsChanged); strings.Join(got, ",") != strings.Join(ids([]domain.RemoteRef{r1, r2}), ",") {
				t.Errorf("flagsChanged = %v", got)
			}
			for _, m := range delta.FlagsChanged {
				if m.Ref == r1 && (!hasFlag(m.Flags, domain.FlagFlagged) || !hasFlag(m.Flags, domain.FlagSeen)) {
					t.Errorf("r1 flags = %v", m.Flags)
				}
				if m.Ref == r2 && hasFlag(m.Flags, "$label1") {
					t.Errorf("r2 flags = %v", m.Flags)
				}
			}

			// state is stable again
			delta, _, err = p.Sync(c, "INBOX", st3)
			if err != nil {
				t.Fatal(err)
			}
			if len(delta.Added)+len(delta.Removed)+len(delta.FlagsChanged) != 0 {
				t.Fatalf("delta after catch-up = %+v", delta)
			}
		})
	}
}

func TestSyncUIDValidityChangeAndBadState(t *testing.T) {
	ts := newTestServer(t, capsFull)
	p := ts.provider(t)
	c := ctx(t)
	if _, err := p.CreateFolder(c, "", "Box"); err != nil {
		t.Fatal(err)
	}
	appendMsg(t, p, "Box", testMessage(1, ""))
	_, st, err := p.Sync(c, "Box", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteFolder(c, "Box"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateFolder(c, "", "Box"); err != nil {
		t.Fatal(err)
	}
	appendMsg(t, p, "Box", testMessage(2, ""))
	delta, _, err := p.Sync(c, "Box", st)
	if err != nil {
		t.Fatal(err)
	}
	if !delta.FullResync || len(delta.Added) != 1 || delta.Added[0].Header.MessageID != "msg2@example.org" {
		t.Errorf("delta = %+v", delta)
	}

	delta, _, err = p.Sync(c, "Box", "garbage")
	if err != nil {
		t.Fatal(err)
	}
	if !delta.FullResync || len(delta.Added) != 1 {
		t.Errorf("bad state delta = %+v", delta)
	}
}

func TestMove(t *testing.T) {
	for name, caps := range map[string]imap.CapSet{"move+uidplus": capsFull, "fallback": capsMinimal} {
		t.Run(name, func(t *testing.T) {
			ts := newTestServer(t, caps)
			p := ts.provider(t)
			c := ctx(t)
			if _, err := p.CreateFolder(c, "", "Archive"); err != nil {
				t.Fatal(err)
			}
			appendMsg(t, p, "Archive", testMessage(9, "")) // occupy UID 1 in Archive
			r1 := appendMsg(t, p, "INBOX", testMessage(1, ""))
			r2 := appendMsg(t, p, "INBOX", testMessage(2, ""))
			r3 := appendMsg(t, p, "INBOX", testMessage(3, ""))
			_, inboxState, err := p.Sync(c, "INBOX", "")
			if err != nil {
				t.Fatal(err)
			}
			_, archState, err := p.Sync(c, "Archive", "")
			if err != nil {
				t.Fatal(err)
			}

			newRefs, err := p.Move(c, []domain.RemoteRef{r3, r1}, "Archive")
			if err != nil {
				t.Fatal(err)
			}
			if len(newRefs) != 2 || newRefs[0].FolderRemoteID != "Archive" || newRefs[0].ID == "" || newRefs[1].ID == "" {
				t.Fatalf("new refs = %+v", newRefs)
			}
			// order preserved: r3 -> newRefs[0]
			body, err := p.FetchRaw(c, newRefs[0])
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(body)
			if !bytes.Contains(b, []byte("msg3@Example.ORG")) {
				t.Errorf("newRefs[0] is not message 3: %q", b)
			}

			delta, _, err := p.Sync(c, "INBOX", inboxState)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(ids(delta.Removed), ",") != strings.Join(ids([]domain.RemoteRef{r1, r3}), ",") || len(delta.Added) != 0 {
				t.Errorf("inbox delta = %+v", delta)
			}
			delta, _, err = p.Sync(c, "Archive", archState)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(addedIDs(delta.Added), ",") != strings.Join(ids(newRefs), ",") {
				t.Errorf("archive added = %v, want %v", addedIDs(delta.Added), ids(newRefs))
			}
			_ = r2

			// moving into the same folder is a no-op
			same, err := p.Move(c, []domain.RemoteRef{r2}, "INBOX")
			if err != nil || same[0] != r2 {
				t.Errorf("same-folder move = %+v, %v", same, err)
			}
		})
	}
}

func TestDeleteKeepsForeignDeletedWithoutUIDPlus(t *testing.T) {
	ts := newTestServer(t, capsMinimal)
	p := ts.provider(t)
	c := ctx(t)
	r1 := appendMsg(t, p, "INBOX", testMessage(1, ""))
	r2 := appendMsg(t, p, "INBOX", testMessage(2, ""))
	// r1 is flagged \Deleted by someone else and must not be expunged
	if err := p.SetFlags(c, []domain.RemoteRef{r1}, []domain.Flag{domain.FlagDeleted}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(c, []domain.RemoteRef{r2}); err != nil {
		t.Fatal(err)
	}
	delta, _, err := p.Sync(c, "INBOX", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Added) != 2 {
		t.Errorf("expected both messages to remain (flagged), got %d", len(delta.Added))
	}

	// With UIDPLUS only the given UID is expunged.
	ts2 := newTestServer(t, capsFull)
	p2 := ts2.provider(t)
	a := appendMsg(t, p2, "INBOX", testMessage(1, ""), domain.FlagDeleted)
	b := appendMsg(t, p2, "INBOX", testMessage(2, ""))
	if err := p2.Delete(c, []domain.RemoteRef{b}); err != nil {
		t.Fatal(err)
	}
	delta, _, err = p2.Sync(c, "INBOX", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Added) != 1 || delta.Added[0].Ref != a {
		t.Errorf("delta = %+v", delta.Added)
	}
}

func TestAppendAndFetchRaw(t *testing.T) {
	for name, caps := range map[string]imap.CapSet{"uidplus": capsFull, "no-uidplus": capsMinimal} {
		t.Run(name, func(t *testing.T) {
			ts := newTestServer(t, caps)
			p := ts.provider(t)
			c := ctx(t)
			appendMsg(t, p, "INBOX", testMessage(1, ""))
			raw := testMessage(2, "")
			r := appendMsg(t, p, "INBOX", raw, domain.FlagSeen, domain.FlagDraft)
			if r.ID != "2" || r.FolderRemoteID != "INBOX" {
				t.Errorf("ref = %+v", r)
			}
			rc, err := p.FetchRaw(c, r)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			b, _ := io.ReadAll(rc)
			if string(b) != raw {
				t.Errorf("raw mismatch:\n%q\n%q", b, raw)
			}
			// FetchRaw must not set \Seen (BODY.PEEK)
			delta, _, _ := p.Sync(c, "INBOX", "")
			if hasFlag(delta.Added[0].Flags, domain.FlagSeen) {
				t.Error("FetchRaw marked message as seen")
			}
			if !hasFlag(delta.Added[1].Flags, domain.FlagDraft) {
				t.Errorf("append flags = %v", delta.Added[1].Flags)
			}
			if _, err := p.FetchRaw(c, domain.RemoteRef{FolderRemoteID: "INBOX", ID: "99"}); !errors.Is(err, ErrMessageNotFound) {
				t.Errorf("missing message err = %v", err)
			}
			if _, err := p.FetchRaw(c, domain.RemoteRef{FolderRemoteID: "INBOX", ID: "x"}); err == nil {
				t.Error("expected error for invalid ref")
			}
		})
	}
}

func TestContextCancellationReconnects(t *testing.T) {
	ts := newTestServer(t, capsFull)
	p := ts.provider(t)
	if err := p.Connect(ctx(t)); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.ListFolders(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := p.ListFolders(ctx(t)); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
	// server restart: stale connection is detected and replaced
	ts.restart()
	p.mu.Lock()
	p.lastUsed = time.Now().Add(-time.Hour)
	p.mu.Unlock()
	if _, err := p.ListFolders(ctx(t)); err != nil {
		t.Fatalf("after restart: %v", err)
	}
}

func waitHint(t *testing.T, ch <-chan domain.ChangeHint, what string) {
	t.Helper()
	select {
	case h, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed", what)
		}
		if h.FolderRemoteID != "INBOX" {
			t.Errorf("%s: hint = %+v", what, h)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no hint", what)
	}
}

func drain(ch <-chan domain.ChangeHint) {
	for {
		select {
		case <-ch:
		case <-time.After(150 * time.Millisecond):
			return
		}
	}
}

func TestWatchIdle(t *testing.T) {
	ts := newTestServer(t, capsFull)
	p := ts.provider(t)
	other := ts.provider(t)
	wctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Watch(wctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let IDLE start
	appendMsg(t, other, "INBOX", testMessage(1, ""))
	waitHint(t, ch, "append")

	// reconnect after server restart yields a hint
	drain(ch)
	ts.restart()
	waitHint(t, ch, "reconnect")
	drain(ch)
	appendMsg(t, other, "INBOX", testMessage(2, ""))
	waitHint(t, ch, "append after reconnect")

	cancel()
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after cancel")
	}

	if _, err := p.Watch(ctx(t), "NoSuchFolder"); err == nil {
		t.Error("expected error for unknown folder")
	}
}

func TestWatchNoopFallback(t *testing.T) {
	ts := newTestServer(t, capsFull)
	cfg := ts.config()
	cfg.DisableIdle = true
	cfg.PollInterval = 50 * time.Millisecond
	p := ts.providerWith(t, cfg)
	other := ts.provider(t)
	ch, err := p.Watch(ctx(t), "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	appendMsg(t, other, "INBOX", testMessage(1, ""))
	waitHint(t, ch, "noop poll")
}
