// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/textproto"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// MessageFormat is the OpenPGP format detected in a message.
type MessageFormat string

const (
	FormatNone    MessageFormat = "none"     // no OpenPGP content
	FormatPGPMIME MessageFormat = "pgp/mime" // RFC 3156
	FormatInline  MessageFormat = "inline"   // inline PGP in a text/plain body
)

// SignatureStatus is the result of signature verification.
type SignatureStatus string

const (
	// SigNone: the message is not signed.
	SigNone SignatureStatus = "none"
	// SigValid: the signature is cryptographically valid and the signing
	// key is currently valid.
	SigValid SignatureStatus = "valid"
	// SigInvalid: the signature does not match (tampered content, broken
	// signature, unusable key).
	SigInvalid SignatureStatus = "invalid"
	// SigUnknownKey: the signing key is not available.
	SigUnknownKey SignatureStatus = "unknown_key"
	// SigExpiredKey: the signing key is expired, either at signature time
	// (the signature could not be accepted) or now (SignatureError is nil in
	// that case: the signature itself was valid when made).
	SigExpiredKey SignatureStatus = "expired_key"
	// SigRevokedKey: the signing key has been revoked.
	SigRevokedKey SignatureStatus = "revoked_key"
)

// EncryptionStatus describes the encryption layer of a message.
type EncryptionStatus string

const (
	EncNone      EncryptionStatus = "none"      // not encrypted
	EncDecrypted EncryptionStatus = "decrypted" // encrypted and successfully decrypted
	EncFailed    EncryptionStatus = "failed"    // encrypted but could not be decrypted
)

// ReadResult is the outcome of ReadMessage.
type ReadResult struct {
	Format     MessageFormat
	Encryption EncryptionStatus
	// Content is the inner MIME entity (header fields, empty line, body,
	// CRLF line endings): the decrypted part of a multipart/encrypted
	// message, the signed part of a multipart/signed message, or a
	// synthesized text/plain entity for inline PGP. For FormatNone it is
	// the (CRLF-canonicalised) input. Content is nil if decryption failed.
	Content []byte

	// DecryptionKey is the fingerprint of the primary key whose (sub)key
	// decrypted the message.
	DecryptionKey string
	// EncryptedTo lists the recipient key IDs found in the message
	// (upper-case hex; 0000000000000000 for hidden recipients).
	EncryptedTo []string

	Signed    bool
	Signature SignatureStatus
	// SignerFingerprint is the primary key fingerprint of the signer when
	// the key is known, otherwise the issuer fingerprint from the
	// signature if present.
	SignerFingerprint string
	// SignerKeyID is the issuer key ID (of the signing subkey).
	SignerKeyID   string
	SignatureTime time.Time
	// SignatureError carries the verification error, if any.
	SignatureError error

	// HasProtectedHeaders is true when the inner part carries
	// protected-headers="v1".
	HasProtectedHeaders bool
	// ProtectedHeaders are the header fields of the inner part (without
	// Content-* fields) when HasProtectedHeaders is true.
	ProtectedHeaders []HeaderField
	// InnerHeader is the parsed header of Content (e.g. for
	// Autocrypt-Gossip processing).
	InnerHeader textproto.MIMEHeader
	// ForeignParts is true when a PGP/MIME message had parts besides the ones
	// RFC 3156 allows. They are unprotected whatever the signature says and are
	// not part of Content (EFAIL-like mixtures, added text).
	ForeignParts bool
	// LockedKeys are the fingerprints of keys that could decrypt the message,
	// but whose passphrase the PassphraseFunc refused with ErrKeyLocked.
	LockedKeys []string
	// InlineHasUnprotectedText is true for inline PGP when text outside
	// the armored block was dropped from Content.
	InlineHasUnprotectedText bool
}

// ProtectedHeader returns the first protected header field with the given
// name.
func (r *ReadResult) ProtectedHeader(name string) (string, bool) {
	for _, f := range r.ProtectedHeaders {
		if strings.EqualFold(f.Name, name) {
			return f.Value, true
		}
	}
	return "", false
}

// ReadOptions configures ReadMessage.
type ReadOptions struct {
	// VerificationKeys are additional public keys for signature
	// verification that are not (yet) in the key ring, e.g. the sender's
	// key from Autocrypt peer state or an attached key.
	VerificationKeys []*Key
	// Now overrides the verification time (mainly for tests).
	Now time.Time
}

