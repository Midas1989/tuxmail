// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingLogFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	r, err := openLogFile(dir, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n"
	for i := 0; i < 10; i++ {
		if _, err := r.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Path() != filepath.Join(dir, "tuxmail.log") {
		t.Errorf("path %s", r.Path())
	}
	for _, name := range []string{"tuxmail.log", "tuxmail.log.1", "tuxmail.log.2"} {
		st, err := os.Stat(filepath.Join(dir, name))
		if err != nil || st.Size() == 0 || st.Size() > 100 {
			t.Errorf("%s: %v %v", name, st, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "tuxmail.log.3")); err == nil {
		t.Error("more than keep rotated files")
	}
	// Reopening appends.
	r, err = openLogFile(dir, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.size == 0 {
		t.Error("reopened file not appended")
	}
}
