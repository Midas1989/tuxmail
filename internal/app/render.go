// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"

	"github.com/Midas1989/Mailclient/internal/core/threading"
	"github.com/Midas1989/Mailclient/internal/mime/quote"
)

// BasicRenderer is the fallback MessageRenderer until internal/mime/parse
// and internal/mime/sanitize (step B) are wired in. For safety it never
// returns HTML: HTML-only mails are converted to plain text. Only UTF-8 and
// US-ASCII bodies are decoded correctly (other charsets pass through).
type BasicRenderer struct{}

const maxRenderBytes = 32 << 20

type walkedPart struct {
	att  RenderedAttachment
	body []byte
}

func walk(raw []byte, fn func(p walkedPart, text, html bool) bool) error {
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return err
	}
	index := 0
	return e.Walk(func(path []int, ent *message.Entity, err error) error {
		if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
			return err
		}
		if ent.MultipartReader() != nil {
			return nil
		}
		ct, ctParams, _ := ent.Header.ContentType()
		ct = strings.ToLower(ct)
		disp, dParams, _ := ent.Header.ContentDisposition()
		filename := dParams["filename"]
		if filename == "" {
			filename = ctParams["name"]
		}
		if dec, err := new(mime.WordDecoder).DecodeHeader(filename); err == nil {
			filename = dec
		}
		cid := strings.Trim(ent.Header.Get("Content-Id"), "<> ")
		body, rerr := io.ReadAll(io.LimitReader(ent.Body, maxRenderBytes))
		if rerr != nil && !message.IsUnknownEncoding(rerr) {
			return rerr
		}
		isAttachment := strings.EqualFold(disp, "attachment") || filename != "" ||
			(cid != "" && !strings.HasPrefix(ct, "text/")) ||
			(!strings.HasPrefix(ct, "text/") && ct != "" && ct != "message/delivery-status")
		p := walkedPart{body: body}
		if isAttachment {
			if filename == "" {
				filename = "attachment"
				if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
					filename += exts[0]
				}
			}
			if ct == "" {
				ct = "application/octet-stream"
			}
			p.att = RenderedAttachment{Index: index, Filename: filename, MIMEType: ct, Size: int64(len(body)), ContentID: cid,
				Inline: strings.EqualFold(disp, "inline") || (cid != "" && disp == "")}
			index++
			if !fn(p, false, false) {
				return errStopWalk
			}
			return nil
		}
		if !fn(p, ct == "" || ct == "text/plain", ct == "text/html") {
			return errStopWalk
		}
		return nil
	})
}

var errStopWalk = errors.New("stop")

// Render implements MessageRenderer.
func (BasicRenderer) Render(_ context.Context, raw []byte) (RenderedMessage, error) {
	var out RenderedMessage
	var plain, html strings.Builder
	err := walk(raw, func(p walkedPart, text, isHTML bool) bool {
		switch {
		case p.att.Filename != "":
			out.Attachments = append(out.Attachments, p.att)
			if strings.HasPrefix(p.att.MIMEType, "application/pgp-encrypted") || strings.HasSuffix(p.att.Filename, ".asc") {
				out.Encrypted = true
			}
		case text && plain.Len() == 0:
			plain.Write(p.body)
		case isHTML && html.Len() == 0:
			html.Write(p.body)
		}
		return true
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return out, err
	}
	out.Text = strings.TrimSpace(plain.String())
	if out.Text == "" && html.Len() > 0 {
		r := quote.HTML(html.String())
		out.Text = joinNonEmpty("\n\n", r.Visible, r.Quoted, r.Signature)
		out.HasRemoteContent = strings.Contains(strings.ToLower(html.String()), "src=\"http")
	}
	if h, err := mail.CreateReader(bytes.NewReader(raw)); err == nil || message.IsUnknownCharset(err) {
		if h != nil {
			out.InReplyTo = threading.ParseInReplyTo(h.Header.Get("In-Reply-To"))
			out.References = threading.ParseReferences(h.Header.Get("References"))
			if strings.Contains(strings.ToLower(h.Header.Get("Content-Type")), "multipart/encrypted") {
				out.Encrypted = true
			}
		}
	}
	return out, nil
}

// Attachment implements MessageRenderer.
func (BasicRenderer) Attachment(_ context.Context, raw []byte, index int) (RenderedAttachment, []byte, error) {
	var found *walkedPart
	err := walk(raw, func(p walkedPart, _, _ bool) bool {
		if p.att.Filename != "" && p.att.Index == index {
			found = &p
			return false
		}
		return true
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return RenderedAttachment{}, nil, err
	}
	if found == nil {
		return RenderedAttachment{}, nil, errNotFound
	}
	return found.att, found.body, nil
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
