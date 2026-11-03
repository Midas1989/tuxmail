// SPDX-License-Identifier: AGPL-3.0-or-later

// Package content extracts what the sync engine needs from a raw RFC 5322
// message: plain text for the search index and inline-forward detection, a
// snippet, attachment metadata, iCalendar UIDs and the Message-IDs of
// attached messages.
//
// The default implementation is deliberately simple (good enough for
// indexing and snippets). It will be replaced by the full MIME parser
// (internal/mime/parse) through the Extractor interface.
package content

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"mime"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/emersion/go-message"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/quote"
)

// Attachment is the metadata of one attachment part.
type Attachment struct {
	Name      string
	MIME      string
	Size      int64 // decoded size in bytes
	ContentID string
	Inline    bool
}

// Content is the extracted content of a message.
type Content struct {
	// Text is the plain text body (from text/plain, or converted from HTML).
	Text string
	// HTMLPresent is true if the message has an HTML body.
	HTMLPresent bool
	// Snippet is a short preview of the new (unquoted) text.
	Snippet     string
	Attachments []Attachment
	// CalendarUIDs are the UIDs of text/calendar parts.
	CalendarUIDs []string
	// AttachedMessageIDs are Message-IDs of message/rfc822 parts.
	AttachedMessageIDs []string
	// InlineForwards are inline-forwarded blocks found in Text.
	InlineForwards []threading.ForwardedHeader
}

// Extractor extracts Content from a raw message.
type Extractor interface {
	Extract(raw []byte) (Content, error)
}

// Default is the built-in Extractor based on go-message.
type Default struct {
	// SnippetLen is the snippet length in runes (default 200).
	SnippetLen int
	// MaxText bounds the extracted text in bytes (default 1 MiB).
	MaxText int
}

var _ Extractor = Default{}

// maxPartRead bounds how much of a single part is read.
const maxPartRead = 32 << 20

// Extract implements Extractor. Broken MIME structures degrade gracefully:
// whatever could be read before the error is returned without error.
func (d Default) Extract(raw []byte) (Content, error) {
	if d.SnippetLen <= 0 {
		d.SnippetLen = 200
	}
	if d.MaxText <= 0 {
		d.MaxText = 1 << 20
	}
	var c Content
	ent, err := message.Read(bytes.NewReader(raw))
	if ent == nil {
		return c, err
	}
	var plain, html strings.Builder
	walkErr := ent.Walk(func(path []int, part *message.Entity, perr error) error {
		if part == nil {
			return nil
		}
		mt, params, _ := part.Header.ContentType()
		if mt == "" {
			mt = "text/plain"
		}
		disp, dparams, _ := part.Header.ContentDisposition()
		name := dparams["filename"]
		if name == "" {
			name = params["name"]
		}
		if strings.HasPrefix(mt, "multipart/") {
			return nil
		}
		isAttachment := strings.EqualFold(disp, "attachment") || (name != "" && !strings.HasPrefix(mt, "text/plain") && !strings.HasPrefix(mt, "text/html"))
		switch {
		case mt == "text/calendar":
			b := readPart(part, perr, params["charset"])
			c.CalendarUIDs = append(c.CalendarUIDs, CalendarUIDs(string(b))...)
			if name != "" {
				c.Attachments = append(c.Attachments, attachment(part, name, mt, int64(len(b)), disp))
			}
		case mt == "message/rfc822" || mt == "message/global":
			b := readPart(part, perr, "")
			if id := headerMessageID(b); id != "" {
				c.AttachedMessageIDs = append(c.AttachedMessageIDs, id)
			}
			if name == "" {
				name = attachedSubject(b) + ".eml"
			}
			c.Attachments = append(c.Attachments, attachment(part, name, mt, int64(len(b)), "attachment"))
		case !isAttachment && mt == "text/plain":
			b := readPart(part, perr, params["charset"])
			if plain.Len() < d.MaxText {
				if plain.Len() > 0 {
					plain.WriteString("\n\n")
				}
				plain.Write(b)
			}
		case !isAttachment && mt == "text/html":
			b := readPart(part, perr, params["charset"])
			c.HTMLPresent = true
			if html.Len() < d.MaxText {
				html.Write(b)
			}
		default:
			n, _ := io.Copy(io.Discard, io.LimitReader(part.Body, maxPartRead))
			c.Attachments = append(c.Attachments, attachment(part, name, mt, n, disp))
		}
		return nil
	})
	text := plain.String()
	htmlText := html.String()
	if strings.TrimSpace(text) == "" && htmlText != "" {
		r := quote.HTML(htmlText)
		text = joinNonEmpty("\n\n", r.Visible, r.Quoted, r.Signature)
	}
	if len(text) > d.MaxText {
		text = truncateUTF8(text, d.MaxText)
	}
	c.Text = text
	q := quote.Extract(plain.String(), htmlText)
	snip := q.Visible
	if strings.TrimSpace(snip) == "" {
		snip = text
	}
	c.Snippet = Snippet(snip, d.SnippetLen)
	if text != "" {
		c.InlineForwards = threading.DetectForwards(text)
	}
	c.CalendarUIDs = dedupe(c.CalendarUIDs)
	c.AttachedMessageIDs = dedupe(c.AttachedMessageIDs)
	if walkErr != nil && c.Text == "" && len(c.Attachments) == 0 {
		return c, walkErr
	}
	return c, nil
}

