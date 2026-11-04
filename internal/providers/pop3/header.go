// SPDX-License-Identifier: AGPL-3.0-or-later

package pop3

import (
	"bufio"
	"bytes"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // charsets of encoded words
	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
)

// headerInfo is what the sync needs from a message header.
type headerInfo struct {
	h              domain.MessageHeaderInfo
	received       time.Time
	hasAttachments bool
}

// ParseHeader extracts list/threading information from a raw RFC 5322
// header block (a full message works too). It is exported for other
// providers that only get raw headers (e.g. EAS MIME).
func ParseHeader(raw []byte) (domain.MessageHeaderInfo, time.Time, bool) {
	hi := parseHeader(raw)
	return hi.h, hi.received, hi.hasAttachments
}

func parseHeader(raw []byte) headerInfo {
	var out headerInfo
	th, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil && th.Len() == 0 {
		return out
	}
	h := mail.Header{Header: message.Header{Header: th}}
	out.h.MessageID = threading.NormalizeMessageID(h.Get("Message-Id"))
	out.h.InReplyTo = threading.ParseInReplyTo(h.Get("In-Reply-To"))
	out.h.References = threading.ParseReferences(h.Get("References"))
	if s, err := h.Subject(); err == nil {
		out.h.Subject = s
	} else {
		out.h.Subject = h.Get("Subject")
	}
	out.h.From = addrs(h, "From")
	out.h.To = addrs(h, "To")
	out.h.Cc = addrs(h, "Cc")
	out.h.ReplyTo = addrs(h, "Reply-To")
	if d, err := h.Date(); err == nil {
		out.h.Date = d
	} else if t, _, ok := threading.ParseLooseDate(h.Get("Date")); ok {
		out.h.Date = t
	}
	out.h.ThreadIndex = strings.TrimSpace(h.Get("Thread-Index"))
	out.received = receivedTime(th)
	if out.received.IsZero() {
		out.received = out.h.Date
	}
	ct := strings.ToLower(h.Get("Content-Type"))
	out.hasAttachments = strings.HasPrefix(strings.TrimSpace(ct), "multipart/mixed")
	return out
}

func addrs(h mail.Header, key string) []domain.Address {
	list, err := h.AddressList(key)
	if err != nil {
		return nil
	}
	out := make([]domain.Address, 0, len(list))
	for _, a := range list {
		out = append(out, domain.Address{Name: a.Name, Email: a.Address})
	}
	return out
}

// receivedTime returns the date of the topmost Received header (the time
// the receiving server accepted the message).
func receivedTime(th textproto.Header) time.Time {
	v := th.Get("Received")
	if v == "" {
		return time.Time{}
	}
	i := strings.LastIndexByte(v, ';')
	if i < 0 {
		return time.Time{}
	}
	if t, _, ok := threading.ParseLooseDate(strings.TrimSpace(v[i+1:])); ok {
		return t
	}
	return time.Time{}
}
