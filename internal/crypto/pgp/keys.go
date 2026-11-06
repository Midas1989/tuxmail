// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	pgperrors "github.com/ProtonMail/go-crypto/openpgp/errors"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// Armor block types.
const (
	armorPublicKey  = "PGP PUBLIC KEY BLOCK"
	armorPrivateKey = "PGP PRIVATE KEY BLOCK"
	armorMessage    = "PGP MESSAGE"
	armorSignature  = "PGP SIGNATURE"
)

// Key is an OpenPGP transferable public or secret key (certificate).
//
// A Key value is treated as immutable by this package: operations that
// change a key (Unlock, ChangePassphrase, Revoke ...) return a new Key and
// leave the receiver untouched. Its methods are safe for concurrent use
// (go-crypto caches verification results inside the entity, so access is
// serialised internally).
type Key struct {
	mu     sync.Mutex
	entity *openpgp.Entity
}

// NewKeyFromEntity wraps an existing go-crypto entity. The entity must not be
// modified afterwards.
func NewKeyFromEntity(e *openpgp.Entity) *Key { return &Key{entity: e} }

// Entity returns the underlying go-crypto entity. Callers must not modify
// it, and must not use it concurrently with other goroutines (go-crypto
// mutates cached state while verifying or serialising); use Clone first
// when in doubt.
func (k *Key) Entity() *openpgp.Entity { return k.entity }

// Fingerprint returns the primary key fingerprint as upper-case hex
// (40 characters for v4 keys, 64 for v6 keys).
func (k *Key) Fingerprint() string {
	return strings.ToUpper(hex.EncodeToString(k.entity.PrimaryKey.Fingerprint))
}

// KeyID returns the 64-bit key ID of the primary key as upper-case hex.
func (k *Key) KeyID() string { return k.entity.PrimaryKey.KeyIdString() }

// Version returns the key packet version (4 or 6, possibly 5).
func (k *Key) Version() int { return k.entity.PrimaryKey.Version }

// CreationTime returns the creation time of the primary key.
func (k *Key) CreationTime() time.Time { return k.entity.PrimaryKey.CreationTime }

// IsSecret reports whether the key contains secret key material.
func (k *Key) IsSecret() bool {
	return k.entity.PrivateKey != nil && !k.entity.PrivateKey.Dummy()
}

// IsLocked reports whether any secret (sub)key is still encrypted with a
// passphrase. Public keys are never locked.
func (k *Key) IsLocked() bool {
	if k.entity.PrivateKey != nil && !k.entity.PrivateKey.Dummy() && k.entity.PrivateKey.Encrypted {
		return true
	}
	for _, sk := range k.entity.Subkeys {
		if sk.PrivateKey != nil && !sk.PrivateKey.Dummy() && sk.PrivateKey.Encrypted {
			return true
		}
	}
	return false
}

// UserID is a parsed OpenPGP user ID.
type UserID struct {
	Raw     string `json:"raw"`
	Name    string `json:"name,omitempty"`
	Comment string `json:"comment,omitempty"`
	Email   string `json:"email,omitempty"` // as written in the user ID
	Primary bool   `json:"primary"`
	Revoked bool   `json:"revoked"`
	// Valid is true when the user ID has a valid, unexpired self-signature
	// and is not revoked (evaluated at the time passed to Info).
	Valid bool `json:"valid"`
}

// UserIDs returns all user IDs of the key, sorted with the primary user ID
// first and then alphabetically.
func (k *Key) UserIDs() []UserID {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.userIDs(time.Now())
}

func (k *Key) userIDs(now time.Time) []UserID {
	_, primary := k.entity.PrimaryIdentity(now, nil)
	if primary == nil {
		_, primary = k.entity.PrimaryIdentity(time.Time{}, nil)
	}
	out := make([]UserID, 0, len(k.entity.Identities))
	for name, id := range k.entity.Identities {
		u := UserID{Raw: name}
		if id.UserId != nil {
			u.Name, u.Comment, u.Email = id.UserId.Name, id.UserId.Comment, id.UserId.Email
		}
		if u.Email == "" {
			if a, err := mail.ParseAddress(name); err == nil {
				u.Email = a.Address
				if u.Name == "" {
					u.Name = a.Name
				}
			}
		}
		u.Primary = primary != nil && primary.Name == name
		sig, err := id.Verify(time.Time{}, nil)
		u.Revoked = err == nil && id.Revoked(sig, now, nil)
		if errors.Is(err, pgperrors.ErrKeyRevoked) {
			u.Revoked = true
		}
		_, verr := id.Verify(now, nil)
		u.Valid = verr == nil
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Primary != out[j].Primary {
			return out[i].Primary
		}
		return out[i].Raw < out[j].Raw
	})
	return out
}

