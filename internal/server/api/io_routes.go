// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/Midas1989/Mailclient/internal/app"
)

// ioRoutes serves import/export and backup/restore (http.ts section
// "Import/Export/Backup").
func ioRoutes() []Route {
	return []Route{
		{"GET /api/io/formats", func(w w, r r, a A) { send(w)(a.ListIOFormats(r.Context())) }},
		{"POST /api/io/uploads", func(w w, r r, a A) {
			name, _ := url.QueryUnescape(r.Header.Get("X-Filename"))
			if name == "" {
				name = "upload"
			}
			body := http.MaxBytesReader(w, r.Body, app.MaxIOUpload)
			up, err := a.SaveIOUpload(r.Context(), name, body)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, up)
		}},
		{"GET /api/io/downloads/{token}", func(w w, r r, a A) {
			d, err := a.OpenIODownload(r.Context(), r.PathValue("token"))
			if err != nil {
				writeErr(w, err)
				return
			}
			f, err := os.Open(d.Path)
			if err != nil {
				writeErr(w, err)
				return
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", d.MIMEType)
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": d.Filename}))
			w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
			w.Header().Set("Cache-Control", "no-store")
			http.ServeContent(w, r, d.Filename, st.ModTime(), f)
		}},
		{"POST /api/io/inspect", jsonHandler(func(w w, r r, a A, in app.IOSource) { send(w)(a.InspectImport(r.Context(), in)) })},
		{"POST /api/io/import", jsonHandler(func(w w, r r, a A, in app.ImportRequest) {
			job, err := a.StartImport(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, job)
		})},
		{"POST /api/io/export", jsonHandler(func(w w, r r, a A, in app.ExportRequest) {
			job, err := a.StartExport(r.Context(), in)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, job)
		})},
		{"GET /api/io/jobs", func(w w, r r, a A) { send(w)(a.ListIOJobs(r.Context())) }},
		{"GET /api/io/jobs/{id}", func(w w, r r, a A) { send(w)(a.GetIOJob(r.Context(), r.PathValue("id"))) }},
		{"DELETE /api/io/jobs/{id}", func(w w, r r, a A) { done(w, a.CancelIOJob(r.Context(), r.PathValue("id"))) }},

		// An archive with the account secrets, encrypted with a passphrase of the
		// caller's choice, is the secrets in clear for whoever asks: re-authentication.
		{"POST /api/backup", jsonHandler(func(w w, r r, a A, in struct {
			app.BackupRequest
			Reauth Reauth `json:"reauth"`
		}) {
			if in.Scope.Secrets && !needReauth(w, r, in.Reauth) {
				return
			}
			job, err := a.CreateBackup(r.Context(), in.BackupRequest)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, job)
		})},
		{"POST /api/backup/inspect", jsonHandler(func(w w, r r, a A, in struct {
			Source     app.BackupSource `json:"source"`
			Passphrase string           `json:"passphrase"`
		}) {
			send(w)(a.InspectBackup(r.Context(), in.Source, in.Passphrase))
		})},
		{"POST /api/backup/restore", jsonHandler(func(w w, r r, a A, in struct {
			app.RestoreRequest
			Reauth Reauth `json:"reauth"`
		}) {
			if in.Secrets && !needReauth(w, r, in.Reauth) { // replaces stored credentials
				return
			}
			job, err := a.StartRestore(r.Context(), in.RestoreRequest)
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, job)
		})},
		{"GET /api/backup/schedules", func(w w, r r, a A) { send(w)(a.ListBackupSchedules(r.Context())) }},
		{"POST /api/backup/schedules", jsonHandler(func(w w, r r, a A, in struct {
			app.BackupScheduleInput
			Reauth Reauth `json:"reauth"`
		}) {
			if in.Scope.Secrets && !needReauth(w, r, in.Reauth) {
				return
			}
			in.ID = ""
			send(w)(a.SaveBackupSchedule(r.Context(), in.BackupScheduleInput))
		})},
		{"PUT /api/backup/schedules/{id}", jsonHandler(func(w w, r r, a A, in struct {
			app.BackupScheduleInput
			Reauth Reauth `json:"reauth"`
		}) {
			if in.Scope.Secrets && !needReauth(w, r, in.Reauth) {
				return
			}
			in.ID = r.PathValue("id")
			send(w)(a.SaveBackupSchedule(r.Context(), in.BackupScheduleInput))
		})},
		{"DELETE /api/backup/schedules/{id}", func(w w, r r, a A) { done(w, a.DeleteBackupSchedule(r.Context(), r.PathValue("id"))) }},
		{"POST /api/backup/schedules/{id}/run", func(w w, r r, a A) {
			job, err := a.RunBackupSchedule(r.Context(), r.PathValue("id"))
			if err != nil {
				writeErr(w, err)
				return
			}
			writeJSON(w, http.StatusAccepted, job)
		}},
		{"GET /api/backup/schedules/{id}/archives", func(w w, r r, a A) {
			send(w)(a.ListBackupArchives(r.Context(), r.PathValue("id")))
		}},
	}
}
