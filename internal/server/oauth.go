// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"strings"
	"sync"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/config"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/oauth"
	"github.com/Midas1989/Mailclient/internal/providers/graph"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
)

// OAuth sign-in of mail accounts in server mode: the admin client
// registrations (config oauth:, environment as fallback), the web redirect
// /api/oauth/{provider}/callback (served by internal/server/api on the
// user's App) and the public Microsoft Graph change-notification endpoint.

// GraphNotificationsPath receives Graph change notifications.
const GraphNotificationsPath = "/api/graph/notifications"

// Graph posts notifications without browser credentials; they are matched
// by subscription id and clientState instead.
func init() { csrfExempt[GraphNotificationsPath] = true }

var (
	graphNotifierOnce sync.Once
	graphNotifier     *graph.Notifier
)

// oauthAppOptions returns the OAuth options of the per-user Apps.
func (s *Server) oauthAppOptions() *app.OAuthOptions {
	clients := oauth.ClientsFromEnv(nil)
	o := s.cfg.OAuth
	for provider, c := range map[string]config.OAuthClient{oauth.Microsoft: o.Microsoft, oauth.Google: o.Google} {
		if c.ClientID == "" {
			continue
		}
		sec, err := c.Secret()
		if err != nil {
			s.log.Error("oauth: reading client secret failed", "provider", provider, "err", err)
		}
		clients[provider] = oauth.Client{ID: c.ClientID, Secret: sec, Tenant: c.Tenant, Source: "admin"}
	}
	return &app.OAuthOptions{Clients: clients, DenyUserClients: !o.UserClientsAllowed(), RedirectBase: strings.TrimRight(s.cfg.PublicURL, "/")}
}

// mountOAuth registers the Graph notification endpoint (when enabled).
func (s *Server) mountOAuth(mux *http.ServeMux) {
	if !s.cfg.OAuth.GraphNotifications || !strings.HasPrefix(s.cfg.PublicURL, "https://") {
		return
	}
	graphNotifierOnce.Do(func() {
		graphNotifier = graph.NewNotifier(strings.TrimRight(s.cfg.PublicURL, "/") + GraphNotificationsPath)
		tsync.GraphNotifications = func(domain.Account) *graph.Notifier { return graphNotifier }
	})
	mux.Handle("POST "+GraphNotificationsPath, graphNotifier)
}
