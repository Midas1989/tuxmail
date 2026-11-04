// SPDX-License-Identifier: AGPL-3.0-or-later

// Package maildir reads and writes Maildir and Maildir++ directories
// (Dovecot, mutt, notmuch). Flags are taken from the "info" part of the file
// name (":2,DFPRST"); messages in new/ are unread.
//
// Sub-folders are read in both layouts: Maildir++ (".Archive.2025/" below
// the root) and nested directories that are maildirs themselves
// (Dovecot LAYOUT=fs). The writer uses Maildir++.
package maildir

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// MaxMessageSize limits a single message.
const MaxMessageSize = 256 << 20

// Source reads a maildir tree.
type Source struct {
	ie.ProblemList
	root string
}

// Open returns a source for the maildir at root.
func Open(root string) (*Source, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, ie.Corrupt("%s is not a directory", root)
	}
	return &Source{root: root}, nil
}

// IsMaildir reports whether dir has a cur/ or new/ sub-directory.
func IsMaildir(dir string) bool {
	return isDir(filepath.Join(dir, "cur")) || isDir(filepath.Join(dir, "new"))
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

type mdir struct {
	path   string
	folder []string
}

// folders lists the maildirs of the tree.
func (s *Source) folders() ([]mdir, error) {
	var out []mdir
	var walk func(dir string, folder []string, depth int) error
	walk = func(dir string, folder []string, depth int) error {
		if IsMaildir(dir) {
			out = append(out, mdir{path: dir, folder: folder})
		}
		if depth > 16 {
			return nil
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range ents {
			name := e.Name()
			if !e.IsDir() || name == "cur" || name == "new" || name == "tmp" {
				continue
			}
			sub := filepath.Join(dir, name)
			switch {
			case strings.HasPrefix(name, ".") && len(name) > 1 && depth == 0:
				// Maildir++: ".A.B" is folder A/B.
				var f []string
				for _, p := range strings.Split(name[1:], ".") {
					if p != "" {
						f = append(f, p)
					}
				}
				if IsMaildir(sub) {
					out = append(out, mdir{path: sub, folder: append(append([]string{}, folder...), f...)})
				}
			case strings.HasPrefix(name, "."):
			default:
				if err := walk(sub, append(append([]string{}, folder...), name), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(s.root, nil, 0); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i].folder, "/") < strings.Join(out[j].folder, "/") })
	return out, nil
}

// ParseInfo returns the flags encoded in a maildir file name.
func ParseInfo(name string) (ie.Flags, bool) {
	var f ie.Flags
	i := strings.LastIndexAny(name, ":;!")
	if i < 0 || !strings.HasPrefix(name[i+1:], "2,") {
		return f, false
	}
	for _, c := range name[i+3:] {
		switch c {
		case 'D':
			f.Draft = true
		case 'F':
			f.Flagged = true
		case 'P':
			f.Forwarded = true
		case 'R':
			f.Answered = true
		case 'S':
			f.Seen = true
		case 'T':
			f.Deleted = true
		}
	}
	return f, true
}

// Info returns the ":2,..." suffix for flags (letters in ASCII order).
func Info(f ie.Flags) string {
	s := "2,"
	if f.Draft {
		s += "D"
	}
	if f.Flagged {
		s += "F"
	}
	if f.Forwarded {
		s += "P"
	}
	if f.Answered {
		s += "R"
	}
	if f.Seen {
		s += "S"
	}
	if f.Deleted {
		s += "T"
	}
	return s
}

// Walk implements importexport.Source.
func (s *Source) Walk(ctx context.Context, fn func(ie.Message) error) error {
	dirs, err := s.folders()
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return ie.Corrupt("%s contains no maildir (cur/ or new/)", s.root)
	}
	for _, d := range dirs {
		for _, sub := range []string{"cur", "new"} {
			ents, err := os.ReadDir(filepath.Join(d.path, sub))
			if err != nil {
				continue
			}
			names := make([]string, 0, len(ents))
			for _, e := range ents {
				if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			for _, name := range names {
				if err := ctx.Err(); err != nil {
					return err
				}
				p := filepath.Join(d.path, sub, name)
				raw, err := readLimited(p)
				if err == nil && !ie.LooksLikeMessage(raw) {
					err = ie.Corrupt("not an RFC 5322 message")
				}
				if err != nil {
					s.Add(p, err)
					continue
				}
				flags, _ := ParseInfo(name)
				if sub == "new" {
					flags.Seen = false
				}
				var recv time.Time
				if st, err := os.Stat(p); err == nil {
					recv = st.ModTime()
				}
				if err := fn(ie.Message{Raw: raw, Folder: d.folder, Flags: flags, Received: recv}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func readLimited(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxMessageSize+1))
	if err == nil && len(raw) > MaxMessageSize {
		return nil, fmt.Errorf("maildir: message larger than %d bytes", MaxMessageSize)
	}
	return raw, err
}

// ==== export ===================================================================

// Writer writes a Maildir++ tree.
type Writer struct {
	root string
	made map[string]bool
}

// NewWriter writes below root (the INBOX maildir).
func NewWriter(root string) (*Writer, error) {
	w := &Writer{root: root, made: map[string]bool{}}
	return w, w.ensure(root)
}

func (w *Writer) ensure(dir string) error {
	if w.made[dir] {
		return nil
	}
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	w.made[dir] = true
	return nil
}

// FolderDir returns the Maildir++ directory of a folder path.
func FolderDir(root string, folder []string) string {
	if len(folder) == 0 || (len(folder) == 1 && strings.EqualFold(folder[0], "INBOX")) {
		return root
	}
	parts := make([]string, len(folder))
	for i, f := range folder {
		parts[i] = strings.ReplaceAll(ie.SafeName(f), ".", "_")
	}
	return filepath.Join(root, "."+strings.Join(parts, "."))
}

var seq atomic.Uint64

// infoSep is ":" except on Windows, where ":" is not allowed in file names.
func infoSep() string {
	if runtime.GOOS == "windows" {
		return "!"
	}
	return ":"
}

func uniqueName(t time.Time) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	host, _ := os.Hostname()
	host = strings.NewReplacer("/", "_", ":", "_", "!", "_").Replace(host)
	if host == "" {
		host = "tuxmail"
	}
	return fmt.Sprintf("%d.M%dP%dQ%d_%s.%s", t.Unix(), t.Nanosecond()/1000, os.Getpid(), seq.Add(1), hex.EncodeToString(b), host)
}

// Write implements importexport.Sink.
func (w *Writer) Write(_ context.Context, m ie.Message) error {
	dir := FolderDir(w.root, m.Folder)
	if err := w.ensure(dir); err != nil {
		return err
	}
	t := m.Received
	if t.IsZero() {
		t = time.Now()
	}
	name := uniqueName(t)
	tmp := filepath.Join(dir, "tmp", name)
	if err := os.WriteFile(tmp, m.Raw, 0o600); err != nil {
		return err
	}
	dst := filepath.Join(dir, "cur", name+infoSep()+Info(m.Flags))
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	if !m.Received.IsZero() {
		_ = os.Chtimes(dst, m.Received, m.Received)
	}
	return nil
}

// Close implements importexport.Sink.
func (w *Writer) Close() error { return nil }
