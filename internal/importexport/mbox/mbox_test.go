// SPDX-License-Identifier: AGPL-3.0-or-later

package mbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

func walk(t *testing.T, s ie.Source) []ie.Message {
	t.Helper()
	var out []ie.Message
	if err := s.Walk(context.Background(), func(m ie.Message) error { out = append(out, m); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadThunderbirdMbox(t *testing.T) {
	f, err := os.Open("../testdata/sample.mbox")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ms := walk(t, NewReader(f, MboxO, []string{"Inbox"}))
	if len(ms) != 2 {
		t.Fatalf("got %d messages", len(ms))
	}
	if !ms[0].Flags.Seen || ms[0].Flags.Flagged || !ms[1].Flags.Flagged || !ms[1].Flags.Seen {
		t.Fatalf("flags: %+v %+v", ms[0].Flags, ms[1].Flags)
	}
	body := string(ms[0].Raw)
	if strings.Contains(body, "X-Mozilla-Status") {
		t.Fatal("state header not removed")
	}
	if !strings.Contains(body, "\nFrom the quoted line") || !strings.Contains(body, "\n>>From doubly quoted") {
		t.Fatalf("mboxo unquoting wrong:\n%s", body)
	}
	if ms[0].Received.IsZero() || ms[0].Received.Day() != 2 {
		t.Fatalf("received: %v", ms[0].Received)
	}
}

func TestCorrupt(t *testing.T) {
	f, _ := os.Open("../testdata/corrupt.mbox")
	defer f.Close()
	err := NewReader(f, MboxRD, nil).Walk(context.Background(), func(ie.Message) error { return nil })
	if !errors.Is(err, ie.ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestTree(t *testing.T) {
	tr := OpenTree("../testdata/tbprofile/Mail/Local Folders", MboxO)
	ms := walk(t, tr)
	if len(ms) != 4 {
		t.Fatalf("got %d", len(ms))
	}
	folders := map[string]int{}
	for _, m := range ms {
		folders[m.FolderName()]++
	}
	if folders["Inbox"] != 2 || folders["Inbox/Projects"] != 2 {
		t.Fatalf("folders %v", folders)
	}
}

func TestRoundTrip(t *testing.T) {
	in := []ie.Message{
		{Raw: []byte("From: a@example.org\nSubject: x\nMessage-ID: <1@x>\n\nFrom here\n>From quoted\nend\n"), Flags: ie.Flags{Seen: true, Answered: true}},
		{Raw: []byte("From: b@example.org\r\nSubject: y\r\n\r\nno newline"), Flags: ie.Flags{Flagged: true, Forwarded: true}},
	}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, m := range in {
		if err := w.Write(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	out := walk(t, NewReader(&buf, MboxRD, nil))
	if len(out) != 2 {
		t.Fatalf("got %d\n%s", len(out), buf.String())
	}
	norm := func(b []byte) string {
		return strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	}
	for i := range in {
		if norm(in[i].Raw) != norm(out[i].Raw) {
			t.Fatalf("message %d:\n%q\n%q", i, norm(in[i].Raw), norm(out[i].Raw))
		}
		if in[i].Flags != out[i].Flags {
			t.Fatalf("flags %d: %+v != %+v", i, in[i].Flags, out[i].Flags)
		}
	}
	// Tree writer + reader.
	dir := t.TempDir()
	tw, _ := NewTreeWriter(dir)
	tw.Write(context.Background(), ie.Message{Raw: in[0].Raw, Folder: []string{"Archive", "2025"}})
	tw.Write(context.Background(), ie.Message{Raw: in[1].Raw})
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Archive.sbd", "2025.mbox")); err != nil {
		t.Fatal(err)
	}
	got := walk(t, OpenTree(dir, MboxRD))
	if len(got) != 2 || got[0].FolderName() != "Archive/2025" || got[1].FolderName() != "Inbox" {
		t.Fatalf("tree: %v %v", got[0].Folder, got[1].Folder)
	}
}
