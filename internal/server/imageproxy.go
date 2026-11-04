// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Midas1989/Mailclient/internal/app"
	"github.com/Midas1989/Mailclient/internal/imageproxy"
	"github.com/Midas1989/Mailclient/internal/server/api"
)

// The remote image proxy in server mode: mail iframes are sandboxed and
// cannot use the session cookie, so image URLs are signed with a server
// key and carry the user id ("u"). Unsigned requests need a session.

var (
	proxyKeyMu    sync.Mutex
	proxyKeyCache = map[string][]byte{}
)

// imageProxyKey loads (or creates) data_dir/imageproxy.key.
func (s *Server) imageProxyKey() []byte {
	path := filepath.Join(s.cfg.DataDir, "imageproxy.key")
	proxyKeyMu.Lock()
	defer proxyKeyMu.Unlock()
	if k, ok := proxyKeyCache[path]; ok {
		return k
	}
	if b, err := os.ReadFile(path); err == nil {
		if k, err := hex.DecodeString(strings.TrimSpace(string(b))); err == nil && len(k) >= 32 {
			proxyKeyCache[path] = k
			return k
		}
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	if err := os.WriteFile(path, []byte(hex.EncodeToString(k)), 0o600); err != nil {
		s.log.Warn("image proxy: storing the key failed (signed image links will not survive a restart)", "err", err)
	}
	proxyKeyCache[path] = k
	return k
}

// imageSigner returns the signer of a user's App.
func (s *Server) imageSigner(userID int64) *imageproxy.Signer {
	return &imageproxy.Signer{Key: s.imageProxyKey(), Subject: strconv.FormatInt(userID, 10)}
}

// handleImageProxy serves GET /api/proxy/image.
func (s *Server) handleImageProxy(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !imageproxy.Signed(q) {
		s.appHandler(api.AppHandler(func(w http.ResponseWriter, r *http.Request, a *app.App) { a.ServeImageProxy(w, r, true) }))(w, r)
		return
	}
	signer := &imageproxy.Signer{Key: s.imageProxyKey()}
	_, subject, err := signer.Verify(q)
	uid, perr := strconv.ParseInt(subject, 10, 64)
	if err != nil || perr != nil || uid <= 0 {
		http.Error(w, "invalid or expired image link", http.StatusForbidden)
		return
	}
	a, err := s.apps.get(r.Context(), uid)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	a.ServeImageProxy(w, r, false)
}
