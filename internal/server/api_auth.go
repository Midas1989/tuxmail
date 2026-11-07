// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
)

func (s *Server) clientInfoFor(r *http.Request, deviceName string) auth.ClientInfo {
	return auth.ClientInfo{IP: clientFrom(r).IP, UserAgent: r.UserAgent(), DeviceName: deviceName}
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"product":           productMarker,
		"registration":      s.cfg.Auth.Registration,
		"oidcEnabled":       s.cfg.Auth.OIDC.Enabled,
		"passwordMinLength": s.cfg.Auth.PasswordMinLength,
		"passkeysEnabled":   s.auth.PasskeysEnabled(),
		"passkeyAppLogin":   s.auth.PasskeyAppLogin(), // apps may sign in with a passkey directly (assetlinks.json)
		"maxUploadMB":       s.cfg.Limits.MaxUploadMB,
		"clientBuild":       s.clientBuild(), // hash of the web client, not the version
	}
	if s.cfg.Security.ExposeVersion { // NA-07: anonymous clients get no version by default
		out["version"] = s.version
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCSRF returns the CSRF token for SPA clients (also set as cookie).
func (s *Server) handleCSRF(w http.ResponseWriter, r *http.Request) {
	tok, _ := r.Context().Value(ctxCSRF).(string)
	writeJSON(w, http.StatusOK, map[string]string{"csrfToken": tok, "header": csrfHeader})
}

func (s *Server) startSession(w http.ResponseWriter, res *auth.LoginResult) {
	s.setCookie(w, sessionCookie, res.Token, s.cfg.Auth.EffectiveSessionLifetime(), true, http.SameSiteLaxMode)
	// The CSRF token of the new session (prevents fixation of a known token).
	s.setCookie(w, csrfCookie, csrfTokenFor(res.Token), 0, false, http.SameSiteStrictMode)
}

type loginReq struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	TOTPCode     string `json:"totpCode"`
	RecoveryCode string `json:"recoveryCode"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if !readJSON(w, r, &req) || !s.limitLogin(w, r, req.Username) {
		return
	}
	res, err := s.auth.Login(r.Context(), auth.LoginRequest{
		Login: req.Username, Password: req.Password, TOTPCode: req.TOTPCode, RecoveryCode: req.RecoveryCode,
		Kind: auth.KindWeb, Client: s.clientInfoFor(r, ""),
	})
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.writeLoginResult(w, res)
}

func (s *Server) writeLoginResult(w http.ResponseWriter, res *auth.LoginResult) {
	if res.MFARequired {
		writeJSON(w, http.StatusOK, map[string]any{"status": "mfa_required", "challenge": res.Challenge, "methods": []string{"totp", "recovery_code"}})
		return
	}
	s.startSession(w, res)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "user": res.User, "mfaSetupRequired": s.auth.MFASetupRequired(res.User, res.Session)})
}

func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge    string `json:"challenge"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recoveryCode"`
	}
	if !readJSON(w, r, &req) || !s.limitLogin(w, r, "mfa") {
		return
	}
	res, err := s.auth.CompleteMFA(r.Context(), req.Challenge, req.Code, req.RecoveryCode, auth.KindWeb, s.clientInfoFor(r, ""))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.writeLoginResult(w, res)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if err := s.auth.Logout(r.Context(), a.Token); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.streams.revokeSession(a.User.ID, a.Session.ID)
	s.apps.lockPGP(a.User.ID)
	s.auth.Audit(r.Context(), "logout", a.User.Username, true, "")
	if !a.Bearer {
		s.setCookie(w, sessionCookie, "", -1, true, http.SameSiteLaxMode)
		s.setCookie(w, csrfCookie, randomToken(), 0, false, http.SameSiteStrictMode)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, a *authInfo) {
	left, _ := s.auth.RecoveryCodesLeft(r.Context(), a.User.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":              a.User,
		"session":           a.Session,
		"mfaSetupRequired":  s.auth.MFASetupRequired(a.User, a.Session),
		"recoveryCodesLeft": left,
	})
}

