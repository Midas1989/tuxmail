// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"context"
	"mime"
	"strings"
)

// SMIMEReader verifies/decrypts S/MIME messages (implemented by the app on
// internal/crypto/smime with the user's certificates).
type SMIMEReader interface {
	ReadSMIME(ctx context.Context, raw []byte) (*SMIMEResult, error)
}

// SMIMEResult is what an SMIMEReader returns.
type SMIMEResult struct {
	SMIMEInfo
	// Content is the inner MIME entity (decrypted or unwrapped opaque
	// signed data); nil to keep the parsed structure.
	Content []byte
}

// SMIMEInfo is the S/MIME status of a message.
type SMIMEInfo struct {
	Present     bool   `json:"present"`
	Signed      bool   `json:"signed"`
	Encrypted   bool   `json:"encrypted"`
	Decrypted   bool   `json:"decrypted"`
	Signature   string `json:"signature,omitempty"` // trusted | untrusted | mismatch | expired | invalid
	SignerEmail string `json:"signerEmail,omitempty"`
	SignerName  string `json:"signerName,omitempty"`
	Issuer      string `json:"issuer,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
}

// detectSMIME reports the top-level S/MIME structure without keys.
func detectSMIME(ct string, params map[string]string) (signed, encrypted bool) {
	switch ct {
	case "application/pkcs7-mime", "application/x-pkcs7-mime":
		if strings.EqualFold(params["smime-type"], "signed-data") {
			return true, false
		}
		return false, true
	case "multipart/signed":
		p := strings.ToLower(params["protocol"])
		return p == "application/pkcs7-signature" || p == "application/x-pkcs7-signature", false
	}
	return false, false
}

// applySMIME runs after applyPGP: it records the S/MIME status and, with a
// reader, replaces the body by the decrypted content. Signature parts
// (smime.p7s) are not listed as attachments.
func (p *parser) applySMIME(raw []byte) {
	m := p.msg
	ct, params, _ := mime.ParseMediaType(m.Headers.Get("Content-Type"))
	signed, encrypted := detectSMIME(strings.ToLower(ct), params)
	if !signed && !encrypted {
		return
	}
	m.SMIME = SMIMEInfo{Present: true, Signed: signed, Encrypted: encrypted}
	var kept []Attachment
	for _, a := range m.Attachments {
		mt := strings.ToLower(a.MIMEType)
		if mt == "application/pkcs7-signature" || mt == "application/x-pkcs7-signature" ||
			(encrypted && (mt == "application/pkcs7-mime" || mt == "application/x-pkcs7-mime")) {
			continue
		}
		kept = append(kept, a)
	}
	m.Attachments = kept
	if p.opts.SMIME == nil {
		return
	}
	res, err := p.opts.SMIME.ReadSMIME(p.ctx, raw)
	if err != nil {
		m.SMIME.Error = err.Error()
	}
	if res == nil {
		return
	}
	res.Present = true
	m.SMIME = res.SMIMEInfo
	if res.Content != nil && (res.Decrypted || (res.Signed && !strings.EqualFold(ct, "multipart/signed"))) {
		p.texts, p.htmls = nil, nil
		m.Attachments = nil
		m.InlineByCID = map[string]int{}
		m.CalendarParts = nil
		m.AttachedMessages = nil
		hdr, body := splitHeader(res.Content)
		inner := p.entity(hdr, body, "", 0, false)
		m.Root = &Part{Path: m.Root.Path, ContentType: m.Root.ContentType, Params: m.Root.Params,
			Size: m.Root.Size, Children: []*Part{inner}}
		// A decrypted message may itself be signed (sign-then-encrypt).
		var keep []Attachment
		for _, a := range m.Attachments {
			if !strings.Contains(strings.ToLower(a.MIMEType), "pkcs7") {
				keep = append(keep, a)
			}
		}
		m.Attachments = keep
	}
}
