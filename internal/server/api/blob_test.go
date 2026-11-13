// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Midas1989/Mailclient/internal/app"
)

// Only an exact allowlist is served inline (L-7, I-7).
func TestServeBlobInlineAllowlist(t *testing.T) {
	for ct, wantInline := range map[string]bool{
		"image/png":           true,
		"image/png; x=y":      true,
		"application/pdf":     true,
		"audio/ogg":           true,
		"image/png,text/html": false,
		"image/svg+xml":       false,
		"text/javascript":     false,
		"text/html":           false,
		"image/x-evil":        false,
		"":                    false,
	} {
		rec := httptest.NewRecorder()
		serveBlob(rec, app.AttachmentContent{Filename: "f", MIMEType: ct, Data: []byte("x")}, true)
		inline := strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline")
		if inline != wantInline {
			t.Errorf("%q: disposition %q", ct, rec.Header().Get("Content-Disposition"))
		}
		if !wantInline && rec.Header().Get("Content-Type") != "application/octet-stream" {
			t.Errorf("%q: Content-Type %q", ct, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%q: no nosniff", ct)
		}
	}
}
