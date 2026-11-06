// SPDX-License-Identifier: AGPL-3.0-or-later

// Package parse turns raw RFC 5322 messages into the structured form used by
// TuxMail (docs/KONZEPT.md 6, 8.3, 8.6, 8.7).
//
// The parser is deliberately tolerant: it never rejects a message because of
// malformed headers, wrong or missing charsets, broken transfer encodings or
// unterminated multiparts. Problems are reported in Message.Warnings instead.
// Header fields are kept in a github.com/emersion/go-message textproto.Header.
//
// Charsets are decoded without global state (no message.CharsetReader):
// WHATWG and IANA labels via golang.org/x/text, UTF-7 natively, and
// undeclared or mislabeled 8-bit text as UTF-8 if valid, otherwise as
// windows-1252 (configurable).
package parse

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-message/textproto"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/quote"
	"github.com/Midas1989/Mailclient/internal/mime/sanitize"
)

// Defaults for Options.
const (
	DefaultMaxDepth   = 32
	DefaultMaxParts   = 2000
	DefaultSnippetLen = 200
)

// Options configure Parse.
type Options struct {
	// KeyRing, if set, is used to decrypt and verify OpenPGP messages.
	KeyRing *pgp.KeyRing
	// PGP are options passed to KeyRing.ReadMessage.
	PGP pgp.ReadOptions
	// PGPSenderKeys, if set, adds verification keys for the From address
	// (e.g. the sender's Autocrypt key).
	PGPSenderKeys func(ctx context.Context, from string) []*pgp.Key
	// SMIME, if set, verifies and decrypts S/MIME messages (smime.go).
	SMIME SMIMEReader
	// FallbackCharset decodes 8-bit text without (valid) charset label
	// (default windows-1252).
	FallbackCharset string
	// SkipAttachmentData leaves Attachment.Data empty (metadata only).
	SkipAttachmentData bool
	MaxDepth           int
	MaxParts           int
	SnippetLen         int
}

// Part is a node of the MIME body tree.
type Part struct {
	// Path is the IMAP-style part number ("1", "1.2"); "" for the root of a
	// single-part message.
	Path        string            `json:"path"`
	ContentType string            `json:"contentType"` // lower-case media type
	Params      map[string]string `json:"params,omitempty"`
	Disposition string            `json:"disposition,omitempty"` // "inline", "attachment" or ""
	Filename    string            `json:"filename,omitempty"`
	ContentID   string            `json:"contentId,omitempty"` // without angle brackets
	Encoding    string            `json:"encoding,omitempty"`  // Content-Transfer-Encoding
	Charset     string            `json:"charset,omitempty"`
	// Size is the decoded size in bytes (for multiparts the raw size).
	Size     int64   `json:"size"`
	Children []*Part `json:"children,omitempty"`
}

// Attachment is a file attached to (or embedded in) the message.
type Attachment struct {
	PartPath    string `json:"partPath"`
	Filename    string `json:"filename"`
	MIMEType    string `json:"mimeType"`
	Size        int64  `json:"size"`
	ContentID   string `json:"contentId,omitempty"`
	Disposition string `json:"disposition,omitempty"`
	// Inline is true for parts meant to be shown inside the body (images
	// referenced by cid:, inline dispositions).
	Inline bool `json:"inline"`
	// Data is the decoded content (nil with Options.SkipAttachmentData).
	Data []byte `json:"-"`
}

