// SPDX-License-Identifier: AGPL-3.0-or-later

// Package instance tells whether another process runs the sync engine on a
// data directory. Operating-system file locks are used: they disappear with
// the process, so a crash never leaves a stale lock behind.
package instance

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// FileName is the lock file inside a data directory.
const FileName = "engine.lock"

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("data directory is in use by another TuxMail process")

// Lock is a held lock; Release frees it (process exit does as well).
type Lock struct{ f *os.File }

// Acquire takes the lock in dir without waiting.
func Acquire(dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{f: f}, nil
}

// AcquireWait retries Acquire until the wait is over (for services starting
// while a short command line call is active).
func AcquireWait(dir string, wait time.Duration) (*Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		l, err := Acquire(dir)
		if err == nil || !errors.Is(err, ErrHeld) || time.Now().After(deadline) {
			return l, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Release frees the lock. It is safe on a nil lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	unlockFile(l.f)
	l.f.Close()
	l.f = nil
}
