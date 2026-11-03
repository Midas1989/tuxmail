// SPDX-License-Identifier: AGPL-3.0-or-later

package sync

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	imapprov "github.com/Midas1989/Mailclient/internal/providers/imap"
)

var (
	// ErrNotImplemented is returned by actions that need a backend that is
	// not available yet (e.g. sending without a Sender).
	ErrNotImplemented = errors.New("sync: not implemented")
	// ErrUnsupportedKind is returned when no provider is registered for an
	// account kind.
	ErrUnsupportedKind = errors.New("sync: unsupported account kind")
	// ErrNoCredentials is returned when an account's secret is missing.
	ErrNoCredentials = errors.New("sync: no credentials for account")
	// ErrNotRunning is returned by Manager methods before Start or after Stop.
	ErrNotRunning = errors.New("sync: manager not running")
	// ErrOperationPending is returned when a message cannot be acted upon
	// because an earlier operation on it has not reached the server yet.
	ErrOperationPending = errors.New("sync: an earlier operation on this message is still pending")
)

// PermanentError marks an error that must not be retried.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err so that operations fail without retry.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err must not be retried.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p) || errors.Is(err, imapprov.ErrMessageNotFound) || errors.Is(err, ErrUnsupportedKind)
}

// IsNetworkError reports whether err looks like a connectivity problem
// (offline, server unreachable, connection dropped) after which the
// operation should be retried once the connection is back, without counting
// as a failed attempt.
func IsNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"connection closed", "use of closed network connection", "broken pipe",
		"connection reset", "connection refused", "i/o timeout", "no route to host", "network is unreachable", "eof"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// Backoff computes exponential retry delays with full jitter in
// [d/2, d), doubling from Min up to Max.
type Backoff struct {
	Min, Max time.Duration
	cur      time.Duration
}

// Next returns the next delay and advances the backoff.
func (b *Backoff) Next() time.Duration {
	if b.cur <= 0 {
		b.cur = b.Min
	}
	d := b.cur
	b.cur = min(b.cur*2, b.Max)
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(rand.Int64N(int64(half)))
}

// Reset starts over at Min.
func (b *Backoff) Reset() { b.cur = 0 }
