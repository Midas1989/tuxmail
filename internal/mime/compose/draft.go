// SPDX-License-Identifier: AGPL-3.0-or-later

// Package compose builds RFC 5322 messages from drafts (docs/KONZEPT.md 8.2,
// 6.3 and 6.6): sender identities (Send-As / on behalf of), reply and
// forward bodies with localized attribution lines and header blocks,
// signatures (placement, chat short signature), threading headers,
// multipart/alternative with a generated plain-text fallback, inline images
// (multipart/related), attachments and forwarding as message/rfc822.
//
// All text parts are quoted-printable and all binary parts base64 encoded,
// so the generated body is 7-bit clean and can be signed with PGP/MIME
// without being altered in transit (the only exception is an attached
// 8-bit message/rfc822, which RFC 2046 forbids to encode).
package compose

import (
	"context"
	"io"
	"time"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
)

// Kind is the compose action.
type Kind string

const (
	KindNew                 Kind = ""
	KindReply               Kind = "reply"
	KindReplyAll            Kind = "reply_all"
	KindForward             Kind = "forward"
	KindForwardAsAttachment Kind = "forward_as_attachment"
)

// IsReply reports whether k answers the original.
func (k Kind) IsReply() bool { return k == KindReply || k == KindReplyAll }

// IsForward reports whether k forwards the original.
func (k Kind) IsForward() bool { return k == KindForward || k == KindForwardAsAttachment }

// QuoteMode positions the quoted original in replies.
type QuoteMode string

const (
	// QuoteBelow puts the quote below the new text (top-posting, default).
	QuoteBelow QuoteMode = ""
	// QuoteAbove puts the quote above the new text (bottom-posting).
	QuoteAbove QuoteMode = "above"
	// QuoteNone does not quote (messenger view default).
	QuoteNone QuoteMode = "none"
)

// SignatureMode selects which signature variant is used.
type SignatureMode string

const (
	SignatureFull  SignatureMode = ""      // Signature.HTML / Signature.Plain
	SignatureShort SignatureMode = "short" // ShortSignature (chat view)
	SignatureNone  SignatureMode = "none"
)

// Signature placements (domain.Signature.Placement).
const (
	// PlacementBelowReply puts the signature directly below the new text:
	// above the quote when top-posting, at the end when bottom-posting.
	PlacementBelowReply = "below_reply"
	// PlacementAboveQuote puts the signature directly above the quoted
	// original in both quote modes.
	PlacementAboveQuote = "above_quote"
)

// Attachment is a file to attach. Exactly one of Data, Open or BlobRef
// provides the content.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType,omitempty"` // default: by extension, else application/octet-stream
	// ContentID makes the attachment an inline part referenced as cid:<id>
	// from the HTML body (multipart/related).
	ContentID string `json:"contentId,omitempty"`
	Inline    bool   `json:"inline,omitempty"`
	Data      []byte `json:"data,omitempty"`
	// BlobRef references stored content, resolved by Options.Blobs.
	BlobRef string `json:"blobRef,omitempty"`
	// Open opens the content (e.g. a local file); not serializable.
	Open func(ctx context.Context) (io.ReadCloser, error) `json:"-"`
}

// Header is an extra header field. Values are plain text; non-ASCII is
// RFC 2047 encoded and line breaks are removed.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// QuoteOptions configure quoting in replies and forwards.
type QuoteOptions struct {
	Mode QuoteMode `json:"mode,omitempty"`
	// TimeZone for the attribution/forward dates (default time.Local).
	TimeZone *time.Location `json:"-"`
}

