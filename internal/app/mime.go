// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/mime/quote"
	"github.com/Midas1989/Mailclient/internal/mime/sanitize"
	"github.com/Midas1989/Mailclient/internal/sync/content"
)

// URL templates of the HTML sanitizer: inline parts are served by
// GET /api/messages/{id}/cid/{cid}; remote images by the proxy route.
const (
	CIDURLTemplate   = "/api/messages/{id}/cid/{cid}"
	ProxyURLTemplate = "/api/proxy/image?url={url}"
)

// MIMERenderer is the MessageRenderer on internal/mime/parse and
// internal/mime/sanitize (remote content blocked, trackers removed).
type MIMERenderer struct {
	San   *sanitize.Sanitizer
	Parse parse.Options
}

// NewMIMERenderer returns the default renderer.
func NewMIMERenderer() *MIMERenderer {
	return &MIMERenderer{San: sanitize.New(sanitize.Config{CIDURLTemplate: CIDURLTemplate, ProxyURLTemplate: ProxyURLTemplate})}
}

// messageIDKey carries the store message id for cid URLs.
type messageIDKey struct{}

// WithMessageID tells the renderer which message is rendered (cid URLs).
func WithMessageID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, messageIDKey{}, id)
}

func attachmentsOf(m *parse.Message) []RenderedAttachment {
	out := make([]RenderedAttachment, 0, len(m.Attachments))
	for i, a := range m.Attachments {
		out = append(out, RenderedAttachment{Index: i, Filename: a.Filename, MIMEType: a.MIMEType, Size: a.Size, ContentID: a.ContentID, Inline: a.Inline})
	}
	return out
}

// Render implements MessageRenderer.
func (r *MIMERenderer) Render(ctx context.Context, raw []byte) (RenderedMessage, error) {
	opts := r.Parse
	opts.SkipAttachmentData = true
	m, err := parse.ParseContext(ctx, raw, opts)
	if err != nil {
		return RenderedMessage{}, err
	}
	out := RenderedMessage{Text: m.Text, InReplyTo: m.Header.InReplyTo, References: m.Header.References, Attachments: attachmentsOf(m),
		Encrypted: m.PGP.Encrypted, Automated: m.IsAutomated()}
	if strings.TrimSpace(m.HTML) != "" {
		id, _ := ctx.Value(messageIDKey{}).(int64)
		res := r.San.Sanitize(m.HTML, sanitize.Options{MessageID: strconv.FormatInt(id, 10)})
		out.HTML, out.HasRemoteContent = res.HTML, res.HasRemote || res.BlockedRemote > 0
	}
	return out, nil
}

// Attachment implements MessageRenderer.
func (r *MIMERenderer) Attachment(ctx context.Context, raw []byte, index int) (RenderedAttachment, []byte, error) {
	m, err := parse.ParseContext(ctx, raw, r.Parse)
	if err != nil {
		return RenderedAttachment{}, nil, err
	}
	if index < 0 || index >= len(m.Attachments) {
		return RenderedAttachment{}, nil, errNotFound
	}
	a := m.Attachments[index]
	return RenderedAttachment{Index: index, Filename: a.Filename, MIMEType: a.MIMEType, Size: a.Size, ContentID: a.ContentID, Inline: a.Inline}, a.Data, nil
}

// ParseExtractor implements the sync content.Extractor with internal/mime/parse.
type ParseExtractor struct{}

// Extract implements content.Extractor.
func (ParseExtractor) Extract(raw []byte) (content.Content, error) {
	m, err := parse.Parse(raw, parse.Options{SkipAttachmentData: true})
	if err != nil {
		return content.Content{}, err
	}
	c := content.Content{Text: m.Text, HTMLPresent: m.HTML != "", AttachedMessageIDs: m.AttachedMessageIDs, CalendarUIDs: m.CalendarUIDs()}
	q := quote.Extract(m.Text, m.HTML)
	c.Snippet = parse.Snippet(q.Visible, "", parse.DefaultSnippetLen)
	if c.Snippet == "" {
		c.Snippet = m.Snippet
	}
	for _, a := range m.Attachments {
		c.Attachments = append(c.Attachments, content.Attachment{Name: a.Filename, MIME: a.MIMEType, Size: a.Size, ContentID: a.ContentID, Inline: a.Inline})
	}
	c.InlineForwards = threading.DetectForwards(m.Text)
	return c, nil
}
