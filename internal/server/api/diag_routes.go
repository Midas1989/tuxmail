// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"mime"
	"net/http"
	"strconv"
)

// diagRoutes are the sync status and diagnostics endpoints (http.ts
// section Diagnostics).
func diagRoutes() []Route {
	return []Route{
		{"GET /api/sync/status", func(w w, r r, a A) { send(w)(a.SyncStatus(r.Context())) }},
		{"POST /api/sync/{id}/now", withID(func(w w, r r, a A, id int64) { done(w, a.SyncNow(r.Context(), id)) })},
		{"GET /api/diagnostics", func(w w, r r, a A) { send(w)(a.Diagnostics(r.Context())) }},
		// Raw source of a mail (mail window "Quelltext"), never rendered as HTML.
		{"GET /api/messages/{id}/source", withID(func(w w, r r, a A, id int64) {
			src, err := a.MessageSource(r.Context(), id)
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
			_, _ = w.Write([]byte(src))
		})},
		{"GET /api/diagnostics/export", func(w w, r r, a A) {
			z, err := a.ExportDiagnostics(r.Context())
			if err != nil {
				writeErr(w, err)
				return
			}
			w.Header().Set("Content-Type", z.MIMEType)
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": z.Filename}))
			w.Header().Set("Content-Length", strconv.Itoa(len(z.Data)))
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(z.Data)
		}},
	}
}
