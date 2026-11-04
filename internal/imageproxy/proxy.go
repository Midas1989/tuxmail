// SPDX-License-Identifier: AGPL-3.0-or-later

package imageproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Cache stores fetched images by key (hash of the URL).
type Cache interface {
	Get(ctx context.Context, key string) (Image, bool)
	Put(ctx context.Context, key string, img Image, ttl time.Duration)
}

// Proxy serves proxied images.
type Proxy struct {
	Signer  *Signer
	Fetcher *Fetcher
	Cache   Cache // optional
	// TTL of cached images (default 24 h).
	TTL    time.Duration
	Logger *slog.Logger

	inflight sync.Map // key -> *call (collapses concurrent fetches)
}

type call struct {
	done chan struct{}
	img  Image
	err  error
}

// Key returns the cache key of an image URL.
func Key(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// SignURL returns the signed proxy URL of a remote image (the sanitizer's
// URL rewriting callback).
func (p *Proxy) SignURL(raw string) string { return p.Signer.Sign(raw) }

// Get returns an image from the cache or the network.
func (p *Proxy) Get(ctx context.Context, raw string) (Image, error) {
	key := Key(raw)
	if p.Cache != nil {
		if img, ok := p.Cache.Get(ctx, key); ok {
			return img, nil
		}
	}
	c := &call{done: make(chan struct{})}
	if v, loaded := p.inflight.LoadOrStore(key, c); loaded {
		other := v.(*call)
		select {
		case <-other.done:
			return other.img, other.err
		case <-ctx.Done():
			return Image{}, ctx.Err()
		}
	}
	defer func() {
		p.inflight.Delete(key)
		close(c.done)
	}()
	f := p.Fetcher
	if f == nil {
		f = &Fetcher{}
		p.Fetcher = f
	}
	// Fetch detached from the request so that a cancelled view does not
	// poison concurrent waiters; the fetcher has its own timeout.
	c.img, c.err = f.Fetch(context.WithoutCancel(ctx), raw)
	if c.err == nil && p.Cache != nil {
		ttl := p.TTL
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		p.Cache.Put(ctx, key, c.img, ttl)
	}
	return c.img, c.err
}

// ServeHTTP serves a proxy request. A valid signature is always accepted;
// unsigned requests only when authenticated is true (the caller verified a
// session or bearer token).
func (p *Proxy) Serve(w http.ResponseWriter, r *http.Request, authenticated bool) {
	q := r.URL.Query()
	raw := q.Get("url")
	if Signed(q) && p.Signer != nil {
		var err error
		if raw, _, err = p.Signer.Verify(q); err != nil {
			http.Error(w, "invalid or expired image link", http.StatusForbidden)
			return
		}
	} else if !authenticated {
		http.Error(w, "image link not signed", http.StatusForbidden)
		return
	}
	img, err := p.Get(r.Context(), raw)
	if err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, ErrBadURL):
			status = http.StatusBadRequest
		case errors.Is(err, ErrBlockedAddress):
			status = http.StatusForbidden
		case errors.Is(err, ErrNotImage):
			status = http.StatusUnsupportedMediaType
		case errors.Is(err, ErrTooLarge):
			status = http.StatusRequestEntityTooLarge
		}
		if p.Logger != nil {
			p.Logger.Debug("imageproxy: fetch failed", "err", err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	h := w.Header()
	h.Set("Content-Type", img.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(img.Data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "private, max-age=86400")
	// Sandboxed mail frames have an opaque origin.
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	_, _ = w.Write(img.Data)
}
