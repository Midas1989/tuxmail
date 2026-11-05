// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type bombEntry struct {
	name string
	size int64 // declared (and written) size, zero filled
	list *FileEntry
}

// craftBomb writes an archive whose entries are zero filled and compress to a
// few KiB (NC-03).
func craftBomb(t *testing.T, server bool, entries ...bombEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := newWriter(&buf, "")
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Format: FormatVersion, ID: newID(), Created: time.Now(), Server: server}
	for _, e := range entries {
		if e.list != nil {
			m.Files = append(m.Files, *e.list)
		}
	}
	mj, _ := json.Marshal(m)
	if err := w.bytes(manifestName, m.Created, mj); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := w.file(e.name, m.Created, e.size, io.LimitReader(zeroReader{}, e.size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dirBytes(t *testing.T, dir string) (n int64) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func zeroSum(size int64) string {
	h := sha256.New()
	_, _ = io.Copy(h, io.LimitReader(zeroReader{}, size))
	return hex.EncodeToString(h.Sum(nil))
}

const bombBlob = blobPrefix + "00/00/0000000000000000000000000000000000000000000000000000000000000001"

// NC-03: the audit PoC (one blob entry of 64 MiB zeros, 7.5 KB archive) must
// be stopped by the expansion bound long before the entry is written.
func TestExtractStopsDecompressionBomb(t *testing.T) {
	arc := craftBomb(t, false, bombEntry{name: bombBlob, size: 64 << 20})
	if len(arc) > 64<<10 {
		t.Fatalf("test archive is %d bytes; the bomb is not compressing", len(arc))
	}
	tmp := t.TempDir()
	_, err := Extract(context.Background(), src(arc, "bomb"), "", nil, tmp)
	if !errors.Is(err, ErrTooLarge) || !errors.Is(err, ErrTampered) {
		t.Fatalf("err = %v, want ErrTooLarge (and ErrTampered)", err)
	}
	if n := dirBytes(t, tmp); n != 0 {
		t.Fatalf("%d bytes left in the temp dir after the failure", n)
	}
}

func TestExtractBombWrittenBytesAreBounded(t *testing.T) {
	// Observe what reaches the disk before the failure: Extract removes its
	// directory, so drive extractOne directly.
	arc := craftBomb(t, false, bombEntry{name: bombBlob, size: 256 << 20})
	dir := t.TempDir()
	_, err := extractOne(context.Background(), src(arc, "bomb"), "", dir, true)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	limit := ExpansionSlack + MaxExpansionRatio*int64(len(arc)) + 2<<20
	if n := dirBytes(t, dir); n > limit {
		t.Fatalf("%d bytes written for a %d byte archive, limit %d", n, len(arc), limit)
	}
}

// NC-03: entries that declare more than the cap are rejected before a file
// is created.
func TestExtractRejectsOversizedEntryBeforeWriting(t *testing.T) {
	old := MaxBlobEntryBytes
	MaxBlobEntryBytes = 1024
	t.Cleanup(func() { MaxBlobEntryBytes = old })
	arc := craftBomb(t, false, bombEntry{name: bombBlob, size: 4096})
	dir := t.TempDir()
	_, err := extractOne(context.Background(), src(arc, "big"), "", dir, true)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if n := dirBytes(t, dir); n != 0 {
		t.Fatalf("%d bytes written before the size check", n)
	}
}

func TestExtractTotalBound(t *testing.T) {
	old := MaxExtractBytes
	MaxExtractBytes = 1 << 20
	t.Cleanup(func() { MaxExtractBytes = old })
	// Manifest alone declares more than the total.
	e := &FileEntry{Path: dbName, Size: 2 << 20, SHA256: zeroSum(2 << 20)}
	arc := craftBomb(t, false, bombEntry{name: dbName, size: 2 << 20, list: e})
	dir := t.TempDir()
	if _, err := extractOne(context.Background(), src(arc, "big"), "", dir, true); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if n := dirBytes(t, dir); n != 0 {
		t.Fatalf("%d bytes written", n)
	}
	// Several entries that fit individually but not together (a base
	// archive: its non-blob entries are read and discarded, but still count).
	var es []bombEntry
	for i := range 4 {
		es = append(es, bombEntry{name: "db/part" + string(rune('a'+i)), size: 400 << 10})
	}
	arc = craftBomb(t, false, es...)
	if _, err := extractOne(context.Background(), src(arc, "many"), "", t.TempDir(), false); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge for the total", err)
	}
}

// The header size of a regular file must match the manifest before anything
// is written.
func TestExtractHeaderSizeMustMatchManifest(t *testing.T) {
	e := &FileEntry{Path: dbName, Size: 10, SHA256: zeroSum(10)}
	arc := craftBomb(t, false, bombEntry{name: dbName, size: 4096, list: e})
	dir := t.TempDir()
	_, err := extractOne(context.Background(), src(arc, "lie"), "", dir, true)
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("err = %v, want ErrTampered", err)
	}
	if n := dirBytes(t, dir); n != 0 {
		t.Fatalf("%d bytes written", n)
	}
}

// RestoreServer applies the same bounds.
func TestRestoreServerStopsDecompressionBomb(t *testing.T) {
	name := serverPrefix + "tuxmail.db"
	size := int64(64 << 20)
	e := &FileEntry{Path: name, Size: size, SHA256: zeroSum(size)}
	arc := craftBomb(t, true, bombEntry{name: name, size: size, list: e})
	parent := t.TempDir()
	_, err := RestoreServer(context.Background(), src(arc, "bomb"), "", filepath.Join(parent, "data"))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if n := dirBytes(t, parent); n != 0 {
		t.Fatalf("%d bytes left behind", n)
	}

	// Header size differing from the manifest is refused before writing.
	e2 := &FileEntry{Path: name, Size: 10, SHA256: zeroSum(10)}
	arc = craftBomb(t, true, bombEntry{name: name, size: 4096, list: e2})
	if _, err := RestoreServer(context.Background(), src(arc, "lie"), "", filepath.Join(t.TempDir(), "data")); !errors.Is(err, ErrTampered) {
		t.Fatalf("err = %v, want ErrTampered", err)
	}

	// A normal small server archive still restores.
	good := []byte("hello server")
	sum := sha256.Sum256(good)
	var buf bytes.Buffer
	w, _ := newWriter(&buf, "")
	m := Manifest{Format: FormatVersion, ID: newID(), Created: time.Now(), Server: true,
		Files: []FileEntry{{Path: name, Size: int64(len(good)), SHA256: hex.EncodeToString(sum[:])}}}
	mj, _ := json.Marshal(m)
	_ = w.bytes(manifestName, m.Created, mj)
	_ = w.bytes(name, m.Created, good)
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreServer(context.Background(), src(buf.Bytes(), "ok"), "", filepath.Join(t.TempDir(), "data")); err != nil {
		t.Fatalf("regular server restore: %v", err)
	}
}
