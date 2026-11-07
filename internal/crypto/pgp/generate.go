// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"crypto"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// KeyAlgorithm selects the key type for GenerateKey.
type KeyAlgorithm string

const (
	// AlgoCurve25519 generates a v4 key with an EdDSA (Ed25519) primary key
	// and an ECDH (Curve25519) encryption subkey. This is the default and is
	// understood by virtually all current OpenPGP implementations
	// (GnuPG >= 2.1, Thunderbird, K-9/Thunderbird Android, Delta Chat ...).
	AlgoCurve25519 KeyAlgorithm = "curve25519"
	// AlgoCurve25519V6 generates an RFC 9580 v6 key (Ed25519 + X25519).
	// Not all implementations support v6 keys yet.
	AlgoCurve25519V6 KeyAlgorithm = "curve25519-v6"
	// AlgoRSA4096 generates a v4 RSA-4096 key with an RSA-4096 encryption
	// subkey for maximum compatibility with old software.
	AlgoRSA4096 KeyAlgorithm = "rsa4096"
)

// UserIDSpec describes a user ID for key generation.
type UserIDSpec struct {
	Name    string
	Comment string
	Email   string
}

func (u UserIDSpec) String() string {
	if uid := packet.NewUserId(u.Name, u.Comment, u.Email); uid != nil {
		return uid.Id
	}
	return u.Name + " (" + u.Comment + ") <" + u.Email + ">"
}

// GenerateOptions configures GenerateKey.
type GenerateOptions struct {
	// UserIDs to bind to the key. The first one becomes the primary user ID.
	// At least one is required for v4 keys.
	UserIDs []UserIDSpec
	// Algorithm defaults to AlgoCurve25519.
	Algorithm KeyAlgorithm
	// Passphrase protects the secret key. Empty means unprotected.
	Passphrase []byte
	// Lifetime is the validity period of the key; zero means no expiry.
	Lifetime time.Duration
	// Now overrides the creation time (mainly for tests).
	Now time.Time
	// Rand overrides the randomness source (mainly for tests).
	Rand io.Reader
}

// GenerateKey generates a new secret key. If a passphrase is given the
// returned key is locked (as it would be stored); use Unlock to use it.
func GenerateKey(opts GenerateOptions) (*Key, error) {
	cfg := &packet.Config{
		Rand:          opts.Rand,
		DefaultHash:   crypto.SHA256,
		DefaultCipher: packet.CipherAES256,
	}
	// The library rounds the creation time to the nearest second, so a key made
	// in the second half of a second would lie up to half a second in the
	// future and be unusable right away. Truncate instead.
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.Truncate(time.Second)
	cfg.Time = func() time.Time { return now }
	if opts.Lifetime > 0 {
		secs := opts.Lifetime / time.Second
		if secs > math.MaxUint32 {
			return nil, errors.New("pgp: key lifetime too long")
		}
		if secs == 0 {
			secs = 1
		}
		cfg.KeyLifetimeSecs = uint32(secs)
	}
	switch opts.Algorithm {
	case "", AlgoCurve25519:
		cfg.Algorithm = packet.PubKeyAlgoEdDSA
		cfg.Curve = packet.Curve25519
	case AlgoCurve25519V6:
		cfg.V6Keys = true
		cfg.Algorithm = packet.PubKeyAlgoEd25519
		cfg.DefaultHash = crypto.SHA512
		cfg.AEADConfig = &packet.AEADConfig{DefaultMode: packet.AEADModeOCB}
	case AlgoRSA4096:
		cfg.Algorithm = packet.PubKeyAlgoRSA
		cfg.RSABits = 4096
	default:
		return nil, fmt.Errorf("pgp: unsupported key algorithm %q", opts.Algorithm)
	}
	if len(opts.UserIDs) == 0 && !cfg.V6Keys {
		return nil, errors.New("pgp: at least one user ID is required")
	}
	for _, u := range opts.UserIDs {
		if strings.ContainsAny(u.Name+u.Comment+u.Email, "()<>\x00") {
			return nil, fmt.Errorf("pgp: user ID %q contains invalid characters", u.String())
		}
	}

	var (
		e   *openpgp.Entity
		err error
	)
	if len(opts.UserIDs) == 0 {
		e, err = openpgp.NewEntityWithoutId(cfg)
	} else {
		first := opts.UserIDs[0]
		e, err = openpgp.NewEntity(first.Name, first.Comment, first.Email, cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("pgp: generate key: %w", err)
	}
	for _, u := range opts.UserIDs[min(1, len(opts.UserIDs)):] {
		if err := e.AddUserId(u.Name, u.Comment, u.Email, cfg); err != nil {
			return nil, fmt.Errorf("pgp: add user ID %q: %w", u.String(), err)
		}
	}
	if len(opts.Passphrase) > 0 {
		if err := e.EncryptPrivateKeys(opts.Passphrase, cfg); err != nil {
			return nil, fmt.Errorf("pgp: protect key: %w", err)
		}
	}
	k := &Key{entity: e}
	// Normalise through serialization so that the returned key is
	// indistinguishable from one loaded from storage.
	return k.Clone()
}

// AddUserID returns a copy of the (unlocked secret) key with an additional
// user ID.
func (k *Key) AddUserID(u UserIDSpec, now time.Time) (*Key, error) {
	c, err := k.unlockedClone()
	if err != nil {
		return nil, err
	}
	cfg := configAt(now)
	if exp := k.Expires(); !exp.IsZero() {
		secs := max(min(exp.Sub(k.CreationTime())/time.Second, math.MaxUint32), 1)
		cfg.KeyLifetimeSecs = uint32(secs) // #nosec G115 -- clamped to 1..MaxUint32 above
	}
	if err := c.entity.AddUserId(u.Name, u.Comment, u.Email, cfg); err != nil {
		return nil, fmt.Errorf("pgp: add user ID: %w", err)
	}
	return c, nil
}
