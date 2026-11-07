// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// Passkeys for connecting apps (internal/auth/passkey_device.go):
//
//	POST /api/device/passkey/begin    (public, native)  -> {challengeId, publicKey}; 404 without auth.passkeys.android_apps
//	POST /api/device/passkey/login    (public, native)  {challengeId, credential, device_name, client_public_key, scope}
//	                                                     -> token like /api/device/login
//	POST /api/device/approve/passkey  (browser session) {user_code} -> {challengeId, publicKey}; the answer goes to
//	                                                     /api/device/approve as "passkey": {challengeId, credential}
//	POST /api/device/approve/oidc     (browser session) {user_code} -> {url}: new sign-in at the identity
//	                                                     provider that approves this request (device_oidc.go)
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
	if !s.auth.PasskeyAppLogin() {
		writeError(w, http.StatusNotFound, "not_found", "no app may sign in with a passkey on this server")
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
	if !s.auth.PasskeyAppLogin() {
		writeError(w, http.StatusNotFound, "not_found", "no app may sign in with a passkey on this server")
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

// handleDeviceApprovePasskeyBegin starts the passkey confirmation of the device
// request shown on the verification page (bound to its user code).
func (s *Server) handleDeviceApprovePasskeyBegin(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		UserCode string `json:"user_code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.Bearer || a.Session.Kind != auth.KindWeb {
		writeError(w, http.StatusForbidden, "forbidden", "approve devices from a browser session")
		return
	}
	if !s.limitLogin(w, r, a.User.Username) {
		return
	}
	id, assertion, err := s.auth.BeginPasskeyDeviceApproval(r.Context(), a.User.ID, req.UserCode)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, passkeyBeginResponse{ChallengeID: id, PublicKey: assertion.Response})
}

// approvalProof is the step-up of a device approval, given after the request
// was shown: a passkey assertion bound to this request, or password and/or
// code (reauth). A recent sign-in alone never is (device code phishing): an
// account without password and second factor cannot use reauth here, whose
// fallback accepts a recent passkey or IdP session; it confirms with a passkey
// or at the identity provider (handleDeviceApproveOIDC) instead.
func (s *Server) approvalProof(w http.ResponseWriter, r *http.Request, a *authInfo, userCode string, pk *passkeyProof, password, code, recovery string) bool {
	if pk != nil && pk.ChallengeID != "" {
		if a.Session.Kind != auth.KindWeb {
			refuseDevice(w)
			return false
		}
		if err := s.auth.VerifyPasskeyDeviceApproval(r.Context(), a.User.ID, userCode, pk.ChallengeID, pk.Credential); err != nil {
			s.writeAuthError(w, err)
			return false
		}
		s.rotateAfterStepUp(w, r, a)
		return true
	}
	if !a.User.HasPassword && !a.User.TOTPEnabled {
		writeError(w, http.StatusForbidden, "approval_proof_required",
			"confirm with a passkey or a new sign-in at your identity provider; without either, set up a passkey first")
		return false
	}
	return s.reauth(w, r, a, password, code, recovery)
}

// handleDeviceApproveOIDC starts the confirmation of the shown request at the
// identity provider. POST with CSRF: a link from outside cannot start it.
func (s *Server) handleDeviceApproveOIDC(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		UserCode string `json:"user_code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.Bearer || a.Session.Kind != auth.KindWeb {
		writeError(w, http.StatusForbidden, "forbidden", "approve devices from a browser session")
		return
	}
	if !s.limitLogin(w, r, a.User.Username) {
		return
	}
	u, err := s.auth.OIDCDeviceApprovalURL(r.Context(), a.User.ID, req.UserCode)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	// Bind the flow to this browser like a normal OIDC login.
	pu, _ := url.Parse(u)
	s.setCookie(w, oidcCookie, pu.Query().Get("state"), 10*time.Minute, true, http.SameSiteLaxMode)
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
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