// CalendarPart is a text/calendar body part (iMIP invitation, reply ...).
type CalendarPart struct {
	PartPath string `json:"partPath"`
	Method   string `json:"method,omitempty"` // REQUEST, REPLY, CANCEL ... (upper-case)
	UID      string `json:"uid,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Sequence string `json:"sequence,omitempty"`
	Data     []byte `json:"-"` // UTF-8 iCalendar text
}

// AttachedMessage describes a message/rfc822 part (forward as attachment).
type AttachedMessage struct {
	PartPath  string         `json:"partPath"`
	MessageID string         `json:"messageId,omitempty"` // normalized
	Subject   string         `json:"subject,omitempty"`
	From      domain.Address `json:"from"`
	Date      time.Time      `json:"date"`
}

// PGPInfo describes the OpenPGP state of the message.
type PGPInfo struct {
	Format    pgp.MessageFormat `json:"format"`
	Encrypted bool              `json:"encrypted"`
	// Decrypted is true when the encrypted content could be read; the body
	// fields of Message then hold the plaintext.
	Decrypted bool                `json:"decrypted"`
	Signed    bool                `json:"signed"`
	Signature pgp.SignatureStatus `json:"signature,omitempty"`
	// Verified is true for a valid signature.
	Verified          bool      `json:"verified"`
	SignerFingerprint string    `json:"signerFingerprint,omitempty"`
	SignerKeyID       string    `json:"signerKeyId,omitempty"`
	SignatureTime     time.Time `json:"signatureTime,omitempty"`
	DecryptionKey     string    `json:"decryptionKey,omitempty"`
	EncryptedTo       []string  `json:"encryptedTo,omitempty"`
	// ProtectedHeaders is true if the inner part carried protected headers.
	ProtectedHeaders bool `json:"protectedHeaders"`
	// PartiallyProtected is true for inline PGP with text outside the
	// protected block; that text was dropped.
	PartiallyProtected bool `json:"partiallyProtected,omitempty"`
	// Error describes a decryption failure or signature problem.
	Error string `json:"error,omitempty"`
}

// ListInfo holds mailing list headers (RFC 2369, 2919, 8058).
type ListInfo struct {
	ID   string `json:"id,omitempty"`   // List-Id identifier (without brackets)
	Name string `json:"name,omitempty"` // List-Id phrase
	// Unsubscribe are the List-Unsubscribe URIs in header order.
	Unsubscribe []string `json:"unsubscribe,omitempty"`
	// UnsubscribeMailto / UnsubscribeHTTP are the first URI of each kind.
	UnsubscribeMailto string `json:"unsubscribeMailto,omitempty"`
	UnsubscribeHTTP   string `json:"unsubscribeHttp,omitempty"`
	// OneClick is true when List-Unsubscribe-Post announces RFC 8058
	// one-click unsubscription and an HTTPS URI is present.
	OneClick   bool   `json:"oneClick"`
	Post       string `json:"post,omitempty"` // raw List-Unsubscribe-Post
	Precedence string `json:"precedence,omitempty"`
}

// IsList reports whether the message looks like list or bulk mail.
func (l ListInfo) IsList() bool {
	return l.ID != "" || len(l.Unsubscribe) > 0 || l.Precedence == "bulk" || l.Precedence == "list"
}

// Message is a parsed message.
type Message struct {
	// Header holds the threading-relevant header info (Message-ID etc.
	// normalized as in package threading).
	Header domain.MessageHeaderInfo `json:"header"`
	Sender []domain.Address         `json:"sender,omitempty"`
	// Headers are the raw (undecoded) header fields of the outer message.
	Headers textproto.Header `json:"-"`

	Text    string `json:"text"`           // best plain-text body ("\n" line endings)
	HTML    string `json:"html,omitempty"` // best HTML body (unsanitized!)
	Snippet string `json:"snippet"`
	// TextFromHTML is true if Text was generated from HTML.
	TextFromHTML bool `json:"textFromHtml,omitempty"`

	Root        *Part        `json:"root"`
	Attachments []Attachment `json:"attachments,omitempty"`
	// InlineByCID maps content IDs to indexes in Attachments.
	InlineByCID map[string]int `json:"-"`

	CalendarParts      []CalendarPart    `json:"calendarParts,omitempty"`
	AttachedMessages   []AttachedMessage `json:"attachedMessages,omitempty"`
	AttachedMessageIDs []string          `json:"attachedMessageIds,omitempty"`

	Autocrypt       []string  `json:"-"` // raw Autocrypt header values
	AutocryptGossip []string  `json:"-"` // gossip headers of the decrypted part
	PGP             PGPInfo   `json:"pgp"`
	SMIME           SMIMEInfo `json:"smime"`

	List          ListInfo    `json:"list"`
	Auth          AuthSummary `json:"auth"`
	AutoSubmitted string      `json:"autoSubmitted,omitempty"` // lower-case value, "" if absent
	// DispositionNotificationTo is set when the sender requests a read receipt.
	DispositionNotificationTo []domain.Address `json:"dispositionNotificationTo,omitempty"`

	// Size is the raw size in bytes.
	Size int64 `json:"size"`
	// Raw references the input (not copied).
	Raw []byte `json:"-"`
	// Warnings lists tolerated problems.
	Warnings []string `json:"warnings,omitempty"`
}

// HasAttachments reports whether the message has non-inline attachments.
func (m *Message) HasAttachments() bool {
	for _, a := range m.Attachments {
		if !a.Inline {
			return true
		}
	}
	return false
}

// IsAutomated reports whether the message was sent automatically
// (Auto-Submitted other than "no", RFC 3834) or is list/bulk mail.
func (m *Message) IsAutomated() bool {
	return (m.AutoSubmitted != "" && m.AutoSubmitted != "no") || m.List.IsList()
}

// CalendarUIDs returns the iCalendar UIDs of all calendar parts.
func (m *Message) CalendarUIDs() []string {
	var out []string
	for _, c := range m.CalendarParts {
		if c.UID != "" {
			out = append(out, c.UID)
		}
	}
	return out
}

// FingerprintInput returns the input for threading.Fingerprint.
func (m *Message) FingerprintInput() threading.FingerprintInput {
	body := m.Text
	if len(body) > threading.FingerprintBodyPrefix {
		body = body[:threading.FingerprintBodyPrefix]
	}
	return threading.FingerprintInput{
		Date: m.Header.Date, From: m.Header.From, To: m.Header.To, Subject: m.Header.Subject,
		TextSize: int64(len(m.Text)), Body: []byte(body),
	}
}

// Parse parses raw with a background context.
func Parse(raw []byte, opts Options) (*Message, error) {
	return ParseContext(context.Background(), raw, opts)
}

// ParseContext parses a complete message. It only returns an error if ctx is
// cancelled; malformed input is parsed as well as possible.
func ParseContext(ctx context.Context, raw []byte, opts Options) (*Message, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = DefaultMaxDepth
	}
	if opts.MaxParts <= 0 {
		opts.MaxParts = DefaultMaxParts
	}
	if opts.SnippetLen <= 0 {
		opts.SnippetLen = DefaultSnippetLen
	}
	p := &parser{ctx: ctx, opts: opts, dec: newHeaderDecoder(opts.FallbackCharset)}
	m := &Message{Raw: raw, Size: int64(len(raw)), InlineByCID: map[string]int{}}
	p.msg = m

	hdr, body := splitHeader(raw)
	m.Headers = hdr
	p.headerInfo(hdr)
	m.Root = p.entity(hdr, body, "", 0, false)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.applyPGP(raw)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.applySMIME(raw)
	p.finishBody()
	p.list(hdr)
	m.Auth = parseAuthResults(hdr.Values("Authentication-Results"))
	m.AutoSubmitted = strings.ToLower(strings.TrimSpace(firstToken(hdr.Get("Auto-Submitted"))))
	m.Autocrypt = hdr.Values("Autocrypt")
	if v := hdr.Get("Disposition-Notification-To"); v != "" {
		m.DispositionNotificationTo = p.dec.addresses(v)
	}
	for _, a := range m.AttachedMessages {
		if a.MessageID != "" {
			m.AttachedMessageIDs = append(m.AttachedMessageIDs, a.MessageID)
		}
	}
	return m, nil
}

type parser struct {
	ctx   context.Context
	opts  Options
	dec   *headerDecoder
	msg   *Message
	parts int

	texts, htmls []string
}

func (p *parser) warn(format string, args ...any) {
	if len(p.msg.Warnings) < 50 {
		p.msg.Warnings = append(p.msg.Warnings, fmt.Sprintf(format, args...))
	}
}

func (p *parser) headerInfo(h textproto.Header) {
	m := p.msg
	d := p.dec
	info := domain.MessageHeaderInfo{
		MessageID:   threading.NormalizeMessageID(h.Get("Message-Id")),
		InReplyTo:   threading.ParseInReplyTo(unfold(h.Get("In-Reply-To"))),
		References:  threading.ParseReferences(unfold(strings.Join(h.Values("References"), " "))),
		Subject:     d.text(h.Get("Subject")),
		From:        d.addresses(h.Get("From")),
		To:          d.addresses(strings.Join(h.Values("To"), ", ")),
		Cc:          d.addresses(strings.Join(h.Values("Cc"), ", ")),
		Bcc:         d.addresses(strings.Join(h.Values("Bcc"), ", ")),
		ReplyTo:     d.addresses(h.Get("Reply-To")),
		Date:        parseDate(h.Get("Date")),
		ThreadIndex: unfold(h.Get("Thread-Index")),
	}
	if info.Date.IsZero() && h.Has("Date") {
		p.warn("unparsable Date header %q", h.Get("Date"))
	}
	m.Header = info
	m.Sender = d.addresses(h.Get("Sender"))
}

// splitHeader separates header and body and parses the header leniently:
// folded lines are joined, lines without colon are ignored, an mbox "From "
// line is skipped. Both CRLF and bare LF line endings are accepted.
func splitHeader(raw []byte) (textproto.Header, []byte) {
	var fields [][]byte
	var name string
	var val []byte
	flush := func() {
		if name != "" {
			fields = append(fields, []byte(name+": "+string(val)+"\r\n"))
		}
		name, val = "", nil
	}
	// textproto.Header keeps fields in reverse order internally (Get
	// returns the topmost field), so they are added last to first.
	build := func() textproto.Header {
		var h textproto.Header
		for i := len(fields) - 1; i >= 0; i-- {
			h.AddRaw(fields[i])
		}
		return h
	}
	rest := raw
	first := true
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		var line []byte
		if i < 0 {
			line, rest = rest, nil
		} else {
			line, rest = rest[:i], rest[i+1:]
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			flush()
			return build(), rest
		}
		if first && bytes.HasPrefix(line, []byte("From ")) {
			first = false
			continue
		}
		first = false
		if line[0] == ' ' || line[0] == '\t' {
			if name != "" {
				val = append(val, "\r\n"...)
				val = append(val, line...)
			}
			continue
		}
		c := bytes.IndexByte(line, ':')
		if c <= 0 {
			flush()
			continue
		}
		// "Name : value" is tolerated, "two words: value" is not a field.
		n := string(bytes.TrimRight(line[:c], " \t"))
		if n == "" || strings.ContainsAny(n, " \t") {
			flush()
			continue
		}
		flush()
		name = n
		val = append([]byte(nil), bytes.TrimLeft(line[c+1:], " \t")...)
	}
	flush()
	return build(), nil
}

// entity parses one MIME entity and records body parts and attachments.
func (p *parser) entity(h textproto.Header, body []byte, partPath string, depth int, inAttached bool) *Part {
	p.parts++
	ct := h.Get("Content-Type")
	mt, params := p.dec.mediaType(ct)
	if mt == "" || !strings.Contains(mt, "/") {
		if ct != "" {
			p.warn("invalid Content-Type %q at part %q", ct, partPath)
		}
		mt = "text/plain"
	}
	disp, dparams := p.dec.mediaType(h.Get("Content-Disposition"))
	if disp != "inline" && disp != "attachment" && disp != "" {
		disp = "attachment"
	}
	part := &Part{
		Path: partPath, ContentType: mt, Params: params, Disposition: disp,
		ContentID: strings.Trim(strings.TrimSpace(unfold(h.Get("Content-Id"))), "<>"),
		Encoding:  strings.ToLower(strings.TrimSpace(h.Get("Content-Transfer-Encoding"))),
		Charset:   params["charset"],
	}
	part.Filename = cleanFilename(firstNonEmpty(dparams["filename"], params["name"]))

	if strings.HasPrefix(mt, "multipart/") {
		part.Size = int64(len(body))
		if depth >= p.opts.MaxDepth {
			p.warn("maximum nesting depth reached at part %q", partPath)
			return part
		}
		p.multipart(part, body, depth, inAttached)
		return part
	}

	data := p.decodeTransfer(part.Encoding, body, partPath)
	part.Size = int64(len(data))

	switch {
	case mt == "message/rfc822" || mt == "message/global":
		p.attachedMessage(part, data, depth, inAttached)
		return part
	case mt == "application/pgp-signature" || mt == "application/pgp-encrypted" && part.Filename == "":
		// PGP/MIME control parts: not shown as attachments.
		return part
	case mt == "text/calendar" || mt == "application/ics":
		p.calendar(part, data, inAttached)
		if (part.Filename != "" || disp == "attachment") && !inAttached {
			p.addAttachment(part, data, false)
		}
		return part
	}

	isText := mt == "text/plain" || mt == "text/html"
	if isText && !inAttached && disp != "attachment" && (part.Filename == "" || disp == "inline") {
		text, fellBack := p.decodeText(part, data)
		if fellBack && part.Charset != "" {
			p.warn("charset %q of part %q could not be applied", part.Charset, partPath)
		}
		if mt == "text/html" {
			p.htmls = append(p.htmls, text)
		} else {
			if strings.EqualFold(params["format"], "flowed") {
				text = decodeFlowed(text, strings.EqualFold(params["delsp"], "yes"))
			}
			p.texts = append(p.texts, text)
		}
		return part
	}
	if inAttached {
		return part
	}
	inline := disp == "inline" || disp == "" && part.ContentID != "" && strings.HasPrefix(mt, "image/")
	if disp == "" && part.ContentID == "" && part.Filename == "" && strings.HasPrefix(mt, "image/") {
		inline = true
	}
	p.addAttachment(part, data, inline)
	return part
}

func (p *parser) addAttachment(part *Part, data []byte, inline bool) {
	a := Attachment{
		PartPath: part.Path, Filename: part.Filename, MIMEType: part.ContentType, Size: int64(len(data)),
		ContentID: part.ContentID, Disposition: part.Disposition, Inline: inline,
	}
	if a.Filename == "" {
		a.Filename = defaultFilename(part)
	}
	if !p.opts.SkipAttachmentData {
		a.Data = data
	}
	p.msg.Attachments = append(p.msg.Attachments, a)
	if a.ContentID != "" {
		p.msg.InlineByCID[a.ContentID] = len(p.msg.Attachments) - 1
	}
}

func (p *parser) multipart(part *Part, body []byte, depth int, inAttached bool) {
	boundary := part.Params["boundary"]
	if boundary == "" {
		boundary = guessBoundary(body)
		if boundary == "" {
			p.warn("multipart part %q without boundary", part.Path)
			// Treat the body as plain text so that nothing is lost.
			if !inAttached {
				p.texts = append(p.texts, toValidUTF8(body, p.opts.FallbackCharset))
			}
			return
		}
		p.warn("multipart part %q: boundary guessed", part.Path)
	}
	bodies, terminated := splitMultipart(body, boundary)
	if !terminated {
		p.warn("multipart part %q: missing closing boundary", part.Path)
	}
	if part.ContentType == "multipart/encrypted" {
		// The ciphertext is not content; it is handled by applyPGP.
		inAttached = true
	}
	alternative := part.ContentType == "multipart/alternative"
	var altText, altHTML []string
	for i, b := range bodies {
		if p.parts >= p.opts.MaxParts {
			p.warn("too many parts; rest ignored")
			break
		}
		if p.ctx.Err() != nil {
			return
		}
		ch, cb := splitHeader(b)
		if part.ContentType == "multipart/digest" && ch.Get("Content-Type") == "" {
			ch.Set("Content-Type", "message/rfc822")
		}
		childPath := fmt.Sprint(i + 1)
		if part.Path != "" {
			childPath = part.Path + "." + childPath
		}
		if alternative {
			// Collect each alternative separately and keep the best.
			saveT, saveH := p.texts, p.htmls
			p.texts, p.htmls = nil, nil
			part.Children = append(part.Children, p.entity(ch, cb, childPath, depth+1, inAttached))
			if len(p.texts) > 0 {
				altText = p.texts
			}
			if len(p.htmls) > 0 {
				altHTML = p.htmls
			}
			p.texts, p.htmls = saveT, saveH
			continue
		}
		part.Children = append(part.Children, p.entity(ch, cb, childPath, depth+1, inAttached))
	}
	if alternative {
		p.texts = append(p.texts, altText...)
		p.htmls = append(p.htmls, altHTML...)
	}
}

var boundaryLineRe = regexp.MustCompile(`(?m)^--([^\s-][^\r\n]{0,69}?)\s*$`)

func guessBoundary(body []byte) string {
	if m := boundaryLineRe.FindSubmatch(body); m != nil {
		return string(bytes.TrimSuffix(m[1], []byte("--")))
	}
	return ""
}

// splitMultipart splits a multipart body at its boundaries. Preamble and
// epilogue are dropped. It tolerates trailing whitespace after boundaries,
// LF line endings and a missing closing boundary (terminated=false).
func splitMultipart(body []byte, boundary string) (parts [][]byte, terminated bool) {
	delim := []byte("--" + boundary)
	var cur []byte
	in := false
	start := -1
	pos := 0
	for pos <= len(body) {
		i := bytes.IndexByte(body[pos:], '\n')
		lineEnd := len(body)
		next := len(body) + 1
		if i >= 0 {
			lineEnd = pos + i
			next = lineEnd + 1
		}
		line := bytes.TrimRight(body[pos:lineEnd], " \t\r")
		if bytes.HasPrefix(line, delim) {
			rest := line[len(delim):]
			isClose := bytes.Equal(rest, []byte("--"))
			if len(rest) == 0 || isClose {
				if in && start >= 0 {
					// Adjacent boundaries yield an empty part.
					cur = body[start:max(start, trimLineBreakBefore(body, pos))]
					parts = append(parts, cur)
				}
				if isClose {
					return parts, true
				}
				in = true
				start = next
				if start > len(body) {
					start = len(body)
				}
			}
		}
		pos = next
	}
	if in && start >= 0 && start <= len(body) {
		if last := body[start:]; len(bytes.TrimSpace(last)) > 0 {
			parts = append(parts, last)
		}
	}
	return parts, false
}

// trimLineBreakBefore returns the end of the content before a boundary line
// at pos: the CRLF (or LF) preceding the boundary belongs to the boundary.
func trimLineBreakBefore(body []byte, pos int) int {
	end := pos
	if end > 0 && body[end-1] == '\n' {
		end--
		if end > 0 && body[end-1] == '\r' {
			end--
		}
	}
	return end
}

// decodeTransfer decodes the Content-Transfer-Encoding leniently.
func (p *parser) decodeTransfer(enc string, body []byte, partPath string) []byte {
	switch enc {
	case "base64":
		return decodeBase64Lenient(body)
	case "quoted-printable":
		return decodeQPLenient(body)
	case "", "7bit", "8bit", "binary":
		return body
	case "x-uuencode", "uuencode", "x-uue":
		if d, ok := decodeUU(body); ok {
			return d
		}
	}
	p.warn("unknown transfer encoding %q at part %q", enc, partPath)
	return body
}

func decodeBase64Lenient(b []byte) []byte {
	clean := make([]byte, 0, len(b))
	for _, c := range b {
		if isBase64Char(c) {
			clean = append(clean, c)
		} else if c == '=' {
			break
		}
	}
	if len(clean)%4 == 1 {
		clean = clean[:len(clean)-1]
	}
	out := make([]byte, base64.RawStdEncoding.DecodedLen(len(clean)))
	n, err := base64.RawStdEncoding.Decode(out, clean)
	if err != nil {
		return out[:n]
	}
	return out[:n]
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// decodeQPLenient decodes quoted-printable; invalid escapes are kept
// literally, trailing whitespace before line breaks is removed.
func decodeQPLenient(b []byte) []byte {
	out := make([]byte, 0, len(b))
	lines := bytes.Split(b, []byte("\n"))
	for li, line := range lines {
		line = bytes.TrimSuffix(line, []byte("\r"))
		line = bytes.TrimRight(line, " \t")
		soft := false
		for i := 0; i < len(line); i++ {
			c := line[i]
			if c != '=' {
				out = append(out, c)
				continue
			}
			if i == len(line)-1 {
				soft = true
				break
			}
			if i+2 < len(line) {
				h, ok1 := unhex(line[i+1])
				l, ok2 := unhex(line[i+2])
				if ok1 && ok2 {
					out = append(out, h<<4|l)
					i += 2
					continue
				}
			}
			out = append(out, c)
		}
		if !soft && li < len(lines)-1 {
			out = append(out, '\r', '\n')
		}
	}
	return out
}

// decodeUU decodes a uuencoded body ("begin 644 name" ... "end").
func decodeUU(b []byte) ([]byte, bool) {
	var out []byte
	inBody := false
	for _, line := range bytes.Split(b, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !inBody {
			if bytes.HasPrefix(line, []byte("begin ")) {
				inBody = true
			}
			continue
		}
		if bytes.Equal(line, []byte("end")) || len(line) == 0 {
			if bytes.Equal(line, []byte("end")) {
				return out, true
			}
			continue
		}
		n := int((line[0] - ' ') & 63)
		if n == 0 {
			continue
		}
		var dec []byte
		for i := 1; i < len(line) && len(dec) < n; i += 4 {
			var q [4]byte
			for k := 0; k < 4; k++ {
				if i+k < len(line) {
					q[k] = (line[i+k] - ' ') & 63
				}
			}
			dec = append(dec, q[0]<<2|q[1]>>4, q[1]<<4|q[2]>>2, q[2]<<6|q[3])
		}
		if len(dec) > n {
			dec = dec[:n]
		}
		out = append(out, dec...)
	}
	return out, inBody
}

var htmlMetaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([a-z0-9_.:\-]+)`)

