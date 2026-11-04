// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/hmac"
	"crypto/rand"
	// #nosec G505 -- HMAC-SHA1 is mandated by RFC 4226/6238 (authenticator app compatibility)
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strings"
	"time"
)

// TOTP implements RFC 6238 time-based one-time passwords (RFC 4226 HOTP with
// a time-step counter). TuxMail enrols SHA-1, 6 digits, 30 s steps, which is
// what all common authenticator apps support.
type TOTP struct {
	Secret []byte
	Digits int              // default 6
	Period time.Duration    // default 30s
	Hash   func() hash.Hash // default sha1.New
}

// TOTP algorithm constructors, exported for tests with RFC 6238 vectors.
var (
	TOTPSHA1   = sha1.New
	TOTPSHA256 = sha256.New
	TOTPSHA512 = sha512.New
)

func (t TOTP) digits() int {
	if t.Digits == 0 {
		return 6
	}
	return t.Digits
}

func (t TOTP) period() time.Duration {
	if t.Period == 0 {
		return 30 * time.Second
	}
	return t.Period
}

// Step returns the time-step counter for at.
func (t TOTP) Step(at time.Time) int64 {
	return at.Unix() / int64(t.period()/time.Second)
}

// CodeAt returns the code for the given time-step counter.
func (t TOTP) CodeAt(step int64) string {
	h := t.Hash
	if h == nil {
		h = sha1.New
	}
	mac := hmac.New(h, t.Secret)
	var msg [8]byte
	// #nosec G115 -- TOTP time step, non-negative for any date after 1970
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for range t.digits() {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", t.digits(), bin%mod)
}

// Code returns the code valid at time at.
func (t TOTP) Code(at time.Time) string { return t.CodeAt(t.Step(at)) }

// Verify checks code against the steps at-skew..at+skew and returns the
// matching step. Steps <= lastStep are rejected to prevent replay.
func (t TOTP) Verify(code string, at time.Time, skew int, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != t.digits() {
		return 0, false
	}
	cur := t.Step(at)
	for d := -skew; d <= skew; d++ {
		st := cur + int64(d)
		if st <= lastStep {
			continue
		}
		if hmac.Equal([]byte(t.CodeAt(st)), []byte(code)) {
			return st, true
		}
	}
	return 0, false
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret.
func NewTOTPSecret() []byte {
	s := make([]byte, 20)
	if _, err := rand.Read(s); err != nil {
		panic(err)
	}
	return s
}

// EncodeTOTPSecret returns the base32 form shown to users.
func EncodeTOTPSecret(secret []byte) string { return b32.EncodeToString(secret) }

// TOTPURL returns an otpauth:// URL for QR codes.
func TOTPURL(issuer, account string, secret []byte) string {
	v := url.Values{}
	v.Set("secret", EncodeTOTPSecret(secret))
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	label := url.PathEscape(issuer + ":" + account)
	return "otpauth://totp/" + label + "?" + v.Encode()
}
