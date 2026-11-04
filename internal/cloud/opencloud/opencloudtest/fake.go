// SPDX-License-Identifier: AGPL-3.0-or-later

// Package opencloudtest is a fake OpenCloud server for tests: OIDC
// discovery and token endpoint (authorization code with PKCE, refresh),
// LibreGraph drives/createLink/permissions, WebDAV per space with oc:fileid
// and TUS uploads.
package opencloudtest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/webdav"
)

// Personal and project space ids.
const (
	PersonalSpace = "storage-users-1$anna-uuid!anna-uuid"
	ProjectSpace  = "storage-users-1$proj-uuid!proj-uuid"
)

// Link is a created link permission.
type Link struct {
	Drive, Item, ID, Type, Password, Expiration string
	Revoked                                     bool
}

type tusUpload struct {
	space, path string
	length      int64
	data        []byte
}

// Server is the fake.
type Server struct {
	*httptest.Server
	ClientID string
	// Code is the authorization code the fake accepts.
	Code string

	mu        sync.Mutex
	challenge string
	access    string
	refreshes int
	Links     []Link
	TUSPatch  int
	// FailNextPatch makes the next TUS PATCH fail after storing the data
	// (tests resume).
	FailNextPatch bool
	tus           map[string]*tusUpload
	fs            map[string]webdav.FileSystem
	dav           map[string]*webdav.Handler
	idMu          sync.Mutex
	ids           map[string]string // item id -> space:path
}

// New starts the fake.
func New() *Server {
	s := &Server{ClientID: "OpenCloudDesktop", Code: "auth-code-1", tus: map[string]*tusUpload{}, fs: map[string]webdav.FileSystem{},
		dav: map[string]*webdav.Handler{}, ids: map[string]string{}, access: "access-1"}
	for _, sp := range []string{PersonalSpace, ProjectSpace} {
		fs := &idFS{FileSystem: webdav.NewMemFS(), space: sp, s: s}
		s.fs[sp] = fs
		s.dav[sp] = &webdav.Handler{Prefix: "/dav/spaces/" + sp, FileSystem: fs, LockSystem: webdav.NewMemLS()}
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Authorize records the PKCE challenge of an authorization URL
// (the browser step).
func (s *Server) Authorize(challenge string) { s.mu.Lock(); s.challenge = challenge; s.mu.Unlock() }

// ExpireAccess invalidates the current access token (forces a refresh).
func (s *Server) ExpireAccess() { s.mu.Lock(); s.access = "expired-" + s.access; s.mu.Unlock() }

// Refreshes returns the number of refresh grants.
func (s *Server) Refreshes() int { s.mu.Lock(); defer s.mu.Unlock(); return s.refreshes }

// LinkList returns the links.
func (s *Server) LinkList() []Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Link(nil), s.Links...)
}

