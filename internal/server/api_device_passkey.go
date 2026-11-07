// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"net/http"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// Passkeys for connecting apps (internal/auth/passkey_device.go):
//
//	POST /api/device/passkey/begin    (public, native)  -> {challengeId, publicKey}
//	POST /api/device/passkey/login    (public, native)  {challengeId, credential, device_name, client_public_key, scope}
//	                                                     -> token like /api/device/login
//	POST /api/device/approve/passkey  (browser session) -> {challengeId, publicKey}; the answer goes to
//	                                                     /api/device/approve as "passkey": {challengeId, credential}
//	GET  /.well-known/assetlinks.json (public)          Digital Asset Links of auth.passkeys.android_apps
//
// The two native endpoints are CSRF-exempt like /api/device/login (no cookie,
// no session, the token only in the body; docs/security/BASIS-ABWEICHUNGEN.md
// no. 2). They share the rate limit and the network throttle of the web
// passkey login.

type passkeyProof struct {
	ChallengeID string          `json:"challengeId"`
	Credential  json.RawMessage `json:"credential"`
}

func (s *Server) handleDevicePasskeyBegin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.PasskeysEnabled() {
		s.writeAuthError(w, auth.ErrPasskeysDisabled)
		return
	}
	if !s.limitLogin(w, r, "passkey") {
		return
	}
	id, assertion, err := s.auth.BeginPasskeyDeviceLogin(r.Context(), s.clientInfoFor(r, ""))
	if err != nil {
		s.writePasskeyLoginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, passkeyBeginResponse{ChallengeID: id, PublicKey: assertion.Response})
}

func (s *Server) handleDevicePasskeyLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		passkeyProof
		DeviceName      string `json:"device_name"`
		ClientPublicKey string `json:"client_public_key"`
		Scope           string `json:"scope"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !s.auth.PasskeysEnabled() {
		s.writeAuthError(w, auth.ErrPasskeysDisabled)
		return
	}
	if !s.limitLogin(w, r, "passkey") {
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
	res, err := s.auth.FinishPasskeyDeviceLogin(r.Context(), req.ChallengeID, req.Credential, ci)
	if err != nil {
		s.writePasskeyLoginError(w, err)
		return
	}
	s.writeDeviceToken(w, res)
}

// handleDeviceApprovePasskeyBegin starts the passkey confirmation of a device
// approval on the verification page.
func (s *Server) handleDeviceApprovePasskeyBegin(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if a.Bearer || a.Session.Kind != auth.KindWeb {
		writeError(w, http.StatusForbidden, "forbidden", "approve devices from a browser session")
		return
	}
	if !s.limitLogin(w, r, a.User.Username) {
		return
	}
	id, assertion, err := s.auth.BeginPasskeyDeviceApproval(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, passkeyBeginResponse{ChallengeID: id, PublicKey: assertion.Response})
}

// approvalProof is the step-up of a device approval: a passkey assertion, a
// fresh passkey sign-in of this session, or password and code (reauth).
func (s *Server) approvalProof(w http.ResponseWriter, r *http.Request, a *authInfo, pk *passkeyProof, password, code, recovery string) bool {
	if pk != nil && pk.ChallengeID != "" {
		if err := s.auth.VerifyPasskeyDeviceApproval(r.Context(), a.User.ID, pk.ChallengeID, pk.Credential); err != nil {
			s.writeAuthError(w, err)
			return false
		}
		return true
	}
	if password == "" && code == "" && recovery == "" && s.auth.PasskeySessionFresh(a.Session) {
		return true
	}
	return s.reauth(w, r, a, password, code, recovery)
}

func (s *Server) handleAssetLinks(w http.ResponseWriter, r *http.Request) {
	links := s.cfg.AndroidAssetLinks()
	if len(links) == 0 {
		writeError(w, http.StatusNotFound, "not_found", "no Android apps configured")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, links)
}
