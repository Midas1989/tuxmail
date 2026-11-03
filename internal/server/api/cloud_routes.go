// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/Midas1989/Mailclient/internal/app"
)

// cloudRoutes are the cloud files endpoints (KONZEPT 12, http.ts section Cloud).
func cloudRoutes() []Route {
	return []Route{
		{"GET /api/cloud/accounts", func(w w, r r, a A) { send(w)(a.ListCloudAccounts(r.Context())) }},
		{"DELETE /api/cloud/accounts/{id}", withID(func(w w, r r, a A, id int64) { done(w, a.DeleteCloudAccount(r.Context(), id)) })},
		{"POST /api/cloud/login", jsonHandler(func(w w, r r, a A, in app.CloudLoginInput) {
			send(w)(a.StartCloudLogin(r.Context(), in))
		})},
		{"GET /api/cloud/login/{flowId}", func(w w, r r, a A) { send(w)(a.PollCloudLogin(r.Context(), r.PathValue("flowId"))) }},
		{"DELETE /api/cloud/login/{flowId}", func(w w, r r, a A) { done(w, a.CancelCloudLogin(r.Context(), r.PathValue("flowId"))) }},
		{"GET /api/cloud/oauth/callback", cloudOAuthCallback},
		{"GET /api/cloud/accounts/{id}/files", withID(func(w w, r r, a A, id int64) {
			send(w)(a.ListCloudFiles(r.Context(), id, r.URL.Query().Get("path")))
		})},
		{"DELETE /api/cloud/accounts/{id}/files", withID(func(w w, r r, a A, id int64) {
			done(w, a.DeleteCloudFile(r.Context(), id, r.URL.Query().Get("path")))
		})},
		{"GET /api/cloud/accounts/{id}/quota", withID(func(w w, r r, a A, id int64) { send(w)(a.CloudQuota(r.Context(), id)) })},
		{"GET /api/cloud/accounts/{id}/download", withID(serveCloudFile)},
		{"POST /api/cloud/accounts/{id}/folders", withID(func(w w, r r, a A, id int64) {
			var in struct{ Path string }
			if readJSON(w, r, &in) {
				send(w)(a.CreateCloudFolder(r.Context(), id, in.Path))
			}
		})},
		{"POST /api/cloud/accounts/{id}/move", withID(func(w w, r r, a A, id int64) {
			var in struct{ From, To string }
			if readJSON(w, r, &in) {
				done(w, a.MoveCloudFile(r.Context(), id, in.From, in.To))
			}
		})},
		{"POST /api/cloud/accounts/{id}/upload", withID(func(w w, r r, a A, id int64) {
			var in struct {
				Dir         string `json:"dir"`
				UploadToken string `json:"uploadToken"`
			}
			if readJSON(w, r, &in) {
				send(w)(a.UploadCloudFile(r.Context(), id, in.Dir, in.UploadToken))
			}
		})},
		{"POST /api/cloud/accounts/{id}/import", withID(func(w w, r r, a A, id int64) {
			var in struct{ Path string }
			if readJSON(w, r, &in) {
				send(w)(a.ImportCloudFile(r.Context(), id, in.Path))
			}
		})},
		{"GET /api/cloud/accounts/{id}/password", withID(func(w w, r r, a A, id int64) { send(w)(a.GenerateCloudPassword(r.Context(), id)) })},
		{"POST /api/cloud/save", jsonHandler(func(w w, r r, a A, in app.CloudSaveInput) { send(w)(a.SaveAttachmentToCloud(r.Context(), in)) })},
		{"GET /api/cloud/shares", func(w w, r r, a A) { send(w)(a.ListCloudShares(r.Context())) }},
		{"POST /api/cloud/shares", jsonHandler(func(w w, r r, a A, in app.CloudShareInput) {
			sh, err := a.CreateCloudShare(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, sh)
		})},
		{"DELETE /api/cloud/shares/{id}", withID(func(w w, r r, a A, id int64) { send(w)(a.RevokeCloudShare(r.Context(), id)) })},
		{"GET /api/cloud/shares/{id}/password", withID(func(w w, r r, a A, id int64) { send(w)(a.CloudSharePassword(r.Context(), id)) })},
	}
}

// serveCloudFile streams a cloud file (attachment disposition: content of
// remote servers is never rendered from the API origin).
func serveCloudFile(w http.ResponseWriter, r *http.Request, a *app.App, id int64) {
	rc, e, err := a.OpenCloudFile(r.Context(), id, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": e.Name}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if e.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
	}
	_, _ = io.Copy(w, rc)
}

// cloudOAuthCallback completes an OpenCloud login in server mode and shows
// a small page (the login tab can be closed; the app polls the flow).
func cloudOAuthCallback(w http.ResponseWriter, r *http.Request, a *app.App) {
	q := r.URL.Query()
	f, err := a.CompleteCloudOAuth(r.Context(), q.Get("state"), q.Get("code"), q.Get("error"))
	msg := "TuxMail is now connected to your cloud. You can close this window."
	status := http.StatusOK
	switch {
	case err != nil:
		msg, status = "Login failed: "+err.Error(), app.AsError(err).Status
	case f.Status == app.CloudFlowError:
		msg = "Login failed: " + f.Error
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>TuxMail</title><p style=\"font:16px sans-serif;margin:3em\">%s</p>", html.EscapeString(msg))
}
