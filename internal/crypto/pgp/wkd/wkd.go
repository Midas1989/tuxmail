// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wkd implements OpenPGP Web Key Directory lookups
// (draft-koch-openpgp-webkey-service): the advanced method
// (https://openpgpkey.<domain>/.well-known/openpgpkey/<domain>/hu/<hash>?l=<local>)
// with fallback to the direct method
// (https://<domain>/.well-known/openpgpkey/hu/<hash>?l=<local>).
package wkd

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
)

// MaxResponseSize limits the size of a WKD response.
const MaxResponseSize = 2 << 20

const zbase32Alphabet = "ybndrfg8ejkmcpqxot1uwisza345h769"

// ZBase32 encodes b with the z-base-32 alphabet (no padding).
func ZBase32(b []byte) string {
	var sb strings.Builder
	var buf uint64
	bits := 0
	for _, c := range b {
		buf = buf<<8 | uint64(c)
		bits += 8
		for bits >= 5 {
			bits -= 5
			sb.WriteByte(zbase32Alphabet[(buf>>uint(bits))&0x1f])
		}
	}
	if bits > 0 {
		sb.WriteByte(zbase32Alphabet[(buf<<uint(5-bits))&0x1f])
	}
	return sb.String()
}

// HashLocalPart returns the z-base-32 encoded SHA-1 of the lower-cased
// local part, as used in WKD URLs.
func HashLocalPart(local string) string {
	sum := sha1.Sum([]byte(strings.ToLower(local)))
	return ZBase32(sum[:])
}

// splitAddress splits an e-mail address into local part and lower-cased
// domain.
func splitAddress(email string) (local, domain string, err error) {
	email = strings.TrimSpace(email)
	i := strings.LastIndexByte(email, '@')
	if i <= 0 || i == len(email)-1 {
		return "", "", fmt.Errorf("wkd: invalid e-mail address %q", email)
	}
	local, domain = email[:i], strings.ToLower(email[i+1:])
	if strings.ContainsAny(domain, "/?#@ ") {
		return "", "", fmt.Errorf("wkd: invalid domain %q", domain)
	}
	return local, domain, nil
}

// AdvancedURL returns the URL of the advanced method for email.
func AdvancedURL(email string) (string, error) {
	local, domain, err := splitAddress(email)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://openpgpkey.%s/.well-known/openpgpkey/%s/hu/%s?l=%s",
		domain, domain, HashLocalPart(local), url.QueryEscape(local)), nil
}

// DirectURL returns the URL of the direct method for email.
func DirectURL(email string) (string, error) {
	local, domain, err := splitAddress(email)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s/.well-known/openpgpkey/hu/%s?l=%s",
		domain, HashLocalPart(local), url.QueryEscape(local)), nil
}

// Client performs WKD lookups. The zero value uses http.DefaultClient.
type Client struct {
	// HTTPClient is used for requests (inject for tests or proxies).
	HTTPClient *http.Client
	// UserAgent is sent with each request (optional).
	UserAgent string
}

// Source implements pgp.KeyLookup.
func (c *Client) Source() pgp.KeySource { return pgp.SourceWKD }

// LookupByEmail implements pgp.KeyLookup. It tries the advanced method and
// falls back to the direct method. Only keys carrying a user ID with the
// requested address are returned. pgp.ErrKeyNotFound is returned if neither
// method yields a matching key.
func (c *Client) LookupByEmail(ctx context.Context, email string) ([]*pgp.Key, error) {
	adv, err := AdvancedURL(email)
	if err != nil {
		return nil, err
	}
	direct, _ := DirectURL(email)

	var errs []error
	for _, u := range []string{adv, direct} {
		data, err := c.fetch(ctx, u)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			errs = append(errs, err)
			continue
		}
		keys, err := pgp.ReadKeys(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("wkd: %s: %w", u, err))
			continue
		}
		var out []*pgp.Key
		for _, k := range keys {
			if k.HasEmail(email) {
				out = append(out, k.Public())
			}
		}
		if len(out) > 0 {
			return out, nil
		}
		errs = append(errs, fmt.Errorf("wkd: %s: no key with user ID %s", u, email))
	}
	return nil, fmt.Errorf("%w: %w", pgp.ErrKeyNotFound, errors.Join(errs...))
}

func (c *Client) fetch(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wkd: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wkd: %s: HTTP %d", u, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("wkd: %s: %w", u, err)
	}
	if len(data) > MaxResponseSize {
		return nil, fmt.Errorf("wkd: %s: response too large", u)
	}
	return data, nil
}

var _ pgp.KeyLookup = (*Client)(nil)
