// SPDX-License-Identifier: AGPL-3.0-or-later

package pop3

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// ==== fake server ==============================================================

type fakeMsg struct {
	uidl string
	raw  string
}

type fakeServer struct {
	t       *testing.T
	ln      net.Listener
	tlsCfg  *tls.Config
	mu      sync.Mutex
	msgs    []fakeMsg
	user    string
	pass    string
	noTop   bool
	stls    bool
	logins  int
	cmds    []string
	tlsUsed bool
}

func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pop.test"},
		DNSNames: []string{"pop.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func newFakeServer(t *testing.T) (*fakeServer, *x509.CertPool) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg, pool := selfSigned(t)
	s := &fakeServer{t: t, ln: ln, tlsCfg: cfg, user: "anna", pass: "secret", stls: true}
	go s.serve()
	t.Cleanup(func() { ln.Close() })
	return s, pool
}

func (s *fakeServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) add(uidl, raw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, fakeMsg{uidl, raw})
}

func (s *fakeServer) uidls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, m := range s.msgs {
		out = append(out, m.uidl)
	}
	return out
}

func (s *fakeServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *fakeServer) handle(nc net.Conn) {
	defer nc.Close()
	r, w := bufio.NewReader(nc), bufio.NewWriter(nc)
	out := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\r\n", args...)
		w.Flush()
	}
	out("+OK fake POP3 ready")
	authed, user := false, ""
	s.mu.Lock()
	snapshot := append([]fakeMsg(nil), s.msgs...)
	s.mu.Unlock()
	deleted := map[int]bool{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)
		s.mu.Lock()
		s.cmds = append(s.cmds, verb)
		s.mu.Unlock()
		num := func() (int, bool) {
			n, err := strconv.Atoi(strings.Fields(arg + " x")[0])
			if err != nil || n < 1 || n > len(snapshot) || deleted[n] {
				out("-ERR no such message")
				return 0, false
			}
			return n, true
		}
		switch verb {
		case "CAPA":
			out("+OK")
			w.WriteString("USER\r\nUIDL\r\nSASL PLAIN XOAUTH2\r\n")
			if !s.noTop {
				w.WriteString("TOP\r\n")
			}
			s.mu.Lock()
			stls := s.stls
			s.mu.Unlock()
			if stls {
				w.WriteString("STLS\r\n")
			}
			out(".")
		case "STLS":
			out("+OK begin TLS")
			tc := tls.Server(nc, s.tlsCfg)
			if err := tc.Handshake(); err != nil {
				return
			}
			nc = tc
			r, w = bufio.NewReader(tc), bufio.NewWriter(tc)
			s.mu.Lock()
			s.tlsUsed = true
			s.mu.Unlock()
		case "USER":
			user = arg
			out("+OK")
		case "PASS":
			if user == s.user && arg == s.pass {
				authed = true
				s.mu.Lock()
				s.logins++
				s.mu.Unlock()
				out("+OK logged in")
			} else {
				out("-ERR invalid login")
			}
		case "AUTH":
			mech, ir, _ := strings.Cut(arg, " ")
			b, _ := base64.StdEncoding.DecodeString(ir)
			switch strings.ToUpper(mech) {
			case "PLAIN":
				parts := strings.Split(string(b), "\x00")
				if len(parts) == 3 && parts[1] == s.user && parts[2] == s.pass {
					authed = true
					s.mu.Lock()
					s.logins++
					s.mu.Unlock()
					out("+OK")
				} else {
					out("-ERR auth failed")
				}
			case "XOAUTH2":
				if strings.Contains(string(b), "auth=Bearer good-token") {
					authed = true
					out("+OK")
				} else {
					out("+ eyJzdGF0dXMiOiI0MDEifQ==")
					r.ReadString('\n')
					out("-ERR auth failed")
				}
			default:
				out("-ERR unsupported")
			}
		case "QUIT":
			s.mu.Lock()
			var keep []fakeMsg
			gone := map[string]bool{}
			for i, m := range snapshot {
				if deleted[i+1] {
					gone[m.uidl] = true
				}
			}
			for _, m := range s.msgs {
				if !gone[m.uidl] {
					keep = append(keep, m)
				}
			}
			s.msgs = keep
			s.mu.Unlock()
			out("+OK bye")
			return
		default:
			if !authed {
				out("-ERR not authenticated")
				continue
			}
			switch verb {
			case "NOOP":
				out("+OK")
			case "STAT":
				out("+OK %d 0", len(snapshot))
			case "LIST":
				out("+OK")
				for i, m := range snapshot {
					if !deleted[i+1] {
						out("%d %d", i+1, len(m.raw))
					}
				}
				out(".")
			case "UIDL":
				out("+OK")
				for i, m := range snapshot {
					if !deleted[i+1] {
						out("%d %s", i+1, m.uidl)
					}
				}
				out(".")
			case "TOP":
				if s.noTop {
					out("-ERR unknown command")
					continue
				}
				if n, ok := num(); ok {
					hdr, _, _ := strings.Cut(snapshot[n-1].raw, "\r\n\r\n")
					out("+OK")
					writeStuffed(w, hdr+"\r\n\r\n")
					out(".")
				}
			case "RETR":
				if n, ok := num(); ok {
					out("+OK")
					writeStuffed(w, snapshot[n-1].raw)
					out(".")
				}
			case "DELE":
				if n, ok := num(); ok {
					deleted[n] = true
					out("+OK deleted")
				}
			default:
				out("-ERR unknown command")
			}
		}
	}
}

