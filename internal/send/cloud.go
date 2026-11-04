// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/mime/compose"
	"github.com/Midas1989/Mailclient/internal/mime/sanitize"
	"github.com/Midas1989/Mailclient/internal/store"
)

// DefaultCloudExpiry is the default share lifetime (KONZEPT 12.2).
const DefaultCloudExpiry = 14 * 24 * time.Hour

type uploadedShare struct {
	name string
	size int64
	Share
	linkID int64
}

func htmlToText(s string) string { return sanitize.ToText(s) }

// openAttachment returns the content of an attachment.
func openAttachment(ctx context.Context, a compose.Attachment, blobs BlobResolver) (io.ReadCloser, int64, error) {
	switch {
	case a.Data != nil:
		return io.NopCloser(bytes.NewReader(a.Data)), int64(len(a.Data)), nil
	case a.Open != nil:
		rc, err := a.Open(ctx)
		return rc, -1, err
	case a.BlobRef != "" && blobs != nil:
		rc, err := blobs.OpenBlob(ctx, a.BlobRef)
		return rc, -1, err
	}
	return nil, 0, fmt.Errorf("send: attachment %q has no content", a.Filename)
}

// materialize reads Open-only attachments into Data so that a request can be
// serialized. BlobRefs stay references unless all is set.
func materialize(ctx context.Context, as []compose.Attachment, blobs BlobResolver, all bool) error {
	for i := range as {
		a := &as[i]
		if a.Data != nil || (a.BlobRef != "" && !all) || a.Open == nil && a.BlobRef == "" {
			continue
		}
		rc, _, err := openAttachment(ctx, *a, blobs)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("send: read attachment %q: %w", a.Filename, err)
		}
		a.Data, a.Open = data, nil
	}
	return nil
}

// uploadCloud uploads the cloud files and records the links.
func (s *Service) uploadCloud(ctx context.Context, req *Request, res *Result) ([]uploadedShare, error) {
	if s.cfg.Cloud == nil {
		return nil, ErrNoCloudUploader
	}
	c := &req.Cloud
	if c.Delivery == "" {
		c.Delivery = PasswordInMail
	}
	if c.Delivery == PasswordMessenger && s.cfg.Messenger == nil {
		return nil, ErrNoMessenger
	}
	expiry := c.Expiry
	if expiry <= 0 {
		expiry = DefaultCloudExpiry
	}
	var out []uploadedShare
	for i, f := range c.Files {
		rc, size, err := openAttachment(ctx, f, s.cfg.Blobs)
		if err != nil {
			return nil, err
		}
		var body io.Reader = rc
		if size < 0 {
			// Unknown size: buffer to report it in the link block.
			data, err := io.ReadAll(rc)
			if err != nil {
				rc.Close()
				return nil, err
			}
			size, body = int64(len(data)), bytes.NewReader(data)
		}
		account := c.AccountID
		if i < len(c.FileAccounts) && c.FileAccounts[i] != 0 {
			account = c.FileAccounts[i]
		}
		share, err := s.cfg.Cloud.Upload(ctx, account, UploadFile{
			Name: f.Filename, ContentType: f.ContentType, Size: size, Body: body,
			Subject: req.Draft.Subject, Expiry: expiry,
		})
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("send: upload %q: %w", f.Filename, err)
		}
		if share.Expires.IsZero() {
			share.Expires = s.cfg.Now().Add(expiry)
		}
		us := uploadedShare{name: f.Filename, size: size, Share: share}
		if s.cfg.CloudLinks != nil {
			ref := ""
			if share.Password != "" && s.cfg.Secrets != nil {
				ref = fmt.Sprintf("cloudlink/%d/%d/%s", s.cfg.Now().UnixNano(), i, f.Filename)
				if err := s.cfg.Secrets.Set(ref, []byte(share.Password)); err != nil {
					ref = ""
				}
			}
			id, err := s.cfg.CloudLinks.CreateCloudLink(ctx, store.CloudLink{
				AccountID: account, Provider: share.Provider, RemotePath: share.RemotePath, ShareID: share.ShareID,
				URL: share.URL, PasswordRef: ref, FileName: f.Filename, ExpiresAt: share.Expires,
			})
			if err == nil {
				us.linkID = id
			} else {
				res.Warnings = append(res.Warnings, "cloud_link_record: "+err.Error())
			}
		}
		out = append(out, us)
		sr := ShareResult{FileName: f.Filename, URL: share.URL, Expires: share.Expires, LinkID: us.linkID}
		if c.Delivery == PasswordNone {
			sr.Password = share.Password
		}
		res.Shares = append(res.Shares, sr)
	}
	return out, nil
}