// ReadMessage detects, decrypts and verifies OpenPGP content in raw, which
// may be a complete RFC 5322 message or a MIME entity. It handles
// multipart/encrypted (including nested multipart/signed), multipart/signed
// and inline PGP in text/plain bodies.
//
// A non-nil result is returned whenever OpenPGP content was recognised, even
// if decryption failed (Encryption == EncFailed); the error then describes
// the failure. Signature problems are reported in the result, not as errors.
func (r *KeyRing) ReadMessage(ctx context.Context, raw []byte, opts ReadOptions) (*ReadResult, error) {
	if err := r.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	op := &opKeyRing{ctx: ctx, ring: r, cache: map[string]*openpgp.Entity{}}
	for _, k := range opts.VerificationKeys {
		if k != nil {
			op.extra = append(op.extra, k.Public().entity)
		}
	}
	raw = CanonicalizeCRLF(raw)
	res := &ReadResult{Format: FormatNone, Encryption: EncNone, Signature: SigNone}
	if err := r.readEntity(ctx, op, raw, opts, res, 0); err != nil {
		return res, err
	}
	if res.Content != nil {
		h, _ := splitEntity(res.Content)
		res.InnerHeader, _ = parseHeader(h)
		if res.Format != FormatNone {
			res.ProtectedHeaders, res.HasProtectedHeaders = extractProtectedHeaders(res.Content)
		}
	}
	return res, nil
}

const maxNesting = 4

func (r *KeyRing) readEntity(ctx context.Context, op *opKeyRing, raw []byte, opts ReadOptions, res *ReadResult, depth int) error {
	if depth > maxNesting {
		return fmt.Errorf("%w: nesting too deep", ErrMalformedMIME)
	}
	hdr, body := splitEntity(raw)
	h, err := parseHeader(hdr)
	if err != nil {
		return err
	}
	mt, params := mediaType(h.Get("Content-Type"))
	switch {
	case mt == "multipart/encrypted" && strings.EqualFold(params["protocol"], "application/pgp-encrypted"):
		res.Format = FormatPGPMIME
		content, err := r.decryptPGPMIME(ctx, op, body, params["boundary"], opts, res)
		if err != nil {
			res.Encryption = EncFailed
			return err
		}
		res.Encryption = EncDecrypted
		res.Content = content
		// RFC 3156 6.1: signed, then encrypted.
		ih, _ := splitEntity(content)
		if ihh, err := parseHeader(ih); err == nil {
			imt, iparams := mediaType(ihh.Get("Content-Type"))
			if imt == "multipart/signed" && strings.EqualFold(iparams["protocol"], "application/pgp-signature") && !res.Signed {
				return r.readEntity(ctx, op, content, opts, res, depth+1)
			}
		}
		return nil
	case mt == "multipart/signed" && strings.EqualFold(params["protocol"], "application/pgp-signature"):
		res.Format = FormatPGPMIME
		signed, err := verifyPGPMIME(op, body, params["boundary"], opts, res)
		if err != nil {
			return err
		}
		res.Content = signed
		return nil
	case mt == "text/plain":
		decoded, err := decodeTransfer(body, h.Get("Content-Transfer-Encoding"))
		if err != nil {
			return err
		}
		if bytes.Contains(decoded, []byte("-----BEGIN PGP MESSAGE-----")) {
			res.Format = FormatInline
			return r.readInlineEncrypted(ctx, op, decoded, params, opts, res)
		}
		if bytes.Contains(decoded, []byte("-----BEGIN PGP SIGNED MESSAGE-----")) {
			res.Format = FormatInline
			return readInlineSigned(op, decoded, params, opts, res)
		}
	}
	res.Content = raw
	return nil
}

func (r *KeyRing) decryptPGPMIME(ctx context.Context, op *opKeyRing, body []byte, boundary string, opts ReadOptions, res *ReadResult) ([]byte, error) {
	parts, err := splitMultipart(body, boundary)
	if err != nil {
		return nil, err
	}
	if len(parts) < 2 {
		return nil, fmt.Errorf("%w: multipart/encrypted needs two parts", ErrMalformedMIME)
	}
	if len(parts) > 2 {
		res.ForeignParts = true
	}
	// Part 1: application/pgp-encrypted with "Version: 1" (not checked
	// strictly). Part 2: application/octet-stream with the message.
	ph, pb := splitEntity(parts[1])
	phh, err := parseHeader(ph)
	if err != nil {
		return nil, err
	}
	pb, err = decodeTransfer(pb, phh.Get("Content-Transfer-Encoding"))
	if err != nil {
		return nil, err
	}
	plain, err := r.decryptMessage(ctx, op, pb, opts, res)
	if err != nil {
		return nil, err
	}
	return CanonicalizeCRLF(plain), nil
}

