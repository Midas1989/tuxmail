// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"mime"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"golang.org/x/net/html/charset"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// Email flag bits kept in the folder state.
const (
	bitRead uint8 = 1 << iota
	bitFlagged
	bitAnswered
	bitForwarded
	bitDraft
)

// itemFlags are the mutable properties of a mail item.
type itemFlags struct {
	Mask       uint8
	Categories []string
}

// mergeFlags applies the properties present in an ApplicationData element
// (Add or Change) to f. Properties missing from a Change keep their value.
func mergeFlags(f itemFlags, d *wbxml.Node) itemFlags {
	set := func(bit uint8, on bool) {
		if on {
			f.Mask |= bit
		} else {
			f.Mask &^= bit
		}
	}
	if r := d.Child("Email:Read"); r != nil {
		set(bitRead, strings.TrimSpace(r.Value()) == "1")
	}
	if fl := d.Child("Email:Flag"); fl != nil {
		// An empty Flag element clears the flag; status 2 = active,
		// 1 = complete, 0 = cleared.
		set(bitFlagged, fl.TextOf("Email:Status") == "2")
	}
	if v := d.Child("Email2:LastVerbExecuted"); v != nil {
		switch strings.TrimSpace(v.Value()) {
		case "1", "2":
			set(bitAnswered, true)
		case "3":
			set(bitForwarded, true)
		}
	}
	if dr := d.Child("Email2:IsDraft"); dr != nil {
		set(bitDraft, strings.TrimSpace(dr.Value()) == "1")
	}
	if cats := d.Child("Email:Categories"); cats != nil {
		f.Categories = nil
		for _, c := range cats.All("Email:Category") {
			if v := strings.TrimSpace(strings.Map(dropControl, c.Value())); v != "" {
				f.Categories = append(f.Categories, v)
			}
		}
	}
	return f
}

// domainFlags converts item flags to domain flags; categories become
// keywords through toKeyword.
func domainFlags(f itemFlags, toKeyword func(string) (string, bool)) []domain.Flag {
	var out []domain.Flag
	if f.Mask&bitRead != 0 {
		out = append(out, domain.FlagSeen)
	}
	if f.Mask&bitFlagged != 0 {
		out = append(out, domain.FlagFlagged)
	}
	if f.Mask&bitAnswered != 0 {
		out = append(out, domain.FlagAnswered)
	}
	if f.Mask&bitForwarded != 0 {
		out = append(out, domain.FlagForwarded)
	}
	if f.Mask&bitDraft != 0 {
		out = append(out, domain.FlagDraft)
	}
	for _, c := range f.Categories {
		if kw, ok := toKeyword(c); ok && kw != "" {
			out = append(out, domain.Flag(kw))
		}
	}
	if out == nil {
		out = []domain.Flag{}
	}
	return out
}

// parseAddressList parses an EAS address field ("Name" <a@b>, c@d).
func parseAddressList(s string) []domain.Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	dec := &mail.AddressParser{WordDecoder: wordDecoder}
	if list, err := dec.ParseList(s); err == nil {
		out := make([]domain.Address, len(list))
		for i, a := range list {
			out[i] = domain.Address{Name: a.Name, Email: a.Address}
		}
		return out
	}
	// Lenient fallback: split on ',' / ';' and parse one by one.
	var out []domain.Address
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if a, err := dec.Parse(part); err == nil {
			out = append(out, domain.Address{Name: a.Name, Email: a.Address})
		} else if strings.Contains(part, "@") {
			out = append(out, domain.Address{Email: strings.Trim(part, "<> \"")})
		}
	}
	return out
}

// wordDecoder decodes RFC 2047 words in all charsets known to x/net.
var wordDecoder = &mime.WordDecoder{CharsetReader: charset.NewReaderLabel}

// normalizeIDs extracts message ids ("<a@b> <c@d>") without brackets.
func normalizeIDs(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			break
		}
		if id := strings.TrimSpace(s[i+1 : i+j]); id != "" {
			out = append(out, id)
		}
		s = s[i+j+1:]
	}
	if len(out) == 0 {
		if t := strings.TrimSpace(s); t != "" && !strings.ContainsAny(t, " \t") {
			out = append(out, t)
		}
	}
	return out
}

// mimeHeader parses the header section of a (possibly truncated) MIME
// message.
func mimeHeader(data []byte) textproto.MIMEHeader {
	if len(data) == 0 {
		return nil
	}
	if i := bytes.Index(data, []byte("\r\n\r\n")); i >= 0 {
		data = data[:i+4]
	} else if i := bytes.Index(data, []byte("\n\n")); i >= 0 {
		data = data[:i+2]
	} else {
		data = append(append([]byte(nil), data...), "\r\n\r\n"...)
	}
	h, err := textproto.NewReader(bufio.NewReader(bytes.NewReader(data))).ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return nil
	}
	return h
}

