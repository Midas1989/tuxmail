// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// stallIMAP is a scripted server that answers normally except for the
// commands in hang (no answer at all, connection stays open). LIST answers
// with `trickle` folder lines, one every 40 ms (a slow but alive server).
func stallIMAP(t *testing.T, hang map[string]bool, trickle int) Config {
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
				fmt.Fprint(conn, "* OK [CAPABILITY IMAP4rev1 AUTH=PLAIN] ready\r\n")
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
					if hang[cmd] {
						continue // never answer
					}
					switch cmd {
					case "CAPABILITY":
						fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1 AUTH=PLAIN\r\n%s OK done\r\n", tag)
					case "LIST":
						for i := range trickle {
							time.Sleep(40 * time.Millisecond)
							fmt.Fprintf(conn, "* LIST () \"/\" \"F%d\"\r\n", i)
						}
						fmt.Fprintf(conn, "* LIST () \"/\" \"INBOX\"\r\n%s OK done\r\n", tag)
					case "SELECT", "EXAMINE":
						fmt.Fprintf(conn, "* 0 EXISTS\r\n* OK [UIDVALIDITY 1] ok\r\n* OK [UIDNEXT 1] ok\r\n%s OK done\r\n", tag)
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
		Server:         domain.ServerConfig{Host: host, Port: pn, TLS: domain.TLSNone, Auth: domain.AuthPassword, Username: "u"},
		Password:       "p",
		CommandTimeout: 200 * time.Millisecond,
	}
}

func newStallProvider(t *testing.T, cfg Config) *Provider {
	t.Helper()
	cfg.DialContext = (&net.Dialer{}).DialContext // loopback is fine in tests
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func expectStalled(t *testing.T, err error, took time.Duration) {
	t.Helper()
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("err = %v, want ErrStalled", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("ErrStalled must be a net.Error with Timeout() (retried as a network error)")
	}
	if took > 3*time.Second {
		t.Fatalf("took %v, the guard should fire after ~200ms", took)
	}
}

func TestStalledCommandIsAborted(t *testing.T) {
	p := newStallProvider(t, stallIMAP(t, map[string]bool{"LIST": true}, 0))
	start := time.Now()
	_, err := p.ListFolders(context.Background())
	expectStalled(t, err, time.Since(start))

	// The broken connection was dropped: the provider is usable again (here
	// the next attempt stalls again, but it does not hang either).
	start = time.Now()
	_, err = p.ListFolders(context.Background())
	expectStalled(t, err, time.Since(start))
}

func TestStalledLoginIsAborted(t *testing.T) {
	p := newStallProvider(t, stallIMAP(t, map[string]bool{"AUTHENTICATE": true, "LOGIN": true}, 0))
	start := time.Now()
	err := p.Connect(context.Background())
	expectStalled(t, err, time.Since(start))
}

func TestSlowButAliveServerIsNotAborted(t *testing.T) {
	// 15 lines, 40 ms apart = 600 ms in total, three times the timeout; the
	// gaps are far below it.
	p := newStallProvider(t, stallIMAP(t, nil, 15))
	folders, err := p.ListFolders(context.Background())
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if len(folders) < 16 {
		t.Fatalf("got %d folders, want 16", len(folders))
	}
}

func TestStalledWatchSelectIsAborted(t *testing.T) {
	p := newStallProvider(t, stallIMAP(t, map[string]bool{"SELECT": true, "EXAMINE": true}, 0))
	start := time.Now()
	_, err := p.Watch(context.Background(), "INBOX")
	expectStalled(t, err, time.Since(start))
}

func TestGuardStallDisarmed(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	ac := newActivityConn(a)
	stop := guardStall(ac, 50*time.Millisecond)
	if stop() {
		t.Fatal("guard fired although it was disarmed at once")
	}
	time.Sleep(120 * time.Millisecond)
	if err := ac.SetReadDeadline(time.Now()); err != nil { // fails on a closed pipe
		t.Fatalf("a disarmed guard closed the connection: %v", err)
	}
	ac.Close()
}
