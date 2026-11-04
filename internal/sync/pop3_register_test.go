// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	pop3prov "github.com/Midas1989/Mailclient/internal/providers/pop3"
	"github.com/Midas1989/Mailclient/internal/secrets"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// miniPOP3 is a plaintext POP3 server with a fixed message list.
type miniPOP3 struct {
	ln   net.Listener
	mu   stdsync.Mutex
	msgs [][2]string // uidl, raw
}

func newMiniPOP3(t *testing.T) *miniPOP3 {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &miniPOP3{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return s
}

func (s *miniPOP3) handle(c net.Conn) {
	defer c.Close()
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	out := func(f string, a ...any) { fmt.Fprintf(w, f+"\r\n", a...); w.Flush() }
	s.mu.Lock()
	msgs := append([][2]string(nil), s.msgs...)
	s.mu.Unlock()
	out("+OK hi")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
		verb = strings.ToUpper(verb)
		var n int
		fmt.Sscan(arg, &n)
		switch verb {
		case "CAPA":
			out("+OK\r\nUSER\r\nUIDL\r\nTOP\r\n.")
		case "USER", "PASS", "NOOP":
			out("+OK")
		case "UIDL", "LIST":
			out("+OK")
			for i, m := range msgs {
				if verb == "UIDL" {
					out("%d %s", i+1, m[0])
				} else {
					out("%d %d", i+1, len(m[1]))
				}
			}
			out(".")
		case "TOP", "RETR":
			if n < 1 || n > len(msgs) {
				out("-ERR no such message")
				continue
			}
			body := msgs[n-1][1]
			if verb == "TOP" {
				body, _, _ = strings.Cut(body, "\r\n\r\n")
				body += "\r\n\r\n"
			}
			out("+OK\r\n%s.", body)
		case "QUIT":
			out("+OK")
			return
		default:
			out("-ERR")
		}
	}
}

func TestPOP3AccountLocalFolders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	srv := newMiniPOP3(t)
	srv.mu.Lock()
	srv.msgs = [][2]string{{"u1", "From: bob@example.org\r\nSubject: Hi\r\nMessage-ID: <p1@x>\r\nDate: Mon, 2 Nov 2026 09:00:00 +0000\r\n\r\nbody\r\n"}}
	srv.mu.Unlock()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, _ := blob.New(filepath.Join(dir, "blobs"))
	sec := secrets.NewMemory()
	_ = sec.Set("s", []byte("pw"))
	port := srv.ln.Addr().(*net.TCPAddr).Port
	accID, err := st.CreateAccount(ctx, domain.Account{Kind: domain.KindPOP3, DisplayName: "Pop", SecretRef: "s", Enabled: true,
		Incoming: &domain.ServerConfig{Host: "127.0.0.1", Port: port, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	if !DefaultRegistry().Supports(domain.KindPOP3) {
		t.Fatal("pop3 not registered")
	}
	m, err := New(Config{Store: st, Blobs: blobs, Secrets: sec, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: time.Minute, FolderListInterval: 50 * time.Millisecond, MinBackoff: 20 * time.Millisecond,
		MaxBackoff: 100 * time.Millisecond, OpBackoff: func(int) time.Duration { return 0 }, MaintenanceDelay: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	wait := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; i < 400; i++ {
			if cond() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timeout: %s", what)
	}
	var msg store.Message
	wait("message", func() bool {
		var err error
		msg, err = st.FindMessageByMessageID(ctx, accID, "p1@x")
		return err == nil
	})
	fid, err := m.Actions().CreateFolder(ctx, accID, 0, "Projekte")
	if err != nil {
		t.Fatal(err)
	}
	wait("folder created", func() bool {
		f, err := st.GetFolder(ctx, fid)
		return err == nil && f.RemoteID == domain.LocalFolderPrefix+"Projekte"
	})
	if err := m.Actions().Move(ctx, []int64{msg.ID}, fid); err != nil {
		t.Fatal(err)
	}
	inLocal := func() bool {
		locs, _ := st.Locations(ctx, msg.ID)
		return len(locs) == 1 && locs[0].FolderID == fid && locs[0].RemoteRef == "u1"
	}
	wait("moved to local folder", inLocal)
	// Several folder list refreshes later the local folder and message are still there.
	time.Sleep(300 * time.Millisecond)
	m.SyncNow(accID)
	time.Sleep(200 * time.Millisecond)
	if !inLocal() {
		t.Fatal("local folder content lost after resync")
	}
	if _, err := st.FolderByRemoteID(ctx, accID, pop3prov.InboxID); err != nil {
		t.Fatal(err)
	}
}
