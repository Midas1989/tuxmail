// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/Midas1989/Mailclient/internal/app"
)

// MessengerRoutes returns the messenger endpoints (http.ts, section
// "Messenger"): plugin opt-in and status, pairing, accounts, media.
func MessengerRoutes() []Route {
	return []Route{
		{"GET /api/messenger/plugins", func(w w, r r, a A) { send(w)(a.ListMessengerPlugins(r.Context())) }},
		{"PATCH /api/messenger/plugins/{name}", jsonHandler(func(w w, r r, a A, in app.MessengerPluginPatch) {
			send(w)(a.UpdateMessengerPlugin(r.Context(), r.PathValue("name"), in))
		})},
		{"POST /api/messenger/plugins/{name}/restart", func(w w, r r, a A) {
			done(w, a.RestartMessengerPlugin(r.Context(), r.PathValue("name")))
		}},
		{"POST /api/messenger/plugins/{name}/component", func(w w, r r, a A) {
			done(w, a.InstallMessengerComponent(r.Context(), r.PathValue("name")))
		}},
		{"POST /api/messenger/pairings", jsonHandler(func(w w, r r, a A, in app.MessengerPairingInput) {
			p, err := a.StartMessengerPairing(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, p)
		})},
		{"GET /api/messenger/pairings/{id}", func(w w, r r, a A) { send(w)(a.GetMessengerPairing(r.Context(), r.PathValue("id"))) }},
		{"POST /api/messenger/pairings/{id}/input", jsonHandler(func(w w, r r, a A, in struct{ Field, Value string }) {
			done(w, a.SubmitMessengerPairingInput(r.Context(), r.PathValue("id"), in.Field, in.Value))
		})},
		{"DELETE /api/messenger/pairings/{id}", func(w w, r r, a A) { done(w, a.CancelMessengerPairing(r.Context(), r.PathValue("id"))) }},
		{"GET /api/messenger/accounts", func(w w, r r, a A) { send(w)(a.ListMessengerAccounts(r.Context())) }},
		{"DELETE /api/messenger/accounts/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.RemoveMessengerAccount(r.Context(), id)) })},
		{"POST /api/messenger/accounts/{id}/sync", withID(func(w w, r r, a A, id int64) { done(w, a.SyncMessengerAccount(r.Context(), id)) })},
		{"POST /api/conversations/{id}/typing", withID(func(w w, r r, a A, id int64) {
			var in struct{ Active bool }
			if readJSON(w, r, &in) {
				done(w, a.SetMessengerTyping(r.Context(), id, in.Active))
			}
		})},
		{"GET /api/messenger/media/{id}", withID(func(w w, r r, a A, id int64) {
			c, err := a.MessengerMedia(r.Context(), id)
			if err != nil {
				writeErr(w, err)
				return
			}
			serveBlob(w, c, r.URL.Query().Get("download") == "")
		})},
	}
}