// ReadFile returns a file of a space.
func (s *Server) ReadFile(space, p string) ([]byte, error) {
	f, err := s.fs[space].OpenFile(context.Background(), p, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch p {
	case "/status.php":
		s.writeJSON(w, 200, map[string]any{"installed": true, "productname": "OpenCloud", "version": "2.0.0"})
		return
	case "/.well-known/openid-configuration":
		s.writeJSON(w, 200, map[string]any{"issuer": s.URL, "authorization_endpoint": s.URL + "/signin/v1/identifier/_/authorize",
			"token_endpoint": s.URL + "/konnect/v1/token"})
		return
	case "/konnect/v1/token":
		s.token(w, r)
		return
	}
	s.mu.Lock()
	ok := r.Header.Get("Authorization") == "Bearer "+s.access
	s.mu.Unlock()
	if !ok {
		s.writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthenticated", "message": "invalid token"}})
		return
	}
	switch {
	case p == "/graph/v1.0/me":
		s.writeJSON(w, 200, map[string]any{"displayName": "Anna Example", "mail": "anna@example.de", "onPremisesSamAccountName": "anna"})
	case p == "/graph/v1.0/me/drives":
		s.writeJSON(w, 200, map[string]any{"value": []any{
			map[string]any{"id": PersonalSpace, "name": "Anna Example", "driveType": "personal",
				"root":  map[string]any{"webDavUrl": s.URL + "/dav/spaces/" + PersonalSpace},
				"quota": map[string]any{"used": 2048, "total": 1 << 30, "remaining": 1<<30 - 2048}},
			map[string]any{"id": ProjectSpace, "name": "Projekt Relaunch", "driveType": "project",
				"root": map[string]any{"webDavUrl": s.URL + "/dav/spaces/" + ProjectSpace}},
			map[string]any{"id": "virtual-shares", "name": "Shares", "driveType": "virtual"},
		}})
	case strings.HasPrefix(p, "/graph/v1beta1/drives/"):
		s.graphItems(w, r)
	case strings.HasPrefix(p, "/tus/"):
		s.tusPatch(w, r)
	case strings.HasPrefix(p, "/dav/spaces/"):
		rest := strings.TrimPrefix(p, "/dav/spaces/")
		space, _, _ := strings.Cut(rest, "/")
		h, ok := s.dav[space]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && r.Header.Get("Tus-Resumable") != "" {
			s.tusCreate(w, r, space, strings.TrimPrefix(rest, space))
			return
		}
		h.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f := r.PostForm
	if f.Get("client_id") != s.ClientID {
		s.writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch f.Get("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if f.Get("code") != s.Code || base64.RawURLEncoding.EncodeToString(sum[:]) != s.challenge || f.Get("redirect_uri") == "" {
			s.writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
	case "refresh_token":
		if f.Get("refresh_token") != "refresh-1" {
			s.writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
		s.refreshes++
		s.access = "access-" + strconv.Itoa(s.refreshes+1)
	default:
		s.writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	s.writeJSON(w, 200, map[string]any{"access_token": s.access, "token_type": "Bearer", "expires_in": 300, "refresh_token": "refresh-1"})
}

func (s *Server) graphItems(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/graph/v1beta1/drives/"), "/")
	if len(parts) < 4 || parts[1] != "items" {
		http.NotFound(w, r)
		return
	}
	drive, item := parts[0], parts[2]
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idMu.Lock()
	loc, ok := s.ids[item]
	s.idMu.Unlock()
	if !ok || !strings.HasPrefix(loc, drive+":") {
		s.writeJSON(w, 404, map[string]any{"error": map[string]string{"code": "itemNotFound", "message": "item not found"}})
		return
	}
	switch {
	case parts[3] == "createLink" && r.Method == http.MethodPost:
		var in struct {
			Type, Password, ExpirationDateTime string
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Type != "view" && in.Type != "upload" {
			s.writeJSON(w, 400, map[string]any{"error": map[string]string{"code": "invalidRequest", "message": "bad type"}})
			return
		}
		if len(in.Password) < 8 {
			s.writeJSON(w, 400, map[string]any{"error": map[string]string{"code": "invalidRequest", "message": "password required"}})
			return
		}
		l := Link{Drive: drive, Item: item, ID: fmt.Sprintf("perm-%d", len(s.Links)+1), Type: in.Type, Password: in.Password, Expiration: in.ExpirationDateTime}
		s.Links = append(s.Links, l)
		out := map[string]any{"id": l.ID, "hasPassword": true, "link": map[string]any{"type": l.Type, "webUrl": s.URL + "/s/" + l.ID}}
		if l.Expiration != "" {
			out["expirationDateTime"] = l.Expiration
		}
		s.writeJSON(w, 200, out)
	case parts[3] == "permissions" && len(parts) == 5 && r.Method == http.MethodDelete:
		for i := range s.Links {
			if s.Links[i].ID == parts[4] && !s.Links[i].Revoked {
				s.Links[i].Revoked = true
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		s.writeJSON(w, 404, map[string]any{"error": map[string]string{"code": "itemNotFound", "message": "no such permission"}})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) tusCreate(w http.ResponseWriter, r *http.Request, space, dir string) {
	n, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil {
		http.Error(w, "Upload-Length required", http.StatusBadRequest)
		return
	}
	var name string
	for _, kv := range strings.Split(r.Header.Get("Upload-Metadata"), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), " ")
		if k == "filename" {
			b, _ := base64.StdEncoding.DecodeString(v)
			name = string(b)
		}
	}
	if name == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	id := fmt.Sprintf("u%d", len(s.tus)+1)
	s.tus[id] = &tusUpload{space: space, path: path.Join("/", dir, name), length: n}
	s.mu.Unlock()
	w.Header().Set("Tus-Resumable", "1.0.0")
	w.Header().Set("Location", s.URL+"/tus/"+id)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) tusPatch(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/tus/")
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.tus[id]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Tus-Resumable", "1.0.0")
	if r.Method == http.MethodHead {
		w.Header().Set("Upload-Offset", strconv.Itoa(len(u.data)))
		w.Header().Set("Upload-Length", strconv.FormatInt(u.length, 10))
		return
	}
	off, _ := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if r.Method != http.MethodPatch || r.Header.Get("Content-Type") != "application/offset+octet-stream" || off != int64(len(u.data)) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	b, _ := io.ReadAll(r.Body)
	u.data = append(u.data, b...)
	s.TUSPatch++
	if int64(len(u.data)) == u.length {
		f, err := s.fs[u.space].OpenFile(r.Context(), u.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.Write(u.data)
			f.Close()
		}
	}
	if s.FailNextPatch {
		s.FailNextPatch = false
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Upload-Offset", strconv.Itoa(len(u.data)))
	w.WriteHeader(http.StatusNoContent)
}

// ==== file ids ================================================================

// idFS adds an oc:fileid dead property to every file.
type idFS struct {
	webdav.FileSystem
	space string
	s     *Server
}

func (f *idFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	file, err := f.FileSystem.OpenFile(ctx, name, flag, perm)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(f.space + ":" + path.Clean("/"+name)))
	id := f.space + "!" + hex.EncodeToString(sum[:8])
	f.s.idMu.Lock()
	f.s.ids[id] = f.space + ":" + path.Clean("/"+name)
	f.s.idMu.Unlock()
	return &idFile{File: file, id: id}, nil
}

type idFile struct {
	webdav.File
	id string
}

var fileIDName = xml.Name{Space: "http://owncloud.org/ns", Local: "fileid"}

func (f *idFile) DeadProps() (map[xml.Name]webdav.Property, error) {
	return map[xml.Name]webdav.Property{fileIDName: {XMLName: fileIDName, InnerXML: []byte(f.id)}}, nil
}

func (f *idFile) Patch([]webdav.Proppatch) ([]webdav.Propstat, error) {
	return nil, nil
}
