// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package app

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// NC-04: the backup download fallback (rename across file systems) must not
// leave a world-readable copy of the archive.
func TestCopyPathIsPrivate(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := copyPath(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("copy: %v mode %v", err, fi.Mode().Perm())
	}
}
