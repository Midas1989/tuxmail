// SPDX-License-Identifier: AGPL-3.0-or-later

package imageproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/Midas1989/Mailclient/internal/netguard"
)

// Errors of Fetch.
var (
	ErrBlockedAddress = errors.New("imageproxy: destination address not allowed")
	ErrBadURL         = errors.New("imageproxy: invalid image URL")
	ErrNotImage       = errors.New("imageproxy: not an image")
	ErrTooLarge       = errors.New("imageproxy: image too large")
)

// DefaultMaxSize is the size limit of a proxied image.
const DefaultMaxSize = 10 << 20

// allowedTypes are the image types passed through. SVG is excluded: it is
// active content.
var allowedTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"image/bmp": true, "image/x-icon": true, "image/vnd.microsoft.icon": true, "image/avif": true}

// Image is a fetched image.
type Image struct {
	Data        []byte
	ContentType string
}

// Fetcher downloads images safely.
type Fetcher struct {
	// MaxSize bounds the body (default 10 MiB).
	MaxSize int64
	// Timeout bounds the whole request (default 15 s).
	Timeout time.Duration
	// AllowPrivate disables the address checks (tests only).
	AllowPrivate bool
	// Transport overrides the HTTP transport (tests); the address checks
	// are only applied by the default transport.
	Transport http.RoundTripper

	client *http.Client
}

// blocked reports whether an address must not be contacted: everything
// netguard does not classify as public, plus NAT64 ranges in general.
func blocked(a netip.Addr) bool {
	a = a.Unmap()
	return !netguard.IsPublic(a) || nat64.Contains(a)
}

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// control runs after DNS resolution for every connection attempt, so a
// host name that resolves (or re-resolves) to an internal address is
// refused at connect time.
func control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	if blocked(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlockedAddress, ap.Addr())
	}
	return nil
}

func (f *Fetcher) httpClient() *http.Client {
	if f.client != nil {
		return f.client
	}
	tr := f.Transport
	if tr == nil {
		d := &net.Dialer{Timeout: 10 * time.Second}
		if !f.AllowPrivate {
			d.Control = control
		}
		tr = &http.Transport{
			Proxy:                  nil, // never through the environment proxy (would bypass the checks)
			DialContext:            d.DialContext,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  10 * time.Second,
			MaxIdleConns:           20,
			IdleConnTimeout:        60 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
		}
	}
	f.client = &http.Client{
		Transport: tr,
		Jar:       nil, // no cookies
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return errors.New("imageproxy: too many redirects")
			}
			if err := checkURL(req.URL, f.AllowPrivate); err != nil {
				return err
			}
			req.Header.Del("Referer")
			req.Header.Del("Cookie")
			return nil
		},
	}
	return f.client
}

// checkURL validates scheme, port and literal IP hosts.
func checkURL(u *url.URL, allowPrivate bool) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ErrBadURL
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" && port != "8080" && port != "8443" && !allowPrivate {
		return fmt.Errorf("%w: port %s", ErrBlockedAddress, port)
	}
	h := strings.Trim(u.Hostname(), "[]")
	if strings.EqualFold(h, "localhost") || strings.HasSuffix(strings.ToLower(h), ".localhost") {
		if !allowPrivate {
			return ErrBlockedAddress
		}
	}
	if a, err := netip.ParseAddr(h); err == nil && blocked(a) && !allowPrivate {
		return ErrBlockedAddress
	}
	return nil
}

// Fetch downloads an image.
func (f *Fetcher) Fetch(ctx context.Context, raw string) (Image, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Image{}, ErrBadURL
	}
	if err := checkURL(u, f.AllowPrivate); err != nil {
		return Image{}, err
	}
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Image{}, ErrBadURL
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif,image/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; TuxMail image proxy)")
	resp, err := f.httpClient().Do(req)
	if err != nil {
		return Image{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Image{}, fmt.Errorf("imageproxy: upstream %s", resp.Status)
	}
	max := f.MaxSize
	if max <= 0 {
		max = DefaultMaxSize
	}
	if resp.ContentLength > max {
		return Image{}, ErrTooLarge
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	ct = strings.ToLower(ct)
	if ct != "" && !strings.HasPrefix(ct, "image/") && ct != "application/octet-stream" {
		return Image{}, fmt.Errorf("%w: %s", ErrNotImage, ct)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return Image{}, err
	}
	if int64(len(data)) > max {
		return Image{}, ErrTooLarge
	}
	// The sniffed type is authoritative (servers lie about types).
	sniffed := strings.ToLower(strings.Split(http.DetectContentType(data), ";")[0])
	switch {
	case allowedTypes[sniffed]:
		ct = sniffed
	case allowedTypes[ct] && (sniffed == "application/octet-stream"):
		// e.g. AVIF, which DetectContentType does not know.
	default:
		return Image{}, fmt.Errorf("%w: %s", ErrNotImage, sniffed)
	}
	return Image{Data: data, ContentType: ct}, nil
}
