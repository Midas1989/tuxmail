// SPDX-License-Identifier: AGPL-3.0-or-later

package instance

import (
	"errors"
	"testing"
	"time"
)

func TestLockExcludesAndReleases(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire: %v", err)
	}
	if _, err := AcquireWait(dir, 300*time.Millisecond); !errors.Is(err, ErrHeld) {
		t.Fatalf("wait: %v", err)
	}
	l.Release()
	l.Release() // idempotent
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()
}
