// SPDX-License-Identifier: AGPL-3.0-or-later

// Package imageproxy loads remote images of HTML mails on behalf of the
// reader (KONZEPT 8.3): the sender never sees the reader's IP address,
// cookies or referrer, and only images reach the mail view.
//
// Mail iframes are sandboxed (opaque origin) and, in the mobile shell,
// cannot send Authorization headers, so image URLs carry an HMAC token
// (Signer) with expiry instead of relying on the session. Fetching is
// guarded against SSRF: only http(s) on default-ish ports, every dialed IP
// is checked after DNS resolution (which also defeats DNS rebinding), and
// private, loopback, link-local, multicast and other special ranges are
// refused. Responses are limited in size and type and cached.
package imageproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Path is the route of the proxy.
const Path = "/api/proxy/image"

// Signer creates and verifies signed proxy URLs.
type Signer struct {
	// Key is the per-installation secret (at least 32 bytes).
	Key []byte
	// Subject binds the URL to a user (server mode: the user id), so that
	// the server can route an unauthenticated request to the right cache.
	Subject string
	// TTL is the validity of signed URLs (default 7 days). Expiry is rounded
	// up to whole hours so that re-rendering yields cacheable URLs.
	TTL time.Duration
	Now func() time.Time
}

// ErrBadSignature is returned for invalid or expired tokens.
var ErrBadSignature = errors.New("imageproxy: invalid or expired signature")

func (s *Signer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Signer) mac(raw, sub string, exp int64) string {
	h := hmac.New(sha256.New, s.Key)
	h.Write([]byte("tuxmail-imageproxy\x00" + sub + "\x00" + strconv.FormatInt(exp, 10) + "\x00" + raw))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:24])
}

// Sign returns the proxy URL (path and query) for a remote image URL.
func (s *Signer) Sign(raw string) string {
	ttl := s.TTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	exp := s.now().Add(ttl).Truncate(time.Hour).Add(time.Hour).Unix()
	// "url" comes first so that the link matches the sanitizer's proxy
	// template prefix ("/api/proxy/image?url=").
	q := "url=" + url.QueryEscape(raw) + "&e=" + strconv.FormatInt(exp, 10) + "&s=" + s.mac(raw, s.Subject, exp)
	if s.Subject != "" {
		q += "&u=" + url.QueryEscape(s.Subject)
	}
	return Path + "?" + q
}

// Verify checks the token of a proxy request and returns the image URL and
// subject.
func (s *Signer) Verify(q url.Values) (raw, subject string, err error) {
	raw, subject = q.Get("url"), q.Get("u")
	sig, e := q.Get("s"), q.Get("e")
	if raw == "" || sig == "" || e == "" || len(s.Key) == 0 {
		return "", "", ErrBadSignature
	}
	exp, perr := strconv.ParseInt(e, 10, 64)
	if perr != nil || s.now().Unix() > exp {
		return "", "", ErrBadSignature
	}
	if !hmac.Equal([]byte(sig), []byte(s.mac(raw, subject, exp))) {
		return "", "", ErrBadSignature
	}
	return raw, subject, nil
}

// Signed reports whether a query carries a token at all.
func Signed(q url.Values) bool { return q.Get("s") != "" && strings.TrimSpace(q.Get("e")) != "" }