// emailMessage converts the ApplicationData of a mail item.
func emailMessage(folder, serverID string, d *wbxml.Node, flags itemFlags, toKeyword func(string) (string, bool)) domain.RemoteMessage {
	rm := domain.RemoteMessage{Ref: domain.RemoteRef{FolderRemoteID: folder, ID: serverID}}
	h := &rm.Header
	h.Subject = d.TextOf("Email:Subject")
	h.From = parseAddressList(d.TextOf("Email:From"))
	h.To = parseAddressList(d.TextOf("Email:To"))
	h.Cc = parseAddressList(d.TextOf("Email:Cc"))
	h.ReplyTo = parseAddressList(d.TextOf("Email:ReplyTo"))
	h.Bcc = parseAddressList(d.TextOf("Email2:Bcc"))
	rm.ReceivedAt = parseEASTime(d.TextOf("Email:DateReceived"))
	h.Date = rm.ReceivedAt

	var mimeData []byte
	if b := d.Child("AirSyncBase:Body"); b != nil {
		if b.TextOf("AirSyncBase:Type") == "4" {
			mimeData = b.Child("AirSyncBase:Data").Bytes()
		}
		if b.Has("AirSyncBase:EstimatedDataSize") {
			rm.Size = int64(b.IntOf("AirSyncBase:EstimatedDataSize", 0))
		}
	}
	if m := d.Child("Email:MIMEData"); m != nil { // EAS 2.5
		mimeData = m.Bytes()
		rm.Size = int64(d.IntOf("Email:MIMESize", len(mimeData)))
	}
	if rm.Size == 0 {
		rm.Size = int64(len(mimeData))
	}
	// The (truncated) MIME arrives with every item: the sync engine derives
	// the preview of mails kept with their header only from it.
	rm.Partial = mimeData
	if hdr := mimeHeader(mimeData); hdr != nil {
		if ids := normalizeIDs(hdr.Get("Message-Id")); len(ids) > 0 {
			h.MessageID = ids[0]
		}
		h.InReplyTo = normalizeIDs(hdr.Get("In-Reply-To"))
		h.References = normalizeIDs(strings.Join(hdr.Values("References"), " "))
		h.ThreadIndex = strings.TrimSpace(hdr.Get("Thread-Index"))
		if t, err := mail.ParseDate(hdr.Get("Date")); err == nil {
			h.Date = t
		}
		if h.Subject == "" {
			if s, err := wordDecoder.DecodeHeader(hdr.Get("Subject")); err == nil {
				h.Subject = s
			}
		}
		if len(h.From) == 0 {
			h.From = parseAddressList(hdr.Get("From"))
		}
		if len(h.To) == 0 {
			h.To = parseAddressList(hdr.Get("To"))
		}
		if len(h.Cc) == 0 {
			h.Cc = parseAddressList(hdr.Get("Cc"))
		}
		if len(h.ReplyTo) == 0 {
			h.ReplyTo = parseAddressList(hdr.Get("Reply-To"))
		}
		ct := strings.ToLower(hdr.Get("Content-Type"))
		if strings.HasPrefix(ct, "multipart/mixed") || strings.HasPrefix(ct, "multipart/signed") {
			rm.HasAttachments = true
		}
	}
	if h.Date.IsZero() {
		h.Date = rm.ReceivedAt
	}
	if rm.ReceivedAt.IsZero() {
		rm.ReceivedAt = h.Date
	}
	if rm.ReceivedAt.IsZero() {
		rm.ReceivedAt = time.Now().UTC()
		h.Date = rm.ReceivedAt
	}
	for _, a := range append(d.Path("AirSyncBase:Attachments").All("AirSyncBase:Attachment"),
		d.Path("Email:Attachments").All("Email:Attachment")...) {
		if a.TextOf("AirSyncBase:IsInline") != "1" {
			rm.HasAttachments = true
		}
	}
	if conv := d.Child("Email2:ConversationId"); conv != nil && len(conv.Bytes()) > 0 {
		h.ServerThreadID = base64.RawURLEncoding.EncodeToString(conv.Bytes())
	}
	rm.Flags = domainFlags(flags, toKeyword)
	return rm
}

func dropControl(r rune) rune {
	if r < 0x20 || r == 0x7f {
		return -1
	}
	return r
}
