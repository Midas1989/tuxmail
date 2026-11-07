// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Device approval by a new sign-in at the identity provider, for accounts
// without password and passkey (passkey_device.go explains why a recent sign-in
// alone never approves a device). The approval page starts it after the
// request was shown (POST, CSRF-protected): the flow asks the IdP for a fresh
// authentication (prompt=login, max_age=0) and its state is bound to the user,
// the user code and the start time. The callback approves only if the ID token
// names the same account and its auth_time is not older than the start. No
// session is created or changed.
//
// The binding lives in oidc_states.return_to (no schema change): a value with
// the prefix below, which SafeReturnPath never produces for a normal login
// (those always start with "/").
const oidcApprovePrefix = "device-approve:"

// DeviceApprovedPath is where the browser goes after a device approval at the
// identity provider (OIDCCallback returns it without a LoginResult).
const DeviceApprovedPath = "/device?approved=1"

// oidcAuthTimeSkew tolerates clocks of IdP and server that differ a little.
const oidcAuthTimeSkew = 30

// ErrOIDCApproval is a failed device approval at the identity provider (the
// reason is in the audit log).
var ErrOIDCApproval = errors.New("auth: the sign-in at the identity provider did not confirm the device")

// OIDCDeviceApprovalURL starts the confirmation of the pending device request
// userCode by userID at the identity provider and returns the URL to send the
// browser to.
func (s *Service) OIDCDeviceApprovalURL(ctx context.Context, userID int64, userCode string) (string, error) {
	c, err := s.oidcClient(ctx)
	if err != nil {
		return "", err
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	if !u.OIDC {
		return "", fmt.Errorf("%w: this account does not sign in at the identity provider", ErrInvalidInput)
	}
	if err := s.StepUpCheck(ctx, userID); err != nil {
		return "", err
	}
	if _, err := s.LookupDeviceRequest(ctx, userCode); err != nil {
		return "", err
	}
	state, nonce, verifier := randomToken(24), randomToken(24), oauth2.GenerateVerifier()
	bind := oidcApprovePrefix + strconv.FormatInt(userID, 10) + ":" + NormalizeUserCode(userCode) + ":" + strconv.FormatInt(s.now().Unix(), 10)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO oidc_states(state_hash, nonce, verifier, return_to, expires_at) VALUES (?,?,?,?,?)`,
		hashToken(state), nonce, verifier, bind, s.now().Add(oidcStateTTL).Unix()); err != nil {
		return "", err
	}
	return c.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0")), nil
}

// finishOIDCDeviceApproval is the callback part of OIDCDeviceApprovalURL; the
// ID token was verified (signature, issuer, audience, expiry, nonce).
func (s *Service) finishOIDCDeviceApproval(ctx context.Context, issuer, bind string, cl oidcClaims, all map[string]any) error {
	parts := strings.Split(strings.TrimPrefix(bind, oidcApprovePrefix), ":")
	if len(parts) != 3 {
		return ErrOIDCApproval
	}
	userID, err1 := strconv.ParseInt(parts[0], 10, 64)
	started, err2 := strconv.ParseInt(parts[2], 10, 64)
	userCode := parts[1]
	if err1 != nil || err2 != nil {
		return ErrOIDCApproval
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return ErrOIDCApproval
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username})
	fail := func(reason string) error {
		s.Audit(ctx, "device.oidc", u.Username, false, reason)
		s.stepUpFailed(ctx, userID, "identity provider: "+reason)
		return ErrOIDCApproval
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE oidc_issuer = ? AND oidc_subject = ?`, issuer, cl.Subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && id != userID) {
		return fail("signed in as another account")
	}
	if err != nil {
		return err
	}
	at, ok := all["auth_time"].(float64)
	if !ok || at != math.Trunc(at) {
		return fail("no auth_time in the ID token")
	}
	if int64(at) < started-oidcAuthTimeSkew {
		return fail("the identity provider did not ask again (auth_time before the request)")
	}
	if u.Disabled {
		return fail("disabled")
	}
	if err := s.ApproveDevice(ctx, userID, userCode, true); err != nil {
		s.Audit(ctx, "device.oidc", u.Username, false, "request no longer pending")
		return ErrOIDCApproval
	}
	s.ClearStepUp(ctx, userID)
	s.Audit(ctx, "device.oidc", u.Username, true, "user_code="+userCode)
	return nil
}