// formatSize renders a byte count (decimal comma for German).
func formatSize(n int64, locale string) string {
	var s string
	switch {
	case n >= 1<<30:
		s = fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		s = fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		s = fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		s = fmt.Sprintf("%d B", n)
	}
	if strings.HasPrefix(strings.ToLower(locale), "de") {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}

// insertLinkBlock appends the localized cloud link block to the new text
// (before signature and quote, which compose adds).
func insertLinkBlock(d *compose.Draft, shares []uploadedShare, mode PasswordDelivery, locale string) {
	t := compose.CloudLinkTexts(locale)
	var plain []string
	var htm []string
	plain = append(plain, t.Intro)
	htm = append(htm, `<div class="tuxmail-cloudlinks"><p>`+html.EscapeString(t.Intro)+`</p><ul>`)
	anyPassword := false
	for _, s := range shares {
		size := formatSize(s.size, locale)
		valid := ""
		if !s.Expires.IsZero() {
			valid = " (" + t.ValidUntil + " " + s.Expires.Local().Format(t.DateLayout) + ")"
		}
		plain = append(plain, "", "📎 "+s.name+" ("+size+")", t.Link+": "+s.URL)
		li := `<li>📎 <a href="` + html.EscapeString(s.URL) + `">` + html.EscapeString(s.name) + `</a> (` + size + `)`
		if s.Password != "" {
			anyPassword = true
		}
		if s.Password != "" && mode == PasswordInMail {
			plain = append(plain, t.Password+": "+s.Password+valid)
			li += `<br>` + html.EscapeString(t.Password) + `: <code>` + html.EscapeString(s.Password) + `</code>` + html.EscapeString(valid)
		} else if valid != "" {
			plain[len(plain)-1] += valid
			li += html.EscapeString(valid)
		}
		htm = append(htm, li+`</li>`)
	}
	htm = append(htm, `</ul>`)
	if anyPassword && (mode == PasswordSeparateMail || mode == PasswordMessenger) {
		plain = append(plain, "", t.PasswordSeparate)
		htm = append(htm, `<p>`+html.EscapeString(t.PasswordSeparate)+`</p>`)
	}
	htm = append(htm, `</div>`)
	block := strings.Join(plain, "\n")
	if strings.TrimSpace(d.TextBody) != "" || d.HTMLBody == "" {
		d.TextBody = strings.TrimRight(d.TextBody, "\n ")
		if d.TextBody != "" {
			d.TextBody += "\n\n"
		}
		d.TextBody += block
	}
	if d.HTMLBody != "" {
		d.HTMLBody = appendToHTML(d.HTMLBody, strings.Join(htm, ""))
	}
}

// appendToHTML inserts fragment before </body> (or at the end).
func appendToHTML(doc, fragment string) string {
	if i := strings.LastIndex(strings.ToLower(doc), "</body>"); i >= 0 {
		return doc[:i] + fragment + doc[i:]
	}
	return doc + fragment
}

// deliverPasswords sends the share passwords by separate mail or messenger.
func (s *Service) deliverPasswords(ctx context.Context, req *Request, msg *compose.Message, shares []uploadedShare, res *Result) {
	var lines []string
	for _, sh := range shares {
		if sh.Password != "" {
			lines = append(lines, sh.name+": "+sh.Password)
		}
	}
	if len(lines) == 0 {
		return
	}
	t := compose.CloudLinkTexts(req.Draft.Locale)
	r := strings.NewReplacer("{subject}", msg.Subject, "{list}", strings.Join(lines, "\n"))
	text := r.Replace(t.PasswordMailBody)
	switch req.Cloud.Delivery {
	case PasswordSeparateMail:
		d := req.Draft
		pw := Request{
			Draft: compose.Draft{
				Identity: d.Identity, From: d.From, OnBehalfOf: d.OnBehalfOf,
				To: d.To, Cc: d.Cc, Bcc: d.Bcc, Subject: r.Replace(t.PasswordMailSubject),
				TextBody: text, Locale: d.Locale, SignatureMode: compose.SignatureNone,
				References: []string{msg.MessageID},
			},
			PGP:    PGPOptions{Sign: req.PGP.Sign, Encrypt: req.PGP.Encrypt, NoAutocrypt: req.PGP.NoAutocrypt},
			Source: req.Source, Chain: req.Chain,
		}
		if pw.PGP.Encrypt == PGPOn && !res.Encrypted {
			pw.PGP.Encrypt = PGPAuto
		}
		pr, err := s.deliver(ctx, pw, &Result{})
		if err != nil {
			res.Warnings = append(res.Warnings, "password_mail: "+err.Error())
			return
		}
		res.PasswordMailMessageID = pr.MessageID
	case PasswordMessenger:
		for _, to := range req.Draft.To {
			if err := s.cfg.Messenger.SendPassword(ctx, req.Cloud.MessengerAccountID, to, text); err != nil {
				res.Warnings = append(res.Warnings, "password_messenger: "+err.Error())
			}
		}
	}
}
