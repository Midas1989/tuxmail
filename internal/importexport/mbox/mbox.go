// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mbox reads and writes Unix mailbox files (mboxo and mboxrd), as
// used by Thunderbird, Apple Mail and Google Takeout.
//
// Messages are separated by "From " lines. On export, lines matching
// /^>*From / are quoted with one more ">" (mboxrd); on import, the quoting
// is removed according to the Variant. Client state headers (Status,
// X-Status, X-Mozilla-Status, X-Mozilla-Status2, X-Mozilla-Keys) are turned
// into flags and removed from the message.
//
// A Thunderbird mail directory ("Inbox", "Inbox.sbd/Sub", index files
// "*.msf" that are ignored) is read with OpenTree.
package mbox

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
)

// Variant selects how "From " quoting is undone.
type Variant int

const (
	// MboxRD removes one ">" from every line matching /^>+From /.
	MboxRD Variant = iota
	// MboxO removes the ">" only from lines starting with ">From ".
	MboxO
)

// MaxMessageSize limits a single message.
const MaxMessageSize = 256 << 20

// Reader reads one mbox stream.
type Reader struct {
	ie.ProblemList
	r       io.Reader
	variant Variant
	folder  []string
	name    string
}

// NewReader returns a reader for an mbox stream; folder is assigned to
// every message.
func NewReader(r io.Reader, v Variant, folder []string) *Reader {
	return &Reader{r: r, variant: v, folder: folder, name: "mbox"}
}

var fromPrefix = []byte("From ")

// Walk implements importexport.Source.
func (rd *Reader) Walk(ctx context.Context, fn func(ie.Message) error) error {
	br := bufio.NewReaderSize(rd.r, 64<<10)
	var (
		cur      bytes.Buffer
		started  bool
		received time.Time
		prevNL   = true // previous line was empty (or start of file)
		n        int
		tooLarge bool
		first    = true
	)
	flush := func() error {
		if !started {
			return nil
		}
		n++
		if tooLarge {
			rd.Add(fmt.Sprintf("%s #%d", rd.name, n), fmt.Errorf("message larger than %d bytes", MaxMessageSize))
			cur.Reset()
			tooLarge = false
			return nil
		}
		raw := bytes.TrimRight(cur.Bytes(), "\n")
		raw = append(append([]byte{}, raw...), '\n')
		cur.Reset()
		if !ie.LooksLikeMessage(raw) {
			rd.Add(fmt.Sprintf("%s #%d", rd.name, n), ie.Corrupt("no message header"))
			return nil
		}
		raw, flags := stripState(raw)
		return fn(ie.Message{Raw: raw, Folder: rd.folder, Flags: flags, Received: received})
	}
	for {
		line, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Very long line: collect the rest.
			buf := append([]byte{}, line...)
			for err == bufio.ErrBufferFull {
				line, err = br.ReadSlice('\n')
				buf = append(buf, line...)
			}
			line = buf
		}
		if len(line) == 0 && err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if first {
			first = false
			if !bytes.HasPrefix(line, fromPrefix) {
				if len(bytes.TrimSpace(line)) == 0 {
					first = true
					continue
				}
				return ie.Corrupt("%s does not start with a \"From \" line", rd.name)
			}
		}
		// Normalize CRLF.
		if l := len(line); l >= 2 && line[l-2] == '\r' && line[l-1] == '\n' {
			line = append(line[:l-2:l-2], '\n')
		}
		if prevNL && bytes.HasPrefix(line, fromPrefix) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := flush(); err != nil {
				return err
			}
			started = true
			received = parseFromLine(line)
			prevNL = false
			continue
		}
		prevNL = len(bytes.TrimRight(line, "\n")) == 0
		if !tooLarge {
			cur.Write(rd.unquote(line))
			if cur.Len() > MaxMessageSize {
				tooLarge = true
				cur.Reset()
			}
		}
		if err == io.EOF {
			break
		}
	}
	return flush()
}

func (rd *Reader) unquote(line []byte) []byte {
	if len(line) == 0 || line[0] != '>' {
		return line
	}
	i := 0
	for i < len(line) && line[i] == '>' {
		i++
	}
	if !bytes.HasPrefix(line[i:], fromPrefix) {
		return line
	}
	if rd.variant == MboxO && i != 1 {
		return line
	}
	return line[1:]
}