// Draft is a message to compose.
type Draft struct {
	Identity domain.Identity `json:"identity"`
	// From overrides the From address (free sender address for catch-all
	// domains, or "Send As" a shared mailbox). No Sender header is added.
	From *domain.Address `json:"from,omitempty"`
	// OnBehalfOf sends on behalf of another mailbox: From becomes this
	// address and Sender the identity (Exchange "Send on Behalf").
	OnBehalfOf *domain.Address `json:"onBehalfOf,omitempty"`

	To      []domain.Address `json:"to,omitempty"`
	Cc      []domain.Address `json:"cc,omitempty"`
	Bcc     []domain.Address `json:"bcc,omitempty"`
	ReplyTo []domain.Address `json:"replyTo,omitempty"` // default: Identity.ReplyTo
	Subject string           `json:"subject"`

	// TextBody / HTMLBody are the new content written by the user. With
	// only HTMLBody, the plain alternative is generated from it.
	TextBody string `json:"textBody,omitempty"`
	HTMLBody string `json:"htmlBody,omitempty"`

	Attachments []Attachment `json:"attachments,omitempty"`

	Kind Kind `json:"kind,omitempty"`
	// Original is the message replied to or forwarded.
	Original *parse.Message `json:"-"`
	// OriginalRaw is the raw original; it is parsed when Original is nil
	// (serialized drafts, e.g. scheduled sends).
	OriginalRaw []byte `json:"originalRaw,omitempty"`
	// InReplyTo / References override the values derived from Original.
	InReplyTo  string   `json:"inReplyTo,omitempty"`
	References []string `json:"references,omitempty"`
	// NoForwardReferences disables References: <original> on forwards
	// (KONZEPT 6.3: set by default so that other clients thread forwards).
	NoForwardReferences bool `json:"noForwardReferences,omitempty"`
	// OmitOriginalAttachments drops the original's attachments on inline
	// forwards.
	OmitOriginalAttachments bool `json:"omitOriginalAttachments,omitempty"`

	Quote QuoteOptions `json:"quote"`
	// Locale ("en", "de") for attribution lines and forward headers.
	Locale string `json:"locale,omitempty"`
	// LocalizedPrefixes uses "AW:"/"WG:" instead of "Re:"/"Fwd:" when the
	// subject is derived (off by default).
	LocalizedPrefixes bool `json:"localizedPrefixes,omitempty"`
	// KeepSubject keeps Subject as given even for replies/forwards; by
	// default an empty Subject is derived from the original.
	KeepSubject bool `json:"keepSubject,omitempty"`

	Signature      *domain.Signature `json:"signature,omitempty"`
	SignatureMode  SignatureMode     `json:"signatureMode,omitempty"`
	ShortSignature string            `json:"shortSignature,omitempty"`

	// Headers are extra header fields (e.g. List-Unsubscribe for mail merge).
	Headers []Header `json:"headers,omitempty"`
	// AutoSubmitted sets Auto-Submitted (RFC 3834), e.g. "auto-replied".
	AutoSubmitted string `json:"autoSubmitted,omitempty"`
	// RequestReadReceipt adds Disposition-Notification-To with the From address.
	RequestReadReceipt bool `json:"requestReadReceipt,omitempty"`
	// Autocrypt is added as Autocrypt header (Autocrypt Level 1).
	Autocrypt *pgp.AutocryptHeader `json:"-"`

	// Date defaults to Options.Now(); MessageID (without brackets) is
	// generated when empty.
	Date      time.Time `json:"date,omitempty"`
	MessageID string    `json:"messageId,omitempty"`
}

// BlobResolver opens stored attachment content.
type BlobResolver interface {
	OpenBlob(ctx context.Context, ref string) (io.ReadCloser, error)
}

// Options configure Compose.
type Options struct {
	Now   func() time.Time
	Rand  io.Reader // default crypto/rand
	Blobs BlobResolver
	// MaxReferences bounds References (default DefaultMaxReferences).
	MaxReferences int
}

// Field is a rendered header field; Value is encoded for transport and may
// be folded (CRLF + space).
type Field struct {
	Name  string
	Value string
}

// Message is a composed message.
type Message struct {
	// Header holds the non-Content-* header fields in output order.
	Header []Field
	// Body is the MIME entity: Content-* fields, empty line and body, CRLF
	// line endings. It is what PGP/MIME signs or encrypts.
	Body []byte

	MessageID string // normalized, without brackets
	Subject   string // unencoded
	Date      time.Time
	From      domain.Address
	Sender    *domain.Address
	// EnvelopeFrom is the SMTP MAIL FROM address (Sender if set, else From).
	EnvelopeFrom string
	// Recipients are the envelope recipients (To, Cc, Bcc, Bcc-self).
	Recipients []string
	// InReplyTo and References as written (normalized IDs).
	InReplyTo  string
	References []string
	// Text and HTML are the final bodies (with signature and quote).
	Text, HTML string
}

// Bytes returns the complete RFC 5322 message.
func (m *Message) Bytes() []byte {
	var b []byte
	for _, f := range m.Header {
		b = append(b, f.Name...)
		b = append(b, ": "...)
		b = append(b, f.Value...)
		b = append(b, "\r\n"...)
	}
	return append(b, m.Body...)
}

// Get returns the first header value with the given name (case-insensitive).
func (m *Message) Get(name string) string {
	for _, f := range m.Header {
		if equalFold(f.Name, name) {
			return f.Value
		}
	}
	return ""
}

// Set replaces (or appends) a header field.
func (m *Message) Set(name, value string) {
	for i, f := range m.Header {
		if equalFold(f.Name, name) {
			m.Header[i].Value = value
			return
		}
	}
	m.Header = append(m.Header, Field{Name: name, Value: value})
}

// Del removes all header fields with the given name.
func (m *Message) Del(name string) {
	out := m.Header[:0]
	for _, f := range m.Header {
		if !equalFold(f.Name, name) {
			out = append(out, f)
		}
	}
	m.Header = out
}
