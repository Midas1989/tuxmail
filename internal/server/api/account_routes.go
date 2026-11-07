// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/Midas1989/Mailclient/internal/app"
)

// accountEditRoutes are the endpoints of the account edit dialog (http.ts
// section "Account edit"; internal/app/account_edit.go).
func accountEditRoutes() []Route {
	return []Route{
		{"GET /api/accounts/{id}/settings", withID(func(w w, r r, a A, id int64) { send(w)(a.GetAccountSettings(r.Context(), id)) })},
		{"PATCH /api/accounts/{id}", withID(func(w w, r r, a A, id int64) {
			var in app.AccountUpdate
			if readJSON(w, r, &in) {
				send(w)(a.UpdateAccount(r.Context(), id, in))
			}
		})},
		{"DELETE /api/accounts/{id}", withID(func(w w, r r, a A, id int64) {
			keep := r.URL.Query().Get("keepData")
			done(w, a.DeleteAccount(r.Context(), id, app.DeleteAccountOptions{KeepData: keep == "1" || keep == "true"}))
		})},
		{"PUT /api/accounts/{id}/password", withID(func(w w, r r, a A, id int64) {
			var in struct {
				Password string `json:"password"`
				Verify   bool   `json:"verify"`
			}
			if readJSON(w, r, &in) {
				send(w)(a.ChangeAccountPassword(r.Context(), id, in.Password, in.Verify))
			}
		})},
		{"POST /api/accounts/{id}/test", withID(func(w w, r r, a A, id int64) {
			var in app.ConnectionTestInput
			if readJSON(w, r, &in) {
				send(w)(a.TestAccountConnection(r.Context(), id, in))
			}
		})},
		{"POST /api/accounts/{id}/sync", withID(func(w w, r r, a A, id int64) { done(w, a.SyncAccountNow(r.Context(), id)) })},
		{"GET /api/accounts/{id}/diagnostics", withID(func(w w, r r, a A, id int64) {
			d, err := a.AccountDiagnostics(r.Context(), id)
			if err == nil {
				w.Header().Set("Content-Disposition", `attachment; filename="tuxmail-account-diagnostics.json"`)
			}
			reply(w, d, err)
		})},
		{"POST /api/identities", jsonHandler(func(w w, r r, a A, in app.Identity) {
			in.ID = 0
			id, err := a.CreateIdentity(r.Context(), in)
			if err == nil {
				writeJSON(w, http.StatusCreated, id)
				return
			}
			writeErr(w, err)
		})},
		{"DELETE /api/identities/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteIdentity(r.Context(), id)) })},
		{"POST /api/identities/{id}/default", withID(func(w w, r r, a A, id int64) { done(w, a.SetDefaultIdentity(r.Context(), id)) })},
	}
}
