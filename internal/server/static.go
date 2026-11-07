// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// placeholderIndex is served until the web frontend (frontend/dist) is embedded.
const placeholderIndex = `<!doctype html>
<html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>TuxMail</title></head>
<body style="font-family:system-ui,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>TuxMail</h1>
<p>Der Server läuft. Die Weboberfläche ist in diesem Build noch nicht enthalten.</p>
<p>The server is running. The web frontend is not included in this build yet.</p>
</body></html>
`

// staticHandler serves the SPA: existing files directly, every other non-API
// path falls back to index.html so client-side routes work on reload.
func (s *Server) staticHandler() http.Handler {
	fsys := s.static
	if fsys != nil {
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			fsys = nil
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if fsys == nil {
			serveIndex(w, r, []byte(placeholderIndex))
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasSuffix(name, ".html") { // offline.html: needs the nonces of this response
					if page, err := fs.ReadFile(fsys, name); err == nil {
						serveIndex(w, r, injectNonces(page, noncesFrom(r)))
						return
					}
				}
				serveFile(w, r, fsys, name, st.ModTime())
				return
			}
			// Missing asset files are real 404s; only routes fall back.
			if path.Ext(name) != "" && !strings.HasSuffix(name, ".html") {
				http.NotFound(w, r)
				return
			}
		}
		idx, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			serveIndex(w, r, []byte(placeholderIndex))
			return
		}
		// The web client needs a session: browsers without one log in first.
		if !s.loggedIn(r) {
			loginRedirect(w, r)
			return
		}
		tok, _ := r.Context().Value(ctxCSRF).(string)
		serveIndex(w, r, injectMeta(idx, tok, noncesFrom(r)))
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, b []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(b))
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string, mod time.Time) {
	f, err := fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(b)
	}
	switch {
	case strings.HasPrefix(name, "assets/"): // content-hashed by the bundler
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case name == "sw.js" || name == "service-worker.js" || strings.HasSuffix(name, ".webmanifest"):
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, mod, rs)
}
