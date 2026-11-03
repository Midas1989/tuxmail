// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// condstoreServer scripts a CONDSTORE-capable server (go-imap's memory
// server has no CONDSTORE support). Mailbox: UIDVALIDITY 7, UIDs 1,3,4,
// HIGHESTMODSEQ modseq; UID 3 changed at modseq 110.
type condstoreServer struct {
	modseq   int
	mu       sync.Mutex
	commands []string
}

func (s *condstoreServer) serve(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
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
	return ln.Addr().String()
}

func (s *condstoreServer) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	w := func(format string, args ...any) { fmt.Fprintf(c, format+"\r\n", args...) }
	const caps = "IMAP4rev1 CONDSTORE UIDPLUS IDLE"
	w("* OK [CAPABILITY %s] ready", caps)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		tag, cmd, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		s.mu.Lock()
		s.commands = append(s.commands, cmd)
		s.mu.Unlock()
		up := strings.ToUpper(cmd)
		switch {
		case up == "CAPABILITY":
			w("* CAPABILITY %s", caps)
			w("%s OK done", tag)
		case strings.HasPrefix(up, "LOGIN "):
			w("%s OK [CAPABILITY %s] logged in", tag, caps)
		case strings.HasPrefix(up, "SELECT "):
			w("* FLAGS (\\Seen \\Flagged \\Deleted)")
			w("* 3 EXISTS")
			w("* OK [UIDVALIDITY 7] ok")
			w("* OK [UIDNEXT 5] ok")
			w("* OK [HIGHESTMODSEQ %d] ok", s.modseq)
			w("%s OK [READ-WRITE] selected", tag)
		case strings.HasPrefix(up, "UID SEARCH"):
			w("* SEARCH 1 3 4")
			w("%s OK done", tag)
		case strings.HasPrefix(up, "UID FETCH 4 "):
			hdr := "Message-ID: <m4@Example.org>\r\nReferences: <m1@example.org>\r\n\r\n"
			fmt.Fprintf(c, "* 3 FETCH (UID 4 FLAGS (\\Seen) RFC822.SIZE 120 INTERNALDATE \"02-Nov-2026 10:00:00 +0100\" "+
				"ENVELOPE (\"Mon, 02 Nov 2026 10:00:00 +0100\" \"Hi\" ((\"Alice\" NIL \"alice\" \"example.org\")) NIL NIL "+
				"((NIL NIL \"bob\" \"example.org\")) NIL NIL NIL \"<m4@example.org>\") "+
				"BODYSTRUCTURE (\"TEXT\" \"PLAIN\" (\"CHARSET\" \"utf-8\") NIL NIL \"7BIT\" 5 1 NIL NIL NIL NIL) "+
				"BODY[HEADER.FIELDS (MESSAGE-ID IN-REPLY-TO REFERENCES THREAD-INDEX DATE)] {%d}\r\n%s)\r\n", len(hdr), hdr)
			w("%s OK done", tag)
		case strings.HasPrefix(up, "UID FETCH 1:3 ") && strings.Contains(up, "CHANGEDSINCE 100"):
			w("* 2 FETCH (UID 3 FLAGS (\\Flagged) MODSEQ (110))")
			w("%s OK done", tag)
		case up == "LOGOUT":
			w("* BYE")
			w("%s OK bye", tag)
			return
		case up == "NOOP":
			w("%s OK done", tag)
		default:
			w("%s BAD unexpected %q", tag, cmd)
		}
	}
}

func TestSyncCondStore(t *testing.T) {
	srv := &condstoreServer{modseq: 120}
	addr := srv.serve(t)
	host, port, _ := net.SplitHostPort(addr)
	p, err := New(Config{
		Server:   domain.ServerConfig{Host: host, Port: atoi(port), TLS: domain.TLSNone, Username: "alice"},
		Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	prev := (&folderState{UIDValidity: 7, UIDNext: 4, ModSeq: 100, UIDs: []uint32{1, 2, 3}}).encode()
	delta, next, err := p.Sync(ctx, "INBOX", prev)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Capabilities().CondStore {
		t.Error("CONDSTORE not detected")
	}
	if len(delta.Added) != 1 || delta.Added[0].Ref.ID != "4" || delta.Added[0].Header.MessageID != "m4@example.org" ||
		strings.Join(delta.Added[0].Header.References, ",") != "m1@example.org" || delta.Added[0].Header.Subject != "Hi" {
		t.Errorf("added = %+v", delta.Added)
	}
	if len(delta.Removed) != 1 || delta.Removed[0].ID != "2" {
		t.Errorf("removed = %+v", delta.Removed)
	}
	if len(delta.FlagsChanged) != 1 || delta.FlagsChanged[0].Ref.ID != "3" || !hasFlag(delta.FlagsChanged[0].Flags, domain.FlagFlagged) {
		t.Errorf("flagsChanged = %+v", delta.FlagsChanged)
	}
	st, err := decodeState(next)
	if err != nil {
		t.Fatal(err)
	}
	if st.ModSeq != 120 || st.UIDNext != 5 || st.Flags != nil || fmt.Sprint(st.UIDs) != "[1 3 4]" {
		t.Errorf("next state = %+v", st)
	}

	// Unchanged HIGHESTMODSEQ/UIDNEXT/EXISTS: no SEARCH, no FETCH at all.
	srv.mu.Lock()
	srv.commands = nil
	srv.mu.Unlock()
	delta, _, err = p.Sync(ctx, "INBOX", next)
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Added)+len(delta.Removed)+len(delta.FlagsChanged) != 0 {
		t.Errorf("delta = %+v", delta)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for _, c := range srv.commands {
		if strings.Contains(c, "SEARCH") || strings.Contains(c, "FETCH") {
			t.Errorf("unexpected command %q", c)
		}
	}
}
