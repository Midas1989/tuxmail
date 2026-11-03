// SPDX-License-Identifier: AGPL-3.0-or-later

package parse

import (
	"strings"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
)

// detectPGP inspects the parsed structure without keys.
func (p *parser) detectPGP() {
	m := p.msg
	root := m.Root
	switch {
	case root.ContentType == "multipart/encrypted" && strings.EqualFold(root.Params["protocol"], "application/pgp-encrypted"):
		m.PGP.Format, m.PGP.Encrypted = pgp.FormatPGPMIME, true
	case root.ContentType == "multipart/signed" && strings.EqualFold(root.Params["protocol"], "application/pgp-signature"):
		m.PGP.Format, m.PGP.Signed = pgp.FormatPGPMIME, true
	default:
		for _, t := range p.texts {
			if strings.Contains(t, "-----BEGIN PGP MESSAGE-----") {
				m.PGP.Format, m.PGP.Encrypted = pgp.FormatInline, true
				break
			}
			if strings.Contains(t, "-----BEGIN PGP SIGNED MESSAGE-----") {
				m.PGP.Format, m.PGP.Signed = pgp.FormatInline, true
				break
			}
		}
	}
	if m.PGP.Format == "" {
		m.PGP.Format = pgp.FormatNone
	}
}

// applyPGP detects OpenPGP content and, with a key ring, decrypts/verifies
// it. For decrypted messages the body tree, texts and attachments are
// replaced by those of the plaintext.
func (p *parser) applyPGP(raw []byte) {
	p.detectPGP()
	m := p.msg
	if m.PGP.Format == pgp.FormatNone || p.opts.KeyRing == nil {
		return
	}
	res, err := p.opts.KeyRing.ReadMessage(p.ctx, raw, p.opts.PGP)
	if err != nil {
		m.PGP.Error = err.Error()
	}
	if res == nil {
		return
	}
	m.PGP.Format = res.Format
	m.PGP.Encrypted = res.Encryption != pgp.EncNone
	m.PGP.Decrypted = res.Encryption == pgp.EncDecrypted
	m.PGP.Signed = res.Signed
	m.PGP.Signature = res.Signature
	m.PGP.Verified = res.Signature == pgp.SigValid
	m.PGP.SignerFingerprint = res.SignerFingerprint
	m.PGP.SignerKeyID = res.SignerKeyID
	m.PGP.SignatureTime = res.SignatureTime
	m.PGP.DecryptionKey = res.DecryptionKey
	m.PGP.EncryptedTo = res.EncryptedTo
	m.PGP.ProtectedHeaders = res.HasProtectedHeaders
	if res.SignatureError != nil && m.PGP.Error == "" {
		m.PGP.Error = res.SignatureError.Error()
	}
	if res.HasProtectedHeaders {
		if subj, ok := res.ProtectedHeader("Subject"); ok {
			m.Header.Subject = p.dec.text(subj)
		}
	}
	if res.InnerHeader != nil {
		m.AutocryptGossip = res.InnerHeader.Values("Autocrypt-Gossip")
	}
	// Replace the body with the decrypted (or inline-unarmored) content.
	if res.Content != nil && (m.PGP.Decrypted || res.Format == pgp.FormatInline) {
		p.texts, p.htmls = nil, nil
		m.Attachments = nil
		m.InlineByCID = map[string]int{}
		m.CalendarParts = nil
		m.AttachedMessages = nil
		hdr, body := splitHeader(res.Content)
		inner := p.entity(hdr, body, "", 0, false)
		m.Root = &Part{Path: m.Root.Path, ContentType: m.Root.ContentType, Params: m.Root.Params,
			Size: m.Root.Size, Children: []*Part{inner}}
	}
}
