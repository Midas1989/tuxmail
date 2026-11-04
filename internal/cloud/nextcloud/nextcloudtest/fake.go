// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nextcloudtest is a fake Nextcloud server for tests: Login Flow
// v2, OCS user/capabilities/shares, WebDAV files (golang.org/x/net/webdav
// on an in-memory file system) and chunked upload v2.
package nextcloudtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/webdav"
)

// Share is a created public link.
type Share struct {
	ID          int
	Path        string
	Password    string
	ExpireDate  string
	Permissions string
	Note        string
	Revoked     bool
}

// Server is the fake. Credentials: LoginName / AppPassword.
type Server struct {
	*httptest.Server
	LoginName   string
	UserID      string
	AppPassword string
	FS          webdav.FileSystem

	mu sync.Mutex
	// LoginDone makes the poll endpoint return the credentials.
	LoginDone bool
	Shares    []Share
	// Chunks counts uploaded chunks of chunked uploads.
	Chunks  int
	uploads map[string]map[string][]byte
	dav     *webdav.Handler
}

// New starts a fake server.
func New() *Server {
	s := &Server{LoginName: "anna@example.de", UserID: "anna", AppPassword: "app-pass-123", FS: webdav.NewMemFS(),
		uploads: map[string]map[string][]byte{}}
	s.dav = &webdav.Handler{Prefix: "/remote.php/dav/files/" + s.UserID, FileSystem: s.FS, LockSystem: webdav.NewMemLS()}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// SetLoginDone marks the browser login as finished.
func (s *Server) SetLoginDone() { s.mu.Lock(); s.LoginDone = true; s.mu.Unlock() }

// ShareList returns a copy of the shares.
func (s *Server) ShareList() []Share {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Share(nil), s.Shares...)
}

// ReadFile returns a stored file.
func (s *Server) ReadFile(p string) ([]byte, error) {
	f, err := s.FS.OpenFile(context.Background(), p, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func ocs(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	code := 200
	if status != 200 {
		code = status
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ocs": map[string]any{
		"meta": map[string]any{"status": map[bool]string{true: "ok", false: "failure"}[status == 200], "statuscode": code, "message": ""},
		"data": data}})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/index.php/login/v2" && r.Method == http.MethodPost:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"poll":  map[string]string{"token": "poll-token", "endpoint": s.URL + "/index.php/login/v2/poll"},
			"login": s.URL + "/index.php/login/v2/flow/abc",
		})
		return
	case p == "/index.php/login/v2/poll":
		_ = r.ParseForm()
		s.mu.Lock()
		done := s.LoginDone
		s.mu.Unlock()
		if r.PostForm.Get("token") != "poll-token" || !done {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"server": s.URL, "loginName": s.LoginName, "appPassword": s.AppPassword})
		return
	}
	if u, pw, ok := r.BasicAuth(); !ok || u != s.LoginName || pw != s.AppPassword {
		if strings.HasPrefix(p, "/ocs/") {
			ocs(w, http.StatusUnauthorized, nil)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case strings.HasPrefix(p, "/ocs/") && r.Header.Get("OCS-APIRequest") != "true":
		w.WriteHeader(http.StatusPreconditionFailed)
	case p == "/ocs/v2.php/cloud/user":
		ocs(w, 200, map[string]any{"id": s.UserID, "display-name": "Anna Example", "email": "anna@example.de",
			"quota": map[string]any{"used": 1024, "total": 1 << 30}})
	case p == "/ocs/v2.php/cloud/capabilities":
		ocs(w, 200, map[string]any{"capabilities": map[string]any{
			"password_policy": map[string]any{"minLength": 20, "enforceUpperLowerCase": true, "enforceNumericCharacters": true,
				"enforceSpecialCharacters": true},
			"files_sharing": map[string]any{"public": map[string]any{"enabled": true, "password": map[string]any{"enforced": true},
				"expire_date": map[string]any{"enabled": true, "days": 14}}},
		}})
	case p == "/ocs/v2.php/apps/files_sharing/api/v1/shares" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		f := r.PostForm
		if f.Get("shareType") != "3" {
			ocs(w, http.StatusBadRequest, nil)
			return
		}
		if _, err := s.FS.Stat(r.Context(), f.Get("path")); err != nil {
			ocs(w, http.StatusNotFound, nil)
			return
		}
		if len(f.Get("password")) < 20 {
			ocs(w, http.StatusBadRequest, nil) // password policy
			return
		}
		s.mu.Lock()
		sh := Share{ID: 100 + len(s.Shares), Path: f.Get("path"), Password: f.Get("password"), ExpireDate: f.Get("expireDate"),
			Permissions: f.Get("permissions"), Note: f.Get("note")}
		s.Shares = append(s.Shares, sh)
		s.mu.Unlock()
		exp := ""
		if sh.ExpireDate != "" {
			exp = sh.ExpireDate + " 00:00:00"
		}
		ocs(w, 200, map[string]any{"id": strconv.Itoa(sh.ID), "url": fmt.Sprintf("%s/s/Tok%d", s.URL, sh.ID), "token": fmt.Sprintf("Tok%d", sh.ID),
			"expiration": exp, "path": sh.Path})
	case strings.HasPrefix(p, "/ocs/v2.php/apps/files_sharing/api/v1/shares/") && r.Method == http.MethodDelete:
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/ocs/v2.php/apps/files_sharing/api/v1/shares/"))
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := range s.Shares {
			if s.Shares[i].ID == id && !s.Shares[i].Revoked {
				s.Shares[i].Revoked = true
				ocs(w, 200, []any{})
				return
			}
		}
		ocs(w, http.StatusNotFound, nil)
	case strings.HasPrefix(p, "/remote.php/dav/uploads/"+s.UserID+"/"):
		s.chunked(w, r)
	case strings.HasPrefix(p, "/remote.php/dav/files/"+s.UserID+"/") || p == "/remote.php/dav/files/"+s.UserID:
		s.dav.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) chunked(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/uploads/"+s.UserID+"/")
	dir, chunk, _ := strings.Cut(rest, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("Destination") == "" && r.Method != http.MethodDelete {
		http.Error(w, "missing Destination", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case "MKCOL":
		s.uploads[dir] = map[string][]byte{}
		w.WriteHeader(http.StatusCreated)
	case http.MethodPut:
		m, ok := s.uploads[dir]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		m[chunk] = b
		s.Chunks++
		w.WriteHeader(http.StatusCreated)
	case "MOVE":
		m, ok := s.uploads[dir]
		if !ok || chunk != ".file" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		for _, k := range keys {
			buf.Write(m[k])
		}
		if tl := r.Header.Get("OC-Total-Length"); tl != "" && tl != strconv.Itoa(buf.Len()) {
			http.Error(w, "length mismatch", http.StatusBadRequest)
			return
		}
		du, _ := url.Parse(r.Header.Get("Destination"))
		target := strings.TrimPrefix(du.Path, "/remote.php/dav/files/"+s.UserID)
		f, err := s.FS.OpenFile(r.Context(), target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_, _ = f.Write(buf.Bytes())
		f.Close()
		delete(s.uploads, dir)
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		delete(s.uploads, dir)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
