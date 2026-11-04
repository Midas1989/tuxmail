// SPDX-License-Identifier: AGPL-3.0-or-later

package imageproxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// StoreCache keeps images in the blob store with an index in the store
// (table proxy_cache).
type StoreCache struct {
	Store *store.Store
	Blobs *blob.Store
	Now   func() time.Time
}

func (c *StoreCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Get implements Cache.
func (c *StoreCache) Get(ctx context.Context, key string) (Image, bool) {
	e, err := c.Store.GetProxyCache(ctx, key, c.now())
	if err != nil {
		return Image{}, false
	}
	rc, err := c.Blobs.Open(e.BlobHash)
	if err != nil {
		return Image{}, false
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, DefaultMaxSize*2))
	if err != nil {
		return Image{}, false
	}
	return Image{Data: data, ContentType: e.ContentType}, true
}

// Put implements Cache.
func (c *StoreCache) Put(ctx context.Context, key string, img Image, ttl time.Duration) {
	h, err := c.Blobs.Put(bytes.NewReader(img.Data))
	if err != nil {
		return
	}
	now := c.now()
	_ = c.Store.PutProxyCache(ctx, store.ProxyCacheEntry{Key: key, BlobHash: h, ContentType: img.ContentType,
		Size: int64(len(img.Data)), FetchedAt: now, ExpiresAt: now.Add(ttl)})
}

// Purge removes expired entries and their unreferenced blobs.
func (c *StoreCache) Purge(ctx context.Context) error {
	hashes, err := c.Store.PurgeProxyCache(ctx, c.now())
	var errs []error
	for _, h := range hashes {
		if err := c.Blobs.Delete(h); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(append(errs, err)...)
}