// handleUpdateMe changes the caller's own profile. The request type has no
// other field on purpose: role, disabled, email and the like are ignored, and
// the target is always the authenticated user, never an id from the request.
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		DisplayName *string `json:"displayName"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.DisplayName == nil {
		writeError(w, http.StatusBadRequest, "invalid_input", "displayName is required")
		return
	}
	u, err := s.auth.UpdateOwnProfile(r.Context(), a.User.ID, *req.DisplayName)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
		TOTPCode        string `json:"totpCode"`     // first password of an account with TOTP
		RecoveryCode    string `json:"recoveryCode"` // alternative to totpCode
	}
	if !readJSON(w, r, &req) {
		return
	}
	if a.User.HasPassword && req.CurrentPassword == "" {
		s.writeAuthError(w, fmt.Errorf("%w: enter your current password", auth.ErrReauthRequired))
		return
	}
	if !s.limitLogin(w, r, a.User.Username) {
		return
	}
	var err error
	if a.User.HasPassword {
		err = s.auth.ChangePassword(r.Context(), a.User.ID, req.CurrentPassword, req.NewPassword, a.Session.ID)
	} else {
		// OIDC-only account: setting a first local password re-authenticates (NA-02).
		err = s.auth.SetFirstPassword(r.Context(), a.User.ID, req.NewPassword, a.Session.ID, req.TOTPCode, req.RecoveryCode)
	}
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.streams.revokeUser(a.User.ID, a.Session.ID) // other sessions ended with the password change
	s.apps.lockPGP(a.User.ID)
	s.rotateAfterStepUp(w, r, a)
	w.WriteHeader(http.StatusNoContent)
}

type sessionView struct {
	*auth.Session
	Current bool `json:"current"`
}

func sessionViews(list []*auth.Session, current int64) []sessionView {
	out := make([]sessionView, 0, len(list))
	for _, x := range list {
		out = append(out, sessionView{Session: x, Current: x.ID == current})
	}
	return out
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request, a *authInfo) {
	list, err := s.auth.ListSessions(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessionViews(list, a.Session.ID)})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request, a *authInfo) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	// The own session is a sign-out; ending another one needs proof, or a stolen
	// cookie could throw out its owner.
	if id != a.Session.ID {
		var req reauthReq
		if !readJSON(w, r, &req) || !s.reauth(w, r, a, req.Password, req.Code, req.RecoveryCode) {
			return
		}
	}
	if err := s.auth.RevokeSession(r.Context(), a.User.ID, id); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.streams.revokeSession(a.User.ID, id)
	s.apps.lockPGP(a.User.ID)
	if id == a.Session.ID && !a.Bearer {
		s.setCookie(w, sessionCookie, "", -1, true, http.SameSiteLaxMode)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeOthers(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req reauthReq
	if !readJSON(w, r, &req) || !s.reauth(w, r, a, req.Password, req.Code, req.RecoveryCode) {
		return
	}
	if err := s.auth.RevokeAllSessions(r.Context(), a.User.ID, a.Session.ID); err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.streams.revokeUser(a.User.ID, a.Session.ID)
	s.apps.lockPGP(a.User.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req reauthReq
	if !readJSON(w, r, &req) || !s.reauth(w, r, a, req.Password, req.Code, req.RecoveryCode) {
		return
	}
	enr, err := s.auth.BeginTOTP(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, enr)
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	codes, err := s.auth.ConfirmTOTP(r.Context(), a.User.ID, req.Code)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// reauth is the step-up before a sensitive change (Basis spec/AUTH.md §3): the
// password if the user has one and, with TOTP, a second factor; an account with
// neither needs a recent login from its identity provider or passkey. Wrong answers
// count persistently per user (NA-05). Device tokens never get here: they have
// nothing to prove themselves with. Success gives the session a new id.
func (s *Server) reauth(w http.ResponseWriter, r *http.Request, a *authInfo, password, code, recovery string) bool {
	if a.Session.Kind == auth.KindDevice {
		refuseDevice(w)
		return false
	}
	if s.stepUpEmpty(w, a, password, code, recovery) || !s.limitLogin(w, r, a.User.Username) {
		return false
	}
	if err := s.auth.VerifyStepUp(r.Context(), a.User.ID, a.Session.ID, password, code, recovery); err != nil {
		s.writeAuthError(w, err)
		return false
	}
	s.rotateAfterStepUp(w, r, a)
	return true
}

// stepUpEmpty answers a request without any proof (reauth_required) when the
// account has one to give. Such a request counts for neither the step-up lock
// nor the login rate limit: it is a form sent too early, not a guess.
func (s *Server) stepUpEmpty(w http.ResponseWriter, a *authInfo, password, code, recovery string) bool {
	if !auth.StepUpInputMissing(a.User, password, code, recovery) {
		return false
	}
	s.writeAuthError(w, fmt.Errorf("%w: enter your password", auth.ErrReauthRequired))
	return true
}

// rotateAfterStepUp gives the session a new id and the browser the new cookies. The
// request goes on; a failure here must not undo a proof that succeeded.
func (s *Server) rotateAfterStepUp(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if a.Bearer || a.Session.Kind != auth.KindWeb {
		return
	}
	next, err := s.auth.RotateWebSession(r.Context(), a.Session, a.Token)
	if err != nil {
		s.log.Warn("session rotation", "request_id", requestID(r), "err", err)
		return
	}
	if next == "" {
		return
	}
	a.Token = next
	s.setCookie(w, sessionCookie, next, s.cfg.Auth.EffectiveSessionLifetime(), true, http.SameSiteLaxMode)
	s.setCookie(w, csrfCookie, csrfTokenFor(next), 0, false, http.SameSiteStrictMode)
}

type reauthReq struct {
	Password     string `json:"password"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recoveryCode"`
}

