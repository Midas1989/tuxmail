// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/mime/signature"
)

// Errors returned by Compose.
var (
	ErrNoSender      = errors.New("compose: no sender address")
	ErrNoOriginal    = errors.New("compose: reply/forward without original message")
	ErrBadHeaderName = errors.New("compose: invalid header name")
)

// reservedHeaders may not be set through Draft.Headers.
var reservedHeaders = map[string]bool{
	"from": true, "sender": true, "to": true, "cc": true, "bcc": true, "subject": true, "date": true,
	"message-id": true, "in-reply-to": true, "references": true, "mime-version": true, "reply-to": true,
	"content-type": true, "content-transfer-encoding": true, "content-disposition": true,
}

// Compose renders d into a message.
func Compose(ctx context.Context, d *Draft, opts Options) (*Message, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Rand == nil {
		opts.Rand = rand.Reader
	}
	if d.Original == nil && len(d.OriginalRaw) > 0 {
		orig, err := parse.ParseContext(ctx, d.OriginalRaw, parse.Options{})
		if err != nil {
			return nil, err
		}
		dd := *d
		dd.Original = orig
		d = &dd
	}
	if d.Kind != KindNew && d.Original == nil && d.Kind != KindReply && d.Kind != KindReplyAll {
		return nil, ErrNoOriginal
	}

	m := &Message{}
	// ---- Sender ----
	ident := domain.Address{Name: d.Identity.Name, Email: d.Identity.Email}
	switch {
	case d.OnBehalfOf != nil && d.OnBehalfOf.Email != "":
		m.From = *d.OnBehalfOf
		if !strings.EqualFold(ident.Email, m.From.Email) && ident.Email != "" {
			s := ident
			m.Sender = &s
		}
	case d.From != nil && d.From.Email != "":
		m.From = *d.From
	default:
		m.From = ident
	}
	if m.From.Email == "" {
		return nil, ErrNoSender
	}
	m.EnvelopeFrom = m.From.Email
	if m.Sender != nil {
		m.EnvelopeFrom = m.Sender.Email
	}

	// ---- Threading ----
	if d.Original != nil {
		oh := d.Original.Header
		if d.Kind.IsReply() {
			if d.InReplyTo == "" {
				d2 := *d
				d2.InReplyTo = oh.MessageID
				d = &d2
			}
			if d.References == nil {
				d2 := *d
				d2.References = References(oh, opts.MaxReferences)
				d = &d2
			}
		} else if d.Kind.IsForward() && !d.NoForwardReferences && d.References == nil {
			d2 := *d
			d2.References = References(oh, opts.MaxReferences)
			d = &d2
		}
		if d.Subject == "" && !d.KeepSubject {
			d2 := *d
			if d.Kind.IsReply() {
				d2.Subject = ReplySubject(oh.Subject, d.Locale, d.LocalizedPrefixes)
			} else if d.Kind.IsForward() {
				d2.Subject = ForwardSubject(oh.Subject, d.Locale, d.LocalizedPrefixes)
			}
			d = &d2
		}
	}
	m.InReplyTo = threading.NormalizeMessageID(d.InReplyTo)
	for _, r := range d.References {
		if id := threading.NormalizeMessageID(r); id != "" {
			m.References = append(m.References, id)
		}
	}
	m.Subject = sanitizeHeaderText(d.Subject)

	// ---- Date & Message-ID ----
	m.Date = d.Date
	if m.Date.IsZero() {
		m.Date = opts.Now()
	}
	m.MessageID = threading.NormalizeMessageID(d.MessageID)
	if m.MessageID == "" {
		id, err := NewMessageID(opts.Rand, m.From.Email)
		if err != nil {
			return nil, err
		}
		m.MessageID = id
	}

	// ---- Body ----
	b := buildBodies(d)
	m.Text, m.HTML = b.text, b.html
	entity, err := buildEntity(ctx, d, b, opts)
	if err != nil {
		return nil, err
	}
	m.Body = entity

	// ---- Header ----
	add := func(name, value string) {
		if value != "" {
			m.Header = append(m.Header, Field{Name: name, Value: fold(name, value)})
		}
	}
	add("Date", m.Date.Format(time.RFC1123Z))
	add("From", formatAddress(m.From))
	if m.Sender != nil {
		add("Sender", formatAddress(*m.Sender))
	}
	add("To", formatAddressList(d.To))
	add("Cc", formatAddressList(d.Cc))
	replyTo := d.ReplyTo
	if len(replyTo) == 0 && d.Identity.ReplyTo != "" && !strings.EqualFold(d.Identity.ReplyTo, m.From.Email) {
		replyTo = []domain.Address{{Email: d.Identity.ReplyTo}}
	}
	add("Reply-To", formatAddressList(replyTo))
	add("Subject", encodeText(m.Subject))
	add("Message-ID", "<"+m.MessageID+">")
	if m.InReplyTo != "" {
		add("In-Reply-To", "<"+m.InReplyTo+">")
	}
	if len(m.References) > 0 {
		refs := make([]string, len(m.References))
		for i, r := range m.References {
			refs[i] = "<" + r + ">"
		}
		add("References", strings.Join(refs, " "))
	}
	add("MIME-Version", "1.0")
	if d.AutoSubmitted != "" {
		add("Auto-Submitted", sanitizeHeaderText(d.AutoSubmitted))
	}
	if d.RequestReadReceipt {
		add("Disposition-Notification-To", formatAddress(m.From))
	}
	for _, h := range d.Headers {
		if !validHeaderName(h.Name) {
			return nil, fmt.Errorf("%w %q", ErrBadHeaderName, h.Name)
		}
		if reservedHeaders[strings.ToLower(h.Name)] {
			return nil, fmt.Errorf("%w %q (reserved)", ErrBadHeaderName, h.Name)
		}
		if strings.EqualFold(h.Name, "Auto-Submitted") && d.AutoSubmitted != "" {
			continue
		}
		add(h.Name, encodeText(h.Value))
	}
	if d.Autocrypt != nil {
		f := d.Autocrypt.Field()
		m.Header = append(m.Header, Field{Name: f.Name, Value: f.Value})
	}

	// ---- Envelope recipients ----
	seen := map[string]bool{}
	for _, list := range [][]domain.Address{d.To, d.Cc, d.Bcc} {
		for _, a := range list {
			e := strings.TrimSpace(a.Email)
			if e != "" && !seen[strings.ToLower(e)] {
				seen[strings.ToLower(e)] = true
				m.Recipients = append(m.Recipients, e)
			}
		}
	}
	if d.Identity.BccSelf && d.Identity.Email != "" && !seen[strings.ToLower(d.Identity.Email)] {
		m.Recipients = append(m.Recipients, d.Identity.Email)
	}
	return m, nil
}

