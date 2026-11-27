// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultCommandTimeout is how long a command may run without any data
// moving on the connection before the connection is considered stalled.
const DefaultCommandTimeout = 3 * time.Minute

// ErrStalled is returned when the server stopped responding to a command:
// nothing was received or sent for Config.CommandTimeout. go-imap only bounds
// the time inside a response that has started, not the wait for the first
// byte, so without this a hung server would block the account forever.
var ErrStalled error = stalledError{}

type stalledError struct{}

func (stalledError) Error() string   { return "imap: server stopped responding (command timed out)" }
func (stalledError) Timeout() bool   { return true }
func (stalledError) Temporary() bool { return true }

var _ net.Error = stalledError{}

// activityConn records when data last moved in either direction. Both
// directions count: a large APPEND only writes, a long FETCH only reads.
type activityConn struct {
	net.Conn
	last atomic.Int64 // unix nanoseconds
}

func newActivityConn(c net.Conn) *activityConn {
	a := &activityConn{Conn: c}
	a.touch()
	return a
}

func (a *activityConn) touch() { a.last.Store(time.Now().UnixNano()) }

func (a *activityConn) Read(b []byte) (int, error) {
	n, err := a.Conn.Read(b)
	if n > 0 {
		a.touch()
	}
	return n, err
}

func (a *activityConn) Write(b []byte) (int, error) {
	n, err := a.Conn.Write(b)
	if n > 0 {
		a.touch()
	}
	return n, err
}

// stallGuard closes the connection when no data moves for d while a command
// runs. The idle time before the guard starts does not count.
type stallGuard struct {
	mu      sync.Mutex
	stopped bool
	fired   bool
	done    chan struct{}
}

// guardStall arms a guard on conn. The returned function disarms it and
// reports whether it closed the connection. A nil conn or d <= 0 disables
// the guard.
func guardStall(conn *activityConn, d time.Duration) (stop func() (fired bool)) {
	if conn == nil || d <= 0 {
		return func() bool { return false }
	}
	g := &stallGuard{done: make(chan struct{})}
	start := time.Now().UnixNano()
	tick := max(d/8, 5*time.Millisecond)
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-g.done:
				return
			case now := <-t.C:
				last := max(start, conn.last.Load())
				if now.UnixNano()-last < int64(d) {
					continue
				}
				g.mu.Lock()
				if !g.stopped {
					g.fired = true
					conn.Close() // unblocks the reader; the pending command fails
				}
				g.mu.Unlock()
				return
			}
		}
	}()
	return func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		if !g.stopped {
			g.stopped = true
			close(g.done)
		}
		return g.fired
	}
}
