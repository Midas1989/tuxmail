// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// hostileIMAP is a minimal scripted IMAP server for audit NF-04 tests. onFetch
// writes the answer to a UID FETCH (without the tagged status line); the
// server then sends "<tag> OK".
func hostileIMAP(t *testing.T, exists int, onFetch func(w io.Writer)) Config {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				fmt.Fprint(conn, "* OK hostile ready\r\n")
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					f := strings.Fields(line)
					if len(f) < 2 {
						continue
					}
					tag, cmd := f[0], strings.ToUpper(f[1])
					if cmd == "UID" && len(f) > 2 {
						cmd = "UID " + strings.ToUpper(f[2])
					}
					switch cmd {
					case "CAPABILITY":
						fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1 AUTH=PLAIN\r\n%s OK done\r\n", tag)
					case "LOGIN", "AUTHENTICATE":
						fmt.Fprintf(conn, "%s OK logged in\r\n", tag)
					case "SELECT", "EXAMINE":
						fmt.Fprintf(conn, "* %d EXISTS\r\n* OK [UIDVALIDITY 1] ok\r\n* OK [UIDNEXT %d] ok\r\n%s OK [READ-WRITE] done\r\n", exists, exists+1, tag)
					case "UID SEARCH":
						fmt.Fprintf(conn, "* SEARCH 1\r\n%s OK done\r\n", tag)
					case "UID FETCH":
						onFetch(conn)
						fmt.Fprintf(conn, "%s OK done\r\n", tag)
					case "LOGOUT":
						fmt.Fprintf(conn, "* BYE\r\n%s OK bye\r\n", tag)
						return
					default:
						fmt.Fprintf(conn, "%s OK done\r\n", tag)
					}
				}
			}()
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	var pn int
	fmt.Sscan(port, &pn)
	return Config{
		Server:   domain.ServerConfig{Host: host, Port: pn, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "u"},
		Password: "p",
	}
}

// streamLiteral writes n filler bytes; it stops quietly when the client hangs up.
func streamLiteral(w io.Writer, n int) {
	chunk := []byte(strings.Repeat("X-Filler: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\r\n", 512))
	for n > 0 {
		k := min(n, len(chunk))
		if _, err := w.Write(chunk[:k]); err != nil {
			return
		}
		n -= k
	}
}

// NF-04: a header FETCH literal larger than the section limit is refused while
// it is read; the connection is dropped instead of draining the literal.
func TestSyncHugeHeaderLiteralRefused(t *testing.T) {
	size := maxFetchSection + (1 << 20)
	cfg := hostileIMAP(t, 1, func(w io.Writer) {
		fmt.Fprintf(w, "* 1 FETCH (UID 1 FLAGS () RFC822.SIZE 100 BODY[HEADER.FIELDS (Message-ID In-Reply-To References Thread-Index Date)] {%d}\r\n", size)
		streamLiteral(w, size)
		fmt.Fprint(w, ")\r\n")
	})
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	start := time.Now()
	_, _, err = p.Sync(ctx(t), "INBOX", "")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v", d)
	}
}

// A well-behaved header literal below the limit still works.
func TestSyncSmallHeaderLiteralAccepted(t *testing.T) {
	hdr := "Message-ID: <a@b>\r\nDate: Mon, 02 Jan 2006 15:04:05 +0000\r\n\r\n"
	cfg := hostileIMAP(t, 1, func(w io.Writer) {
		fmt.Fprintf(w, "* 1 FETCH (UID 1 FLAGS () RFC822.SIZE 100 BODY[HEADER.FIELDS (Message-ID In-Reply-To References Thread-Index Date)] {%d}\r\n%s)\r\n", len(hdr), hdr)
	})
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	delta, _, err := p.Sync(ctx(t), "INBOX", "")
	if err != nil || len(delta.Added) != 1 || delta.Added[0].Header.MessageID != "a@b" {
		t.Fatalf("delta = %+v, err = %v", delta, err)
	}
}

