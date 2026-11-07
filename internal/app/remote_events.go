// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

// Live events of the server in server mode: the core holds the WebSocket
// (token in a header) and hands each frame to the window.

const connFrameMax = 1 << 20

// connDialEvents opens the event stream with the device token.
func (a *App) connDialEvents(ctx context.Context, base string) (*websocket.Conn, error) {
	u, err := url.Parse(base + "/api/events")
	if err != nil {
		return nil, err
	}
	origin := *u
	origin.Path = ""
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	cfg, err := websocket.NewConfig(u.String(), origin.String())
	if err != nil {
		return nil, err
	}
	cfg.Protocol = []string{"tuxmail.v1"}
	if err := a.connSetAuthorization(cfg.Header, base); err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, a.cr.timing.ProbeTimeout)
	defer cancel()
	return cfg.DialContext(dctx)
}

func (a *App) connStopStream() {
	a.cr.mu.Lock()
	stop := a.cr.stream
	a.cr.stream = nil
	a.cr.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// connEnsureStream starts the event stream if none runs.
func (a *App) connEnsureStream(ctx context.Context) {
	c := a.connCurrent()
	if c == nil || c.Mode != ConnModeThin {
		return
	}
	a.cr.mu.Lock()
	if a.cr.stream != nil {
		a.cr.mu.Unlock()
		return
	}
	base := a.cr.startCtx
	if base == nil {
		base = ctx
	}
	sctx, cancel := context.WithCancel(base)
	a.cr.stream = cancel
	a.cr.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		err := a.connReadEvents(sctx, c.URL)
		cancel()
		a.cr.mu.Lock()
		a.cr.stream = nil
		a.cr.mu.Unlock()
		if sctx.Err() == nil {
			// Revoked or gone: the next check finds out which. Do not hammer a
			// server that accepts the check but refuses the stream.
			if err != nil {
				a.log.Debug("app: event stream ended", "err", err)
			}
			select {
			case <-base.Done():
			case <-time.After(a.cr.timing.RetryMin / 5):
			}
			a.connKick()
		}
	}()
}

func (a *App) connReadEvents(ctx context.Context, base string) error {
	ws, err := a.connDialEvents(ctx, base)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
	defer stop()
	defer ws.Close()
	ws.MaxPayloadBytes = connFrameMax
	for {
		// Keep-alives come every 45 s; silence means the path is dead.
		_ = ws.SetReadDeadline(time.Now().Add(2 * time.Minute))
		var frame string
		if err := websocket.Message.Receive(ws, &frame); err != nil {
			if errors.Is(err, websocket.ErrFrameTooLarge) {
				continue
			}
			return err
		}
		a.connSeen()
		a.emitRemoteRaw(frame)
	}
}
