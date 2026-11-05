// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/config"
)

// oidcEnv is a service with a fake identity provider and an offset clock:
// the OIDC library validates tokens against real time, the service clock can
// be shifted afterwards with offset.
type oidcEnv struct {
	s      *Service
	idp    *fakeIdP
	offset time.Duration
}

func newOIDCEnv(t *testing.T, mod ...func(*config.Config)) *oidcEnv {
	t.Helper()
	idp := newFakeIdP(t)
	secretFile := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secretFile, []byte("s3cret\n"), 0o600)
	s, _ := newTestService(t, append([]func(*config.Config){func(c *config.Config) {
		c.Auth.OIDC = config.OIDC{Enabled: true, Issuer: idp.srv.URL, ClientID: "tuxmail", ClientSecretFile: secretFile, GroupsClaim: "groups"}
	}}, mod...)...)
	e := &oidcEnv{s: s, idp: idp}
	s.Now = func() time.Time { return time.Now().Add(e.offset) }
	return e
}

func (e *oidcEnv) login(t *testing.T, claims map[string]any) *LoginResult {
	t.Helper()
	ctx := context.Background()
	au, err := e.s.OIDCAuthURL(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	state, code := e.idp.authorize(t, au, claims)
	res, _, err := e.s.OIDCCallback(ctx, state, code, ClientInfo{IP: "192.0.2.50"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// NA-02: an OIDC-linked admin who logs in with the local password does not
// get the OIDC exemption from totp_required_for_admins; the session created by
// the OIDC callback does.
func TestReauditNA02AdminMFAPolicyIsPerSession(t *testing.T) {
	ctx := context.Background()
	e := newOIDCEnv(t, func(c *config.Config) { c.Auth.TOTPRequiredForAdmins = true })
	s := e.s
	root := mustUser(t, s, "root", RoleAdmin, goodPW)

	if !s.MFASetupRequired(root, nil) {
		t.Fatal("plain admin without TOTP is not required to enrol")
	}
	// Linking via a verified e-mail address (A-05 path).
	oidcRes := e.login(t, map[string]any{"sub": "root-sub", "preferred_username": "root", "email": "root@example.de", "email_verified": true})
	if oidcRes.User.ID != root.ID || !oidcRes.User.OIDC {
		t.Fatalf("account not linked: %+v", oidcRes.User)
	}
	if oidcRes.Session.LoginMethod != MethodOIDC {
		t.Fatalf("OIDC session recorded as %q", oidcRes.Session.LoginMethod)
	}
	if s.MFASetupRequired(oidcRes.User, oidcRes.Session) {
		t.Fatal("OIDC session of a linked admin must stay exempt (the IdP is the second factor)")
	}
	pwRes, err := s.Login(ctx, LoginRequest{Login: "root", Password: goodPW, Client: ClientInfo{IP: "192.0.2.51"}})
	if err != nil {
		t.Fatal(err)
	}
	if pwRes.Session.LoginMethod != MethodPassword {
		t.Fatalf("password session recorded as %q", pwRes.Session.LoginMethod)
	}
	if !s.MFASetupRequired(pwRes.User, pwRes.Session) {
		t.Fatal("NA-02: password login of an OIDC-linked admin bypasses totp_required_for_admins")
	}
	// The stored method survives Authenticate (what the admin middleware sees).
	sess, u, err := s.Authenticate(ctx, pwRes.Token, "")
	if err != nil || !s.MFASetupRequired(u, sess) {
		t.Fatalf("authenticated password session exempt: %v", err)
	}
	sess, u, err = s.Authenticate(ctx, oidcRes.Token, "")
	if err != nil || s.MFASetupRequired(u, sess) {
		t.Fatalf("authenticated OIDC session not exempt: %v", err)
	}
	// With TOTP enrolled nobody is required to enrol.
	tp := enableTOTPLive(t, s, root.ID)
	_ = tp
	u, _ = s.GetUser(ctx, root.ID)
	if s.MFASetupRequired(u, pwRes.Session) {
		t.Fatal("admin with TOTP still required to enrol")
	}
}

// enableTOTPLive enrols TOTP on a service whose clock follows real time.
func enableTOTPLive(t *testing.T, s *Service, id int64) TOTP {
	t.Helper()
	enr, err := s.BeginTOTP(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := b32.DecodeString(enr.Secret)
	tp := TOTP{Secret: secret}
	// Enrol one minute in the past so that the code of "now" is a fresh step.
	old := s.Now
	s.Now = func() time.Time { return old().Add(-time.Minute) }
	defer func() { s.Now = old }()
	if _, err := s.ConfirmTOTP(context.Background(), id, tp.Code(s.Now())); err != nil {
		t.Fatal(err)
	}
	return tp
}

// NA-02: an OIDC-only account may not give itself a local password without
// re-authenticating, and the attempt is audited.
func TestReauditNA02FirstPasswordNeedsReauth(t *testing.T) {
	ctx := context.Background()
	e := newOIDCEnv(t)
	s := e.s
	mustUser(t, s, "root", RoleAdmin, goodPW)
	res := e.login(t, map[string]any{"sub": "sso-sub", "preferred_username": "sso", "email": "sso@idp.example", "email_verified": true})
	u := res.User
	if u.HasPassword || !u.OIDC {
		t.Fatalf("expected an OIDC-only user: %+v", u)
	}
	const newPW = "a-brand-new-password-123"

	// The old backdoor: ChangePassword with an empty current password.
	if err := s.ChangePassword(ctx, u.ID, "", newPW, res.Session.ID); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("empty-current-password change accepted: %v", err)
	}
	// A session that was not created by OIDC (e.g. a device token) is refused.
	if err := s.SetFirstPassword(ctx, u.ID, newPW, 99999, "", ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("foreign session accepted: %v", err)
	}
	// A stale OIDC session is refused ...
	e.offset = FirstPasswordReauthWindow + time.Minute
	if err := s.SetFirstPassword(ctx, u.ID, newPW, res.Session.ID, "", ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("stale OIDC session accepted: %v", err)
	}
	if got, _ := s.GetUser(ctx, u.ID); got.HasPassword {
		t.Fatal("password was set despite refusal")
	}
	// ... a fresh one (the user just came back from the IdP) is accepted.
	e.offset = 0
	other := e.login(t, map[string]any{"sub": "sso-sub", "preferred_username": "sso", "email": "sso@idp.example", "email_verified": true})
	e.offset = time.Minute
	if err := s.SetFirstPassword(ctx, u.ID, newPW, other.Session.ID, "", ""); err != nil {
		t.Fatalf("fresh OIDC session refused: %v", err)
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "sso", Password: newPW}); err != nil {
		t.Fatal(err)
	}
	// Both outcomes are audited.
	audit, _ := s.ListAudit(ctx, 100, 0)
	var denied, allowed int
	for _, a := range audit {
		if a.Action == "user.set_first_password" {
			if a.Success {
				allowed++
			} else {
				denied++
			}
		}
	}
	if denied < 2 || allowed != 1 {
		t.Fatalf("audit: denied=%d allowed=%d", denied, allowed)
	}
}

// NA-02: with TOTP enabled the second factor is the re-authentication, even
// in a fresh OIDC session; wrong codes are counted by the step-up lockout.
func TestReauditNA02FirstPasswordWithTOTP(t *testing.T) {
	ctx := context.Background()
	e := newOIDCEnv(t)
	s := e.s
	mustUser(t, s, "root", RoleAdmin, goodPW)
	res := e.login(t, map[string]any{"sub": "sso-sub2", "preferred_username": "sso2", "email": "sso2@idp.example", "email_verified": true})
	tp := enableTOTPLive(t, s, res.User.ID)
	const newPW = "a-brand-new-password-123"
	if err := s.SetFirstPassword(ctx, res.User.ID, newPW, res.Session.ID, "", ""); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("fresh session alone accepted although TOTP is enabled: %v", err)
	}
	if err := s.SetFirstPassword(ctx, res.User.ID, newPW, res.Session.ID, "000000", ""); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if err := s.SetFirstPassword(ctx, res.User.ID, newPW, res.Session.ID, tp.Code(s.Now()), ""); err != nil {
		t.Fatalf("correct code refused: %v", err)
	}
}