// buildEntity creates the MIME body.
func buildEntity(ctx context.Context, d *Draft, b bodies, opts Options) ([]byte, error) {
	// The counter keeps nested boundaries distinct even with a
	// deterministic random source; "=_" cannot occur in QP or base64 bodies.
	level := 0
	boundary := func() (string, error) {
		t, err := randomToken(opts.Rand, 12)
		level++
		return fmt.Sprintf("=_tm%d_%s", level, t), err
	}
	var inline, attached []*part

	// Inline parts of the original referenced by the quoted HTML.
	if d.Original != nil && b.html != "" {
		for _, cid := range b.cids {
			if i, ok := d.Original.InlineByCID[cid]; ok && i < len(d.Original.Attachments) {
				oa := d.Original.Attachments[i]
				if oa.Data != nil {
					inline = append(inline, attachmentPart(Attachment{Filename: oa.Filename, ContentType: oa.MIMEType, ContentID: cid, Inline: true}, oa.Data))
				}
			}
		}
	}
	// Original attachments on inline forwards.
	if d.Original != nil && d.Kind == KindForward && !d.OmitOriginalAttachments {
		used := map[string]bool{}
		for _, c := range b.cids {
			used[c] = true
		}
		for _, oa := range d.Original.Attachments {
			// Inline images already re-attached for the quoted HTML are
			// skipped; for plain-text forwards they become attachments.
			if oa.Data == nil || b.html != "" && oa.ContentID != "" && used[oa.ContentID] {
				continue
			}
			attached = append(attached, attachmentPart(Attachment{Filename: oa.Filename, ContentType: oa.MIMEType}, oa.Data))
		}
	}
	if d.Kind == KindForwardAsAttachment && d.Original != nil {
		name := d.Original.Header.Subject
		if name == "" {
			name = "message"
		}
		attached = append(attached, attachmentPart(Attachment{Filename: name + ".eml", ContentType: "message/rfc822"}, d.Original.Raw))
	}
	// Stored signature images referenced by the HTML (cid:).
	if len(b.sigImages) > 0 {
		used := map[string]bool{}
		for _, c := range signature.CIDs(b.html) {
			used[c] = true
		}
		for _, im := range b.sigImages {
			if !used[im.ContentID] {
				continue
			}
			a := Attachment{Filename: im.Filename, ContentType: im.MIMEType, ContentID: im.ContentID, Inline: true, BlobRef: im.BlobHash}
			data, err := attachmentData(ctx, a, opts.Blobs)
			if err != nil {
				// A missing image must not stop the mail.
				continue
			}
			inline = append(inline, attachmentPart(a, data))
		}
	}
	for _, a := range b.dataImages {
		inline = append(inline, attachmentPart(a, a.Data))
	}
	for _, a := range d.Attachments {
		data, err := attachmentData(ctx, a, opts.Blobs)
		if err != nil {
			return nil, fmt.Errorf("compose: attachment %q: %w", a.Filename, err)
		}
		p := attachmentPart(a, data)
		if (a.Inline || a.ContentID != "") && b.html != "" && a.ContentID != "" {
			inline = append(inline, p)
		} else {
			attached = append(attached, p)
		}
	}

	var root *part
	if b.html == "" {
		root = textPart("plain", b.text)
	} else {
		bd, err := boundary()
		if err != nil {
			return nil, err
		}
		root = multipart("alternative", bd, textPart("plain", b.text), textPart("html", b.html))
	}
	if len(inline) > 0 {
		bd, err := boundary()
		if err != nil {
			return nil, err
		}
		root = multipart("related", bd, append([]*part{root}, inline...)...)
	}
	if len(attached) > 0 {
		bd, err := boundary()
		if err != nil {
			return nil, err
		}
		root = multipart("mixed", bd, append([]*part{root}, attached...)...)
	}
	return root.bytes(), nil
}

