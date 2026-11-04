// SPDX-License-Identifier: AGPL-3.0-or-later

package eml

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

func collect(t *testing.T, s ie.Source) []ie.Message {
	t.Helper()
	var out []ie.Message
	if err := s.Walk(context.Background(), func(m ie.Message) error { out = append(out, m); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSingleAndDir(t *testing.T) {
	s, err := Open("../testdata/sample.eml")
	if err != nil {
		t.Fatal(err)
	}
	ms := collect(t, s)
	if len(ms) != 1 || ie.MessageID(ms[0].Raw) != "eml-1@example.org" {
		t.Fatalf("single: %+v", ms)
	}
	d, err := Open("../testdata/emldir")
	if err != nil {
		t.Fatal(err)
	}
	ms = collect(t, d)
	if len(ms) != 2 {
		t.Fatalf("dir: %d messages", len(ms))
	}
	if ms[0].FolderName() != "Archive" || ms[1].FolderName() != "" {
		t.Fatalf("folders: %q %q", ms[0].FolderName(), ms[1].FolderName())
	}
	if len(d.Problems()) != 1 {
		t.Fatalf("problems: %+v", d.Problems())
	}
}

func TestCorruptSingle(t *testing.T) {
	s, err := Open("../testdata/emldir/broken.eml")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Walk(context.Background(), func(ie.Message) error { return nil })
	if !errors.Is(err, ie.ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestZipRoundTrip(t *testing.T) {
	d, _ := Open("../testdata/emldir")
	in := collect(t, d)
	p := filepath.Join(t.TempDir(), "out.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := NewZipWriter(f)
	for _, m := range in {
		if err := zw.Write(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	z, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	out := collect(t, z)
	if len(out) != len(in) {
		t.Fatalf("round trip: %d != %d", len(out), len(in))
	}
	byID := map[string]ie.Message{}
	for _, m := range out {
		byID[ie.MessageID(m.Raw)] = m
	}
	for i, m := range in {
		o := byID[ie.MessageID(m.Raw)]
		if !bytes.Equal(m.Raw, o.Raw) || m.FolderName() != o.FolderName() {
			t.Fatalf("message %d differs", i)
		}
	}
	// Corrupt ZIP.
	bad := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(bad, []byte("PK\x03\x04garbage"), 0o600)
	if _, err := Open(bad); !errors.Is(err, ie.ErrCorrupt) {
		t.Fatalf("corrupt zip: %v", err)
	}
}
