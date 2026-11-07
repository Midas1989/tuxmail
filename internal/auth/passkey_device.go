// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkeys for connecting apps (KONZEPT 15.8/15.9):
//
//   - Device approval in the browser (RFC 8628 verification page, /device): the
//     approval is a long-lived way into the account and needs a step-up given
//     AFTER the request was shown. Besides password (+ code) it accepts a passkey
//     assertion of the signed-in user whose ceremony is bound to the purpose
//     "device-approve", to that user and to the user code of the request shown
//     (Basis spec/AUTH.md §3: step-up by password, passkey or TOTP). A recent
//     passkey sign-in alone is deliberately not enough: with a phishing link
//     (/device?user_code=<attacker's code>) the victim would sign in for
//     themselves and then approve with one click.
//   - Direct passkey sign-in of the Android app (Credential Manager, no browser):
//     a discoverable login whose ceremony is bound to the purpose "device-login"
//     and that accepts ONLY the android:apk-key-hash origins of
//     auth.passkeys.android_apps (without a configured app it does not exist).
//     It issues a device token like /api/device/login, counted as two factors
//     like the web passkey login. The web passkey login never accepts an app
//     origin, and a ceremony of one purpose is useless for the other.
//
// Everything else is the web passkey login of passkeys.go: single-use 5-minute
// challenges, user verification required, counter compare-and-swap, network
// throttle "p:<network>", one generic answer for every failure.
const (
	purposeDeviceLogin   = "device-login"
	purposeDeviceApprove = "device-approve"
)

// webAuthnApps is the relying party of the app sign-in: the configured Android
// app origins (the library also needs the web origin in its list; the app
// endpoint filters it out, see appOrigin). Built per ceremony: it is cheap and
// follows a changed configuration.
func (s *Service) webAuthnApps() (*webauthn.WebAuthn, string, error) {
	if !s.cfg.PasskeysEnabled() {
		return nil, "", ErrPasskeysDisabled
	}
	opaque := s.cfg.PasskeyAppOrigins()
	if len(opaque) == 0 {
		return nil, "", fmt.Errorf("%w: no app may sign in with a passkey (auth.passkeys.android_apps)", ErrNotFound)
	}
	rpID, origin, _ := s.cfg.PasskeyRP()
	wa, err := webauthn.New(s.webAuthnConfig(rpID, origin, opaque))
	if err != nil {
		return nil, "", fmt.Errorf("auth: passkeys for apps: %w", err)
	}
	return wa, rpID, nil
}

// PasskeyAppLogin reports whether apps can sign in with a passkey directly:
// passkeys are on and at least one Android app is configured.
func (s *Service) PasskeyAppLogin() bool {
	return s.cfg.PasskeysEnabled() && len(s.cfg.PasskeyAppOrigins()) > 0
}

// BeginPasskeyDeviceLogin starts the direct passkey sign-in of an app (the
// options are for Credential Manager / navigator.credentials.get).
func (s *Service) BeginPasskeyDeviceLogin(ctx context.Context, ci ClientInfo) (string, *protocol.CredentialAssertion, error) {
	wa, _, err := s.webAuthnApps()
	if err != nil {
		return "", nil, err
	}
	return s.beginDiscoverableLogin(ctx, wa, purposeDeviceLogin, ci)
}

// FinishPasskeyDeviceLogin verifies the app's assertion and issues a device
// token (ci carries device name, client key and scopes). Every failure is
// ErrInvalidCredentials or a *ThrottleError, like the web passkey login.
func (s *Service) FinishPasskeyDeviceLogin(ctx context.Context, challengeID string, credentialJSON []byte, ci ClientInfo) (*LoginResult, error) {
	wa, rpID, err := s.webAuthnApps()
	if err != nil {
		return nil, err
	}
	u, err := s.finishDiscoverableLogin(ctx, wa, rpID, purposeDeviceLogin, challengeID, credentialJSON, ci, s.appOrigin)
	if err != nil {
		return nil, err
	}
	ctx = WithActor(ctx, Actor{UserID: u.ID, Name: u.Username, IP: ci.IP})
	return s.finishLogin(ctx, u, KindDevice, MethodPasskey, true, ci)
}

// appOrigin accepts the configured app origins only.
func (s *Service) appOrigin(o string) bool {
	for _, a := range s.cfg.PasskeyAppOrigins() {
		if o == a {
			return true
		}
	}
	return false
}

// usablePasskeys are the credentials of userID for the current RP ID.
func (s *Service) usablePasskeys(ctx context.Context, userID int64, rpID string) ([]*passkeyRow, error) {
	rows, err := s.userPasskeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := rows[:0]
	for _, r := range rows {
		if r.rpID == rpID {
			out = append(out, r)
		}
	}
	return out, nil
}