// Emails returns the lower-cased e-mail addresses of all user IDs.
func (k *Key) Emails() []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range k.UserIDs() {
		e := NormalizeEmail(u.Email)
		if e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// HasEmail reports whether one of the key's user IDs carries the given
// address (case-insensitive).
func (k *Key) HasEmail(email string) bool {
	email = NormalizeEmail(email)
	if email == "" {
		return false
	}
	for _, e := range k.Emails() {
		if e == email {
			return true
		}
	}
	return false
}

// NormalizeEmail trims and lower-cases an e-mail address. Display names
// ("Name <a@b>") are stripped when present.
func NormalizeEmail(s string) string {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "<>") {
		if a, err := mail.ParseAddress(s); err == nil {
			s = a.Address
		}
	}
	return strings.ToLower(s)
}

// NormalizeFingerprint removes spaces and an optional 0x prefix and
// upper-cases the fingerprint.
func NormalizeFingerprint(fpr string) string {
	fpr = strings.TrimSpace(fpr)
	fpr = strings.TrimPrefix(strings.TrimPrefix(fpr, "0x"), "0X")
	fpr = strings.ReplaceAll(fpr, " ", "")
	fpr = strings.ReplaceAll(fpr, ":", "")
	return strings.ToUpper(fpr)
}

// KeyStatus is the overall validity of a key or subkey.
type KeyStatus string

const (
	StatusValid   KeyStatus = "valid"
	StatusExpired KeyStatus = "expired"
	StatusRevoked KeyStatus = "revoked"
	// StatusInvalid means the key has no valid self-signature or is
	// otherwise unusable (e.g. weak algorithm, creation time in the future).
	StatusInvalid KeyStatus = "invalid"
)

// Capabilities lists what a key or subkey may be used for according to its
// key flags.
type Capabilities struct {
	Certify      bool `json:"certify"`
	Sign         bool `json:"sign"`
	Encrypt      bool `json:"encrypt"`
	Authenticate bool `json:"authenticate"`
}

// String returns the GnuPG-style capability letters (e.g. "SC", "E").
func (c Capabilities) String() string {
	var b strings.Builder
	if c.Sign {
		b.WriteByte('S')
	}
	if c.Certify {
		b.WriteByte('C')
	}
	if c.Encrypt {
		b.WriteByte('E')
	}
	if c.Authenticate {
		b.WriteByte('A')
	}
	return b.String()
}

func capsFromSig(sig *packet.Signature, algo packet.PublicKeyAlgorithm) Capabilities {
	if sig == nil {
		return Capabilities{}
	}
	if !sig.FlagsValid {
		// No key flags: infer from the algorithm (legacy keys).
		return Capabilities{
			Certify: algoCanSign(algo),
			Sign:    algoCanSign(algo),
			Encrypt: algoCanEncrypt(algo),
		}
	}
	return Capabilities{
		Certify:      sig.FlagCertify,
		Sign:         sig.FlagSign,
		Encrypt:      sig.FlagEncryptCommunications || sig.FlagEncryptStorage,
		Authenticate: sig.FlagAuthenticate,
	}
}

func algoCanSign(a packet.PublicKeyAlgorithm) bool {
	switch a {
	case packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSASignOnly, packet.PubKeyAlgoDSA,
		packet.PubKeyAlgoECDSA, packet.PubKeyAlgoEdDSA, packet.PubKeyAlgoEd25519, packet.PubKeyAlgoEd448:
		return true
	}
	return false
}

func algoCanEncrypt(a packet.PublicKeyAlgorithm) bool {
	switch a {
	case packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSAEncryptOnly, packet.PubKeyAlgoElGamal,
		packet.PubKeyAlgoECDH, packet.PubKeyAlgoX25519, packet.PubKeyAlgoX448:
		return true
	}
	return false
}