func writeStuffed(w *bufio.Writer, raw string) {
	for _, l := range strings.Split(strings.TrimSuffix(raw, "\r\n"), "\r\n") {
		if strings.HasPrefix(l, ".") {
			l = "." + l
		}
		w.WriteString(l + "\r\n")
	}
}

func testMail(id, subject string) string {
	return "Received: from mx by pop.test; Mon, 2 Nov 2026 10:00:00 +0000\r\n" +
		"From: Bob <bob@example.org>\r\nTo: anna@example.org\r\nSubject: " + subject + "\r\n" +
		"Date: Mon, 2 Nov 2026 09:59:00 +0000\r\nMessage-ID: <" + id + "@example.org>\r\n" +
		"Content-Type: text/plain\r\n\r\nHello\r\n.dotted line\r\n"
}

// ==== tests ====================================================================

type memLocal struct {
	mu     sync.Mutex
	copies map[string]bool
}

func (m *memLocal) LocalFolders(context.Context) ([]domain.Folder, error) {
	return []domain.Folder{{RemoteID: "local/Projects", Name: "Projects"}}, nil
}

func (m *memLocal) HasLocalCopy(_ context.Context, uidl string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.copies[uidl], nil
}

func newProvider(t *testing.T, s *fakeServer, pool *x509.CertPool, mod func(*Config)) *Provider {
	t.Helper()
	cfg := Config{
		Server:    domain.ServerConfig{Host: "127.0.0.1", Port: s.port(), TLS: domain.TLSStartTLS, Username: "anna", Auth: domain.AuthPassword},
		Password:  "secret",
		TLSConfig: &tls.Config{RootCAs: pool, ServerName: "pop.test"},
		Policy:    Policy{LeaveOnServer: true},
	}
	if mod != nil {
		mod(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestSyncFetchLeaveOnServer(t *testing.T) {
	s, pool := newFakeServer(t)
	s.add("u1", testMail("m1", "First"))
	s.add("u2", testMail("m2", "Second"))
	p := newProvider(t, s, pool, nil)
	ctx := context.Background()

	d, st, err := p.Sync(ctx, InboxID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 2 || d.Added[0].Ref.ID != "u1" || d.Added[0].Header.Subject != "First" ||
		d.Added[0].Header.MessageID != "m1@example.org" || d.Added[0].Header.From[0].Email != "bob@example.org" {
		t.Fatalf("added = %+v", d.Added)
	}
	s.mu.Lock()
	tlsUsed := s.tlsUsed
	s.mu.Unlock()
	if !tlsUsed {
		t.Error("STLS not used")
	}
	if d.Added[0].ReceivedAt.IsZero() || d.Added[0].Size == 0 {
		t.Errorf("received/size missing: %+v", d.Added[0])
	}
	rc, err := p.FetchRaw(ctx, domain.RemoteRef{FolderRemoteID: InboxID, ID: "u2"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	if !strings.Contains(string(raw), "\r\n.dotted line\r\n") || !strings.Contains(string(raw), "Subject: Second") {
		t.Fatalf("raw = %q", raw)
	}

	// Incremental: one new, one removed elsewhere.
	s.mu.Lock()
	s.msgs = s.msgs[1:]
	s.mu.Unlock()
	s.add("u3", testMail("m3", "Third"))
	d, st2, err := p.Sync(ctx, InboxID, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 || d.Added[0].Ref.ID != "u3" || len(d.Removed) != 0 {
		t.Fatalf("delta = %+v", d)
	}
	if strings.Contains(string(st2), "u1") {
		t.Errorf("state keeps vanished id: %s", st2)
	}
	// Leave on server: nothing deleted.
	if err := p.Delete(ctx, []domain.RemoteRef{{FolderRemoteID: InboxID, ID: "u2"}}); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if got := s.uidls(); len(got) != 2 {
		t.Fatalf("server = %v", got)
	}
}

func TestNoTopFallbackAndCache(t *testing.T) {
	s, pool := newFakeServer(t)
	s.noTop = true
	s.add("a", testMail("a", "No top"))
	p := newProvider(t, s, pool, func(c *Config) { c.Server.Auth = domain.AuthPassword })
	ctx := context.Background()
	d, _, err := p.Sync(ctx, InboxID, "")
	if err != nil || len(d.Added) != 1 || d.Added[0].Header.Subject != "No top" {
		t.Fatal(err, d)
	}
	ncmds := func() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.cmds) }
	before := ncmds()
	rc, err := p.FetchRaw(ctx, d.Added[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if ncmds() != before {
		t.Errorf("cached message was downloaded again")
	}
}

func TestDeletePolicies(t *testing.T) {
	ctx := context.Background()
	t.Run("delete on local delete", func(t *testing.T) {
		s, pool := newFakeServer(t)
		s.add("x", testMail("x", "X"))
		s.add("y", testMail("y", "Y"))
		p := newProvider(t, s, pool, func(c *Config) { c.Policy = Policy{LeaveOnServer: true, DeleteOnLocalDelete: true} })
		if _, _, err := p.Sync(ctx, InboxID, ""); err != nil {
			t.Fatal(err)
		}
		if err := p.Delete(ctx, []domain.RemoteRef{{FolderRemoteID: "local/Trash", ID: "x"}, {ID: "l-123"}}); err != nil {
			t.Fatal(err)
		}
		if got := s.uidls(); len(got) != 1 || got[0] != "y" {
			t.Fatalf("server = %v", got)
		}
	})
	t.Run("delete after days needs a local copy", func(t *testing.T) {
		s, pool := newFakeServer(t)
		s.add("old", testMail("old", "Old"))
		s.add("kept", testMail("kept", "Kept"))
		now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		local := &memLocal{copies: map[string]bool{"old": true}}
		p := newProvider(t, s, pool, func(c *Config) {
			c.Policy = Policy{LeaveOnServer: true, DeleteAfterDays: 7}
			c.Local = local
			c.Now = func() time.Time { return now }
		})
		_, st, err := p.Sync(ctx, InboxID, "")
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(8 * 24 * time.Hour)
		if _, _, err := p.Sync(ctx, InboxID, st); err != nil {
			t.Fatal(err)
		}
		p.Close()
		if got := s.uidls(); len(got) != 1 || got[0] != "kept" {
			t.Fatalf("server = %v (kept has no local copy and must stay)", got)
		}
	})
	t.Run("no leave deletes after fetch", func(t *testing.T) {
		s, pool := newFakeServer(t)
		s.add("z", testMail("z", "Z"))
		p := newProvider(t, s, pool, func(c *Config) { c.Policy = Policy{LeaveOnServer: false} })
		_, st, err := p.Sync(ctx, InboxID, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(s.uidls()) != 1 {
			t.Fatal("deleted before a local copy existed")
		}
		rc, err := p.FetchRaw(ctx, domain.RemoteRef{FolderRemoteID: InboxID, ID: "z"})
		if err != nil {
			t.Fatal(err)
		}
		rc.Close()
		if _, _, err := p.Sync(ctx, InboxID, st); err != nil {
			t.Fatal(err)
		}
		if got := s.uidls(); len(got) != 0 {
			t.Fatalf("server = %v", got)
		}
	})
}

func TestFoldersMoveAppend(t *testing.T) {
	s, pool := newFakeServer(t)
	p := newProvider(t, s, pool, func(c *Config) { c.Local = &memLocal{} })
	ctx := context.Background()
	fs, err := p.ListFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]domain.FolderRole{}
	for _, f := range fs {
		ids[f.RemoteID] = f.Role
	}
	if ids[InboxID] != domain.RoleInbox || ids["local/Trash"] != domain.RoleTrash || ids["local/Sent"] != domain.RoleSent {
		t.Fatalf("folders = %+v", fs)
	}
	if _, ok := ids["local/Projects"]; !ok {
		t.Fatal("store folder missing")
	}
	f, err := p.CreateFolder(ctx, "local/Projects", "Alpha")
	if err != nil || f.RemoteID != "local/Projects/Alpha" {
		t.Fatal(err, f)
	}
	refs, err := p.Move(ctx, []domain.RemoteRef{{FolderRemoteID: InboxID, ID: "u1"}}, f.RemoteID)
	if err != nil || refs[0].FolderRemoteID != f.RemoteID || refs[0].ID != "u1" {
		t.Fatal(err, refs)
	}
	if _, err := p.Move(ctx, refs, "Elsewhere"); err == nil {
		t.Fatal("move to unknown folder accepted")
	}
	r, err := p.Append(ctx, "local/Sent", strings.NewReader("x"), nil)
	if err != nil || !strings.HasPrefix(r.ID, localRefPrefix) {
		t.Fatal(err, r)
	}
	if _, err := p.Append(ctx, InboxID, strings.NewReader("x"), nil); err == nil {
		t.Fatal("append to inbox accepted")
	}
	if _, err := p.FetchRaw(ctx, r); err != ErrMessageNotFound {
		t.Fatal(err)
	}
	d, st, err := p.Sync(ctx, "local/Sent", "")
	if err != nil || len(d.Added) != 0 || st == "" {
		t.Fatal(err, d, st)
	}
}

func TestAuthFailuresAndOAuth(t *testing.T) {
	s, pool := newFakeServer(t)
	ctx := context.Background()
	p := newProvider(t, s, pool, func(c *Config) { c.Password = "wrong" })
	if _, _, err := p.Sync(ctx, InboxID, ""); err == nil {
		t.Fatal("login with wrong password succeeded")
	}
	p2 := newProvider(t, s, pool, func(c *Config) {
		c.Server.Auth = domain.AuthOAuth2
		c.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "good-token", nil })
	})
	if _, _, err := p2.Sync(ctx, InboxID, ""); err != nil {
		t.Fatal(err)
	}
	p3 := newProvider(t, s, pool, func(c *Config) {
		c.Server.Auth = domain.AuthOAuth2
		c.TokenSource = TokenSourceFunc(func(context.Context) (string, error) { return "bad", nil })
	})
	if _, _, err := p3.Sync(ctx, InboxID, ""); err == nil {
		t.Fatal("bad token accepted")
	}
	// Plain connection refused when TLS is required but STLS missing.
	s.mu.Lock()
	s.stls = false
	s.mu.Unlock()
	p4 := newProvider(t, s, pool, nil)
	if _, _, err := p4.Sync(ctx, InboxID, ""); err == nil {
		t.Fatal("connected without STLS")
	}
}

func TestPolicyFromExtra(t *testing.T) {
	p := PolicyFromExtra(map[string]string{ExtraDeleteAfterDays: "14", ExtraDeleteOnLocalDelete: "true"})
	if !p.LeaveOnServer || p.DeleteAfterDays != 14 || !p.DeleteOnLocalDelete {
		t.Fatal(p)
	}
	if PolicyFromExtra(map[string]string{ExtraLeaveOnServer: "false"}).LeaveOnServer {
		t.Fatal("leave not parsed")
	}
}

func TestWatchTicks(t *testing.T) {
	s, pool := newFakeServer(t)
	p := newProvider(t, s, pool, func(c *Config) { c.PollInterval = 10 * time.Millisecond })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := p.Watch(ctx, InboxID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case h := <-ch:
		if h.FolderRemoteID != InboxID {
			t.Fatal(h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no hint")
	}
}