// HasUsablePasskey reports whether userID can confirm with a passkey here.
func (s *Service) HasUsablePasskey(ctx context.Context, userID int64) bool {
	if !s.cfg.PasskeysEnabled() {
		return false
	}
	rpID, _, _ := s.cfg.PasskeyRP()
	rows, err := s.usablePasskeys(ctx, userID, rpID)
	return err == nil && len(rows) > 0
}

// BeginPasskeyDeviceApproval starts the passkey confirmation of the pending
// device request userCode by the signed-in userID. The ceremony is bound to
// both; the options list the user's own credentials only. A new begin replaces
// a pending one of the same user.
func (s *Service) BeginPasskeyDeviceApproval(ctx context.Context, userID int64, userCode string) (string, *protocol.CredentialAssertion, error) {
	wa, rpID, err := s.webAuthn()
	if err != nil {
		return "", nil, err
	}
	if _, err := s.LookupDeviceRequest(ctx, userCode); err != nil {
		return "", nil, err
	}
	if err := s.StepUpCheck(ctx, userID); err != nil {
		return "", nil, err
	}
	wu, _, err := s.approvalUser(ctx, userID, rpID)
	if err != nil {
		return "", nil, err
	}
	assertion, sd, err := wa.BeginLogin(wu, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return "", nil, fmt.Errorf("auth: begin passkey confirmation: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM passkey_challenges WHERE kind = 'login' AND user_id = ?`, userID); err != nil {
		return "", nil, err
	}
	id, err := s.putBoundChallenge(ctx, "login", purposeDeviceApprove, NormalizeUserCode(userCode), userID, sd)
	if err != nil {
		return "", nil, err
	}
	return id, assertion, nil
}

func (s *Service) approvalUser(ctx context.Context, userID int64, rpID string) (*webauthnUser, []*passkeyRow, error) {
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.usablePasskeys(ctx, userID, rpID)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, fmt.Errorf("%w: this account has no passkey for this server", ErrNotFound)
	}
	handle, err := s.passkeyHandle(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	wu := &webauthnUser{handle: handle, name: u.Username, displayName: u.DisplayName}
	for _, r := range rows {
		wu.creds = append(wu.creds, r.credential())
	}
	return wu, rows, nil
}

// VerifyPasskeyDeviceApproval checks the passkey confirmation of the device
// request userCode by userID. It must be an assertion of one of userID's own
// passkeys with user verification, for the ceremony started by
// BeginPasskeyDeviceApproval for the same user and code. It runs under the
// step-up lock of the account (stepup.go) and counts a refused assertion
// there. Failures are ErrPasskeyInvalid (the reason is in the audit log) or a
// *ThrottleError while the lock holds.
func (s *Service) VerifyPasskeyDeviceApproval(ctx context.Context, userID int64, userCode, challengeID string, credentialJSON []byte) error {
	wa, rpID, err := s.webAuthn()
	if err != nil {
		return err
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.StepUpCheck(ctx, userID); err != nil {
		return err
	}
	fail := func(reason string) error {
		s.Audit(ctx, "device.passkey", u.Username, false, reason)
		s.stepUpFailed(ctx, userID, "passkey: "+reason)
		return ErrPasskeyInvalid
	}
	sd, err := s.takeBoundChallenge(ctx, challengeID, "login", purposeDeviceApprove, NormalizeUserCode(userCode), userID)
	if err != nil {
		if errors.Is(err, ErrPasskeyChallenge) {
			// Not a wrong answer (an old or foreign form): not counted.
			s.Audit(ctx, "device.passkey", u.Username, false, "unknown, expired, used or foreign ceremony")
			return ErrPasskeyInvalid
		}
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(credentialJSON)
	if err != nil {
		return fail("malformed response: " + protocolReason(err))
	}
	wu, rows, err := s.approvalUser(ctx, userID, rpID)
	if err != nil {
		return fail("no passkey")
	}
	var row *passkeyRow
	for _, r := range rows {
		if bytes.Equal(r.credentialID, parsed.RawID) {
			row = r
		}
	}
	if row == nil {
		return fail("passkey of another account or unknown")
	}
	cred, err := wa.ValidateLogin(wu, *sd, parsed)
	if err != nil {
		return fail(protocolReason(err))
	}
	if !cred.Flags.UserVerified {
		return fail("user verification missing")
	}
	if err := s.acceptAssertion(ctx, u, row, cred); err != nil {
		return fail(err.Error())
	}
	s.ClearStepUp(ctx, userID)
	s.Audit(ctx, "device.passkey", u.Username, true, fmt.Sprintf("confirmed with passkey %q user_code=%s", row.name, NormalizeUserCode(userCode)))
	return nil
}
