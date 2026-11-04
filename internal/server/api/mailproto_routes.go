// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/workflow/vacation"
)

// mailProtoRoutes are the remote content and Sieve endpoints (http.ts
// section "Mail protocols").
func mailProtoRoutes() []Route {
	type allow struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	return []Route{
		{"GET /api/messages/{id}/remote-content", withID(func(w w, r r, a A, id int64) { send(w)(a.LoadRemoteContent(r.Context(), id)) })},
		{"GET /api/remote-content/allow", func(w w, r r, a A) { send(w)(a.ListRemoteAllow(r.Context())) }},
		{"POST /api/remote-content/allow", jsonHandler(func(w w, r r, a A, in allow) {
			done(w, a.AllowRemoteContent(r.Context(), in.Kind, in.Value))
		})},
		{"POST /api/remote-content/allow/remove", jsonHandler(func(w w, r r, a A, in allow) {
			done(w, a.RemoveRemoteAllow(r.Context(), in.Kind, in.Value))
		})},
		{"GET /api/accounts/{id}/sieve", withID(func(w w, r r, a A, id int64) { send(w)(a.GetSieveStatus(r.Context(), id)) })},
		{"PUT /api/accounts/{id}/sieve/workflows", withID(func(w w, r r, a A, id int64) {
			var in struct{ Enabled bool }
			if readJSON(w, r, &in) {
				send(w)(a.SetSieveWorkflowSync(r.Context(), id, in.Enabled))
			}
		})},
		{"POST /api/accounts/{id}/sieve/check", withID(func(w w, r r, a A, id int64) {
			var in struct{ Script string }
			if readJSON(w, r, &in) {
				send(w)(a.CheckSieveScript(r.Context(), id, in.Script))
			}
		})},
		{"POST /api/accounts/{id}/sieve/activate", withID(func(w w, r r, a A, id int64) {
			var in struct{ Name string }
			if readJSON(w, r, &in) {
				done(w, a.ActivateSieveScript(r.Context(), id, in.Name))
			}
		})},
		{"GET /api/accounts/{id}/sieve/scripts/{name}", withID(func(w w, r r, a A, id int64) {
			send(w)(a.GetSieveScript(r.Context(), id, r.PathValue("name")))
		})},
		{"PUT /api/accounts/{id}/sieve/scripts/{name}", withID(func(w w, r r, a A, id int64) {
			var in struct {
				Script   string `json:"script"`
				Activate bool   `json:"activate"`
			}
			if readJSON(w, r, &in) {
				done(w, a.PutSieveScript(r.Context(), id, r.PathValue("name"), in.Script, in.Activate))
			}
		})},
		{"GET /api/accounts/{id}/sieve/config", withID(func(w w, r r, a A, id int64) { send(w)(a.GetSieveConfig(r.Context(), id)) })},
		{"PUT /api/accounts/{id}/sieve/config", withID(func(w w, r r, a A, id int64) {
			var in app.SieveConfig
			if readJSON(w, r, &in) {
				send(w)(a.SetSieveConfig(r.Context(), id, in))
			}
		})},
		{"GET /api/vacation", func(w w, r r, a A) { send(w)(a.GetVacationSettings(r.Context())) }},
		{"PUT /api/vacation", jsonHandler(func(w w, r r, a A, in vacation.Settings) {
			send(w)(a.SaveVacationSettings(r.Context(), in))
		})},
		{"DELETE /api/accounts/{id}/sieve/scripts/{name}", withID(func(w w, r r, a A, id int64) {
			done(w, a.DeleteSieveScript(r.Context(), id, r.PathValue("name")))
		})},
	}
}
