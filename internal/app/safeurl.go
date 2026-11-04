// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"net/url"
	"strings"
)

// linkURL returns u if it is safe as a clickable link in the app window
// (http, https, mailto, tel, sip/sips), otherwise "". URLs from received
// invitations, subscribed calendars and vCards are attacker-controlled; a
// "javascript:" or "file:" link rendered as <a href> in the main window
// would run in (or open from) the app's origin (docs/security/AUDIT-B-content.md).
func linkURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" || strings.ContainsAny(u, "\x00\r\n\t") {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	switch strings.ToLower(p.Scheme) {
	case "https", "http":
		if p.Host == "" {
			return ""
		}
		return u
	case "mailto", "tel", "sip", "sips":
		return u
	}
	return ""
}

// isDataImage reports whether u is an inline raster image (data:image/...,
// not SVG).
func isDataImage(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "data:image/") && !strings.HasPrefix(l, "data:image/svg")
}