// decryptMessage decrypts an armored or binary OpenPGP message and fills
// the encryption and signature fields of res.
func (r *KeyRing) decryptMessage(ctx context.Context, op *opKeyRing, data []byte, opts ReadOptions, res *ReadResult) ([]byte, error) {
	var in io.Reader = bytes.NewReader(data)
	if i := bytes.Index(data, []byte("-----BEGIN PGP MESSAGE-----")); i >= 0 {
		block, err := armor.Decode(bytes.NewReader(data[i:]))
		if err != nil {
			return nil, fmt.Errorf("pgp: decode armor: %w", err)
		}
		if block.Type != armorMessage {
			return nil, fmt.Errorf("pgp: unexpected armor type %q", block.Type)
		}
		in = block.Body
	}
	op.secret = true
	var promptErr error
	attempts := map[string]int{}
	prompt := func(keys []openpgp.Key, symmetric bool) ([]byte, error) {
		if len(keys) == 0 {
			return nil, ErrNoDecryptionKey
		}
		r.mu.RLock()
		pf := r.passphrase
		r.mu.RUnlock()
		if pf == nil {
			promptErr = ErrNoPassphrase
			return nil, promptErr
		}
		for _, k := range keys {
			e := k.Entity
			fpr := strings.ToUpper(hex.EncodeToString(e.PrimaryKey.Fingerprint))
			if attempts[fpr] >= MaxPassphraseAttempts {
				continue
			}
			attempts[fpr]++
			pass, err := pf(ctx, PassphraseRequest{
				Fingerprint: fpr, KeyID: e.PrimaryKey.KeyIdString(),
				UserID: primaryUID(&Key{entity: e}), Attempt: attempts[fpr],
			})
			if errors.Is(err, ErrKeyLocked) {
				// Another recipient key may be available.
				attempts[fpr] = MaxPassphraseAttempts
				res.LockedKeys = append(res.LockedKeys, fpr)
				continue
			}
			if err != nil {
				promptErr = err
				return nil, err
			}
			// Success or not, let go-crypto retry: it either uses the now
			// decrypted key or calls us again for the next attempt.
			_ = e.DecryptPrivateKeys(pass)
			return nil, nil
		}
		promptErr = ErrWrongPassphrase
		if len(res.LockedKeys) > 0 {
			promptErr = ErrKeyLocked
		}
		return nil, promptErr
	}
	md, err := openpgp.ReadMessage(in, op, prompt, verifyConfig(opts.Now))
	if err != nil {
		if promptErr != nil {
			return nil, promptErr
		}
		if errors.Is(err, pgperrors.ErrKeyIncorrect) {
			return nil, ErrNoDecryptionKey
		}
		return nil, fmt.Errorf("pgp: decrypt: %w", err)
	}
	for _, id := range md.EncryptedToKeyIds {
		res.EncryptedTo = append(res.EncryptedTo, fmt.Sprintf("%016X", id))
	}
	if !md.IsEncrypted {
		return nil, fmt.Errorf("%w: OpenPGP message is not encrypted", ErrMalformedMIME)
	}
	plain, err := io.ReadAll(md.UnverifiedBody)
	if err != nil {
		// Integrity failure (MDC/AEAD) or truncated data: never release
		// unauthenticated plaintext.
		return nil, fmt.Errorf("pgp: decrypt: %w", err)
	}
	if md.DecryptedWith.Entity != nil {
		res.DecryptionKey = strings.ToUpper(hex.EncodeToString(md.DecryptedWith.Entity.PrimaryKey.Fingerprint))
	}
	if md.IsSigned {
		fillSignature(res, md, opts.Now)
	}
	return plain, nil
}

// signatureClockSkew is what a sender's clock may be ahead: a signature from
// "the future" is refused by go-crypto, and a clock a few seconds off is no
// sign of a changed message.
const signatureClockSkew = 5 * time.Minute

func verifyConfig(now time.Time) *packet.Config {
	if now.IsZero() {
		now = time.Now().Add(signatureClockSkew)
	}
	return configAt(now)
}

