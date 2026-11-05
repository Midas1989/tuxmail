// SPDX-License-Identifier: AGPL-3.0-or-later

package pop3

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// pipeConn returns a client conn whose peer is driven by serve (which runs
// after the request line was consumed).
func pipeConn(t *testing.T, serve func(w *bufio.Writer)) *conn {
	t.Helper()
	cl, sv := net.Pipe()
	t.Cleanup(func() { cl.Close(); sv.Close() })
	go func() {
		r := bufio.NewReader(sv)
		if _, err := r.ReadString('\n'); err != nil {
			return
		}
		w := bufio.NewWriterSize(sv, 64<<10)
		serve(w)
		w.Flush()
	}()
	return newConn(cl, 10*time.Second)
}

// NF-09: a UIDL answer with more lines than the cap is refused.
func TestUIDLTooManyLines(t *testing.T) {
	c := pipeConn(t, func(w *bufio.Writer) {
		w.WriteString("+OK\r\n")
		for i := 1; i <= maxListingLines+10; i++ {
			if _, err := fmt.Fprintf(w, "%d u%d\r\n", i, i); err != nil {
				return
			}
		}
		w.WriteString(".\r\n")
	})
	if _, err := c.uidlList(); err == nil || !strings.Contains(err.Error(), "lines") {
		t.Fatalf("err = %v, want line-count error", err)
	}
}

// NF-09: a LIST answer above the byte cap is refused (few, long lines).
func TestListTooManyBytes(t *testing.T) {
	line := strings.Repeat("x", 900<<10)
	c := pipeConn(t, func(w *bufio.Writer) {
		w.WriteString("+OK\r\n")
		for i := 0; i < 24; i++ {
			if _, err := fmt.Fprintf(w, "%d 10 %s\r\n", i+1, line); err != nil {
				return
			}
		}
		w.WriteString(".\r\n")
	})
	if _, err := c.list(); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want size error", err)
	}
}

// NF-09: an endless listing does not keep the client reading forever.
func TestEndlessListingDropsConnection(t *testing.T) {
	old := maxDrain
	maxDrain = 1 << 20
	t.Cleanup(func() { maxDrain = old })
	big := strings.Repeat("y", 1000)
	c := pipeConn(t, func(w *bufio.Writer) {
		w.WriteString("+OK\r\n")
		for {
			if _, err := fmt.Fprintf(w, "1 %s\r\n", big); err != nil {
				return
			}
		}
	})
	done := make(chan error, 1)
	go func() { _, err := c.uidlList(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("endless listing accepted")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("client still reading an endless listing")
	}
}

// Normal listings keep working.
func TestListingNormal(t *testing.T) {
	c := pipeConn(t, func(w *bufio.Writer) {
		io.WriteString(w, "+OK\r\n1 uid-a\r\n2 uid-b\r\n.\r\n")
	})
	m, err := c.uidlList()
	if err != nil || len(m) != 2 || m[2] != "uid-b" {
		t.Fatalf("m = %v, err = %v", m, err)
	}
}
