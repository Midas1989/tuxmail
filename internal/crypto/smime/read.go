// SPDX-License-Identifier: AGPL-3.0-or-later

package smime

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/textproto"
	"strings"
	"time"

	"github.com/smallstep/pkcs7"
)

// SigStatus is the signature verdict.
type SigStatus string

const (
	SigNone      SigStatus = ""
	SigTrusted   SigStatus = "trusted"   // valid, chain to a trusted root, address matches
	SigUntrusted SigStatus = "untrusted" // valid signature, unknown/untrusted issuer
	SigMismatch  SigStatus = "mismatch"  // valid, but the certificate is not for the sender address
	SigExpired   SigStatus = "expired"   // valid, certificate outside its validity
	SigInvalid   SigStatus = "invalid"   // content was modified or the signature is broken
)

// ReadOptions configure Read.
type ReadOptions struct {
	// Identities are the own certificates/keys used for decryption.
	Identities []*Identity
	// Roots are trusted roots (system roots plus user-approved
	// certificates); nil means x509.SystemCertPool.
	Roots *x509.CertPool
	// Trusted are end-entity certificates the user approved explicitly
	// (trusted regardless of their issuer).
	Trusted []*x509.Certificate
	// From is the sender address to match against the certificate.
	From string
	Now  time.Time
}

// Result describes an S/MIME message.
type Result struct {
	Signed    bool      `json:"signed"`
	Encrypted bool      `json:"encrypted"`
	Decrypted bool      `json:"decrypted"`
	Signature SigStatus `json:"signature,omitempty"`
	// Signer describes the signer certificate.
	SignerEmail       string    `json:"signerEmail,omitempty"`
	SignerName        string    `json:"signerName,omitempty"`
	SignerIssuer      string    `json:"signerIssuer,omitempty"`
	SignerFingerprint string    `json:"signerFingerprint,omitempty"`
	SignerNotAfter    time.Time `json:"signerNotAfter,omitzero"`
	Error             string    `json:"error,omitempty"`
	// Content is the inner MIME entity (decrypted or unwrapped); nil if
	// nothing was unwrapped (multipart/signed keeps its structure, Content
	// is its first part).
	Content []byte `json:"-"`
	// Certificates found in signatures (to collect for encryption).
	Certificates []*x509.Certificate `json:"-"`
	// Signer is the signer certificate.
	Signer *x509.Certificate `json:"-"`
}

// IsSMIME reports whether a raw message (or entity) is S/MIME at the top
// level, without keys.
func IsSMIME(raw []byte) bool {
	h, _, err := splitEntity(raw)
	if err != nil {
		return false
	}
	ct, params, _ := mime.ParseMediaType(h.Get("Content-Type"))
	switch ct {
	case "application/pkcs7-mime", "application/x-pkcs7-mime":
		return true
	case "multipart/signed":
		p := strings.ToLower(params["protocol"])
		return p == "application/pkcs7-signature" || p == "application/x-pkcs7-signature"
	}
	return false
}

// Read verifies and/or decrypts a message (nested sign+encrypt included).
func Read(raw []byte, o ReadOptions) (*Result, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	res := &Result{}
	cur := raw
	for depth := 0; depth < 4; depth++ {
		h, body, err := splitEntity(cur)
		if err != nil {
			return res, err
		}
		ct, params, _ := mime.ParseMediaType(h.Get("Content-Type"))
		switch ct {
		case "application/pkcs7-mime", "application/x-pkcs7-mime":
			der, err := decodeBody(h, body)
			if err != nil {
				return res, err
			}
			p7, err := pkcs7.Parse(der)
			if err != nil {
				return res, fmt.Errorf("smime: parse: %w", err)
			}
			typ := strings.ToLower(params["smime-type"])
			if typ == "signed-data" || (typ == "" && len(p7.Signers) > 0) {
				res.Signed = true
				verify(p7, o, res)
				cur = canonical(p7.Content)
				res.Content = cur
				continue
			}
			// enveloped-data
			res.Encrypted = true
			plain, err := decrypt(p7, o.Identities)
			if err != nil {
				res.Error = err.Error()
				return res, nil
			}
			res.Decrypted = true
			cur = canonical(plain)
			res.Content = cur
			continue
		case "multipart/signed":
			p := strings.ToLower(params["protocol"])
			if p != "application/pkcs7-signature" && p != "application/x-pkcs7-signature" {
				return res, nil
			}
			res.Signed = true
			content, sigEntity, err := splitSigned(body, params["boundary"])
			if err != nil {
				res.Signature, res.Error = SigInvalid, err.Error()
				return res, nil
			}
			sh, sb, err := splitEntity(sigEntity)
			if err != nil {
				res.Signature, res.Error = SigInvalid, err.Error()
				return res, nil
			}
			der, err := decodeBody(sh, sb)
			if err != nil {
				res.Signature, res.Error = SigInvalid, err.Error()
				return res, nil
			}
			p7, err := pkcs7.Parse(der)
			if err != nil {
				res.Signature, res.Error = SigInvalid, err.Error()
				return res, nil
			}
			p7.Content = content
			verify(p7, o, res)
			res.Content = content
			return res, nil
		default:
			if depth == 0 {
				return res, ErrNotSMIME
			}
			return res, nil
		}
	}
	return res, nil
}