// decodeText converts a text part to UTF-8 with "\n" line endings.
func (p *parser) decodeText(part *Part, data []byte) (string, bool) {
	cs := part.Charset
	if cs == "" && part.ContentType == "text/html" {
		head := data
		if len(head) > 4096 {
			head = head[:4096]
		}
		if m := htmlMetaCharsetRe.FindSubmatch(head); m != nil {
			cs = string(m[1])
		}
	}
	s, fellBack := decodeCharset(cs, data, p.opts.FallbackCharset)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimPrefix(s, "\uFEFF"), fellBack
}

// decodeFlowed undoes RFC 3676 format=flowed soft line breaks.
func decodeFlowed(s string, delsp bool) string {
	lines := strings.Split(s, "\n")
	var out []string
	var cur strings.Builder
	curQuote := -1
	flush := func() {
		if curQuote >= 0 {
			out = append(out, strings.Repeat(">", curQuote)+prefixSpace(curQuote)+cur.String())
		}
		cur.Reset()
		curQuote = -1
	}
	for _, l := range lines {
		q := 0
		for q < len(l) && l[q] == '>' {
			q++
		}
		content := l[q:]
		content = strings.TrimPrefix(content, " ") // space stuffing
		if curQuote >= 0 && q != curQuote {
			flush()
		}
		curQuote = q
		if content != "-- " && strings.HasSuffix(content, " ") {
			if delsp {
				content = content[:len(content)-1]
			}
			cur.WriteString(content)
			continue
		}
		cur.WriteString(content)
		flush()
	}
	if cur.Len() > 0 {
		flush()
	}
	return strings.Join(out, "\n")
}