// parseFromLine extracts the date of "From sender Mon Jan  2 15:04:05 2006".
func parseFromLine(line []byte) time.Time {
	s := strings.TrimSpace(string(line[len(fromPrefix):]))
	if _, rest, ok := strings.Cut(s, " "); ok {
		s = strings.TrimSpace(rest)
	}
	s = strings.Join(strings.Fields(s), " ")
	for _, layout := range []string{"Mon Jan 2 15:04:05 2006", "Mon Jan 2 15:04:05 -0700 2006", "Mon Jan 2 15:04:05 MST 2006", time.RFC1123Z} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// stripState removes client state headers and returns the flags they encode.
func stripState(raw []byte) ([]byte, ie.Flags) {
	var f ie.Flags
	end := bytes.Index(raw, []byte("\n\n"))
	if end < 0 {
		end = len(raw)
	} else {
		end++ // keep the newline of the last header line
	}
	hdr := raw[:end]
	var out bytes.Buffer
	out.Grow(len(raw))
	removed := false
	lines := bytes.SplitAfter(hdr, []byte("\n"))
	skipCont := false
	for _, l := range lines {
		if skipCont && len(l) > 0 && (l[0] == ' ' || l[0] == '\t') {
			continue
		}
		skipCont = false
		k, v, ok := bytes.Cut(l, []byte(":"))
		if ok {
			key := strings.ToLower(string(k))
			val := strings.TrimSpace(string(v))
			switch key {
			case "status":
				f.Seen = f.Seen || strings.Contains(val, "R")
				removed, skipCont = true, true
				continue
			case "x-status":
				f.Answered = f.Answered || strings.Contains(val, "A")
				f.Flagged = f.Flagged || strings.Contains(val, "F")
				f.Deleted = f.Deleted || strings.Contains(val, "D")
				f.Draft = f.Draft || strings.Contains(val, "T")
				removed, skipCont = true, true
				continue
			case "x-mozilla-status":
				if n, err := strconv.ParseUint(val, 16, 32); err == nil {
					f.Seen = f.Seen || n&0x0001 != 0
					f.Answered = f.Answered || n&0x0002 != 0
					f.Flagged = f.Flagged || n&0x0004 != 0
					f.Deleted = f.Deleted || n&0x0008 != 0
					f.Forwarded = f.Forwarded || n&0x1000 != 0
				}
				removed, skipCont = true, true
				continue
			case "x-mozilla-status2", "x-mozilla-keys":
				removed, skipCont = true, true
				continue
			}
		}
		out.Write(l)
	}
	if !removed {
		return raw, f
	}
	out.Write(raw[end:])
	return out.Bytes(), f
}

// ==== Thunderbird directory trees ===========================================

// Tree reads all mbox files below a directory (Thunderbird layout).
type Tree struct {
	ie.ProblemList
	root    string
	variant Variant
}

// OpenTree returns a source for a directory of mbox files. A single file
// is accepted as well.
func OpenTree(root string, v Variant) *Tree { return &Tree{root: root, variant: v} }

// skipFile reports index and metadata files of mail clients.
func skipFile(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(name, ".") {
		return true
	}
	for _, ext := range []string{".msf", ".dat", ".json", ".html", ".sqlite", ".sqlite-wal", ".sqlite-shm", ".js", ".txt", ".log", ".bak", ".lock", ".mab", ".eml", ".ics", ".vcf", ".toc", ".plist", ".xml", ".ini"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return lower == "msgfilterrules.dat" || lower == "popstate.dat" || lower == "filterlog.html"
}

func isMbox(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 5)
	n, _ := io.ReadFull(f, head)
	return n == 5 && bytes.Equal(head, fromPrefix)
}

// Files lists the mbox files of the tree with their folder paths.
func (t *Tree) Files() ([]FileEntry, error) {
	st, err := os.Stat(t.root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []FileEntry{{Path: t.root, Folder: []string{folderName(filepath.Base(t.root))}}}, nil
	}
	var out []FileEntry
	err = filepath.WalkDir(t.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || skipFile(d.Name()) || !isMbox(p) {
			return nil
		}
		rel, _ := filepath.Rel(t.root, p)
		var folder []string
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			folder = append(folder, folderName(part))
		}
		out = append(out, FileEntry{Path: p, Folder: folder})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// FileEntry is one mbox file of a tree.
type FileEntry struct {
	Path   string
	Folder []string
}

func folderName(part string) string {
	part = strings.TrimSuffix(part, ".sbd")
	part = strings.TrimSuffix(part, ".mbox")
	part = strings.TrimSuffix(part, ".mbx")
	return part
}

// Walk implements importexport.Source.
func (t *Tree) Walk(ctx context.Context, fn func(ie.Message) error) error {
	files, err := t.Files()
	if err != nil {
		return err
	}
	for _, fe := range files {
		f, err := os.Open(fe.Path)
		if err != nil {
			t.Add(fe.Path, err)
			continue
		}
		rd := NewReader(f, t.variant, fe.Folder)
		rd.name = filepath.Base(fe.Path)
		err = rd.Walk(ctx, fn)
		f.Close()
		t.Merge(rd.Problems())
		if err != nil {
			if isCorrupt(err) {
				t.Add(fe.Path, err)
				continue
			}
			return err
		}
	}
	return nil
}

func isCorrupt(err error) bool { return errors.Is(err, ie.ErrCorrupt) }

// ==== export ===================================================================

// Writer writes an mboxrd stream.
type Writer struct {
	w *bufio.Writer
}

// NewWriter writes mboxrd to w; Close flushes (it does not close w).
func NewWriter(w io.Writer) *Writer { return &Writer{w: bufio.NewWriterSize(w, 64<<10)} }

// Write implements importexport.Sink.
func (w *Writer) Write(_ context.Context, m ie.Message) error {
	date := m.Received
	if date.IsZero() {
		date = ie.ParseDate(ie.HeaderValue(m.Raw, "Date"))
	}
	if date.IsZero() {
		date = time.Unix(0, 0)
	}
	if _, err := fmt.Fprintf(w.w, "From MAILER-DAEMON %s\n", date.UTC().Format("Mon Jan _2 15:04:05 2006")); err != nil {
		return err
	}
	raw := bytes.ReplaceAll(m.Raw, []byte("\r\n"), []byte("\n"))
	raw = addState(raw, m.Flags)
	for len(raw) > 0 {
		line := raw
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			line, raw = raw[:i+1], raw[i+1:]
		} else {
			raw = nil
		}
		j := 0
		for j < len(line) && line[j] == '>' {
			j++
		}
		if bytes.HasPrefix(line[j:], fromPrefix) {
			w.w.WriteByte('>')
		}
		w.w.Write(line)
	}
	if len(m.Raw) > 0 && m.Raw[len(m.Raw)-1] != '\n' {
		w.w.WriteByte('\n')
	}
	return w.w.WriteByte('\n')
}

// addState prepends Status/X-Status headers for the flags.
func addState(raw []byte, f ie.Flags) []byte {
	var hdr bytes.Buffer
	if f.Seen {
		hdr.WriteString("Status: RO\n")
	}
	xs := ""
	if f.Answered {
		xs += "A"
	}
	if f.Flagged {
		xs += "F"
	}
	if f.Deleted {
		xs += "D"
	}
	if f.Draft {
		xs += "T"
	}
	if xs != "" {
		hdr.WriteString("X-Status: " + xs + "\n")
	}
	if f.Forwarded {
		hdr.WriteString(fmt.Sprintf("X-Mozilla-Status: %04x\n", 0x1000))
	}
	if hdr.Len() == 0 {
		return raw
	}
	// Insert after the header block, before the empty line.
	end := bytes.Index(raw, []byte("\n\n"))
	if end < 0 {
		return append(append(append([]byte{}, raw...), '\n'), hdr.Bytes()...)
	}
	out := make([]byte, 0, len(raw)+hdr.Len())
	out = append(out, raw[:end+1]...)
	out = append(out, hdr.Bytes()...)
	return append(out, raw[end+1:]...)
}

// Close implements importexport.Sink.
func (w *Writer) Close() error { return w.w.Flush() }

// TreeWriter writes one mbox file per folder ("Folder.mbox",
// sub-folders in "Folder.sbd/").
type TreeWriter struct {
	root  string
	files map[string]*treeFile
}

type treeFile struct {
	f *os.File
	w *Writer
}

// NewTreeWriter writes below root.
func NewTreeWriter(root string) (*TreeWriter, error) {
	return &TreeWriter{root: root, files: map[string]*treeFile{}}, os.MkdirAll(root, 0o700)
}

// TreePath returns the relative file path of a folder.
func TreePath(folder []string) string {
	if len(folder) == 0 {
		folder = []string{"Inbox"}
	}
	parts := make([]string, len(folder))
	for i, f := range folder {
		parts[i] = ie.SafeName(f)
		if i < len(folder)-1 {
			parts[i] += ".sbd"
		}
	}
	parts[len(parts)-1] += ".mbox"
	return filepath.Join(parts...)
}

// Write implements importexport.Sink.
func (t *TreeWriter) Write(ctx context.Context, m ie.Message) error {
	rel := TreePath(m.Folder)
	tf := t.files[rel]
	if tf == nil {
		p := filepath.Join(t.root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		tf = &treeFile{f: f, w: NewWriter(f)}
		t.files[rel] = tf
	}
	return tf.w.Write(ctx, m)
}

// Close implements importexport.Sink.
func (t *TreeWriter) Close() error {
	var first error
	for _, tf := range t.files {
		if err := tf.w.Close(); err != nil && first == nil {
			first = err
		}
		if err := tf.f.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
