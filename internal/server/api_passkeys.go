// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Midas1989/Mailclient/internal/auth"
)

// Passkey endpoints (WebAuthn, see internal/auth/passkeys.go):
//
//	GET    /api/auth/passkeys                        -> {passkeys, enabled, max, reauth: {password, code}}
//	POST   /api/auth/passkeys/register/begin         {password, code, recoveryCode} -> {challengeId, publicKey}
//	POST   /api/auth/passkeys/register/finish        {challengeId, name, credential} -> 201 {passkey}
//	PATCH  /api/auth/passkeys/{id}                   {name}
//	DELETE /api/auth/passkeys/{id}                   {password, code, recoveryCode}
//	POST   /api/auth/passkeys/login/begin            (public) -> {challengeId, publicKey}
//	POST   /api/auth/passkeys/login/finish           (public) {challengeId, credential} -> like /api/auth/login
//
// All POST/PATCH/DELETE requests pass the CSRF middleware (Origin check and
// double-submit token), the public login ones as well - they are not exempt.
// Adding and removing passkeys is limited to cookie sessions of the web client;
// device tokens of native clients cannot manage them.

type passkeyBeginResponse struct {
	ChallengeID string `json:"challengeId"`
	PublicKey   any    `json:"publicKey"`
}

// passkeyManager lets only web (cookie) sessions through: a device token is a
// long-lived bearer credential and must not be able to add login paths.
func passkeyManager(w http.ResponseWriter, a *authInfo) bool {
	if a.Bearer || a.Session.Kind != auth.KindWeb {
		writeError(w, http.StatusForbidden, "forbidden", "passkeys can only be managed in the web client")
		return false
	}
	return true
}

func (s *Server) handlePasskeyList(w http.ResponseWriter, r *http.Request, a *authInfo) {
	list, err := s.auth.ListPasskeys(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	// "reauth" tells the UI which proofs adding or removing a passkey asks for.
	writeJSON(w, http.StatusOK, map[string]any{
		"passkeys": list, "enabled": s.auth.PasskeysEnabled(), "max": auth.MaxPasskeysPerUser,
		"reauth": map[string]bool{"password": a.User.HasPassword, "code": a.User.TOTPEnabled},
	})
}

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if !passkeyManager(w, a) {
		return
	}
	var req reauthReq
	if !readJSON(w, r, &req) {
		return
	}
	if !s.auth.PasskeysEnabled() {
		s.writeAuthError(w, auth.ErrPasskeysDisabled)
		return
	}
	if !s.limitLogin(w, r, a.User.Username) {
		return
	}
	if err := s.auth.VerifyPasskeyStepUp(r.Context(), a.User.ID, a.Session.ID, req.Password, req.Code, req.RecoveryCode); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.rotateAfterStepUp(w, r, a)
	id, creation, err := s.auth.BeginPasskeyRegistration(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, passkeyBeginResponse{ChallengeID: id, PublicKey: creation.Response})
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if !passkeyManager(w, a) {
		return
	}
	var req struct {
		ChallengeID string          `json:"challengeId"`
		Name        string          `json:"name"`
		Credential  json.RawMessage `json:"credential"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	pk, err := s.auth.FinishPasskeyRegistration(r.Context(), a.User.ID, req.ChallengeID, req.Name, req.Credential)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"passkey": pk})
}

func (s *Server) handlePasskeyRename(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if !passkeyManager(w, a) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.auth.RenamePasskey(r.Context(), a.User.ID, id, req.Name); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if !passkeyManager(w, a) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req reauthReq
	if !readJSON(w, r, &req) || !s.limitLogin(w, r, a.User.Username) {
		return
	}
	if err := s.auth.VerifyPasskeyStepUp(r.Context(), a.User.ID, a.Session.ID, req.Password, req.Code, req.RecoveryCode); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.rotateAfterStepUp(w, r, a)
	if err := s.auth.DeletePasskey(r.Context(), a.User.ID, id); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writePasskeyLoginError answers failures of the public login endpoints. The
// message never says why a passkey was refused.
func (s *Server) writePasskeyLoginError(w http.ResponseWriter, err error) {
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "passkey sign-in failed")
		return
	}
	s.writeAuthError(w, err)
}

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.PasskeysEnabled() {
		s.writeAuthError(w, auth.ErrPasskeysDisabled)
		return
	}
	if !s.limitLogin(w, r, "passkey") {
		return
	}
	id, assertion, err := s.auth.BeginPasskeyLogin(r.Context(), s.clientInfoFor(r, ""))
	if err != nil {
		s.writePasskeyLoginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, passkeyBeginResponse{ChallengeID: id, PublicKey: assertion.Response})
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChallengeID string          `json:"challengeId"`
		Credential  json.RawMessage `json:"credential"`
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
	res, err := s.auth.FinishPasskeyLogin(r.Context(), req.ChallengeID, req.Credential, auth.KindWeb, s.clientInfoFor(r, ""))
	if err != nil {
		s.writePasskeyLoginError(w, err)
		return
	}
	s.writeLoginResult(w, res)
}
