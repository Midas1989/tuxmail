// SPDX-License-Identifier: AGPL-3.0-or-later

package imap

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Watch opens a dedicated connection, selects folderRemoteID read-only and
// emits a ChangeHint whenever the server reports new, expunged or changed
// messages. It uses IDLE when available (re-issued periodically by
// go-imap) and NOOP polling otherwise. Lost connections are re-established
// with exponential backoff; a hint is emitted after every reconnect because
// changes may have been missed. Hints are coalesced: the channel has a buffer
// of one and never blocks the watcher. The channel is closed when ctx ends.
func (p *Provider) Watch(ctx context.Context, folderRemoteID string) (<-chan domain.ChangeHint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(chan domain.ChangeHint, 1)
	w := &watcher{p: p, folder: folderRemoteID, out: out, notify: make(chan struct{}, 1)}

	// Establish the first session synchronously so configuration errors
	// surface to the caller.
	c, err := w.connect(ctx)
	if err != nil {
		return nil, err
	}
	go w.run(ctx, c)
	return out, nil
}

var errConnClosed = errors.New("imap: connection closed")

type watcher struct {
	p      *Provider
	folder string
	out    chan domain.ChangeHint
	notify chan struct{}
}

func (w *watcher) signal() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *watcher) emit() {
	select {
	case w.out <- domain.ChangeHint{FolderRemoteID: w.folder}:
	default: // a hint is already pending
	}
}

func (w *watcher) connect(ctx context.Context) (*imapclient.Client, error) {
	handler := &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { w.signal() },
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				w.signal()
			}
		},
		Fetch: func(msg *imapclient.FetchMessageData) {
			// drain the item stream so the decoder can continue
			for item := msg.Next(); item != nil; item = msg.Next() {
			}
			w.signal()
		},
	}
	c, err := w.p.dial(ctx, handler)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if _, err := c.Select(w.folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		c.Close()
		return nil, ctxErr(ctx, err)
	}
	return c, nil
}

func (w *watcher) run(ctx context.Context, c *imapclient.Client) {
	defer close(w.out)
	backoff := w.p.cfg.MinBackoff
	for {
		err := w.session(ctx, c)
		c.Close()
		if ctx.Err() != nil {
			return
		}
		w.p.log.Info("imap: watch connection lost", "folder", w.folder, "err", err)
		for {
			// full jitter in [backoff/2, backoff)
			d := backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
			t := time.NewTimer(d)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			backoff = min(backoff*2, w.p.cfg.MaxBackoff)
			c, err = w.connect(ctx)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return
			}
			w.p.log.Info("imap: watch reconnect failed", "folder", w.folder, "err", err)
		}
		backoff = w.p.cfg.MinBackoff
		w.emit() // changes may have happened while disconnected
	}
}

// session runs IDLE or NOOP polling on c until the connection fails or ctx
// ends.
func (w *watcher) session(ctx context.Context, c *imapclient.Client) error {
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	if c.Caps().Has(imap.CapIdle) && !w.p.cfg.DisableIdle {
		idle, err := c.Idle()
		if err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- idle.Wait() }()
		for {
			select {
			case <-w.notify:
				w.emit()
			case err := <-done:
				return err
			case <-c.Closed():
				return errConnClosed
			case <-ctx.Done():
				_ = idle.Close()
				return ctx.Err()
			}
		}
	}

	t := time.NewTicker(w.p.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-w.notify:
			w.emit()
		case <-t.C:
			if err := c.Noop().Wait(); err != nil {
				return err
			}
		case <-c.Closed():
			return errConnClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
