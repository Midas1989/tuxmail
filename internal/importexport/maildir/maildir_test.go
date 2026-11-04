// SPDX-License-Identifier: AGPL-3.0-or-later

package maildir

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

func TestParseInfo(t *testing.T) {
	f, ok := ParseInfo("123.M1P2.host:2,FRS")
	if !ok || !f.Flagged || !f.Answered || !f.Seen || f.Draft {
		t.Fatalf("%+v", f)
	}
	if Info(f) != "2,FRS" {
		t.Fatal(Info(f))
	}
	if _, ok := ParseInfo("plain"); ok {
		t.Fatal("no info expected")
	}
}

func TestRoundTrip(t *testing.T) {
	root := t.TempDir()
	w, err := NewWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	in := []ie.Message{
		{Raw: []byte("Subject: a\n\nA\n"), Flags: ie.Flags{Seen: true, Flagged: true}},
		{Raw: []byte("Subject: b\n\nB\n"), Folder: []string{"Archive", "2025"}, Flags: ie.Flags{Draft: true}},
	}
	for _, m := range in {
		if err := w.Write(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	// A message in new/ of the root is unread.
	os.WriteFile(filepath.Join(root, "new", "999.new.host"), []byte("Subject: c\n\nC\n"), 0o600)
	// Nested layout (Dovecot LAYOUT=fs).
	os.MkdirAll(filepath.Join(root, "Nested", "cur"), 0o700)
	os.WriteFile(filepath.Join(root, "Nested", "cur", "1.x:2,S"), []byte("Subject: d\n\nD\n"), 0o600)
	os.WriteFile(filepath.Join(root, "cur", "broken:2,"), []byte("garbage"), 0o600)

	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ie.Message{}
	err = s.Walk(context.Background(), func(m ie.Message) error {
		got[ie.HeaderValue(m.Raw, "Subject")] = m
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || len(s.Problems()) != 1 {
		t.Fatalf("got %d, problems %v", len(got), s.Problems())
	}
	if m := got["a"]; !m.Flags.Seen || !m.Flags.Flagged || m.FolderName() != "" || !bytes.Equal(m.Raw, in[0].Raw) {
		t.Fatalf("a: %+v", m)
	}
	if m := got["b"]; !m.Flags.Draft || m.FolderName() != "Archive/2025" {
		t.Fatalf("b: %+v %v", m.Flags, m.Folder)
	}
	if m := got["c"]; m.Flags.Seen {
		t.Fatal("new/ must be unread")
	}
	if m := got["d"]; m.FolderName() != "Nested" || !m.Flags.Seen {
		t.Fatalf("d: %v", m.Folder)
	}
}

func TestNotMaildir(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Walk(context.Background(), func(ie.Message) error { return nil }); !errors.Is(err, ie.ErrCorrupt) {
		t.Fatal(err)
	}
}
