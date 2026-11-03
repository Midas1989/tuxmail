// SPDX-License-Identifier: AGPL-3.0-or-later

// Package vks implements lookups against a Verifying Keyserver (VKS) such
// as keys.openpgp.org (https://keys.openpgp.org/about/api).
package vks

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
)

// DefaultBaseURL is the keys.openpgp.org instance.
const DefaultBaseURL = "https://keys.openpgp.org"

// MaxResponseSize limits the size of a VKS response.
const MaxResponseSize = 2 << 20

// Client queries a VKS instance. The zero value queries keys.openpgp.org
// with http.DefaultClient.
type Client struct {
	// BaseURL of the keyserver, without trailing slash (default
	// DefaultBaseURL).
	BaseURL    string
	HTTPClient *http.Client
	UserAgent  string
}

// Source implements pgp.KeyLookup.
func (c *Client) Source() pgp.KeySource { return pgp.SourceKeyserver }

// LookupByEmail implements pgp.KeyLookup. keys.openpgp.org only publishes
// user IDs whose address was verified by the key owner; keys without a
// matching user ID are dropped.
func (c *Client) LookupByEmail(ctx context.Context, email string) ([]*pgp.Key, error) {
	email = strings.TrimSpace(email)
	if !strings.Contains(email, "@") {
		return nil, fmt.Errorf("vks: invalid e-mail address %q", email)
	}
	keys, err := c.get(ctx, "/vks/v1/by-email/"+url.PathEscape(email))
	if err != nil {
		return nil, err
	}
	var out []*pgp.Key
	for _, k := range keys {
		if k.HasEmail(email) {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil, pgp.ErrKeyNotFound
	}
	return out, nil
}

// LookupByFingerprint fetches the key with the given fingerprint (v4: 40
// hex digits, v6: 64 hex digits). The returned key may be stripped of user
// IDs if none are verified.
func (c *Client) LookupByFingerprint(ctx context.Context, fingerprint string) (*pgp.Key, error) {
	fpr := pgp.NormalizeFingerprint(fingerprint)
	if len(fpr) != 40 && len(fpr) != 64 {
		return nil, fmt.Errorf("vks: invalid fingerprint %q", fingerprint)
	}
	for _, r := range fpr {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return nil, fmt.Errorf("vks: invalid fingerprint %q", fingerprint)
		}
	}
	keys, err := c.get(ctx, "/vks/v1/by-fingerprint/"+fpr)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if k.Fingerprint() == fpr {
			return k, nil
		}
	}
	return nil, fmt.Errorf("%w: keyserver returned a different key", pgp.ErrKeyNotFound)
}

// LookupByKeyID fetches keys by 64-bit key ID (16 hex digits).
func (c *Client) LookupByKeyID(ctx context.Context, keyID string) ([]*pgp.Key, error) {
	id := pgp.NormalizeFingerprint(keyID)
	if len(id) != 16 {
		return nil, fmt.Errorf("vks: invalid key ID %q", keyID)
	}
	return c.get(ctx, "/vks/v1/by-keyid/"+id)
}

func (c *Client) get(ctx context.Context, path string) ([]*pgp.Key, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/pgp-keys")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vks: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, pgp.ErrKeyNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("vks: %s: HTTP %d", path, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("vks: %w", err)
	}
	if len(data) > MaxResponseSize {
		return nil, fmt.Errorf("vks: response too large")
	}
	keys, err := pgp.ReadKeys(data)
	if err != nil {
		return nil, fmt.Errorf("vks: %w", err)
	}
	out := make([]*pgp.Key, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Public())
	}
	return out, nil
}

var _ pgp.KeyLookup = (*Client)(nil)
