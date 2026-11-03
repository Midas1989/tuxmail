// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"crypto"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// SignOptions configures SignMIME.
type SignOptions struct {
	// ProtectedHeaders are copied into the signed inner part (see
	// AddProtectedHeaders). For signed-only messages the outer headers stay
	// unchanged.
	ProtectedHeaders []HeaderField
	// Now overrides the signature time (mainly for tests).
	Now time.Time
}

// SignMIME builds an RFC 3156 multipart/signed entity around inner, which
// must be a complete MIME entity (header fields, empty line, body). The
// line endings of inner are canonicalised to CRLF before signing. Callers
// should make sure inner is 7-bit clean (quoted-printable or base64
// transfer encoding) and has no trailing whitespace, otherwise MTAs may
// break the signature. signer must be an unlocked secret key.
func SignMIME(inner []byte, signer *Key, opts SignOptions) (*MIMEEntity, error) {
	if err := checkSigner(signer, opts.Now); err != nil {
		return nil, err
	}
	inner = CanonicalizeCRLF(inner)
	if len(opts.ProtectedHeaders) > 0 {
		var err error
		if inner, err = AddProtectedHeaders(inner, opts.ProtectedHeaders); err != nil {
			return nil, err
		}
	}
	sc, err := signer.Clone() // private copy: go-crypto mutates cached state
	if err != nil {
		return nil, err
	}
	var sigBuf bytes.Buffer
	if err := openpgp.DetachSign(&sigBuf, []*openpgp.Entity{sc.entity}, bytes.NewReader(inner), configAt(opts.Now)); err != nil {
		return nil, fmt.Errorf("pgp: sign: %w", err)
	}
	micalg, err := micalgOf(sigBuf.Bytes())
	if err != nil {
		return nil, err
	}
	var armored bytes.Buffer
	if err := writeArmored(&armored, armorSignature, sigBuf.Bytes()); err != nil {
		return nil, err
	}

	boundary := randomBoundary()
	var body bytes.Buffer
	body.WriteString("This is an OpenPGP/MIME signed message (RFC 4880 and 3156)\r\n")
	body.WriteString("--" + boundary + "\r\n")
	body.Write(inner)
	body.WriteString("\r\n--" + boundary + "\r\n")
	body.WriteString("Content-Type: application/pgp-signature; name=\"signature.asc\"\r\n")
	body.WriteString("Content-Description: OpenPGP digital signature\r\n")
	body.WriteString("Content-Disposition: attachment; filename=\"signature.asc\"\r\n\r\n")
	body.Write(CanonicalizeCRLF(armored.Bytes()))
	body.WriteString("\r\n--" + boundary + "--\r\n")

	ct := mime.FormatMediaType("multipart/signed", map[string]string{
		"micalg":   micalg,
		"protocol": "application/pgp-signature",
		"boundary": boundary,
	})
	return &MIMEEntity{
		Header: []HeaderField{{Name: "Content-Type", Value: ct}},
		Body:   body.Bytes(),
		Micalg: micalg,
	}, nil
}

// EncryptOptions configures EncryptMIME.
type EncryptOptions struct {
	// Signer, if set, must be an unlocked secret key; the message is then
	// signed and encrypted in one OpenPGP message (RFC 3156 section 6.2).
	Signer *Key
	// EncryptToSelf adds Self (or, if Self is nil, Signer) to the
	// recipients so that the sender can read the message in the Sent folder.
	EncryptToSelf bool
	Self          *Key
	// ProtectedHeaders are copied into the encrypted inner part.
	ProtectedHeaders []HeaderField
	// HideSubject replaces the outer subject with HiddenSubject (see
	// MIMEEntity.OuterSubject). The real subject must be part of
	// ProtectedHeaders; if it is missing, HideSubject is ignored.
	HideSubject bool
	// Now overrides the encryption/signature time (mainly for tests).
	Now time.Time
}

// EncryptMIME builds an RFC 3156 multipart/encrypted entity containing
// inner (a complete MIME entity) encrypted to recipients.
func EncryptMIME(inner []byte, recipients []*Key, opts EncryptOptions) (*MIMEEntity, error) {
	inner = CanonicalizeCRLF(inner)
	outerSubject := ""
	if len(opts.ProtectedHeaders) > 0 {
		var err error
		if inner, err = AddProtectedHeaders(inner, opts.ProtectedHeaders); err != nil {
			return nil, err
		}
		if opts.HideSubject {
			for _, f := range opts.ProtectedHeaders {
				if strings.EqualFold(f.Name, "Subject") {
					outerSubject = HiddenSubject
				}
			}
		}
	}
	armored, err := encryptArmored(inner, recipients, opts)
	if err != nil {
		return nil, err
	}

	boundary := randomBoundary()
	var body bytes.Buffer
	body.WriteString("This is an OpenPGP/MIME encrypted message (RFC 4880 and 3156)\r\n")
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Type: application/pgp-encrypted\r\n")
	body.WriteString("Content-Description: PGP/MIME version identification\r\n\r\n")
	body.WriteString("Version: 1\r\n\r\n")
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Type: application/octet-stream; name=\"encrypted.asc\"\r\n")
	body.WriteString("Content-Description: OpenPGP encrypted message\r\n")
	body.WriteString("Content-Disposition: inline; filename=\"encrypted.asc\"\r\n\r\n")
	body.Write(CanonicalizeCRLF(armored))
	body.WriteString("\r\n--" + boundary + "--\r\n")

	ct := mime.FormatMediaType("multipart/encrypted", map[string]string{
		"protocol": "application/pgp-encrypted",
		"boundary": boundary,
	})
	return &MIMEEntity{
		Header:       []HeaderField{{Name: "Content-Type", Value: ct}},
		Body:         body.Bytes(),
		OuterSubject: outerSubject,
	}, nil
}

