// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/signature"
	"github.com/Midas1989/Mailclient/internal/server/sealed"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/store/blob"
)

// Device authorization flow for the desktop client ("Mit TuxMail-Server
// verbinden"), modelled on RFC 8628. Field names follow the RFC (snake_case).

func decodePubKey(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	s = strings.TrimRight(s, "=")
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil || len(b) != 32 {
		return nil, errors.New("client_public_key must be a base64 X25519 public key (32 bytes)")
	}
	return b, nil
}

func deviceError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Server) handleDeviceAuthorize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientName      string `json:"client_name"`
		ClientPublicKey string `json:"client_public_key"`
		Scope           string `json:"scope"`
	}
	if !readJSON(w, r, &req) || !s.limitLogin(w, r, "device-authorize") {
		return
	}
	pub, err := decodePubKey(req.ClientPublicKey)
	if err != nil {
		deviceError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	scopes, err := auth.ParseDeviceScopes(req.Scope)
	if err != nil {
		deviceError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	ci := s.clientInfoFor(r, req.ClientName)
	ci.PubKey = pub
	ci.Scopes = scopes
	g, err := s.auth.StartDeviceAuthorization(r.Context(), ci)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	verify := s.cfg.PublicURLJoin("/device")
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               g.DeviceCode,
		"user_code":                 g.UserCode,
		"verification_uri":          verify,
		"verification_uri_complete": verify + "?user_code=" + url.QueryEscape(g.UserCode),
		"interval":                  int(g.Interval / time.Second),
		"expires_in":                int(g.ExpiresIn / time.Second),
	})
}

func (s *Server) writeDeviceToken(w http.ResponseWriter, res *auth.LoginResult) {
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": res.Token,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(res.Session.ExpiresAt) / time.Second),
		"scope":        strings.Join(res.Session.Scopes, " "),
		"user":         res.User,
		"session_id":   res.Session.ID,
	})
}

// handleDeviceRotate issues the successor of the presented device token, see
// internal/auth/devicetokens.go. The client stores the new token, switches to it
// and forgets the old one; the old one ends with the first use of the new one.
func (s *Server) handleDeviceRotate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	next, exp, err := s.auth.RotateDeviceToken(r.Context(), a.Session, a.Token)
	if errors.Is(err, auth.ErrTokenSuperseded) {
		writeError(w, http.StatusConflict, "token_superseded", "this token was already replaced")
		return
	}
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": next,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(exp) / time.Second),
		"scope":        strings.Join(a.Session.Scopes, " "),
		"session_id":   a.Session.ID,
	})
}

// Polling limits of /api/device/token (NA-08). A client polls every 5 s
// (12/min), RFC 8628 clients slow down when told to.
const (
	deviceTokenCodePerMinute = 20
	deviceTokenCodeBurst     = 10
	deviceTokenIPPerMinute   = 120
	deviceTokenIPBurst       = 60
)

func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if ok, wait := s.deviceTokenIP.allow(limiterKey(clientFrom(r).IP)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		deviceError(w, http.StatusTooManyRequests, "rate_limited", "too many token requests from this network")
		return
	}
	// Per device code (keyed by its hash, the code itself is never kept in
	// memory): RFC 8628 section 3.5 answers too-frequent polling with slow_down.
	sum := sha256.Sum256([]byte(req.DeviceCode))
	if ok, wait := s.deviceTokenCode.allow(string(sum[:16])); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		deviceError(w, http.StatusBadRequest, "slow_down", "polling too fast")
		return
	}
	res, err := s.auth.PollDeviceToken(r.Context(), req.DeviceCode)
	switch {
	case err == nil:
		s.writeDeviceToken(w, res)
	case errors.Is(err, auth.ErrAuthorizationPending), errors.Is(err, auth.ErrSlowDown),
		errors.Is(err, auth.ErrAccessDenied), errors.Is(err, auth.ErrExpiredToken):
		deviceError(w, http.StatusBadRequest, err.Error(), "")
	case errors.Is(err, auth.ErrInvalidCode):
		deviceError(w, http.StatusBadRequest, "invalid_grant", "unknown device code")
	default:
		s.writeAuthError(w, err)
	}
}

