// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/store"
)

// oauthRoutes are the OAuth sign-in and cloud account endpoints (http.ts,
// section "OAuth/Cloud accounts").
func oauthRoutes() []Route {
	return []Route{
		{"GET /api/oauth/providers", func(w w, r r, a A) { send(w)(a.OAuthProviders(r.Context())) }},
		{"PUT /api/oauth/clients/{provider}", func(w w, r r, a A) {
			var in app.OAuthClientInput
			if readJSON(w, r, &in) {
				in.Provider = r.PathValue("provider")
				done(w, a.SaveOAuthClient(r.Context(), in))
			}
		}},
		{"DELETE /api/oauth/clients/{provider}", func(w w, r r, a A) {
			done(w, a.DeleteOAuthClient(r.Context(), r.PathValue("provider")))
		}},
		{"POST /api/oauth/start", jsonHandler(func(w w, r r, a A, in app.OAuthStartInput) { send(w)(a.StartOAuth(r.Context(), in)) })},
		{"GET /api/oauth/flows/{flowId}/status", func(w w, r r, a A) { send(w)(a.OAuthFlow(r.Context(), r.PathValue("flowId"))) }},
		{"DELETE /api/oauth/flows/{flowId}", func(w w, r r, a A) { done(w, a.CancelOAuth(r.Context(), r.PathValue("flowId"))) }},
		// Browser redirect target of the web flow (session cookie, SameSite=Lax).
		{"GET /api/oauth/{provider}/callback", func(w w, r r, a A) {
			page, err := a.OAuthCallback(r.Context(), r.URL.Query())
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
			}
			_, _ = w.Write([]byte(page))
		}},
		{"POST /api/accounts/{id}/shared-mailboxes", withID(func(w w, r r, a A, id int64) {
			var in app.SharedMailboxInput
			if readJSON(w, r, &in) {
				in.AccountID = id
				acc, err := a.AddSharedMailbox(r.Context(), in)
				if err == nil {
					writeJSON(w, http.StatusCreated, acc)
					return
				}
				writeErr(w, err)
			}
		})},
		{"POST /api/accounts/{id}/pim-sync", withID(func(w w, r r, a A, id int64) { send(w)(a.SyncCloudPIM(r.Context(), id)) })},
		{"GET /api/identities/delegations", func(w w, r r, a A) { send(w)(a.ListIdentityDelegations(r.Context())) }},
		{"PUT /api/identities/{id}/delegation", withID(func(w w, r r, a A, id int64) {
			var d store.IdentityDelegation
			if readJSON(w, r, &d) {
				d.IdentityID = id
				done(w, a.SetIdentityDelegation(r.Context(), d))
			}
		})},
	}
}
