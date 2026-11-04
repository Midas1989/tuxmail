// SPDX-License-Identifier: AGPL-3.0-or-later

package pst

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

func TestNotPST(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.pst")
	os.WriteFile(p, []byte("not a pst file"), 0o600)
	if _, err := Open(p); !errors.Is(err, ie.ErrCorrupt) {
		t.Fatal(err)
	}
	// Valid magic, damaged content.
	os.WriteFile(p, append([]byte("!BDN"), make([]byte, 600)...), 0o600)
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Walk(context.Background(), func(ie.Message) error { return nil }); err == nil {
		t.Fatal("damaged PST must fail")
	}
}

// TestSamplePST reads the sample file shipped with go-pst (module cache).
func TestSamplePST(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/mooijtech/go-pst/v6").Output()
	if err != nil {
		t.Skip("go-pst module directory not found")
	}
	p := filepath.Join(strings.TrimSpace(string(out)), "data", "support.pst")
	if _, err := os.Stat(p); err != nil {
		t.Skip("sample PST not available")
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	err = s.Walk(context.Background(), func(m ie.Message) error {
		n++
		if !ie.LooksLikeMessage(m.Raw) {
			t.Fatalf("message %d is not RFC 5322:\n%s", n, m.Raw[:min(200, len(m.Raw))])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no messages")
	}
	t.Logf("%d messages, %d other items, %d problems", n, s.Skipped(), len(s.Problems()))
}
