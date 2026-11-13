// SPDX-License-Identifier: AGPL-3.0-or-later

// Package eml reads and writes single RFC 5322 messages (.eml): one file, a
// directory tree (sub-directories become folders) or a ZIP archive.
package eml

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// MaxMessageSize limits a single message (larger files are skipped).
const MaxMessageSize = 256 << 20

// Source reads .eml files.
type Source struct {
	ie.ProblemList
	path string
	kind string // file | dir | zip
	zr   *zip.Reader
}

// Open returns a source for a .eml file, a directory or a .zip archive.
func Open(p string) (*Source, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return &Source{path: p, kind: "dir"}, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	f.Close()
	if n == 4 && string(head) == "PK\x03\x04" {
		zr, err := zip.OpenReader(p)
		if err != nil {
			return nil, ie.Corrupt("zip: %v", err)
		}
		return &Source{path: p, kind: "zip", zr: &zr.Reader}, nil
	}
	return &Source{path: p, kind: "file"}, nil
}

// NewZip returns a source for a ZIP archive held by r.
func NewZip(r io.ReaderAt, size int64) (*Source, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, ie.Corrupt("zip: %v", err)
	}
	return &Source{kind: "zip", zr: zr}, nil
}

// Limits of a ZIP archive of messages (audit I-11): entries and the sum of
// the uncompressed sizes (as for unzip in the app layer).
const (
	MaxZipEntries = 200000
	MaxZipTotal   = 8 << 30
)

func isEML(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".eml" || ext == ".msg822" || ext == ".mime"
}

// Walk implements importexport.Source.
func (s *Source) Walk(ctx context.Context, fn func(ie.Message) error) error {
	switch s.kind {
	case "file":
		raw, err := readLimited(s.path)
		if err != nil {
			return err
		}
		if !ie.LooksLikeMessage(raw) {
			return ie.Corrupt("%s is not an RFC 5322 message", filepath.Base(s.path))
		}
		return fn(ie.Message{Raw: raw, Received: modTime(s.path)})
	case "dir":
		var files []string
		err := filepath.WalkDir(s.path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && isEML(d.Name()) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return err
		}
		sort.Strings(files)
		for _, p := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw, err := readLimited(p)
			if err == nil && !ie.LooksLikeMessage(raw) {
				err = ie.Corrupt("not an RFC 5322 message")
			}
			if err != nil {
				s.Add(p, err)
				continue
			}
			rel, _ := filepath.Rel(s.path, filepath.Dir(p))
			if err := fn(ie.Message{Raw: raw, Folder: splitFolder(filepath.ToSlash(rel)), Received: modTime(p)}); err != nil {
				return err
			}
		}
		return nil
	case "zip":
		if len(s.zr.File) > MaxZipEntries {
			return ie.Corrupt("zip: more than %d entries", MaxZipEntries)
		}
		files := make([]*zip.File, 0, len(s.zr.File))
		var total uint64
		for _, f := range s.zr.File {
			if !f.FileInfo().IsDir() && isEML(f.Name) && !strings.HasPrefix(path.Base(f.Name), "._") {
				if total += f.UncompressedSize64; total > MaxZipTotal {
					return ie.Corrupt("zip: content too large")
				}
				files = append(files, f)
			}
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw, err := readZip(f)
			if err == nil && !ie.LooksLikeMessage(raw) {
				err = ie.Corrupt("not an RFC 5322 message")
			}
			if err != nil {
				s.Add(f.Name, err)
				continue
			}
			if err := fn(ie.Message{Raw: raw, Folder: splitFolder(path.Dir(f.Name)), Received: f.Modified}); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("eml: unknown source")
}

func splitFolder(rel string) []string {
	if rel == "." || rel == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(rel, "/") {
		if p != "" && p != "." && p != ".." {
			out = append(out, p)
		}
	}
	return out
}

func readLimited(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxMessageSize+1))
	if err == nil && len(raw) > MaxMessageSize {
		return nil, fmt.Errorf("eml: %s is larger than %d bytes", filepath.Base(p), MaxMessageSize)
	}
	return raw, err
}

func readZip(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > MaxMessageSize {
		return nil, fmt.Errorf("eml: %s is larger than %d bytes", f.Name, MaxMessageSize)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, ie.Corrupt("zip entry %s: %v", f.Name, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, MaxMessageSize+1))
	if err != nil {
		return nil, ie.Corrupt("zip entry %s: %v", f.Name, err)
	}
	return raw, nil
}

func modTime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// ==== export ===================================================================

// ZipWriter writes messages into a ZIP archive, one .eml per message in a
// directory per folder.
type ZipWriter struct {
	zw    *zip.Writer
	n     int
	names map[string]bool
}

// NewZipWriter writes a ZIP archive to w. Close finishes the archive (it
// does not close w).
func NewZipWriter(w io.Writer) *ZipWriter {
	return &ZipWriter{zw: zip.NewWriter(w), names: map[string]bool{}}
}

// FileName returns the .eml name of the n-th message (1-based).
func FileName(n int, m ie.Message) string {
	subj := ie.HeaderValue(m.Raw, "Subject")
	if len(subj) > 60 {
		subj = subj[:60]
	}
	name := fmt.Sprintf("%05d", n)
	if subj != "" {
		name += " " + ie.SafeName(subj)
	}
	return name + ".eml"
}

func folderDir(m ie.Message) string {
	parts := make([]string, 0, len(m.Folder))
	for _, f := range m.Folder {
		parts = append(parts, ie.SafeName(f))
	}
	return path.Join(parts...)
}

// Write implements importexport.Sink.
func (z *ZipWriter) Write(_ context.Context, m ie.Message) error {
	z.n++
	name := path.Join(folderDir(m), FileName(z.n, m))
	if z.names[name] {
		name = strings.TrimSuffix(name, ".eml") + fmt.Sprintf("-%d.eml", z.n)
	}
	z.names[name] = true
	mod := m.Received
	if mod.IsZero() {
		mod = ie.ParseDate(ie.HeaderValue(m.Raw, "Date"))
	}
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mod}
	w, err := z.zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = w.Write(m.Raw)
	return err
}

// Close implements importexport.Sink.
func (z *ZipWriter) Close() error { return z.zw.Close() }

// DirWriter writes .eml files into a directory tree.
type DirWriter struct {
	root string
	n    int
}

// NewDirWriter writes into root (created if needed).
func NewDirWriter(root string) (*DirWriter, error) {
	return &DirWriter{root: root}, os.MkdirAll(root, 0o700)
}

// Write implements importexport.Sink.
func (d *DirWriter) Write(_ context.Context, m ie.Message) error {
	d.n++
	dir := filepath.Join(d.root, filepath.FromSlash(folderDir(m)))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dir, FileName(d.n, m))
	if err := os.WriteFile(p, m.Raw, 0o600); err != nil {
		return err
	}
	if !m.Received.IsZero() {
		_ = os.Chtimes(p, m.Received, m.Received)
	}
	return nil
}

// Close implements importexport.Sink.
func (d *DirWriter) Close() error { return nil }