func attachment(part *message.Entity, name, mt string, size int64, disp string) Attachment {
	cid := strings.Trim(strings.TrimSpace(part.Header.Get("Content-Id")), "<>")
	return Attachment{Name: name, MIME: mt, Size: size, ContentID: cid, Inline: strings.EqualFold(disp, "inline")}
}

// readPart reads a text part, converting legacy single-byte charsets that
// go-message does not decode itself (no global charset reader is
// installed) and replacing invalid UTF-8.
func readPart(part *message.Entity, perr error, charset string) []byte {
	b, _ := io.ReadAll(io.LimitReader(part.Body, maxPartRead))
	if perr != nil && message.IsUnknownCharset(perr) {
		b = decodeLegacy(b, charset)
	}
	if !utf8.Valid(b) {
		b = bytes.ToValidUTF8(b, []byte("�"))
	}
	return b
}

// decodeLegacy decodes ISO-8859-x / Windows-125x approximately as Latin-1
// (exact for ISO-8859-1, good enough for indexing otherwise).
func decodeLegacy(b []byte, charset string) []byte {
	cs := strings.ToLower(charset)
	if !strings.HasPrefix(cs, "iso-8859") && !strings.HasPrefix(cs, "windows-125") && !strings.HasPrefix(cs, "latin") &&
		cs != "cp1252" {
		return b
	}
	if utf8.Valid(b) {
		return b
	}
	var out bytes.Buffer
	out.Grow(len(b) * 2)
	for _, x := range b {
		out.WriteRune(rune(x))
	}
	return out.Bytes()
}

func headerMessageID(raw []byte) string {
	m, err := mail.ReadMessage(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return ""
	}
	return threading.NormalizeMessageID(m.Header.Get("Message-Id"))
}

func attachedSubject(raw []byte) string {
	m, err := mail.ReadMessage(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return "message"
	}
	s := decodeWords(m.Header.Get("Subject"))
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	if s == "" {
		return "message"
	}
	return truncateUTF8(s, 100)
}

// CalendarUIDs returns the UID properties of an iCalendar text (unfolding
// continuation lines).
func CalendarUIDs(ical string) []string {
	ical = strings.ReplaceAll(ical, "\r\n", "\n")
	ical = strings.ReplaceAll(ical, "\n ", "")
	ical = strings.ReplaceAll(ical, "\n\t", "")
	var out []string
	for _, line := range strings.Split(ical, "\n") {
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if n, _, _ := strings.Cut(name, ";"); strings.EqualFold(strings.TrimSpace(n), "UID") {
			if v := strings.TrimSpace(val); v != "" {
				out = append(out, v)
			}
		}
	}
	return dedupe(out)
}

// Snippet collapses whitespace and truncates text to n runes.
func Snippet(text string, n int) string {
	f := strings.Fields(text)
	s := strings.Join(f, " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func dedupe(l []string) []string {
	if len(l) < 2 {
		return l
	}
	seen := map[string]bool{}
	out := l[:0]
	for _, v := range l {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func newWordDecoder() *mime.WordDecoder {
	return &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		b, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		return bytes.NewReader(decodeLegacy(b, charset)), nil
	}}
}

func decodeWords(s string) string {
	if d, err := newWordDecoder().DecodeHeader(s); err == nil {
		return d
	}
	return s
}

// ErrNoHeader is returned by ParseHeader for input without a header block.
var ErrNoHeader = errors.New("content: message has no header")

// ParseHeader extracts the threading-relevant header fields of a raw
// message (used for locally created messages such as drafts and sent
// copies before the server has seen them).
func ParseHeader(raw []byte) (domain.MessageHeaderInfo, error) {
	var h domain.MessageHeaderInfo
	m, err := mail.ReadMessage(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		return h, ErrNoHeader
	}
	hd := m.Header
	h.MessageID = threading.NormalizeMessageID(hd.Get("Message-Id"))
	h.InReplyTo = threading.ParseInReplyTo(hd.Get("In-Reply-To"))
	h.References = threading.ParseReferences(strings.Join(hd["References"], " "))
	h.Subject = decodeWords(hd.Get("Subject"))
	h.From = addrList(hd.Get("From"))
	h.To = addrList(hd.Get("To"))
	h.Cc = addrList(hd.Get("Cc"))
	h.Bcc = addrList(hd.Get("Bcc"))
	h.ReplyTo = addrList(hd.Get("Reply-To"))
	h.ThreadIndex = strings.TrimSpace(hd.Get("Thread-Index"))
	if t, err := mail.ParseDate(hd.Get("Date")); err == nil {
		h.Date = t
	} else {
		h.Date = time.Time{}
	}
	return h, nil
}

func addrList(v string) []domain.Address {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	p := &mail.AddressParser{WordDecoder: newWordDecoder()}
	l, err := p.ParseList(v)
	if err != nil {
		return nil
	}
	out := make([]domain.Address, 0, len(l))
	for _, a := range l {
		out = append(out, domain.Address{Name: a.Name, Email: a.Address})
	}
	return out
}