// NF-04: a flood of FETCH responses for one command is cut off.
func TestCollectFetchMessageCountBounded(t *testing.T) {
	cfg := hostileIMAP(t, 1, func(w io.Writer) {
		for i := 0; i < 5000; i++ {
			if _, err := fmt.Fprintf(w, "* %d FETCH (UID %d FLAGS (\\Seen))\r\n", i+1, i+1); err != nil {
				return
			}
		}
	})
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	err = p.do(ctx(t), func(c *imapclient.Client) error {
		_, err := collectFetch(c, c.Fetch(imap.UIDSet{imap.UIDRange{Start: 1, Stop: 5000}}, &imap.FetchOptions{UID: true, Flags: true}), maxFetchSection, maxFetchTotal, 10)
		return err
	})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
}

// NF-04: FetchRaw does not buffer a literal beyond the configured limit.
func TestFetchRawSizeLimit(t *testing.T) {
	ts := newTestServer(t, capsFull)
	cfg := ts.config()
	cfg.MaxMessageSize = 1 << 10
	p := ts.providerWith(t, cfg)
	c := ctx(t)
	small := appendMsg(t, p, "INBOX", testMessage(1, ""))
	big := appendMsg(t, p, "INBOX", testMessage(2, strings.Repeat("A", 4<<10)))
	rc, err := p.FetchRaw(c, small)
	if err != nil {
		t.Fatalf("small message: %v", err)
	}
	rc.Close()
	if _, err := p.FetchRaw(c, big); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("big message err = %v, want ErrMessageTooLarge", err)
	}
	// The explicit limit may only lower the configured one.
	if _, err := p.FetchRawLimit(c, small, 10); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("explicit limit err = %v", err)
	}
	// The provider recovers on a fresh connection.
	rc, err = p.FetchRaw(c, small)
	if err != nil {
		t.Fatalf("after oversize: %v", err)
	}
	rc.Close()
}

// NF-04: a hostile literal announced as gigabytes is not buffered.
func TestFetchRawHostileLiteral(t *testing.T) {
	cfg := hostileIMAP(t, 1, func(w io.Writer) {
		fmt.Fprintf(w, "* 1 FETCH (UID 1 BODY[] {%d}\r\n", 3<<30)
		streamLiteral(w, 3<<20)
	})
	cfg.MaxMessageSize = 1 << 20
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	_, err = p.FetchRaw(ctx(t), domain.RemoteRef{FolderRemoteID: "INBOX", ID: "1"})
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", err)
	}
}

// NF-04: folders announcing more messages than the limit are refused.
func TestSyncFolderTooLarge(t *testing.T) {
	cfg := hostileIMAP(t, 2_000_000_000, func(io.Writer) {})
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	_, _, err = p.Sync(ctx(t), "INBOX", "")
	if !errors.Is(err, ErrFolderTooLarge) {
		t.Fatalf("err = %v, want ErrFolderTooLarge", err)
	}

	ts := newTestServer(t, capsFull)
	c2 := ts.config()
	c2.MaxFolderMessages = 2
	p2 := ts.providerWith(t, c2)
	for i := 1; i <= 3; i++ {
		appendMsg(t, p2, "INBOX", testMessage(i, ""))
	}
	if _, _, err := p2.Sync(ctx(t), "INBOX", ""); !errors.Is(err, ErrFolderTooLarge) {
		t.Fatalf("configured limit: err = %v", err)
	}
}

// NF-04: the ID connection reads lines with a bound before buffering them.
func TestReadBoundedLine(t *testing.T) {
	long := strings.Repeat("a", 5000) + "\r\n"
	if _, err := readBoundedLine(bufio.NewReader(strings.NewReader(long)), 4096); err == nil {
		t.Error("over-long line accepted")
	}
	got, err := readBoundedLine(bufio.NewReader(strings.NewReader(long)), 6000)
	if err != nil || got != long {
		t.Errorf("line = %d bytes, err = %v", len(got), err)
	}
	// An endless line without newline must fail without reading it all.
	r := bufio.NewReaderSize(io.LimitReader(zeroReader{}, 1<<30), 4096)
	if _, err := readBoundedLine(r, 1<<20); err == nil {
		t.Error("endless line accepted")
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}
