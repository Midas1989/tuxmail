// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Watch implements domain.MailProvider with EAS Ping (long-poll push).
// All watched folders of a provider share one Ping loop: servers cancel a
// running Ping when the same device sends another one. A hint is emitted
// for every folder the server reports as changed, after reconnects and
// when the server asks for a folder hierarchy sync. The channel is closed
// when ctx ends.
func (p *Provider) Watch(ctx context.Context, folderRemoteID string) (<-chan domain.ChangeHint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.hub.subscribe(ctx, folderRemoteID), nil
}

type pingSub struct {
	folder string // remote id
	ch     chan domain.ChangeHint
}

type pingHub struct {
	p *Provider

	mu        sync.Mutex
	subs      []*pingSub
	running   bool
	stopped   bool
	cancel    context.CancelFunc // of the loop
	interrupt context.CancelFunc // of the running Ping
	heartbeat time.Duration
	maxFolder int
	// awaiting holds collections reported as changed that have not been
	// synced yet; the next Ping waits for them (servers answer a Ping
	// immediately while changes are pending).
	awaiting map[string]bool
	synced   chan struct{}
}

func newPingHub(p *Provider) *pingHub {
	return &pingHub{p: p, heartbeat: p.cfg.Heartbeat, awaiting: map[string]bool{}, synced: make(chan struct{}, 1)}
}

// markSynced is called after a collection was synchronized.
func (h *pingHub) markSynced(id string) {
	h.mu.Lock()
	_, waiting := h.awaiting[id]
	delete(h.awaiting, id)
	h.mu.Unlock()
	if waiting {
		select {
		case h.synced <- struct{}{}:
		default:
		}
	}
}

// waitSynced blocks until the awaited collections were synced, at most d.
func (h *pingHub) waitSynced(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		h.mu.Lock()
		n := len(h.awaiting)
		h.mu.Unlock()
		if n == 0 {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			h.mu.Lock()
			h.awaiting = map[string]bool{}
			h.mu.Unlock()
			return true
		case <-h.synced:
		}
	}
}

func (h *pingHub) subscribe(ctx context.Context, folder string) <-chan domain.ChangeHint {
	s := &pingSub{folder: folder, ch: make(chan domain.ChangeHint, 1)}
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		close(s.ch)
		return s.ch
	}
	h.subs = append(h.subs, s)
	if !h.running {
		h.running = true
		lctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go h.loop(lctx)
	} else if h.interrupt != nil {
		h.interrupt() // restart Ping with the new folder set
	}
	h.mu.Unlock()
	context.AfterFunc(ctx, func() { h.unsubscribe(s) })
	return s.ch
}

func (h *pingHub) unsubscribe(s *pingSub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, x := range h.subs {
		if x == s {
			h.subs = append(h.subs[:i], h.subs[i+1:]...)
			close(s.ch)
			if h.interrupt != nil {
				h.interrupt()
			}
			return
		}
	}
}

func (h *pingHub) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = true
	if h.cancel != nil {
		h.cancel()
	}
	for _, s := range h.subs {
		close(s.ch)
	}
	h.subs = nil
}

// emit sends a hint to the subscribers of the given collection ids (all
// subscribers if ids is nil).
func (h *pingHub) emit(ids []string) {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		if ids != nil && !want[CollectionID(s.folder)] {
			continue
		}
		select {
		case s.ch <- domain.ChangeHint{FolderRemoteID: s.folder}:
		default:
		}
	}
}

func (h *pingHub) folders() []PingFolder {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	var out []PingFolder
	for _, s := range h.subs {
		id := CollectionID(s.folder)
		if !seen[id] {
			seen[id] = true
			out = append(out, PingFolder{ID: id, Class: "Email"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if h.maxFolder > 0 && len(out) > h.maxFolder {
		out = out[:h.maxFolder]
	}
	return out
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (h *pingHub) loop(ctx context.Context) {
	p := h.p
	backoff := p.cfg.MinBackoff
	failed := false
	for ctx.Err() == nil {
		folders := h.folders()
		h.mu.Lock()
		if len(folders) == 0 {
			h.running, h.interrupt = false, nil
			h.cancel()
			h.mu.Unlock()
			return
		}
		pctx, interrupt := context.WithCancel(ctx)
		h.interrupt = interrupt
		hb := h.heartbeat
		h.mu.Unlock()

		res, err := p.c.Ping(pctx, hb, folders)
		interrupted := pctx.Err() != nil && ctx.Err() == nil
		interrupt()
		if ctx.Err() != nil {
			return
		}
		if interrupted {
			continue
		}
		if err == nil && (res.Status == PingServerError || res.Status == PingSyntaxError) {
			err = &StatusError{Cmd: "Ping", Status: res.Status}
		}
		if err != nil {
			p.log.Info("eas: ping failed", "err", err)
			failed = true
			d := backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)+1))
			backoff = min(backoff*2, p.cfg.MaxBackoff)
			if !sleepCtx(ctx, d) {
				return
			}
			continue
		}
		backoff = p.cfg.MinBackoff
		if failed {
			failed = false
			h.emit(nil) // changes may have been missed
		}
		switch res.Status {
		case PingExpired, PingMissingParams:
		case PingChanges:
			h.mu.Lock()
			for _, id := range res.Changed {
				h.awaiting[id] = true
			}
			h.mu.Unlock()
			h.emit(res.Changed)
			if !h.waitSynced(ctx, p.cfg.RearmTimeout) {
				return
			}
		case PingBadHeartbeat:
			h.mu.Lock()
			if res.Heartbeat > 0 {
				h.heartbeat = time.Duration(res.Heartbeat) * time.Second
			} else {
				h.heartbeat = max(h.heartbeat/2, time.Minute)
			}
			h.mu.Unlock()
		case PingTooManyFolders:
			h.mu.Lock()
			h.maxFolder = max(res.MaxFolders, 1)
			h.mu.Unlock()
		case PingHierarchyChanged:
			// Refresh the hierarchy (the cached list is what ListFolders
			// returns), then let the engine resync all watched folders.
			if _, err := p.c.FolderSync(ctx); err != nil {
				p.log.Info("eas: folder sync after ping failed", "err", err)
				if !sleepCtx(ctx, backoff) {
					return
				}
			}
			h.emit(nil)
		}
	}
}
