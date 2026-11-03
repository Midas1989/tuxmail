// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"bufio"
	"bytes"
	netmail "net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// headerFields are fetched with BODY.PEEK[HEADER.FIELDS (...)] in addition
// to the ENVELOPE, because the envelope lacks References and Thread-Index
// and some servers mangle In-Reply-To.
var headerFields = []string{"Message-ID", "In-Reply-To", "References", "Thread-Index", "Date"}

func headerSection() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: headerFields,
		Peek:         true,
	}
}

func headerFetchOptions() *imap.FetchOptions {
	return &imap.FetchOptions{
		UID:           true,
		Flags:         true,
		Envelope:      true,
		InternalDate:  true,
		RFC822Size:    true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imap.FetchItemBodySection{headerSection()},
	}
}

// NormalizeMessageID strips surrounding angle brackets and all whitespace and
// lower-cases the domain part (KONZEPT 6.2). It returns "" for empty input.
func NormalizeMessageID(id string) string {
	id = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, id)
	if i := strings.IndexByte(id, '<'); i >= 0 {
		if j := strings.IndexByte(id[i:], '>'); j > 0 {
			id = id[i+1 : i+j]
		} else {
			id = id[i+1:]
		}
	}
	id = strings.Trim(id, "<>")
	if at := strings.LastIndexByte(id, '@'); at >= 0 {
		id = id[:at+1] + strings.ToLower(id[at+1:])
	}
	return id
}

// ParseMessageIDList parses a References/In-Reply-To header value into
// normalized IDs (in header order). Bracketed IDs are preferred; if none are
// present, whitespace-separated tokens are used.
func ParseMessageIDList(v string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(id string) {
		id = NormalizeMessageID(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	rest := v
	for {
		i := strings.IndexByte(rest, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(rest[i:], '>')
		if j < 0 {
			break
		}
		add(rest[i : i+j+1])
		rest = rest[i+j+1:]
	}
	if len(out) == 0 {
		for _, f := range strings.Fields(v) {
			if strings.Contains(f, "@") {
				add(f)
			}
		}
	}
	return out
}

func parseHeaderBlock(b []byte) textproto.MIMEHeader {
	if len(b) == 0 {
		return textproto.MIMEHeader{}
	}
	if !bytes.HasSuffix(b, []byte("\r\n\r\n")) && !bytes.HasSuffix(b, []byte("\n\n")) {
		b = append(append([]byte{}, b...), "\r\n\r\n"...)
	}
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(b))).ReadMIMEHeader()
	if err != nil && h == nil {
		return textproto.MIMEHeader{}
	}
	return h
}

func convAddrs(l []imap.Address) []domain.Address {
	var out []domain.Address
	for _, a := range l {
		if a.IsGroupStart() || a.IsGroupEnd() {
			continue
		}
		email := a.Addr()
		if email == "" {
			continue
		}
		out = append(out, domain.Address{Name: a.Name, Email: email})
	}
	return out
}

// convFlags maps IMAP flags to domain flags, normalizing the case of system
// flags. \Recent is dropped.
func convFlags(flags []imap.Flag) []domain.Flag {
	out := make([]domain.Flag, 0, len(flags))
	for _, f := range flags {
		s := string(f)
		if strings.HasPrefix(s, `\`) {
			switch strings.ToLower(s) {
			case `\seen`:
				s = string(domain.FlagSeen)
			case `\answered`:
				s = string(domain.FlagAnswered)
			case `\flagged`:
				s = string(domain.FlagFlagged)
			case `\deleted`:
				s = string(domain.FlagDeleted)
			case `\draft`:
				s = string(domain.FlagDraft)
			case `\recent`:
				continue
			}
		} else if strings.EqualFold(s, string(domain.FlagForwarded)) {
			s = string(domain.FlagForwarded)
		}
		out = append(out, domain.Flag(s))
	}
	return out
}

func toIMAPFlags(flags []domain.Flag) []imap.Flag {
	out := make([]imap.Flag, 0, len(flags))
	for _, f := range flags {
		if f != "" {
			out = append(out, imap.Flag(f))
		}
	}
	return out
}

// hasAttachments inspects a BODYSTRUCTURE.
func hasAttachments(bs imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	found := false
	bs.Walk(func(path []int, part imap.BodyStructure) bool {
		if found {
			return false
		}
		single, ok := part.(*imap.BodyStructureSinglePart)
		if !ok {
			return true
		}
		disp := ""
		if d := single.Disposition(); d != nil {
			disp = strings.ToLower(d.Value)
		}
		mt := single.MediaType()
		switch {
		case disp == "attachment":
			found = true
		case mt == "application/pgp-signature", mt == "application/pkcs7-signature",
			mt == "application/x-pkcs7-signature", mt == "application/pgp-encrypted":
		case single.Filename() != "" && disp != "inline":
			found = true
		case mt == "message/rfc822":
			found = true
		case strings.HasPrefix(mt, "application/") && len(path) > 0:
			found = true
		}
		return true
	})
	return found
}

func toRemoteMessage(folder string, buf *imapclient.FetchMessageBuffer) domain.RemoteMessage {
	m := domain.RemoteMessage{
		Ref:            domain.RemoteRef{FolderRemoteID: folder, ID: strconv.FormatUint(uint64(buf.UID), 10)},
		Flags:          convFlags(buf.Flags),
		Size:           buf.RFC822Size,
		ReceivedAt:     buf.InternalDate,
		HasAttachments: hasAttachments(buf.BodyStructure),
	}
	h := parseHeaderBlock(buf.FindBodySection(headerSection()))
	if env := buf.Envelope; env != nil {
		m.Header.Subject = env.Subject
		m.Header.From = convAddrs(env.From)
		m.Header.To = convAddrs(env.To)
		m.Header.Cc = convAddrs(env.Cc)
		m.Header.Bcc = convAddrs(env.Bcc)
		m.Header.ReplyTo = convAddrs(env.ReplyTo)
		m.Header.Date = env.Date
		m.Header.MessageID = NormalizeMessageID(env.MessageID)
		for _, id := range env.InReplyTo {
			if id = NormalizeMessageID(id); id != "" {
				m.Header.InReplyTo = append(m.Header.InReplyTo, id)
			}
		}
	}
	if v := h.Get("Message-Id"); v != "" {
		m.Header.MessageID = NormalizeMessageID(v)
	}
	if v := h.Get("In-Reply-To"); v != "" {
		if ids := ParseMessageIDList(v); len(ids) > 0 {
			m.Header.InReplyTo = ids
		}
	}
	m.Header.References = ParseMessageIDList(strings.Join(h.Values("References"), " "))
	m.Header.ThreadIndex = strings.TrimSpace(h.Get("Thread-Index"))
	if m.Header.Date.IsZero() {
		if v := h.Get("Date"); v != "" {
			if t, err := netmail.ParseDate(v); err == nil {
				m.Header.Date = t
			}
		}
	}
	if m.Header.Date.IsZero() {
		m.Header.Date = buf.InternalDate
	}
	if m.ReceivedAt.IsZero() {
		m.ReceivedAt = m.Header.Date
	}
	if !m.ReceivedAt.IsZero() {
		m.ReceivedAt = m.ReceivedAt.In(time.UTC)
	}
	return m
}
