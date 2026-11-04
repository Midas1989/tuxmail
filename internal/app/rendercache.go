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

var errNoBody = errors.New("app: body not available")

const maxRenderCache = 256

type renderCache struct {
	mu    sync.Mutex
	m     map[string]RenderedMessage
	order []string
}

var rendered = &renderCache{m: map[string]RenderedMessage{}}

func (c *renderCache) get(k string) (RenderedMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[k]
	return r, ok
}

func (c *renderCache) put(k string, r RenderedMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k]; ok {
		return
	}
	if len(c.order) >= maxRenderCache {
		delete(c.m, c.order[0])
		c.order = c.order[1:]
	}
	c.m[k] = r
	c.order = append(c.order, k)
}

// renderCached renders a stored mail (downloading its body on demand);
// errNoBody when the body is not available.
func (a *App) renderCached(ctx context.Context, m store.Message) (RenderedMessage, error) {
	key := ""
	if m.BlobHash != "" {
		key = m.BlobHash + "#" + strconv.FormatInt(m.ID, 10)
		if r, ok := rendered.get(key); ok {
			return r, nil
		}
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return RenderedMessage{}, errNoBody
	}
	r, err := a.render.Render(WithMessageID(ctx, m.ID), raw)
	if err != nil {
		return r, err
	}
	if key == "" { // fetched on demand: the blob hash is known now
		if mm, err := a.st.GetMessage(ctx, m.ID); err == nil && mm.BlobHash != "" {
			key = mm.BlobHash + "#" + strconv.FormatInt(m.ID, 10)
		}
	}
	if key != "" {
		rendered.put(key, r)
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