func decrypt(p7 *pkcs7.PKCS7, ids []*Identity) ([]byte, error) {
	if len(ids) == 0 {
		return nil, ErrNoDecryptKey
	}
	var lastErr error = ErrNoDecryptKey
	for _, id := range ids {
		plain, err := p7.Decrypt(id.Cert, id.Key)
		if err == nil {
			return plain, nil
		}
		lastErr = err
	}
	if errors.Is(lastErr, ErrNoDecryptKey) {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%w: %v", ErrNoDecryptKey, lastErr)
}

func verify(p7 *pkcs7.PKCS7, o ReadOptions, res *Result) {
	res.Certificates = append(res.Certificates, p7.Certificates...)
	signer := p7.GetOnlySigner()
	if err := p7.Verify(); err != nil {
		res.Signature, res.Error = SigInvalid, err.Error()
		return
	}
	if signer == nil && len(p7.Certificates) > 0 {
		signer = p7.Certificates[0]
	}
	if signer == nil {
		res.Signature = SigInvalid
		return
	}
	res.Signer = signer
	res.SignerName = signer.Subject.CommonName
	res.SignerIssuer = signer.Issuer.CommonName
	res.SignerFingerprint = Fingerprint(signer)
	res.SignerNotAfter = signer.NotAfter
	if em := Emails(signer); len(em) > 0 {
		res.SignerEmail = em[0]
	}
	res.Signature = trust(signer, p7.Certificates, o)
}

func trust(c *x509.Certificate, pool []*x509.Certificate, o ReadOptions) SigStatus {
	if o.Now.Before(c.NotBefore) || o.Now.After(c.NotAfter) {
		return SigExpired
	}
	if o.From != "" {
		match := false
		for _, e := range Emails(c) {
			if strings.EqualFold(e, o.From) {
				match = true
			}
		}
		if !match {
			return SigMismatch
		}
	}
	for _, t := range o.Trusted {
		if t.Equal(c) {
			return SigTrusted
		}
	}
	roots := o.Roots
	if roots == nil {
		var err error
		if roots, err = x509.SystemCertPool(); err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
	}
	inter := x509.NewCertPool()
	for _, p := range pool {
		if !p.Equal(c) {
			inter.AddCert(p)
		}
	}
	_, err := c.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: o.Now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}})
	if err != nil {
		return SigUntrusted
	}
	return SigTrusted
}

// ==== MIME helpers ===============================================================

func splitEntity(raw []byte) (textproto.MIMEHeader, []byte, error) {
	r := textproto.NewReader(bufio.NewReader(bytes.NewReader(raw)))
	h, err := r.ReadMIMEHeader()
	if err != nil && len(h) == 0 {
		return nil, nil, fmt.Errorf("smime: header: %w", err)
	}
	idx := bytes.Index(raw, []byte("\r\n\r\n"))
	n := 4
	if j := bytes.Index(raw, []byte("\n\n")); j >= 0 && (idx < 0 || j < idx) {
		idx, n = j, 2
	}
	if idx < 0 {
		return h, nil, nil
	}
	return h, raw[idx+n:], nil
}

func decodeBody(h textproto.MIMEHeader, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(h.Get("Content-Transfer-Encoding"))) {
	case "base64":
		clean := bytes.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, body)
		out := make([]byte, base64.StdEncoding.DecodedLen(len(clean)))
		n, err := base64.StdEncoding.Decode(out, clean)
		if err != nil {
			return nil, fmt.Errorf("smime: base64: %w", err)
		}
		return out[:n], nil
	}
	return body, nil
}

// splitSigned returns the exact bytes of the signed first part (without
// the CRLF before the next delimiter) and the signature entity.
func splitSigned(body []byte, boundary string) ([]byte, []byte, error) {
	if boundary == "" {
		return nil, nil, errors.New("smime: multipart/signed without boundary")
	}
	body = canonical(body)
	delim := []byte("--" + boundary)
	var parts [][]byte
	i := bytes.Index(body, delim)
	if i < 0 {
		return nil, nil, errors.New("smime: boundary not found")
	}
	for {
		start := i + len(delim)
		if bytes.HasPrefix(body[start:], []byte("--")) {
			break
		}
		// skip transport padding and CRLF
		nl := bytes.Index(body[start:], []byte("\r\n"))
		if nl < 0 {
			break
		}
		start += nl + 2
		next := bytes.Index(body[start:], append([]byte("\r\n"), delim...))
		if next < 0 {
			return nil, nil, errors.New("smime: unterminated part")
		}
		parts = append(parts, body[start:start+next])
		i = start + next + 2
	}
	if len(parts) != 2 {
		return nil, nil, fmt.Errorf("smime: multipart/signed has %d parts", len(parts))
	}
	return parts[0], parts[1], nil
}