// AlgorithmName returns a human-readable name of the public key algorithm,
// e.g. "RSA-4096", "Ed25519", "EdDSA (Ed25519, legacy)" or "ECDH (Curve25519)".
func AlgorithmName(pk *packet.PublicKey) string {
	switch pk.PubKeyAlgo {
	case packet.PubKeyAlgoRSA, packet.PubKeyAlgoRSAEncryptOnly, packet.PubKeyAlgoRSASignOnly:
		if bits, err := pk.BitLength(); err == nil {
			return fmt.Sprintf("RSA-%d", bits)
		}
		return "RSA"
	case packet.PubKeyAlgoDSA:
		return "DSA"
	case packet.PubKeyAlgoElGamal:
		return "ElGamal"
	case packet.PubKeyAlgoECDSA, packet.PubKeyAlgoECDH, packet.PubKeyAlgoEdDSA:
		name := map[packet.PublicKeyAlgorithm]string{
			packet.PubKeyAlgoECDSA: "ECDSA", packet.PubKeyAlgoECDH: "ECDH", packet.PubKeyAlgoEdDSA: "EdDSA",
		}[pk.PubKeyAlgo]
		if c, err := pk.Curve(); err == nil {
			return fmt.Sprintf("%s (%s)", name, c)
		}
		return name
	case packet.PubKeyAlgoX25519:
		return "X25519"
	case packet.PubKeyAlgoX448:
		return "X448"
	case packet.PubKeyAlgoEd25519:
		return "Ed25519"
	case packet.PubKeyAlgoEd448:
		return "Ed448"
	}
	return fmt.Sprintf("algorithm-%d", pk.PubKeyAlgo)
}

// SubkeyInfo describes a subkey.
type SubkeyInfo struct {
	Fingerprint  string       `json:"fingerprint"`
	KeyID        string       `json:"keyId"`
	Algorithm    string       `json:"algorithm"`
	BitLength    int          `json:"bitLength,omitempty"`
	Created      time.Time    `json:"created"`
	Expires      time.Time    `json:"expires,omitempty"` // zero = does not expire
	Capabilities Capabilities `json:"capabilities"`
	Status       KeyStatus    `json:"status"`
	Secret       bool         `json:"secret"` // secret material present (not a stub)
}

// KeyInfo is a summary of a key for display and decision making.
type KeyInfo struct {
	Fingerprint  string       `json:"fingerprint"`
	KeyID        string       `json:"keyId"`
	Version      int          `json:"version"`
	Algorithm    string       `json:"algorithm"`
	BitLength    int          `json:"bitLength,omitempty"`
	Created      time.Time    `json:"created"`
	Expires      time.Time    `json:"expires,omitempty"` // zero = does not expire
	Capabilities Capabilities `json:"capabilities"`      // primary key flags
	// Usable summarises what the key as a whole (primary + valid subkeys)
	// can be used for at the evaluation time.
	Usable  Capabilities `json:"usable"`
	Status  KeyStatus    `json:"status"`
	Secret  bool         `json:"secret"`
	Locked  bool         `json:"locked"`
	UserIDs []UserID     `json:"userIds"`
	Subkeys []SubkeyInfo `json:"subkeys"`
}

