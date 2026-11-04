// SPDX-License-Identifier: AGPL-3.0-or-later

// Package vaultwardentest is a fake Vaultwarden server for tests: prelogin,
// the password and refresh_token grants (with optional TOTP), text and
// file Sends (v2 upload flow) and Send deletion.
package vaultwardentest

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/cloud/vaultwarden"
)

// Send is a stored Send request plus its uploaded file.
type Send struct {
	ID, AccessID string
	Request      map[string]any
	Data         []byte
	FileName     string // multipart file name (the encrypted name)
	Deleted      bool
}

// Server is the fake.
type Server struct {
	*httptest.Server
	Email    string
	Password string
	KDF      vaultwarden.KDFParams
	// TOTP, when set, is required as twoFactorToken.
	TOTP    string
	UserKey vaultwarden.SymmetricKey

	mu      sync.Mutex
	access  string
	Sends   []*Send
	Logins  int
	Refresh int
}

// New starts a fake with PBKDF2 (1000 iterations, fast tests).
func New() *Server {
	s := &Server{Email: "anna@example.de", Password: "correct horse battery", KDF: vaultwarden.KDFParams{Type: vaultwarden.KDFPBKDF2, Iterations: vaultwarden.MinPBKDF2Iterations},
		access: "vw-access-1"}
	s.UserKey = make([]byte, 64)
	_, _ = rand.Read(s.UserKey)
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// SendList returns the Sends.
func (s *Server) SendList() []Send {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Send, len(s.Sends))
	for i, x := range s.Sends {
		out[i] = *x
	}
	return out
}

// ExpireAccess invalidates the access token.
func (s *Server) ExpireAccess() { s.mu.Lock(); s.access += "-expired"; s.mu.Unlock() }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch p := r.URL.Path; {
	case p == "/identity/accounts/prelogin":
		var in struct{ Email string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		writeJSON(w, 200, map[string]any{"kdf": s.KDF.Type, "kdfIterations": s.KDF.Iterations, "kdfMemory": s.KDF.Memory,
			"kdfParallelism": s.KDF.Parallelism})
	case p == "/identity/connect/token":
		s.token(w, r)
	case strings.HasPrefix(p, "/api/"):
		s.mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+s.access
		s.mu.Unlock()
		if !ok {
			writeJSON(w, 401, map[string]string{"message": "Unauthorized"})
			return
		}
		s.api(w, r, strings.TrimPrefix(p, "/api"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f := r.PostForm
	s.mu.Lock()
	defer s.mu.Unlock()
	switch f.Get("grant_type") {
	case "password":
		mk, _ := vaultwarden.MasterKey(s.Password, s.Email, s.KDF)
		if f.Get("username") != s.Email || f.Get("password") != vaultwarden.MasterPasswordHash(mk, s.Password) || f.Get("deviceIdentifier") == "" {
			writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "Username or password is incorrect. Try again",
				"ErrorModel": map[string]string{"Message": "Username or password is incorrect. Try again"}})
			return
		}
		if s.TOTP != "" && (f.Get("twoFactorToken") != s.TOTP || f.Get("twoFactorProvider") != "0") {
			writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "Two factor required.",
				"TwoFactorProviders": []string{"0"}, "TwoFactorProviders2": map[string]any{"0": nil}})
			return
		}
		key, _ := vaultwarden.StretchMasterKey(mk).Encrypt(s.UserKey)
		s.Logins++
		writeJSON(w, 200, map[string]any{"access_token": s.access, "expires_in": 3600, "refresh_token": "vw-refresh",
			"token_type": "Bearer", "Key": key.String(), "Kdf": s.KDF.Type, "KdfIterations": s.KDF.Iterations})
	case "refresh_token":
		if f.Get("refresh_token") != "vw-refresh" {
			writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
			return
		}
		s.Refresh++
		s.access = fmt.Sprintf("vw-access-%d", s.Refresh+1)
		writeJSON(w, 200, map[string]any{"access_token": s.access, "expires_in": 3600, "refresh_token": "vw-refresh", "token_type": "Bearer"})
	default:
		writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
	}
}

