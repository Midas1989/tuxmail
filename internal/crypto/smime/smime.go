// SPDX-License-Identifier: AGPL-3.0-or-later

// Package smime implements S/MIME (RFC 8551) for TuxMail (KONZEPT 8.6):
// importing PKCS#12 identities, signing (multipart/signed with a detached
// application/pkcs7-signature), encrypting (application/pkcs7-mime
// enveloped-data, AES-256-CBC, RSA), and reading incoming messages
// (verify, decrypt, opaque signed-data) with trust through the system roots
// plus certificates the user approved. Certificates of signed incoming mail
// are returned so that the caller can collect them for later encryption.
//
// Like internal/crypto/pgp, the functions operate on MIME entities: the
// Content-* header fields, an empty line and the body, with CRLF line
// endings (compose.Message.Body).
package smime

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/smallstep/pkcs7"
	"software.sslmate.com/src/go-pkcs12"
)

func init() {
	// The library default is DES-CBC; S/MIME 4.0 requires AES. Set once.
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
}

// Errors.
var (
	ErrNoKey        = errors.New("smime: no private key")
	ErrUnsupported  = errors.New("smime: only RSA keys are supported for encryption")
	ErrNotSMIME     = errors.New("smime: not an S/MIME message")
	ErrNoRecipients = errors.New("smime: no recipient certificates")
	ErrNoDecryptKey = errors.New("smime: no matching private key to decrypt")
)

// Identity is an own certificate with its private key.
type Identity struct {
	Cert  *x509.Certificate
	Chain []*x509.Certificate // intermediates (without the end-entity)
	Key   crypto.Signer
}

// ImportPKCS12 reads a .p12/.pfx file (legacy RC2/3DES and modern
// PBES2/AES encodings).
func ImportPKCS12(data []byte, password string) (*Identity, error) {
	key, cert, chain, err := pkcs12.DecodeChain(data, password)
	if err != nil {
		return nil, fmt.Errorf("smime: PKCS#12: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, ErrNoKey
	}
	return &Identity{Cert: cert, Chain: chain, Key: signer}, nil
}

// ExportPKCS12 encodes an identity (modern encoding).
func ExportPKCS12(id *Identity, password string) ([]byte, error) {
	return pkcs12.Modern.Encode(id.Key, id.Cert, id.Chain, password)
}

// Fingerprint returns the SHA-256 fingerprint (hex, upper case).
func Fingerprint(c *x509.Certificate) string {
	s := sha256.Sum256(c.Raw)
	return strings.ToUpper(hex.EncodeToString(s[:]))
}

// Emails returns the e-mail addresses of a certificate (SAN and subject
// emailAddress), lower case.
func Emails(c *x509.Certificate) []string {
	set := map[string]bool{}
	var out []string
	add := func(e string) {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" && !set[e] {
			set[e] = true
			out = append(out, e)
		}
	}
	for _, e := range c.EmailAddresses {
		add(e)
	}
	oidEmail := []int{1, 2, 840, 113549, 1, 9, 1}
	for _, n := range c.Subject.Names {
		if n.Type.Equal(oidEmail) {
			if s, ok := n.Value.(string); ok {
				add(s)
			}
		}
	}
	return out
}

// CanEncryptTo reports whether a certificate is usable for encryption now.
func CanEncryptTo(c *x509.Certificate, now time.Time) bool {
	if now.Before(c.NotBefore) || now.After(c.NotAfter) {
		return false
	}
	if _, ok := c.PublicKey.(*rsa.PublicKey); !ok {
		return false
	}
	if c.KeyUsage != 0 && c.KeyUsage&(x509.KeyUsageKeyEncipherment) == 0 {
		return false
	}
	return hasEmailEKU(c)
}

func hasEmailEKU(c *x509.Certificate) bool {
	if len(c.ExtKeyUsage) == 0 {
		return true
	}
	for _, u := range c.ExtKeyUsage {
		if u == x509.ExtKeyUsageEmailProtection || u == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

// ==== sign / encrypt ============================================================

func newBoundary() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "----=_smime_" + hex.EncodeToString(b)
}

// base64Lines encodes data as base64 with 76-character CRLF lines.
func base64Lines(data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var b bytes.Buffer
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
	return b.Bytes()
}

// canonical converts line endings to CRLF.
func canonical(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n"))
}

// Sign wraps a MIME entity into multipart/signed with a detached signature
// (SHA-256). The signer's chain is included.
func Sign(entity []byte, id *Identity) ([]byte, error) {
	if id == nil || id.Key == nil {
		return nil, ErrNoKey
	}
	// The CRLF before the delimiter belongs to the delimiter (RFC 2046).
	entity = bytes.TrimSuffix(canonical(entity), []byte("\r\n"))
	sd, err := pkcs7.NewSignedData(entity)
	if err != nil {
		return nil, err
	}
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := sd.AddSignerChain(id.Cert, id.Key, id.Chain, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, fmt.Errorf("smime: sign: %w", err)
	}
	sd.Detach()
	der, err := sd.Finish()
	if err != nil {
		return nil, err
	}
	bd := newBoundary()
	var b bytes.Buffer
	fmt.Fprintf(&b, "Content-Type: multipart/signed; protocol=\"application/pkcs7-signature\"; micalg=sha-256;\r\n boundary=\"%s\"\r\n\r\n", bd)
	b.WriteString("This is a cryptographically signed message in MIME format.\r\n\r\n")
	fmt.Fprintf(&b, "--%s\r\n", bd)
	b.Write(entity)
	fmt.Fprintf(&b, "\r\n--%s\r\n", bd)
	b.WriteString("Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"smime.p7s\"\r\n")
	b.WriteString("Content-Description: S/MIME Cryptographic Signature\r\n\r\n")
	b.Write(base64Lines(der))
	fmt.Fprintf(&b, "--%s--\r\n", bd)
	return b.Bytes(), nil
}

// Encrypt encrypts a MIME entity to the recipients (and should include the
// sender's own certificate so that the Sent copy stays readable).
func Encrypt(entity []byte, recipients []*x509.Certificate) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, ErrNoRecipients
	}
	for _, c := range recipients {
		if _, ok := c.PublicKey.(*rsa.PublicKey); !ok {
			return nil, fmt.Errorf("%w (%s)", ErrUnsupported, c.Subject.CommonName)
		}
	}
	der, err := pkcs7.Encrypt(canonical(entity), recipients)
	if err != nil {
		return nil, fmt.Errorf("smime: encrypt: %w", err)
	}
	var b bytes.Buffer
	b.WriteString("Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("Content-Disposition: attachment; filename=\"smime.p7m\"\r\n")
	b.WriteString("Content-Description: S/MIME Encrypted Message\r\n\r\n")
	b.Write(base64Lines(der))
	return b.Bytes(), nil
}

// SignAndEncrypt signs, then encrypts (RFC 8551 order).
func SignAndEncrypt(entity []byte, id *Identity, recipients []*x509.Certificate) ([]byte, error) {
	signed, err := Sign(entity, id)
	if err != nil {
		return nil, err
	}
	return Encrypt(signed, recipients)
}
