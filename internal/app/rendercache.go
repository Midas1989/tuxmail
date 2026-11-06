// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/Midas1989/Mailclient/internal/store"
)

// Opening a mail parses and sanitizes its raw MIME. Raw mails are
// immutable (blob hash), so the rendered result is cached: reopening a mail,
// switching back and forth with J/K and the mail window are instant. The
// key includes the message id because cid: URLs contain it.
//
// The cache belongs to one App: the rendered HTML carries image-proxy URLs
// signed for that user (server mode), so entries must never be served to
// another user (NH-05).

var errNoBody = errors.New("app: body not available")

const (
	maxRenderCache      = 256
	maxRenderCacheBytes = 64 << 20 // per App
)

type renderCache struct {
	mu    sync.Mutex
	m     map[string]RenderedMessage
	size  map[string]int
	order []string
	bytes int
}

func (r RenderedMessage) cost() int {
	n := 256 + len(r.Text) + len(r.HTML)
	for _, a := range r.Attachments {
		n += 64 + len(a.Filename) + len(a.MIMEType) + len(a.ContentID)
	}
	return n
}

func (c *renderCache) get(k string) (RenderedMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[k]
	return r, ok
}

func (c *renderCache) put(k string, r RenderedMessage) {
	cost := r.cost()
	if cost > maxRenderCacheBytes/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m, c.size = map[string]RenderedMessage{}, map[string]int{}
	}
	if _, ok := c.m[k]; ok {
		return
	}
	for len(c.order) > 0 && (len(c.m) >= maxRenderCache || c.bytes+cost > maxRenderCacheBytes) {
		old := c.order[0]
		c.order = c.order[1:]
		c.bytes -= c.size[old]
		delete(c.m, old)
		delete(c.size, old)
	}
	c.m[k], c.size[k] = r, cost
	c.order = append(c.order, k)
	c.bytes += cost
}

// clear forgets all entries (the keys that decrypted them changed).
func (c *renderCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m, c.size, c.order, c.bytes = nil, nil, nil, 0
}

// renderCached renders a stored mail (downloading its body on demand);
// errNoBody when the body is not available.
func (a *App) renderCached(ctx context.Context, m store.Message) (RenderedMessage, error) {
	a.pgpSweep() // nothing decrypted outlives its passphrase
	key := ""
	if m.BlobHash != "" {
		key = m.BlobHash + "#" + strconv.FormatInt(m.ID, 10)
		if r, ok := a.rendered.get(key); ok {
			return r, nil
		}
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return RenderedMessage{}, errNoBody
	}
	generation := a.comfort.pgp.generation.Load()
	r, err := a.render.Render(WithMessageID(ctx, m.ID), raw)
	if err != nil {
		return r, err
	}
	if key == "" { // fetched on demand: the blob hash is known now
		if mm, err := a.st.GetMessage(ctx, m.ID); err == nil && mm.BlobHash != "" {
			key = mm.BlobHash + "#" + strconv.FormatInt(m.ID, 10)
		}
	}
	if key != "" && a.comfort.pgp.generation.Load() == generation {
		a.rendered.put(key, r)
	}
	return r, nil
}

// MessageSource returns the raw RFC 5322 source of a mail ("Quelltext
// anzeigen" in the mail window), downloading it on demand.
func (a *App) MessageSource(ctx context.Context, id int64) (string, error) {
	m, err := a.st.GetMessage(ctx, id)
	if err != nil {
		return "", err
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// MessageSource is the Wails binding of App.MessageSource.
func (x *Api) MessageSource(id int64) (string, error) { return r1(x.app.MessageSource(x.ctx, id)) }