func prefixSpace(q int) string {
	if q > 0 {
		return " "
	}
	return ""
}

func (p *parser) attachedMessage(part *Part, data []byte, depth int, inAttached bool) {
	ch, cb := splitHeader(data)
	am := AttachedMessage{
		PartPath:  part.Path,
		MessageID: threading.NormalizeMessageID(ch.Get("Message-Id")),
		Subject:   p.dec.text(ch.Get("Subject")),
		Date:      parseDate(ch.Get("Date")),
	}
	if from := p.dec.addresses(ch.Get("From")); len(from) > 0 {
		am.From = from[0]
	}
	if !inAttached {
		p.msg.AttachedMessages = append(p.msg.AttachedMessages, am)
		if part.Filename == "" {
			name := am.Subject
			if name == "" {
				name = "message"
			}
			part.Filename = cleanFilename(name + ".eml")
		}
		p.addAttachment(part, data, false)
	}
	if depth < p.opts.MaxDepth {
		childPath := part.Path + ".1"
		if part.Path == "" {
			childPath = "1"
		}
		part.Children = append(part.Children, p.entity(ch, cb, childPath, depth+1, true))
	}
}

func (p *parser) calendar(part *Part, data []byte, inAttached bool) {
	text, _ := decodeCharset(part.Charset, data, p.opts.FallbackCharset)
	cp := CalendarPart{PartPath: part.Path, Method: strings.ToUpper(part.Params["method"]), Data: []byte(text)}
	inEvent := false
	for _, line := range unfoldICal(text) {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if i := strings.IndexByte(name, ';'); i >= 0 {
			name = name[:i]
		}
		name = strings.ToUpper(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		switch name {
		case "METHOD":
			if cp.Method == "" {
				cp.Method = strings.ToUpper(value)
			}
		case "BEGIN":
			if strings.EqualFold(value, "VEVENT") || strings.EqualFold(value, "VTODO") {
				inEvent = true
			}
		case "UID":
			if inEvent && cp.UID == "" {
				cp.UID = value
			}
		case "SUMMARY":
			if inEvent && cp.Summary == "" {
				cp.Summary = unescapeICal(value)
			}
		case "SEQUENCE":
			if inEvent && cp.Sequence == "" {
				cp.Sequence = value
			}
		}
	}
	if !inAttached {
		p.msg.CalendarParts = append(p.msg.CalendarParts, cp)
	}
}

// unfoldICal joins folded iCalendar lines (RFC 5545 3.1). It is linear in the
// input size: a logical line is assembled in a strings.Builder instead of by
// repeated string concatenation, which was quadratic for a line folded over
// many continuation lines (NB-02).
func unfoldICal(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var (
		out      []string
		cur      strings.Builder
		pending  string // current logical line while it has no continuation
		building bool   // a continuation was seen; cur holds the line
		have     bool
	)
	flush := func() {
		switch {
		case !have:
		case building:
			out = append(out, cur.String())
			cur = strings.Builder{}
			building = false
		default:
			out = append(out, pending)
		}
	}
	for l := range strings.SplitSeq(s, "\n") {
		if have && (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) {
			if !building {
				cur.WriteString(pending)
				building = true
			}
			cur.WriteString(l[1:])
			continue
		}
		flush()
		pending, have = l, true
	}
	flush()
	return out
}

func unescapeICal(s string) string {
	return strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`).Replace(s)
}

// finishBody assembles Text, HTML and Snippet.
func (p *parser) finishBody() {
	m := p.msg
	m.Text = strings.TrimRight(strings.Join(p.texts, "\n"), "\n")
	m.HTML = strings.Join(p.htmls, "\n")
	if strings.TrimSpace(m.Text) == "" && m.HTML != "" {
		m.Text = sanitize.ToText(m.HTML)
		m.TextFromHTML = true
	}
	m.Snippet = Snippet(m.Text, m.HTML, p.opts.SnippetLen)
}

// Snippet returns the whitespace-collapsed visible text (quotes and
// signatures removed) truncated to n runes with an ellipsis.
func Snippet(text, html string, n int) string {
	r := quote.Extract(text, html)
	s := strings.Join(strings.Fields(r.Visible), " ")
	if s == "" {
		s = strings.Join(strings.Fields(text), " ")
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	cut := string(runes[:n])
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}

// cleanFilename strips directories and control characters from a name.
func cleanFilename(name string) string { return CleanFilename(name) }

// CleanFilename makes an untrusted attachment name safe to display and to
// save: directories (also Windows "..\\"), control characters and Unicode
// bidi/format controls are removed (RTLO spoofing "invoice\u202Efdp.exe"
// shows as "invoiceexe.pdf"), as are trailing dots and spaces (ignored by
// Windows, "x.exe." would hide the real extension), and the name is
// limited to 255 bytes keeping the extension.
func CleanFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		case r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069),
			r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r == 0xfeff:
			return -1 // bidi and invisible format characters
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	name = strings.TrimSpace(name)
	if name == "" || name == "/" {
		return ""
	}
	if len(name) > 255 {
		ext := path.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		cut := 255 - len(ext)
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = name[:cut] + ext
	}
	return name
}

var extByType = map[string]string{
	"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp",
	"application/pdf": ".pdf", "text/calendar": ".ics", "message/rfc822": ".eml",
	"text/plain": ".txt", "text/html": ".html", "application/pgp-keys": ".asc",
	"application/ms-tnef": ".dat",
}

func defaultFilename(part *Part) string {
	base := "attachment"
	if part.Path != "" {
		base += "-" + strings.ReplaceAll(part.Path, ".", "-")
	}
	return base + extByType[part.ContentType]
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func firstToken(v string) string {
	v = unfold(v)
	if i := strings.IndexAny(v, ";( \t"); i >= 0 {
		return v[:i]
	}
	return v
}
