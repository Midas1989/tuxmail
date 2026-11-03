// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/websocket"

	"github.com/Midas1989/Mailclient/internal/app"
)

// eventsHandler streams the user's ApiEvents over a WebSocket (one JSON
// object per text frame). Authentication happened in the wrap function
// (session cookie or Bearer device token); browsers additionally must send
// an allowed Origin (cross-site WebSocket hijacking protection).
func eventsHandler(opts Options) AppHandler {
	return func(w http.ResponseWriter, r *http.Request, a *app.App) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: "websocket_required", Message: "use a WebSocket connection"})
			return
		}
		srv := websocket.Server{
			Handshake: func(cfg *websocket.Config, req *http.Request) error {
				// Subprotocols: "tuxmail.v1" (+ "tuxmail.bearer.<token>",
				// consumed by authentication). Answer with exactly one.
				offered := cfg.Protocol
				cfg.Protocol = nil
				for _, p := range offered {
					if p == "tuxmail.v1" {
						cfg.Protocol = []string{p}
					}
				}
				origin := req.Header.Get("Origin")
				if origin == "" {
					return nil // native clients (Bearer token); no ambient credentials
				}
				if opts.AllowOrigin != nil {
					if opts.AllowOrigin(req, origin) {
						return nil
					}
				} else if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, req.Host) {
					return nil
				}
				return websocket.ErrBadWebSocketOrigin
			},
			Handler: func(ws *websocket.Conn) { streamEvents(r.Context(), ws, a) },
		}
		srv.ServeHTTP(w, r)
	}
}

const writeTimeout = 10 * time.Second

func streamEvents(parent context.Context, ws *websocket.Conn, a *app.App) {
	defer ws.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	events, unsubscribe := a.Events().Subscribe(ctx, 256)
	defer unsubscribe()
	// Reader: the client sends nothing meaningful; a read error means the
	// connection is gone.
	go func() {
		defer cancel()
		buf := make([]byte, 512)
		for {
			if _, err := ws.Read(buf); err != nil {
				if err != io.EOF {
					return
				}
				return
			}
		}
	}()
	send := func(v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return true
		}
		_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
		return websocket.Message.Send(ws, string(b)) == nil
	}
	if !send(app.Event{Type: app.EventConnection, State: "online"}) {
		return
	}
	keepalive := time.NewTicker(45 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok || !send(ev) {
				return
			}
		case <-keepalive.C:
			// A connection event doubles as keep-alive through proxies.
			if !send(app.Event{Type: app.EventConnection, State: "online"}) {
				return
			}
		}
	}
}