// Info returns a summary of the key evaluated at time now (zero = time.Now()).
func (k *Key) Info(now time.Time) KeyInfo {
	if now.IsZero() {
		now = time.Now()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	e := k.entity
	pk := e.PrimaryKey
	info := KeyInfo{
		Fingerprint: k.Fingerprint(),
		KeyID:       k.KeyID(),
		Version:     pk.Version,
		Algorithm:   AlgorithmName(pk),
		Created:     pk.CreationTime,
		Status:      k.status(now),
		Secret:      k.IsSecret(),
		Locked:      k.IsLocked(),
		UserIDs:     k.userIDs(now),
	}
	if bits, err := pk.BitLength(); err == nil {
		info.BitLength = int(bits)
	}
	if sig, err := e.PrimarySelfSignature(time.Time{}, nil); err == nil && sig != nil {
		info.Capabilities = capsFromSig(sig, pk.PubKeyAlgo)
		info.Expires = expiryOf(pk, sig)
	}
	for i := range e.Subkeys {
		sk := &e.Subkeys[i]
		si := SubkeyInfo{
			Fingerprint: strings.ToUpper(hex.EncodeToString(sk.PublicKey.Fingerprint)),
			KeyID:       sk.PublicKey.KeyIdString(),
			Algorithm:   AlgorithmName(sk.PublicKey),
			Created:     sk.PublicKey.CreationTime,
			Secret:      sk.PrivateKey != nil && !sk.PrivateKey.Dummy(),
			Status:      StatusValid,
		}
		if bits, err := sk.PublicKey.BitLength(); err == nil {
			si.BitLength = int(bits)
		}
		if sig, err := sk.LatestValidBindingSignature(time.Time{}, nil); err == nil {
			si.Capabilities = capsFromSig(sig, sk.PublicKey.PubKeyAlgo)
			si.Expires = expiryOf(sk.PublicKey, sig)
		} else {
			si.Status = StatusInvalid
		}
		// A subkey never outlives its primary key.
		if !info.Expires.IsZero() && (si.Expires.IsZero() || si.Expires.After(info.Expires)) {
			si.Expires = info.Expires
		}
		if si.Status == StatusValid {
			if _, err := sk.Verify(now, nil); err != nil {
				si.Status = statusFromErr(err)
			}
		}
		if info.Status != StatusValid && si.Status == StatusValid {
			si.Status = info.Status
		}
		info.Subkeys = append(info.Subkeys, si)
	}
	if info.Status == StatusValid {
		if _, ok := e.EncryptionKey(now, nil); ok {
			info.Usable.Encrypt = true
		}
		if _, ok := e.SigningKey(now, nil); ok {
			info.Usable.Sign = true
		}
		if _, ok := e.CertificationKey(now, nil); ok {
			info.Usable.Certify = info.Capabilities.Certify
		}
	}
	return info
}

func expiryOf(pk *packet.PublicKey, sig *packet.Signature) time.Time {
	if sig == nil || sig.KeyLifetimeSecs == nil || *sig.KeyLifetimeSecs == 0 {
		return time.Time{}
	}
	return pk.CreationTime.Add(time.Duration(*sig.KeyLifetimeSecs) * time.Second)
}

func statusFromErr(err error) KeyStatus {
	switch {
	case err == nil:
		return StatusValid
	case errors.Is(err, pgperrors.ErrKeyRevoked):
		return StatusRevoked
	case errors.Is(err, pgperrors.ErrKeyExpired), errors.Is(err, pgperrors.ErrSignatureExpired):
		return StatusExpired
	}
	return StatusInvalid
}

// Status returns the validity of the primary key at time now
// (zero = time.Now()).
func (k *Key) Status(now time.Time) KeyStatus {
	if now.IsZero() {
		now = time.Now()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.status(now)
}

func (k *Key) status(now time.Time) KeyStatus {
	if k.entity.Revoked(now) {
		return StatusRevoked
	}
	_, err := k.entity.VerifyPrimaryKey(now, nil)
	if err != nil && !errors.Is(err, pgperrors.ErrKeyRevoked) && !errors.Is(err, pgperrors.ErrKeyExpired) {
		// The self-signature may have expired: check without time constraints
		// to distinguish "expired" from "broken".
		if _, err2 := k.entity.VerifyPrimaryKey(time.Time{}, nil); err2 == nil {
			if k.entity.PrimaryKey.CreationTime.After(now) {
				return StatusInvalid
			}
			return StatusExpired
		}
	}
	return statusFromErr(err)
}

// Expires returns the expiry time of the primary key (zero = never).
func (k *Key) Expires() time.Time {
	k.mu.Lock()
	defer k.mu.Unlock()
	sig, err := k.entity.PrimarySelfSignature(time.Time{}, nil)
	if err != nil {
		return time.Time{}
	}
	return expiryOf(k.entity.PrimaryKey, sig)
}

// CanEncrypt reports whether the key has a valid encryption (sub)key at now.
func (k *Key) CanEncrypt(now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.entity.EncryptionKey(now, nil)
	return ok
}

// CanSign reports whether the key has a valid signing (sub)key at now. It
// does not check whether secret material is available.
func (k *Key) CanSign(now time.Time) bool {
	if now.IsZero() {
		now = time.Now()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.entity.SigningKey(now, nil)
	return ok
}

// ---------------------------------------------------------------------------
// Parsing

// ReadKeys parses one or more keys from armored or binary input. Armored
// input may contain several concatenated armor blocks.
func ReadKeys(data []byte) ([]*Key, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, ErrNoKeyData
	}
	var entities openpgp.EntityList
	// Binary OpenPGP data always starts with a packet tag byte with the high
	// bit set; everything else is treated as (possibly embedded) armor.
	if data[0]&0x80 == 0 && bytes.Contains(trimmed, []byte("-----BEGIN PGP ")) {
		rest := trimmed
		for {
			i := bytes.Index(rest, []byte("-----BEGIN PGP "))
			if i < 0 {
				break
			}
			rest = rest[i:]
			block, err := armor.Decode(bytes.NewReader(rest))
			if err != nil {
				return nil, fmt.Errorf("pgp: decode armor: %w", err)
			}
			if block.Type != armorPublicKey && block.Type != armorPrivateKey {
				return nil, fmt.Errorf("pgp: unexpected armor type %q", block.Type)
			}
			el, err := openpgp.ReadKeyRing(block.Body)
			if err != nil {
				return nil, fmt.Errorf("pgp: read keys: %w", err)
			}
			entities = append(entities, el...)
			// Continue after the END line of this block.
			end := bytes.Index(rest, []byte("-----END PGP "))
			if end < 0 {
				break
			}
			rest = rest[end+len("-----END PGP "):]
		}
	} else {
		// Binary data must not be trimmed: trailing bytes may look like
		// whitespace.
		el, err := openpgp.ReadKeyRing(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("pgp: read keys: %w", err)
		}
		entities = el
	}
	if len(entities) == 0 {
		return nil, ErrNoKeyData
	}
	keys := make([]*Key, 0, len(entities))
	for _, e := range entities {
		keys = append(keys, &Key{entity: e})
	}
	return keys, nil
}

// ReadKey parses exactly one key (the first one if several are present).
func ReadKey(data []byte) (*Key, error) {
	keys, err := ReadKeys(data)
	if err != nil {
		return nil, err
	}
	return keys[0], nil
}

// ---------------------------------------------------------------------------
// Export

// PublicBytes returns the binary transferable public key.
func (k *Key) PublicBytes() ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.publicBytes()
}

func (k *Key) publicBytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := k.entity.Serialize(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SecretBytes returns the binary transferable secret key. If the key is
// unlocked the secret material is written unprotected; export the stored
// (locked) key to keep the passphrase protection.
func (k *Key) SecretBytes() ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.secretBytes()
}

func (k *Key) secretBytes() ([]byte, error) {
	if k.entity.PrivateKey == nil {
		return nil, ErrNotSecretKey
	}
	var buf bytes.Buffer
	if err := k.entity.SerializePrivateWithoutSigning(&buf, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Bytes returns the secret key bytes for secret keys and the public key
// bytes otherwise. It is the canonical form used by KeyStore.
func (k *Key) Bytes() ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.entity.PrivateKey != nil {
		return k.secretBytes()
	}
	return k.publicBytes()
}

// ArmoredPublic returns the ASCII-armored public key.
func (k *Key) ArmoredPublic() (string, error) {
	b, err := k.PublicBytes()
	if err != nil {
		return "", err
	}
	return armorBytes(b, armorPublicKey, nil)
}

// ArmoredSecret returns the ASCII-armored secret key (see SecretBytes).
func (k *Key) ArmoredSecret() (string, error) {
	b, err := k.SecretBytes()
	if err != nil {
		return "", err
	}
	return armorBytes(b, armorPrivateKey, nil)
}

func armorBytes(b []byte, blockType string, headers map[string]string) (string, error) {
	var buf bytes.Buffer
	w, err := armor.EncodeWithChecksumOption(&buf, blockType, headers, false)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(b); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	buf.WriteByte('\n')
	return buf.String(), nil
}

// Public returns a copy of the key without any secret key material.
func (k *Key) Public() *Key {
	b, err := k.PublicBytes()
	if err != nil {
		return k
	}
	el, err := openpgp.ReadKeyRing(bytes.NewReader(b))
	if err != nil || len(el) == 0 {
		return k
	}
	return &Key{entity: el[0]}
}

// RestrictToEmail returns a public copy of the key with the user IDs of that
// address only. A directory answers for one address: further user IDs in its
// answer must not make the key usable for other people.
func (k *Key) RestrictToEmail(addr string) (*Key, error) {
	c, err := k.Public().Clone()
	if err != nil {
		return nil, err
	}
	want := NormalizeEmail(addr)
	for name, id := range c.entity.Identities {
		email := name
		if id.UserId != nil && id.UserId.Email != "" {
			email = id.UserId.Email
		}
		if NormalizeEmail(email) != want {
			delete(c.entity.Identities, name)
		}
	}
	if len(c.entity.Identities) == 0 {
		return nil, ErrKeyNotFound
	}
	return c.Clone()
}

// Clone returns a deep copy of the key.
func (k *Key) Clone() (*Key, error) {
	b, err := k.Bytes()
	if err != nil {
		return nil, err
	}
	el, err := openpgp.ReadKeyRing(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if len(el) != 1 {
		return nil, fmt.Errorf("pgp: clone produced %d keys", len(el))
	}
	return &Key{entity: el[0]}, nil
}

// ---------------------------------------------------------------------------
// Secret key operations

// Unlock returns a copy of the key with all secret key material decrypted.
// Keys that are not protected are returned as a copy unchanged.
func (k *Key) Unlock(passphrase []byte) (*Key, error) {
	if !k.IsSecret() {
		return nil, ErrNotSecretKey
	}
	c, err := k.Clone()
	if err != nil {
		return nil, err
	}
	if !c.IsLocked() {
		return c, nil
	}
	if err := c.entity.DecryptPrivateKeys(passphrase); err != nil {
		return nil, ErrWrongPassphrase
	}
	return c, nil
}

// ChangePassphrase returns a copy of the key protected with newPassphrase.
// oldPassphrase is ignored for unprotected keys. An empty newPassphrase
// removes the protection.
func (k *Key) ChangePassphrase(oldPassphrase, newPassphrase []byte) (*Key, error) {
	u, err := k.Unlock(oldPassphrase)
	if err != nil {
		return nil, err
	}
	if len(newPassphrase) == 0 {
		return u, nil
	}
	if err := u.entity.EncryptPrivateKeys(newPassphrase, nil); err != nil {
		return nil, fmt.Errorf("pgp: encrypt secret key: %w", err)
	}
	// Round-trip through serialization so the returned key is in its stored
	// (locked) state.
	return u.Clone()
}

// RevocationReason is the reason code of a key revocation (RFC 9580 5.2.3.31).
type RevocationReason uint8

const (
	RevocationNoReason    RevocationReason = RevocationReason(packet.NoReason)
	RevocationSuperseded  RevocationReason = RevocationReason(packet.KeySuperseded)
	RevocationCompromised RevocationReason = RevocationReason(packet.KeyCompromised)
	RevocationRetired     RevocationReason = RevocationReason(packet.KeyRetired)
)

// Revoke returns a copy of the key carrying a key revocation signature. The
// receiver must be an unlocked secret key. The returned key is still a
// secret key; export its public part (ArmoredPublic) to publish the
// revocation.
func (k *Key) Revoke(reason RevocationReason, text string, now time.Time) (*Key, error) {
	c, err := k.unlockedClone()
	if err != nil {
		return nil, err
	}
	if err := c.entity.Revoke(packet.ReasonForRevocation(reason), text, configAt(now)); err != nil {
		return nil, fmt.Errorf("pgp: revoke: %w", err)
	}
	return c, nil
}

// RevokeSubkey returns a copy of the key in which the subkey with the given
// fingerprint is revoked. The receiver must be an unlocked secret key.
func (k *Key) RevokeSubkey(subkeyFingerprint string, reason RevocationReason, text string, now time.Time) (*Key, error) {
	c, err := k.unlockedClone()
	if err != nil {
		return nil, err
	}
	want := NormalizeFingerprint(subkeyFingerprint)
	for i := range c.entity.Subkeys {
		sk := &c.entity.Subkeys[i]
		if strings.ToUpper(hex.EncodeToString(sk.PublicKey.Fingerprint)) == want {
			if err := sk.Revoke(packet.ReasonForRevocation(reason), text, configAt(now)); err != nil {
				return nil, fmt.Errorf("pgp: revoke subkey: %w", err)
			}
			return c, nil
		}
	}
	return nil, ErrKeyNotFound
}

func (k *Key) unlockedClone() (*Key, error) {
	if !k.IsSecret() {
		return nil, ErrNotSecretKey
	}
	if k.IsLocked() {
		return nil, ErrKeyLocked
	}
	return k.Clone()
}

// configAt returns a go-crypto config whose clock is fixed to now (or the
// real clock if now is zero).
func configAt(now time.Time) *packet.Config {
	cfg := &packet.Config{DefaultHash: 0}
	if !now.IsZero() {
		cfg.Time = func() time.Time { return now }
	}
	return cfg
}

// writeArmored writes data as an armored block to w.
func writeArmored(w io.Writer, blockType string, data []byte) error {
	aw, err := armor.EncodeWithChecksumOption(w, blockType, nil, false)
	if err != nil {
		return err
	}
	if _, err := aw.Write(data); err != nil {
		return err
	}
	return aw.Close()
}