// handleDeviceLogin is the direct login alternative for clients.
func (s *Server) handleDeviceLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		TOTPCode        string `json:"totp_code"`
		RecoveryCode    string `json:"recovery_code"`
		DeviceName      string `json:"device_name"`
		ClientPublicKey string `json:"client_public_key"`
		Scope           string `json:"scope"`
	}
	if !readJSON(w, r, &req) || !s.limitLogin(w, r, req.Username) {
		return
	}
	pub, err := decodePubKey(req.ClientPublicKey)
	if err != nil {
		deviceError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	scopes, err := auth.ParseDeviceScopes(req.Scope)
	if err != nil {
		deviceError(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	ci := s.clientInfoFor(r, req.DeviceName)
	ci.PubKey = pub
	ci.Scopes = scopes
	res, err := s.auth.Login(r.Context(), auth.LoginRequest{
		Login: req.Username, Password: req.Password, TOTPCode: req.TOTPCode, RecoveryCode: req.RecoveryCode,
		Kind: auth.KindDevice, Client: ci,
	})
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	if res.MFARequired {
		// Clients resend the request including totp_code.
		deviceError(w, http.StatusUnauthorized, "mfa_required", "two-factor code required (totp_code or recovery_code)")
		return
	}
	s.writeDeviceToken(w, res)
}

// handleDeviceRequest shows the approval page what is being approved.
func (s *Server) handleDeviceRequest(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if !s.limitLogin(w, r, "device-request") { // no user code enumeration (A-10)
		return
	}
	req, err := s.auth.LookupDeviceRequest(r.Context(), r.URL.Query().Get("user_code"))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	// passkey: the account can confirm with a passkey instead of password and
	// code; passkeyFresh: this session's own passkey sign-in is the confirmation.
	writeJSON(w, http.StatusOK, map[string]any{"request": req, "totpRequired": a.User.TOTPEnabled, "passwordRequired": a.User.HasPassword,
		"passkey": s.auth.HasUsablePasskey(r.Context(), a.User.ID), "passkeyFresh": s.auth.PasskeySessionFresh(a.Session)})
}

func (s *Server) handleDeviceApprove(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		UserCode     string        `json:"user_code"`
		Approve      *bool         `json:"approve"`
		Password     string        `json:"password"`
		TOTPCode     string        `json:"totp_code"`
		RecoveryCode string        `json:"recovery_code"`
		Passkey      *passkeyProof `json:"passkey"` // api_device_passkey.go
	}
	if !readJSON(w, r, &req) || !s.limitLogin(w, r, a.User.Username) {
		return
	}
	if a.Bearer {
		writeError(w, http.StatusForbidden, "forbidden", "approve devices from a browser session")
		return
	}
	approve := req.Approve == nil || *req.Approve
	// Approving creates a long-lived way into the account.
	if approve && !s.approvalProof(w, r, a, req.Passkey, req.Password, req.TOTPCode, req.RecoveryCode) {
		return
	}
	if err := s.auth.ApproveDevice(r.Context(), a.User.ID, req.UserCode, approve); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MailData is the part of a user's mail store exported for provisioning.
type MailData struct {
	Accounts   []domain.Account   `json:"accounts"`
	Identities []domain.Identity  `json:"identities"`
	Signatures []domain.Signature `json:"signatures"`
}

// MailDataSource reads MailData from a user data directory.
type MailDataSource func(ctx context.Context, userDir string) (*MailData, error)

// UserStoreFile is the per-user mail database inside data_dir/users/<id>/.
const UserStoreFile = "tuxmail.db"

// storeMailData reads the per-user store if it exists (it is never created here).
func storeMailData(ctx context.Context, userDir string) (*MailData, error) {
	md := &MailData{Accounts: []domain.Account{}, Identities: []domain.Identity{}, Signatures: []domain.Signature{}}
	path := filepath.Join(userDir, UserStoreFile)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return md, nil
	}
	st, err := store.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	if md.Accounts, err = st.ListAccounts(ctx); err != nil {
		return nil, err
	}
	for _, a := range md.Accounts {
		ids, err := st.ListIdentities(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		md.Identities = append(md.Identities, ids...)
	}
	if md.Signatures, err = st.ListSignatures(ctx); err != nil {
		return nil, err
	}
	if md.Accounts == nil {
		md.Accounts = []domain.Account{}
	}
	if md.Signatures == nil {
		md.Signatures = []domain.Signature{}
	}
	embedSignatureImages(filepath.Join(userDir, "blobs"), md.Signatures)
	return md, nil
}

// embedSignatureImages turns the cid: images of signatures into data: URIs
// so that a provisioned client can store them in its own blob store.
func embedSignatureImages(dir string, sigs []domain.Signature) {
	if _, err := os.Stat(dir); err != nil {
		return
	}
	var bs *blob.Store
	for i := range sigs {
		if len(sigs[i].Images) == 0 {
			continue
		}
		if bs == nil {
			var err error
			if bs, err = blob.New(dir); err != nil {
				return
			}
		}
		byCID := map[string]domain.SignatureImage{}
		for _, im := range sigs[i].Images {
			byCID[im.ContentID] = im
		}
		sigs[i].HTML = signature.RewriteImages(sigs[i].HTML, func(src string) (string, bool) {
			im, ok := byCID[strings.Trim(strings.TrimPrefix(src, "cid:"), "<> ")]
			if !ok || !strings.HasPrefix(src, "cid:") {
				return src, true
			}
			rc, err := bs.Open(im.BlobHash)
			if err != nil {
				return src, true
			}
			defer rc.Close()
			data, err := io.ReadAll(io.LimitReader(rc, signature.MaxImageSize+1))
			if err != nil {
				return src, true
			}
			return signature.DataURI(im.MIMEType, data), true
		})
		sigs[i].Images = nil
	}
}

// Provisioning modes recommended to the desktop client.
const (
	ModeThin  = "thin"  // the client uses this server as its backend
	ModeLocal = "local" // the client syncs mail itself with the given settings
)

// handleProvisioningProfile returns the configuration bundle for a client
// (without secrets). It works with a device token or a browser session.
func (s *Server) handleProvisioningProfile(w http.ResponseWriter, r *http.Request, a *authInfo) {
	ctx := r.Context()
	md, err := s.mailData(ctx, s.auth.UserDir(a.User.ID))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	templates, err := s.auth.TemplatesForUser(ctx, a.User)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	settings, err := s.auth.Settings(ctx, a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	mode, reason := ModeLocal, "no accounts are synchronised by the server; apply the templates locally"
	if len(md.Accounts) > 0 {
		mode, reason = ModeThin, "the server already synchronises this user's accounts"
	}
	if raw, ok := settings["provisioning"]; ok {
		var p struct {
			Mode string `json:"mode"`
		}
		if json.Unmarshal(raw, &p) == nil && (p.Mode == ModeThin || p.Mode == ModeLocal) {
			mode, reason = p.Mode, "chosen by the user"
		}
	}
	refs := 0
	for _, acc := range md.Accounts {
		if acc.SecretRef != "" {
			refs++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": 1,
		"server":  map[string]string{"publicUrl": s.cfg.PublicURL, "version": s.version},
		"user": map[string]any{"id": a.User.ID, "username": a.User.Username, "email": a.User.Email,
			"displayName": a.User.DisplayName, "role": a.User.Role},
		"mode":       map[string]string{"recommended": mode, "reason": reason},
		"accounts":   md.Accounts,
		"identities": md.Identities,
		"signatures": md.Signatures,
		"templates":  templates,
		"settings":   settings,
		"secrets": map[string]any{
			"count":     refs,
			"available": refs > 0 && len(a.Session.ClientPubKey) == 32,
			"endpoint":  "/api/provisioning/secrets",
		},
	})
}

// handleProvisioningSecrets returns the account secrets sealed to the X25519
// key the client registered when its device token was issued.
func (s *Server) handleProvisioningSecrets(w http.ResponseWriter, r *http.Request, a *authInfo) {
	ctx := r.Context()
	if len(a.Session.ClientPubKey) != 32 {
		writeError(w, http.StatusConflict, "no_client_key", "this device token has no client public key; re-authorize with client_public_key")
		return
	}
	md, err := s.mailData(ctx, s.auth.UserDir(a.User.ID))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	vault, err := s.auth.UserSecrets(ctx, a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	out := map[string][]byte{}
	for _, acc := range md.Accounts {
		if acc.SecretRef == "" {
			continue
		}
		v, err := vault.Get(acc.SecretRef)
		if err != nil {
			continue // missing secrets are simply not included
		}
		out[acc.SecretRef] = v
	}
	plain, err := json.Marshal(map[string]any{"secrets": out})
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	box, err := sealed.Seal(a.Session.ClientPubKey, plain)
	clear(plain)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.auth.Audit(ctx, "provisioning.secrets", a.User.Username, true, "device="+a.Session.DeviceName)
	writeJSON(w, http.StatusOK, box)
}
