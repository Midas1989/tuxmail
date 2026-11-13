// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"net/http"
	"strings"
	"testing"
)

// The desktop read cache answers attachments with CSP, nosniff and the inline
// allowlist; cached content types are not trusted (L-15).
func TestHardenBlobResult(t *testing.T) {
	for ct, wantInline := range map[string]bool{
		"image/png": true, "image/png,text/html": false, "image/svg+xml": false, "text/javascript": false, "text/html": false,
	} {
		res := &connResult{status: 200, header: http.Header{"Content-Type": {ct}, "Content-Disposition": {`inline; filename="a.bin"`}}}
		hardenBlobResult("/api/messages/5/attachments/0", res)
		if got := strings.HasPrefix(res.header.Get("Content-Disposition"), "inline"); got != wantInline {
			t.Errorf("%q: disposition %q", ct, res.header.Get("Content-Disposition"))
		}
		if !wantInline && res.header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("%q: type %q", ct, res.header.Get("Content-Type"))
		}
		if res.header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(res.header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%q: headers %v", ct, res.header)
		}
	}
	res := &connResult{status: 200, header: http.Header{"Content-Type": {"application/json"}}}
	hardenBlobResult("/api/messages/5", res)
	if res.header.Get("Content-Security-Policy") != "" {
		t.Error("JSON route changed")
	}
}
