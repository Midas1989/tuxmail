// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

// Regression tests of security audit A (docs/security/AUDIT-A-server.md).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/config"
)

// A-01: user ids are never reused, also not when the data directory of a
// deleted user was kept.
func TestAuditA01UserIDsNotReused(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	mustUser(t, s, "admin", RoleAdmin, goodPW)
	a := mustUser(t, s, "anna", RoleUser, goodPW)
	if err := s.DeleteUser(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	b := mustUser(t, s, "ben", RoleUser, goodPW)
	if b.ID == a.ID {
		t.Fatalf("id %d reused after delete", a.ID)
	}
	if err := s.DeleteUser(ctx, b.ID, false); err != nil { // data kept
		t.Fatal(err)
	}
	// Even if the high-water mark were lost, an existing directory is skipped.
	if _, err := s.db.Exec(`DELETE FROM id_highwater`); err != nil {
		t.Fatal(err)
	}
	c := mustUser(t, s, "carl", RoleUser, goodPW)
	if c.ID == b.ID {
		t.Fatalf("new user inherits the kept data directory of id %d", b.ID)
	}
	if _, err := os.Stat(filepath.Join(s.UserDir(b.ID))); err != nil {
		t.Fatal("kept data was removed", err)
	}
}

// A-05: a self-chosen (unverified) address cannot capture the first OIDC
// login of the address owner (account pre-hijacking).
func TestAuditA05OIDCPreHijack(t *testing.T) {
	ctx := context.Background()
	idp := newFakeIdP(t)
	secretFile := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secretFile, []byte("s3cret\n"), 0o600)
	s, _ := newTestService(t, func(c *config.Config) {
		c.Auth.Registration = "invite"
		c.Auth.OIDC = config.OIDC{Enabled: true, Issuer: idp.srv.URL, ClientID: "tuxmail", ClientSecretFile: secretFile, GroupsClaim: "groups"}
	})
	s.Now = time.Now
	// The attacker registers with an open invite and the victim's address.
	_, tok, err := s.CreateInvite(ctx, "", RoleUser, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := s.Register(ctx, tok, NewUser{Username: "mallory", Email: "ceo@example.de", Password: goodPW})
	if err != nil {
		t.Fatal(err)
	}
	login := func(claims map[string]any) *LoginResult {
		au, err := s.OIDCAuthURL(ctx, "/")
		if err != nil {
			t.Fatal(err)
		}
		state, code := idp.authorize(t, au, claims)
		res, _, err := s.OIDCCallback(ctx, state, code, ClientInfo{})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := login(map[string]any{"sub": "ceo-sub", "preferred_username": "ceo", "email": "ceo@example.de", "email_verified": true})
	if res.User.ID == attacker.ID {
		t.Fatal("OIDC login of the address owner was linked to the attacker's pre-registered account")
	}
	// Admin-set addresses are still linked.
	kim := mustUser(t, s, "kim", RoleUser, goodPW)
	if res := login(map[string]any{"sub": "kim-sub", "email": "kim@example.de", "email_verified": true}); res.User.ID != kim.ID {
		t.Fatal("admin-created account not linked")
	}
	// Unverified claims do not change the stored address.
	res2 := login(map[string]any{"sub": "kim-sub", "email": "other@example.de", "email_verified": false})
	if res2.User.Email != "kim@example.de" {
		t.Fatalf("unverified OIDC address stored: %s", res2.User.Email)
	}
}

// A-07: logins by username and by e-mail address share one lockout counter.
func TestAuditA07ThrottleAliases(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService(t)
	mustUser(t, s, "anna", RoleUser, goodPW)
	for i := range throttleFreeFails {
		login := "anna@example.de"
		if i%2 == 1 {
			login = " ANNA "
		}
		s.Login(ctx, LoginRequest{Login: login, Password: "wrong password!"})
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "anna", Password: goodPW}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("alternating username/e-mail avoids the lockout: %v", err)
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "anna@example.de", Password: goodPW}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("e-mail alias not locked: %v", err)
	}
}

// A-08: concurrent second-factor guesses on one challenge are limited.
func TestAuditA08MFAChallengeAttemptsAtomic(t *testing.T) {
	ctx := context.Background()
	s, clk := newTestService(t)
	u := mustUser(t, s, "dave", RoleUser, goodPW)
	enr, _ := s.BeginTOTP(ctx, u.ID)
	secret, _ := b32.DecodeString(enr.Secret)
	if _, err := s.ConfirmTOTP(ctx, u.ID, TOTP{Secret: secret}.Code(clk.Now())); err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Minute)
	res, err := s.Login(ctx, LoginRequest{Login: "dave", Password: goodPW})
	if err != nil || !res.MFARequired {
		t.Fatal(res, err)
	}
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.CompleteMFA(ctx, res.Challenge, "000000", "", "", ClientInfo{})
		}()
	}
	wg.Wait()
	var attempts int
	if err := s.db.QueryRow(`SELECT attempts FROM mfa_challenges WHERE token_hash = ?`, hashToken(res.Challenge)).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts > mfaChallengeMaxTry {
		t.Fatalf("%d guesses on one challenge (max %d)", attempts, mfaChallengeMaxTry)
	}
	if _, err := s.CompleteMFA(ctx, res.Challenge, TOTP{Secret: secret}.Code(clk.Now()), "", "", ClientInfo{}); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("exhausted challenge still accepted: %v", err)
	}
}

func TestSafeReturnPathRejectsControlCharTricks(t *testing.T) {
	bad := []string{"/\t/evil.example", "/\n/evil.example", "/ /evil.example", "/\\evil.example", "//evil.example", "/%09/x\t", "https://evil.example", "/ /evil", "/\x7f/evil", "/%2F%2Fevil"}
	for _, p := range bad {
		if got := SafeReturnPath(p); got != "/" {
			t.Errorf("SafeReturnPath(%q) = %q, want /", p, got)
		}
	}
	for _, p := range []string{"/", "/mail", "/settings/accounts?x=1#a"} {
		if got := SafeReturnPath(p); got != p {
			t.Errorf("SafeReturnPath(%q) = %q, want unchanged", p, got)
		}
	}
}

// E-03: a stranger failing logins from one network must not lock the user
// out of a different network.
func TestLoginLockoutIsPerNetwork(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	mustUser(t, s, "victim", RoleUser, "correct horse battery staple")
	for i := 0; i < throttleFreeFails+2; i++ {
		s.Login(ctx, LoginRequest{Login: "victim", Password: "wrong", Client: ClientInfo{IP: "203.0.113.7"}})
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "victim", Password: "correct horse battery staple", Client: ClientInfo{IP: "203.0.113.7"}}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("attacker network not throttled: %v", err)
	}
	if _, err := s.Login(ctx, LoginRequest{Login: "victim", Password: "correct horse battery staple", Client: ClientInfo{IP: "198.51.100.20"}}); err != nil {
		t.Fatalf("victim locked out from another network: %v", err)
	}
	s.UnlockLogin(ctx, "victim")
}
