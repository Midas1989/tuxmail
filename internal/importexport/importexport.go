// SPDX-License-Identifier: AGPL-3.0-or-later

// Package importexport holds the format-independent types of TuxMail's
// import and export (KONZEPT 13). The format readers and writers live in
// the sub-packages (eml, mbox, maildir, pst, thunderbird for mail; ics for
// calendars; vcf, csv and ldif for contacts); they only depend on this
// package, so they can be used without the store (CLI, tests).
//
// A mail Source produces Messages (raw RFC 5322 bytes, a folder path, flags
// and the received date) in a stable order, so that an interrupted import
// can be resumed by skipping the first n messages. A Sink consumes them.
package importexport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrCorrupt marks input that is not a valid file of the expected format.
var ErrCorrupt = errors.New("importexport: corrupt or unsupported input")

// ErrNotAvailable is returned for formats that are not supported in this build.
var ErrNotAvailable = errors.New("importexport: format not available in this build")

// Corrupt wraps a format error so that errors.Is(err, ErrCorrupt) holds.
func Corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}

// Flags are the IMAP system flags of a message.
type Flags struct {
	Seen      bool `json:"seen,omitempty"`
	Answered  bool `json:"answered,omitempty"`
	Flagged   bool `json:"flagged,omitempty"`
	Draft     bool `json:"draft,omitempty"`
	Deleted   bool `json:"deleted,omitempty"`
	Forwarded bool `json:"forwarded,omitempty"`
}

// Message is one mail of an import or export.
type Message struct {
	// Raw is the complete RFC 5322 message.
	Raw []byte
	// Folder is the folder path below the source root (empty: root/inbox),
	// e.g. ["Archive", "2025"].
	Folder []string
	Flags  Flags
	// Received is the internal date (zero: unknown, use the Date header).
	Received time.Time
}

// FolderName joins the folder path with "/" ("" for the root).
func (m Message) FolderName() string { return strings.Join(m.Folder, "/") }

// Source is a readable collection of messages (a file, directory or archive).
type Source interface {
	// Walk calls fn for every message in a stable order. A non-nil error
	// from fn stops the walk and is returned. Damaged single messages are
	// reported through the Problems of a Reporter if the source supports it
	// and skipped; a structurally broken source returns an ErrCorrupt error.
	Walk(ctx context.Context, fn func(Message) error) error
}

// Sink receives messages, e.g. an export file.
type Sink interface {
	Write(ctx context.Context, m Message) error
	Close() error
}

// Problem describes an item that could not be read and was skipped.
type Problem struct {
	Item  string `json:"item"`
	Error string `json:"error"`
}

// Reporter is implemented by sources that skip damaged items.
type Reporter interface {
	Problems() []Problem
}

// ProblemList collects skipped items; it is embedded by sources.
type ProblemList struct{ list []Problem }

// Add records a skipped item.
func (p *ProblemList) Add(item string, err error) {
	if len(p.list) < 1000 {
		p.list = append(p.list, Problem{Item: item, Error: err.Error()})
	}
}

// Merge appends the problems of another source.
func (p *ProblemList) Merge(other []Problem) {
	for _, o := range other {
		if len(p.list) < 1000 {
			p.list = append(p.list, o)
		}
	}
}

// Problems implements Reporter.
func (p *ProblemList) Problems() []Problem { return p.list }

// SafeName turns a folder or file name into a portable file name component.
func SafeName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20, strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ". ")
	if out == "" {
		out = "_"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

// MessageID returns the Message-ID header of a raw message (without angle
// brackets, "" if none). Only the header block is scanned.
func MessageID(raw []byte) string {
	return strings.Trim(HeaderValue(raw, "Message-Id"), "<> \t")
}

// HeaderValue returns the first value of a header field (unfolded, not
// decoded) from a raw message.
func HeaderValue(raw []byte, name string) string {
	hdr := raw
	if i := indexHeaderEnd(raw); i >= 0 {
		hdr = raw[:i]
	}
	lines := strings.Split(strings.ReplaceAll(string(hdr), "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		k, v, ok := strings.Cut(lines[i], ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), name) || strings.ContainsAny(k, " \t") {
			continue
		}
		for i+1 < len(lines) && len(lines[i+1]) > 0 && (lines[i+1][0] == ' ' || lines[i+1][0] == '\t') {
			i++
			v += " " + strings.TrimSpace(lines[i])
		}
		return strings.TrimSpace(v)
	}
	return ""
}

func indexHeaderEnd(raw []byte) int {
	s := string(raw[:min(len(raw), 256<<10)])
	a := strings.Index(s, "\r\n\r\n")
	b := strings.Index(s, "\n\n")
	switch {
	case a < 0:
		return b
	case b < 0:
		return a
	default:
		return min(a, b)
	}
}

// LooksLikeMessage reports whether raw starts with something resembling an
// RFC 5322 header field.
func LooksLikeMessage(raw []byte) bool {
	s := string(raw[:min(len(raw), 4096)])
	s = strings.TrimLeft(s, "\r\n")
	line, _, _ := strings.Cut(s, "\n")
	k, _, ok := strings.Cut(line, ":")
	return ok && k != "" && !strings.ContainsAny(k, " \t") && len(k) < 80
}

// Dedupe tracks Message-IDs seen during an import.
type Dedupe struct{ seen map[string]bool }

// NewDedupe returns an empty set.
func NewDedupe() *Dedupe { return &Dedupe{seen: map[string]bool{}} }

// Seen reports whether the Message-ID was already recorded and records it.
// Messages without Message-ID are never duplicates.
func (d *Dedupe) Seen(id string) bool {
	id = strings.ToLower(strings.Trim(id, "<> "))
	if id == "" {
		return false
	}
	if d.seen[id] {
		return true
	}
	d.seen[id] = true
	return false
}

// Add records a Message-ID without checking.
func (d *Dedupe) Add(id string) {
	if id = strings.ToLower(strings.Trim(id, "<> ")); id != "" {
		d.seen[id] = true
	}
}

// Progress is a snapshot of a running job.
type Progress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"` // 0: unknown
	Skipped int    `json:"skipped"`
	Failed  int    `json:"failed"`
	Current string `json:"current,omitempty"`
}

// ParseDate parses an RFC 5322 Date header leniently ("" or invalid: zero).
func ParseDate(v string) time.Time {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "("); i > 0 {
		v = strings.TrimSpace(v[:i])
	}
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04 -0700", "Mon Jan 2 15:04:05 2006", time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}