func verifyPGPMIME(op *opKeyRing, body []byte, boundary string, opts ReadOptions, res *ReadResult) ([]byte, error) {
	parts, err := splitMultipart(body, boundary)
	if err != nil {
		return nil, err
	}
	if len(parts) < 2 {
		return nil, fmt.Errorf("%w: multipart/signed needs two parts", ErrMalformedMIME)
	}
	if len(parts) > 2 {
		res.ForeignParts = true
	}
	signed := parts[0]
	sh, sb := splitEntity(parts[1])
	shh, err := parseHeader(sh)
	if err != nil {
		return nil, err
	}
	sb, err = decodeTransfer(sb, shh.Get("Content-Transfer-Encoding"))
	if err != nil {
		return nil, err
	}
	verifyDetached(op, signed, sb, opts, res)
	return signed, nil
}

// verifyDetached verifies an (armored or binary) detached signature over
// signed and records the outcome in res.
func verifyDetached(op *opKeyRing, signed, sig []byte, opts ReadOptions, res *ReadResult) {
	res.Signed = true
	var sigReader io.Reader = bytes.NewReader(sig)
	if i := bytes.Index(sig, []byte("-----BEGIN PGP ")); i >= 0 {
		block, err := armor.Decode(bytes.NewReader(sig[i:]))
		if err != nil {
			res.Signature, res.SignatureError = SigInvalid, fmt.Errorf("pgp: decode signature armor: %w", err)
			return
		}
		sigReader = block.Body
	}
	verifyDetachedReader(op, signed, sigReader, opts, res)
}

func verifyDetachedReader(op *opKeyRing, signed []byte, sigReader io.Reader, opts ReadOptions, res *ReadResult) {
	res.Signed = true
	md, err := openpgp.VerifyDetachedSignatureReader(op, bytes.NewReader(signed), sigReader, verifyConfig(opts.Now))
	if err != nil {
		res.Signature, res.SignatureError = SigInvalid, err
		if errors.Is(err, pgperrors.ErrUnknownIssuer) {
			res.Signature = SigUnknownKey
		}
		return
	}
	if _, err := io.Copy(io.Discard, md.UnverifiedBody); err != nil {
		res.Signature, res.SignatureError = SigInvalid, err
		return
	}
	fillSignature(res, md, opts.Now)
}

func fillSignature(res *ReadResult, md *openpgp.MessageDetails, now time.Time) {
	res.Signed = true
	res.SignatureError = md.SignatureError
	var signer *openpgp.Entity
	if md.SignedBy != nil {
		signer = md.SignedBy.Entity
	}
	if c := md.SelectedCandidate; c != nil {
		if signer == nil {
			signer = c.SignedByEntity
		}
		if c.IssuerKeyId != 0 {
			res.SignerKeyID = fmt.Sprintf("%016X", c.IssuerKeyId)
		}
		if signer == nil && len(c.IssuerFingerprint) > 0 {
			res.SignerFingerprint = strings.ToUpper(hex.EncodeToString(c.IssuerFingerprint))
		}
		if c.CorrespondingSig != nil {
			res.SignatureTime = c.CorrespondingSig.CreationTime
		}
	}
	if md.Signature != nil {
		res.SignatureTime = md.Signature.CreationTime
		if md.Signature.IssuerKeyId != nil {
			res.SignerKeyID = fmt.Sprintf("%016X", *md.Signature.IssuerKeyId)
		}
	}
	if signer != nil {
		res.SignerFingerprint = strings.ToUpper(hex.EncodeToString(signer.PrimaryKey.Fingerprint))
	}
	err := md.SignatureError
	switch {
	case err == nil && md.SignedBy != nil:
		res.Signature = SigValid
		switch (&Key{entity: md.SignedBy.Entity}).Status(signerTime(now)) {
		case StatusExpired:
			res.Signature = SigExpiredKey
		case StatusRevoked:
			res.Signature = SigRevokedKey
		}
	case err == nil:
		res.Signature = SigInvalid
	case errors.Is(err, pgperrors.ErrUnknownIssuer):
		res.Signature = SigUnknownKey
	case errors.Is(err, pgperrors.ErrKeyExpired):
		res.Signature = SigExpiredKey
	case errors.Is(err, pgperrors.ErrKeyRevoked):
		res.Signature = SigRevokedKey
	default:
		res.Signature = SigInvalid
	}
}

