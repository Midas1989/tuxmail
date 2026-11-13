// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"mime"
	"net/http"
	"strings"
)

// inlineTypes are the only media types served inline from the API origin.
// Everything else (text/html, image/svg+xml, javascript, ...) is a download.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/avif": true,
	"application/pdf": true,
	"audio/ogg":       true, "audio/webm": true, "audio/wav": true, "audio/x-wav": true, "audio/mpeg": true,
	"audio/mp4": true, "audio/aac": true, "audio/flac": true, "audio/opus": true,
	"video/mp4": true, "video/webm": true, "video/ogg": true, "video/quicktime": true,
}

// BlobContentType returns the Content-Type to send for a stored attachment
// type and whether it may be shown inline. Anything that is not exactly
// type/subtype (e.g. "image/png,text/html") or not on the allowlist becomes
// application/octet-stream and is never inline.
func BlobContentType(ct string) (string, bool) {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || strings.Count(mt, "/") != 1 || !inlineTypes[mt] {
		return "application/octet-stream", false
	}
	return mt, true
}

// hardenBlobResult makes the desktop read cache answer attachment and cid
// routes with the protections of the server (CSP, nosniff, inline allowlist);
// cached content types and dispositions are not trusted.
func hardenBlobResult(path string, res *connResult) {
	if res == nil || res.status != http.StatusOK || !reBlobRoute.MatchString(path) {
		return
	}
	ct, inline := BlobContentType(res.header.Get("Content-Type"))
	res.header.Set("Content-Type", ct)
	disp, params, err := mime.ParseMediaType(res.header.Get("Content-Disposition"))
	if err != nil || !inline || disp != "inline" {
		disp = "attachment"
	}
	res.header.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": params["filename"]}))
	res.header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	res.header.Set("X-Content-Type-Options", "nosniff")
}