func (s *Server) handleTOTPDisable(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req reauthReq
	if !readJSON(w, r, &req) || !s.reauth(w, r, a, req.Password, req.Code, req.RecoveryCode) {
		return
	}
	if err := s.auth.ResetTOTP(r.Context(), a.User.ID); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRecoveryCodes(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var req reauthReq
	if !readJSON(w, r, &req) {
		return
	}
	if !a.User.TOTPEnabled {
		writeError(w, http.StatusConflict, "totp_not_enabled", "two-factor authentication is not enabled")
		return
	}
	if !s.reauth(w, r, a, req.Password, req.Code, req.RecoveryCode) {
		return
	}
	codes, err := s.auth.RegenerateRecoveryCodes(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

func (s *Server) handleInviteInfo(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Auth.Registration != "invite" {
		s.writeAuthError(w, auth.ErrRegistration)
		return
	}
	if !s.limitLogin(w, r, "invite") {
		return
	}
	inv, err := s.auth.LookupInvite(r.Context(), r.PathValue("token"))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": inv.Email, "role": inv.Role, "expiresAt": inv.ExpiresAt})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InviteToken string `json:"inviteToken"`
		Username    string `json:"username"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Password    string `json:"password"`
	}
	if !readJSON(w, r, &req) || !s.limitLogin(w, r, "invite") {
		return
	}
	ctx := auth.WithActor(r.Context(), auth.Actor{Name: req.Username, IP: clientFrom(r).IP})
	u, err := s.auth.Register(ctx, req.InviteToken, auth.NewUser{
		Username: req.Username, Email: req.Email, DisplayName: req.DisplayName, Password: req.Password,
	})
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	res, err := s.auth.Login(r.Context(), auth.LoginRequest{Login: u.Username, Password: req.Password, Client: s.clientInfoFor(r, "")})
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	s.startSession(w, res)
	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "user": res.User})
}

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if !s.limitLogin(w, r, "oidc") {
		return
	}
	u, err := s.auth.OIDCAuthURL(r.Context(), r.URL.Query().Get("return"))
	if err != nil {
		if errors.Is(err, auth.ErrOIDCDisabled) {
			s.writeAuthError(w, err)
			return
		}
		s.log.Error("oidc login", "err", err)
		http.Redirect(w, r, "/login?error=oidc_unavailable", http.StatusFound)
		return
	}
	// Bind the flow to this browser: the callback must present the same state.
	pu, _ := url.Parse(u)
	s.setCookie(w, oidcCookie, pu.Query().Get("state"), 10*time.Minute, true, http.SameSiteLaxMode)
	http.Redirect(w, r, u, http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	s.setCookie(w, oidcCookie, "", -1, true, http.SameSiteLaxMode)
	if q.Get("error") != "" {
		http.Redirect(w, r, "/login?error=oidc_"+url.QueryEscape(q.Get("error")), http.StatusFound)
		return
	}
	if state == "" || s.cookie(r, oidcCookie) != state {
		http.Redirect(w, r, "/login?error=oidc_state", http.StatusFound)
		return
	}
	res, ret, err := s.auth.OIDCCallback(r.Context(), state, q.Get("code"), s.clientInfoFor(r, ""))
	if err != nil {
		code := "oidc_failed"
		switch {
		case errors.Is(err, auth.ErrOIDCDenied):
			code = "oidc_denied"
		case errors.Is(err, auth.ErrUserDisabled):
			code = "user_disabled"
		default:
			s.log.Warn("oidc callback failed", "err", err)
		}
		http.Redirect(w, r, "/login?error="+code, http.StatusFound)
		return
	}
	s.startSession(w, res)
	http.Redirect(w, r, ret, http.StatusFound)
}

func (s *Server) handleListSettings(w http.ResponseWriter, r *http.Request, a *authInfo) {
	all, err := s.auth.Settings(r.Context(), a.User.ID)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, all)
}

func (s *Server) handleGetSetting(w http.ResponseWriter, r *http.Request, a *authInfo) {
	v, err := s.auth.GetSetting(r.Context(), a.User.ID, r.PathValue("key"))
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(v)
}

func (s *Server) handlePutSetting(w http.ResponseWriter, r *http.Request, a *authInfo) {
	var v json.RawMessage
	if !readJSON(w, r, &v) {
		return
	}
	if err := s.auth.SetSetting(r.Context(), a.User.ID, r.PathValue("key"), v); err != nil {
		s.writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
