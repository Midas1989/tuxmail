// SPDX-License-Identifier: AGPL-3.0-or-later

// Package web embeds the built frontend (frontend/dist) for the server and
// desktop binaries. Build it first:
//
//	go generate ./web     (runs npm ci/npm run build in frontend/ and copies dist/ here)
//
// Without a build only dist/.keep is embedded and the server shows a
// placeholder page.
package web

import (
	"embed"
	"io/fs"
)

//go:generate sh ./build.sh

//go:embed all:dist
var dist embed.FS

// FS returns the frontend files (index.html at the root), or nil when the
// frontend was not built into this binary.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