func (r *KeyRing) readInlineEncrypted(ctx context.Context, op *opKeyRing, text []byte, ctParams map[string]string, opts ReadOptions, res *ReadResult) error {
	text = CanonicalizeCRLF(text)
	begin := bytes.Index(text, []byte("-----BEGIN PGP MESSAGE-----"))
	endMarker := []byte("-----END PGP MESSAGE-----")
	end := bytes.Index(text[begin:], endMarker)
	if end < 0 {
		res.Encryption = EncFailed
		return fmt.Errorf("%w: unterminated PGP MESSAGE block", ErrMalformedMIME)
	}
	end += begin + len(endMarker)
	block := text[begin:end]
	outside := append(append([]byte{}, text[:begin]...), text[end:]...)
	res.InlineHasUnprotectedText = len(bytes.TrimSpace(outside)) > 0

	plain, err := r.decryptMessage(ctx, op, block, opts, res)
	if err != nil {
		res.Encryption = EncFailed
		return err
	}
	res.Encryption = EncDecrypted
	res.Content = textEntity(plain, ctParams["charset"])
	return nil
}

func readInlineSigned(op *opKeyRing, text []byte, ctParams map[string]string, opts ReadOptions, res *ReadResult) error {
	b, rest := clearsign.Decode(text)
	if b == nil {
		return fmt.Errorf("%w: invalid cleartext signed message", ErrMalformedMIME)
	}
	begin := bytes.Index(text, []byte("-----BEGIN PGP SIGNED MESSAGE-----"))
	res.InlineHasUnprotectedText = len(bytes.TrimSpace(text[:begin])) > 0 || len(bytes.TrimSpace(rest)) > 0
	verifyDetachedReader(op, b.Bytes, b.ArmoredSignature.Body, opts, res)
	res.Content = textEntity(b.Plaintext, ctParams["charset"])
	return nil
}

func textEntity(text []byte, charset string) []byte {
	if charset == "" {
		charset = "utf-8"
	}
	var b bytes.Buffer
	b.WriteString("Content-Type: " + mime.FormatMediaType("text/plain", map[string]string{"charset": charset}) + "\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.Write(CanonicalizeCRLF(text))
	return b.Bytes()
}

// opKeyRing is the per-operation view of a KeyRing handed to go-crypto. It
// hands out private copies of entities so that decrypting secret keys or
// caching signature validity never touches shared state.
type opKeyRing struct {
	ctx    context.Context
	ring   *KeyRing
	secret bool
	extra  []*openpgp.Entity
	cache  map[string]*openpgp.Entity
}

func (o *opKeyRing) EntitiesById(id uint64) []*openpgp.Entity {
	var out []*openpgp.Entity
	keys, _ := o.ring.FindByKeyID(o.ctx, id)
	for _, k := range keys {
		fpr := k.Fingerprint()
		e, ok := o.cache[fpr]
		if !ok {
			var c *Key
			var err error
			if o.secret {
				c, err = k.Clone()
			} else {
				c = k.Public()
			}
			if err != nil {
				continue
			}
			e = c.entity
			o.cache[fpr] = e
		}
		out = append(out, e)
	}
	for _, e := range o.extra {
		if entityHasKeyID(e, id) {
			out = append(out, e)
		}
	}
	return out
}

func (o *opKeyRing) KeysById(id uint64) []openpgp.Key {
	var out []openpgp.Key
	for _, e := range o.EntitiesById(id) {
		if e.PrimaryKey.KeyId == id {
			out = append(out, openpgp.Key{Entity: e, PublicKey: e.PrimaryKey, PrivateKey: e.PrivateKey})
		}
		for _, sk := range e.Subkeys {
			if sk.PublicKey.KeyId == id {
				out = append(out, openpgp.Key{Entity: e, PublicKey: sk.PublicKey, PrivateKey: sk.PrivateKey})
			}
		}
	}
	return out
}

var _ openpgp.KeyRing = (*opKeyRing)(nil)

// VerifyDetached verifies a detached signature (armored or binary) over
// data using the ring's keys plus extra. It is a convenience for callers
// that handle the MIME structure themselves.
func (r *KeyRing) VerifyDetached(ctx context.Context, data, signature []byte, extra []*Key, now time.Time) (*ReadResult, error) {
	if err := r.ensureLoaded(ctx); err != nil {
		return nil, err
	}
	op := &opKeyRing{ctx: ctx, ring: r, cache: map[string]*openpgp.Entity{}}
	for _, k := range extra {
		op.extra = append(op.extra, k.Public().entity)
	}
	res := &ReadResult{Format: FormatNone, Encryption: EncNone, Signature: SigNone}
	verifyDetached(op, data, signature, ReadOptions{Now: now}, res)
	return res, nil
}