func attachmentData(ctx context.Context, a Attachment, blobs BlobResolver) ([]byte, error) {
	var rc io.ReadCloser
	var err error
	switch {
	case a.Data != nil:
		return a.Data, nil
	case a.Open != nil:
		rc, err = a.Open(ctx)
	case a.BlobRef != "" && blobs != nil:
		rc, err = blobs.OpenBlob(ctx, a.BlobRef)
	default:
		return nil, errors.New("no content")
	}
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// NewMessageID returns a new random Message-ID (without brackets) in the
// domain of the sender address.
func NewMessageID(r io.Reader, sender string) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	tok, err := randomToken(r, 16)
	if err != nil {
		return "", err
	}
	dom := "tuxmail.invalid"
	if i := strings.LastIndexByte(sender, '@'); i >= 0 && i+1 < len(sender) {
		dom = strings.ToLower(strings.Trim(sender[i+1:], "<> "))
	}
	return tok + "@" + dom, nil
}

// ReplyDraft prepares a reply, reply-all or forward draft from orig:
// recipients (replies), subject and threading headers. self lists the
// user's own addresses for reply-all.
func ReplyDraft(orig *parse.Message, kind Kind, ident domain.Identity, self []string) *Draft {
	d := &Draft{Identity: ident, Kind: kind, Original: orig}
	if kind.IsReply() {
		d.To, d.Cc = ReplyRecipients(orig.Header, self, kind == KindReplyAll)
	}
	return d
}