// EncryptInline encrypts (and optionally signs) plaintext into an armored
// "-----BEGIN PGP MESSAGE-----" block for inline PGP. PGP/MIME should be
// preferred; inline PGP is provided for interoperability.
func EncryptInline(plaintext []byte, recipients []*Key, opts EncryptOptions) (string, error) {
	b, err := encryptArmored(plaintext, recipients, opts)
	return string(b), err
}

// ClearSign produces an inline cleartext signature
// ("-----BEGIN PGP SIGNED MESSAGE-----") over text.
func ClearSign(text []byte, signer *Key, now time.Time) (string, error) {
	if err := checkSigner(signer, now); err != nil {
		return "", err
	}
	sc, err := signer.Clone()
	if err != nil {
		return "", err
	}
	sk, ok := sc.entity.SigningKey(signerTime(now), nil)
	if !ok || sk.PrivateKey == nil {
		return "", ErrNoSigningKey
	}
	var buf bytes.Buffer
	w, err := clearsign.Encode(&buf, sk.PrivateKey, configAt(now))
	if err != nil {
		return "", fmt.Errorf("pgp: clearsign: %w", err)
	}
	if _, err := w.Write(text); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	buf.WriteByte('\n')
	return buf.String(), nil
}

func encryptArmored(plaintext []byte, recipients []*Key, opts EncryptOptions) ([]byte, error) {
	now := signerTime(opts.Now)
	to := make([]*openpgp.Entity, 0, len(recipients)+1)
	seen := map[string]bool{}
	addRecipient := func(k *Key) error {
		if k == nil || seen[k.Fingerprint()] {
			return nil
		}
		if !k.CanEncrypt(now) {
			return fmt.Errorf("%w: %s", ErrNoEncryptionKey, k.Fingerprint())
		}
		seen[k.Fingerprint()] = true
		to = append(to, k.Public().entity)
		return nil
	}
	for _, r := range recipients {
		if err := addRecipient(r); err != nil {
			return nil, err
		}
	}
	if opts.EncryptToSelf {
		self := opts.Self
		if self == nil {
			self = opts.Signer
		}
		if err := addRecipient(self); err != nil {
			return nil, err
		}
	}
	if len(to) == 0 {
		return nil, ErrNoRecipients
	}
	var signers []*openpgp.Entity
	if opts.Signer != nil {
		if err := checkSigner(opts.Signer, opts.Now); err != nil {
			return nil, err
		}
		sc, err := opts.Signer.Clone()
		if err != nil {
			return nil, err
		}
		signers = []*openpgp.Entity{sc.entity}
	}

	var out bytes.Buffer
	aw, err := armor.EncodeWithChecksumOption(&out, armorMessage, nil, false)
	if err != nil {
		return nil, err
	}
	pw, err := openpgp.EncryptWithParams(aw, to, nil, &openpgp.EncryptParams{
		Signers: signers,
		Config:  configAt(opts.Now),
	})
	if err != nil {
		return nil, fmt.Errorf("pgp: encrypt: %w", err)
	}
	if _, err := io.Copy(pw, bytes.NewReader(plaintext)); err != nil {
		return nil, fmt.Errorf("pgp: encrypt: %w", err)
	}
	if err := pw.Close(); err != nil {
		return nil, fmt.Errorf("pgp: encrypt: %w", err)
	}
	if err := aw.Close(); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func signerTime(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now()
	}
	return now
}

func checkSigner(signer *Key, now time.Time) error {
	if signer == nil {
		return ErrNotSecretKey
	}
	if !signer.IsSecret() {
		return ErrNotSecretKey
	}
	if signer.IsLocked() {
		return ErrKeyLocked
	}
	if !signer.CanSign(signerTime(now)) {
		return fmt.Errorf("%w: %s", ErrNoSigningKey, signer.Fingerprint())
	}
	return nil
}

// micalgOf derives the RFC 3156 micalg parameter from a binary signature.
func micalgOf(sig []byte) (string, error) {
	p, err := packet.Read(bytes.NewReader(sig))
	if err != nil {
		return "", fmt.Errorf("pgp: parse signature: %w", err)
	}
	s, ok := p.(*packet.Signature)
	if !ok {
		return "", fmt.Errorf("pgp: unexpected packet %T in signature", p)
	}
	return micalgName(s.Hash)
}

func micalgName(h crypto.Hash) (string, error) {
	switch h {
	case crypto.SHA1:
		return "pgp-sha1", nil
	case crypto.RIPEMD160:
		return "pgp-ripemd160", nil
	case crypto.SHA224:
		return "pgp-sha224", nil
	case crypto.SHA256:
		return "pgp-sha256", nil
	case crypto.SHA384:
		return "pgp-sha384", nil
	case crypto.SHA512:
		return "pgp-sha512", nil
	case crypto.SHA3_256:
		return "pgp-sha3-256", nil
	case crypto.SHA3_512:
		return "pgp-sha3-512", nil
	}
	return "", fmt.Errorf("pgp: unsupported signature hash %v", h)
}