func (s *Server) api(w http.ResponseWriter, r *http.Request, p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	newSend := func(in map[string]any) *Send {
		n := len(s.Sends) + 1
		sd := &Send{ID: fmt.Sprintf("send-%d", n), AccessID: fmt.Sprintf("acc%d", n), Request: in}
		s.Sends = append(s.Sends, sd)
		return sd
	}
	resp := func(sd *Send) map[string]any {
		out := map[string]any{"id": sd.ID, "accessId": sd.AccessID, "deletionDate": sd.Request["deletionDate"],
			"expirationDate": sd.Request["expirationDate"], "type": sd.Request["type"]}
		if sd.Request["type"] == float64(1) {
			out["file"] = map[string]any{"id": "file-" + sd.ID}
		}
		return out
	}
	decode := func() (map[string]any, bool) {
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, 400, map[string]string{"message": "bad json"})
			return nil, false
		}
		for _, k := range []string{"name", "key"} {
			if v, _ := in[k].(string); !strings.HasPrefix(v, "2.") {
				writeJSON(w, 400, map[string]string{"message": k + " must be a cipher string"})
				return nil, false
			}
		}
		if d, _ := in["deletionDate"].(string); d == "" {
			writeJSON(w, 400, map[string]string{"message": "deletionDate required"})
			return nil, false
		} else if t, err := time.Parse(time.RFC3339, d); err != nil || t.After(time.Now().Add(32*24*time.Hour)) {
			writeJSON(w, 400, map[string]string{"message": "invalid deletionDate"})
			return nil, false
		}
		return in, true
	}
	switch {
	case p == "/sends" && r.Method == http.MethodPost:
		in, ok := decode()
		if !ok {
			return
		}
		if in["type"] != float64(0) || in["text"] == nil {
			writeJSON(w, 400, map[string]string{"message": "text send expected"})
			return
		}
		writeJSON(w, 200, resp(newSend(in)))
	case p == "/sends/file/v2" && r.Method == http.MethodPost:
		in, ok := decode()
		if !ok {
			return
		}
		if in["type"] != float64(1) || in["file"] == nil || in["fileLength"] == nil {
			writeJSON(w, 400, map[string]string{"message": "file send expected"})
			return
		}
		sd := newSend(in)
		writeJSON(w, 200, map[string]any{"fileUploadType": 0, "object": "send-fileUpload",
			"url": "/sends/" + sd.ID + "/file/file-" + sd.ID, "sendResponse": resp(sd)})
	case strings.HasPrefix(p, "/sends/") && strings.Contains(p, "/file/") && r.Method == http.MethodPost:
		id := strings.Split(strings.TrimPrefix(p, "/sends/"), "/")[0]
		for _, sd := range s.Sends {
			if sd.ID == id {
				f, hdr, err := r.FormFile("data")
				if err != nil {
					writeJSON(w, 400, map[string]string{"message": "data missing"})
					return
				}
				sd.Data, _ = io.ReadAll(f)
				sd.FileName = hdr.Filename
				if float64(len(sd.Data)) != sd.Request["fileLength"] {
					writeJSON(w, 400, map[string]string{"message": "length mismatch"})
					return
				}
				w.WriteHeader(200)
				return
			}
		}
		writeJSON(w, 404, map[string]string{"message": "send not found"})
	case strings.HasPrefix(p, "/sends/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(p, "/sends/")
		for _, sd := range s.Sends {
			if sd.ID == id && !sd.Deleted {
				sd.Deleted = true
				w.WriteHeader(200)
				return
			}
		}
		writeJSON(w, 404, map[string]string{"message": "send not found"})
	default:
		http.NotFound(w, r)
	}
}
